package ui

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
)

type sessionsState struct {
	phase                   phase
	spriteEndpoint          catalog.Destination
	spriteEndpointSelection catalog.SpriteEndpointSelection
	spriteDetailEndpointID  catalog.DestinationID
	spriteSnapshotDetail    sourceSnapshot
}

func (s *sessionsState) open(endpoint catalog.Destination, selection catalog.SpriteEndpointSelection) {
	s.spriteEndpoint = endpoint
	s.spriteEndpointSelection = selection
	s.spriteDetailEndpointID = endpoint.ID
	s.spriteSnapshotDetail = sourceSnapshot{}
}

func (s *sessionsState) invalidate() {
	s.spriteSnapshotDetail.current = false
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

func (m Model) updateSpriteSessionsDone(msg spriteSessionsDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || msg.endpointID != m.sessions.spriteDetailEndpointID || (m.currentPhase() != phaseLoadingSprite && m.currentPhase() != phaseRefreshingSprite) {
		return m, nil
	}
	initial := m.currentPhase() == phaseLoadingSprite
	m.cancelOperation()
	if errors.Is(msg.err, catalog.ErrSpriteHerdrMissing) {
		m.sessions.spriteSnapshotDetail = sourceSnapshot{}
		m.refreshNotice = ""
		m.setPhase(phaseConfirmingSpriteInstall)
		m.syncSpriteItems("")
		return m, nil
	}
	if msg.err != nil {
		m.sessions.invalidate()
		m.refreshNotice = "Sprite session refresh failed: " + printable(msg.err.Error())
	} else {
		m.sessions.spriteSnapshotDetail = sourceSnapshot{rows: append([]catalog.Destination(nil), msg.rows...), loaded: true, current: true}
		m.refreshNotice = ""
	}
	m.setPhase(phaseReady)
	selected := m.selectedID
	if initial {
		selected = ""
	}
	m.syncSpriteItems(selected)
	return m, nil
}

func (m Model) updateInstallSpriteHerdrDone(msg installSpriteHerdrDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || m.currentPhase() != phaseInstallingSpriteHerdr {
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil {
		m.notice = "Couldn’t install Herdr: " + printable(msg.err.Error())
		m.setPhase(phaseReady)
		return m, nil
	}
	m.setPhase(phaseLoadingSprite)
	cmd := m.beginSpriteSessions()
	return m, tea.Batch(cmd, m.spinner.Tick)
}

func (m *Model) beginSpriteSessions() tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	opID, ctx := m.beginOperation()
	endpointID := m.sessions.spriteDetailEndpointID
	selection := m.sessions.spriteEndpointSelection
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
	opID, ctx := m.beginOperation()
	m.setPhase(phaseInstallingSpriteHerdr)
	selection := m.sessions.spriteEndpointSelection
	return func() tea.Msg {
		return installSpriteHerdrDone{opID: opID, err: operations.InstallSpriteHerdr(ctx, selection)}
	}
}

func (m *Model) beginSpriteRefresh() tea.Cmd {
	m.setPhase(phaseRefreshingSprite)
	cmd := m.beginSpriteSessions()
	return tea.Batch(cmd, m.spinner.Tick)
}
