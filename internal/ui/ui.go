// Package ui owns the terminal picker state machine and rendering.
package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	spriteapi "github.com/joshuadavidthomas/hrd/internal/sprites"
)

const (
	defaultWidth  = 80
	defaultHeight = 24
)

// Operations is the catalog work needed by the picker. Implementations must
// honor context cancellation for both methods.
type Operations interface {
	List(context.Context) ([]catalog.Destination, error)
	Prepare(context.Context, catalog.Destination) (catalog.PreparedAttach, error)
}
type inventoryOperations interface {
	Inventory(context.Context) catalog.Inventory
}
type spriteOperations interface {
	Create(context.Context, string) spriteapi.CreateResult
	PrepareSprite(context.Context, string) (catalog.PreparedAttach, error)
}
type sshKeyOperations interface {
	SSHAddCommand(context.Context) (*exec.Cmd, error)
}

type phase uint8

const (
	phaseLoading phase = iota + 1
	phaseReady
	phaseRefreshing
	phasePreparing
	phaseAttaching
	phaseInitialLoadFailed
	phaseNamingSprite
	phaseCreatingSprite
	phaseConfirmingHostKey
	phaseStartingSSHAgent
	phaseAddingSSHKey
)

type listDone struct {
	opID         uint64
	destinations []catalog.Destination
	inventory    *catalog.Inventory
	err          error
}

type prepareDone struct {
	opID        uint64
	destination catalog.Destination
	prepared    catalog.PreparedAttach
	err         error
}

type attachDone struct {
	opID uint64
	err  error
}
type createDone struct {
	opID   uint64
	result spriteapi.CreateResult
}
type sshAddReady struct {
	opID    uint64
	command *exec.Cmd
	err     error
}
type sshAddDone struct {
	opID uint64
	err  error
}

type pendingKind uint8

const (
	pendingNone pendingKind = iota
	pendingCreate
	pendingDestination
	pendingCreatedSprite
)

type pendingOperation struct {
	kind        pendingKind
	name        string
	destination catalog.Destination
}

type execProcessFunc func(*exec.Cmd, tea.ExecCallback) tea.Cmd

type keyMap struct {
	attach  key.Binding
	refresh key.Binding
	filter  key.Binding
	help    key.Binding
	quit    key.Binding
	force   key.Binding
	up      key.Binding
	down    key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		attach:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "attach")),
		refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		quit:    key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		force:   key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
		up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	}
}

// Model is the Bubble Tea model for the destination picker.
type Model struct {
	root        context.Context
	operations  Operations
	execProcess execProcessFunc
	keys        keyMap

	phase         phase
	nextOpID      uint64
	operationID   uint64
	operationStop context.CancelFunc
	initialCmd    tea.Cmd

	destinations         []catalog.Destination
	selectedID           catalog.DestinationID
	cursor               int
	attachEnabled        bool
	loadNotice           string
	attachNotice         string
	showHelp             bool
	editingFilter        bool
	quitting             bool
	filter               string
	filterBefore         string
	spriteName           string
	createBlock          string
	prepared             catalog.PreparedAttach
	spriteCreateEnabled  bool
	preparingSprite      bool
	pending              pendingOperation
	sshRecoveryAttempted bool

	width  int
	height int
}

// New creates a picker and schedules its initial local session load.
func New(root context.Context, operations Operations) Model {
	if root == nil {
		panic("ui: nil root context")
	}
	if operations == nil {
		panic("ui: nil operations")
	}

	m := Model{
		root:          root,
		operations:    operations,
		execProcess:   tea.ExecProcess,
		keys:          newKeyMap(),
		phase:         phaseLoading,
		attachEnabled: false,
		width:         defaultWidth,
		height:        defaultHeight,
	}
	m.initialCmd = m.beginList()
	return m
}

// Init starts the initial inventory load.
func (m Model) Init() tea.Cmd { return m.initialCmd }

// Update applies one Bubble Tea message.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
		return m, nil
	case listDone:
		return m.updateListDone(msg)
	case prepareDone:
		return m.updatePrepareDone(msg)
	case attachDone:
		return m.updateAttachDone(msg)
	case createDone:
		return m.updateCreateDone(msg)
	case sshAddReady:
		return m.updateSSHAddReady(msg)
	case sshAddDone:
		return m.updateSSHAddDone(msg)
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	default:
		return m, nil
	}
}

func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.phase == phaseAttaching || m.phase == phaseAddingSSHKey {
		return m, nil
	}
	textEditing := m.editingFilter || m.phase == phaseNamingSprite
	if key.Matches(msg, m.keys.force) || (key.Matches(msg, m.keys.quit) && !textEditing) {
		m.cancelOperation()
		_ = m.prepared.Close()
		m.quitting = true
		return m, tea.Quit
	}

	if m.phase == phaseNamingSprite {
		return m.updateName(msg)
	}
	if m.phase == phaseConfirmingHostKey {
		if msg.String() == "enter" {
			return m, m.beginAttach()
		}
		if msg.String() == "esc" {
			_ = m.prepared.Close()
			m.prepared = catalog.PreparedAttach{}
			m.phase = phaseRefreshing
			return m, m.beginList()
		}
		return m, nil
	}
	if m.phase != phaseReady {
		if m.phase == phaseInitialLoadFailed && key.Matches(msg, m.keys.refresh) {
			m.phase = phaseLoading
			m.loadNotice = ""
			m.initialCmd = nil
			return m, m.beginList()
		}
		return m, nil
	}

	if m.editingFilter {
		return m.updateFilter(msg)
	}

	switch {
	case key.Matches(msg, m.keys.attach):
		if !m.attachEnabled {
			return m, nil
		}
		destination, ok := m.selectedDestination()
		if !ok {
			return m, nil
		}
		m.showHelp = false
		m.loadNotice = ""
		m.attachNotice = ""
		m.selectedID = destination.ID
		return m, m.beginPrepare(destination)
	case msg.String() == "n":
		if _, ok := m.operations.(spriteOperations); ok && m.spriteCreateEnabled && m.createBlock == "" {
			m.spriteName = ""
			m.phase = phaseNamingSprite
			return m, nil
		}
		return m, nil
	case key.Matches(msg, m.keys.refresh):
		m.showHelp = false
		m.loadNotice = ""
		m.phase = phaseRefreshing
		return m, m.beginList()
	case key.Matches(msg, m.keys.filter):
		m.editingFilter = true
		m.filterBefore = m.filter
		m.showHelp = false
		m.cursor = 0
		return m, nil
	case key.Matches(msg, m.keys.help):
		m.showHelp = !m.showHelp
		return m, nil
	case key.Matches(msg, m.keys.up):
		m.cursor = max(0, m.cursor-1)
		return m, nil
	case key.Matches(msg, m.keys.down):
		m.cursor = min(max(0, len(m.visibleDestinations())-1), m.cursor+1)
		return m, nil
	default:
		return m, nil
	}
}

func (m Model) updateName(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.phase = phaseReady
	case "enter":
		if m.spriteName == "" {
			m.attachNotice = "Enter a Sprite name."
			return m, nil
		}
		return m, m.beginCreate(m.spriteName)
	case "backspace", "ctrl+h":
		if m.spriteName != "" {
			_, size := utf8.DecodeLastRuneInString(m.spriteName)
			m.spriteName = m.spriteName[:len(m.spriteName)-size]
		}
	default:
		if text := msg.Key().Text; text != "" {
			m.spriteName += text
		}
	}
	return m, nil
}

func (m Model) updateFilter(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.editingFilter = false
	case "esc":
		m.filter = m.filterBefore
		m.editingFilter = false
	case "backspace", "ctrl+h":
		if m.filter != "" {
			_, size := utf8.DecodeLastRuneInString(m.filter)
			m.filter = m.filter[:len(m.filter)-size]
			m.cursor = 0
		}
	default:
		if text := msg.Key().Text; text != "" {
			m.filter += text
			m.cursor = 0
		}
	}
	return m, nil
}

func (m Model) updateListDone(msg listDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operationID || (m.phase != phaseLoading && m.phase != phaseRefreshing) {
		return m, nil
	}
	wasInitial := m.phase == phaseLoading
	m.cancelOperation()

	if msg.err != nil {
		if wasInitial {
			m.phase = phaseInitialLoadFailed
			m.destinations = nil
			m.attachEnabled = false
			m.loadNotice = msg.err.Error()
			return m, nil
		}

		m.phase = phaseReady
		m.attachEnabled = false
		m.loadNotice = "Refresh failed: " + msg.err.Error()
		return m, nil
	}

	m.phase = phaseReady
	m.attachEnabled = len(msg.destinations) > 0
	m.spriteCreateEnabled = false
	m.loadNotice = ""
	if msg.inventory != nil {
		m.spriteCreateEnabled = !msg.inventory.Sprites.Skipped && msg.inventory.Sprites.Err == nil
		if msg.inventory.Local.Err != nil {
			m.loadNotice = "Local refresh failed: " + msg.inventory.Local.Err.Error()
		}
		if msg.inventory.Sprites.Err != nil {
			m.loadNotice = "Sprite refresh failed: " + msg.inventory.Sprites.Err.Error()
		}
	}
	m.destinations = append([]catalog.Destination(nil), msg.destinations...)
	for _, destination := range m.destinations {
		if endpoint, ok := destination.Endpoint.(catalog.SpriteEndpoint); ok && endpoint.Name == m.createBlock {
			m.createBlock = ""
		}
	}
	m.cursor = 0
	m.filter = ""
	m.editingFilter = false
	return m, nil
}

func (m Model) updatePrepareDone(msg prepareDone) (tea.Model, tea.Cmd) {
	if m.phase != phasePreparing || msg.opID != m.operationID || msg.destination.ID != m.selectedID {
		_ = msg.prepared.Close()
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil {
		_ = msg.prepared.Close()
		if catalog.NeedsSSHKey(msg.err) && !m.sshRecoveryAttempted {
			return m, m.beginSSHKeyRecovery()
		}
		m.attachNotice = prepareFailureNotice(msg.err)
		if catalog.NeedsSSHKey(msg.err) {
			m.attachNotice = "ssh-add finished without loading a key. Add a key, then try again."
		}
		if m.preparingSprite {
			m.phase = phaseRefreshing
			return m, m.beginList()
		}
		m.phase = phaseReady
		m.attachEnabled = true
		return m, nil
	}
	m.prepared = msg.prepared
	if msg.prepared.Fingerprint != "" {
		m.phase = phaseConfirmingHostKey
		return m, nil
	}
	return m, m.beginAttach()
}

func (m Model) updateAttachDone(msg attachDone) (tea.Model, tea.Cmd) {
	if m.phase != phaseAttaching || msg.opID != m.operationID {
		return m, nil
	}
	_ = m.prepared.Close()
	m.prepared = catalog.PreparedAttach{}
	if msg.err != nil {
		m.attachNotice = "Attach failed: " + msg.err.Error()
	} else {
		m.attachNotice = ""
	}
	m.phase = phaseRefreshing
	m.attachEnabled = false
	return m, m.beginList()
}

func (m Model) updateCreateDone(msg createDone) (tea.Model, tea.Cmd) {
	if m.phase != phaseCreatingSprite || msg.opID != m.operationID {
		return m, nil
	}
	m.cancelOperation()
	if msg.result.Confirmed {
		m.selectedID = catalog.DestinationID("created:" + msg.result.Name)
		return m, m.beginPrepareSprite(msg.result.Name)
	}
	if msg.result.Rejected {
		if catalog.NeedsSSHKey(msg.result.Err) && !m.sshRecoveryAttempted {
			return m, m.beginSSHKeyRecovery()
		}
		m.phase = phaseNamingSprite
		if catalog.NeedsSSHKey(msg.result.Err) {
			m.attachNotice = "ssh-add finished without loading a key. Add a key, then try again."
		} else {
			m.attachNotice = "Create failed: " + msg.result.Err.Error()
		}
		return m, nil
	}
	m.createBlock = msg.result.Name
	m.attachNotice = "Creation outcome is unknown. Reconcile " + msg.result.Name + " before creating another Sprite."
	m.phase = phaseRefreshing
	return m, m.beginList()
}

func (m Model) updateSSHAddReady(msg sshAddReady) (tea.Model, tea.Cmd) {
	if m.phase != phaseStartingSSHAgent || msg.opID != m.operationID {
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil {
		return m.sshKeySetupFailed(msg.err)
	}
	m.phase = phaseAddingSSHKey
	opID := msg.opID
	return m, m.execProcess(msg.command, func(err error) tea.Msg {
		return sshAddDone{opID: opID, err: err}
	})
}

func (m Model) updateSSHAddDone(msg sshAddDone) (tea.Model, tea.Cmd) {
	if m.phase != phaseAddingSSHKey || msg.opID != m.operationID {
		return m, nil
	}
	if msg.err != nil {
		return m.sshKeySetupFailed(msg.err)
	}
	return m.retryPending()
}

func (m *Model) beginSSHKeyRecovery() tea.Cmd {
	operations, ok := m.operations.(sshKeyOperations)
	if !ok {
		m.attachNotice = "Couldn’t start ssh-add."
		return nil
	}
	m.sshRecoveryAttempted = true
	m.attachNotice = ""
	m.operationID = m.nextID()
	ctx, cancel := context.WithCancel(m.root)
	m.operationStop = cancel
	m.phase = phaseStartingSSHAgent
	opID := m.operationID
	return func() tea.Msg {
		command, err := operations.SSHAddCommand(ctx)
		return sshAddReady{opID: opID, command: command, err: err}
	}
}

func (m Model) retryPending() (tea.Model, tea.Cmd) {
	switch m.pending.kind {
	case pendingCreate:
		return m, m.startCreate(m.pending.name)
	case pendingDestination:
		return m, m.startPrepare(m.pending.destination)
	case pendingCreatedSprite:
		return m, m.startPrepareSprite(m.pending.name)
	default:
		m.phase = phaseReady
		m.attachNotice = "SSH key setup completed, but there is no pending Sprite operation."
		return m, nil
	}
}

func (m Model) sshKeySetupFailed(err error) (tea.Model, tea.Cmd) {
	m.attachNotice = "ssh-add did not load a key: " + printable(err.Error())
	if m.pending.kind == pendingCreate {
		m.phase = phaseNamingSprite
		return m, nil
	}
	m.phase = phaseRefreshing
	return m, m.beginList()
}

func (m *Model) beginList() tea.Cmd {
	m.operationID = m.nextID()
	ctx, cancel := context.WithCancel(m.root)
	m.operationStop = cancel
	opID := m.operationID
	operations := m.operations
	return func() tea.Msg {
		if sourceOperations, ok := operations.(inventoryOperations); ok {
			inventory := sourceOperations.Inventory(ctx)
			rows := append(append([]catalog.Destination(nil), inventory.Local.Rows...), inventory.Sprites.Rows...)
			var err error
			if inventory.Local.Err != nil && inventory.Sprites.Err != nil {
				err = fmt.Errorf("local: %w; Sprites: %v", inventory.Local.Err, inventory.Sprites.Err)
			}
			return listDone{opID: opID, destinations: rows, inventory: &inventory, err: err}
		}
		destinations, err := operations.List(ctx)
		return listDone{opID: opID, destinations: append([]catalog.Destination(nil), destinations...), err: err}
	}
}

func (m *Model) beginCreate(name string) tea.Cmd {
	m.pending = pendingOperation{kind: pendingCreate, name: name}
	m.sshRecoveryAttempted = false
	return m.startCreate(name)
}

func (m *Model) startCreate(name string) tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	m.operationID = m.nextID()
	ctx, cancel := context.WithCancel(m.root)
	m.operationStop = cancel
	m.phase = phaseCreatingSprite
	opID := m.operationID
	return func() tea.Msg { return createDone{opID: opID, result: operations.Create(ctx, name)} }
}
func (m *Model) beginPrepareSprite(name string) tea.Cmd {
	m.pending = pendingOperation{kind: pendingCreatedSprite, name: name}
	return m.startPrepareSprite(name)
}

func (m *Model) startPrepareSprite(name string) tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	m.operationID = m.nextID()
	ctx, cancel := context.WithCancel(m.root)
	m.operationStop = cancel
	m.phase = phasePreparing
	m.preparingSprite = true
	opID := m.operationID
	destination := catalog.Destination{ID: catalog.DestinationID("created:" + name), Label: name}
	return func() tea.Msg {
		prepared, err := operations.PrepareSprite(ctx, name)
		return prepareDone{opID: opID, destination: destination, prepared: prepared, err: err}
	}
}
func (m *Model) beginAttach() tea.Cmd {
	args := m.prepared.HerdrArgs()
	command := exec.CommandContext(m.root, "herdr", append([]string(nil), args...)...)
	if socket := m.prepared.SSHAuthSocket(); socket != "" {
		environment := withoutEnvironment(os.Environ(), "SPRITE_TOKEN")
		command.Env = withEnvironment(environment, "SSH_AUTH_SOCK", socket)
	}
	m.phase = phaseAttaching
	opID := m.operationID
	return m.execProcess(command, func(err error) tea.Msg { return attachDone{opID: opID, err: err} })
}

func (m *Model) beginPrepare(destination catalog.Destination) tea.Cmd {
	m.pending = pendingOperation{kind: pendingDestination, destination: destination}
	m.sshRecoveryAttempted = false
	return m.startPrepare(destination)
}

func (m *Model) startPrepare(destination catalog.Destination) tea.Cmd {
	m.operationID = m.nextID()
	ctx, cancel := context.WithCancel(m.root)
	m.operationStop = cancel
	m.phase = phasePreparing
	_, m.preparingSprite = destination.Endpoint.(catalog.SpriteEndpoint)
	opID := m.operationID
	operations := m.operations
	return func() tea.Msg {
		prepared, err := operations.Prepare(ctx, destination)
		return prepareDone{
			opID:        opID,
			destination: destination,
			prepared:    prepared,
			err:         err,
		}
	}
}

func (m *Model) nextID() uint64 {
	m.nextOpID++
	return m.nextOpID
}

func (m *Model) cancelOperation() {
	if m.operationStop != nil {
		m.operationStop()
		m.operationStop = nil
	}
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	selectedStyle = lipgloss.NewStyle().Bold(true)
	accentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	runningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	warningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	mutedStyle    = lipgloss.NewStyle().Faint(true)
	keyStyle      = lipgloss.NewStyle().Bold(true)
)

// View renders the current state in Bubble Tea v2's View value.
func (m Model) View() tea.View {
	var content string
	switch m.phase {
	case phaseLoading:
		content = m.simpleView("Loading destinations…", "q quit")
	case phaseInitialLoadFailed:
		content = m.simpleView("Couldn’t load destinations.\n"+m.loadNotice, "r retry   q quit")
	case phaseRefreshing:
		content = m.snapshotView("Refreshing destinations…", "q quit")
	case phasePreparing:
		content = m.snapshotView("Preparing "+m.selectedLabel()+"…", "q quit")
	case phaseNamingSprite:
		content = m.nameView()
	case phaseCreatingSprite:
		content = m.simpleView("Creating "+m.spriteName+"…\nThis can take a moment.", "q quit")
	case phaseConfirmingHostKey:
		content = m.hostKeyView()
	case phaseStartingSSHAgent:
		content = m.simpleView("Starting a private ssh-agent…", "q quit")
	case phaseAddingSSHKey:
		content = m.simpleView("ssh-add has the terminal.\nComplete its prompt to continue.", "")
	case phaseAttaching:
		content = m.simpleView("Herdr has the terminal.", "")
	case phaseReady:
		if len(m.destinations) == 0 {
			body := "No destinations found."
			if m.loadNotice != "" {
				body = warningLine(m.loadNotice) + "\n\n" + body
			}
			content = m.simpleView(body, m.readyHelp())
		} else {
			content = m.snapshotView("", m.readyHelp())
		}
	default:
		content = m.simpleView("Unknown UI state.", "ctrl+c quit")
	}
	content = fitView(content, m.width, m.height)
	if content != "" {
		content += "\n"
	}
	return tea.NewView(content)
}

func (m Model) snapshotView(status, footer string) string {
	parts := m.header()
	if m.attachNotice != "" {
		parts = append(parts, "", warningLine(m.attachNotice))
	}
	if m.loadNotice != "" {
		parts = append(parts, "", warningLine(m.loadNotice))
	}
	if status != "" {
		parts = append(parts, "", accentStyle.Render("↻ "+printable(status)))
	}
	if m.editingFilter || m.filter != "" {
		value := m.filter
		if m.editingFilter {
			value += "▏"
		}
		parts = append(parts, "", mutedStyle.Render("Search"), "  "+printable(value))
	}

	visible := m.visibleDestinations()
	if len(visible) == 0 {
		if m.filter == "" {
			parts = append(parts, "", "No destinations found.")
		} else {
			parts = append(parts, "", "No destinations match “"+printable(m.filter)+"”.")
		}
	} else {
		rows, selectedLine := destinationRows(visible, m.cursor, m.width)
		available := max(1, m.height-len(parts)-footerLineCount(footer)-2)
		start := max(0, selectedLine-available+1)
		end := min(len(rows), start+available)
		parts = append(parts, "")
		parts = append(parts, rows[start:end]...)
	}
	if footer != "" {
		parts = append(parts, "", footer)
	}
	return strings.Join(parts, "\n")
}

func (m Model) simpleView(body, footer string) string {
	parts := m.header()
	parts = append(parts, "")
	for _, line := range strings.Split(body, "\n") {
		parts = append(parts, printable(line))
	}
	if m.attachNotice != "" {
		parts = append(parts, "", warningLine(m.attachNotice))
	}
	if footer != "" {
		parts = append(parts, "", footer)
	}
	return strings.Join(parts, "\n")
}

func (m Model) readyHelp() string {
	if m.editingFilter {
		return keyHint("enter", "apply") + "   " + keyHint("esc", "cancel") + "   " + keyHint("ctrl+c", "quit")
	}
	if m.showHelp {
		attach := keyHint("enter", "attach")
		if !m.attachEnabled {
			attach = mutedStyle.Render("attach unavailable until refresh succeeds")
		}
		secondLine := []string{keyHint("?", "close help"), keyHint("q", "quit")}
		if _, ok := m.operations.(spriteOperations); ok && m.spriteCreateEnabled && m.createBlock == "" {
			secondLine = append([]string{keyHint("n", "new Sprite")}, secondLine...)
		}
		return strings.Join([]string{
			keyHint("↑↓", "move") + "   " + attach + "   " + keyHint("/", "search") + "   " + keyHint("r", "refresh"),
			strings.Join(secondLine, "   "),
		}, "\n")
	}
	if !m.attachEnabled {
		return keyHint("r", "retry") + "   " + keyHint("/", "search") + "   " + keyHint("?", "help") + "   " + keyHint("q", "quit")
	}
	parts := []string{keyHint("↑↓", "move"), keyHint("enter", "attach")}
	if _, ok := m.operations.(spriteOperations); ok && m.spriteCreateEnabled && m.createBlock == "" {
		parts = append(parts, keyHint("n", "new Sprite"))
	}
	parts = append(parts, keyHint("/", "search"), keyHint("r", "refresh"), keyHint("?", "help"), keyHint("q", "quit"))
	return strings.Join(parts, "   ")
}

func (m Model) selectedDestination() (catalog.Destination, bool) {
	visible := m.visibleDestinations()
	if m.cursor < 0 || m.cursor >= len(visible) {
		return catalog.Destination{}, false
	}
	return visible[m.cursor], true
}

func (m Model) visibleDestinations() []catalog.Destination {
	if m.filter == "" {
		return m.destinations
	}
	needle := strings.ToLower(m.filter)
	visible := make([]catalog.Destination, 0, len(m.destinations))
	for _, destination := range m.destinations {
		if strings.Contains(strings.ToLower(destination.Label), needle) {
			visible = append(visible, destination)
		}
	}
	return visible
}

func (m Model) selectedLabel() string {
	for _, destination := range m.destinations {
		if destination.ID == m.selectedID {
			return printable(destination.Label)
		}
	}
	return "destination"
}

func (m Model) header() []string {
	title := titleStyle.Render("hrd")
	if summary := m.inventorySummary(); summary != "" {
		title += "  " + mutedStyle.Render(summary)
	}
	lines := []string{title}
	if m.height >= 6 {
		lines = append(lines, mutedStyle.Render("Choose a Herdr destination."))
	}
	return lines
}

func (m Model) inventorySummary() string {
	local, sprites := 0, 0
	for _, destination := range m.destinations {
		switch destination.Endpoint.(type) {
		case catalog.LocalEndpoint:
			local++
		case catalog.SpriteEndpoint:
			sprites++
		}
	}
	parts := make([]string, 0, 2)
	if local > 0 {
		parts = append(parts, fmt.Sprintf("%d local", local))
	}
	if sprites == 1 {
		parts = append(parts, "1 Sprite")
	} else if sprites > 1 {
		parts = append(parts, fmt.Sprintf("%d Sprites", sprites))
	}
	return strings.Join(parts, " · ")
}

func (m Model) nameView() string {
	value := m.spriteName + "▏"
	if m.spriteName == "" {
		value = mutedStyle.Render("lowercase name") + accentStyle.Render("▏")
	}
	parts := []string{
		titleStyle.Render("hrd") + "  " + mutedStyle.Render("/ New Sprite"),
		"",
		mutedStyle.Render("Name"),
		"  " + value,
		"",
		mutedStyle.Render("Use 1–63 lowercase letters, digits, and interior hyphens."),
		warningStyle.Render("Creation and waking may incur charges."),
	}
	if m.attachNotice != "" {
		parts = append(parts, "", warningLine(m.attachNotice))
	}
	parts = append(parts, "", keyHint("enter", "create")+"   "+keyHint("esc", "cancel"))
	return strings.Join(parts, "\n")
}

func (m Model) hostKeyView() string {
	return strings.Join([]string{
		titleStyle.Render("hrd") + "  " + mutedStyle.Render("/ Verify host key"),
		"",
		"Authenticated through the Sprites API:",
		accentStyle.Render(m.prepared.Fingerprint),
		"",
		"OpenSSH must show the same fingerprint.",
		"",
		keyHint("enter", "continue") + "   " + keyHint("esc", "cancel"),
	}, "\n")
}

func destinationRows(destinations []catalog.Destination, cursor, width int) ([]string, int) {
	rows := make([]string, 0, len(destinations)+4)
	selectedLine := 0
	previousGroup := ""
	for index, destination := range destinations {
		group := destinationGroup(destination)
		if group != previousGroup {
			if len(rows) > 0 {
				rows = append(rows, "")
			}
			rows = append(rows, mutedStyle.Render(group))
			previousGroup = group
		}
		if index == cursor {
			selectedLine = len(rows)
		}
		rows = append(rows, destinationLine(destination, index == cursor, width))
	}
	return rows, selectedLine
}

func destinationGroup(destination catalog.Destination) string {
	switch destination.Endpoint.(type) {
	case catalog.LocalEndpoint:
		return "Local"
	case catalog.SpriteEndpoint:
		return "Sprites"
	default:
		return "Other"
	}
}

func destinationLine(destination catalog.Destination, selected bool, width int) string {
	width = min(width, 72)
	state, symbol := "unknown", "?"
	stateStyle := mutedStyle
	if status, ok := destination.Status.(catalog.LocalSessionStatus); ok {
		if status.Running {
			state, symbol, stateStyle = "running", "●", runningStyle
		} else {
			state, symbol = "stopped", "○"
		}
	}
	if status, ok := destination.Status.(catalog.SpriteStatus); ok {
		state = string(status.Lifecycle)
		symbol = "○"
		if status.Lifecycle == spriteapi.Running {
			symbol, stateStyle = "●", runningStyle
		}
	}
	marker := "  "
	labelStyle := lipgloss.NewStyle()
	if selected {
		marker = accentStyle.Render("› ")
		labelStyle = selectedStyle
	}
	status := stateStyle.Render(symbol + " " + state)
	labelWidth := max(1, width-2-lipgloss.Width(status)-2)
	label := clip(printable(destination.Label), labelWidth)
	padding := strings.Repeat(" ", max(2, width-2-lipgloss.Width(label)-lipgloss.Width(status)))
	return marker + labelStyle.Render(label) + padding + status
}

func warningLine(text string) string {
	return warningStyle.Render("! ") + printable(text)
}

func keyHint(key, action string) string {
	return keyStyle.Render(key) + " " + mutedStyle.Render(action)
}

func prepareFailureNotice(err error) string {
	message := printable(err.Error())
	if strings.Contains(message, "SSH_AUTH_SOCK is unset") {
		return "SSH key required. Start ssh-agent, load a key with ssh-add, then try again."
	}
	if strings.Contains(message, "ssh-agent has no public keys") {
		return "SSH key required. Load a key with ssh-add, then try again."
	}
	return "Couldn’t prepare destination: " + message
}

func withoutEnvironment(environment []string, key string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return result
}

func withEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func printable(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(text, "?"))
}

func fitView(content string, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for index := range lines {
		lines[index] = clip(lines[index], width)
	}
	return strings.Join(lines, "\n")
}

func clip(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	return ansi.Truncate(text, width-1, "…")
}

func footerLineCount(footer string) int {
	if footer == "" {
		return 0
	}
	return strings.Count(footer, "\n") + 1
}

var _ tea.Model = Model{}
