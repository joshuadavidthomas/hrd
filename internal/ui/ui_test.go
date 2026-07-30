package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	"github.com/joshuadavidthomas/hrd/internal/herdr"
	"github.com/joshuadavidthomas/hrd/internal/sprites"
	"github.com/joshuadavidthomas/hrd/internal/sshagent"
)

type fakeOperations struct {
	inventoryCalls atomic.Int32
	prepareCalls   atomic.Int32
	inventoryFn    func(context.Context, int) catalog.Inventory
	prepareFn      func(context.Context, catalog.Destination, int) (catalog.PreparedAttach, error)
}

func (f *fakeOperations) Inventory(ctx context.Context) catalog.Inventory {
	call := int(f.inventoryCalls.Add(1))
	if f.inventoryFn != nil {
		return f.inventoryFn(ctx, call)
	}
	return catalog.Inventory{
		Local:   catalog.SourceResult{Rows: localRows()},
		Sprites: catalog.SourceResult{Skipped: true},
	}
}

func (f *fakeOperations) Prepare(ctx context.Context, destination catalog.Destination) (catalog.PreparedAttach, error) {
	call := int(f.prepareCalls.Add(1))
	if f.prepareFn != nil {
		return f.prepareFn(ctx, destination, call)
	}
	return catalog.Prepare(destination)
}

type fakeSpriteOperations struct {
	*fakeOperations
	inventoryCalls     atomic.Int32
	createCalls        atomic.Int32
	listSpriteCalls    atomic.Int32
	installCalls       atomic.Int32
	stopCalls          atomic.Int32
	prepareSpriteCalls atomic.Int32
	inventoryFn        func(context.Context, int) catalog.Inventory
	createFn           func(context.Context, string, int) sprites.CreateResult
	listSpriteFn       func(context.Context, catalog.SpriteEndpointSelection, int) ([]catalog.Destination, error)
	installFn          func(context.Context, catalog.SpriteEndpointSelection, int) error
	stopFn             func(context.Context, catalog.SpriteSessionSelection, int) error
	prepareSpriteFn    func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error)
	preparedSelection  catalog.SpriteSessionSelection
	preparedTransport  catalog.SpriteTransport
}

func (f *fakeSpriteOperations) Inventory(ctx context.Context) catalog.Inventory {
	call := int(f.inventoryCalls.Add(1))
	if f.inventoryFn != nil {
		return f.inventoryFn(ctx, call)
	}
	return catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", sprites.Running)}}
}

func (f *fakeSpriteOperations) Create(ctx context.Context, name string) sprites.CreateResult {
	call := int(f.createCalls.Add(1))
	if f.createFn != nil {
		return f.createFn(ctx, name, call)
	}
	return sprites.CreateResult{Name: name, Confirmed: true}
}

func (f *fakeSpriteOperations) ListSpriteSessions(ctx context.Context, endpoint catalog.SpriteEndpointSelection) ([]catalog.Destination, error) {
	call := int(f.listSpriteCalls.Add(1))
	if f.listSpriteFn != nil {
		return f.listSpriteFn(ctx, endpoint, call)
	}
	return spriteSessionRows("devbox", sprites.Running), nil
}

func (f *fakeSpriteOperations) InstallSpriteHerdr(ctx context.Context, endpoint catalog.SpriteEndpointSelection) error {
	call := int(f.installCalls.Add(1))
	if f.installFn != nil {
		return f.installFn(ctx, endpoint, call)
	}
	return nil
}

func (f *fakeSpriteOperations) StopSpriteSession(ctx context.Context, selection catalog.SpriteSessionSelection) error {
	call := int(f.stopCalls.Add(1))
	if f.stopFn != nil {
		return f.stopFn(ctx, selection, call)
	}
	return nil
}

func (f *fakeSpriteOperations) PrepareSprite(ctx context.Context, selection catalog.SpriteSessionSelection, transport catalog.SpriteTransport) (catalog.PreparedAttach, error) {
	call := int(f.prepareSpriteCalls.Add(1))
	f.preparedSelection, f.preparedTransport = selection, transport
	if f.prepareSpriteFn != nil {
		return f.prepareSpriteFn(ctx, selection, transport, call)
	}
	return nil, errors.New("unexpected Sprite preparation")
}

type fakeSSHOperations struct {
	*fakeSpriteOperations
	addCalls   atomic.Int32
	addCommand *exec.Cmd
	addErr     error
	selected   string
	selectErr  error
}

func (f *fakeSSHOperations) SSHAddCommand(context.Context) (*exec.Cmd, error) {
	f.addCalls.Add(1)
	return f.addCommand, f.addErr
}
func (f *fakeSSHOperations) SelectSSHKey(fingerprint string) error {
	f.selected = fingerprint
	return f.selectErr
}

type fakeTerminalCommand struct{}

func (*fakeTerminalCommand) Run() error          { return nil }
func (*fakeTerminalCommand) SetStdin(io.Reader)  {}
func (*fakeTerminalCommand) SetStdout(io.Writer) {}
func (*fakeTerminalCommand) SetStderr(io.Writer) {}

func TestInitialAsyncLoadUsesBubblesList(t *testing.T) {
	ops := &fakeOperations{}
	model := New(context.Background(), ops)
	if model.phase != phaseLoading || model.operationID != 1 || len(model.browser.Items()) != 0 {
		t.Fatalf("new model phase/id/items = %v/%d/%d", model.phase, model.operationID, len(model.browser.Items()))
	}
	if content := model.View().Content; !strings.Contains(content, "Loading local sessions") {
		t.Fatalf("loading view = %q", content)
	}
	model, cmd := update(t, model, run(t, model.initialCmd))
	if cmd != nil || model.phase != phaseReady || len(model.browser.Items()) != 2 || ops.inventoryCalls.Load() != 1 {
		t.Fatalf("loaded phase/items/calls/cmd = %v/%d/%d/%v", model.phase, len(model.browser.Items()), ops.inventoryCalls.Load(), cmd)
	}
	view := model.View()
	for _, want := range []string{"default", "Local", "running", "alpha", "stopped", "enter", "refresh"} {
		if !strings.Contains(view.Content, want) {
			t.Errorf("ready view lacks %q: %q", want, view.Content)
		}
	}
	if !view.AltScreen || view.WindowTitle == "" {
		t.Fatalf("view flags = alt %v title %q", view.AltScreen, view.WindowTitle)
	}
}

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
	baseRow, baseColumn := position(model.View().Content, "hrd  / Sessions & Sprites")
	model.inventoryRefreshing = true
	model.inventoryRefreshVisible = true
	refreshing := model.View().Content
	toastRow, toastColumn := position(refreshing, "hrd  / Sessions & Sprites")
	if baseRow != toastRow || baseColumn != toastColumn {
		t.Fatalf("refresh moved title from %d,%d to %d,%d", baseRow, baseColumn, toastRow, toastColumn)
	}
	if !strings.Contains(ansi.Strip(refreshing), "Refreshing local and Sprite inventory") {
		t.Fatalf("refresh toast missing: %q", refreshing)
	}
}

func TestShortToastKeepsFailureAheadOfRefreshProgress(t *testing.T) {
	for height := 1; height <= 4; height++ {
		model := ready(t, &fakeOperations{})
		model, _ = update(t, model, tea.WindowSizeMsg{Width: 80, Height: height})
		model.phase = phaseRefreshingSprite
		model.notice = "Attach failed: socket closed"
		plain := ansi.Strip(model.View().Content)
		if !strings.Contains(plain, "Attach failed") {
			t.Errorf("height %d hid failure toast: %q", height, plain)
		}
	}
}

func TestPartialSourceFailureRetainsStaleRowsAndDisablesTheirActions(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(_ context.Context, call int) catalog.Inventory {
		result := catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}}
		if call == 1 {
			result.Sprites.Rows = spriteEndpointRows("devbox", sprites.Warm)
		} else {
			result.Sprites.Err = errors.New("provider unavailable")
		}
		return result
	}
	model := ready(t, ops)
	model.browser.Select(2)
	model.selectedID = selected(t, model).ID
	model, refresh := update(t, model, press("r"))
	model, _ = update(t, model, runFirst(t, refresh))
	if model.phase != phaseReady || !model.localSnapshot.current || model.spriteSnapshot.current {
		t.Fatalf("snapshot current flags = local %v Sprite %v", model.localSnapshot.current, model.spriteSnapshot.current)
	}
	if selected(t, model).Label != "devbox" {
		t.Fatalf("selection was not retained: %q", selected(t, model).Label)
	}
	content := model.View().Content
	for _, want := range []string{"devbox", "stale", "provider unavailable", "refresh failed"} {
		if !strings.Contains(content, want) {
			t.Errorf("stale view lacks %q: %q", want, content)
		}
	}
	unchanged, cmd := update(t, model, press("enter"))
	if cmd != nil || unchanged.screen != screenEndpoints || ops.listSpriteCalls.Load() != 0 {
		t.Fatal("stale Sprite endpoint started a state-sensitive effect")
	}
}

func TestLocalSourceFailureRetainsSpritesAndRecovers(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(_ context.Context, call int) catalog.Inventory {
		result := catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", sprites.Running)}}
		if call == 2 {
			result.Local.Rows = nil
			result.Local.Err = errors.New("local unavailable")
		}
		return result
	}
	model := ready(t, ops)
	model, refresh := update(t, model, press("r"))
	model, _ = update(t, model, runFirst(t, refresh))
	if model.localSnapshot.current || !model.spriteSnapshot.current || len(model.browser.Items()) != 3 {
		t.Fatalf("partial local failure = local %v Sprite %v items %d", model.localSnapshot.current, model.spriteSnapshot.current, len(model.browser.Items()))
	}
	model, refresh = update(t, model, press("r"))
	model, _ = update(t, model, runFirst(t, refresh))
	if !model.localSnapshot.current || !model.spriteSnapshot.current || model.refreshNotice != "" {
		t.Fatalf("source recovery = local %v Sprite %v warning %q", model.localSnapshot.current, model.spriteSnapshot.current, model.refreshNotice)
	}
}

func TestTotalInitialFailureRetriesToReady(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(_ context.Context, call int) catalog.Inventory {
		if call == 1 {
			return catalog.Inventory{Local: catalog.SourceResult{Err: errors.New("local down")}, Sprites: catalog.SourceResult{Err: errors.New("Sprites down")}}
		}
		return catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", sprites.Running)}}
	}
	model := New(context.Background(), ops)
	model, _ = update(t, model, run(t, model.initialCmd))
	if model.phase != phaseInitialLoadFailed || !strings.Contains(model.View().Content, "local down") || !strings.Contains(model.View().Content, "Sprites down") {
		t.Fatalf("initial failure = phase %v view %q", model.phase, model.View().Content)
	}
	model, retry := update(t, model, press("r"))
	model, _ = update(t, model, runFirst(t, retry))
	if model.phase != phaseReady || len(model.browser.Items()) != 3 {
		t.Fatalf("initial retry = phase %v items %d", model.phase, len(model.browser.Items()))
	}
}

func TestBackgroundRefreshIsSilentAndUpdatesInventory(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(_ context.Context, call int) catalog.Inventory {
		state := sprites.Running
		if call > 1 {
			state = sprites.Cold
		}
		return catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", state)}}
	}
	model := ready(t, ops)
	oldID := model.operationID

	model, refresh := update(t, model, refreshTick(time.Now()))
	if refresh == nil || !model.inventoryRefreshing || model.inventoryRefreshVisible || model.operationID <= oldID {
		t.Fatalf("background refresh = cmd %v refreshing %v visible %v id %d→%d", refresh, model.inventoryRefreshing, model.inventoryRefreshVisible, oldID, model.operationID)
	}
	view := ansi.Strip(model.View().Content)
	if strings.Contains(view, "Refreshing") || strings.Contains(view, "checked") || strings.Contains(view, "ago") {
		t.Fatalf("background refresh exposed progress or a timer: %q", view)
	}
	model, _ = update(t, model, runFirst(t, refresh))
	if model.inventoryRefreshing || ops.inventoryCalls.Load() != 2 {
		t.Fatalf("background refresh completion = refreshing %v calls %d", model.inventoryRefreshing, ops.inventoryCalls.Load())
	}
	if view := ansi.Strip(model.View().Content); !strings.Contains(view, "cold") || strings.Contains(view, "checked") || strings.Contains(view, "ago") {
		t.Fatalf("background refresh did not quietly update lifecycle: %q", view)
	}
}

func TestBackgroundRefreshFailureUsesPanelStateWithoutToast(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(_ context.Context, call int) catalog.Inventory {
		if call > 1 {
			return catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Err: errors.New("provider unavailable")}}
		}
		return catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", sprites.Running)}}
	}
	model := ready(t, ops)
	model, refresh := update(t, model, refreshTick(time.Now()))
	model, _ = update(t, model, runFirst(t, refresh))
	view := ansi.Strip(model.View().Content)
	if model.spriteSnapshot.current || !strings.Contains(view, "stale") || !strings.Contains(view, "refresh failed") {
		t.Fatalf("silent failure did not mark panel stale: current %v view %q", model.spriteSnapshot.current, view)
	}
	if strings.Contains(view, "provider unavailable") || strings.Contains(view, "Refreshing") {
		t.Fatalf("silent failure rendered a toast: %q", view)
	}
}

func TestNamingSupersedesBackgroundRefresh(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := ready(t, ops)
	model, _ = update(t, model, refreshTick(time.Now()))
	refreshID := model.operationID

	model, _ = update(t, model, press("n"))
	model, _ = update(t, model, press("x"))
	if model.phase != phaseNamingSprite || model.nameInput.Value() != "x" || model.inventoryRefreshing || model.operationID <= refreshID {
		t.Fatalf("naming did not supersede refresh = phase %v value %q refreshing %v id %d→%d", model.phase, model.nameInput.Value(), model.inventoryRefreshing, refreshID, model.operationID)
	}
	model, cmd := update(t, model, inventoryDone{opID: refreshID, inventory: catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", sprites.Cold)}}})
	if cmd != nil || model.phase != phaseNamingSprite || model.nameInput.Value() != "x" {
		t.Fatalf("late background refresh closed naming: phase %v value %q cmd %v", model.phase, model.nameInput.Value(), cmd)
	}
}

func TestFilterOpenBeforeBackgroundRefreshSurvivesCompletion(t *testing.T) {
	ops := &fakeOperations{}
	model := ready(t, ops)
	model, _ = update(t, model, press("/"))
	for _, letter := range "alp" {
		model, _ = update(t, model, press(string(letter)))
	}
	model, refresh := update(t, model, refreshTick(time.Now()))
	if refresh == nil || !model.inventoryRefreshing || model.inventoryRefreshVisible {
		t.Fatalf("filter blocked silent refresh = cmd %v refreshing %v visible %v", refresh, model.inventoryRefreshing, model.inventoryRefreshVisible)
	}
	model, _ = update(t, model, runFirst(t, refresh))
	if !model.browser.SettingFilter() || model.browser.FilterValue() != "alp" || len(model.browser.VisibleItems()) != 1 {
		t.Fatalf("filter after background refresh = setting %v query %q items %d", model.browser.SettingFilter(), model.browser.FilterValue(), len(model.browser.VisibleItems()))
	}
}

func TestFilterStartedDuringEndpointRefreshSurvivesCompletion(t *testing.T) {
	ops := &fakeOperations{}
	model := ready(t, ops)
	model, refresh := update(t, model, press("r"))
	if !model.inventoryRefreshing || refresh == nil {
		t.Fatalf("background refresh = refreshing %v cmd %v", model.inventoryRefreshing, refresh)
	}

	model, _ = update(t, model, press("/"))
	for _, letter := range "alp" {
		model, _ = update(t, model, press(string(letter)))
	}
	if !model.browser.SettingFilter() || model.browser.FilterValue() != "alp" {
		t.Fatalf("active filter = setting %v query %q", model.browser.SettingFilter(), model.browser.FilterValue())
	}

	model, _ = update(t, model, runFirst(t, refresh))
	if !model.browser.SettingFilter() || model.browser.FilterValue() != "alp" {
		t.Fatalf("filter after refresh = setting %v query %q", model.browser.SettingFilter(), model.browser.FilterValue())
	}
	if got := model.browser.VisibleItems(); len(got) != 1 || selected(t, model).Label != "alpha" {
		t.Fatalf("filtered items/selection = %d/%q", len(got), selected(t, model).Label)
	}
}

func TestInventoryRefreshKeepsReadyInteractionsAndCancelsOnNewEffect(t *testing.T) {
	started := make(chan struct{})
	ops := &fakeOperations{}
	ops.inventoryFn = func(ctx context.Context, call int) catalog.Inventory {
		if call == 1 {
			return catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Skipped: true}}
		}
		close(started)
		<-ctx.Done()
		return catalog.Inventory{Local: catalog.SourceResult{Err: ctx.Err()}, Sprites: catalog.SourceResult{Skipped: true}}
	}
	model := ready(t, ops)
	model, refresh := update(t, model, press("r"))
	if model.phase != phaseReady || !model.inventoryRefreshing || !strings.Contains(model.View().Content, "Refreshing local") {
		t.Fatalf("refresh blocked view = phase %v refreshing %v view %q", model.phase, model.inventoryRefreshing, model.View().Content)
	}
	model, _ = update(t, model, press("down"))
	if selected(t, model).Label != "alpha" {
		t.Fatal("list navigation froze during inventory refresh")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- runFirst(t, refresh) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	model, prepare := update(t, model, press("enter"))
	if model.phase != phasePreparing || model.inventoryRefreshing || prepare == nil {
		t.Fatalf("new effect did not supersede refresh = phase %v refreshing %v cmd %v", model.phase, model.inventoryRefreshing, prepare)
	}
	select {
	case completion := <-done:
		model, stale := update(t, model, completion)
		if stale != nil || model.phase != phasePreparing {
			t.Fatal("canceled refresh completion changed active effect")
		}
	case <-time.After(time.Second):
		t.Fatal("superseded refresh was not canceled")
	}
}

func TestNavigationBetweenEndpointAndSpriteScreensResetsFilter(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := ready(t, ops)
	model.browser.SetFilterText("devbox")
	model, load := update(t, model, press("enter"))
	if model.browser.FilterValue() != "" || model.browser.SettingFilter() {
		t.Fatalf("endpoint filter survived drill-in = setting %v query %q", model.browser.SettingFilter(), model.browser.FilterValue())
	}
	model, _ = update(t, model, runFirst(t, load))
	model.browser.SetFilterText("work")
	model, reconcile := update(t, model, press("esc"))
	if model.screen != screenEndpoints || model.browser.FilterValue() != "" || model.browser.SettingFilter() || reconcile == nil {
		t.Fatalf("Sprite filter survived back navigation = screen %v setting %v query %q cmd %v", model.screen, model.browser.SettingFilter(), model.browser.FilterValue(), reconcile)
	}
	model, _ = update(t, model, runFirst(t, reconcile))
}

func TestSpriteRefreshPreservesAppliedFilter(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := spriteReady(t, ops)
	model.browser.SetFilterText("work")
	if model.browser.FilterValue() != "work" || len(model.browser.VisibleItems()) != 1 {
		t.Fatalf("initial filter = %q with %d items", model.browser.FilterValue(), len(model.browser.VisibleItems()))
	}

	model, refresh := update(t, model, press("r"))
	model, _ = update(t, model, runFirst(t, refresh))
	if model.browser.FilterValue() != "work" || len(model.browser.VisibleItems()) != 1 || selected(t, model).Label != "work" {
		t.Fatalf("filter after Sprite refresh = %q with %d items, selection %q", model.browser.FilterValue(), len(model.browser.VisibleItems()), selected(t, model).Label)
	}
}

func TestSpriteDrillInLoadsOnlySelectedSpriteSessions(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := ready(t, ops)
	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	if model.screen != screenSprite || model.phase != phaseLoadingSprite || load == nil || ops.listSpriteCalls.Load() != 0 {
		t.Fatalf("drill phase/screen/calls/cmd = %v/%v/%d/%v", model.phase, model.screen, ops.listSpriteCalls.Load(), load)
	}
	for _, want := range []string{"Waking devbox", "checking Herdr", "three minutes"} {
		if !strings.Contains(model.View().Content, want) {
			t.Errorf("loading copy lacks %q: %q", want, model.View().Content)
		}
	}
	model, _ = update(t, model, runFirst(t, load))
	if model.phase != phaseReady || len(model.browser.Items()) != 2 || ops.listSpriteCalls.Load() != 1 || ops.prepareSpriteCalls.Load() != 0 {
		t.Fatalf("detail state/items/list/prepare = %v/%d/%d/%d", model.phase, len(model.browser.Items()), ops.listSpriteCalls.Load(), ops.prepareSpriteCalls.Load())
	}
	for _, want := range []string{"default", "running", "work", "stopped"} {
		if !strings.Contains(model.View().Content, want) {
			t.Errorf("detail view lacks %q: %q", want, model.View().Content)
		}
	}
}

func TestMissingHerdrRequiresExplicitInstallConfirmation(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.listSpriteFn = func(_ context.Context, _ catalog.SpriteEndpointSelection, call int) ([]catalog.Destination, error) {
		if call == 1 {
			return nil, catalog.ErrSpriteHerdrMissing
		}
		return spriteSessionRows("devbox", sprites.Running), nil
	}
	model := ready(t, ops)
	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	model, _ = update(t, model, runFirst(t, load))
	if model.phase != phaseConfirmingSpriteInstall || ops.installCalls.Load() != 0 {
		t.Fatalf("missing Herdr phase/install calls = %v/%d", model.phase, ops.installCalls.Load())
	}
	for _, want := range []string{"Install Herdr", "download and run", "https://herdr.dev/install.sh"} {
		if !strings.Contains(model.View().Content, want) {
			t.Errorf("install confirmation lacks %q: %q", want, model.View().Content)
		}
	}
	model, cmd := update(t, model, press("n"))
	if cmd == nil || model.screen != screenEndpoints || model.phase != phaseReady || ops.installCalls.Load() != 0 {
		t.Fatalf("install rejection = screen %v phase %v calls %d cmd %v", model.screen, model.phase, ops.installCalls.Load(), cmd)
	}
	model, _ = update(t, model, runFirst(t, cmd))

	model.browser.Select(2)
	model, load = update(t, model, press("enter"))
	ops.listSpriteFn = func(_ context.Context, _ catalog.SpriteEndpointSelection, _ int) ([]catalog.Destination, error) {
		return nil, catalog.ErrSpriteHerdrMissing
	}
	model, _ = update(t, model, runFirst(t, load))
	model, install := update(t, model, press("y"))
	if model.phase != phaseInstallingSpriteHerdr || install == nil || ops.installCalls.Load() != 0 {
		t.Fatalf("install start = phase %v calls %d cmd %v", model.phase, ops.installCalls.Load(), install)
	}
	ops.listSpriteFn = func(_ context.Context, _ catalog.SpriteEndpointSelection, _ int) ([]catalog.Destination, error) {
		return spriteSessionRows("devbox", sprites.Running), nil
	}
	model, relist := update(t, model, runFirst(t, install))
	if model.phase != phaseLoadingSprite || relist == nil || ops.installCalls.Load() != 1 {
		t.Fatalf("install completion = phase %v calls %d cmd %v", model.phase, ops.installCalls.Load(), relist)
	}
	model, _ = update(t, model, runFirst(t, relist))
	if model.phase != phaseReady || len(model.browser.Items()) != 2 {
		t.Fatalf("post-install list = phase %v items %d", model.phase, len(model.browser.Items()))
	}
}

func TestSpriteDetailSnapshotIsResetAcrossEndpoints(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(context.Context, int) catalog.Inventory {
		rows := append(spriteEndpointRows("alpha", sprites.Running), spriteEndpointRows("beta", sprites.Warm)...)
		return catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: rows}}
	}
	ops.listSpriteFn = func(_ context.Context, endpoint catalog.SpriteEndpointSelection, _ int) ([]catalog.Destination, error) {
		alpha, _ := catalog.SelectSpriteEndpoint(spriteEndpointRows("alpha", sprites.Running)[0])
		if reflect.DeepEqual(endpoint, alpha) {
			return spriteSessionRows("alpha", sprites.Running), nil
		}
		return nil, errors.New("beta unavailable")
	}
	model := ready(t, ops)
	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	model, _ = update(t, model, runFirst(t, load))
	if len(model.browser.Items()) != 2 {
		t.Fatalf("alpha detail items = %d", len(model.browser.Items()))
	}
	model, reconcile := update(t, model, press("esc"))
	model, _ = update(t, model, runFirst(t, reconcile))
	model.browser.Select(3)
	model, load = update(t, model, press("enter"))
	if len(model.browser.Items()) != 0 || model.spriteSnapshotDetail.loaded {
		t.Fatal("beta load exposed alpha snapshot")
	}
	model, _ = update(t, model, runFirst(t, load))
	if model.phase != phaseReady || len(model.browser.Items()) != 0 || strings.Contains(model.View().Content, "default") {
		t.Fatalf("failed beta detail leaked alpha rows: %q", model.View().Content)
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

func TestSelectableRowsShareArrowWithoutBackground(t *testing.T) {
	item := browserItem{
		destination: catalog.Destination{Label: "default"},
		symbol:      "●",
		state:       "running",
		stateStyle:  runningStyle,
	}
	browserRow := renderBrowserRow(item, true, 48)
	transportRow := renderTransportOption("Native", "recommended", "Herdr runs inside the Sprite.", true, 48)
	sshModel := Model{sshKeyChoices: []catalog.SSHIdentity{{Comment: "Work key", Algorithm: "ssh-ed25519"}}, sshKeyCursor: 0}
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

func TestNamedSpriteNativeAndSSHPreparation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport catalog.SpriteTransport
		move      bool
	}{
		{"native", catalog.NativePreview, false},
		{"ssh", catalog.SSHFallback, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
			terminal := &fakeTerminalCommand{}
			ops.prepareSpriteFn = func(_ context.Context, selection catalog.SpriteSessionSelection, transport catalog.SpriteTransport, _ int) (catalog.PreparedAttach, error) {
				if want := spriteSessionSelection("devbox", "work"); !reflect.DeepEqual(selection, want) || transport != tc.transport {
					t.Fatalf("PrepareSprite selection/transport = %#v/%v", selection, transport)
				}
				if transport == catalog.NativePreview {
					return catalog.PreparedNativeAttach{Command: terminal}, nil
				}
				return catalog.PrepareSpriteSSH("devbox", "work", sprites.Prepared{Port: 41234, SSHAuthSocket: "/tmp/hrd-agent"})
			}
			model := spriteReady(t, ops)
			model.browser.Select(1)
			model, _ = update(t, model, press("enter"))
			if tc.move {
				model, _ = update(t, model, press("down"))
			}
			var native tea.ExecCommand
			var processArgs, processEnv []string
			model.execCommand = func(command tea.ExecCommand, callback tea.ExecCallback) tea.Cmd {
				native = command
				return func() tea.Msg { return callback(nil) }
			}
			model.execProcess = func(command *exec.Cmd, callback tea.ExecCallback) tea.Cmd {
				processArgs = append([]string(nil), command.Args...)
				processEnv = append([]string(nil), command.Env...)
				return func() tea.Msg { return callback(nil) }
			}
			model, prepare := update(t, model, press("enter"))
			if model.phase != phasePreparing || prepare == nil {
				t.Fatalf("prepare phase/cmd = %v/%v", model.phase, prepare)
			}
			if tc.transport == catalog.NativePreview {
				view := ansi.Strip(model.View().Content)
				if !strings.Contains(view, "Choose a connection") || strings.Contains(view, "Waking") || strings.Contains(view, "Herdr has the terminal") {
					t.Fatalf("native preparation flashed an intermediate screen: %q", view)
				}
			}
			model, handoff := update(t, model, runFirst(t, prepare))
			if model.phase != phaseAttaching || handoff == nil {
				t.Fatalf("handoff phase/cmd = %v/%v", model.phase, handoff)
			}
			if model.spriteSnapshot.current || model.spriteSnapshotDetail.current {
				t.Fatalf("attach retained current observations = endpoint %v sessions %v", model.spriteSnapshot.current, model.spriteSnapshotDetail.current)
			}
			if tc.transport == catalog.NativePreview {
				if native != terminal || len(processArgs) != 0 {
					t.Fatalf("native handoff = %T, process args %#v", native, processArgs)
				}
			} else {
				want := []string{"herdr", "--remote", "ssh://sprite@127.0.0.1:41234", "--session", "work"}
				if !reflect.DeepEqual(processArgs, want) {
					t.Fatalf("SSH Herdr args = %#v, want %#v", processArgs, want)
				}
				if !contains(processEnv, "SSH_AUTH_SOCK=/tmp/hrd-agent") || containsPrefix(processEnv, "SPRITE_TOKEN=") {
					t.Fatalf("SSH child environment = %#v", processEnv)
				}
			}
		})
	}
}

func TestBackAfterNativeAttachReconcilesSpriteLifecycle(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(_ context.Context, call int) catalog.Inventory {
		lifecycle := sprites.Cold
		if call > 1 {
			lifecycle = sprites.Running
		}
		return catalog.Inventory{
			Local:   catalog.SourceResult{Rows: localRows()},
			Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", lifecycle)},
		}
	}
	ops.prepareSpriteFn = func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error) {
		return catalog.PreparedNativeAttach{Command: &fakeTerminalCommand{}}, nil
	}
	model := ready(t, ops)
	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	model, _ = update(t, model, runFirst(t, load))
	model, _ = update(t, model, press("enter"))
	model.execCommand = func(tea.ExecCommand, tea.ExecCallback) tea.Cmd {
		return func() tea.Msg { return attachDone{opID: model.operationID} }
	}
	model, prepare := update(t, model, press("enter"))
	model, attach := update(t, model, runFirst(t, prepare))
	model, sessions := update(t, model, run(t, attach))
	model, _ = update(t, model, runFirst(t, sessions))
	if model.spriteSnapshot.current {
		t.Fatal("successful native attach left pre-attach provider lifecycle current")
	}

	model, reconcile := update(t, model, press("esc"))
	if reconcile == nil || !model.inventoryRefreshing || model.spriteSnapshot.current || !strings.Contains(ansi.Strip(model.View().Content), "stale") {
		t.Fatalf("back did not invalidate and reconcile lifecycle: refreshing %v current %v view %q cmd %v", model.inventoryRefreshing, model.spriteSnapshot.current, model.View().Content, reconcile)
	}
	model, _ = update(t, model, runFirst(t, reconcile))
	if !model.spriteSnapshot.current || !strings.Contains(ansi.Strip(model.View().Content), "running") || strings.Contains(ansi.Strip(model.View().Content), "cold") {
		t.Fatalf("reconciled lifecycle = current %v view %q", model.spriteSnapshot.current, model.View().Content)
	}
}

func TestStopErrorInvalidatesUntilSpriteRefreshReconciles(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.stopFn = func(context.Context, catalog.SpriteSessionSelection, int) error {
		return errors.New("stop response lost")
	}
	model := spriteReady(t, ops)
	model, _ = update(t, model, press("s"))
	model, stop := update(t, model, press("y"))
	model, refresh := update(t, model, runFirst(t, stop))
	if model.phase != phaseRefreshingSprite || model.spriteSnapshotDetail.current || refresh == nil {
		t.Fatalf("stop error state = phase %v current %v cmd %v", model.phase, model.spriteSnapshotDetail.current, refresh)
	}
	if !strings.Contains(model.notice, "stop response lost") || strings.Contains(model.helpView(), "stop") || strings.Contains(model.helpView(), "restart") {
		t.Fatalf("stop error notice/help = %q/%q", model.notice, model.helpView())
	}

	model, _ = update(t, model, runFirst(t, refresh))
	if model.phase != phaseReady || !model.spriteSnapshotDetail.current || !strings.Contains(model.View().Content, "stop response lost") {
		t.Fatalf("reconciled stop error = phase %v current %v view %q", model.phase, model.spriteSnapshotDetail.current, model.View().Content)
	}
}

func TestStopConfirmationAndRestartComposeStopThenAttach(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := spriteReady(t, ops)
	model, cmd := update(t, model, press("s"))
	if cmd != nil || model.phase != phaseConfirmingStop || model.restart {
		t.Fatalf("stop confirmation = phase %v restart %v cmd %v", model.phase, model.restart, cmd)
	}
	if !strings.Contains(model.View().Content, "Pane processes in this session will exit") {
		t.Fatalf("confirmation lacks process warning: %q", model.View().Content)
	}
	model, _ = update(t, model, press("n"))
	if model.phase != phaseReady || ops.stopCalls.Load() != 0 {
		t.Fatal("cancel stopped the session")
	}

	model, _ = update(t, model, press("R"))
	model, stop := update(t, model, press("y"))
	if stop != nil || model.phase != phaseChoosingSpriteTransport || ops.stopCalls.Load() != 0 {
		t.Fatalf("restart transport choice = calls %d phase %v cmd %v", ops.stopCalls.Load(), model.phase, stop)
	}
	model, stop = update(t, model, press("enter"))
	model, follow := update(t, model, runFirst(t, stop))
	if follow == nil || ops.stopCalls.Load() != 1 || model.phase != phasePreparing || model.spriteSnapshotDetail.current {
		t.Fatalf("restart composition = calls %d phase %v current %v cmd %v", ops.stopCalls.Load(), model.phase, model.spriteSnapshotDetail.current, follow)
	}
}

func TestRestartTransportCancelDoesNotStopAndPrepareFailureRefreshesWithNotice(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.prepareSpriteFn = func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error) {
		return nil, errors.New("native setup failed")
	}
	model := spriteReady(t, ops)
	model, _ = update(t, model, press("R"))
	model, _ = update(t, model, press("y"))
	model, cmd := update(t, model, press("esc"))
	if cmd != nil || model.phase != phaseReady || ops.stopCalls.Load() != 0 {
		t.Fatalf("transport cancel = phase %v stops %d cmd %v", model.phase, ops.stopCalls.Load(), cmd)
	}

	model, _ = update(t, model, press("R"))
	model, _ = update(t, model, press("y"))
	model, stop := update(t, model, press("enter"))
	model, prepare := update(t, model, runFirst(t, stop))
	if model.spriteSnapshotDetail.current || model.phase != phasePreparing || prepare == nil {
		t.Fatalf("post-stop invalidation = current %v phase %v cmd %v", model.spriteSnapshotDetail.current, model.phase, prepare)
	}
	model, refresh := update(t, model, runFirst(t, prepare))
	if model.phase != phaseRefreshingSprite || refresh == nil || !strings.Contains(model.notice, "native setup failed") {
		t.Fatalf("prepare failure refresh = phase %v notice %q cmd %v", model.phase, model.notice, refresh)
	}
	model, _ = update(t, model, runFirst(t, refresh))
	if model.phase != phaseReady || !model.spriteSnapshotDetail.current || !strings.Contains(model.View().Content, "native setup failed") {
		t.Fatalf("post-failure refresh = phase %v current %v view %q", model.phase, model.spriteSnapshotDetail.current, model.View().Content)
	}
}

func TestAttachFailureNoticeSurvivesAutomaticRefresh(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport catalog.SpriteTransport
	}{
		{name: "native", transport: catalog.NativePreview},
		{name: "process", transport: catalog.SSHFallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPRITE_TOKEN", "must-not-reach-child")
			ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
			ops.prepareSpriteFn = func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error) {
				if tc.transport == catalog.NativePreview {
					return catalog.PreparedNativeAttach{Command: &fakeTerminalCommand{}}, nil
				}
				return catalog.PrepareSpriteSSH("devbox", "default", sprites.Prepared{Port: 22})
			}
			model := spriteReady(t, ops)
			model, _ = update(t, model, press("enter"))
			if tc.transport == catalog.SSHFallback {
				model, _ = update(t, model, press("down"))
			}
			model.execCommand = func(tea.ExecCommand, tea.ExecCallback) tea.Cmd {
				return func() tea.Msg {
					return attachDone{opID: model.operationID, err: errors.New("native control failure\nunsafe")}
				}
			}
			model.execProcess = func(command *exec.Cmd, callback tea.ExecCallback) tea.Cmd {
				if containsPrefix(command.Env, "SPRITE_TOKEN=") {
					t.Fatalf("process environment contains SPRITE_TOKEN: %#v", command.Env)
				}
				return func() tea.Msg { return callback(errors.New("process control failure\nunsafe")) }
			}
			model, prepare := update(t, model, press("enter"))
			model, attach := update(t, model, runFirst(t, prepare))
			model, refresh := update(t, model, run(t, attach))
			if model.phase != phaseRefreshingSprite || refresh == nil || !strings.Contains(model.notice, "control failureunsafe") {
				t.Fatalf("attach failure = phase %v notice %q cmd %v", model.phase, model.notice, refresh)
			}
			model, _ = update(t, model, runFirst(t, refresh))
			if !strings.Contains(model.View().Content, "control failureunsafe") {
				t.Fatalf("successful refresh cleared attach failure: %q", model.View().Content)
			}
		})
	}
}

func TestBackgroundInventoryRefreshDoesNotPollSpriteDetail(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := spriteReady(t, ops)
	inventoryCalls := ops.inventoryCalls.Load()
	sessionCalls := ops.listSpriteCalls.Load()

	model, cmd := update(t, model, refreshTick(time.Now()))
	if cmd == nil || model.phase != phaseReady || model.inventoryRefreshing || ops.inventoryCalls.Load() != inventoryCalls || ops.listSpriteCalls.Load() != sessionCalls {
		t.Fatalf("detail refresh tick polled = phase %v refreshing %v inventory %d→%d sessions %d→%d cmd %v", model.phase, model.inventoryRefreshing, inventoryCalls, ops.inventoryCalls.Load(), sessionCalls, ops.listSpriteCalls.Load(), cmd)
	}
	content := ansi.Strip(model.View().Content)
	if strings.Contains(content, "checked") || strings.Contains(content, "ago") {
		t.Fatalf("Sprite detail shows an observation timer: %q", content)
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
	model.spriteSnapshotDetail.current = false
	model.syncSpriteItems(selected(t, model).ID)
	if help := model.helpView(); strings.Contains(help, "enter") || strings.Contains(help, "restart") || strings.Contains(help, "stop") {
		t.Fatalf("stale-session help exposes actions = %q", help)
	}
	for _, phase := range []phase{phaseLoadingSprite, phaseRefreshingSprite, phaseInstallingSpriteHerdr} {
		model.phase = phase
		if help := model.helpView(); !strings.Contains(help, "esc") || !strings.Contains(help, "cancel") {
			t.Errorf("phase %v help = %q", phase, help)
		}
	}
}

func TestHostKeyRequiresExplicitConfirmationAndCancelDropsPrepared(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.prepareSpriteFn = func(_ context.Context, _ catalog.SpriteSessionSelection, _ catalog.SpriteTransport, _ int) (catalog.PreparedAttach, error) {
		return catalog.PrepareSpriteSSH("devbox", "default", sprites.Prepared{Port: 22, Fingerprint: "SHA256:verified"})
	}
	model := spriteReady(t, ops)
	model, _ = update(t, model, press("enter"))
	model, _ = update(t, model, press("down"))
	execCalls := 0
	model.execProcess = func(*exec.Cmd, tea.ExecCallback) tea.Cmd { execCalls++; return nil }
	model, prepare := update(t, model, press("enter"))
	model, cmd := update(t, model, runFirst(t, prepare))
	if cmd != nil || model.phase != phaseConfirmingHostKey || execCalls != 0 || !strings.Contains(model.View().Content, "SHA256:verified") {
		t.Fatalf("host confirmation = phase %v calls %d cmd %v view %q", model.phase, execCalls, cmd, model.View().Content)
	}
	model, refresh := update(t, model, press("esc"))
	if model.prepared != nil || model.phase != phaseRefreshingSprite || refresh == nil {
		t.Fatalf("cancel cleanup = prepared %v phase %v cmd %v", model.prepared, model.phase, refresh)
	}
}

func TestSSHKeySelectionUsesExactlyOneKeyAndRetries(t *testing.T) {
	selection := &sshagent.SelectionRequired{Source: "1Password", Identities: []sshagent.Identity{
		{Fingerprint: "SHA256:work", Comment: "Work", Algorithm: "ssh-ed25519"},
		{Fingerprint: "SHA256:personal", Comment: "Personal", Algorithm: "ssh-ed25519"},
	}}
	base := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	base.prepareSpriteFn = func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error) {
		return nil, selection
	}
	ops := &fakeSSHOperations{fakeSpriteOperations: base}
	model := spriteReady(t, ops)
	model, _ = update(t, model, press("enter"))
	model, _ = update(t, model, press("down"))
	model, prepare := update(t, model, press("enter"))
	model, _ = update(t, model, runFirst(t, prepare))
	if model.phase != phaseChoosingSSHKey || !strings.Contains(model.View().Content, "Choose exactly one") {
		t.Fatalf("SSH selection phase/view = %v/%q", model.phase, model.View().Content)
	}
	model, _ = update(t, model, press("down"))
	model, retry := update(t, model, press("enter"))
	if ops.selected != "SHA256:personal" || model.phase != phasePreparing || retry == nil {
		t.Fatalf("key selection/retry = %q/%v/%v", ops.selected, model.phase, retry)
	}
}

func TestSSHAddRecoveryRunsOnce(t *testing.T) {
	base := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	base.prepareSpriteFn = func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error) {
		return nil, sshagent.ErrNoKeys
	}
	ops := &fakeSSHOperations{fakeSpriteOperations: base, addCommand: exec.Command("ssh-add")}
	model := spriteReady(t, ops)
	model, _ = update(t, model, press("enter"))
	model, _ = update(t, model, press("down"))
	model, prepare := update(t, model, press("enter"))
	model, start := update(t, model, runFirst(t, prepare))
	if model.phase != phaseStartingSSHAgent || start == nil || !model.sshRecoveryTried {
		t.Fatalf("recovery start = phase %v cmd %v tried %v", model.phase, start, model.sshRecoveryTried)
	}
	model.execProcess = func(command *exec.Cmd, callback tea.ExecCallback) tea.Cmd {
		if !reflect.DeepEqual(command.Args, []string{"ssh-add"}) {
			t.Fatalf("ssh-add command args = %#v", command.Args)
		}
		return func() tea.Msg { return callback(nil) }
	}
	model, add := update(t, model, run(t, start))
	model, retry := update(t, model, run(t, add))
	if retry == nil || model.phase != phasePreparing || ops.addCalls.Load() != 1 || base.prepareSpriteCalls.Load() != 1 {
		t.Fatalf("ssh-add retry = phase %v add %d prepare %d cmd %v", model.phase, ops.addCalls.Load(), base.prepareSpriteCalls.Load(), retry)
	}
	model, noSecondRecovery := update(t, model, runFirst(t, retry))
	if noSecondRecovery != nil || model.phase != phaseReady || ops.addCalls.Load() != 1 || base.prepareSpriteCalls.Load() != 2 {
		t.Fatalf("second failure = phase %v add %d prepare %d cmd %v", model.phase, ops.addCalls.Load(), base.prepareSpriteCalls.Load(), noSecondRecovery)
	}
}

func TestConfirmedAndRejectedCreateOutcomes(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
		ops.inventoryFn = func(_ context.Context, call int) catalog.Inventory {
			rows := spriteEndpointRows("devbox", sprites.Running)
			if call > 1 {
				rows = append(rows, spriteEndpointRows("newbox", sprites.Running)...)
			}
			return catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: rows}}
		}
		model := ready(t, ops)
		model, _ = update(t, model, press("n"))
		model.nameInput.SetValue("newbox")
		model, create := update(t, model, press("enter"))
		model, refresh := update(t, model, runFirst(t, create))
		if !model.inventoryRefreshing || model.inventoryRefreshVisible || strings.Contains(ansi.Strip(model.View().Content), "Refreshing") {
			t.Fatalf("confirmed create reconciliation was visible: refreshing %v visible %v view %q", model.inventoryRefreshing, model.inventoryRefreshVisible, model.View().Content)
		}
		model, _ = update(t, model, runFirst(t, refresh))
		if selected(t, model).Label != "newbox" || model.createBlock != "" {
			t.Fatalf("confirmed create selection/block = %q/%q", selected(t, model).Label, model.createBlock)
		}
	})
	t.Run("rejected", func(t *testing.T) {
		ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
		ops.createFn = func(_ context.Context, name string, _ int) sprites.CreateResult {
			return sprites.CreateResult{Name: name, Rejected: true, Err: errors.New("name refused")}
		}
		model := ready(t, ops)
		model, _ = update(t, model, press("n"))
		model.nameInput.SetValue("newbox")
		model, create := update(t, model, press("enter"))
		model, _ = update(t, model, runFirst(t, create))
		if model.phase != phaseNamingSprite || !model.nameInput.Focused() || !strings.Contains(model.View().Content, "name refused") || model.inventoryRefreshing {
			t.Fatalf("rejected create = phase %v focused %v refreshing %v view %q", model.phase, model.nameInput.Focused(), model.inventoryRefreshing, model.View().Content)
		}
	})
}

func TestUnknownCreateOutcomeBlocksFurtherCreationUntilInventoryReconciles(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.createFn = func(_ context.Context, name string, _ int) sprites.CreateResult {
		return sprites.CreateResult{Name: name, Err: errors.New("timeout")}
	}
	ops.inventoryFn = func(_ context.Context, call int) catalog.Inventory {
		result := catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", sprites.Running)}}
		if call == 2 {
			result.Sprites.Err = errors.New("cannot reconcile")
			result.Sprites.Rows = nil
		}
		if call >= 3 {
			result.Sprites.Rows = append(result.Sprites.Rows, spriteEndpointRows("newbox", sprites.Running)...)
		}
		return result
	}
	model := ready(t, ops)
	model, _ = update(t, model, press("n"))
	model.nameInput.SetValue("newbox")
	model, create := update(t, model, press("enter"))
	model, refresh := update(t, model, runFirst(t, create))
	if model.createBlock != "newbox" || model.phase != phaseReady || !model.inventoryRefreshing || model.inventoryRefreshVisible || refresh == nil {
		t.Fatalf("unknown outcome block/phase/refreshing/visible/cmd = %q/%v/%v/%v/%v", model.createBlock, model.phase, model.inventoryRefreshing, model.inventoryRefreshVisible, refresh)
	}
	model, _ = update(t, model, runFirst(t, refresh))
	if !strings.Contains(model.View().Content, "outcome is unknown") || !strings.Contains(model.View().Content, "refresh failed") || strings.Contains(model.View().Content, "cannot reconcile") {
		t.Fatalf("silent reconciliation feedback = %q", model.View().Content)
	}
	model, cmd := update(t, model, press("n"))
	if cmd != nil || model.phase != phaseReady || ops.createCalls.Load() != 1 {
		t.Fatal("unknown creation outcome allowed another create")
	}
	model, refresh = update(t, model, press("r"))
	model, _ = update(t, model, runFirst(t, refresh))
	if model.createBlock != "" || model.createNotice != "" {
		t.Fatalf("confirmed reconciliation did not clear create block/notice: %q/%q", model.createBlock, model.createNotice)
	}
}

func TestQuitCancelsEffectAndRejectsLatePreparedCompletion(t *testing.T) {
	started := make(chan struct{})
	ops := &fakeOperations{prepareFn: func(ctx context.Context, destination catalog.Destination, _ int) (catalog.PreparedAttach, error) {
		close(started)
		<-ctx.Done()
		return catalog.Prepare(destination)
	}}
	model := ready(t, ops)
	model, prepare := update(t, model, press("enter"))
	done := make(chan tea.Msg, 1)
	go func() { done <- runFirst(t, prepare) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("prepare did not start")
	}
	model, quit := update(t, model, press("q"))
	if !model.quitting || quit == nil {
		t.Fatal("quit did not cancel preparation")
	}
	select {
	case completion := <-done:
		model, late := update(t, model, completion)
		if late != nil || !model.quitting || model.prepared != nil {
			t.Fatal("late prepared completion survived quit cleanup")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled preparation did not return")
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

func TestSpriteNamingUsesTextInputAndQIsText(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := ready(t, ops)
	model, _ = update(t, model, press("n"))
	model, _ = update(t, model, press("q"))
	if model.quitting || model.nameInput.Value() != "q" {
		t.Fatalf("name input q = value %q quitting %v", model.nameInput.Value(), model.quitting)
	}
}

func ready(t *testing.T, operations Operations) Model {
	t.Helper()
	model := New(context.Background(), operations)
	model, _ = update(t, model, run(t, model.initialCmd))
	if model.phase != phaseReady {
		t.Fatalf("initial load phase = %v, notice %q", model.phase, model.notice)
	}
	return model
}

func spriteReady(t *testing.T, operations Operations) Model {
	t.Helper()
	model := ready(t, operations)
	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	model, _ = update(t, model, runFirst(t, load))
	if model.screen != screenSprite || model.phase != phaseReady {
		t.Fatalf("Sprite detail phase/screen = %v/%v", model.phase, model.screen)
	}
	return model
}

func localRows() []catalog.Destination {
	rows, err := catalog.LocalDestinations([]herdr.Session{{Name: "default", Default: true, Running: true}, {Name: "alpha"}})
	if err != nil {
		panic(err)
	}
	return rows
}

func spriteEndpointRows(name string, state sprites.State) []catalog.Destination {
	rows, err := catalog.SpriteDestinations([]sprites.Sprite{{Name: name, State: state}})
	if err != nil {
		panic(err)
	}
	return rows
}

func spriteSessionRows(name string, state sprites.State) []catalog.Destination {
	endpoint := spriteEndpointRows(name, state)[0]
	selection, err := catalog.SelectSpriteEndpoint(endpoint)
	if err != nil {
		panic(err)
	}
	rows, err := catalog.SpriteSessionDestinations(selection, []herdr.Session{{Name: "default", Default: true, Running: true}, {Name: "work"}})
	if err != nil {
		panic(err)
	}
	return rows
}

func spriteSessionSelection(name, session string) catalog.SpriteSessionSelection {
	for _, row := range spriteSessionRows(name, sprites.Running) {
		if row.Session == session {
			selection, err := catalog.SelectSpriteSession(row)
			if err != nil {
				panic(err)
			}
			return selection
		}
	}
	panic("missing Sprite session")
}

func selected(t *testing.T, model Model) catalog.Destination {
	t.Helper()
	destination, ok := model.selectedDestination()
	if !ok {
		t.Fatal("no selected destination")
	}
	return destination
}

func update(t *testing.T, model Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := model.Update(msg)
	result, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T", updated)
	}
	return result, cmd
}

func run(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("cannot run nil command")
	}
	return cmd()
}

func runFirst(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	msg := run(t, cmd)
	if batch, ok := msg.(tea.BatchMsg); ok {
		if len(batch) == 0 {
			t.Fatal("empty command batch")
		}
		return run(t, batch[0])
	}
	return msg
}

func press(name string) tea.KeyPressMsg {
	keyValue := tea.Key{Text: name}
	switch name {
	case "enter":
		keyValue = tea.Key{Code: tea.KeyEnter}
	case "up":
		keyValue = tea.Key{Code: tea.KeyUp}
	case "down":
		keyValue = tea.Key{Code: tea.KeyDown}
	case "esc":
		keyValue = tea.Key{Code: tea.KeyEscape}
	case "ctrl+c":
		keyValue = tea.Key{Code: 'c', Mod: tea.ModCtrl}
	default:
		runes := []rune(name)
		if len(runes) == 1 {
			keyValue.Code = runes[0]
		}
	}
	return tea.KeyPressMsg(keyValue)
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
func containsPrefix(values []string, wanted string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, wanted) {
			return true
		}
	}
	return false
}

func assertFits(t *testing.T, view tea.View, width, height int) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(view.Content, "\n"), "\n")
	if len(lines) > height {
		t.Fatalf("view height = %d, want <= %d: %q", len(lines), height, view.Content)
	}
	for index, line := range lines {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("line %d width = %d, want <= %d: %q", index, got, width, line)
		}
	}
}
