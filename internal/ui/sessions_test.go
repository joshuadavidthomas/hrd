package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	"github.com/joshuadavidthomas/hrd/internal/sprites"
)

func TestWarmSpritesPrefetchSessionsAndOpenFromCache(t *testing.T) {
	for _, state := range []sprites.State{sprites.Running, sprites.Warm} {
		t.Run(string(state), func(t *testing.T) {
			ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
			ops.inventoryFn = func(context.Context, int) catalog.Inventory {
				return catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", state)}}
			}
			model := New(context.Background(), ops)
			model, prefetch := update(t, model, run(t, model.initialCmd))
			if prefetch == nil || !model.ready() || ops.listSpriteCalls.Load() != 0 {
				t.Fatalf("prefetch start = ready %v calls %d cmd %v", model.ready(), ops.listSpriteCalls.Load(), prefetch)
			}
			model, _ = update(t, model, run(t, prefetch))
			if ops.listSpriteCalls.Load() != 1 {
				t.Fatalf("prefetch calls = %d", ops.listSpriteCalls.Load())
			}
			model.browser.Select(2)
			model, load := update(t, model, press("enter"))
			if load == nil || model.active != workflowHome || model.home.phase != homeOpeningSprite {
				t.Fatalf("cached open start = active %v phase %v cmd %v", model.active, model.home.phase, load)
			}
			model, _ = update(t, model, runFirst(t, load))
			if model.active != workflowSessions || !model.ready() || len(model.browser.Items()) != 2 || ops.listSpriteCalls.Load() != 1 || ops.syncConfigCalls.Load() != 1 {
				t.Fatalf("cached open = active %v ready %v items %d list/sync %d/%d", model.active, model.ready(), len(model.browser.Items()), ops.listSpriteCalls.Load(), ops.syncConfigCalls.Load())
			}
			model.sessions.invalidate()
			if _, cached := model.sessions.cached(model.sessions.spriteEndpoint.ID); cached {
				t.Fatal("invalidated session observation remained cached")
			}
		})
	}
}

func TestWarmSessionPrefetchBatchesTargets(t *testing.T) {
	alpha := spriteEndpointRows("alpha", sprites.Running)[0]
	beta := spriteEndpointRows("beta", sprites.Warm)[0]
	alphaSelection, err := catalog.SelectSpriteEndpoint(alpha)
	if err != nil {
		t.Fatal(err)
	}
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(context.Context, int) catalog.Inventory {
		return catalog.Inventory{
			Local:   catalog.SourceResult{Rows: localRows()},
			Sprites: catalog.SourceResult{Rows: []catalog.Destination{alpha, beta}},
		}
	}
	ops.listSpriteFn = func(_ context.Context, selection catalog.SpriteEndpointSelection, _ int) ([]catalog.Destination, error) {
		if reflect.DeepEqual(selection, alphaSelection) {
			return spriteSessionRows("alpha", sprites.Running), nil
		}
		return spriteSessionRows("beta", sprites.Warm), nil
	}

	model := New(context.Background(), ops)
	model, prefetch := update(t, model, run(t, model.initialCmd))
	batchMsg := run(t, prefetch)
	batch, ok := batchMsg.(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("prefetch batch = %T with %d commands", batchMsg, len(batch))
	}
	if model.sessions.prefetchRemaining != 2 || model.sessions.prefetchCancel == nil {
		t.Fatalf("started prefetch = remaining %d cancel %v", model.sessions.prefetchRemaining, model.sessions.prefetchCancel != nil)
	}

	model, _ = update(t, model, run(t, batch[0]))
	if _, cached := model.sessions.cached(alpha.ID); !cached {
		t.Fatal("first target was not cached")
	}
	if _, cached := model.sessions.cached(beta.ID); cached {
		t.Fatal("second target was cached before its command completed")
	}
	if model.sessions.prefetchRemaining != 1 || model.sessions.prefetchCancel == nil {
		t.Fatalf("partial prefetch = remaining %d cancel %v", model.sessions.prefetchRemaining, model.sessions.prefetchCancel != nil)
	}

	model, _ = update(t, model, run(t, batch[1]))
	if _, cached := model.sessions.cached(beta.ID); !cached {
		t.Fatal("second target was not cached")
	}
	if model.sessions.prefetchRemaining != 0 || model.sessions.prefetchCancel != nil {
		t.Fatalf("completed prefetch = remaining %d cancel %v", model.sessions.prefetchRemaining, model.sessions.prefetchCancel != nil)
	}
	if ops.listSpriteCalls.Load() != 2 {
		t.Fatalf("prefetch calls = %d", ops.listSpriteCalls.Load())
	}
}

func TestWarmPrefetchRequiresCurrentSpriteInventory(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(_ context.Context, call int) catalog.Inventory {
		inventory := catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}}
		if call == 1 {
			inventory.Sprites.Rows = spriteEndpointRows("devbox", sprites.Warm)
		} else {
			inventory.Sprites.Err = errors.New("provider unavailable")
		}
		return inventory
	}
	model := New(context.Background(), ops)
	model, prefetch := update(t, model, run(t, model.initialCmd))
	if prefetch == nil {
		t.Fatal("warm Sprite did not begin prefetch")
	}
	model, refresh := update(t, model, press("r"))
	model, nextPrefetch := update(t, model, runFirst(t, refresh))
	if model.home.spriteSnapshot.current || nextPrefetch != nil {
		t.Fatalf("stale Sprite source prefetched: current %v cmd %v", model.home.spriteSnapshot.current, nextPrefetch)
	}
	model, _ = update(t, model, run(t, prefetch))
	if _, cached := model.sessions.cached(spriteEndpointRows("devbox", sprites.Warm)[0].ID); cached {
		t.Fatal("canceled stale prefetch populated the cache")
	}
}

func TestExplicitRefreshClearsCacheDuringSilentRefresh(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := New(context.Background(), ops)
	model, prefetch := update(t, model, run(t, model.initialCmd))
	model, _ = update(t, model, run(t, prefetch))
	endpointID := spriteEndpointRows("devbox", sprites.Running)[0].ID
	if _, cached := model.sessions.cached(endpointID); !cached {
		t.Fatal("warm prefetch did not populate the cache")
	}
	model, _ = update(t, model, refreshTick(time.Now()))
	if !model.home.inventoryRefreshing || model.home.inventoryRefreshVisible {
		t.Fatalf("silent refresh state = refreshing %v visible %v", model.home.inventoryRefreshing, model.home.inventoryRefreshVisible)
	}
	model, _ = update(t, model, press("r"))
	if _, cached := model.sessions.cached(endpointID); cached {
		t.Fatal("explicit refresh promotion retained the session cache")
	}
}

func TestRunningToColdInventoryRejectsOldPrefetch(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(_ context.Context, call int) catalog.Inventory {
		state := sprites.Running
		if call > 1 {
			state = sprites.Cold
		}
		return catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", state)}}
	}
	model := New(context.Background(), ops)
	model, oldPrefetch := update(t, model, run(t, model.initialCmd))
	model, _ = update(t, model, run(t, model.beginSilentInventory()))
	model, _ = update(t, model, run(t, oldPrefetch))
	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	if load == nil || model.active != workflowHome || model.home.phase != homeOpeningSprite {
		t.Fatalf("Cold Sprite used stale prefetch: active %v phase %v cmd %v", model.active, model.home.phase, load)
	}
}

func TestColdSpriteStaysOnHomeUntilSessionsLoad(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(context.Context, int) catalog.Inventory {
		return catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", sprites.Cold)}}
	}
	model := New(context.Background(), ops)
	model, prefetch := update(t, model, run(t, model.initialCmd))
	if prefetch != nil || ops.listSpriteCalls.Load() != 0 {
		t.Fatalf("cold Sprite was prefetched: calls %d cmd %v", ops.listSpriteCalls.Load(), prefetch)
	}
	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	if load == nil || model.active != workflowHome || model.home.phase != homeOpeningSprite {
		t.Fatalf("cold open = active %v phase %v cmd %v", model.active, model.home.phase, load)
	}
	plain := ansi.Strip(model.View().Content)
	for _, want := range []string{"Local sessions", "Sprites", "Loading devbox sessions"} {
		if !strings.Contains(plain, want) {
			t.Errorf("Home while opening lacks %q: %q", want, plain)
		}
	}
	model, _ = update(t, model, runFirst(t, load))
	if model.active != workflowSessions || !model.ready() {
		t.Fatalf("cold completion = active %v ready %v", model.active, model.ready())
	}
}

func TestNavigationBetweenEndpointAndSpriteScreensResetsFilter(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := ready(t, ops)
	model.browser.SetFilterText("devbox")
	model, load := update(t, model, press("enter"))
	if model.browser.FilterValue() != "devbox" || model.browser.SettingFilter() {
		t.Fatalf("endpoint filter changed while opening = setting %v query %q", model.browser.SettingFilter(), model.browser.FilterValue())
	}
	model, _ = update(t, model, runFirst(t, load))
	if model.browser.FilterValue() != "" || model.browser.SettingFilter() {
		t.Fatalf("endpoint filter survived drill-in = setting %v query %q", model.browser.SettingFilter(), model.browser.FilterValue())
	}
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
	if ops.syncConfigCalls.Load() != 1 {
		t.Fatalf("Sprite refresh config sync calls = %d, want initial open only", ops.syncConfigCalls.Load())
	}
	if model.browser.FilterValue() != "work" || len(model.browser.VisibleItems()) != 1 || selected(t, model).Label != "work" {
		t.Fatalf("filter after Sprite refresh = %q with %d items, selection %q", model.browser.FilterValue(), len(model.browser.VisibleItems()), selected(t, model).Label)
	}
}

func TestSpriteDrillInLoadsOnlySelectedSpriteSessions(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := ready(t, ops)
	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	if model.active != workflowHome || model.home.phase != homeOpeningSprite || model.sessions.phase != sessionsLoading || load == nil || ops.listSpriteCalls.Load() != 0 {
		t.Fatalf("drill workflows/phases/calls/cmd = %v/%v/%v/%d/%v", model.active, model.home.phase, model.sessions.phase, ops.listSpriteCalls.Load(), load)
	}
	loadingView := ansi.Strip(model.View().Content)
	for _, want := range []string{"Local sessions", "Sprites", "Loading devbox sessions", ansi.Strip(model.spinner.View())} {
		if !strings.Contains(loadingView, want) {
			t.Errorf("opening Home lacks %q: %q", want, loadingView)
		}
	}
	if strings.Contains(loadingView, "devbox sessions\n") {
		t.Errorf("opening Home rendered the Sprite session panel: %q", loadingView)
	}
	if strings.Contains(loadingView, "refresh failed") {
		t.Errorf("opening a Sprite was shown as a failed refresh: %q", loadingView)
	}
	model, _ = update(t, model, runFirst(t, load))
	if !model.ready() || len(model.browser.Items()) != 2 || ops.listSpriteCalls.Load() != 1 || ops.syncConfigCalls.Load() != 1 || ops.prepareSpriteCalls.Load() != 0 {
		t.Fatalf("detail state/items/list/sync/prepare = %v/%d/%d/%d/%d", model.active, len(model.browser.Items()), ops.listSpriteCalls.Load(), ops.syncConfigCalls.Load(), ops.prepareSpriteCalls.Load())
	}
	for _, want := range []string{"default", "running", "work", "stopped"} {
		if !strings.Contains(model.View().Content, want) {
			t.Errorf("detail view lacks %q: %q", want, model.View().Content)
		}
	}
}

func TestSpriteOpenRetriesPendingConfigSyncAfterFailure(t *testing.T) {
	tests := []struct {
		name          string
		wantSyncCalls int32
		ops           func() *fakeSpriteOperations
	}{
		{
			name:          "session list failure",
			wantSyncCalls: 1,
			ops: func() *fakeSpriteOperations {
				ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
				ops.listSpriteFn = func(_ context.Context, _ catalog.SpriteEndpointSelection, call int) ([]catalog.Destination, error) {
					if call == 1 {
						return nil, errors.New("list failed")
					}
					return spriteSessionRows("devbox", sprites.Running), nil
				}
				return ops
			},
		},
		{
			name:          "config sync failure",
			wantSyncCalls: 2,
			ops: func() *fakeSpriteOperations {
				ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
				ops.syncConfigFn = func(_ context.Context, _ catalog.SpriteEndpointSelection, call int) error {
					if call == 1 {
						return errors.New("sync failed")
					}
					return nil
				}
				return ops
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ops := test.ops()
			model := ready(t, ops)
			model.browser.Select(2)
			model, open := update(t, model, press("enter"))
			model, _ = update(t, model, runFirst(t, open))
			if !model.sessions.configSyncPending || model.active != workflowSessions {
				t.Fatalf("failed open = pending %v active %v", model.sessions.configSyncPending, model.active)
			}
			if test.name == "config sync failure" {
				if !model.ready() || len(model.browser.Items()) != 2 || !strings.Contains(model.refreshNotice, "Herdr config sync failed: sync failed") {
					t.Fatalf("sync failure = ready %v items %d notice %q", model.ready(), len(model.browser.Items()), model.refreshNotice)
				}
			}
			model, refresh := update(t, model, press("r"))
			model, _ = update(t, model, runFirst(t, refresh))
			if model.sessions.configSyncPending || !model.ready() || ops.syncConfigCalls.Load() != test.wantSyncCalls {
				t.Fatalf("retry = pending %v ready %v sync calls %d", model.sessions.configSyncPending, model.ready(), ops.syncConfigCalls.Load())
			}
		})
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
	if load == nil || model.sessions.phase != sessionsLoading {
		t.Fatalf("cached missing-Herdr start = phase %v cmd %v", model.sessions.phase, load)
	}
	model, _ = update(t, model, runFirst(t, load))
	if model.sessions.phase != sessionsConfirmingInstall || ops.syncConfigCalls.Load() != 0 {
		t.Fatalf("cached missing-Herdr result = phase %v sync calls %d", model.sessions.phase, ops.syncConfigCalls.Load())
	}
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
	if _, cached := model.sessions.cached(model.sessions.spriteEndpoint.ID); cached {
		t.Fatal("successful install retained the missing-Herdr cache entry")
	}
	if model.sessions.phase != sessionsInstallingHerdr || relist == nil || ops.installCalls.Load() != 1 {
		t.Fatalf("install completion = phase %v calls %d cmd %v", model.sessions.phase, ops.installCalls.Load(), relist)
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
	if model.active != workflowHome || model.home.phase != homeOpeningSprite || model.sessions.spriteSnapshotDetail.loaded {
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
