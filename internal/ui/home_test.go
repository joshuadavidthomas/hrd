package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	"github.com/joshuadavidthomas/hrd/internal/sprites"
)

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
	model.home.endpointSelection = selected(t, model).ID
	model, refresh := update(t, model, press("r"))
	model, _ = update(t, model, runFirst(t, refresh))
	if !model.ready() || !model.home.localSnapshot.current || model.home.spriteSnapshot.current {
		t.Fatalf("snapshot current flags = local %v Sprite %v", model.home.localSnapshot.current, model.home.spriteSnapshot.current)
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
	if cmd != nil || unchanged.active != workflowHome || ops.listSpriteCalls.Load() != 0 {
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
	if model.home.localSnapshot.current || !model.home.spriteSnapshot.current || len(model.browser.Items()) != 3 {
		t.Fatalf("partial local failure = local %v Sprite %v items %d", model.home.localSnapshot.current, model.home.spriteSnapshot.current, len(model.browser.Items()))
	}
	model, refresh = update(t, model, press("r"))
	model, _ = update(t, model, runFirst(t, refresh))
	if !model.home.localSnapshot.current || !model.home.spriteSnapshot.current || model.refreshNotice != "" {
		t.Fatalf("source recovery = local %v Sprite %v warning %q", model.home.localSnapshot.current, model.home.spriteSnapshot.current, model.refreshNotice)
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
	if model.home.phase != homeInitialLoadFailed || !strings.Contains(model.View().Content, "local down") || !strings.Contains(model.View().Content, "Sprites down") {
		t.Fatalf("initial failure = phase %v view %q", model.active, model.View().Content)
	}
	model, retry := update(t, model, press("r"))
	model, _ = update(t, model, runFirst(t, retry))
	if !model.ready() || len(model.browser.Items()) != 3 {
		t.Fatalf("initial retry = phase %v items %d", model.active, len(model.browser.Items()))
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
	oldID := model.operation.id

	model, refresh := update(t, model, refreshTick(time.Now()))
	if refresh == nil || !model.home.inventoryRefreshing || model.home.inventoryRefreshVisible || model.operation.id <= oldID {
		t.Fatalf("background refresh = cmd %v refreshing %v visible %v id %d→%d", refresh, model.home.inventoryRefreshing, model.home.inventoryRefreshVisible, oldID, model.operation.id)
	}
	view := ansi.Strip(model.View().Content)
	if strings.Contains(view, "Refreshing") || strings.Contains(view, "checked") || strings.Contains(view, "ago") {
		t.Fatalf("background refresh exposed progress or a timer: %q", view)
	}
	model, _ = update(t, model, runFirst(t, refresh))
	if model.home.inventoryRefreshing || ops.inventoryCalls.Load() != 2 {
		t.Fatalf("background refresh completion = refreshing %v calls %d", model.home.inventoryRefreshing, ops.inventoryCalls.Load())
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
	if model.home.spriteSnapshot.current || !strings.Contains(view, "stale") || !strings.Contains(view, "refresh failed") {
		t.Fatalf("silent failure did not mark panel stale: current %v view %q", model.home.spriteSnapshot.current, view)
	}
	if strings.Contains(view, "provider unavailable") || strings.Contains(view, "Refreshing") {
		t.Fatalf("silent failure rendered a toast: %q", view)
	}
}

func TestNamingSupersedesBackgroundRefresh(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := ready(t, ops)
	model, _ = update(t, model, refreshTick(time.Now()))
	refreshID := model.operation.id

	model, _ = update(t, model, press("n"))
	model, _ = update(t, model, press("x"))
	if model.home.phase != homeNamingSprite || model.nameInput.Value() != "x" || model.home.inventoryRefreshing || model.operation.id <= refreshID {
		t.Fatalf("naming did not supersede refresh = phase %v value %q refreshing %v id %d→%d", model.active, model.nameInput.Value(), model.home.inventoryRefreshing, refreshID, model.operation.id)
	}
	model, cmd := update(t, model, inventoryDone{opID: refreshID, inventory: catalog.Inventory{Local: catalog.SourceResult{Rows: localRows()}, Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", sprites.Cold)}}})
	if cmd != nil || model.home.phase != homeNamingSprite || model.nameInput.Value() != "x" {
		t.Fatalf("late background refresh closed naming: phase %v value %q cmd %v", model.active, model.nameInput.Value(), cmd)
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
	if refresh == nil || !model.home.inventoryRefreshing || model.home.inventoryRefreshVisible {
		t.Fatalf("filter blocked silent refresh = cmd %v refreshing %v visible %v", refresh, model.home.inventoryRefreshing, model.home.inventoryRefreshVisible)
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
	if !model.home.inventoryRefreshing || refresh == nil {
		t.Fatalf("background refresh = refreshing %v cmd %v", model.home.inventoryRefreshing, refresh)
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
	if !model.ready() || !model.home.inventoryRefreshing || !strings.Contains(model.View().Content, "Refreshing local") {
		t.Fatalf("refresh blocked view = phase %v refreshing %v view %q", model.active, model.home.inventoryRefreshing, model.View().Content)
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
	if model.connection.phase != connectionPreparing || model.home.inventoryRefreshing || prepare == nil {
		t.Fatalf("new effect did not supersede refresh = phase %v refreshing %v cmd %v", model.active, model.home.inventoryRefreshing, prepare)
	}
	select {
	case completion := <-done:
		model, stale := update(t, model, completion)
		if stale != nil || model.connection.phase != connectionPreparing {
			t.Fatal("canceled refresh completion changed active effect")
		}
	case <-time.After(time.Second):
		t.Fatal("superseded refresh was not canceled")
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
		if !model.home.inventoryRefreshing || model.home.inventoryRefreshVisible || strings.Contains(ansi.Strip(model.View().Content), "Refreshing") {
			t.Fatalf("confirmed create reconciliation was visible: refreshing %v visible %v view %q", model.home.inventoryRefreshing, model.home.inventoryRefreshVisible, model.View().Content)
		}
		model, _ = update(t, model, runFirst(t, refresh))
		if selected(t, model).Label != "newbox" || model.home.createBlock != "" {
			t.Fatalf("confirmed create selection/block = %q/%q", selected(t, model).Label, model.home.createBlock)
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
		if model.home.phase != homeNamingSprite || !model.nameInput.Focused() || !strings.Contains(model.View().Content, "name refused") || model.home.inventoryRefreshing {
			t.Fatalf("rejected create = phase %v focused %v refreshing %v view %q", model.active, model.nameInput.Focused(), model.home.inventoryRefreshing, model.View().Content)
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
	if model.home.createBlock != "newbox" || !model.ready() || !model.home.inventoryRefreshing || model.home.inventoryRefreshVisible || refresh == nil {
		t.Fatalf("unknown outcome block/phase/refreshing/visible/cmd = %q/%v/%v/%v/%v", model.home.createBlock, model.active, model.home.inventoryRefreshing, model.home.inventoryRefreshVisible, refresh)
	}
	model, _ = update(t, model, runFirst(t, refresh))
	if !strings.Contains(model.View().Content, "outcome is unknown") || !strings.Contains(model.View().Content, "refresh failed") || strings.Contains(model.View().Content, "cannot reconcile") {
		t.Fatalf("silent reconciliation feedback = %q", model.View().Content)
	}
	model, cmd := update(t, model, press("n"))
	if cmd != nil || !model.ready() || ops.createCalls.Load() != 1 {
		t.Fatal("unknown creation outcome allowed another create")
	}
	model, refresh = update(t, model, press("r"))
	model, _ = update(t, model, runFirst(t, refresh))
	if model.home.createBlock != "" || model.home.createNotice != "" {
		t.Fatalf("confirmed reconciliation did not clear create block/notice: %q/%q", model.home.createBlock, model.home.createNotice)
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
