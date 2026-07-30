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
	for _, want := range []string{"Local sessions", "Sprites", "checked just now"} {
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
	for _, want := range []string{"devbox", "stale", "provider unavailable", "checked"} {
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

func TestIdleRefreshHasGenerationGuardAndPreservesSelection(t *testing.T) {
	ops := &fakeOperations{}
	model := ready(t, ops)
	model, _ = update(t, model, press("down"))
	oldID := model.operationID
	model, batch := update(t, model, refreshTick(time.Now()))
	if model.phase != phaseReady || !model.inventoryRefreshing || model.operationID <= oldID || batch == nil {
		t.Fatalf("idle refresh phase/refreshing/id/cmd = %v/%v/%d/%v", model.phase, model.inventoryRefreshing, model.operationID, batch)
	}
	refreshID := model.operationID
	model, staleCmd := update(t, model, inventoryDone{opID: oldID, inventory: catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()[:1]}, Sprites: catalog.SourceResult{Skipped: true}}})
	if staleCmd != nil || model.operationID != refreshID || model.phase != phaseReady || !model.inventoryRefreshing {
		t.Fatal("stale inventory completion replaced the active generation")
	}
	model, _ = update(t, model, runFirst(t, batch))
	if selected(t, model).Label != "alpha" || ops.inventoryCalls.Load() != 2 {
		t.Fatalf("selection/calls after idle refresh = %q/%d", selected(t, model).Label, ops.inventoryCalls.Load())
	}
}

func TestFilterStartedDuringEndpointRefreshSurvivesCompletion(t *testing.T) {
	ops := &fakeOperations{}
	model := ready(t, ops)
	model, refresh := update(t, model, refreshTick(time.Now()))
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
	model, refresh := update(t, model, refreshTick(time.Now()))
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
	model, _ = update(t, model, press("esc"))
	if model.screen != screenEndpoints || model.browser.FilterValue() != "" || model.browser.SettingFilter() {
		t.Fatalf("Sprite filter survived back navigation = screen %v setting %v query %q", model.screen, model.browser.SettingFilter(), model.browser.FilterValue())
	}
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
	if cmd != nil || model.screen != screenEndpoints || model.phase != phaseReady || ops.installCalls.Load() != 0 {
		t.Fatalf("install rejection = screen %v phase %v calls %d cmd %v", model.screen, model.phase, ops.installCalls.Load(), cmd)
	}

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
	model, _ = update(t, model, press("esc"))
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
			model, handoff := update(t, model, runFirst(t, prepare))
			if model.phase != phaseAttaching || handoff == nil {
				t.Fatalf("handoff phase/cmd = %v/%v", model.phase, handoff)
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

func TestSpriteDetailShowsSharedObservationAgeWithoutPolling(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	observed := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	model := ready(t, ops)
	model.now = func() time.Time { return observed }
	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	model, _ = update(t, model, runFirst(t, load))
	model.now = func() time.Time { return observed.Add(65 * time.Second) }

	content := model.View().Content
	if strings.Count(content, "Herdr checked") != 1 || !strings.Contains(content, "Herdr checked 1m ago") {
		t.Fatalf("detail observation line = %q", content)
	}
	calls := ops.listSpriteCalls.Load()
	model, cmd := update(t, model, refreshTick(observed.Add(refreshInterval)))
	if cmd == nil || model.phase != phaseReady || model.inventoryRefreshing || ops.listSpriteCalls.Load() != calls {
		t.Fatalf("detail refresh tick polled = phase %v refreshing %v calls %d→%d cmd %v", model.phase, model.inventoryRefreshing, calls, ops.listSpriteCalls.Load(), cmd)
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
	if model.createBlock != "newbox" || model.phase != phaseReady || !model.inventoryRefreshing || refresh == nil {
		t.Fatalf("unknown outcome block/phase/refreshing/cmd = %q/%v/%v/%v", model.createBlock, model.phase, model.inventoryRefreshing, refresh)
	}
	model, _ = update(t, model, runFirst(t, refresh))
	if !strings.Contains(model.View().Content, "outcome is unknown") || !strings.Contains(model.View().Content, "cannot reconcile") {
		t.Fatalf("unknown notice did not survive failed reconciliation: %q", model.View().Content)
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
