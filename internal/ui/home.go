package ui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	spriteapi "github.com/joshuadavidthomas/hrd/internal/sprites"
)

type homeState struct {
	phase                   homePhase
	localSnapshot           sourceSnapshot
	spriteSnapshot          sourceSnapshot
	destinations            []catalog.Destination
	endpointSelection       catalog.DestinationID
	selectSprite            string
	createNotice            string
	inventoryRefreshing     bool
	inventoryRefreshVisible bool
	createBlock             string
}

func (s *homeState) invalidateSprites() {
	s.spriteSnapshot.current = false
}

type inventoryDone struct {
	opID      uint64
	inventory catalog.Inventory
}

type createDone struct {
	opID   uint64
	result spriteapi.CreateResult
}

type refreshTick time.Time

func scheduleRefresh() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return refreshTick(t) })
}

func (m Model) updateHomeKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.home.phase {
	case homeNamingSprite:
		return m.updateName(msg)
	case homeInitialLoadFailed:
		if key.Matches(msg, m.keys.refresh) {
			m.showHome(homeLoading)
			m.refreshNotice = ""
			return m, m.beginInventory()
		}
		return m, nil
	case homeLoading, homeCreatingSprite:
		return m, nil
	case homeReady:
	default:
		panic("ui: invalid Home phase")
	}

	if key.Matches(msg, m.keys.refresh) && !m.browser.SettingFilter() {
		m.refreshNotice = ""
		if m.home.inventoryRefreshing {
			m.home.inventoryRefreshVisible = true
			return m, m.spinner.Tick
		}
		return m, tea.Batch(m.beginInventory(), m.spinner.Tick)
	}
	if msg.String() == "n" && !m.browser.SettingFilter() {
		if _, ok := m.operations.(spriteOperations); ok && m.home.spriteSnapshot.current && m.home.createBlock == "" {
			m.supersedeOperation()
			m.nameInput.Reset()
			m.nameInput.SetValue("")
			m.nameInput.Focus()
			m.notice = ""
			m.showHome(homeNamingSprite)
		}
		return m, nil
	}
	if msg.String() == "enter" && !m.browser.SettingFilter() {
		destination, ok := m.selectedDestination()
		if !ok || !m.selectedCurrent() {
			return m, nil
		}
		m.notice = ""
		m.home.endpointSelection = destination.ID
		if _, ok := destination.Endpoint.(catalog.SpriteEndpoint); ok {
			selection, err := catalog.SelectSpriteEndpoint(destination)
			if err != nil {
				m.notice = prepareFailureNotice(err)
				return m, nil
			}
			m.sessions.open(destination, selection)
			m.home.invalidateSprites()
			m.showSessions(sessionsLoading)
			m.browser.ResetFilter()
			m.browser.SetItems(nil)
			return m, tea.Batch(m.beginSpriteSessions(), m.spinner.Tick)
		}
		return m, tea.Batch(m.beginPrepareLocal(destination), m.spinner.Tick)
	}
	return m.updateBrowserKey(msg)
}

func (m Model) updateName(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.nameInput.Blur()
		m.showHome(homeReady)
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

func (m Model) updateInventoryDone(msg inventoryDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || (m.home.phase != homeLoading && !m.home.inventoryRefreshing) {
		return m, nil
	}
	wasInitial := m.home.phase == homeLoading
	showFeedback := wasInitial || m.home.inventoryRefreshVisible
	m.cancelOperation()
	m.home.inventoryRefreshing = false
	m.home.inventoryRefreshVisible = false
	m.home.localSnapshot = refreshSnapshot(m.home.localSnapshot, msg.inventory.Local)
	m.home.spriteSnapshot = refreshSnapshot(m.home.spriteSnapshot, msg.inventory.Sprites)
	m.home.destinations = append(append([]catalog.Destination(nil), m.home.localSnapshot.rows...), m.home.spriteSnapshot.rows...)
	if showFeedback {
		m.refreshNotice = inventoryNotice(msg.inventory, wasInitial)
	}
	if wasInitial && !m.home.localSnapshot.loaded && !m.home.spriteSnapshot.loaded {
		m.showHome(homeInitialLoadFailed)
		if m.refreshNotice == "" {
			m.refreshNotice = "No inventory source loaded."
		}
		return m, nil
	}
	m.reconcileCreateBlock()
	m.showHome(homeReady)
	selected := m.home.endpointSelection
	if m.home.selectSprite != "" {
		for _, row := range m.home.destinations {
			if endpoint, ok := row.Endpoint.(catalog.SpriteEndpoint); ok && endpoint.Name == m.home.selectSprite {
				selected = row.ID
				break
			}
		}
		m.home.selectSprite = ""
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

func (m Model) updateCreateDone(msg createDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || m.home.phase != homeCreatingSprite {
		return m, nil
	}
	m.cancelOperation()
	result := msg.result
	if result.Confirmed && spriteapi.ValidName(result.Name) == nil {
		m.home.selectSprite = result.Name
		m.showHome(homeReady)
		return m, m.beginSilentInventory()
	}
	if result.Rejected {
		m.showHome(homeNamingSprite)
		m.nameInput.Focus()
		if result.Err == nil {
			m.notice = "Create failed."
		} else {
			m.notice = "Create failed: " + printable(result.Err.Error())
		}
		return m, nil
	}
	m.home.createBlock = result.Name
	m.home.createNotice = "Creation outcome is unknown. Reconcile " + printable(result.Name) + " before creating another Sprite."
	m.showHome(homeReady)
	return m, m.beginSilentInventory()
}

func (m *Model) beginInventory() tea.Cmd {
	return m.beginInventoryWithFeedback(true)
}

func (m *Model) beginSilentInventory() tea.Cmd {
	return m.beginInventoryWithFeedback(false)
}

func (m *Model) beginInventoryWithFeedback(visible bool) tea.Cmd {
	opID, ctx := m.beginOperation()
	if m.home.phase != homeLoading {
		m.home.inventoryRefreshing = true
		m.home.inventoryRefreshVisible = visible
	}
	operations := m.operations
	return func() tea.Msg {
		return inventoryDone{opID: opID, inventory: operations.Inventory(ctx)}
	}
}

func (m *Model) beginCreate(name string) tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	opID, ctx := m.beginOperation()
	m.showHome(homeCreatingSprite)
	return func() tea.Msg { return createDone{opID: opID, result: operations.Create(ctx, name)} }
}

func (m *Model) reconcileCreateBlock() {
	if m.home.createBlock == "" || !m.home.spriteSnapshot.current {
		return
	}
	for _, destination := range m.home.spriteSnapshot.rows {
		if endpoint, ok := destination.Endpoint.(catalog.SpriteEndpoint); ok && endpoint.Name == m.home.createBlock {
			m.home.createBlock = ""
			m.home.createNotice = ""
			return
		}
	}
}

func (m Model) homeView(width, height int) string {
	switch m.home.phase {
	case homeLoading:
		return ""
	case homeInitialLoadFailed:
		return clip("No inventory source loaded. Press r to retry.", width)
	case homeNamingSprite:
		return fitView(strings.Join([]string{
			"Name your Sprite",
			m.nameInput.View(),
			mutedStyle.Render("1–63 lowercase letters, digits, and interior hyphens"),
			warningStyle.Render("Creating and waking a Sprite may incur charges."),
		}, "\n"), width, height)
	case homeCreatingSprite:
		return m.busyLine("Creating "+m.nameInput.Value()+"…", width)
	case homeReady:
		if len(m.browser.Items()) == 0 {
			return "No local sessions or Sprite endpoints found."
		}
		return m.listView(width, height)
	default:
		panic("ui: invalid Home phase")
	}
}
