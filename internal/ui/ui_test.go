package ui

import (
	"context"
	"errors"
	"image/color"
	"io"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	"github.com/joshuadavidthomas/hrd/internal/config"
	"github.com/joshuadavidthomas/hrd/internal/herdr"
	"github.com/joshuadavidthomas/hrd/internal/sprites"
)

const testConfigSync = config.HerdrConfigSyncOpen

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
	syncConfigCalls    atomic.Int32
	stopCalls          atomic.Int32
	prepareSpriteCalls atomic.Int32
	inventoryFn        func(context.Context, int) catalog.Inventory
	createFn           func(context.Context, string, int) sprites.CreateResult
	listSpriteFn       func(context.Context, catalog.SpriteEndpointSelection, int) ([]catalog.Destination, error)
	installFn          func(context.Context, catalog.SpriteEndpointSelection, int) (bool, error)
	syncConfigFn       func(context.Context, catalog.SpriteEndpointSelection, int) error
	stopFn             func(context.Context, catalog.SpriteSessionSelection, int) error
	prepareSpriteFn    func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error)
	preparedSelection  catalog.SpriteSessionSelection
	preparedTransport  catalog.SpriteTransport
	addCalls           atomic.Int32
	addCommand         *exec.Cmd
	addErr             error
	selected           string
	selectErr          error
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

func (f *fakeSpriteOperations) InstallSpriteHerdr(ctx context.Context, endpoint catalog.SpriteEndpointSelection) (bool, error) {
	call := int(f.installCalls.Add(1))
	if f.installFn != nil {
		return f.installFn(ctx, endpoint, call)
	}
	return true, nil
}

func (f *fakeSpriteOperations) SyncSpriteHerdrConfig(ctx context.Context, endpoint catalog.SpriteEndpointSelection) error {
	call := int(f.syncConfigCalls.Add(1))
	if f.syncConfigFn != nil {
		return f.syncConfigFn(ctx, endpoint, call)
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

func (f *fakeSpriteOperations) SSHAddCommand(context.Context) (*exec.Cmd, error) {
	f.addCalls.Add(1)
	return f.addCommand, f.addErr
}
func (f *fakeSpriteOperations) SelectSSHKey(fingerprint string) error {
	f.selected = fingerprint
	return f.selectErr
}

type fakeTerminalCommand struct{}

func (*fakeTerminalCommand) Run() error          { return nil }
func (*fakeTerminalCommand) SetStdin(io.Reader)  {}
func (*fakeTerminalCommand) SetStdout(io.Writer) {}
func (*fakeTerminalCommand) SetStderr(io.Writer) {}

func TestBackgroundColorUpdatesCharmStyles(t *testing.T) {
	model := New(context.Background(), &fakeOperations{}, testConfigSync)
	model, _ = update(t, model, tea.BackgroundColorMsg{Color: color.RGBA{R: 255, G: 255, B: 255, A: 255}})
	if model.isDark {
		t.Fatal("white terminal background was classified as dark")
	}
	if model.background == nil {
		t.Fatal("terminal background was not retained")
	}
}

func TestInitialAsyncLoadUsesBubblesList(t *testing.T) {
	ops := &fakeOperations{}
	model := New(context.Background(), ops, testConfigSync)
	if model.home.phase != homeLoading || model.operation.id != 1 || len(model.browser.Items()) != 0 {
		t.Fatalf("new model phase/id/items = %v/%d/%d", model.active, model.operation.id, len(model.browser.Items()))
	}
	if view := model.View(); view.Content != "" || !view.AltScreen || view.WindowTitle == "" {
		t.Fatalf("initial view = content %q alt %v title %q", view.Content, view.AltScreen, view.WindowTitle)
	}
	model, cmd := update(t, model, run(t, model.initialCmd))
	if cmd != nil || !model.ready() || len(model.browser.Items()) != 2 || ops.inventoryCalls.Load() != 1 {
		t.Fatalf("loaded phase/items/calls/cmd = %v/%d/%d/%v", model.active, len(model.browser.Items()), ops.inventoryCalls.Load(), cmd)
	}
	view := model.View()
	for _, want := range []string{"Local sessions", "default", "running", "alpha", "stopped", "enter", "refresh"} {
		if !strings.Contains(view.Content, want) {
			t.Errorf("ready view lacks %q: %q", want, view.Content)
		}
	}
	if !view.AltScreen || view.WindowTitle == "" {
		t.Fatalf("view flags = alt %v title %q", view.AltScreen, view.WindowTitle)
	}
}

func TestActiveWorkflowOwnsItsPhase(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := New(context.Background(), ops, testConfigSync)
	if model.active != workflowHome || model.home.phase != homeLoading {
		t.Fatalf("initial workflow/phase = %v/%v", model.active, model.home.phase)
	}
	model, _ = update(t, model, run(t, model.initialCmd))
	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	if model.active != workflowHome || model.home.phase != homeOpeningSprite || model.sessions.phase != sessionsLoading {
		t.Fatalf("opening workflows/phases = %v/%v/%v", model.active, model.home.phase, model.sessions.phase)
	}
	model, _ = update(t, model, runFirst(t, load))
	model, _ = update(t, model, press("enter"))
	if model.active != workflowConnection || model.connection.phase != connectionChoosingTransport {
		t.Fatalf("connection workflow/phase = %v/%v", model.active, model.connection.phase)
	}
	model, _ = update(t, model, press("esc"))
	if model.active != workflowSessions || model.sessions.phase != sessionsReady {
		t.Fatalf("return workflow/phase = %v/%v", model.active, model.sessions.phase)
	}
}

func ready(t *testing.T, operations Operations) Model {
	t.Helper()
	model := New(context.Background(), operations, testConfigSync)
	model, _ = update(t, model, run(t, model.initialCmd))
	if !model.ready() {
		t.Fatalf("initial load phase = %v, notice %q", model.active, model.notice)
	}
	return model
}

func spriteReady(t *testing.T, operations Operations) Model {
	t.Helper()
	model := ready(t, operations)
	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	model, _ = update(t, model, runFirst(t, load))
	if model.active != workflowSessions || !model.ready() {
		t.Fatalf("Sprite detail phase/screen = %v/%v", model.active, model.active)
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
