// Package ui owns hrd's terminal navigation and presentation.
package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	spriteapi "github.com/joshuadavidthomas/hrd/internal/sprites"
)

const (
	defaultWidth            = 80
	defaultHeight           = 24
	refreshInterval         = 10 * time.Second
	minBrowserPanelWidth    = 60
	maxBrowserPanelWidth    = 82
	maxSurfaceWidth         = 104
	surfaceHorizontalMargin = 4
	browserMarkerWidth      = 2
	browserStatusGap        = 3
	browserStatusWidth      = 9
	browserPanelChrome      = 4
)

// Operations is the catalog work used by the UI.
type Operations interface {
	Inventory(context.Context) catalog.Inventory
	Prepare(context.Context, catalog.Destination) (catalog.PreparedAttach, error)
}

type spriteOperations interface {
	Create(context.Context, string) spriteapi.CreateResult
	ListSpriteSessions(context.Context, catalog.SpriteEndpointSelection) ([]catalog.Destination, error)
	InstallSpriteHerdr(context.Context, catalog.SpriteEndpointSelection) error
	StopSpriteSession(context.Context, catalog.SpriteSessionSelection) error
	PrepareSprite(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport) (catalog.PreparedAttach, error)
}

type sshKeyOperations interface {
	SSHAddCommand(context.Context) (*exec.Cmd, error)
	SelectSSHKey(string) error
}

type screen uint8

const (
	screenEndpoints screen = iota + 1
	screenSprite
)

type phase uint8

const (
	phaseLoading phase = iota + 1
	phaseReady
	phaseNamingSprite
	phaseCreatingSprite
	phaseLoadingSprite
	phaseRefreshingSprite
	phaseConfirmingSpriteInstall
	phaseInstallingSpriteHerdr
	phaseConfirmingStop
	phaseStopping
	phaseChoosingSpriteTransport
	phasePreparing
	phaseConfirmingHostKey
	phaseStartingSSHAgent
	phaseAddingSSHKey
	phaseChoosingSSHKey
	phaseAttaching
	phaseInitialLoadFailed
)

type sourceSnapshot struct {
	rows    []catalog.Destination
	loaded  bool
	current bool
}

type browserItem struct {
	destination catalog.Destination
	kind        string
	symbol      string
	state       string
	stateStyle  lipgloss.Style
	stale       bool
}

func (i browserItem) FilterValue() string {
	return strings.Join([]string{printable(i.destination.Label), i.kind, i.state}, " ")
}

type browserDelegate struct{}

func (browserDelegate) Height() int                         { return 1 }
func (browserDelegate) Spacing() int                        { return 0 }
func (browserDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (browserDelegate) Render(w io.Writer, model list.Model, index int, raw list.Item) {
	item, ok := raw.(browserItem)
	if !ok {
		return
	}
	_, _ = fmt.Fprint(w, renderBrowserRow(item, index == model.Index(), max(1, model.Width()-1)))
}

func renderBrowserRow(item browserItem, selected bool, width int) string {
	marker := "  "
	markerStyle := lipgloss.NewStyle()
	labelStyle := lipgloss.NewStyle()
	if selected {
		marker = "› "
		markerStyle = accentStyle
		labelStyle = selectedStyle
	}
	stateWidth := min(browserStatusWidth, max(1, width-browserMarkerWidth-browserStatusGap-1))
	labelWidth := max(1, width-browserMarkerWidth-browserStatusGap-stateWidth)
	stateStyle := item.stateStyle
	if item.stale {
		stateStyle = warningStyle
	}
	label := labelStyle.Width(labelWidth).Render(clip(printable(item.destination.Label), labelWidth))
	state := stateStyle.Width(stateWidth).Render(clip(item.symbol+" "+item.state, stateWidth))
	row := markerStyle.Render(marker) + label + strings.Repeat(" ", browserStatusGap) + state
	return clip(row, width)
}

type keyMap struct {
	enter   key.Binding
	refresh key.Binding
	create  key.Binding
	stop    key.Binding
	restart key.Binding
	back    key.Binding
	help    key.Binding
	quit    key.Binding
	force   key.Binding
	confirm key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		create:  key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new Sprite")),
		stop:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stop")),
		restart: key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "restart")),
		back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "more")),
		quit:    key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		force:   key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
		confirm: key.NewBinding(key.WithKeys("y", "enter"), key.WithHelp("y", "confirm")),
	}
}

type inventoryDone struct {
	opID      uint64
	inventory catalog.Inventory
}

type spriteSessionsDone struct {
	opID       uint64
	endpointID catalog.DestinationID
	rows       []catalog.Destination
	err        error
}

type installSpriteHerdrDone struct {
	opID uint64
	err  error
}

type prepareDone struct {
	opID     uint64
	prepared catalog.PreparedAttach
	err      error
}

type stopDone struct {
	opID uint64
	err  error
}

type createDone struct {
	opID   uint64
	result spriteapi.CreateResult
}

type attachDone struct {
	opID uint64
	err  error
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

type refreshTick time.Time

type prepareOperation struct {
	destination catalog.Destination
	transport   catalog.SpriteTransport
	sprite      bool
}

type execProcessFunc func(*exec.Cmd, tea.ExecCallback) tea.Cmd
type execCommandFunc func(tea.ExecCommand, tea.ExecCallback) tea.Cmd

// Model is hrd's Bubble Tea model.
type Model struct {
	root        context.Context
	operations  Operations
	execProcess execProcessFunc
	execCommand execCommandFunc
	keys        keyMap

	screen screen
	phase  phase
	width  int
	height int

	browser   list.Model
	spinner   spinner.Model
	help      help.Model
	nameInput textinput.Model

	nextOpID      uint64
	operationID   uint64
	operationStop context.CancelFunc
	initialCmd    tea.Cmd
	quitting      bool

	localSnapshot     sourceSnapshot
	spriteSnapshot    sourceSnapshot
	destinations      []catalog.Destination
	selectedID        catalog.DestinationID
	endpointSelection catalog.DestinationID
	selectSprite      string

	spriteEndpoint          catalog.Destination
	spriteEndpointSelection catalog.SpriteEndpointSelection
	spriteDetailEndpointID  catalog.DestinationID
	spriteSnapshotDetail    sourceSnapshot

	notice                  string
	createNotice            string
	refreshNotice           string
	inventoryRefreshing     bool
	inventoryRefreshVisible bool
	createBlock             string
	showHelp                bool
	restart                 bool
	restartStopSucceeded    bool
	confirmDest             catalog.Destination
	activePrepare           prepareOperation
	prepared                catalog.PreparedAttach

	transport        catalog.SpriteTransport
	sshRecoveryTried bool
	sshKeySource     string
	sshKeyChoices    []catalog.SSHIdentity
	sshKeyCursor     int
}

// New creates a model and schedules the first inventory read.
func New(root context.Context, operations Operations) Model {
	if root == nil {
		panic("ui: nil root context")
	}
	if operations == nil {
		panic("ui: nil operations")
	}

	browser := list.New(nil, browserDelegate{}, defaultWidth-6, 12)
	browser.SetShowTitle(false)
	browser.SetShowStatusBar(false)
	browser.SetShowPagination(true)
	browser.SetShowHelp(false)
	browser.SetShowFilter(true)
	browser.DisableQuitKeybindings()
	browser.KeyMap.Quit.SetEnabled(false)
	browser.KeyMap.ForceQuit.SetEnabled(false)

	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(accentStyle))
	helpModel := help.New()
	helpStyles := help.DefaultDarkStyles()
	helpStyles.ShortKey = accentStyle.Bold(true)
	helpStyles.ShortDesc = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	helpStyles.ShortSeparator = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	helpStyles.FullKey = helpStyles.ShortKey
	helpStyles.FullDesc = helpStyles.ShortDesc
	helpStyles.FullSeparator = helpStyles.ShortSeparator
	helpModel.Styles = helpStyles
	name := textinput.New()
	name.Prompt = ""
	name.Placeholder = "lowercase-name"
	name.CharLimit = 63
	name.SetWidth(32)
	name.SetVirtualCursor(true)

	m := Model{
		root:        root,
		operations:  operations,
		execProcess: tea.ExecProcess,
		execCommand: tea.Exec,
		keys:        newKeyMap(),
		screen:      screenEndpoints,
		phase:       phaseLoading,
		width:       defaultWidth,
		height:      defaultHeight,
		browser:     browser,
		spinner:     spin,
		help:        helpModel,
		nameInput:   name,
	}
	m.initialCmd = m.beginInventory()
	return m
}

// Init starts inventory loading, animation, and silent background refresh.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.initialCmd, m.spinner.Tick, scheduleRefresh())
}

func scheduleRefresh() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return refreshTick(t) })
}

// Update advances navigation and provider effects.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.quitting {
		if done, ok := msg.(prepareDone); ok {
			closePrepared(done.prepared)
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
		m.resizeComponents()
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.busy() {
			return m, cmd
		}
		return m, nil
	case refreshTick:
		cmds := make([]tea.Cmd, 0, 2)
		if m.screen == screenEndpoints && m.phase == phaseReady && !m.inventoryRefreshing {
			cmds = append(cmds, m.beginSilentInventory())
		}
		cmds = append(cmds, scheduleRefresh())
		return m, tea.Batch(cmds...)
	case inventoryDone:
		return m.updateInventoryDone(msg)
	case spriteSessionsDone:
		return m.updateSpriteSessionsDone(msg)
	case installSpriteHerdrDone:
		return m.updateInstallSpriteHerdrDone(msg)
	case prepareDone:
		return m.updatePrepareDone(msg)
	case stopDone:
		return m.updateStopDone(msg)
	case createDone:
		return m.updateCreateDone(msg)
	case attachDone:
		return m.updateAttachDone(msg)
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
	textEditing := m.phase == phaseNamingSprite || (m.phase == phaseReady && m.browser.SettingFilter())
	if key.Matches(msg, m.keys.force) || (key.Matches(msg, m.keys.quit) && !textEditing) {
		m.cancelOperation()
		closePrepared(m.prepared)
		m.prepared = nil
		m.quitting = true
		return m, tea.Quit
	}

	switch m.phase {
	case phaseNamingSprite:
		return m.updateName(msg)
	case phaseChoosingSpriteTransport:
		return m.updateTransport(msg)
	case phaseConfirmingSpriteInstall:
		return m.updateSpriteInstallConfirmation(msg)
	case phaseConfirmingStop:
		return m.updateStopConfirmation(msg)
	case phaseConfirmingHostKey:
		if msg.String() == "enter" || msg.String() == "y" {
			cmd := m.beginAttach()
			return m, cmd
		}
		if msg.String() == "esc" || msg.String() == "n" {
			closePrepared(m.prepared)
			m.prepared = nil
			m.restartStopSucceeded = false
			cmd := m.beginSpriteRefresh()
			return m, cmd
		}
		return m, nil
	case phaseChoosingSSHKey:
		return m.updateSSHKeyChoice(msg)
	case phaseLoadingSprite, phaseRefreshingSprite, phaseInstallingSpriteHerdr:
		if msg.String() == "esc" {
			return m, m.returnToEndpoints()
		}
		return m, nil
	case phaseInitialLoadFailed:
		if key.Matches(msg, m.keys.refresh) {
			m.phase = phaseLoading
			m.refreshNotice = ""
			cmd := m.beginInventory()
			return m, tea.Batch(cmd, m.spinner.Tick)
		}
		return m, nil
	case phaseAddingSSHKey, phaseAttaching:
		return m, nil
	}

	if m.phase != phaseReady {
		return m, nil
	}
	if key.Matches(msg, m.keys.help) && !m.browser.SettingFilter() {
		m.showHelp = !m.showHelp
		m.help.ShowAll = m.showHelp
		return m, nil
	}
	if key.Matches(msg, m.keys.refresh) && !m.browser.SettingFilter() {
		m.refreshNotice = ""
		if m.screen == screenSprite {
			cmd := m.beginSpriteRefresh()
			return m, cmd
		}
		if m.inventoryRefreshing {
			m.inventoryRefreshVisible = true
			return m, m.spinner.Tick
		}
		cmd := m.beginInventory()
		return m, tea.Batch(cmd, m.spinner.Tick)
	}
	if m.screen == screenSprite && !m.browser.SettingFilter() {
		if key.Matches(msg, m.keys.back) {
			return m, m.returnToEndpoints()
		}
		if key.Matches(msg, m.keys.stop) {
			if destination, ok := m.selectedSpriteSessionForMutation(); ok {
				m.confirmDest = destination
				m.restart = false
				m.phase = phaseConfirmingStop
			}
			return m, nil
		}
		if key.Matches(msg, m.keys.restart) {
			if destination, ok := m.selectedSpriteSessionForMutation(); ok {
				m.confirmDest = destination
				m.restart = true
				m.phase = phaseConfirmingStop
			}
			return m, nil
		}
	}
	if m.screen == screenEndpoints && msg.String() == "n" && !m.browser.SettingFilter() {
		if _, ok := m.operations.(spriteOperations); ok && m.spriteSnapshot.current && m.createBlock == "" {
			m.startOperation()
			m.nameInput.Reset()
			m.nameInput.SetValue("")
			m.nameInput.Focus()
			m.notice = ""
			m.phase = phaseNamingSprite
		}
		return m, nil
	}
	if msg.String() == "enter" && !m.browser.SettingFilter() {
		destination, ok := m.selectedDestination()
		if !ok || !m.selectedCurrent() {
			return m, nil
		}
		m.notice = ""
		m.selectedID = destination.ID
		if m.screen == screenEndpoints {
			m.endpointSelection = destination.ID
			if _, ok := destination.Endpoint.(catalog.SpriteEndpoint); ok {
				selection, err := catalog.SelectSpriteEndpoint(destination)
				if err != nil {
					m.notice = prepareFailureNotice(err)
					return m, nil
				}
				m.spriteEndpoint = destination
				m.spriteEndpointSelection = selection
				m.spriteDetailEndpointID = destination.ID
				m.spriteSnapshot.current = false
				m.spriteSnapshotDetail = sourceSnapshot{}
				m.screen = screenSprite
				m.phase = phaseLoadingSprite
				m.browser.ResetFilter()
				m.browser.SetItems(nil)
				cmd := m.beginSpriteSessions()
				return m, tea.Batch(cmd, m.spinner.Tick)
			}
			cmd := m.beginPrepareLocal(destination)
			return m, tea.Batch(cmd, m.spinner.Tick)
		}
		m.confirmDest = destination
		m.restart = false
		m.transport = catalog.NativePreview
		m.phase = phaseChoosingSpriteTransport
		return m, nil
	}

	filterState := m.browser.FilterState()
	filterValue := m.browser.FilterValue()
	var cmd tea.Cmd
	m.browser, cmd = m.browser.Update(msg)
	m.refreshBrowserFilter(filterState, filterValue)
	if destination, ok := m.selectedDestination(); ok {
		m.selectedID = destination.ID
		if m.screen == screenEndpoints {
			m.endpointSelection = destination.ID
		}
	}
	return m, cmd
}

func (m Model) updateName(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.nameInput.Blur()
		m.phase = phaseReady
		return m, nil
	case "enter":
		name := m.nameInput.Value()
		if err := spriteapi.ValidName(name); err != nil {
			m.notice = printable(err.Error())
			return m, nil
		}
		m.nameInput.Blur()
		cmd := m.beginCreate(name)
		return m, tea.Batch(cmd, m.spinner.Tick)
	}
	var cmd tea.Cmd
	m.nameInput, cmd = m.nameInput.Update(msg)
	return m, cmd
}

func (m Model) updateTransport(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "down", "j", "k":
		if m.transport == catalog.NativePreview {
			m.transport = catalog.SSHFallback
		} else {
			m.transport = catalog.NativePreview
		}
		return m, nil
	case "esc":
		m.restart = false
		m.phase = phaseReady
		return m, nil
	case "enter":
		if m.restart {
			cmd := m.beginStop(m.confirmDest)
			return m, tea.Batch(cmd, m.spinner.Tick)
		}
		cmd := m.beginPrepareSprite(m.confirmDest, m.transport)
		return m, tea.Batch(cmd, m.spinner.Tick)
	default:
		return m, nil
	}
}

func (m Model) updateStopConfirmation(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		if m.restart {
			m.transport = catalog.NativePreview
			m.phase = phaseChoosingSpriteTransport
			return m, nil
		}
		cmd := m.beginStop(m.confirmDest)
		return m, tea.Batch(cmd, m.spinner.Tick)
	case "n", "esc":
		m.restart = false
		m.phase = phaseReady
	}
	return m, nil
}

func (m Model) updateSpriteInstallConfirmation(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		cmd := m.beginInstallSpriteHerdr()
		return m, tea.Batch(cmd, m.spinner.Tick)
	case "n", "esc":
		return m, m.returnToEndpoints()
	}
	return m, nil
}

func (m Model) updateInventoryDone(msg inventoryDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operationID || (m.phase != phaseLoading && !m.inventoryRefreshing) {
		return m, nil
	}
	wasInitial := m.phase == phaseLoading
	showFeedback := wasInitial || m.inventoryRefreshVisible
	m.cancelOperation()
	m.inventoryRefreshing = false
	m.inventoryRefreshVisible = false
	m.localSnapshot = refreshSnapshot(m.localSnapshot, msg.inventory.Local)
	m.spriteSnapshot = refreshSnapshot(m.spriteSnapshot, msg.inventory.Sprites)
	m.destinations = append(append([]catalog.Destination(nil), m.localSnapshot.rows...), m.spriteSnapshot.rows...)
	if showFeedback {
		m.refreshNotice = inventoryNotice(msg.inventory, wasInitial)
	}
	if wasInitial && !m.localSnapshot.loaded && !m.spriteSnapshot.loaded {
		m.phase = phaseInitialLoadFailed
		if m.refreshNotice == "" {
			m.refreshNotice = "No inventory source loaded."
		}
		return m, nil
	}
	m.reconcileCreateBlock()
	m.phase = phaseReady
	selected := m.selectedID
	if m.selectSprite != "" {
		for _, row := range m.destinations {
			if endpoint, ok := row.Endpoint.(catalog.SpriteEndpoint); ok && endpoint.Name == m.selectSprite {
				selected = row.ID
				break
			}
		}
		m.selectSprite = ""
	}
	m.syncEndpointItems(selected)
	return m, nil
}

func refreshSnapshot(previous sourceSnapshot, result catalog.SourceResult) sourceSnapshot {
	if result.Skipped {
		return sourceSnapshot{}
	}
	if result.Err != nil {
		previous.current = false
		return previous
	}
	return sourceSnapshot{rows: append([]catalog.Destination(nil), result.Rows...), loaded: true, current: true}
}

func inventoryNotice(inventory catalog.Inventory, initial bool) string {
	action := "refresh"
	if initial {
		action = "load"
	}
	var notices []string
	if inventory.Local.Err != nil {
		notices = append(notices, "Local "+action+" failed: "+printable(inventory.Local.Err.Error()))
	}
	if inventory.Sprites.Err != nil {
		notices = append(notices, "Sprite "+action+" failed: "+printable(inventory.Sprites.Err.Error()))
	}
	return strings.Join(notices, "; ")
}

func (m Model) updateSpriteSessionsDone(msg spriteSessionsDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operationID || msg.endpointID != m.spriteDetailEndpointID || (m.phase != phaseLoadingSprite && m.phase != phaseRefreshingSprite) {
		return m, nil
	}
	initial := m.phase == phaseLoadingSprite
	m.cancelOperation()
	if errors.Is(msg.err, catalog.ErrSpriteHerdrMissing) {
		m.spriteSnapshotDetail = sourceSnapshot{}
		m.refreshNotice = ""
		m.phase = phaseConfirmingSpriteInstall
		m.syncSpriteItems("")
		return m, nil
	}
	if msg.err != nil {
		m.spriteSnapshotDetail.current = false
		m.refreshNotice = "Sprite session refresh failed: " + printable(msg.err.Error())
	} else {
		m.spriteSnapshotDetail = sourceSnapshot{rows: append([]catalog.Destination(nil), msg.rows...), loaded: true, current: true}
		m.refreshNotice = ""
	}
	m.phase = phaseReady
	selected := m.selectedID
	if initial {
		selected = ""
	}
	m.syncSpriteItems(selected)
	return m, nil
}

func (m Model) updateInstallSpriteHerdrDone(msg installSpriteHerdrDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operationID || m.phase != phaseInstallingSpriteHerdr {
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil {
		m.notice = "Couldn’t install Herdr: " + printable(msg.err.Error())
		m.phase = phaseReady
		return m, nil
	}
	m.phase = phaseLoadingSprite
	cmd := m.beginSpriteSessions()
	return m, tea.Batch(cmd, m.spinner.Tick)
}

func (m Model) updateStopDone(msg stopDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operationID || m.phase != phaseStopping {
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil {
		m.restart = false
		m.notice = "Couldn’t stop session: " + printable(msg.err.Error())
		m.spriteSnapshotDetail.current = false
		m.syncSpriteItems(m.confirmDest.ID)
		cmd := m.beginSpriteRefresh()
		return m, cmd
	}
	m.spriteSnapshotDetail.current = false
	m.syncSpriteItems(m.confirmDest.ID)
	if m.restart {
		m.restart = false
		m.restartStopSucceeded = true
		cmd := m.beginPrepareSprite(m.confirmDest, m.transport)
		return m, tea.Batch(cmd, m.spinner.Tick)
	}
	cmd := m.beginSpriteRefresh()
	return m, cmd
}

func (m Model) updateCreateDone(msg createDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operationID || m.phase != phaseCreatingSprite {
		return m, nil
	}
	m.cancelOperation()
	result := msg.result
	if result.Confirmed && spriteapi.ValidName(result.Name) == nil {
		m.selectSprite = result.Name
		m.phase = phaseReady
		return m, m.beginSilentInventory()
	}
	if result.Rejected {
		m.phase = phaseNamingSprite
		m.nameInput.Focus()
		if result.Err == nil {
			m.notice = "Create failed."
		} else {
			m.notice = "Create failed: " + printable(result.Err.Error())
		}
		return m, nil
	}
	m.createBlock = result.Name
	m.createNotice = "Creation outcome is unknown. Reconcile " + printable(result.Name) + " before creating another Sprite."
	m.phase = phaseReady
	return m, m.beginSilentInventory()
}

func (m Model) updatePrepareDone(msg prepareDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operationID || m.phase != phasePreparing {
		closePrepared(msg.prepared)
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil {
		closePrepared(msg.prepared)
		if !m.restartStopSucceeded && m.activePrepare.sprite && m.activePrepare.transport == catalog.SSHFallback {
			if source, choices, ok := catalog.SSHKeyChoices(msg.err); ok {
				m.phase = phaseChoosingSSHKey
				m.sshKeySource = printable(source)
				m.sshKeyChoices = append([]catalog.SSHIdentity(nil), choices...)
				m.sshKeyCursor = 0
				return m, nil
			}
			if catalog.NeedsSSHKey(msg.err) && !m.sshRecoveryTried {
				cmd := m.beginSSHKeyRecovery()
				return m, cmd
			}
		}
		m.notice = prepareFailureNotice(msg.err)
		if m.restartStopSucceeded {
			m.restartStopSucceeded = false
			cmd := m.beginSpriteRefresh()
			return m, cmd
		}
		m.phase = phaseReady
		return m, nil
	}
	if msg.prepared == nil {
		m.notice = "Couldn’t prepare destination: no attachment was returned."
		if m.restartStopSucceeded {
			m.restartStopSucceeded = false
			cmd := m.beginSpriteRefresh()
			return m, cmd
		}
		m.phase = phaseReady
		return m, nil
	}
	m.prepared = msg.prepared
	switch prepared := msg.prepared.(type) {
	case catalog.PreparedProcessAttach:
		if prepared.Fingerprint != "" {
			m.phase = phaseConfirmingHostKey
			return m, nil
		}
		cmd := m.beginProcessAttach(prepared)
		return m, cmd
	case catalog.PreparedNativeAttach:
		if prepared.Command == nil {
			closePrepared(m.prepared)
			m.prepared = nil
			m.notice = "Couldn’t prepare destination: no native command was returned."
			if m.restartStopSucceeded {
				m.restartStopSucceeded = false
				cmd := m.beginSpriteRefresh()
				return m, cmd
			}
			m.phase = phaseReady
			return m, nil
		}
		cmd := m.beginNativeAttach(prepared)
		return m, cmd
	default:
		closePrepared(m.prepared)
		m.prepared = nil
		m.notice = "Couldn’t prepare destination: unknown attachment type."
		if m.restartStopSucceeded {
			m.restartStopSucceeded = false
			cmd := m.beginSpriteRefresh()
			return m, cmd
		}
		m.phase = phaseReady
		return m, nil
	}
}

func (m Model) updateAttachDone(msg attachDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operationID || m.phase != phaseAttaching {
		return m, nil
	}
	closePrepared(m.prepared)
	m.prepared = nil
	if msg.err != nil {
		m.notice = "Attach failed: " + printable(msg.err.Error())
	} else {
		m.notice = ""
	}
	if m.activePrepare.sprite {
		m.restartStopSucceeded = false
		cmd := m.beginSpriteRefresh()
		return m, cmd
	}
	m.phase = phaseReady
	return m, m.beginSilentInventory()
}

func (m Model) updateSSHAddReady(msg sshAddReady) (tea.Model, tea.Cmd) {
	if msg.opID != m.operationID || m.phase != phaseStartingSSHAgent {
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil || msg.command == nil {
		if msg.err == nil {
			msg.err = fmt.Errorf("no ssh-add command")
		}
		m.notice = "Couldn’t start ssh-add: " + printable(msg.err.Error())
		m.phase = phaseReady
		return m, nil
	}
	m.phase = phaseAddingSSHKey
	opID := msg.opID
	return m, m.execProcess(msg.command, func(err error) tea.Msg { return sshAddDone{opID: opID, err: err} })
}

func (m Model) updateSSHAddDone(msg sshAddDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operationID || m.phase != phaseAddingSSHKey {
		return m, nil
	}
	if msg.err != nil {
		m.notice = "ssh-add did not load a key: " + printable(msg.err.Error())
		m.phase = phaseReady
		return m, nil
	}
	cmd := m.retrySpritePrepare()
	return m, tea.Batch(cmd, m.spinner.Tick)
}

func (m Model) updateSSHKeyChoice(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		m.sshKeyCursor = max(0, m.sshKeyCursor-1)
	case "down", "j":
		m.sshKeyCursor = min(max(0, len(m.sshKeyChoices)-1), m.sshKeyCursor+1)
	case "esc":
		m.sshKeyChoices = nil
		m.phase = phaseReady
	case "enter":
		if m.sshKeyCursor < 0 || m.sshKeyCursor >= len(m.sshKeyChoices) {
			return m, nil
		}
		operations, ok := m.operations.(sshKeyOperations)
		if !ok {
			m.notice = "Couldn’t select an SSH key."
			return m, nil
		}
		if err := operations.SelectSSHKey(m.sshKeyChoices[m.sshKeyCursor].Fingerprint); err != nil {
			m.notice = "Couldn’t select SSH key: " + printable(err.Error())
			return m, nil
		}
		m.sshKeyChoices = nil
		cmd := m.retrySpritePrepare()
		return m, tea.Batch(cmd, m.spinner.Tick)
	}
	return m, nil
}

func (m *Model) returnToEndpoints() tea.Cmd {
	m.screen = screenEndpoints
	m.phase = phaseReady
	m.browser.ResetFilter()
	m.syncEndpointItems(m.endpointSelection)
	if !m.spriteSnapshot.loaded || m.spriteSnapshot.current {
		return nil
	}
	return m.beginSilentInventory()
}

func (m *Model) beginInventory() tea.Cmd {
	return m.beginInventoryWithFeedback(true)
}

func (m *Model) beginSilentInventory() tea.Cmd {
	return m.beginInventoryWithFeedback(false)
}

func (m *Model) beginInventoryWithFeedback(visible bool) tea.Cmd {
	m.startOperation()
	if m.phase != phaseLoading {
		m.inventoryRefreshing = true
		m.inventoryRefreshVisible = visible
	}
	opID := m.operationID
	ctx := m.operationContext()
	operations := m.operations
	return func() tea.Msg {
		return inventoryDone{opID: opID, inventory: operations.Inventory(ctx)}
	}
}

func (m *Model) beginSpriteSessions() tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	m.startOperation()
	opID := m.operationID
	ctx := m.operationContext()
	endpointID := m.spriteDetailEndpointID
	selection := m.spriteEndpointSelection
	return func() tea.Msg {
		rows, err := operations.ListSpriteSessions(ctx, selection)
		return spriteSessionsDone{opID: opID, endpointID: endpointID, rows: append([]catalog.Destination(nil), rows...), err: err}
	}
}

func (m *Model) beginInstallSpriteHerdr() tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	m.startOperation()
	m.phase = phaseInstallingSpriteHerdr
	opID := m.operationID
	ctx := m.operationContext()
	selection := m.spriteEndpointSelection
	return func() tea.Msg {
		return installSpriteHerdrDone{opID: opID, err: operations.InstallSpriteHerdr(ctx, selection)}
	}
}

func (m *Model) beginSpriteRefresh() tea.Cmd {
	m.phase = phaseRefreshingSprite
	cmd := m.beginSpriteSessions()
	return tea.Batch(cmd, m.spinner.Tick)
}

func (m *Model) beginCreate(name string) tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	m.startOperation()
	m.phase = phaseCreatingSprite
	opID := m.operationID
	ctx := m.operationContext()
	return func() tea.Msg { return createDone{opID: opID, result: operations.Create(ctx, name)} }
}

func (m *Model) beginStop(destination catalog.Destination) tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	selection, err := catalog.SelectSpriteSession(destination)
	if err != nil {
		m.notice = "Couldn’t stop session: " + printable(err.Error())
		m.phase = phaseReady
		return nil
	}
	m.startOperation()
	m.phase = phaseStopping
	opID := m.operationID
	ctx := m.operationContext()
	return func() tea.Msg { return stopDone{opID: opID, err: operations.StopSpriteSession(ctx, selection)} }
}

func (m *Model) beginPrepareLocal(destination catalog.Destination) tea.Cmd {
	m.startOperation()
	m.phase = phasePreparing
	m.activePrepare = prepareOperation{destination: destination}
	opID := m.operationID
	ctx := m.operationContext()
	operations := m.operations
	return func() tea.Msg {
		prepared, err := operations.Prepare(ctx, destination)
		return prepareDone{opID: opID, prepared: prepared, err: err}
	}
}

func (m *Model) beginPrepareSprite(destination catalog.Destination, transport catalog.SpriteTransport) tea.Cmd {
	m.sshRecoveryTried = false
	return m.startPrepareSprite(destination, transport)
}

func (m *Model) startPrepareSprite(destination catalog.Destination, transport catalog.SpriteTransport) tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	selection, err := catalog.SelectSpriteSession(destination)
	if err != nil {
		m.notice = prepareFailureNotice(err)
		m.phase = phaseReady
		return nil
	}
	m.startOperation()
	m.phase = phasePreparing
	m.activePrepare = prepareOperation{destination: destination, transport: transport, sprite: true}
	opID := m.operationID
	ctx := m.operationContext()
	return func() tea.Msg {
		prepared, err := operations.PrepareSprite(ctx, selection, transport)
		return prepareDone{opID: opID, prepared: prepared, err: err}
	}
}

func (m *Model) beginSSHKeyRecovery() tea.Cmd {
	operations, ok := m.operations.(sshKeyOperations)
	if !ok {
		m.notice = "Couldn’t start ssh-add."
		m.phase = phaseReady
		return nil
	}
	m.sshRecoveryTried = true
	m.startOperation()
	m.phase = phaseStartingSSHAgent
	opID := m.operationID
	ctx := m.operationContext()
	return func() tea.Msg {
		command, err := operations.SSHAddCommand(ctx)
		return sshAddReady{opID: opID, command: command, err: err}
	}
}

func (m *Model) retrySpritePrepare() tea.Cmd {
	return m.startPrepareSprite(m.activePrepare.destination, catalog.SSHFallback)
}

func (m *Model) beginAttach() tea.Cmd {
	prepared, ok := m.prepared.(catalog.PreparedProcessAttach)
	if !ok {
		return nil
	}
	return m.beginProcessAttach(prepared)
}

func (m *Model) beginProcessAttach(prepared catalog.PreparedProcessAttach) tea.Cmd {
	m.invalidateSpriteAttachObservations()
	command := exec.CommandContext(m.root, "herdr", prepared.HerdrArgs()...)
	environment := withoutEnvironment(os.Environ(), "SPRITE_TOKEN")
	if socket := prepared.SSHAuthSocket(); socket != "" {
		environment = withEnvironment(environment, "SSH_AUTH_SOCK", socket)
	}
	command.Env = environment
	m.phase = phaseAttaching
	opID := m.operationID
	return m.execProcess(command, func(err error) tea.Msg { return attachDone{opID: opID, err: err} })
}

func (m *Model) beginNativeAttach(prepared catalog.PreparedNativeAttach) tea.Cmd {
	m.invalidateSpriteAttachObservations()
	m.phase = phaseAttaching
	opID := m.operationID
	return m.execCommand(prepared.Command, func(err error) tea.Msg { return attachDone{opID: opID, err: err} })
}

func (m *Model) invalidateSpriteAttachObservations() {
	if !m.activePrepare.sprite {
		return
	}
	m.spriteSnapshot.current = false
	m.spriteSnapshotDetail.current = false
	m.syncSpriteItems(m.selectedID)
}

func (m *Model) startOperation() {
	m.cancelOperation()
	m.inventoryRefreshing = false
	m.inventoryRefreshVisible = false
	m.operationID = m.nextID()
}

func (m *Model) operationContext() context.Context {
	ctx, cancel := context.WithCancel(m.root)
	m.operationStop = cancel
	return ctx
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

func (m Model) selectedDestination() (catalog.Destination, bool) {
	item, ok := m.browser.SelectedItem().(browserItem)
	if !ok {
		return catalog.Destination{}, false
	}
	return item.destination, true
}

func (m Model) selectedCurrent() bool {
	item, ok := m.browser.SelectedItem().(browserItem)
	return ok && !item.stale
}

func (m Model) selectedSpriteSessionForMutation() (catalog.Destination, bool) {
	destination, ok := m.selectedDestination()
	if !ok || !m.spriteSnapshotDetail.current {
		return catalog.Destination{}, false
	}
	status, ok := destination.Status.(catalog.SpriteSessionStatus)
	return destination, ok && status.Running
}

func (m *Model) syncEndpointItems(selected catalog.DestinationID) {
	items := make([]list.Item, 0, len(m.destinations))
	for _, destination := range m.destinations {
		items = append(items, m.endpointBrowserItem(destination, !m.destinationCurrent(destination)))
	}
	m.replaceBrowserItems(items)
	m.selectListID(selected)
	m.endpointSelection = m.selectedID
	m.resizeComponents()
}

func (m *Model) syncSpriteItems(selected catalog.DestinationID) {
	items := make([]list.Item, 0, len(m.spriteSnapshotDetail.rows))
	for _, destination := range m.spriteSnapshotDetail.rows {
		status, _ := destination.Status.(catalog.SpriteSessionStatus)
		item := browserItem{
			destination: destination,
			kind:        "Session",
			symbol:      "○",
			state:       "stopped",
			stateStyle:  mutedStyle,
			stale:       !m.spriteSnapshotDetail.current,
		}
		if status.Running {
			item.symbol = "●"
			item.state = "running"
			item.stateStyle = runningStyle
		}
		if item.stale {
			item.symbol = "!"
			item.state = "stale"
		}
		items = append(items, item)
	}
	m.replaceBrowserItems(items)
	m.selectListID(selected)
	m.resizeComponents()
}

func (m *Model) replaceBrowserItems(items []list.Item) {
	state := m.browser.FilterState()
	query := m.browser.FilterValue()
	m.browser.SetItems(items)
	if state != list.Unfiltered {
		m.browser.SetFilterText(query)
		if state == list.Filtering {
			m.browser.SetFilterState(state)
		}
	}
}

func (m *Model) refreshBrowserFilter(previousState list.FilterState, previousValue string) {
	state := m.browser.FilterState()
	value := m.browser.FilterValue()
	if state != list.Filtering || (state == previousState && value == previousValue) {
		return
	}
	m.browser.SetFilterText(value)
	m.browser.SetFilterState(state)
}

func (m *Model) selectListID(id catalog.DestinationID) {
	if id != "" {
		for index, raw := range m.browser.VisibleItems() {
			if item, ok := raw.(browserItem); ok && item.destination.ID == id {
				m.browser.Select(index)
				m.selectedID = id
				return
			}
		}
	}
	m.browser.Select(0)
	if destination, ok := m.selectedDestination(); ok {
		m.selectedID = destination.ID
	} else {
		m.selectedID = ""
	}
}

func (m Model) destinationCurrent(destination catalog.Destination) bool {
	switch destination.Endpoint.(type) {
	case catalog.LocalEndpoint:
		return m.localSnapshot.current
	case catalog.SpriteEndpoint:
		return m.spriteSnapshot.current
	default:
		return false
	}
}

func (m Model) endpointBrowserItem(destination catalog.Destination, stale bool) browserItem {
	item := browserItem{destination: destination, symbol: "?", state: "unknown", stateStyle: mutedStyle, stale: stale}
	switch status := destination.Status.(type) {
	case catalog.LocalSessionStatus:
		item.kind = "Local"
		item.symbol = "○"
		item.state = "stopped"
		if status.Running {
			item.symbol = "●"
			item.state = "running"
			item.stateStyle = runningStyle
		}
	case catalog.SpriteStatus:
		item.kind = "Sprite"
		switch status.Lifecycle {
		case spriteapi.Running:
			item.symbol = "●"
			item.state = "running"
			item.stateStyle = runningStyle
		case spriteapi.Warm:
			item.symbol = "◐"
			item.state = "warm"
			item.stateStyle = warningStyle
		case spriteapi.Cold:
			item.symbol = "○"
			item.state = "cold"
		}
	}
	if stale {
		item.symbol = "!"
		item.state = "stale"
	}
	return item
}

func (m *Model) reconcileCreateBlock() {
	if m.createBlock == "" || !m.spriteSnapshot.current {
		return
	}
	for _, destination := range m.spriteSnapshot.rows {
		if endpoint, ok := destination.Endpoint.(catalog.SpriteEndpoint); ok && endpoint.Name == m.createBlock {
			m.createBlock = ""
			m.createNotice = ""
			return
		}
	}
}

func (m Model) busy() bool {
	if m.inventoryRefreshing && m.inventoryRefreshVisible {
		return true
	}
	switch m.phase {
	case phaseLoading, phaseCreatingSprite, phaseLoadingSprite, phaseRefreshingSprite,
		phaseInstallingSpriteHerdr, phaseStopping, phasePreparing, phaseStartingSSHAgent:
		return true
	default:
		return false
	}
}

var (
	accentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	titleStyle    = lipgloss.NewStyle().Bold(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	runningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	warningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	borderStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// View renders a compact surface centered when the terminal has room.
func (m Model) View() tea.View {
	surfaceWidth := m.surfaceWidth()
	m.help.SetWidth(surfaceWidth)

	lines := []string{m.breadcrumbView()}

	footer := m.helpView()
	footerHeight := lipgloss.Height(footer)
	bodyBudget := max(1, m.height-len(lines)-footerHeight-3)
	if body := m.bodyView(surfaceWidth, bodyBudget); body != "" {
		lines = append(lines, "", body)
	}
	if footer != "" {
		lines = append(lines, "", footer)
	}
	surface := fitView(strings.Join(lines, "\n"), surfaceWidth, m.height)
	content := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, surface)
	if toast := m.toastView(); toast != "" {
		canvas := lipgloss.NewCanvas(m.width, m.height)
		toastY := max(0, min(1, m.height-lipgloss.Height(toast)))
		layers := lipgloss.NewCompositor(
			lipgloss.NewLayer(content),
			lipgloss.NewLayer(toast).X(max(0, (m.width-lipgloss.Width(toast))/2)).Y(toastY).Z(1),
		)
		canvas.Compose(layers)
		content = canvas.Render()
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "hrd · Herdr sessions"
	return view
}

func (m Model) breadcrumbView() string {
	segments := m.breadcrumbSegments()
	if len(segments) == 0 {
		return titleStyle.Render("hrd")
	}
	prefix := titleStyle.Render("hrd") + "  "
	available := max(1, m.surfaceWidth()-lipgloss.Width(prefix))
	context := "/ " + strings.Join(segments, " / ")
	for lipgloss.Width(context) > available && len(segments) > 1 {
		if segments[0] != "…" {
			segments = append([]string{"…"}, segments[1:]...)
		} else if len(segments) > 2 {
			segments = append(segments[:1], segments[2:]...)
		} else {
			segments = segments[1:]
		}
		context = "/ " + strings.Join(segments, " / ")
	}
	return prefix + mutedStyle.Render(clip(context, available))
}

func (m Model) breadcrumbSegments() []string {
	segments := make([]string, 0, 2)
	if m.screen == screenSprite {
		segments = append(segments, m.spriteName())
	}

	switch m.phase {
	case phaseConfirmingStop, phaseStopping, phaseChoosingSpriteTransport:
		if m.confirmDest.Label != "" {
			segments = append(segments, printable(m.confirmDest.Label))
		}
	case phasePreparing, phaseConfirmingHostKey, phaseStartingSSHAgent, phaseAddingSSHKey, phaseChoosingSSHKey, phaseAttaching:
		if m.activePrepare.destination.Label != "" {
			segments = append(segments, printable(m.activePrepare.destination.Label))
		}
	}
	return segments
}

func (m Model) bodyView(width, height int) string {
	switch m.phase {
	case phaseLoading:
		return m.busyLine("Loading local sessions and Sprite endpoints…", width)
	case phaseInitialLoadFailed:
		return clip("No inventory source loaded. Press r to retry.", width)
	case phaseNamingSprite:
		return fitView(strings.Join([]string{
			"Name your Sprite",
			m.nameInput.View(),
			mutedStyle.Render("1–63 lowercase letters, digits, and interior hyphens"),
			warningStyle.Render("Creating and waking a Sprite may incur charges."),
		}, "\n"), width, height)
	case phaseCreatingSprite:
		return m.busyLine("Creating "+m.nameInput.Value()+"…", width)
	case phaseLoadingSprite:
		return m.busyLine("Waking "+m.spriteName()+" and checking Herdr… Up to three minutes.", width)
	case phaseConfirmingSpriteInstall:
		return fitView(strings.Join([]string{
			titleStyle.Render("Install Herdr on " + m.spriteName() + "?"),
			"hrd will download and run this installer inside the Sprite:",
			accentStyle.Render("https://herdr.dev/install.sh"),
		}, "\n"), width, height)
	case phaseInstallingSpriteHerdr:
		return m.busyLine("Installing Herdr on "+m.spriteName()+"… Up to three minutes.", width)
	case phaseRefreshingSprite:
		return m.listView(width, height)
	case phaseConfirmingStop:
		action := "Stop"
		if m.restart {
			action = "Restart"
		}
		return fitView(strings.Join([]string{
			titleStyle.Render(action + " " + printable(m.confirmDest.Label) + "?"),
			warningStyle.Render("Pane processes in this session will exit."),
		}, "\n"), width, height)
	case phaseStopping:
		action := "Stopping "
		if m.restart {
			action = "Stopping before restart: "
		}
		return m.listWithWork(action+printable(m.confirmDest.Label)+"…", width, height)
	case phaseChoosingSpriteTransport:
		return fitView(m.transportView(width), width, height)
	case phasePreparing:
		if m.activePrepare.sprite {
			if m.activePrepare.transport == catalog.SSHFallback {
				return m.busyLine("Setting up SSH on "+m.spriteName()+"… Up to three minutes.", width)
			}
			return fitView(m.transportView(width), width, height)
		}
		return m.busyLine("Preparing local session "+printable(m.activePrepare.destination.Label)+"…", width)
	case phaseConfirmingHostKey:
		prepared, _ := m.prepared.(catalog.PreparedProcessAttach)
		return fitView(strings.Join([]string{
			titleStyle.Render("Verify SSH host key"),
			"Authenticated through the Sprites API:",
			accentStyle.Render(printable(prepared.Fingerprint)),
			"Continue only if OpenSSH shows this fingerprint.",
		}, "\n"), width, height)
	case phaseStartingSSHAgent:
		return m.busyLine("Starting a private ssh-agent…", width)
	case phaseAddingSSHKey:
		return fitView("ssh-add has the terminal. Complete its prompt to continue.", width, height)
	case phaseChoosingSSHKey:
		return fitView(m.sshKeyView(width), width, height)
	case phaseAttaching:
		if m.activePrepare.sprite && m.activePrepare.transport == catalog.NativePreview {
			return m.spriteReadyView(width, height)
		}
		return "Herdr has the terminal."
	case phaseReady:
		if m.screen == screenSprite {
			return m.spriteReadyView(width, height)
		}
		if len(m.browser.Items()) == 0 {
			return "No local sessions or Sprite endpoints found."
		}
		return m.listView(width, height)
	default:
		return "Unknown UI state."
	}
}

func (m Model) toastView() string {
	notices := make([]string, 0, 3)
	for _, notice := range []string{m.notice, m.createNotice, m.refreshNotice} {
		if notice == "" {
			continue
		}
		style := warningStyle
		lower := strings.ToLower(notice)
		if strings.Contains(lower, "failed") || strings.Contains(lower, "couldn’t") {
			style = errorStyle
		}
		notices = append(notices, style.Render("! "+printable(notice)))
	}
	progress := ""
	if m.inventoryRefreshing && m.inventoryRefreshVisible {
		progress = m.spinner.View() + " " + accentStyle.Render("Refreshing local and Sprite inventory…")
	} else if m.phase == phaseRefreshingSprite {
		progress = m.spinner.View() + " " + accentStyle.Render("Refreshing "+m.spriteName()+" sessions…")
	}
	messages := append([]string(nil), notices...)
	if progress != "" {
		messages = append(messages, progress)
	}
	if len(messages) == 0 {
		return ""
	}

	width := max(12, min(72, m.width-4))
	contentWidth := max(1, width-4)
	for index, message := range messages {
		messages[index] = clip(message, contentWidth)
	}
	toast := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderStyle.GetForeground()).
		Background(lipgloss.Color("235")).
		Padding(0, 1).
		Width(width).
		Render(strings.Join(messages, "\n"))
	if lipgloss.Height(toast) <= m.height {
		return toast
	}
	return lipgloss.NewStyle().
		Background(lipgloss.Color("235")).
		Width(max(1, m.width)).
		Render(clip(messages[0], max(1, m.width)))
}

func (m Model) busyLine(text string, width int) string {
	return clip(m.spinner.View()+" "+accentStyle.Render(printable(text)), width)
}

func (m Model) listWithWork(text string, width, height int) string {
	work := m.busyLine(text, width)
	if height <= 2 || len(m.browser.Items()) == 0 {
		return work
	}
	return work + "\n" + m.listView(width, height-1)
}

func (m Model) listView(width, height int) string {
	panelWidth := m.browserPanelWidth(width)
	centerPanel := func(panel string) string {
		return lipgloss.PlaceHorizontal(width, lipgloss.Center, panel)
	}
	filter := ""
	if m.browser.FilterState() != list.Unfiltered {
		filter = "Filter  " + m.browser.FilterInput.View()
	}
	visible := m.browser.VisibleItems()
	selected := m.browser.Index()
	panelCount := 1
	if m.screen == screenEndpoints {
		panelCount = 0
		hasLocal := m.browser.FilterState() == list.Unfiltered && m.localSnapshot.loaded
		hasSprites := m.browser.FilterState() == list.Unfiltered && m.spriteSnapshot.loaded
		for _, raw := range visible {
			item, ok := raw.(browserItem)
			if !ok {
				continue
			}
			switch item.destination.Endpoint.(type) {
			case catalog.LocalEndpoint:
				hasLocal = true
			case catalog.SpriteEndpoint:
				hasSprites = true
			}
		}
		if hasLocal {
			panelCount++
		}
		if hasSprites {
			panelCount++
		}
	}
	layoutOverhead := func(showPage bool) int {
		parts := panelCount
		overhead := panelCount * 2
		if filter != "" {
			parts++
			overhead++
		}
		if showPage {
			parts++
			overhead++
		}
		return overhead + max(0, parts-1)
	}
	maxRows := max(1, height-layoutOverhead(false))
	if len(visible) > maxRows {
		maxRows = max(1, height-layoutOverhead(true))
	}
	start := 0
	if len(visible) > maxRows {
		start = max(0, min(selected-maxRows/2, len(visible)-maxRows))
	}
	end := min(len(visible), start+maxRows)

	type indexedItem struct {
		item  browserItem
		index int
	}
	local := make([]indexedItem, 0)
	sprites := make([]indexedItem, 0)
	sessions := make([]indexedItem, 0)
	for index := start; index < end; index++ {
		item, ok := visible[index].(browserItem)
		if !ok {
			continue
		}
		entry := indexedItem{item: item, index: index}
		switch item.destination.Endpoint.(type) {
		case catalog.LocalEndpoint:
			local = append(local, entry)
		case catalog.SpriteEndpoint:
			if m.screen == screenSprite {
				sessions = append(sessions, entry)
			} else {
				sprites = append(sprites, entry)
			}
		}
	}

	renderEntries := func(entries []indexedItem) []renderedBrowserItem {
		rows := make([]renderedBrowserItem, len(entries))
		for i, entry := range entries {
			rows[i] = renderedBrowserItem{item: entry.item, selected: entry.index == selected}
		}
		return rows
	}
	parts := make([]string, 0, 4)
	if filter != "" {
		parts = append(parts, centerPanel(lipgloss.NewStyle().Width(panelWidth).Render(clip(filter, panelWidth))))
	}
	if m.screen == screenSprite {
		badge := fmt.Sprintf("%d", len(m.spriteSnapshotDetail.rows))
		parts = append(parts, centerPanel(renderBrowserPanel(m.spriteName()+" sessions", badge, renderEntries(sessions), panelWidth, "No Herdr sessions found.")))
	} else {
		if len(local) > 0 || (m.browser.FilterState() == list.Unfiltered && m.localSnapshot.loaded) {
			badge := fmt.Sprintf("%d", len(m.localSnapshot.rows))
			if !m.localSnapshot.current {
				badge = "refresh failed · " + badge
			}
			parts = append(parts, centerPanel(renderBrowserPanel("Local sessions", badge, renderEntries(local), panelWidth, "No local sessions.")))
		}
		if len(sprites) > 0 || (m.browser.FilterState() == list.Unfiltered && m.spriteSnapshot.loaded) {
			badge := fmt.Sprintf("%d", len(m.spriteSnapshot.rows))
			if !m.spriteSnapshot.current {
				badge = "refresh failed · " + badge
			}
			parts = append(parts, centerPanel(renderBrowserPanel("Sprites", badge, renderEntries(sprites), panelWidth, "No Sprites.")))
		}
	}
	if start > 0 || end < len(visible) {
		page := lipgloss.NewStyle().Width(panelWidth).Render(mutedStyle.Render(fmt.Sprintf("Showing %d–%d of %d", start+1, end, len(visible))))
		parts = append(parts, centerPanel(page))
	}
	return fitView(strings.Join(parts, "\n\n"), width, height)
}

type renderedBrowserItem struct {
	item     browserItem
	selected bool
}

func renderBrowserPanel(title, badge string, rows []renderedBrowserItem, width int, empty string) string {
	width = max(12, width)
	innerWidth := width - 2
	badgeText := ""
	if badge != "" {
		badgeText = " " + printable(badge) + " "
	}
	maxTitleWidth := max(1, width-lipgloss.Width(badgeText)-7)
	titleText := " " + clip(printable(title), maxTitleWidth) + " "
	fill := max(1, innerWidth-lipgloss.Width(titleText)-lipgloss.Width(badgeText)-2)
	top := borderStyle.Render("╭─") + titleStyle.Render(titleText) + borderStyle.Render(strings.Repeat("─", fill))
	if badgeText != "" {
		top += mutedStyle.Render(badgeText)
	}
	top += borderStyle.Render("─╮")

	body := make([]string, 0, max(1, len(rows)))
	if len(rows) == 0 {
		line := mutedStyle.Render(empty)
		body = append(body, borderStyle.Render("│")+" "+lipgloss.NewStyle().Width(innerWidth-2).Render(line)+" "+borderStyle.Render("│"))
	} else {
		for _, row := range rows {
			line := renderBrowserRow(row.item, row.selected, innerWidth-2)
			body = append(body, borderStyle.Render("│")+" "+lipgloss.NewStyle().Width(innerWidth-2).Render(line)+" "+borderStyle.Render("│"))
		}
	}
	bottom := borderStyle.Render("╰" + strings.Repeat("─", innerWidth) + "╯")
	return strings.Join(append(append([]string{top}, body...), bottom), "\n")
}

func (m Model) spriteReadyView(width, height int) string {
	return m.listView(width, height)
}

func (m Model) transportView(width int) string {
	type transportOption struct {
		transport catalog.SpriteTransport
		title     string
		badge     string
		detail    string
	}
	options := []transportOption{
		{
			transport: catalog.NativePreview,
			title:     "Native",
			badge:     "recommended",
			detail:    "Herdr runs inside the Sprite. No SSH service or key.",
		},
		{
			transport: catalog.SSHFallback,
			title:     "SSH",
			detail:    "Local Herdr connects through your selected SSH key.",
		},
	}
	chooserWidth := max(12, min(68, width))
	rows := make([]string, 0, len(options)*3)
	for index, option := range options {
		if index > 0 {
			rows = append(rows, borderStyle.Render("├"+strings.Repeat("─", chooserWidth-2)+"┤"))
		}
		rows = append(rows, renderTransportOption(option.title, option.badge, option.detail, option.transport == m.transport, chooserWidth))
	}
	panel := borderStyle.Render("╭"+strings.Repeat("─", chooserWidth-2)+"╮") + "\n" +
		strings.Join(rows, "\n") + "\n" +
		borderStyle.Render("╰"+strings.Repeat("─", chooserWidth-2)+"╯")
	heading := "Choose a connection"
	if m.restart {
		heading = "Choose a connection for the restarted session"
	}
	content := strings.Join([]string{
		titleStyle.Render(heading),
		mutedStyle.Render("Select where Herdr itself should run."),
		"",
		panel,
	}, "\n")
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, content)
}

func renderTransportOption(title, badge, detail string, selected bool, width int) string {
	contentWidth := max(1, width-4)
	marker := "  "
	markerStyle := lipgloss.NewStyle()
	titleStyleForOption := lipgloss.NewStyle()
	if selected {
		marker = "› "
		markerStyle = accentStyle
		titleStyleForOption = selectedStyle
	}
	badgeStyle := mutedStyle
	detailStyle := mutedStyle

	badgeWidth := lipgloss.Width(badge)
	minimumTitleWidth := min(lipgloss.Width(title), max(1, contentWidth-browserMarkerWidth))
	if badgeWidth > contentWidth-browserMarkerWidth-minimumTitleWidth {
		badge = ""
		badgeWidth = 0
	}
	titleWidth := max(1, contentWidth-browserMarkerWidth-badgeWidth)
	first := markerStyle.Render(marker) + titleStyleForOption.Width(titleWidth).Render(clip(title, titleWidth))
	if badge != "" {
		first += badgeStyle.Render(badge)
	}
	detailWidth := max(1, contentWidth-browserMarkerWidth)
	second := markerStyle.Render(strings.Repeat(" ", browserMarkerWidth)) + detailStyle.Width(detailWidth).Render(clip(detail, detailWidth))
	return borderStyle.Render("│") + " " + first + " " + borderStyle.Render("│") + "\n" +
		borderStyle.Render("│") + " " + second + " " + borderStyle.Render("│")
}

func (m Model) sshKeyView(width int) string {
	source := m.sshKeySource
	if source == "" {
		source = "ssh-agent"
	}
	lines := []string{fmt.Sprintf("%s exposes %d keys. Choose exactly one.", printable(source), len(m.sshKeyChoices))}
	for index, choice := range m.sshKeyChoices {
		selected := index == m.sshKeyCursor
		marker := "  "
		markerStyle := lipgloss.NewStyle()
		labelStyle := lipgloss.NewStyle()
		if selected {
			marker = "› "
			markerStyle = accentStyle
			labelStyle = selectedStyle
		}
		label := printable(choice.Comment)
		if label == "" {
			label = "Unnamed key"
		}
		algorithm := printable(choice.Algorithm)
		algorithmWidth := min(lipgloss.Width(algorithm), max(1, width-browserMarkerWidth-browserStatusGap-1))
		labelWidth := max(1, width-browserMarkerWidth-browserStatusGap-algorithmWidth)
		row := markerStyle.Render(marker) +
			labelStyle.Width(labelWidth).Render(clip(label, labelWidth)) +
			strings.Repeat(" ", browserStatusGap) +
			mutedStyle.Width(algorithmWidth).Render(clip(algorithm, algorithmWidth))
		lines = append(lines, row)
	}
	if m.sshKeyCursor >= 0 && m.sshKeyCursor < len(m.sshKeyChoices) {
		lines = append(lines, mutedStyle.Render("Fingerprint ")+accentStyle.Render(printable(m.sshKeyChoices[m.sshKeyCursor].Fingerprint)))
	}
	return strings.Join(lines, "\n")
}

func (m Model) helpView() string {
	var short []key.Binding
	var full [][]key.Binding
	movement := key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑/↓", "move"))
	filtering := key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter"))
	escapeTo := func(destination string) key.Binding {
		return key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", destination))
	}
	cancelTo := func(destination string) key.Binding {
		return key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", destination))
	}
	switch m.phase {
	case phaseNamingSprite:
		short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "create")), escapeTo("home"), m.keys.force}
	case phaseChoosingSpriteTransport:
		short = []key.Binding{
			key.NewBinding(key.WithKeys("↑/↓"), key.WithHelp("↑/↓", "choose")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "connect")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "sessions")),
			m.keys.quit,
		}
	case phasePreparing:
		short = []key.Binding{m.keys.quit}
	case phaseConfirmingSpriteInstall:
		short = []key.Binding{m.keys.confirm, cancelTo("home"), m.keys.quit}
	case phaseConfirmingStop:
		short = []key.Binding{m.keys.confirm, cancelTo("sessions"), m.keys.quit}
	case phaseConfirmingHostKey:
		short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "continue")), cancelTo("sessions"), m.keys.quit}
	case phaseChoosingSSHKey:
		short = []key.Binding{key.NewBinding(key.WithKeys("↑/↓"), key.WithHelp("↑/↓", "choose")), key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "use key")), escapeTo("sessions"), m.keys.quit}
	case phaseLoadingSprite, phaseRefreshingSprite, phaseInstallingSpriteHerdr:
		short = []key.Binding{escapeTo("home"), m.keys.force}
	case phaseReady:
		if m.browser.SettingFilter() {
			short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply")), escapeTo("list"), m.keys.force}
		} else if m.screen == screenSprite {
			actions := make([]key.Binding, 0, 3)
			if _, ok := m.selectedDestination(); ok && m.selectedCurrent() {
				actions = append(actions, m.keys.enter)
			}
			if _, ok := m.selectedSpriteSessionForMutation(); ok {
				actions = append(actions, m.keys.stop, m.keys.restart)
			}
			navigation := []key.Binding{movement, escapeTo("home"), filtering, m.keys.refresh, m.keys.quit, m.keys.help}
			short = []key.Binding{movement, escapeTo("home")}
			short = append(short, actions...)
			short = append(short, filtering, m.keys.refresh, m.keys.help)
			full = [][]key.Binding{actions, navigation}
		} else {
			actions := make([]key.Binding, 0, 2)
			if _, ok := m.selectedDestination(); ok && m.selectedCurrent() {
				actions = append(actions, m.keys.enter)
			}
			if _, ok := m.operations.(spriteOperations); ok && m.spriteSnapshot.current && m.createBlock == "" {
				actions = append(actions, m.keys.create)
			}
			navigation := []key.Binding{movement, filtering, m.keys.refresh, m.keys.quit, m.keys.help}
			short = append([]key.Binding{movement}, actions...)
			short = append(short, filtering, m.keys.refresh, m.keys.help)
			full = [][]key.Binding{actions, navigation}
		}
	default:
		short = []key.Binding{m.keys.quit}
	}
	if m.help.ShowAll && len(full) > 0 {
		return m.help.FullHelpView(full)
	}
	return m.help.ShortHelpView(short)
}

func (m Model) spriteName() string {
	if endpoint, ok := m.spriteEndpoint.Endpoint.(catalog.SpriteEndpoint); ok {
		return printable(endpoint.Name)
	}
	if endpoint, ok := m.activePrepare.destination.Endpoint.(catalog.SpriteEndpoint); ok {
		return printable(endpoint.Name)
	}
	return "Sprite"
}

func (m Model) surfaceWidth() int {
	return max(1, min(maxSurfaceWidth, m.width-surfaceHorizontalMargin))
}

func (m Model) browserPanelWidth(available int) int {
	ideal := minBrowserPanelWidth
	for _, raw := range m.browser.Items() {
		item, ok := raw.(browserItem)
		if !ok {
			continue
		}
		rowWidth := lipgloss.Width(printable(item.destination.Label)) +
			browserMarkerWidth + browserStatusGap + browserStatusWidth + browserPanelChrome
		ideal = max(ideal, rowWidth)
	}
	if m.screen == screenSprite {
		title := m.spriteName() + " sessions"
		badge := fmt.Sprintf("%d", len(m.spriteSnapshotDetail.rows))
		ideal = max(ideal, lipgloss.Width(title)+lipgloss.Width(badge)+9)
	}
	return max(1, min(available, min(maxBrowserPanelWidth, ideal)))
}

func (m *Model) resizeComponents() {
	surfaceWidth := m.surfaceWidth()
	m.browser.SetWidth(surfaceWidth)
	m.browser.SetHeight(max(1, m.height-9))
	m.help.SetWidth(surfaceWidth)
	m.nameInput.SetWidth(max(1, min(40, surfaceWidth)))
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
	return "Couldn’t prepare destination: " + printable(err.Error())
}

func withoutEnvironment(environment []string, name string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return result
}

func withEnvironment(environment []string, name, value string) []string {
	result := withoutEnvironment(environment, name)
	return append(result, name+"="+value)
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

var _ tea.Model = Model{}
var _ list.ItemDelegate = browserDelegate{}
