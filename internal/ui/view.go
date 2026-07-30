package ui

import (
	"fmt"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
)

func (m Model) busy() bool {
	if m.home.inventoryRefreshing && m.home.inventoryRefreshVisible {
		return true
	}
	switch m.active {
	case workflowHome:
		return m.home.phase == homeCreatingSprite
	case workflowSessions:
		return m.sessions.phase == sessionsLoading || m.sessions.phase == sessionsRefreshing || m.sessions.phase == sessionsInstallingHerdr
	case workflowConnection:
		return m.connection.phase == connectionStopping || m.connection.phase == connectionPreparing || m.connection.phase == connectionStartingSSHAgent
	default:
		panic("ui: invalid active workflow")
	}
}

var (
	accentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	titleStyle    = lipgloss.NewStyle().Bold(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	runningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	warningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	borderStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// View renders a compact surface centered when the terminal has room.
func (m Model) View() tea.View {
	if m.active == workflowHome && m.home.phase == homeLoading {
		view := tea.NewView("")
		view.AltScreen = true
		view.WindowTitle = "hrd · Herdr sessions"
		return view
	}

	surfaceWidth := m.surfaceWidth()
	m.help.SetWidth(surfaceWidth)

	lines := []string{m.breadcrumbView()}

	footer := m.helpView()
	footerHeight := lipgloss.Height(footer)
	bodyBudget := max(1, m.height-len(lines)-footerHeight-3)
	if body := m.bodyView(surfaceWidth, bodyBudget); body != "" {
		lines = append(lines, "", body)
	}
	if footer != "" {
		lines = append(lines, "", footer)
	}
	surface := fitView(strings.Join(lines, "\n"), surfaceWidth, m.height)
	content := lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, surface)
	if toast := m.toastView(); toast != "" {
		canvas := lipgloss.NewCanvas(m.width, m.height)
		toastY := max(0, min(1, m.height-lipgloss.Height(toast)))
		layers := lipgloss.NewCompositor(
			lipgloss.NewLayer(content),
			lipgloss.NewLayer(toast).X(max(0, (m.width-lipgloss.Width(toast))/2)).Y(toastY).Z(1),
		)
		canvas.Compose(layers)
		content = canvas.Render()
	}
	view := tea.NewView(content)
	view.AltScreen = true
	view.WindowTitle = "hrd · Herdr sessions"
	return view
}

func (m Model) breadcrumbView() string {
	segments := m.breadcrumbSegments()
	if len(segments) == 0 {
		return titleStyle.Render("hrd")
	}
	prefix := titleStyle.Render("hrd") + "  "
	available := max(1, m.surfaceWidth()-lipgloss.Width(prefix))
	context := "/ " + strings.Join(segments, " / ")
	for lipgloss.Width(context) > available && len(segments) > 1 {
		if segments[0] != "…" {
			segments = append([]string{"…"}, segments[1:]...)
		} else if len(segments) > 2 {
			segments = append(segments[:1], segments[2:]...)
		} else {
			segments = segments[1:]
		}
		context = "/ " + strings.Join(segments, " / ")
	}
	return prefix + mutedStyle.Render(clip(context, available))
}

func (m Model) breadcrumbSegments() []string {
	segments := make([]string, 0, 2)
	if m.showsSprite() {
		segments = append(segments, m.spriteName())
	}

	if m.active == workflowConnection {
		switch m.connection.phase {
		case connectionConfirmingStop, connectionStopping, connectionChoosingTransport:
			if m.connection.destination.Label != "" {
				segments = append(segments, printable(m.connection.destination.Label))
			}
		case connectionPreparing, connectionConfirmingHostKey, connectionStartingSSHAgent, connectionAddingSSHKey, connectionChoosingSSHKey, connectionAttaching:
			if m.connection.destination.Label != "" {
				segments = append(segments, printable(m.connection.destination.Label))
			}
		}
	}
	return segments
}

func (m Model) bodyView(width, height int) string {
	switch m.active {
	case workflowHome:
		return m.homeView(width, height)
	case workflowSessions:
		return m.sessionsView(width, height)
	case workflowConnection:
		return m.connectionView(width, height)
	default:
		panic("ui: invalid active workflow")
	}
}

func (m Model) toastView() string {
	notices := make([]string, 0, 3)
	for _, notice := range []string{m.notice, m.home.createNotice, m.refreshNotice} {
		if notice == "" {
			continue
		}
		style := warningStyle
		lower := strings.ToLower(notice)
		if strings.Contains(lower, "failed") || strings.Contains(lower, "couldn’t") {
			style = errorStyle
		}
		notices = append(notices, style.Render("! "+printable(notice)))
	}
	progress := ""
	if m.home.inventoryRefreshing && m.home.inventoryRefreshVisible {
		progress = m.spinner.View() + " " + accentStyle.Render("Refreshing local and Sprite inventory…")
	} else if m.active == workflowSessions && m.sessions.phase == sessionsRefreshing {
		progress = m.spinner.View() + " " + accentStyle.Render("Refreshing "+m.spriteName()+" sessions…")
	}
	messages := append([]string(nil), notices...)
	if progress != "" {
		messages = append(messages, progress)
	}
	if len(messages) == 0 {
		return ""
	}

	width := max(12, min(72, m.width-4))
	contentWidth := max(1, width-4)
	for index, message := range messages {
		messages[index] = clip(message, contentWidth)
	}
	toast := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderStyle.GetForeground()).
		Background(lipgloss.Color("235")).
		Padding(0, 1).
		Width(width).
		Render(strings.Join(messages, "\n"))
	if lipgloss.Height(toast) <= m.height {
		return toast
	}
	return lipgloss.NewStyle().
		Background(lipgloss.Color("235")).
		Width(max(1, m.width)).
		Render(clip(messages[0], max(1, m.width)))
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
	panelWidth := m.browserPanelWidth(width)
	centerPanel := func(panel string) string {
		return lipgloss.PlaceHorizontal(width, lipgloss.Center, panel)
	}
	filter := ""
	if m.browser.FilterState() != list.Unfiltered {
		filter = "Filter  " + m.browser.FilterInput.View()
	}
	visible := m.browser.VisibleItems()
	selected := m.browser.Index()
	panelCount := 1
	if !m.showsSprite() {
		panelCount = 0
		hasLocal := m.browser.FilterState() == list.Unfiltered && m.home.localSnapshot.loaded
		hasSprites := m.browser.FilterState() == list.Unfiltered && m.home.spriteSnapshot.loaded
		for _, raw := range visible {
			item, ok := raw.(browserItem)
			if !ok {
				continue
			}
			switch item.destination.Endpoint.(type) {
			case catalog.LocalEndpoint:
				hasLocal = true
			case catalog.SpriteEndpoint:
				hasSprites = true
			}
		}
		if hasLocal {
			panelCount++
		}
		if hasSprites {
			panelCount++
		}
	}
	layoutOverhead := func(showPage bool) int {
		parts := panelCount
		overhead := panelCount * 2
		if filter != "" {
			parts++
			overhead++
		}
		if showPage {
			parts++
			overhead++
		}
		return overhead + max(0, parts-1)
	}
	maxRows := max(1, height-layoutOverhead(false))
	if len(visible) > maxRows {
		maxRows = max(1, height-layoutOverhead(true))
	}
	start := 0
	if len(visible) > maxRows {
		start = max(0, min(selected-maxRows/2, len(visible)-maxRows))
	}
	end := min(len(visible), start+maxRows)

	type indexedItem struct {
		item  browserItem
		index int
	}
	local := make([]indexedItem, 0)
	sprites := make([]indexedItem, 0)
	sessions := make([]indexedItem, 0)
	for index := start; index < end; index++ {
		item, ok := visible[index].(browserItem)
		if !ok {
			continue
		}
		entry := indexedItem{item: item, index: index}
		switch item.destination.Endpoint.(type) {
		case catalog.LocalEndpoint:
			local = append(local, entry)
		case catalog.SpriteEndpoint:
			if m.showsSprite() {
				sessions = append(sessions, entry)
			} else {
				sprites = append(sprites, entry)
			}
		}
	}

	renderEntries := func(entries []indexedItem) []renderedBrowserItem {
		rows := make([]renderedBrowserItem, len(entries))
		for i, entry := range entries {
			rows[i] = renderedBrowserItem{item: entry.item, selected: entry.index == selected}
		}
		return rows
	}
	parts := make([]string, 0, 4)
	if filter != "" {
		parts = append(parts, centerPanel(lipgloss.NewStyle().Width(panelWidth).Render(clip(filter, panelWidth))))
	}
	if m.showsSprite() {
		badge := fmt.Sprintf("%d", len(m.sessions.spriteSnapshotDetail.rows))
		parts = append(parts, centerPanel(renderBrowserPanel(m.spriteName()+" sessions", badge, renderEntries(sessions), panelWidth, "No Herdr sessions found.")))
	} else {
		if len(local) > 0 || (m.browser.FilterState() == list.Unfiltered && m.home.localSnapshot.loaded) {
			badge := fmt.Sprintf("%d", len(m.home.localSnapshot.rows))
			if !m.home.localSnapshot.current {
				badge = "refresh failed · " + badge
			}
			parts = append(parts, centerPanel(renderBrowserPanel("Local sessions", badge, renderEntries(local), panelWidth, "No local sessions.")))
		}
		if len(sprites) > 0 || (m.browser.FilterState() == list.Unfiltered && m.home.spriteSnapshot.loaded) {
			badge := fmt.Sprintf("%d", len(m.home.spriteSnapshot.rows))
			if !m.home.spriteSnapshot.current {
				badge = "refresh failed · " + badge
			}
			parts = append(parts, centerPanel(renderBrowserPanel("Sprites", badge, renderEntries(sprites), panelWidth, "No Sprites.")))
		}
	}
	if start > 0 || end < len(visible) {
		page := lipgloss.NewStyle().Width(panelWidth).Render(mutedStyle.Render(fmt.Sprintf("Showing %d–%d of %d", start+1, end, len(visible))))
		parts = append(parts, centerPanel(page))
	}
	return fitView(strings.Join(parts, "\n\n"), width, height)
}

type renderedBrowserItem struct {
	item     browserItem
	selected bool
}

func renderBrowserPanel(title, badge string, rows []renderedBrowserItem, width int, empty string) string {
	width = max(12, width)
	innerWidth := width - 2
	badgeText := ""
	if badge != "" {
		badgeText = " " + printable(badge) + " "
	}
	maxTitleWidth := max(1, width-lipgloss.Width(badgeText)-7)
	titleText := " " + clip(printable(title), maxTitleWidth) + " "
	fill := max(1, innerWidth-lipgloss.Width(titleText)-lipgloss.Width(badgeText)-2)
	top := borderStyle.Render("╭─") + titleStyle.Render(titleText) + borderStyle.Render(strings.Repeat("─", fill))
	if badgeText != "" {
		top += mutedStyle.Render(badgeText)
	}
	top += borderStyle.Render("─╮")

	body := make([]string, 0, max(1, len(rows)))
	if len(rows) == 0 {
		line := mutedStyle.Render(empty)
		body = append(body, borderStyle.Render("│")+" "+lipgloss.NewStyle().Width(innerWidth-2).Render(line)+" "+borderStyle.Render("│"))
	} else {
		for _, row := range rows {
			line := renderBrowserRow(row.item, row.selected, innerWidth-2)
			body = append(body, borderStyle.Render("│")+" "+lipgloss.NewStyle().Width(innerWidth-2).Render(line)+" "+borderStyle.Render("│"))
		}
	}
	bottom := borderStyle.Render("╰" + strings.Repeat("─", innerWidth) + "╯")
	return strings.Join(append(append([]string{top}, body...), bottom), "\n")
}

func (m Model) helpView() string {
	var short []key.Binding
	var full [][]key.Binding
	movement := key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑/↓", "move"))
	filtering := key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter"))
	escapeTo := func(destination string) key.Binding {
		return key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", destination))
	}
	cancelTo := func(destination string) key.Binding {
		return key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", destination))
	}
	switch m.active {
	case workflowHome:
		switch m.home.phase {
		case homeNamingSprite:
			short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "create")), escapeTo("home"), m.keys.force}
		case homeReady:
			if m.browser.SettingFilter() {
				short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply")), escapeTo("list"), m.keys.force}
				break
			}
			actions := make([]key.Binding, 0, 2)
			if _, ok := m.selectedDestination(); ok && m.selectedCurrent() {
				actions = append(actions, m.keys.enter)
			}
			if _, ok := m.operations.(spriteOperations); ok && m.home.spriteSnapshot.current && m.home.createBlock == "" {
				actions = append(actions, m.keys.create)
			}
			navigation := []key.Binding{movement, filtering, m.keys.refresh, m.keys.quit, m.keys.help}
			short = append([]key.Binding{movement}, actions...)
			short = append(short, filtering, m.keys.refresh, m.keys.help)
			full = [][]key.Binding{actions, navigation}
		default:
			short = []key.Binding{m.keys.quit}
		}
	case workflowSessions:
		switch m.sessions.phase {
		case sessionsConfirmingInstall:
			short = []key.Binding{m.keys.confirm, cancelTo("home"), m.keys.quit}
		case sessionsLoading, sessionsRefreshing, sessionsInstallingHerdr:
			short = []key.Binding{escapeTo("home"), m.keys.force}
		case sessionsReady:
			if m.browser.SettingFilter() {
				short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply")), escapeTo("list"), m.keys.force}
				break
			}
			actions := make([]key.Binding, 0, 3)
			if _, ok := m.selectedDestination(); ok && m.selectedCurrent() {
				actions = append(actions, m.keys.enter)
			}
			if _, ok := m.selectedSpriteSessionForMutation(); ok {
				actions = append(actions, m.keys.stop, m.keys.restart)
			}
			navigation := []key.Binding{movement, escapeTo("home"), filtering, m.keys.refresh, m.keys.quit, m.keys.help}
			short = []key.Binding{movement, escapeTo("home")}
			short = append(short, actions...)
			short = append(short, filtering, m.keys.refresh, m.keys.help)
			full = [][]key.Binding{actions, navigation}
		default:
			short = []key.Binding{m.keys.quit}
		}
	case workflowConnection:
		switch m.connection.phase {
		case connectionChoosingTransport:
			short = []key.Binding{
				key.NewBinding(key.WithKeys("↑/↓"), key.WithHelp("↑/↓", "choose")),
				key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "connect")),
				escapeTo("sessions"),
				m.keys.quit,
			}
		case connectionPreparing:
			short = []key.Binding{m.keys.quit}
		case connectionConfirmingStop:
			short = []key.Binding{m.keys.confirm, cancelTo("sessions"), m.keys.quit}
		case connectionConfirmingHostKey:
			short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "continue")), cancelTo("sessions"), m.keys.quit}
		case connectionChoosingSSHKey:
			short = []key.Binding{key.NewBinding(key.WithKeys("↑/↓"), key.WithHelp("↑/↓", "choose")), key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "use key")), escapeTo("sessions"), m.keys.quit}
		default:
			short = []key.Binding{m.keys.quit}
		}
	default:
		panic("ui: invalid active workflow")
	}
	if m.help.ShowAll && len(full) > 0 {
		return m.help.FullHelpView(full)
	}
	return m.help.ShortHelpView(short)
}

func (m Model) spriteName() string {
	if endpoint, ok := m.sessions.spriteEndpoint.Endpoint.(catalog.SpriteEndpoint); ok {
		return printable(endpoint.Name)
	}
	if endpoint, ok := m.connection.destination.Endpoint.(catalog.SpriteEndpoint); ok {
		return printable(endpoint.Name)
	}
	return "Sprite"
}

func (m Model) surfaceWidth() int {
	return max(1, min(maxSurfaceWidth, m.width-surfaceHorizontalMargin))
}

func (m Model) browserPanelWidth(available int) int {
	ideal := minBrowserPanelWidth
	for _, raw := range m.browser.Items() {
		item, ok := raw.(browserItem)
		if !ok {
			continue
		}
		rowWidth := lipgloss.Width(printable(item.destination.Label)) +
			browserMarkerWidth + browserStatusGap + browserStatusWidth + browserPanelChrome
		ideal = max(ideal, rowWidth)
	}
	if m.showsSprite() {
		title := m.spriteName() + " sessions"
		badge := fmt.Sprintf("%d", len(m.sessions.spriteSnapshotDetail.rows))
		ideal = max(ideal, lipgloss.Width(title)+lipgloss.Width(badge)+9)
	}
	return max(1, min(available, min(maxBrowserPanelWidth, ideal)))
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
