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
	PrepareSprite(context.Context, string, catalog.SpriteTransport) (catalog.PreparedAttach, error)
}
type sshKeyOperations interface {
	SSHAddCommand(context.Context) (*exec.Cmd, error)
	SelectSSHKey(string) error
}

type sourceSnapshot struct {
	rows    []catalog.Destination
	loaded  bool
	current bool
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
	phaseChoosingSpriteTransport
	phaseConfirmingHostKey
	phaseStartingSSHAgent
	phaseAddingSSHKey
	phaseChoosingSSHKey
)

type listDone struct {
	opID         uint64
	destinations []catalog.Destination
	inventory    *catalog.Inventory
	err          error
}

type prepareDone struct {
	opID     uint64
	prepared catalog.PreparedAttach
	err      error
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

type spriteAttachOrigin uint8

const (
	spriteOriginExisting spriteAttachOrigin = iota + 1
	spriteOriginConfirmedCreate
)

type spriteAttachTarget struct {
	origin    spriteAttachOrigin
	name      string
	transport catalog.SpriteTransport
}

type prepareKind uint8

const (
	prepareLocal prepareKind = iota + 1
	prepareSprite
)

type prepareOperation struct {
	kind        prepareKind
	destination catalog.Destination
	sprite      spriteAttachTarget
}

type execProcessFunc func(*exec.Cmd, tea.ExecCallback) tea.Cmd
type execCommandFunc func(tea.ExecCommand, tea.ExecCallback) tea.Cmd

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
	execCommand execCommandFunc
	keys        keyMap

	phase         phase
	nextOpID      uint64
	operationID   uint64
	operationStop context.CancelFunc
	initialCmd    tea.Cmd

	destinations         []catalog.Destination
	inventoryAware       bool
	localSnapshot        sourceSnapshot
	spriteSnapshot       sourceSnapshot
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
	transportTarget      spriteAttachTarget
	activePrepare        prepareOperation
	sshRecoveryAttempted bool
	sshKeySource         string
	sshKeyChoices        []catalog.SSHIdentity
	sshKeyCursor         int

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

	_, inventoryAware := operations.(inventoryOperations)
	m := Model{
		root:           root,
		operations:     operations,
		execProcess:    tea.ExecProcess,
		execCommand:    tea.Exec,
		keys:           newKeyMap(),
		phase:          phaseLoading,
		inventoryAware: inventoryAware,
		attachEnabled:  false,
		width:          defaultWidth,
		height:         defaultHeight,
	}
	m.initialCmd = m.beginList()
	return m
}

// Init starts the initial inventory load.
func (m Model) Init() tea.Cmd { return m.initialCmd }

// Update applies one Bubble Tea message.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.quitting {
		if completion, ok := msg.(prepareDone); ok {
			closePrepared(completion.prepared)
		}
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
	textEditing := m.editingFilter || m.phase == phaseNamingSprite
	if key.Matches(msg, m.keys.force) || (key.Matches(msg, m.keys.quit) && !textEditing) {
		m.cancelOperation()
		closePrepared(m.prepared)
		m.prepared = nil
		m.quitting = true
		return m, tea.Quit
	}
	if m.phase == phaseAttaching || m.phase == phaseAddingSSHKey {
		return m, nil
	}

	if m.phase == phaseNamingSprite {
		return m.updateName(msg)
	}
	if m.phase == phaseChoosingSpriteTransport {
		return m.updateSpriteTransport(msg)
	}
	if m.phase == phaseConfirmingHostKey {
		if msg.String() == "enter" {
			return m, m.beginAttach()
		}
		if msg.String() == "esc" {
			closePrepared(m.prepared)
			m.prepared = nil
			m.phase = phaseRefreshing
			return m, m.beginList()
		}
		return m, nil
	}
	if m.phase == phaseChoosingSSHKey {
		return m.updateSSHKeyChoice(msg)
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
		if !m.selectedAttachEnabled() {
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
		if endpoint, ok := destination.Endpoint.(catalog.SpriteEndpoint); ok {
			m.beginTransportChoice(spriteAttachTarget{
				origin: spriteOriginExisting,
				name:   endpoint.Name,
			})
			return m, nil
		}
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

func (m *Model) beginTransportChoice(target spriteAttachTarget) {
	target.transport = catalog.NativePreview
	m.transportTarget = target
	m.phase = phaseChoosingSpriteTransport
	m.attachEnabled = false
	m.attachNotice = ""
}

func (m Model) updateSpriteTransport(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.up), key.Matches(msg, m.keys.down):
		if m.transportTarget.transport == catalog.NativePreview {
			m.transportTarget.transport = catalog.SSHFallback
		} else {
			m.transportTarget.transport = catalog.NativePreview
		}
		return m, nil
	case msg.String() == "enter":
		return m, m.beginPrepareSprite(m.transportTarget)
	case msg.String() == "esc":
		if m.transportTarget.origin == spriteOriginConfirmedCreate {
			m.phase = phaseRefreshing
			return m, m.beginList()
		}
		m.phase = phaseReady
		m.attachEnabled = m.hasAttachableDestinations()
		return m, nil
	default:
		return m, nil
	}
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

	if msg.inventory != nil {
		return m.updateInventoryDone(*msg.inventory, wasInitial), nil
	}

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
	m.destinations = append([]catalog.Destination(nil), msg.destinations...)
	m.reconcileCreateBlock()
	m.cursor = 0
	m.filter = ""
	m.editingFilter = false
	return m, nil
}

func (m Model) updateInventoryDone(inventory catalog.Inventory, wasInitial bool) Model {
	selected, hadSelection := m.selectedDestination()
	m.localSnapshot = refreshedSnapshot(m.localSnapshot, inventory.Local)
	m.spriteSnapshot = refreshedSnapshot(m.spriteSnapshot, inventory.Sprites)
	m.destinations = append(append([]catalog.Destination(nil), m.localSnapshot.rows...), m.spriteSnapshot.rows...)
	m.spriteCreateEnabled = m.spriteSnapshot.loaded && m.spriteSnapshot.current
	m.attachEnabled = m.hasAttachableDestinations()
	m.loadNotice = inventoryNotice(inventory, wasInitial)
	m.editingFilter = false
	m.reconcileCreateBlock()

	if wasInitial && !m.localSnapshot.loaded && !m.spriteSnapshot.loaded {
		m.phase = phaseInitialLoadFailed
		m.destinations = nil
		m.attachEnabled = false
		m.spriteCreateEnabled = false
		if m.loadNotice == "" {
			m.loadNotice = "No destination source loaded."
		}
		m.cursor = 0
		return m
	}

	m.phase = phaseReady
	if hadSelection {
		m.restoreCursor(selected.ID)
	} else {
		m.cursor = min(m.cursor, max(0, len(m.visibleDestinations())-1))
	}
	return m
}

func refreshedSnapshot(previous sourceSnapshot, result catalog.SourceResult) sourceSnapshot {
	if result.Skipped {
		return sourceSnapshot{}
	}
	if result.Err != nil {
		previous.current = false
		return previous
	}
	return sourceSnapshot{
		rows:    append([]catalog.Destination(nil), result.Rows...),
		loaded:  true,
		current: true,
	}
}

func inventoryNotice(inventory catalog.Inventory, initial bool) string {
	action := "refresh"
	if initial {
		action = "load"
	}
	var notices []string
	if inventory.Local.Err != nil {
		notices = append(notices, "Local "+action+" failed: "+inventory.Local.Err.Error())
	}
	if inventory.Sprites.Err != nil {
		notices = append(notices, "Sprite "+action+" failed: "+inventory.Sprites.Err.Error())
	}
	return strings.Join(notices, "; ")
}

func (m Model) updatePrepareDone(msg prepareDone) (tea.Model, tea.Cmd) {
	if m.phase != phasePreparing || msg.opID != m.operationID {
		closePrepared(msg.prepared)
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil {
		closePrepared(msg.prepared)
		if m.activePrepare.kind == prepareSprite && m.activePrepare.sprite.transport == catalog.SSHFallback {
			if source, choices, ok := catalog.SSHKeyChoices(msg.err); ok {
				return m.beginSSHKeySelection(source, choices)
			}
			if catalog.NeedsSSHKey(msg.err) && !m.sshRecoveryAttempted {
				return m, m.beginSSHKeyRecovery()
			}
			if catalog.NeedsSSHKey(msg.err) {
				m.attachNotice = "ssh-add finished without loading a key. Add a key, then try again."
			} else {
				m.attachNotice = prepareFailureNotice(msg.err)
			}
			m.phase = phaseRefreshing
			return m, m.beginList()
		}
		m.attachNotice = prepareFailureNotice(msg.err)
		if m.activePrepare.kind == prepareSprite {
			m.phase = phaseRefreshing
			return m, m.beginList()
		}
		m.phase = phaseReady
		m.attachEnabled = true
		return m, nil
	}
	if msg.prepared == nil {
		m.attachNotice = "Couldn’t prepare destination: no attachment was returned."
		if m.activePrepare.kind == prepareSprite {
			m.phase = phaseRefreshing
			return m, m.beginList()
		}
		m.phase = phaseReady
		m.attachEnabled = true
		return m, nil
	}

	m.prepared = msg.prepared
	switch prepared := msg.prepared.(type) {
	case catalog.PreparedProcessAttach:
		if prepared.Fingerprint != "" {
			m.phase = phaseConfirmingHostKey
			return m, nil
		}
		return m, m.beginProcessAttach(prepared)
	case catalog.PreparedNativeAttach:
		if prepared.Command == nil {
			closePrepared(m.prepared)
			m.prepared = nil
			m.attachNotice = "Couldn’t prepare destination: no native command was returned."
			m.phase = phaseRefreshing
			return m, m.beginList()
		}
		return m, m.beginNativeAttach(prepared)
	default:
		closePrepared(m.prepared)
		m.prepared = nil
		m.attachNotice = "Couldn’t prepare destination: unknown attachment type."
		if m.activePrepare.kind == prepareSprite {
			m.phase = phaseRefreshing
			return m, m.beginList()
		}
		m.phase = phaseReady
		m.attachEnabled = true
		return m, nil
	}
}

func (m Model) updateAttachDone(msg attachDone) (tea.Model, tea.Cmd) {
	if m.phase != phaseAttaching || msg.opID != m.operationID {
		return m, nil
	}
	closePrepared(m.prepared)
	m.prepared = nil
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
		if err := spriteapi.ValidName(msg.result.Name); err != nil {
			m.createBlock = msg.result.Name
			m.attachNotice = "Creation was confirmed with an invalid Sprite name. Refresh to reconcile it."
			m.phase = phaseRefreshing
			return m, m.beginList()
		}
		m.beginTransportChoice(spriteAttachTarget{
			origin: spriteOriginConfirmedCreate,
			name:   msg.result.Name,
		})
		return m, nil
	}
	if msg.result.Rejected {
		m.phase = phaseNamingSprite
		if msg.result.Err == nil {
			m.attachNotice = "Create failed."
		} else {
			m.attachNotice = "Create failed: " + printable(msg.result.Err.Error())
		}
		return m, nil
	}
	m.createBlock = msg.result.Name
	m.attachNotice = "Creation outcome is unknown. Reconcile " + msg.result.Name + " before creating another Sprite."
	m.phase = phaseRefreshing
	return m, m.beginList()
}

func (m Model) beginSSHKeySelection(source string, choices []catalog.SSHIdentity) (tea.Model, tea.Cmd) {
	m.phase = phaseChoosingSSHKey
	m.attachNotice = ""
	m.sshKeySource = printable(source)
	m.sshKeyChoices = append([]catalog.SSHIdentity(nil), choices...)
	m.sshKeyCursor = 0
	return m, nil
}

func (m Model) updateSSHKeyChoice(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.up):
		m.sshKeyCursor = max(0, m.sshKeyCursor-1)
		return m, nil
	case key.Matches(msg, m.keys.down):
		m.sshKeyCursor = min(max(0, len(m.sshKeyChoices)-1), m.sshKeyCursor+1)
		return m, nil
	case msg.String() == "esc":
		return m.cancelSSHKeySelection()
	case msg.String() == "enter":
		if m.sshKeyCursor < 0 || m.sshKeyCursor >= len(m.sshKeyChoices) {
			return m, nil
		}
		operations, ok := m.operations.(sshKeyOperations)
		if !ok {
			m.attachNotice = "Couldn’t select an SSH key."
			return m, nil
		}
		choice := m.sshKeyChoices[m.sshKeyCursor]
		if err := operations.SelectSSHKey(choice.Fingerprint); err != nil {
			m.attachNotice = "Couldn’t select SSH key: " + printable(err.Error())
			return m, nil
		}
		m.sshKeyChoices = nil
		m.sshKeySource = ""
		return m.retryPending()
	default:
		return m, nil
	}
}

func (m Model) cancelSSHKeySelection() (tea.Model, tea.Cmd) {
	m.sshKeyChoices = nil
	m.sshKeySource = ""
	return m.afterSpriteAttachCanceled()
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
	target := m.activePrepare.sprite
	if target.transport != catalog.SSHFallback || target.name == "" {
		m.phase = phaseReady
		m.attachNotice = "SSH key setup completed, but there is no pending Sprite fallback."
		return m, nil
	}
	return m, m.startPrepareSprite(target)
}

func (m Model) sshKeySetupFailed(err error) (tea.Model, tea.Cmd) {
	m.attachNotice = "ssh-add did not load a key: " + printable(err.Error())
	m.phase = phaseRefreshing
	return m, m.beginList()
}

func (m Model) afterSpriteAttachCanceled() (tea.Model, tea.Cmd) {
	if m.activePrepare.sprite.origin == spriteOriginConfirmedCreate {
		m.phase = phaseRefreshing
		return m, m.beginList()
	}
	m.phase = phaseReady
	m.attachEnabled = m.hasAttachableDestinations()
	return m, nil
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
			return listDone{opID: opID, inventory: &inventory}
		}
		destinations, err := operations.List(ctx)
		return listDone{opID: opID, destinations: append([]catalog.Destination(nil), destinations...), err: err}
	}
}

func (m *Model) beginCreate(name string) tea.Cmd {
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
func (m *Model) beginPrepareSprite(target spriteAttachTarget) tea.Cmd {
	m.sshRecoveryAttempted = false
	return m.startPrepareSprite(target)
}

func (m *Model) startPrepareSprite(target spriteAttachTarget) tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	m.operationID = m.nextID()
	ctx, cancel := context.WithCancel(m.root)
	m.operationStop = cancel
	m.phase = phasePreparing
	m.activePrepare = prepareOperation{kind: prepareSprite, sprite: target}
	opID := m.operationID
	return func() tea.Msg {
		prepared, err := operations.PrepareSprite(ctx, target.name, target.transport)
		return prepareDone{opID: opID, prepared: prepared, err: err}
	}
}

func (m *Model) beginAttach() tea.Cmd {
	prepared, ok := m.prepared.(catalog.PreparedProcessAttach)
	if !ok {
		return nil
	}
	return m.beginProcessAttach(prepared)
}

func (m *Model) beginProcessAttach(prepared catalog.PreparedProcessAttach) tea.Cmd {
	args := prepared.HerdrArgs()
	command := exec.CommandContext(m.root, "herdr", append([]string(nil), args...)...)
	if socket := prepared.SSHAuthSocket(); socket != "" {
		environment := withoutEnvironment(os.Environ(), "SPRITE_TOKEN")
		command.Env = withEnvironment(environment, "SSH_AUTH_SOCK", socket)
	}
	m.phase = phaseAttaching
	opID := m.operationID
	return m.execProcess(command, func(err error) tea.Msg { return attachDone{opID: opID, err: err} })
}

func (m *Model) beginNativeAttach(prepared catalog.PreparedNativeAttach) tea.Cmd {
	m.phase = phaseAttaching
	opID := m.operationID
	return m.execCommand(prepared.Command, func(err error) tea.Msg { return attachDone{opID: opID, err: err} })
}

func (m *Model) beginPrepare(destination catalog.Destination) tea.Cmd {
	m.sshRecoveryAttempted = false
	return m.startPrepare(destination)
}

func (m *Model) startPrepare(destination catalog.Destination) tea.Cmd {
	m.operationID = m.nextID()
	ctx, cancel := context.WithCancel(m.root)
	m.operationStop = cancel
	m.phase = phasePreparing
	m.activePrepare = prepareOperation{kind: prepareLocal, destination: destination}
	opID := m.operationID
	operations := m.operations
	return func() tea.Msg {
		prepared, err := operations.Prepare(ctx, destination)
		return prepareDone{
			opID:     opID,
			prepared: prepared,
			err:      err,
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
	case phaseChoosingSpriteTransport:
		content = m.spriteTransportView()
	case phaseConfirmingHostKey:
		content = m.hostKeyView()
	case phaseStartingSSHAgent:
		content = m.simpleView("Starting a private ssh-agent…", "q quit")
	case phaseAddingSSHKey:
		content = m.simpleView("ssh-add has the terminal.\nComplete its prompt to continue.", "")
	case phaseChoosingSSHKey:
		content = m.sshKeyView()
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
		rows, selectedLine := destinationRows(visible, m.cursor, m.width, func(destination catalog.Destination) bool {
			return m.inventoryAware && !m.destinationCurrent(destination)
		})
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
	selectedAttachEnabled := m.selectedAttachEnabled()
	if m.showHelp {
		attach := keyHint("enter", "attach")
		if !selectedAttachEnabled {
			unavailable := "attach unavailable until refresh succeeds"
			if m.selectedDestinationStale() {
				unavailable = "stale destination unavailable until its source refreshes"
			}
			attach = mutedStyle.Render(unavailable)
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
	if !selectedAttachEnabled {
		if m.inventoryAware && m.attachEnabled {
			return keyHint("↑↓", "move") + "   " + keyHint("r", "refresh") + "   " + keyHint("/", "search") + "   " + keyHint("?", "help") + "   " + keyHint("q", "quit")
		}
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

func (m Model) selectedAttachEnabled() bool {
	if !m.attachEnabled {
		return false
	}
	destination, ok := m.selectedDestination()
	return ok && m.destinationCurrent(destination)
}

func (m Model) selectedDestinationStale() bool {
	destination, ok := m.selectedDestination()
	return ok && m.inventoryAware && !m.destinationCurrent(destination)
}

func (m Model) destinationCurrent(destination catalog.Destination) bool {
	if !m.inventoryAware {
		return true
	}
	switch destination.Endpoint.(type) {
	case catalog.LocalEndpoint:
		return m.localSnapshot.loaded && m.localSnapshot.current
	case catalog.SpriteEndpoint:
		return m.spriteSnapshot.loaded && m.spriteSnapshot.current
	default:
		return false
	}
}

func (m Model) hasAttachableDestinations() bool {
	if !m.inventoryAware {
		return len(m.destinations) > 0
	}
	return (m.localSnapshot.loaded && m.localSnapshot.current && len(m.localSnapshot.rows) > 0) ||
		(m.spriteSnapshot.loaded && m.spriteSnapshot.current && len(m.spriteSnapshot.rows) > 0)
}

func (m *Model) reconcileCreateBlock() {
	if m.createBlock == "" {
		return
	}
	rows := m.destinations
	if m.inventoryAware {
		if !m.spriteSnapshot.loaded || !m.spriteSnapshot.current {
			return
		}
		rows = m.spriteSnapshot.rows
	}
	for _, destination := range rows {
		if endpoint, ok := destination.Endpoint.(catalog.SpriteEndpoint); ok && endpoint.Name == m.createBlock {
			m.createBlock = ""
			return
		}
	}
}

func (m *Model) restoreCursor(id catalog.DestinationID) {
	visible := m.visibleDestinations()
	for index, destination := range visible {
		if destination.ID == id {
			m.cursor = index
			return
		}
	}
	m.cursor = min(m.cursor, max(0, len(visible)-1))
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
	if m.activePrepare.kind == prepareSprite && m.activePrepare.sprite.name != "" {
		return printable(m.activePrepare.sprite.name)
	}
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

func (m Model) spriteTransportView() string {
	parts := []string{
		titleStyle.Render("hrd") + "  " + mutedStyle.Render("/ "+printable(m.transportTarget.name)+" / Transport"),
		"",
		"Choose how to open this Sprite.",
		"",
	}
	options := []struct {
		title       string
		labels      string
		explanation string
	}{
		{
			title:       "Native Sprite terminal",
			labels:      "preview · recommended",
			explanation: "Herdr and its config live in the Sprite.",
		},
		{
			title:       "SSH thin client",
			labels:      "fallback",
			explanation: "Local Herdr and your SSH agent are used.",
		},
	}
	for index, option := range options {
		marker := "  "
		style := lipgloss.NewStyle()
		selected := (index == 0 && m.transportTarget.transport == catalog.NativePreview) ||
			(index == 1 && m.transportTarget.transport == catalog.SSHFallback)
		if selected {
			marker = accentStyle.Render("› ")
			style = selectedStyle
		}
		if m.width >= 52 {
			parts = append(parts, marker+style.Render(option.title)+"  "+accentStyle.Render(option.labels))
		} else {
			parts = append(parts, marker+style.Render(option.title), "  "+accentStyle.Render(option.labels))
		}
		parts = append(parts, "  "+mutedStyle.Render(option.explanation))
		if index == 0 {
			parts = append(parts, "")
		}
	}
	parts = append(parts, "", keyHint("↑↓/jk", "switch")+"   "+keyHint("enter", "continue")+"   "+keyHint("esc", "cancel"))
	return strings.Join(parts, "\n")
}

func (m Model) sshKeyView() string {
	source := m.sshKeySource
	if source == "" {
		source = "ssh-agent"
	}
	parts := []string{
		titleStyle.Render("hrd") + "  " + mutedStyle.Render("/ Choose SSH key"),
		"",
		fmt.Sprintf("%s exposes %d keys.", source, len(m.sshKeyChoices)),
		"Only the selected public key will be installed in the Sprite.",
		"",
	}
	available := max(1, m.height-11)
	start := max(0, m.sshKeyCursor-available+1)
	end := min(len(m.sshKeyChoices), start+available)
	for index := start; index < end; index++ {
		choice := m.sshKeyChoices[index]
		label := printable(choice.Comment)
		if label == "" {
			label = "Unnamed key"
		}
		marker := "  "
		style := lipgloss.NewStyle()
		if index == m.sshKeyCursor {
			marker = accentStyle.Render("› ")
			style = selectedStyle
		}
		line := marker + style.Render(label) + "  " + mutedStyle.Render(printable(choice.Algorithm))
		parts = append(parts, clip(line, min(m.width, 72)))
	}
	if m.sshKeyCursor >= 0 && m.sshKeyCursor < len(m.sshKeyChoices) {
		parts = append(parts, "", mutedStyle.Render("Fingerprint"), accentStyle.Render(m.sshKeyChoices[m.sshKeyCursor].Fingerprint))
	}
	if m.attachNotice != "" {
		parts = append(parts, "", warningLine(m.attachNotice))
	}
	parts = append(parts, "", keyHint("↑↓", "move")+"   "+keyHint("enter", "use key")+"   "+keyHint("esc", "cancel"))
	return strings.Join(parts, "\n")
}

func (m Model) hostKeyView() string {
	prepared, _ := m.prepared.(catalog.PreparedProcessAttach)
	return strings.Join([]string{
		titleStyle.Render("hrd") + "  " + mutedStyle.Render("/ Verify host key"),
		"",
		"Authenticated through the Sprites API:",
		accentStyle.Render(prepared.Fingerprint),
		"",
		"OpenSSH must show the same fingerprint.",
		"",
		keyHint("enter", "continue") + "   " + keyHint("esc", "cancel"),
	}, "\n")
}

func destinationRows(destinations []catalog.Destination, cursor, width int, stale func(catalog.Destination) bool) ([]string, int) {
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
		rows = append(rows, destinationLine(destination, index == cursor, stale(destination), width))
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

func destinationLine(destination catalog.Destination, selected, stale bool, width int) string {
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
	if stale {
		state, symbol, stateStyle = "stale", "!", warningStyle
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

func closePrepared(prepared catalog.PreparedAttach) {
	if prepared != nil {
		_ = prepared.Close()
	}
}

func prepareFailureNotice(err error) string {
	if err == nil {
		return "Couldn’t prepare destination."
	}
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
