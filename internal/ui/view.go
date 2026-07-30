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
	if m.inventoryRefreshing && m.inventoryRefreshVisible {
		return true
	}
	switch m.phase {
	case phaseCreatingSprite, phaseLoadingSprite, phaseRefreshingSprite,
		phaseInstallingSpriteHerdr, phaseStopping, phasePreparing, phaseStartingSSHAgent:
		return true
	default:
		return false
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
	if m.phase == phaseLoading {
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
	if m.screen == screenSprite {
		segments = append(segments, m.spriteName())
	}

	switch m.phase {
	case phaseConfirmingStop, phaseStopping, phaseChoosingSpriteTransport:
		if m.connection.destination.Label != "" {
			segments = append(segments, printable(m.connection.destination.Label))
		}
	case phasePreparing, phaseConfirmingHostKey, phaseStartingSSHAgent, phaseAddingSSHKey, phaseChoosingSSHKey, phaseAttaching:
		if m.connection.prepare.destination.Label != "" {
			segments = append(segments, printable(m.connection.prepare.destination.Label))
		}
	}
	return segments
}

func (m Model) bodyView(width, height int) string {
	switch m.phase {
	case phaseLoading:
		return ""
	case phaseInitialLoadFailed:
		return clip("No inventory source loaded. Press r to retry.", width)
	case phaseNamingSprite:
		return fitView(strings.Join([]string{
			"Name your Sprite",
			m.nameInput.View(),
			mutedStyle.Render("1–63 lowercase letters, digits, and interior hyphens"),
			warningStyle.Render("Creating and waking a Sprite may incur charges."),
		}, "\n"), width, height)
	case phaseCreatingSprite:
		return m.busyLine("Creating "+m.nameInput.Value()+"…", width)
	case phaseLoadingSprite:
		return m.spriteSessionsLoadingView(width, height)
	case phaseConfirmingSpriteInstall:
		return titleStyle.Render("Install Herdr?")
	case phaseInstallingSpriteHerdr:
		return m.busyLine("Installing Herdr…", width)
	case phaseRefreshingSprite:
		return m.listView(width, height)
	case phaseConfirmingStop:
		action := "Stop"
		if m.connection.restart {
			action = "Restart"
		}
		return fitView(strings.Join([]string{
			titleStyle.Render(action + " " + printable(m.connection.destination.Label) + "?"),
			warningStyle.Render("Pane processes in this session will exit."),
		}, "\n"), width, height)
	case phaseStopping:
		action := "Stopping "
		if m.connection.restart {
			action = "Stopping before restart: "
		}
		return m.listWithWork(action+printable(m.connection.destination.Label)+"…", width, height)
	case phaseChoosingSpriteTransport:
		return fitView(m.transportView(width), width, height)
	case phasePreparing:
		if m.connection.prepare.sprite {
			if m.connection.prepare.transport == catalog.SSHFallback {
				return m.busyLine("Setting up SSH on "+m.spriteName()+"… Up to three minutes.", width)
			}
			return fitView(m.transportView(width), width, height)
		}
		return m.busyLine("Preparing local session "+printable(m.connection.prepare.destination.Label)+"…", width)
	case phaseConfirmingHostKey:
		prepared, _ := m.connection.prepared.(catalog.PreparedProcessAttach)
		return fitView(strings.Join([]string{
			titleStyle.Render("Verify SSH host key"),
			"Authenticated through the Sprites API:",
			accentStyle.Render(printable(prepared.Fingerprint)),
			"Continue only if OpenSSH shows this fingerprint.",
		}, "\n"), width, height)
	case phaseStartingSSHAgent:
		return m.busyLine("Starting a private ssh-agent…", width)
	case phaseAddingSSHKey:
		return fitView("ssh-add has the terminal. Complete its prompt to continue.", width, height)
	case phaseChoosingSSHKey:
		return fitView(m.sshKeyView(width), width, height)
	case phaseAttaching:
		if m.connection.prepare.sprite && m.connection.prepare.transport == catalog.NativePreview {
			return m.listView(width, height)
		}
		return "Herdr has the terminal."
	case phaseReady:
		if m.screen == screenSprite {
			return m.listView(width, height)
		}
		if len(m.browser.Items()) == 0 {
			return "No local sessions or Sprite endpoints found."
		}
		return m.listView(width, height)
	default:
		return "Unknown UI state."
	}
}

func (m Model) toastView() string {
	notices := make([]string, 0, 3)
	for _, notice := range []string{m.notice, m.createNotice, m.refreshNotice} {
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
	if m.inventoryRefreshing && m.inventoryRefreshVisible {
		progress = m.spinner.View() + " " + accentStyle.Render("Refreshing local and Sprite inventory…")
	} else if m.phase == phaseRefreshingSprite {
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
	if m.screen == screenEndpoints {
		panelCount = 0
		hasLocal := m.browser.FilterState() == list.Unfiltered && m.localSnapshot.loaded
		hasSprites := m.browser.FilterState() == list.Unfiltered && m.spriteSnapshot.loaded
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
			if m.screen == screenSprite {
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
	if m.screen == screenSprite {
		badge := fmt.Sprintf("%d", len(m.spriteSnapshotDetail.rows))
		parts = append(parts, centerPanel(renderBrowserPanel(m.spriteName()+" sessions", badge, renderEntries(sessions), panelWidth, "No Herdr sessions found.")))
	} else {
		if len(local) > 0 || (m.browser.FilterState() == list.Unfiltered && m.localSnapshot.loaded) {
			badge := fmt.Sprintf("%d", len(m.localSnapshot.rows))
			if !m.localSnapshot.current {
				badge = "refresh failed · " + badge
			}
			parts = append(parts, centerPanel(renderBrowserPanel("Local sessions", badge, renderEntries(local), panelWidth, "No local sessions.")))
		}
		if len(sprites) > 0 || (m.browser.FilterState() == list.Unfiltered && m.spriteSnapshot.loaded) {
			badge := fmt.Sprintf("%d", len(m.spriteSnapshot.rows))
			if !m.spriteSnapshot.current {
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

func (m Model) spriteSessionsLoadingView(width, height int) string {
	panelWidth := m.browserPanelWidth(width)
	loading := m.spinner.View() + " " + mutedStyle.Render("Loading sessions…")
	panel := renderBrowserPanel(m.spriteName()+" sessions", "", nil, panelWidth, loading)
	return fitView(lipgloss.PlaceHorizontal(width, lipgloss.Center, panel), width, height)
}

func (m Model) transportView(width int) string {
	type transportOption struct {
		transport catalog.SpriteTransport
		title     string
		badge     string
		detail    string
	}
	options := []transportOption{
		{
			transport: catalog.NativePreview,
			title:     "Native",
			badge:     "recommended",
			detail:    "Herdr runs inside the Sprite. No SSH service or key.",
		},
		{
			transport: catalog.SSHFallback,
			title:     "SSH",
			detail:    "Local Herdr connects through your selected SSH key.",
		},
	}
	chooserWidth := max(12, min(68, width))
	rows := make([]string, 0, len(options)*3)
	for index, option := range options {
		if index > 0 {
			rows = append(rows, borderStyle.Render("├"+strings.Repeat("─", chooserWidth-2)+"┤"))
		}
		rows = append(rows, renderTransportOption(option.title, option.badge, option.detail, option.transport == m.connection.transport, chooserWidth))
	}
	panel := borderStyle.Render("╭"+strings.Repeat("─", chooserWidth-2)+"╮") + "\n" +
		strings.Join(rows, "\n") + "\n" +
		borderStyle.Render("╰"+strings.Repeat("─", chooserWidth-2)+"╯")
	heading := "Choose a connection"
	if m.connection.restart {
		heading = "Choose a connection for the restarted session"
	}
	parts := []string{
		titleStyle.Render(heading),
		mutedStyle.Render("Select where Herdr itself should run."),
		"",
		panel,
	}
	if m.phase == phasePreparing && m.connection.transport == catalog.NativePreview {
		parts = append(parts, "", m.busyLine("Preparing native connection…", chooserWidth))
	}
	content := strings.Join(parts, "\n")
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, content)
}

func renderTransportOption(title, badge, detail string, selected bool, width int) string {
	contentWidth := max(1, width-4)
	marker := "  "
	markerStyle := lipgloss.NewStyle()
	titleStyleForOption := lipgloss.NewStyle()
	if selected {
		marker = "› "
		markerStyle = accentStyle
		titleStyleForOption = selectedStyle
	}
	badgeStyle := mutedStyle
	detailStyle := mutedStyle

	badgeWidth := lipgloss.Width(badge)
	minimumTitleWidth := min(lipgloss.Width(title), max(1, contentWidth-browserMarkerWidth))
	if badgeWidth > contentWidth-browserMarkerWidth-minimumTitleWidth {
		badge = ""
		badgeWidth = 0
	}
	titleWidth := max(1, contentWidth-browserMarkerWidth-badgeWidth)
	first := markerStyle.Render(marker) + titleStyleForOption.Width(titleWidth).Render(clip(title, titleWidth))
	if badge != "" {
		first += badgeStyle.Render(badge)
	}
	detailWidth := max(1, contentWidth-browserMarkerWidth)
	second := markerStyle.Render(strings.Repeat(" ", browserMarkerWidth)) + detailStyle.Width(detailWidth).Render(clip(detail, detailWidth))
	return borderStyle.Render("│") + " " + first + " " + borderStyle.Render("│") + "\n" +
		borderStyle.Render("│") + " " + second + " " + borderStyle.Render("│")
}

func (m Model) sshKeyView(width int) string {
	source := m.connection.sshKeySource
	if source == "" {
		source = "ssh-agent"
	}
	lines := []string{fmt.Sprintf("%s exposes %d keys. Choose exactly one.", printable(source), len(m.connection.sshKeyChoices))}
	for index, choice := range m.connection.sshKeyChoices {
		selected := index == m.connection.sshKeyCursor
		marker := "  "
		markerStyle := lipgloss.NewStyle()
		labelStyle := lipgloss.NewStyle()
		if selected {
			marker = "› "
			markerStyle = accentStyle
			labelStyle = selectedStyle
		}
		label := printable(choice.Comment)
		if label == "" {
			label = "Unnamed key"
		}
		algorithm := printable(choice.Algorithm)
		algorithmWidth := min(lipgloss.Width(algorithm), max(1, width-browserMarkerWidth-browserStatusGap-1))
		labelWidth := max(1, width-browserMarkerWidth-browserStatusGap-algorithmWidth)
		row := markerStyle.Render(marker) +
			labelStyle.Width(labelWidth).Render(clip(label, labelWidth)) +
			strings.Repeat(" ", browserStatusGap) +
			mutedStyle.Width(algorithmWidth).Render(clip(algorithm, algorithmWidth))
		lines = append(lines, row)
	}
	if m.connection.sshKeyCursor >= 0 && m.connection.sshKeyCursor < len(m.connection.sshKeyChoices) {
		lines = append(lines, mutedStyle.Render("Fingerprint ")+accentStyle.Render(printable(m.connection.sshKeyChoices[m.connection.sshKeyCursor].Fingerprint)))
	}
	return strings.Join(lines, "\n")
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
	switch m.phase {
	case phaseNamingSprite:
		short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "create")), escapeTo("home"), m.keys.force}
	case phaseChoosingSpriteTransport:
		short = []key.Binding{
			key.NewBinding(key.WithKeys("↑/↓"), key.WithHelp("↑/↓", "choose")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "connect")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "sessions")),
			m.keys.quit,
		}
	case phasePreparing:
		short = []key.Binding{m.keys.quit}
	case phaseConfirmingSpriteInstall:
		short = []key.Binding{m.keys.confirm, cancelTo("home"), m.keys.quit}
	case phaseConfirmingStop:
		short = []key.Binding{m.keys.confirm, cancelTo("sessions"), m.keys.quit}
	case phaseConfirmingHostKey:
		short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "continue")), cancelTo("sessions"), m.keys.quit}
	case phaseChoosingSSHKey:
		short = []key.Binding{key.NewBinding(key.WithKeys("↑/↓"), key.WithHelp("↑/↓", "choose")), key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "use key")), escapeTo("sessions"), m.keys.quit}
	case phaseLoadingSprite, phaseRefreshingSprite, phaseInstallingSpriteHerdr:
		short = []key.Binding{escapeTo("home"), m.keys.force}
	case phaseReady:
		if m.browser.SettingFilter() {
			short = []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply")), escapeTo("list"), m.keys.force}
		} else if m.screen == screenSprite {
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
		} else {
			actions := make([]key.Binding, 0, 2)
			if _, ok := m.selectedDestination(); ok && m.selectedCurrent() {
				actions = append(actions, m.keys.enter)
			}
			if _, ok := m.operations.(spriteOperations); ok && m.spriteSnapshot.current && m.createBlock == "" {
				actions = append(actions, m.keys.create)
			}
			navigation := []key.Binding{movement, filtering, m.keys.refresh, m.keys.quit, m.keys.help}
			short = append([]key.Binding{movement}, actions...)
			short = append(short, filtering, m.keys.refresh, m.keys.help)
			full = [][]key.Binding{actions, navigation}
		}
	default:
		short = []key.Binding{m.keys.quit}
	}
	if m.help.ShowAll && len(full) > 0 {
		return m.help.FullHelpView(full)
	}
	return m.help.ShortHelpView(short)
}

func (m Model) spriteName() string {
	if endpoint, ok := m.spriteEndpoint.Endpoint.(catalog.SpriteEndpoint); ok {
		return printable(endpoint.Name)
	}
	if endpoint, ok := m.connection.prepare.destination.Endpoint.(catalog.SpriteEndpoint); ok {
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
	if m.screen == screenSprite {
		title := m.spriteName() + " sessions"
		badge := fmt.Sprintf("%d", len(m.spriteSnapshotDetail.rows))
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
