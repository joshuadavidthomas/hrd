package ui

import (
	"context"
	"errors"

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
	prefetchRemaining       int
	configSyncPending       bool
}

func (s *sessionsState) open(endpoint catalog.Destination, selection catalog.SpriteEndpointSelection) {
	s.spriteEndpoint = endpoint
	s.spriteEndpointSelection = selection
	s.spriteSnapshotDetail = sourceSnapshot{}
	s.selection = ""
	s.configSyncPending = true
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
	opID         uint64
	rows         []catalog.Destination
	err          error
	configSynced bool
}

type cachedSpriteSessions struct {
	endpointID catalog.DestinationID
	rows       []catalog.Destination
	err        error
}

type spriteSessionsPrefetched struct {
	prefetchID uint64
	result     cachedSpriteSessions
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
			m.selectTransport(catalog.NativePreview)
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
	if msg.configSynced {
		m.sessions.configSyncPending = false
	}
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
	if msg.prefetchID != m.sessions.prefetchID || m.sessions.prefetchRemaining == 0 {
		return m, nil
	}
	m.sessions.prefetchRemaining--
	if m.eagerSpriteCurrent(msg.result.endpointID) {
		m.sessions.store(msg.result)
	}
	if m.sessions.prefetchRemaining == 0 && m.sessions.prefetchCancel != nil {
		m.sessions.prefetchCancel()
		m.sessions.prefetchCancel = nil
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
	m.sessions.configSyncPending = false
	cmd := m.beginSpriteSessions(false, nil)
	return m, tea.Batch(cmd, m.spinner.Tick)
}

func (m *Model) beginSpriteSessions(syncConfig bool, cached *cachedSpriteSessions) tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	opID, ctx := m.beginOperation()
	selection := m.sessions.spriteEndpointSelection
	return func() tea.Msg {
		var rows []catalog.Destination
		var err error
		if cached == nil {
			rows, err = operations.ListSpriteSessions(ctx, selection)
		} else {
			rows = cached.rows
			err = cached.err
		}
		configSynced := false
		if err == nil && syncConfig {
			err = operations.SyncSpriteHerdrConfig(ctx, selection)
			configSynced = err == nil
		}
		return spriteSessionsDone{opID: opID, rows: append([]catalog.Destination(nil), rows...), err: err, configSynced: configSynced}
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
	cmd := m.beginSpriteSessions(m.sessions.configSyncPending, nil)
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
	m.sessions.prefetchRemaining = len(targets)
	cmds := make([]tea.Cmd, 0, len(targets))
	for _, target := range targets {
		cmds = append(cmds, func() tea.Msg {
			rows, err := operations.ListSpriteSessions(ctx, target.selection)
			return spriteSessionsPrefetched{
				prefetchID: prefetchID,
				result: cachedSpriteSessions{
					endpointID: target.endpointID,
					rows:       append([]catalog.Destination(nil), rows...),
					err:        err,
				},
			}
		})
	}
	return tea.Batch(cmds...)
}

func (m *Model) cancelSessionPrefetch() {
	if m.sessions.prefetchCancel == nil {
		return
	}
	m.sessions.prefetchCancel()
	m.sessions.prefetchCancel = nil
	m.sessions.prefetchRemaining = 0
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
