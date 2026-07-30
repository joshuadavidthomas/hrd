package ui

import (
	"errors"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
)

type sessionsState struct {
	phase                   sessionsPhase
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

type sessionsMessage interface {
	sessionsMessage()
}

type spriteSessionsDone struct {
	opID       uint64
	endpointID catalog.DestinationID
	rows       []catalog.Destination
	err        error
}

func (spriteSessionsDone) sessionsMessage() {}

type installSpriteHerdrDone struct {
	opID uint64
	err  error
}

func (installSpriteHerdrDone) sessionsMessage() {}

func (m Model) updateSessionsMessage(msg sessionsMessage) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spriteSessionsDone:
		return m.updateSpriteSessionsDone(msg)
	case installSpriteHerdrDone:
		return m.updateInstallSpriteHerdrDone(msg)
	default:
		panic("ui: invalid Sprite Sessions message")
	}
}

func (m Model) updateSessionsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.sessions.phase {
	case sessionsConfirmingInstall:
		return m.updateSpriteInstallConfirmation(msg)
	case sessionsLoading, sessionsRefreshing, sessionsInstallingHerdr:
		if msg.String() == "esc" {
			return m, m.returnToEndpoints()
		}
		return m, nil
	case sessionsReady:
	default:
		panic("ui: invalid Sprite Sessions phase")
	}

	if key.Matches(msg, m.keys.refresh) && !m.browser.SettingFilter() {
		m.refreshNotice = ""
		return m, m.beginSpriteRefresh()
	}
	if !m.browser.SettingFilter() {
		if key.Matches(msg, m.keys.back) {
			return m, m.returnToEndpoints()
		}
		if key.Matches(msg, m.keys.stop) {
			if destination, ok := m.selectedSpriteSessionForMutation(); ok {
				m.connection.destination = destination
				m.connection.restart = false
				m.showConnection(connectionConfirmingStop)
			}
			return m, nil
		}
		if key.Matches(msg, m.keys.restart) {
			if destination, ok := m.selectedSpriteSessionForMutation(); ok {
				m.connection.destination = destination
				m.connection.restart = true
				m.showConnection(connectionConfirmingStop)
			}
			return m, nil
		}
		if msg.String() == "enter" {
			destination, ok := m.selectedDestination()
			if !ok || !m.selectedCurrent() {
				return m, nil
			}
			m.notice = ""
			m.selectedID = destination.ID
			m.connection.destination = destination
			m.connection.restart = false
			m.connection.transport = catalog.NativePreview
			m.showConnection(connectionChoosingTransport)
			return m, nil
		}
	}
	return m.updateBrowserKey(msg)
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
	if msg.opID != m.operation.id || msg.endpointID != m.sessions.spriteDetailEndpointID || (m.sessions.phase != sessionsLoading && m.sessions.phase != sessionsRefreshing) {
		return m, nil
	}
	initial := m.sessions.phase == sessionsLoading
	m.cancelOperation()
	if errors.Is(msg.err, catalog.ErrSpriteHerdrMissing) {
		m.sessions.spriteSnapshotDetail = sourceSnapshot{}
		m.refreshNotice = ""
		m.showSessions(sessionsConfirmingInstall)
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
	m.showSessions(sessionsReady)
	selected := m.selectedID
	if initial {
		selected = ""
	}
	m.syncSpriteItems(selected)
	return m, nil
}

func (m Model) updateInstallSpriteHerdrDone(msg installSpriteHerdrDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || m.sessions.phase != sessionsInstallingHerdr {
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil {
		m.notice = "Couldn’t install Herdr: " + printable(msg.err.Error())
		m.showSessions(sessionsReady)
		return m, nil
	}
	m.showSessions(sessionsLoading)
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
	m.showSessions(sessionsInstallingHerdr)
	selection := m.sessions.spriteEndpointSelection
	return func() tea.Msg {
		return installSpriteHerdrDone{opID: opID, err: operations.InstallSpriteHerdr(ctx, selection)}
	}
}

func (m *Model) beginSpriteRefresh() tea.Cmd {
	m.showSessions(sessionsRefreshing)
	cmd := m.beginSpriteSessions()
	return tea.Batch(cmd, m.spinner.Tick)
}
