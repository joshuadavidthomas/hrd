package ui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	spriteapi "github.com/joshuadavidthomas/hrd/internal/sprites"
)

func newBrowserList(width, height int, isDark bool) list.Model {
	browser := list.New(nil, browserDelegate{}, width, height)
	browser.SetShowTitle(true)
	browser.SetShowStatusBar(false)
	browser.SetShowPagination(true)
	browser.SetShowHelp(false)
	browser.SetShowFilter(true)
	browser.DisableQuitKeybindings()
	applyListStyle(&browser, isDark)
	return browser
}

func newPickerList(items []list.Item, delegate list.ItemDelegate, width, height int, isDark bool) list.Model {
	picker := list.New(items, delegate, width, height)
	picker.SetShowTitle(false)
	picker.SetShowStatusBar(false)
	picker.SetShowPagination(true)
	picker.SetShowHelp(false)
	picker.SetShowFilter(false)
	picker.SetFilteringEnabled(false)
	picker.DisableQuitKeybindings()
	applyListStyle(&picker, isDark)
	return picker
}

func applyListStyle(model *list.Model, isDark bool) {
	styles := list.DefaultStyles(isDark)
	styles.TitleBar = lipgloss.NewStyle().PaddingBottom(1)
	styles.Title = titleStyle
	styles.StatusBar = mutedStyle.PaddingBottom(1)
	styles.StatusEmpty = mutedStyle
	styles.StatusBarActiveFilter = lipgloss.NewStyle()
	styles.StatusBarFilterCount = mutedStyle
	styles.NoItems = mutedStyle
	styles.PaginationStyle = mutedStyle
	styles.ActivePaginationDot = accentStyle.SetString("•")
	styles.InactivePaginationDot = mutedStyle.SetString("•")
	styles.ArabicPagination = mutedStyle
	model.Styles = styles
	model.Paginator.ActiveDot = styles.ActivePaginationDot.String()
	model.Paginator.InactiveDot = styles.InactivePaginationDot.String()
}

func (m *Model) applyTheme() {
	applyListStyle(&m.browser, m.isDark)
	applyListStyle(&m.transportPicker, m.isDark)
	applyListStyle(&m.sshKeyPicker, m.isDark)

	helpStyles := help.DefaultStyles(m.isDark)
	helpStyles.ShortKey = accentStyle.Bold(true)
	helpStyles.ShortDesc = lipgloss.NewStyle().Foreground(lipgloss.LightDark(m.isDark)(lipgloss.Color("240"), lipgloss.Color("245")))
	helpStyles.ShortSeparator = lipgloss.NewStyle().Foreground(lipgloss.LightDark(m.isDark)(lipgloss.Color("238"), lipgloss.Color("240")))
	helpStyles.FullKey = helpStyles.ShortKey
	helpStyles.FullDesc = helpStyles.ShortDesc
	helpStyles.FullSeparator = helpStyles.ShortSeparator
	m.help.Styles = helpStyles

	inputStyles := textinput.DefaultStyles(m.isDark)
	inputStyles.Focused.Prompt = accentStyle
	inputStyles.Blurred.Prompt = mutedStyle
	m.nameInput.SetStyles(inputStyles)
}

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

type browserSectionDelegate struct {
	selectedIndex int
}

func (browserSectionDelegate) Height() int                         { return 1 }
func (browserSectionDelegate) Spacing() int                        { return 0 }
func (browserSectionDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (delegate browserSectionDelegate) Render(w io.Writer, model list.Model, index int, raw list.Item) {
	item, ok := raw.(browserItem)
	if !ok {
		return
	}
	_, _ = fmt.Fprint(w, renderBrowserRow(item, index == delegate.selectedIndex, max(1, model.Width()-1)))
}

type transportItem struct {
	transport catalog.SpriteTransport
	title     string
	badge     string
	detail    string
}

func (item transportItem) FilterValue() string { return item.title }

func transportItems() []list.Item {
	return []list.Item{
		transportItem{transport: catalog.NativePreview, title: "Native", badge: "recommended", detail: "Herdr runs inside the Sprite. No SSH service or key."},
		transportItem{transport: catalog.SSHFallback, title: "SSH", detail: "Local Herdr connects through your selected SSH key."},
	}
}

type transportDelegate struct{}

func (transportDelegate) Height() int                         { return 2 }
func (transportDelegate) Spacing() int                        { return 0 }
func (transportDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (transportDelegate) Render(w io.Writer, model list.Model, index int, raw list.Item) {
	item, ok := raw.(transportItem)
	if !ok {
		return
	}
	_, _ = fmt.Fprint(w, renderTransportOption(item.title, item.badge, item.detail, index == model.Index(), model.Width()))
}

type sshKeyItem struct {
	identity catalog.SSHIdentity
}

func (item sshKeyItem) FilterValue() string {
	return strings.Join([]string{item.identity.Comment, item.identity.Algorithm, item.identity.Fingerprint}, " ")
}

type sshKeyDelegate struct{}

func (sshKeyDelegate) Height() int                         { return 1 }
func (sshKeyDelegate) Spacing() int                        { return 0 }
func (sshKeyDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (sshKeyDelegate) Render(w io.Writer, model list.Model, index int, raw list.Item) {
	item, ok := raw.(sshKeyItem)
	if !ok {
		return
	}
	_, _ = fmt.Fprint(w, renderSSHKeyRow(item.identity, index == model.Index(), model.Width()))
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
	label := renderCell(labelStyle, printable(item.destination.Label), labelWidth)
	state := renderCell(stateStyle, item.symbol+" "+item.state, stateWidth)
	row := markerStyle.Render(marker) + label + strings.Repeat(" ", browserStatusGap) + state
	return clip(row, width)
}

func renderCell(style lipgloss.Style, value string, width int) string {
	value = clip(value, width)
	return style.Render(value) + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
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
	if destination, ok := m.selectedDestination(); ok {
		m.home.endpointSelection = destination.ID
	} else {
		m.home.endpointSelection = ""
	}
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
	if destination, ok := m.selectedDestination(); ok {
		m.sessions.selection = destination.ID
	} else {
		m.sessions.selection = ""
	}
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
				return
			}
		}
	}
	m.browser.Select(0)
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

func (m Model) updateBrowserKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	filterState := m.browser.FilterState()
	filterValue := m.browser.FilterValue()
	var cmd tea.Cmd
	m.browser, cmd = m.browser.Update(msg)
	m.refreshBrowserFilter(filterState, filterValue)
	if m.browser.FilterState() != filterState {
		m.resizeComponents()
	}
	if destination, ok := m.selectedDestination(); ok {
		switch m.active {
		case workflowHome:
			m.home.endpointSelection = destination.ID
		case workflowSessions:
			m.sessions.selection = destination.ID
		}
	}
	return m, cmd
}

func (m Model) browserListHeight(available int) int {
	panelCount := 1
	if !m.showsSprite() {
		panelCount = 0
		if m.home.localSnapshot.loaded {
			panelCount++
		}
		if m.home.spriteSnapshot.loaded {
			panelCount++
		}
		panelCount = max(1, panelCount)
	}
	overhead := panelCount*4 + max(0, panelCount-1)
	if m.browser.FilterState() != list.Unfiltered {
		overhead++
	}
	return max(1, available-overhead)
}

func (m *Model) resizeComponents() {
	surfaceWidth := m.surfaceWidth()
	m.help.SetWidth(surfaceWidth)
	bodyHeight := m.bodyHeight()
	panelWidth := m.browserPanelWidth(surfaceWidth)
	m.browser.SetSize(max(1, panelWidth-2), m.browserListHeight(bodyHeight))
	chooserWidth := max(12, min(68, surfaceWidth))
	m.transportPicker.SetSize(max(1, chooserWidth-2), 4)
	m.sshKeyPicker.SetSize(max(1, surfaceWidth-2), max(1, min(bodyHeight-3, len(m.sshKeyPicker.Items()))))
	m.nameInput.SetWidth(max(1, min(40, surfaceWidth)))
}
