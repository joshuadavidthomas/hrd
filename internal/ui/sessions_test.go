package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	"github.com/joshuadavidthomas/hrd/internal/sprites"
)

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
	if model.active != workflowHome || model.browser.FilterValue() != "" || model.browser.SettingFilter() || reconcile == nil {
		t.Fatalf("Sprite filter survived back navigation = screen %v setting %v query %q cmd %v", model.active, model.browser.SettingFilter(), model.browser.FilterValue(), reconcile)
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
	if model.active != workflowSessions || model.sessions.phase != sessionsLoading || load == nil || ops.listSpriteCalls.Load() != 0 {
		t.Fatalf("drill phase/screen/calls/cmd = %v/%v/%d/%v", model.active, model.active, ops.listSpriteCalls.Load(), load)
	}
	loadingView := ansi.Strip(model.View().Content)
	for _, want := range []string{"devbox sessions", "Loading sessions", ansi.Strip(model.spinner.View())} {
		if !strings.Contains(loadingView, want) {
			t.Errorf("loading detail lacks %q: %q", want, loadingView)
		}
	}
	for _, unwanted := range []string{"Waking", "checking Herdr", "three minutes"} {
		if strings.Contains(loadingView, unwanted) {
			t.Errorf("loading detail contains %q: %q", unwanted, loadingView)
		}
	}
	model, _ = update(t, model, runFirst(t, load))
	if !model.ready() || len(model.browser.Items()) != 2 || ops.listSpriteCalls.Load() != 1 || ops.prepareSpriteCalls.Load() != 0 {
		t.Fatalf("detail state/items/list/prepare = %v/%d/%d/%d", model.active, len(model.browser.Items()), ops.listSpriteCalls.Load(), ops.prepareSpriteCalls.Load())
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
	if model.sessions.phase != sessionsConfirmingInstall || ops.installCalls.Load() != 0 {
		t.Fatalf("missing Herdr phase/install calls = %v/%d", model.active, ops.installCalls.Load())
	}
	confirmation := ansi.Strip(model.bodyView(model.surfaceWidth(), model.height))
	if !strings.Contains(confirmation, "Install Herdr?") {
		t.Fatalf("install confirmation = %q", confirmation)
	}
	for _, unwanted := range []string{"devbox", "download", "install.sh", "three minutes"} {
		if strings.Contains(confirmation, unwanted) {
			t.Errorf("install confirmation contains %q: %q", unwanted, confirmation)
		}
	}
	model, cmd := update(t, model, press("n"))
	if cmd == nil || model.active != workflowHome || !model.ready() || ops.installCalls.Load() != 0 {
		t.Fatalf("install rejection = screen %v phase %v calls %d cmd %v", model.active, model.active, ops.installCalls.Load(), cmd)
	}
	model, _ = update(t, model, runFirst(t, cmd))

	model.browser.Select(2)
	model, load = update(t, model, press("enter"))
	ops.listSpriteFn = func(_ context.Context, _ catalog.SpriteEndpointSelection, _ int) ([]catalog.Destination, error) {
		return nil, catalog.ErrSpriteHerdrMissing
	}
	model, _ = update(t, model, runFirst(t, load))
	model, install := update(t, model, press("y"))
	if model.sessions.phase != sessionsInstallingHerdr || install == nil || ops.installCalls.Load() != 0 {
		t.Fatalf("install start = phase %v calls %d cmd %v", model.active, ops.installCalls.Load(), install)
	}
	progress := ansi.Strip(model.bodyView(model.surfaceWidth(), model.height))
	if !strings.Contains(progress, "Installing Herdr…") || strings.Contains(progress, "devbox") || strings.Contains(progress, "three minutes") {
		t.Fatalf("install progress = %q", progress)
	}
	ops.listSpriteFn = func(_ context.Context, _ catalog.SpriteEndpointSelection, _ int) ([]catalog.Destination, error) {
		return spriteSessionRows("devbox", sprites.Running), nil
	}
	model, relist := update(t, model, runFirst(t, install))
	if model.sessions.phase != sessionsLoading || relist == nil || ops.installCalls.Load() != 1 {
		t.Fatalf("install completion = phase %v calls %d cmd %v", model.active, ops.installCalls.Load(), relist)
	}
	model, _ = update(t, model, runFirst(t, relist))
	if !model.ready() || len(model.browser.Items()) != 2 {
		t.Fatalf("post-install list = phase %v items %d", model.active, len(model.browser.Items()))
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
	if len(model.browser.Items()) != 0 || model.sessions.spriteSnapshotDetail.loaded {
		t.Fatal("beta load exposed alpha snapshot")
	}
	model, _ = update(t, model, runFirst(t, load))
	if !model.ready() || len(model.browser.Items()) != 0 || strings.Contains(model.View().Content, "default") {
		t.Fatalf("failed beta detail leaked alpha rows: %q", model.View().Content)
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
	if model.sessions.phase != sessionsRefreshing || model.sessions.spriteSnapshotDetail.current || refresh == nil {
		t.Fatalf("stop error state = phase %v current %v cmd %v", model.active, model.sessions.spriteSnapshotDetail.current, refresh)
	}
	if !strings.Contains(model.notice, "stop response lost") || strings.Contains(model.helpView(), "stop") || strings.Contains(model.helpView(), "restart") {
		t.Fatalf("stop error notice/help = %q/%q", model.notice, model.helpView())
	}

	model, _ = update(t, model, runFirst(t, refresh))
	if !model.ready() || !model.sessions.spriteSnapshotDetail.current || !strings.Contains(model.View().Content, "stop response lost") {
		t.Fatalf("reconciled stop error = phase %v current %v view %q", model.active, model.sessions.spriteSnapshotDetail.current, model.View().Content)
	}
}

func TestBackgroundInventoryRefreshDoesNotPollSpriteDetail(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := spriteReady(t, ops)
	inventoryCalls := ops.inventoryCalls.Load()
	sessionCalls := ops.listSpriteCalls.Load()

	model, cmd := update(t, model, refreshTick(time.Now()))
	if cmd == nil || !model.ready() || model.home.inventoryRefreshing || ops.inventoryCalls.Load() != inventoryCalls || ops.listSpriteCalls.Load() != sessionCalls {
		t.Fatalf("detail refresh tick polled = phase %v refreshing %v inventory %d→%d sessions %d→%d cmd %v", model.active, model.home.inventoryRefreshing, inventoryCalls, ops.inventoryCalls.Load(), sessionCalls, ops.listSpriteCalls.Load(), cmd)
	}
	content := ansi.Strip(model.View().Content)
	if strings.Contains(content, "checked") || strings.Contains(content, "ago") {
		t.Fatalf("Sprite detail shows an observation timer: %q", content)
	}
}
