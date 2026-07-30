package ui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	spriteapi "github.com/joshuadavidthomas/hrd/internal/sprites"
)

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
	if !ok || !m.sessions.spriteSnapshotDetail.current {
		return catalog.Destination{}, false
	}
	status, ok := destination.Status.(catalog.SpriteSessionStatus)
	return destination, ok && status.Running
}

func (m *Model) syncEndpointItems(selected catalog.DestinationID) {
	items := make([]list.Item, 0, len(m.home.destinations))
	for _, destination := range m.home.destinations {
		items = append(items, m.endpointBrowserItem(destination, !m.destinationCurrent(destination)))
	}
	m.replaceBrowserItems(items)
	m.selectListID(selected)
	m.home.endpointSelection = m.selectedID
	m.resizeComponents()
}

func (m *Model) syncSpriteItems(selected catalog.DestinationID) {
	items := make([]list.Item, 0, len(m.sessions.spriteSnapshotDetail.rows))
	for _, destination := range m.sessions.spriteSnapshotDetail.rows {
		status, _ := destination.Status.(catalog.SpriteSessionStatus)
		item := browserItem{
			destination: destination,
			kind:        "Session",
			symbol:      "○",
			state:       "stopped",
			stateStyle:  mutedStyle,
			stale:       !m.sessions.spriteSnapshotDetail.current,
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
		return m.home.localSnapshot.current
	case catalog.SpriteEndpoint:
		return m.home.spriteSnapshot.current
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

func (m *Model) resizeComponents() {
	surfaceWidth := m.surfaceWidth()
	m.browser.SetWidth(surfaceWidth)
	m.browser.SetHeight(max(1, m.height-9))
	m.help.SetWidth(surfaceWidth)
	m.nameInput.SetWidth(max(1, min(40, surfaceWidth)))
}
