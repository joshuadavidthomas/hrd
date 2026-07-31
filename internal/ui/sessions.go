package ui

import (
	"context"
	"errors"
	"sync"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	spriteapi "github.com/joshuadavidthomas/hrd/internal/sprites"
)

type sessionsState struct {
	phase                   sessionsPhase
	selection               catalog.DestinationID
	spriteEndpoint          catalog.Destination
	spriteEndpointSelection catalog.SpriteEndpointSelection
	spriteSnapshotDetail    sourceSnapshot
	cache                   map[catalog.DestinationID]cachedSpriteSessions
	prefetchID              uint64
	prefetchCancel          context.CancelFunc
}

func (s *sessionsState) open(endpoint catalog.Destination, selection catalog.SpriteEndpointSelection) {
	s.spriteEndpoint = endpoint
	s.spriteEndpointSelection = selection
	s.spriteSnapshotDetail = sourceSnapshot{}
	s.selection = ""
}

func (s *sessionsState) invalidate() {
	s.spriteSnapshotDetail.current = false
	delete(s.cache, s.spriteEndpoint.ID)
}

func (s sessionsState) cached(endpointID catalog.DestinationID) (cachedSpriteSessions, bool) {
	cached, ok := s.cache[endpointID]
	return cached, ok
}

func (s *sessionsState) store(cached cachedSpriteSessions) {
	if s.cache == nil {
		s.cache = make(map[catalog.DestinationID]cachedSpriteSessions)
	}
	cached.rows = append([]catalog.Destination(nil), cached.rows...)
	s.cache[cached.endpointID] = cached
}

func eagerSprite(destination catalog.Destination) bool {
	status, ok := destination.Status.(catalog.SpriteStatus)
	return ok && (status.Lifecycle == spriteapi.Running || status.Lifecycle == spriteapi.Warm)
}

func (m Model) eagerSpriteCurrent(endpointID catalog.DestinationID) bool {
	if !m.home.spriteSnapshot.current {
		return false
	}
	for _, destination := range m.home.spriteSnapshot.rows {
		if destination.ID == endpointID {
			return eagerSprite(destination)
		}
	}
	return false
}

func (m *Model) clearSessionCache() {
	m.cancelSessionPrefetch()
	m.sessions.cache = nil
}

func (m *Model) reconcileSessionCache() {
	if !m.home.spriteSnapshot.current {
		m.clearSessionCache()
		return
	}
	for endpointID := range m.sessions.cache {
		if !m.eagerSpriteCurrent(endpointID) {
			delete(m.sessions.cache, endpointID)
		}
	}
}

type spriteSessionsDone struct {
	opID uint64
	rows []catalog.Destination
	err  error
}

type cachedSpriteSessions struct {
	endpointID catalog.DestinationID
	rows       []catalog.Destination
	err        error
}

type spriteSessionsPrefetched struct {
	prefetchID uint64
	results    []cachedSpriteSessions
}

type spriteSessionPrefetchTarget struct {
	endpointID catalog.DestinationID
	selection  catalog.SpriteEndpointSelection
}

type installSpriteHerdrDone struct {
	opID uint64
	err  error
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
			m.sessions.selection = destination.ID
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
	if msg.opID != m.operation.id || (m.sessions.phase != sessionsLoading && m.sessions.phase != sessionsRefreshing && m.sessions.phase != sessionsInstallingHerdr) {
		return m, nil
	}
	refreshing := m.sessions.phase == sessionsRefreshing
	selected := m.sessions.selection
	if !refreshing {
		selected = ""
	}
	m.cancelOperation()
	m.applySpriteSessions(cachedSpriteSessions{endpointID: m.sessions.spriteEndpoint.ID, rows: msg.rows, err: msg.err}, refreshing, selected)
	return m, nil
}

func (m *Model) applySpriteSessions(cached cachedSpriteSessions, preserve bool, selected catalog.DestinationID) {
	if !preserve {
		m.browser.ResetFilter()
	}
	if cached.err == nil || errors.Is(cached.err, catalog.ErrSpriteHerdrMissing) {
		m.sessions.store(cached)
	}
	if errors.Is(cached.err, catalog.ErrSpriteHerdrMissing) {
		m.sessions.spriteSnapshotDetail = sourceSnapshot{}
		m.refreshNotice = ""
		m.showSessions(sessionsConfirmingInstall)
		m.syncSpriteItems("")
		return
	}
	if cached.err != nil {
		if preserve {
			m.sessions.invalidate()
		} else {
			m.sessions.spriteSnapshotDetail = sourceSnapshot{}
		}
		m.refreshNotice = "Sprite session refresh failed: " + printable(cached.err.Error())
	} else {
		m.sessions.spriteSnapshotDetail = sourceSnapshot{rows: append([]catalog.Destination(nil), cached.rows...), loaded: true, current: true}
		m.refreshNotice = ""
	}
	m.showSessions(sessionsReady)
	m.syncSpriteItems(selected)
}

func (m Model) updateSpriteSessionsPrefetched(msg spriteSessionsPrefetched) (tea.Model, tea.Cmd) {
	if msg.prefetchID != m.sessions.prefetchID {
		return m, nil
	}
	if m.sessions.prefetchCancel != nil {
		m.sessions.prefetchCancel()
		m.sessions.prefetchCancel = nil
	}
	for _, cached := range msg.results {
		if m.eagerSpriteCurrent(cached.endpointID) {
			m.sessions.store(cached)
		}
	}
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
	m.clearSessionCache()
	cmd := m.beginSpriteSessions()
	return m, tea.Batch(cmd, m.spinner.Tick)
}

func (m *Model) beginSpriteSessions() tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	opID, ctx := m.beginOperation()
	selection := m.sessions.spriteEndpointSelection
	return func() tea.Msg {
		rows, err := operations.ListSpriteSessions(ctx, selection)
		return spriteSessionsDone{opID: opID, rows: append([]catalog.Destination(nil), rows...), err: err}
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

func (m *Model) beginWarmSessionPrefetch() tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok || !m.home.spriteSnapshot.current || m.sessions.prefetchCancel != nil {
		return nil
	}
	targets := make([]spriteSessionPrefetchTarget, 0, len(m.home.spriteSnapshot.rows))
	for _, destination := range m.home.spriteSnapshot.rows {
		if !eagerSprite(destination) {
			continue
		}
		if _, cached := m.sessions.cached(destination.ID); cached {
			continue
		}
		selection, err := catalog.SelectSpriteEndpoint(destination)
		if err != nil {
			continue
		}
		targets = append(targets, spriteSessionPrefetchTarget{endpointID: destination.ID, selection: selection})
	}
	if len(targets) == 0 {
		return nil
	}
	m.sessions.prefetchID++
	prefetchID := m.sessions.prefetchID
	ctx, cancel := context.WithCancel(m.root)
	m.sessions.prefetchCancel = cancel
	return func() tea.Msg {
		results := make([]cachedSpriteSessions, len(targets))
		var workers sync.WaitGroup
		workers.Add(len(targets))
		for index, target := range targets {
			go func() {
				defer workers.Done()
				rows, err := operations.ListSpriteSessions(ctx, target.selection)
				results[index] = cachedSpriteSessions{endpointID: target.endpointID, rows: append([]catalog.Destination(nil), rows...), err: err}
			}()
		}
		workers.Wait()
		return spriteSessionsPrefetched{prefetchID: prefetchID, results: results}
	}
}

func (m *Model) cancelSessionPrefetch() {
	if m.sessions.prefetchCancel == nil {
		return
	}
	m.sessions.prefetchCancel()
	m.sessions.prefetchCancel = nil
	m.sessions.prefetchID++
}

func (m Model) sessionsView(width, height int) string {
	switch m.sessions.phase {
	case sessionsLoading:
		return m.busyLine("Loading sessions…", width)
	case sessionsConfirmingInstall:
		return titleStyle.Render("Install Herdr?")
	case sessionsInstallingHerdr:
		return m.busyLine("Installing Herdr…", width)
	case sessionsRefreshing, sessionsReady:
		return m.listView(width, height)
	default:
		panic("ui: invalid Sprite Sessions phase")
	}
}
