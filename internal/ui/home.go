package ui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	spriteapi "github.com/joshuadavidthomas/hrd/internal/sprites"
)

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
