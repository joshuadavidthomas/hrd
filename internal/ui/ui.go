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
	defaultWidth    = 80
	defaultHeight   = 24
	refreshInterval = 10 * time.Second
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
	rows       []catalog.Destination
	loaded     bool
	current    bool
	observedAt time.Time
}

type browserItem struct {
	destination catalog.Destination
	description string
	stale       bool
}

func (i browserItem) FilterValue() string { return printable(i.destination.Label) }

type browserDelegate struct{}

func (browserDelegate) Height() int                         { return 2 }
func (browserDelegate) Spacing() int                        { return 0 }
func (browserDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (browserDelegate) Render(w io.Writer, model list.Model, index int, raw list.Item) {
	item, ok := raw.(browserItem)
	if !ok {
		return
	}
	width := max(1, model.Width()-2)
	selected := index == model.Index()
	marker := "  "
	title := printable(item.destination.Label)
	detail := printable(item.description)
	if selected {
		marker = accentStyle.Render("› ")
		title = selectedStyle.Render(title)
	}
	if item.stale {
		detail = warningStyle.Render(detail)
	} else {
		detail = mutedStyle.Render(detail)
	}
	_, _ = fmt.Fprint(w, marker+clip(title, width)+"\n  "+clip(detail, width))
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
	cancel  key.Binding
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
		cancel:  key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n", "cancel")),
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

	notice               string
	createNotice         string
	refreshNotice        string
	inventoryRefreshing  bool
	createBlock          string
	showHelp             bool
	restart              bool
	restartStopSucceeded bool
	confirmDest          catalog.Destination
	activePrepare        prepareOperation
	prepared             catalog.PreparedAttach

	transport        catalog.SpriteTransport
	sshRecoveryTried bool
	sshKeySource     string
	sshKeyChoices    []catalog.SSHIdentity
	sshKeyCursor     int

	now func() time.Time
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
		help:        help.New(),
		nameInput:   name,
		now:         time.Now,
	}
	m.initialCmd = m.beginInventory()
	return m
}

// Init starts inventory loading, animation, and the idle refresh clock.
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
		cmds := make([]tea.Cmd, 0, 3)
		if m.screen == screenEndpoints && m.phase == phaseReady && !m.inventoryRefreshing && !m.browser.SettingFilter() {
			refresh := m.beginInventory()
			cmds = append(cmds, refresh, m.spinner.Tick)
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
			m.cancelOperation()
			m.operationID = m.nextID()
			m.screen = screenEndpoints
			m.phase = phaseReady
			m.browser.ResetFilter()
			m.syncEndpointItems(m.endpointSelection)
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
			return m, nil
		}
		cmd := m.beginInventory()
		return m, tea.Batch(cmd, m.spinner.Tick)
	}
	if m.screen == screenSprite && !m.browser.SettingFilter() {
		if key.Matches(msg, m.keys.back) {
			m.screen = screenEndpoints
			m.phase = phaseReady
			m.browser.ResetFilter()
			m.syncEndpointItems(m.endpointSelection)
			return m, nil
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
		m.screen = screenEndpoints
		m.phase = phaseReady
		m.browser.ResetFilter()
		m.syncEndpointItems(m.endpointSelection)
	}
	return m, nil
}

func (m Model) updateInventoryDone(msg inventoryDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operationID || (m.phase != phaseLoading && !m.inventoryRefreshing) {
		return m, nil
	}
	wasInitial := m.phase == phaseLoading
	m.cancelOperation()
	m.inventoryRefreshing = false
	now := m.now()
	m.localSnapshot = refreshSnapshot(m.localSnapshot, msg.inventory.Local, now)
	m.spriteSnapshot = refreshSnapshot(m.spriteSnapshot, msg.inventory.Sprites, now)
	m.destinations = append(append([]catalog.Destination(nil), m.localSnapshot.rows...), m.spriteSnapshot.rows...)
	m.refreshNotice = inventoryNotice(msg.inventory, wasInitial)
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

func refreshSnapshot(previous sourceSnapshot, result catalog.SourceResult, now time.Time) sourceSnapshot {
	if result.Skipped {
		return sourceSnapshot{}
	}
	if result.Err != nil {
		previous.current = false
		return previous
	}
	return sourceSnapshot{rows: append([]catalog.Destination(nil), result.Rows...), loaded: true, current: true, observedAt: now}
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
		m.spriteSnapshotDetail = sourceSnapshot{rows: append([]catalog.Destination(nil), msg.rows...), loaded: true, current: true, observedAt: m.now()}
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
		cmd := m.beginInventory()
		return m, tea.Batch(cmd, m.spinner.Tick)
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
	cmd := m.beginInventory()
	return m, tea.Batch(cmd, m.spinner.Tick)
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
	cmd := m.beginInventory()
	return m, tea.Batch(cmd, m.spinner.Tick)
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

func (m *Model) beginInventory() tea.Cmd {
	m.startOperation()
	if m.phase != phaseLoading {
		m.inventoryRefreshing = true
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
	m.phase = phaseAttaching
	opID := m.operationID
	return m.execCommand(prepared.Command, func(err error) tea.Msg { return attachDone{opID: opID, err: err} })
}

func (m *Model) startOperation() {
	m.cancelOperation()
	m.inventoryRefreshing = false
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
	now := m.now()
	for _, destination := range m.destinations {
		stale := !m.destinationCurrent(destination)
		items = append(items, browserItem{destination: destination, stale: stale, description: m.endpointDescription(destination, stale, now)})
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
		state := "stopped"
		if status.Running {
			state = "running"
		}
		description := state
		if !m.spriteSnapshotDetail.current {
			description += " · stale"
		}
		items = append(items, browserItem{destination: destination, stale: !m.spriteSnapshotDetail.current, description: description})
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

func (m Model) endpointDescription(destination catalog.Destination, stale bool, now time.Time) string {
	switch status := destination.Status.(type) {
	case catalog.LocalSessionStatus:
		state := "stopped"
		if status.Running {
			state = "running"
		}
		result := "Local · " + state
		if stale {
			result += " · stale · checked " + age(now, m.localSnapshot.observedAt)
		}
		return result
	case catalog.SpriteStatus:
		result := "Sprite API · " + string(status.Lifecycle) + " · checked " + age(now, m.spriteSnapshot.observedAt)
		if stale {
			result += " · stale"
		}
		return result
	default:
		return "Unknown source"
	}
}

func age(now, observed time.Time) string {
	if observed.IsZero() {
		return "never"
	}
	d := now.Sub(observed)
	if d < 0 || d < time.Second {
		return "just now"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	}
	return fmt.Sprintf("%dh ago", int(d/time.Hour))
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
	if m.inventoryRefreshing {
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
	warningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	borderStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
)

// View renders a compact bordered application surface.
func (m Model) View() tea.View {
	innerWidth := max(1, min(76, m.width-4))
	innerHeight := max(1, m.height-2)
	m.help.SetWidth(innerWidth)

	header := titleStyle.Render("hrd")
	if m.screen == screenSprite {
		name := "Sprite"
		if endpoint, ok := m.spriteEndpoint.Endpoint.(catalog.SpriteEndpoint); ok {
			name = printable(endpoint.Name)
		}
		header += "  " + mutedStyle.Render("/ Sprites / "+name)
	} else {
		header += "  " + mutedStyle.Render("/ Sessions & Sprites")
	}

	footer := m.helpView()
	footerHeight := lipgloss.Height(footer)
	lines := []string{clip(header, innerWidth)}
	for _, notice := range []string{m.notice, m.createNotice, m.refreshNotice} {
		if notice == "" {
			continue
		}
		style := warningStyle
		if strings.Contains(strings.ToLower(notice), "failed") || strings.Contains(strings.ToLower(notice), "couldn’t") {
			style = errorStyle
		}
		lines = append(lines, clip(style.Render("! "+printable(notice)), innerWidth))
	}

	bodyBudget := max(1, innerHeight-len(lines)-footerHeight)
	body := m.bodyView(innerWidth, bodyBudget)
	if body != "" {
		lines = append(lines, body)
	}
	if footer != "" {
		lines = append(lines, footer)
	}
	content := strings.Join(lines, "\n")
	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderStyle.GetForeground()).
		Padding(0, 1).
		Width(min(80, m.width)).
		Render(content)
	panel = fitView(panel, m.width, m.height)
	if panel != "" {
		panel += "\n"
	}
	view := tea.NewView(panel)
	view.AltScreen = true
	view.WindowTitle = "hrd · Herdr sessions"
	return view
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
		return m.listWithWork("Refreshing sessions on "+m.spriteName()+"…", width, height)
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
		return fitView(m.transportView(), width, height)
	case phasePreparing:
		if m.activePrepare.sprite {
			if m.activePrepare.transport == catalog.SSHFallback {
				return m.busyLine("Setting up SSH on "+m.spriteName()+"… Up to three minutes.", width)
			}
			return m.busyLine("Waking "+m.spriteName()+" and checking Herdr… Up to three minutes.", width)
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
		return fitView(m.sshKeyView(), width, height)
	case phaseAttaching:
		return "Herdr has the terminal."
	case phaseReady:
		if m.inventoryRefreshing {
			return m.listWithWork("Refreshing local and Sprite inventory…", width, height)
		}
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
	browser := m.browser
	browser.SetSize(width, max(1, height))
	return fitView(browser.View(), width, height)
}

func (m Model) spriteReadyView(width, height int) string {
	observation := mutedStyle.Render("Herdr checked " + age(m.now(), m.spriteSnapshotDetail.observedAt))
	if height <= 1 {
		return clip(observation, width)
	}
	if len(m.browser.Items()) == 0 {
		return fitView(observation+"\nNo Herdr sessions found on this Sprite.", width, height)
	}
	return clip(observation, width) + "\n" + m.listView(width, height-1)
}

func (m Model) transportView() string {
	options := []struct {
		transport catalog.SpriteTransport
		title     string
		detail    string
	}{
		{catalog.NativePreview, "Native Sprite terminal", "Default · Herdr runs inside the Sprite"},
		{catalog.SSHFallback, "SSH", "Alternate transport · local Herdr uses your SSH key"},
	}
	action := "attach to "
	if m.restart {
		action = "restart and attach to "
	}
	lines := []string{"Choose transport to " + action + printable(m.confirmDest.Label)}
	for _, option := range options {
		marker := "  "
		title := option.title
		if m.transport == option.transport {
			marker = accentStyle.Render("› ")
			title = selectedStyle.Render(title)
		}
		lines = append(lines, marker+title, "  "+mutedStyle.Render(option.detail))
	}
	return strings.Join(lines, "\n")
}

func (m Model) sshKeyView() string {
	source := m.sshKeySource
	if source == "" {
		source = "ssh-agent"
	}
	lines := []string{fmt.Sprintf("%s exposes %d keys. Choose exactly one.", printable(source), len(m.sshKeyChoices))}
	for index, choice := range m.sshKeyChoices {
		marker := "  "
		label := printable(choice.Comment)
		if label == "" {
			label = "Unnamed key"
		}
		if index == m.sshKeyCursor {
			marker = accentStyle.Render("› ")
			label = selectedStyle.Render(label)
		}
		lines = append(lines, marker+label+"  "+mutedStyle.Render(printable(choice.Algorithm)))
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
	escapeCancel := key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))
	switch m.phase {
	case phaseNamingSprite:
		short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "create")), m.keys.back, m.keys.force}
	case phaseChoosingSpriteTransport:
		short = []key.Binding{key.NewBinding(key.WithKeys("↑/↓"), key.WithHelp("↑/↓", "choose")), key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "continue")), m.keys.back, m.keys.quit}
	case phaseConfirmingSpriteInstall:
		short = []key.Binding{m.keys.confirm, m.keys.cancel, m.keys.quit}
	case phaseConfirmingStop:
		short = []key.Binding{m.keys.confirm, m.keys.cancel, m.keys.quit}
	case phaseConfirmingHostKey:
		short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "continue")), m.keys.back, m.keys.quit}
	case phaseChoosingSSHKey:
		short = []key.Binding{key.NewBinding(key.WithKeys("↑/↓"), key.WithHelp("↑/↓", "choose")), key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "use key")), m.keys.back, m.keys.quit}
	case phaseLoadingSprite, phaseRefreshingSprite, phaseInstallingSpriteHerdr:
		short = []key.Binding{escapeCancel, m.keys.force}
	case phaseReady:
		if m.browser.SettingFilter() {
			short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply")), m.keys.back, m.keys.force}
		} else if m.screen == screenSprite {
			actions := make([]key.Binding, 0, 3)
			if _, ok := m.selectedDestination(); ok && m.selectedCurrent() {
				actions = append(actions, m.keys.enter)
			}
			if _, ok := m.selectedSpriteSessionForMutation(); ok {
				actions = append(actions, m.keys.stop, m.keys.restart)
			}
			navigation := []key.Binding{movement, filtering, m.keys.refresh, m.keys.back, m.keys.quit, m.keys.help}
			short = append(append([]key.Binding(nil), actions...), navigation...)
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
			short = append(append([]key.Binding(nil), actions...), navigation...)
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

func (m *Model) resizeComponents() {
	innerWidth := max(1, min(76, m.width-4))
	m.browser.SetWidth(innerWidth)
	m.browser.SetHeight(max(1, m.height-5))
	m.help.SetWidth(innerWidth)
	m.nameInput.SetWidth(max(1, min(32, innerWidth)))
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
