// Package ui owns hrd's terminal navigation and presentation.
package ui

import (
	"context"
	"os/exec"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
	SSHAddCommand(context.Context) (*exec.Cmd, error)
	SelectSSHKey(string) error
}

type screen uint8

const (
	screenEndpoints screen = iota + 1
	screenSprite
)

type workflow uint8

const (
	workflowHome workflow = iota + 1
	workflowSessions
	workflowConnection
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

type operationState struct {
	next   uint64
	id     uint64
	cancel context.CancelFunc
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

type Model struct {
	root        context.Context
	operations  Operations
	execProcess func(*exec.Cmd, tea.ExecCallback) tea.Cmd
	execCommand func(tea.ExecCommand, tea.ExecCallback) tea.Cmd
	keys        keyMap

	screen screen
	active workflow
	width  int
	height int

	browser   list.Model
	spinner   spinner.Model
	help      help.Model
	nameInput textinput.Model

	operation  operationState
	initialCmd tea.Cmd
	quitting   bool

	selectedID catalog.DestinationID

	notice        string
	refreshNotice string
	showHelp      bool

	home       homeState
	sessions   sessionsState
	connection connectionState
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
		active:      workflowHome,
		width:       defaultWidth,
		height:      defaultHeight,
		browser:     browser,
		spinner:     spin,
		help:        helpModel,
		nameInput:   name,
	}
	m.home.phase = phaseLoading
	m.initialCmd = m.beginInventory()
	return m
}

// Init starts inventory loading and silent background refresh.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.initialCmd, scheduleRefresh())
}

func (m Model) currentPhase() phase {
	switch m.active {
	case workflowHome:
		return m.home.phase
	case workflowSessions:
		return m.sessions.phase
	case workflowConnection:
		return m.connection.phase
	default:
		panic("ui: invalid active workflow")
	}
}

func (m *Model) setPhase(next phase) {
	switch next {
	case phaseLoading, phaseInitialLoadFailed, phaseNamingSprite, phaseCreatingSprite:
		m.active = workflowHome
		m.home.phase = next
	case phaseLoadingSprite, phaseConfirmingSpriteInstall, phaseInstallingSpriteHerdr, phaseRefreshingSprite:
		m.active = workflowSessions
		m.sessions.phase = next
	case phaseConfirmingStop, phaseStopping, phaseChoosingSpriteTransport, phasePreparing,
		phaseConfirmingHostKey, phaseStartingSSHAgent, phaseAddingSSHKey, phaseChoosingSSHKey, phaseAttaching:
		m.active = workflowConnection
		m.connection.phase = next
	case phaseReady:
		if m.screen == screenSprite {
			m.active = workflowSessions
			m.sessions.phase = next
		} else {
			m.active = workflowHome
			m.home.phase = next
		}
	default:
		panic("ui: invalid phase")
	}
}

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
		if m.screen == screenEndpoints && m.currentPhase() == phaseReady && !m.home.inventoryRefreshing {
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
	textEditing := m.currentPhase() == phaseNamingSprite || (m.currentPhase() == phaseReady && m.browser.SettingFilter())
	if key.Matches(msg, m.keys.force) || (key.Matches(msg, m.keys.quit) && !textEditing) {
		m.cancelOperation()
		closePrepared(m.connection.prepared)
		m.connection.prepared = nil
		m.quitting = true
		return m, tea.Quit
	}

	switch m.currentPhase() {
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
			closePrepared(m.connection.prepared)
			m.connection.prepared = nil
			m.connection.restartStopped = false
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
			m.setPhase(phaseLoading)
			m.refreshNotice = ""
			cmd := m.beginInventory()
			return m, cmd
		}
		return m, nil
	case phaseAddingSSHKey, phaseAttaching:
		return m, nil
	}

	if m.currentPhase() != phaseReady {
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
		if m.home.inventoryRefreshing {
			m.home.inventoryRefreshVisible = true
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
				m.connection.destination = destination
				m.connection.restart = false
				m.setPhase(phaseConfirmingStop)
			}
			return m, nil
		}
		if key.Matches(msg, m.keys.restart) {
			if destination, ok := m.selectedSpriteSessionForMutation(); ok {
				m.connection.destination = destination
				m.connection.restart = true
				m.setPhase(phaseConfirmingStop)
			}
			return m, nil
		}
	}
	if m.screen == screenEndpoints && msg.String() == "n" && !m.browser.SettingFilter() {
		if _, ok := m.operations.(spriteOperations); ok && m.home.spriteSnapshot.current && m.home.createBlock == "" {
			m.supersedeOperation()
			m.nameInput.Reset()
			m.nameInput.SetValue("")
			m.nameInput.Focus()
			m.notice = ""
			m.setPhase(phaseNamingSprite)
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
			m.home.endpointSelection = destination.ID
			if _, ok := destination.Endpoint.(catalog.SpriteEndpoint); ok {
				selection, err := catalog.SelectSpriteEndpoint(destination)
				if err != nil {
					m.notice = prepareFailureNotice(err)
					return m, nil
				}
				m.sessions.open(destination, selection)
				m.home.invalidateSprites()
				m.screen = screenSprite
				m.setPhase(phaseLoadingSprite)
				m.browser.ResetFilter()
				m.browser.SetItems(nil)
				cmd := m.beginSpriteSessions()
				return m, tea.Batch(cmd, m.spinner.Tick)
			}
			cmd := m.beginPrepareLocal(destination)
			return m, tea.Batch(cmd, m.spinner.Tick)
		}
		m.connection.destination = destination
		m.connection.restart = false
		m.connection.transport = catalog.NativePreview
		m.setPhase(phaseChoosingSpriteTransport)
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
			m.home.endpointSelection = destination.ID
		}
	}
	return m, cmd
}

func (m *Model) returnToEndpoints() tea.Cmd {
	m.screen = screenEndpoints
	m.setPhase(phaseReady)
	m.browser.ResetFilter()
	m.syncEndpointItems(m.home.endpointSelection)
	if !m.home.spriteSnapshot.loaded || m.home.spriteSnapshot.current {
		return nil
	}
	return m.beginSilentInventory()
}

func (m *Model) supersedeOperation() uint64 {
	m.cancelOperation()
	m.home.inventoryRefreshing = false
	m.home.inventoryRefreshVisible = false
	m.operation.next++
	m.operation.id = m.operation.next
	return m.operation.id
}

func (m *Model) beginOperation() (uint64, context.Context) {
	id := m.supersedeOperation()
	ctx, cancel := context.WithCancel(m.root)
	m.operation.cancel = cancel
	return id, ctx
}

func (m *Model) cancelOperation() {
	if m.operation.cancel != nil {
		m.operation.cancel()
		m.operation.cancel = nil
	}
}

var _ tea.Model = Model{}
var _ list.ItemDelegate = browserDelegate{}
