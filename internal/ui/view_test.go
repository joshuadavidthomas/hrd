package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	"github.com/joshuadavidthomas/hrd/internal/herdr"
	"github.com/joshuadavidthomas/hrd/internal/sprites"
)

func TestWideReadyViewUsesCenteredContentHeightSections(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := ready(t, ops)
	model, _ = update(t, model, tea.WindowSizeMsg{Width: 160, Height: 40})
	plain := ansi.Strip(model.View().Content)
	lines := strings.Split(plain, "\n")
	first, last := -1, -1
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if first < 0 {
			first = index
		}
		last = index
	}
	if first < 5 || last-first > 16 {
		t.Fatalf("content bounds = %d..%d, want a compact vertically centered surface: %q", first, last, plain)
	}
	if leading := len(lines[first]) - len(strings.TrimLeft(lines[first], " ")); leading < 20 {
		t.Fatalf("content starts at column %d, want horizontal centering: %q", leading, lines[first])
	}
	for _, want := range []string{"Local sessions", "Sprites"} {
		if !strings.Contains(plain, want) {
			t.Errorf("grouped view lacks %q: %q", want, plain)
		}
	}
	for _, line := range lines {
		if strings.Contains(line, "default") && !strings.Contains(line, "running") {
			t.Fatalf("session status is not aligned on one row: %q", line)
		}
		if strings.Contains(line, "devbox") && !strings.Contains(line, "running") {
			t.Fatalf("Sprite status is not aligned on one row: %q", line)
		}
	}
}

func TestBrowserPanelWidthTracksNamesWithinBounds(t *testing.T) {
	model := ready(t, &fakeOperations{})
	model.width = 160

	setLabel := func(label string) {
		model.browser.SetItems([]list.Item{browserItem{destination: catalog.Destination{Label: label}}})
	}
	setLabel("short")
	if got := model.browserPanelWidth(model.surfaceWidth()); got != minBrowserPanelWidth {
		t.Fatalf("short-name width = %d, want minimum %d", got, minBrowserPanelWidth)
	}
	setLabel(strings.Repeat("a", 50))
	if got, want := model.browserPanelWidth(model.surfaceWidth()), 50+browserMarkerWidth+browserStatusGap+browserStatusWidth+browserPanelChrome; got != want {
		t.Fatalf("content width = %d, want %d", got, want)
	}
	setLabel(strings.Repeat("a", 100))
	if got := model.browserPanelWidth(model.surfaceWidth()); got != maxBrowserPanelWidth {
		t.Fatalf("long-name width = %d, want maximum %d", got, maxBrowserPanelWidth)
	}
	model.width = 40
	if got, want := model.browserPanelWidth(model.surfaceWidth()), 40-surfaceHorizontalMargin; got != want {
		t.Fatalf("narrow-terminal width = %d, want %d", got, want)
	}
}

func TestWideMainSurfaceKeepsCompleteKeyHints(t *testing.T) {
	model := ready(t, &fakeSpriteOperations{fakeOperations: &fakeOperations{}})
	model, _ = update(t, model, tea.WindowSizeMsg{Width: 160, Height: 40})
	plain := ansi.Strip(model.View().Content)
	for _, hint := range []string{"↑/↓ move", "enter open", "n new Sprite", "/ filter", "r refresh", "? more"} {
		if !strings.Contains(plain, hint) {
			t.Errorf("wide view truncated key hint %q: %q", hint, plain)
		}
	}
	if got := model.surfaceWidth(); got != maxSurfaceWidth {
		t.Fatalf("wide surface width = %d, want %d", got, maxSurfaceWidth)
	}
}

func TestMainSurfaceKeepsNamePaddingAndAlignedStatusColumn(t *testing.T) {
	const spriteName = "sprite-django-language-server-d45b065e"
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(context.Context, int) catalog.Inventory {
		return catalog.Inventory{
			Local:   catalog.SourceResult{Rows: localRows()},
			Sprites: catalog.SourceResult{Rows: spriteEndpointRows(spriteName, sprites.Cold)},
		}
	}
	model := ready(t, ops)
	model, _ = update(t, model, tea.WindowSizeMsg{Width: 160, Height: 40})
	plain := ansi.Strip(model.View().Content)
	wantWidth := max(minBrowserPanelWidth, len(spriteName)+browserMarkerWidth+browserStatusGap+browserStatusWidth+browserPanelChrome)
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "Sprites") && strings.Contains(line, "╭") {
			if got := lipgloss.Width(strings.TrimSpace(line)); got != wantWidth {
				t.Fatalf("Sprite panel width = %d, want %d: %q", got, wantWidth, line)
			}
		}
	}
	nameLine := ""
	localLine := ""
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, spriteName) {
			nameLine = line
		}
		if strings.Contains(line, "default") && strings.Contains(line, "running") {
			localLine = line
		}
	}
	if nameLine == "" || localLine == "" {
		t.Fatalf("rendered rows missing: %q", plain)
	}
	nameEnd := strings.Index(nameLine, spriteName) + len(spriteName)
	statusStart := strings.Index(nameLine, "○ cold")
	if gap := statusStart - nameEnd; gap < browserStatusGap {
		t.Fatalf("name/status gap = %d, want at least %d: %q", gap, browserStatusGap, nameLine)
	}
	localStatus := strings.Index(localLine, "● running")
	localStatusColumn := lipgloss.Width(localLine[:localStatus])
	spriteStatusColumn := lipgloss.Width(nameLine[:statusStart])
	if localStatusColumn != spriteStatusColumn {
		t.Fatalf("status columns differ: local %d Sprite %d\n%s\n%s", localStatusColumn, spriteStatusColumn, localLine, nameLine)
	}
	beforeFilter := model.browserPanelWidth(model.surfaceWidth())
	model.browser.SetFilterText("default")
	if afterFilter := model.browserPanelWidth(model.surfaceWidth()); afterFilter != beforeFilter {
		t.Fatalf("filter changed surface width from %d to %d", beforeFilter, afterFilter)
	}
}

func TestSpriteDetailPanelUsesContentWidth(t *testing.T) {
	t.Run("short name uses minimum", func(t *testing.T) {
		model := spriteReady(t, &fakeSpriteOperations{fakeOperations: &fakeOperations{}})
		model, _ = update(t, model, tea.WindowSizeMsg{Width: 160, Height: 40})
		for _, line := range strings.Split(ansi.Strip(model.View().Content), "\n") {
			if strings.Contains(line, "devbox sessions") && strings.Contains(line, "╭") {
				if got := lipgloss.Width(strings.TrimSpace(line)); got != minBrowserPanelWidth {
					t.Fatalf("detail panel width = %d, want %d: %q", got, minBrowserPanelWidth, line)
				}
				return
			}
		}
		t.Fatal("detail panel heading not rendered")
	})

	t.Run("long Sprite name expands within maximum", func(t *testing.T) {
		name := strings.Repeat("a", 63)
		ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
		ops.inventoryFn = func(context.Context, int) catalog.Inventory {
			return catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: spriteEndpointRows(name, sprites.Running)}}
		}
		model := ready(t, ops)
		model.browser.Select(2)
		model, load := update(t, model, press("enter"))
		model, _ = update(t, model, runFirst(t, load))
		model, _ = update(t, model, tea.WindowSizeMsg{Width: 160, Height: 40})
		for _, line := range strings.Split(ansi.Strip(model.View().Content), "\n") {
			if strings.Contains(line, name) && strings.Contains(line, "╭") {
				if got := lipgloss.Width(strings.TrimSpace(line)); got != maxBrowserPanelWidth {
					t.Fatalf("long-name detail width = %d, want %d: %q", got, maxBrowserPanelWidth, line)
				}
				if !strings.HasSuffix(strings.TrimSpace(line), "╮") {
					t.Fatalf("long-name detail heading lost its border: %q", line)
				}
				return
			}
		}
		t.Fatal("long-name detail panel heading not rendered")
	})
}

func TestRefreshToastDoesNotMoveCenteredContent(t *testing.T) {
	model := ready(t, &fakeOperations{})
	model, _ = update(t, model, tea.WindowSizeMsg{Width: 120, Height: 40})
	position := func(content, text string) (int, int) {
		for row, line := range strings.Split(ansi.Strip(content), "\n") {
			if column := strings.Index(line, text); column >= 0 {
				return row, column
			}
		}
		return -1, -1
	}
	baseRow, baseColumn := position(model.View().Content, "hrd")
	model.home.inventoryRefreshing = true
	model.home.inventoryRefreshVisible = true
	refreshing := model.View().Content
	toastRow, toastColumn := position(refreshing, "hrd")
	if baseRow != toastRow || baseColumn != toastColumn {
		t.Fatalf("refresh moved title from %d,%d to %d,%d", baseRow, baseColumn, toastRow, toastColumn)
	}
	if !strings.Contains(ansi.Strip(refreshing), "Refreshing local and Sprite inventory") {
		t.Fatalf("refresh toast missing: %q", refreshing)
	}
	if toast := model.toastView(); strings.Contains(toast, "\x1b[48") {
		t.Fatalf("toast paints over the terminal background: %q", toast)
	}
}

func TestShortToastKeepsFailureAheadOfRefreshProgress(t *testing.T) {
	for height := 1; height <= 4; height++ {
		model := ready(t, &fakeOperations{})
		model, _ = update(t, model, tea.WindowSizeMsg{Width: 80, Height: height})
		model.showSessions(sessionsRefreshing)
		model.notice = "Attach failed: socket closed"
		plain := ansi.Strip(model.View().Content)
		if !strings.Contains(plain, "Attach failed") {
			t.Errorf("height %d hid failure toast: %q", height, plain)
		}
	}
}

func TestTransportChooserUsesAnAlignedChoicePanel(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := spriteReady(t, ops)
	model, _ = update(t, model, press("enter"))
	plain := ansi.Strip(model.View().Content)
	for _, want := range []string{
		"hrd  / devbox / default",
		"Choose a connection",
		"Select where Herdr itself should run.",
		"Native",
		"recommended",
		"No SSH service or key.",
		"SSH",
		"Local Herdr connects through your selected SSH key.",
		"enter connect",
		"esc sessions",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("transport chooser lacks %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "Alternate transport") || strings.Contains(plain, "attach to default") {
		t.Fatalf("transport chooser retains old copy: %q", plain)
	}
}

func TestBreadcrumbsAndEscapeHintsNameTheReturnDestination(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := ready(t, ops)
	if breadcrumb := ansi.Strip(model.breadcrumbView()); breadcrumb != "hrd" || model.View().WindowTitle != "hrd" {
		t.Fatalf("home breadcrumb/title = %q/%q", breadcrumb, model.View().WindowTitle)
	}
	model, _ = update(t, model, press("/"))
	if breadcrumb := ansi.Strip(model.breadcrumbView()); breadcrumb != "hrd" || !strings.Contains(ansi.Strip(model.helpView()), "esc list") {
		t.Fatalf("filter context = breadcrumb %q help %q", breadcrumb, ansi.Strip(model.helpView()))
	}
	model, _ = update(t, model, press("esc"))

	model, _ = update(t, model, press("n"))
	if breadcrumb := ansi.Strip(model.breadcrumbView()); breadcrumb != "hrd" || !strings.Contains(ansi.Strip(model.helpView()), "esc home") {
		t.Fatalf("new Sprite context = breadcrumb %q help %q", breadcrumb, ansi.Strip(model.helpView()))
	}
	model, _ = update(t, model, press("esc"))

	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	if breadcrumb := ansi.Strip(model.breadcrumbView()); breadcrumb != "hrd" || !strings.Contains(ansi.Strip(model.helpView()), "esc home") {
		t.Fatalf("opening Sprite context = breadcrumb %q help %q", breadcrumb, ansi.Strip(model.helpView()))
	}
	model, _ = update(t, model, runFirst(t, load))
	if help := ansi.Strip(model.helpView()); !strings.Contains(help, "esc home") || model.View().WindowTitle != "hrd · devbox" {
		t.Fatalf("Sprite session help/title = %q/%q", help, model.View().WindowTitle)
	}

	model, _ = update(t, model, press("enter"))
	if breadcrumb := ansi.Strip(model.breadcrumbView()); breadcrumb != "hrd  / devbox / default" || !strings.Contains(ansi.Strip(model.helpView()), "esc sessions") || model.View().WindowTitle != "hrd · devbox · default" {
		t.Fatalf("transport context = breadcrumb %q help %q title %q", breadcrumb, ansi.Strip(model.helpView()), model.View().WindowTitle)
	}
	narrow := model
	narrow.width = 40
	narrowBreadcrumb := ansi.Strip(narrow.breadcrumbView())
	if lipgloss.Width(narrowBreadcrumb) > narrow.surfaceWidth() || !strings.Contains(narrowBreadcrumb, "/ devbox / default") {
		t.Fatalf("narrow breadcrumb lost current location: %q", narrowBreadcrumb)
	}
	tiny := model
	tiny.width = 21
	if breadcrumb := ansi.Strip(tiny.breadcrumbView()); !strings.Contains(breadcrumb, "/ default") || lipgloss.Width(breadcrumb) > tiny.surfaceWidth() {
		t.Fatalf("tiny breadcrumb discarded current location: %q", breadcrumb)
	}
	model, _ = update(t, model, press("esc"))
	model, _ = update(t, model, press("s"))
	if breadcrumb := ansi.Strip(model.breadcrumbView()); breadcrumb != "hrd  / devbox / default" || !strings.Contains(ansi.Strip(model.helpView()), "n/esc sessions") {
		t.Fatalf("stop context = breadcrumb %q help %q", breadcrumb, ansi.Strip(model.helpView()))
	}
}

func TestSelectableRowsShareArrowWithoutBackground(t *testing.T) {
	item := browserItem{
		destination: catalog.Destination{Label: "default"},
		symbol:      "●",
		state:       "running",
		stateStyle:  runningStyle,
	}
	browserRow := renderBrowserRow(item, true, 48)
	transportRow := renderTransportOption("Native", "recommended", "Herdr runs inside the Sprite.", true, 48)
	sshModel := Model{connection: connectionState{sshKeyChoices: []catalog.SSHIdentity{{Comment: "Work key", Algorithm: "ssh-ed25519"}}, sshKeyCursor: 0}}
	sshRows := sshModel.sshKeyView(48)
	for name, rendered := range map[string]string{"browser": browserRow, "transport": transportRow, "SSH": sshRows} {
		if strings.Contains(rendered, "48;5;") {
			t.Errorf("%s selection still has a background: %q", name, rendered)
		}
		if !strings.Contains(rendered, "1;36") {
			t.Errorf("%s selected label is not bold teal: %q", name, rendered)
		}
	}
	browserPlain := ansi.Strip(browserRow)
	transportPlain := ansi.Strip(transportRow)
	sshPlain := ansi.Strip(sshRows)
	if !strings.HasPrefix(browserPlain, "› default") || !strings.Contains(transportPlain, "│ › Native") || !strings.Contains(sshPlain, "› Work key") {
		t.Fatalf("selection markers differ:\nbrowser %q\ntransport %q\nSSH %q", browserPlain, transportPlain, sshPlain)
	}
	if strings.Contains(transportPlain, "● Native") || strings.Contains(transportPlain, "○ Native") {
		t.Fatalf("transport focus still uses radio markers: %q", transportPlain)
	}

	narrow := renderTransportOption("Native", "recommended", "Herdr runs inside the Sprite.", true, 12)
	for _, line := range strings.Split(narrow, "\n") {
		if got := lipgloss.Width(line); got > 12 {
			t.Fatalf("narrow transport row width = %d: %q", got, line)
		}
	}
	if plain := ansi.Strip(narrow); !strings.Contains(plain, "Native") || strings.Contains(plain, "recommended") {
		t.Fatalf("narrow transport did not preserve the primary label: %q", plain)
	}
}

func TestBrowserLayoutUsesCompactSpacingAndConditionalPagination(t *testing.T) {
	if got := (browserDelegate{}).Spacing(); got != 0 {
		t.Fatalf("delegate spacing = %d, want 0", got)
	}
	sessions := make([]herdr.Session, 20)
	for index := range sessions {
		sessions[index] = herdr.Session{Name: fmt.Sprintf("session-%02d", index)}
	}
	rows, err := catalog.LocalDestinations(sessions)
	if err != nil {
		t.Fatal(err)
	}
	ops := &fakeOperations{inventoryFn: func(context.Context, int) catalog.Inventory {
		return catalog.Inventory{Local: catalog.SourceResult{Rows: rows}, Sprites: catalog.SourceResult{Skipped: true}}
	}}
	model := ready(t, ops)
	model, _ = update(t, model, tea.WindowSizeMsg{Width: 50, Height: 12})
	if !model.browser.ShowPagination() || model.browser.Paginator.TotalPages <= 1 {
		t.Fatalf("pagination = shown %v pages %d", model.browser.ShowPagination(), model.browser.Paginator.TotalPages)
	}
	plain := ansi.Strip(model.View().Content)
	if !strings.Contains(plain, "Showing ") || !strings.Contains(plain, "╰") {
		t.Fatalf("custom list clipped pagination or panel border: %q", plain)
	}
	model.browser.SetFilterText("session")
	plain = ansi.Strip(model.View().Content)
	if !strings.Contains(plain, "Filter") || !strings.Contains(plain, "Showing ") || !strings.Contains(plain, "╰") {
		t.Fatalf("filtered list clipped filter, pagination, or panel border: %q", plain)
	}
}

func TestSpinnerProgressesAcrossBusyState(t *testing.T) {
	model := New(context.Background(), &fakeOperations{})
	model.showHome(homeCreatingSprite)
	model.nameInput.SetValue("new-sprite")
	before := model.spinner.View()
	model, cmd := update(t, model, model.spinner.Tick())
	if cmd == nil || model.spinner.View() == before {
		t.Fatalf("spinner did not progress: before %q after %q cmd %v", before, model.spinner.View(), cmd)
	}
	if !strings.Contains(model.View().Content, model.spinner.View()) {
		t.Fatalf("busy view does not render spinner: %q", model.View().Content)
	}
}

func TestHelpOnlyShowsSelectionCapabilities(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := spriteReady(t, ops)
	model.browser.Select(1)
	if help := model.helpView(); !strings.Contains(help, "enter") || !strings.Contains(help, "move") || !strings.Contains(help, "filter") || strings.Contains(help, "restart") || strings.Contains(help, "stop") {
		t.Fatalf("stopped-session help = %q", help)
	}
	model.sessions.spriteSnapshotDetail.current = false
	model.syncSpriteItems(selected(t, model).ID)
	if help := model.helpView(); strings.Contains(help, "enter") || strings.Contains(help, "restart") || strings.Contains(help, "stop") {
		t.Fatalf("stale-session help exposes actions = %q", help)
	}
	for _, phase := range []sessionsPhase{sessionsLoading, sessionsRefreshing, sessionsInstallingHerdr} {
		model.showSessions(phase)
		if help := model.helpView(); !strings.Contains(help, "esc") || !strings.Contains(help, "home") {
			t.Errorf("phase %v help = %q", phase, help)
		}
	}
}

func TestTerminalBoundsAndControlCharacterSanitization(t *testing.T) {
	ops := &fakeOperations{inventoryFn: func(context.Context, int) catalog.Inventory {
		rows := localRows()
		rows[0].Label = "safe\x1b[31m\u202eevil\nnext"
		return catalog.Inventory{Local: catalog.SourceResult{Rows: rows}, Sprites: catalog.SourceResult{Skipped: true}}
	}}
	model := ready(t, ops)
	for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 12}, {Width: 80, Height: 24}} {
		model, _ = update(t, model, size)
		view := model.View()
		assertFits(t, view, size.Width, size.Height)
		if !strings.Contains(view.Content, "╭") || !strings.Contains(view.Content, "╰") {
			t.Fatalf("border was clipped at %dx%d: %q", size.Width, size.Height, view.Content)
		}
		if strings.Contains(view.Content, "\u202e") || strings.Contains(view.Content, "evil\nnext") || !strings.Contains(view.Content, "safe[31mevilnext") {
			t.Fatalf("source text was not sanitized at %dx%d: %q", size.Width, size.Height, view.Content)
		}
	}
}
