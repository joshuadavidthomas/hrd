// Package ui owns the terminal picker state machine and rendering.
package ui

import (
	"context"
	"os/exec"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
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

type phase uint8

const (
	phaseLoading phase = iota + 1
	phaseReady
	phaseRefreshing
	phasePreparing
	phaseAttaching
	phaseInitialLoadFailed
)

type listDone struct {
	opID         uint64
	destinations []catalog.Destination
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

	destinations  []catalog.Destination
	selectedID    catalog.DestinationID
	cursor        int
	attachEnabled bool
	loadNotice    string
	attachNotice  string
	showHelp      bool
	editingFilter bool
	quitting      bool
	filter        string
	filterBefore  string

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
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	default:
		return m, nil
	}
}

func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.phase == phaseAttaching {
		return m, nil
	}
	if key.Matches(msg, m.keys.force) || (key.Matches(msg, m.keys.quit) && !m.editingFilter) {
		m.cancelOperation()
		m.quitting = true
		return m, tea.Quit
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
	m.loadNotice = ""
	m.destinations = append([]catalog.Destination(nil), msg.destinations...)
	m.cursor = 0
	m.filter = ""
	m.editingFilter = false
	return m, nil
}

func (m Model) updatePrepareDone(msg prepareDone) (tea.Model, tea.Cmd) {
	if m.phase != phasePreparing || msg.opID != m.operationID || msg.destination.ID != m.selectedID {
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil {
		m.phase = phaseReady
		m.attachEnabled = true
		m.attachNotice = "Prepare failed: " + msg.err.Error()
		return m, nil
	}

	args := msg.prepared.HerdrArgs()
	command := exec.Command("herdr", append([]string(nil), args...)...)
	m.phase = phaseAttaching
	opID := msg.opID
	cmd := m.execProcess(command, func(err error) tea.Msg {
		return attachDone{opID: opID, err: err}
	})
	return m, cmd
}

func (m Model) updateAttachDone(msg attachDone) (tea.Model, tea.Cmd) {
	if m.phase != phaseAttaching || msg.opID != m.operationID {
		return m, nil
	}
	if msg.err != nil {
		m.attachNotice = "Attach failed: " + msg.err.Error()
	} else {
		m.attachNotice = ""
	}
	m.phase = phaseRefreshing
	m.attachEnabled = false
	return m, m.beginList()
}

func (m *Model) beginList() tea.Cmd {
	m.operationID = m.nextID()
	ctx, cancel := context.WithCancel(m.root)
	m.operationStop = cancel
	opID := m.operationID
	operations := m.operations
	return func() tea.Msg {
		destinations, err := operations.List(ctx)
		return listDone{
			opID:         opID,
			destinations: append([]catalog.Destination(nil), destinations...),
			err:          err,
		}
	}
}

func (m *Model) beginPrepare(destination catalog.Destination) tea.Cmd {
	m.operationID = m.nextID()
	ctx, cancel := context.WithCancel(m.root)
	m.operationStop = cancel
	m.phase = phasePreparing
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

// View renders the current state in Bubble Tea v2's View value.
func (m Model) View() tea.View {
	var content string
	switch m.phase {
	case phaseLoading:
		content = m.simpleView("Loading local sessions…", "q quit • ctrl+c quit")
	case phaseInitialLoadFailed:
		content = m.simpleView(
			"Could not load local sessions.\n"+m.loadNotice,
			"r retry • q quit • ctrl+c quit",
		)
	case phaseRefreshing:
		content = m.snapshotView("Refreshing local sessions…", "q quit • ctrl+c quit")
	case phasePreparing:
		content = m.snapshotView("Preparing "+m.selectedLabel()+"…", "q quit • ctrl+c quit")
	case phaseAttaching:
		content = m.simpleView("Terminal handed to Herdr.", "")
	case phaseReady:
		if len(m.destinations) == 0 {
			body := "No local sessions found."
			if m.loadNotice != "" {
				body = m.loadNotice + "\n" + body
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
	parts := []string{"hrd"}
	if m.attachNotice != "" {
		parts = append(parts, clip(printable(m.attachNotice), m.width))
	}
	if m.loadNotice != "" {
		parts = append(parts, clip(printable(m.loadNotice), m.width))
	}
	if status != "" {
		parts = append(parts, clip(status, m.width))
	}
	if m.editingFilter || m.filter != "" {
		parts = append(parts, clip(printable("Filter: "+m.filter), m.width))
	}

	visible := m.visibleDestinations()
	if len(visible) == 0 {
		if m.filter == "" {
			parts = append(parts, "No local sessions found.")
		} else {
			parts = append(parts, "Nothing matched the filter.")
		}
	} else {
		available := max(1, m.height-len(parts)-footerLineCount(footer)-1)
		start := 0
		if m.cursor >= available {
			start = m.cursor - available + 1
		}
		end := min(len(visible), start+available)
		for index := start; index < end; index++ {
			marker := "  "
			if index == m.cursor {
				marker = "> "
			}
			parts = append(parts, clip(marker+destinationLine(visible[index]), m.width))
		}
	}
	if footer != "" {
		parts = append(parts, footer)
	}
	return strings.Join(parts, "\n")
}

func (m Model) simpleView(body, footer string) string {
	parts := []string{"hrd"}
	for _, line := range strings.Split(body, "\n") {
		parts = append(parts, clip(printable(line), m.width))
	}
	if m.attachNotice != "" {
		parts = append(parts, clip(printable(m.attachNotice), m.width))
	}
	if footer != "" {
		parts = append(parts, "", footer)
	}
	return strings.Join(parts, "\n")
}

func (m Model) readyHelp() string {
	if m.editingFilter {
		return "type to filter (q types q) • enter apply • esc cancel • ctrl+c quit"
	}
	if m.showHelp {
		attach := "enter attach"
		if !m.attachEnabled {
			attach = "attach disabled until refresh succeeds"
		}
		return strings.Join([]string{
			attach + " • r refresh • / filter • ? close help",
			"↑/k up • ↓/j down • q quit • ctrl+c quit",
		}, "\n")
	}
	if !m.attachEnabled {
		return "r retry refresh • / filter • ? help • q quit • ctrl+c quit"
	}
	return "enter attach • r refresh • / filter • ? help • q quit • ctrl+c quit"
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

func destinationLine(destination catalog.Destination) string {
	state := "unknown status"
	if status, ok := destination.Status.(catalog.LocalSessionStatus); ok {
		if status.Running {
			state = "Herdr running"
		} else {
			state = "Herdr stopped"
		}
	}
	return printable(destination.Label) + "  " + state
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

	var clipped strings.Builder
	used := 0
	for _, r := range text {
		runeWidth := lipgloss.Width(string(r))
		if used+runeWidth > width-1 {
			break
		}
		clipped.WriteRune(r)
		used += runeWidth
	}
	clipped.WriteRune('…')
	return clipped.String()
}

func footerLineCount(footer string) int {
	if footer == "" {
		return 0
	}
	return strings.Count(footer, "\n") + 1
}

var _ tea.Model = Model{}
