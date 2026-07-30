package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	spriteapi "github.com/joshuadavidthomas/hrd/internal/sprites"
)

type homeState struct {
	phase                   phase
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

// Update advances navigation and provider effects.
func (m Model) updateName(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.nameInput.Blur()
		m.setPhase(phaseReady)
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
	if msg.opID != m.operation.id || (m.currentPhase() != phaseLoading && !m.home.inventoryRefreshing) {
		return m, nil
	}
	wasInitial := m.currentPhase() == phaseLoading
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
		m.setPhase(phaseInitialLoadFailed)
		if m.refreshNotice == "" {
			m.refreshNotice = "No inventory source loaded."
		}
		return m, nil
	}
	m.reconcileCreateBlock()
	m.setPhase(phaseReady)
	selected := m.selectedID
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
	if msg.opID != m.operation.id || m.currentPhase() != phaseCreatingSprite {
		return m, nil
	}
	m.cancelOperation()
	result := msg.result
	if result.Confirmed && spriteapi.ValidName(result.Name) == nil {
		m.home.selectSprite = result.Name
		m.setPhase(phaseReady)
		return m, m.beginSilentInventory()
	}
	if result.Rejected {
		m.setPhase(phaseNamingSprite)
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
	m.setPhase(phaseReady)
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
	if m.currentPhase() != phaseLoading {
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
	m.setPhase(phaseCreatingSprite)
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
