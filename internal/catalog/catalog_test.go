package catalog_test

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	"github.com/joshuadavidthomas/hrd/internal/herdr"
	"github.com/joshuadavidthomas/hrd/internal/sprites"
)

func TestLocalDestinationsTranslateSessionsInSourceOrder(t *testing.T) {
	t.Parallel()

	sessions := []herdr.Session{
		{Name: "default", Default: true, Running: true},
		{Name: "alpha", Running: false},
		{Name: "zeta", Running: true},
	}
	destinations, err := catalog.LocalDestinations(sessions)
	if err != nil {
		t.Fatalf("LocalDestinations() error = %v", err)
	}
	if len(destinations) != len(sessions) {
		t.Fatalf("LocalDestinations() returned %d rows, want %d", len(destinations), len(sessions))
	}

	for i, destination := range destinations {
		if destination.Session != sessions[i].Name || destination.Label != sessions[i].Name {
			t.Errorf("destination %d names = (%q, %q), want %q", i, destination.Session, destination.Label, sessions[i].Name)
		}
		if _, ok := destination.Endpoint.(catalog.LocalEndpoint); !ok {
			t.Errorf("destination %d endpoint = %T, want LocalEndpoint", i, destination.Endpoint)
		}
		status, ok := destination.Status.(catalog.LocalSessionStatus)
		if !ok {
			t.Errorf("destination %d status = %T, want LocalSessionStatus", i, destination.Status)
			continue
		}
		if status.Running != sessions[i].Running {
			t.Errorf("destination %d running = %v, want %v", i, status.Running, sessions[i].Running)
		}
	}

	// Translation owns its rows rather than retaining the caller's slice.
	sessions[0].Name = "changed"
	if destinations[0].Session != "default" {
		t.Fatalf("destination changed with input slice: %#v", destinations[0])
	}
}

func TestPrepareUsesExactLocalArguments(t *testing.T) {
	t.Parallel()

	destinations, err := catalog.LocalDestinations([]herdr.Session{
		{Name: "default", Default: true},
		{Name: "work tree"},
	})
	if err != nil {
		t.Fatalf("LocalDestinations() error = %v", err)
	}

	defaultResult, err := catalog.Prepare(destinations[0])
	if err != nil {
		t.Fatalf("Prepare(default) error = %v", err)
	}
	defaultAttach := requireProcessAttach(t, defaultResult)
	if got := defaultAttach.HerdrArgs(); len(got) != 0 {
		t.Fatalf("default args = %#v, want no args", got)
	}

	namedResult, err := catalog.Prepare(destinations[1])
	if err != nil {
		t.Fatalf("Prepare(named) error = %v", err)
	}
	namedAttach := requireProcessAttach(t, namedResult)
	want := []string{"--session", "work tree"}
	if got := namedAttach.HerdrArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("named args = %#v, want %#v", got, want)
	}
}

func TestPreparedAttachReturnsArgumentCopies(t *testing.T) {
	t.Parallel()

	destinations, err := catalog.LocalDestinations([]herdr.Session{{Name: "named"}})
	if err != nil {
		t.Fatalf("LocalDestinations() error = %v", err)
	}
	result, err := catalog.Prepare(destinations[0])
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	prepared := requireProcessAttach(t, result)

	first := prepared.HerdrArgs()
	first[0] = "mutated"
	want := []string{"--session", "named"}
	if got := prepared.HerdrArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("args after caller mutation = %#v, want %#v", got, want)
	}
}

func TestDestinationIDsAreUniqueAndIncludeEndpointIdentity(t *testing.T) {
	t.Parallel()

	sessions := []herdr.Session{
		{Name: "default", Default: true},
		{Name: "a:b"},
		{Name: "a/b"},
		{Name: "a b"},
	}
	destinations, err := catalog.LocalDestinations(sessions)
	if err != nil {
		t.Fatalf("LocalDestinations() error = %v", err)
	}
	seen := make(map[catalog.DestinationID]struct{}, len(destinations))
	for _, destination := range destinations {
		if destination.ID == "" {
			t.Fatal("destination has empty ID")
		}
		if _, exists := seen[destination.ID]; exists {
			t.Fatalf("duplicate ID %q", destination.ID)
		}
		seen[destination.ID] = struct{}{}
	}
}

func TestStoppedSessionRemainsSelectable(t *testing.T) {
	t.Parallel()

	destinations, err := catalog.LocalDestinations([]herdr.Session{{Name: "sleeping", Running: false}})
	if err != nil {
		t.Fatalf("LocalDestinations() error = %v", err)
	}
	if len(destinations) != 1 {
		t.Fatalf("LocalDestinations() returned %d rows, want 1", len(destinations))
	}
	status := destinations[0].Status.(catalog.LocalSessionStatus)
	if status.Running {
		t.Fatal("stopped destination was marked running")
	}
	result, err := catalog.Prepare(destinations[0])
	if err != nil {
		t.Fatalf("Prepare(stopped) error = %v", err)
	}
	prepared := requireProcessAttach(t, result)
	if want := []string{"--session", "sleeping"}; !reflect.DeepEqual(prepared.HerdrArgs(), want) {
		t.Fatalf("stopped args = %#v, want %#v", prepared.HerdrArgs(), want)
	}
}

func TestLocalDestinationsRejectsDuplicates(t *testing.T) {
	t.Parallel()

	_, err := catalog.LocalDestinations([]herdr.Session{{Name: "same"}, {Name: "same"}})
	if !errors.Is(err, catalog.ErrDuplicateDestination) {
		t.Fatalf("LocalDestinations() error = %v, want ErrDuplicateDestination", err)
	}
}

func TestLocalDestinationsRejectsInvalidNamesAndDefaults(t *testing.T) {
	t.Parallel()

	tests := map[string][]herdr.Session{
		"empty name":       {{Name: ""}},
		"unmarked default": {{Name: "default"}},
		"wrong default":    {{Name: "named", Default: true}},
	}
	for name, sessions := range tests {
		sessions := sessions
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := catalog.LocalDestinations(sessions)
			if !errors.Is(err, catalog.ErrInvalidDestination) {
				t.Fatalf("LocalDestinations() error = %v, want ErrInvalidDestination", err)
			}
		})
	}
}

func TestPrepareRejectsTamperedDestination(t *testing.T) {
	t.Parallel()

	destinations, err := catalog.LocalDestinations([]herdr.Session{{Name: "valid"}})
	if err != nil {
		t.Fatalf("LocalDestinations() error = %v", err)
	}
	destination := destinations[0]
	destination.Session = "other"
	_, err = catalog.Prepare(destination)
	if !errors.Is(err, catalog.ErrInvalidDestination) {
		t.Fatalf("Prepare() error = %v, want ErrInvalidDestination", err)
	}
}

func TestLocalOperationsInventorySkipsSprites(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inventory := catalog.NewLocalOperations(herdr.New()).Inventory(ctx)
	if !inventory.Sprites.Skipped || inventory.Sprites.Err != nil || len(inventory.Sprites.Rows) != 0 {
		t.Fatalf("Sprite source = %#v, want skipped", inventory.Sprites)
	}
	if !errors.Is(inventory.Local.Err, context.Canceled) {
		t.Fatalf("local inventory error = %v, want context.Canceled", inventory.Local.Err)
	}
}

func TestLocalOperationsPreparesAndObservesCancellation(t *testing.T) {
	t.Parallel()

	destinations, err := catalog.LocalDestinations([]herdr.Session{{Name: "work"}})
	if err != nil {
		t.Fatalf("LocalDestinations() error = %v", err)
	}
	operations := catalog.NewLocalOperations(herdr.New())
	result, err := operations.Prepare(context.Background(), destinations[0])
	if err != nil {
		t.Fatalf("LocalOperations.Prepare() error = %v", err)
	}
	prepared := requireProcessAttach(t, result)
	if want := []string{"--session", "work"}; !reflect.DeepEqual(prepared.HerdrArgs(), want) {
		t.Fatalf("LocalOperations.Prepare() args = %#v, want %#v", prepared.HerdrArgs(), want)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = operations.Prepare(ctx, destinations[0])
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("LocalOperations.Prepare(canceled) error = %v, want context.Canceled", err)
	}
}

func TestSpriteDestinationsKeepObservedLifecycleAndSeparateIDs(t *testing.T) {
	t.Parallel()
	rows, err := catalog.SpriteDestinations([]sprites.Sprite{{Name: "zeta", State: sprites.Cold}, {Name: "alpha", State: sprites.Running}})
	if err != nil {
		t.Fatalf("SpriteDestinations() error = %v", err)
	}
	if rows[0].Label != "alpha" || rows[0].Session != "" {
		t.Fatalf("rows = %#v", rows)
	}
	if status, ok := rows[0].Status.(catalog.SpriteStatus); !ok || status.Lifecycle != sprites.Running {
		t.Fatalf("status = %#v", rows[0].Status)
	}
	local, err := catalog.LocalDestinations([]herdr.Session{{Name: "default", Default: true}})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].ID == local[0].ID {
		t.Fatal("Sprite and local IDs collide")
	}
}

func TestSpriteSessionDestinationsObserveOnlyHerdrSessionState(t *testing.T) {
	t.Parallel()

	endpoints, err := catalog.SpriteDestinations([]sprites.Sprite{{Name: "alpha", State: sprites.Warm}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := catalog.SelectSpriteEndpoint(endpoints[0])
	if err != nil {
		t.Fatal(err)
	}
	sessions := []herdr.Session{
		{Name: "default", Default: true, Running: true},
		{Name: "work tree", Running: false},
	}
	rows, err := catalog.SpriteSessionDestinations(endpoint, sessions)
	if err != nil {
		t.Fatalf("SpriteSessionDestinations() error = %v", err)
	}
	if len(rows) != 2 || rows[0].Session != "default" || rows[1].Session != "work tree" {
		t.Fatalf("rows = %#v", rows)
	}
	for i, row := range rows {
		status, ok := row.Status.(catalog.SpriteSessionStatus)
		if !ok {
			t.Fatalf("row %d status = %T, want SpriteSessionStatus", i, row.Status)
		}
		if status.Running != sessions[i].Running {
			t.Errorf("row %d status = %#v", i, status)
		}
	}
	if got, want := endpoints[0].ID, catalog.DestinationID("v1:sprite:YWxwaGE"); got != want {
		t.Fatalf("endpoint ID = %q, want %q", got, want)
	}
	if endpoints[0].ID == rows[0].ID || rows[0].ID == rows[1].ID {
		t.Fatal("endpoint and session IDs must remain distinct")
	}

	sessions[0].Name = "changed"
	if rows[0].Session != "default" {
		t.Fatal("session rows retained the caller's slice")
	}
}

func TestSpriteSessionDestinationsRejectInvalidSourceRows(t *testing.T) {
	t.Parallel()

	if _, err := catalog.SpriteSessionDestinations(catalog.SpriteEndpointSelection{}, nil); !errors.Is(err, catalog.ErrInvalidDestination) {
		t.Fatalf("invalid Sprite error = %v", err)
	}
	endpoint := spriteEndpointSelection(t, "alpha", sprites.Unknown)
	if _, err := catalog.SpriteSessionDestinations(endpoint, []herdr.Session{{Name: ""}}); !errors.Is(err, catalog.ErrInvalidDestination) {
		t.Fatalf("empty session error = %v", err)
	}
	if _, err := catalog.SpriteSessionDestinations(endpoint, []herdr.Session{{Name: "same"}, {Name: "same"}}); !errors.Is(err, catalog.ErrDuplicateDestination) {
		t.Fatalf("duplicate session error = %v", err)
	}
}

func TestPrepareSpriteSSHUsesExactRemoteURI(t *testing.T) {
	t.Parallel()
	result, err := catalog.PrepareSpriteSSH("alpha", "work", sprites.Prepared{Port: 41234, Fingerprint: "SHA256:test", SSHAuthSocket: "/tmp/hrd-agent.sock"})
	if err != nil {
		t.Fatalf("PrepareSpriteSSH() error = %v", err)
	}
	prepared := requireProcessAttach(t, result)
	if got, want := prepared.HerdrArgs(), []string{"--remote", "ssh://sprite@127.0.0.1:41234", "--session", "work"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	if prepared.SSHAuthSocket() != "/tmp/hrd-agent.sock" {
		t.Fatalf("SSH agent socket = %q", prepared.SSHAuthSocket())
	}
	if prepared.Fingerprint != "SHA256:test" {
		t.Fatalf("fingerprint = %q, want SHA256:test", prepared.Fingerprint)
	}
	for call := 1; call <= 2; call++ {
		if err := prepared.Close(); err != nil {
			t.Fatalf("Close() call %d error = %v", call, err)
		}
	}
}

type fakeTerminalCommand struct{}

func (*fakeTerminalCommand) Run() error          { return nil }
func (*fakeTerminalCommand) SetStdin(io.Reader)  {}
func (*fakeTerminalCommand) SetStdout(io.Writer) {}
func (*fakeTerminalCommand) SetStderr(io.Writer) {}

func TestPreparedVariantsRemainDistinct(t *testing.T) {
	t.Parallel()

	command := &fakeTerminalCommand{}
	var nativeResult catalog.PreparedAttach = catalog.PreparedNativeAttach{Command: command}
	native, ok := nativeResult.(catalog.PreparedNativeAttach)
	if !ok {
		t.Fatalf("native result = %T, want PreparedNativeAttach", nativeResult)
	}
	if native.Command != command {
		t.Fatal("native preparation changed command identity")
	}
	if err := native.Close(); err != nil {
		t.Fatalf("native Close() error = %v", err)
	}
	if _, ok := nativeResult.(catalog.PreparedProcessAttach); ok {
		t.Fatal("native preparation is also a process preparation")
	}

	processResult, err := catalog.PrepareSpriteSSH("alpha", "work", sprites.Prepared{Port: 22})
	if err != nil {
		t.Fatalf("PrepareSpriteSSH() error = %v", err)
	}
	if _, ok := processResult.(catalog.PreparedProcessAttach); !ok {
		t.Fatalf("SSH result = %T, want PreparedProcessAttach", processResult)
	}
	if _, ok := processResult.(catalog.PreparedNativeAttach); ok {
		t.Fatal("process preparation is also a native preparation")
	}
}

func TestSpritePreparationRejectsInvalidVariantInputs(t *testing.T) {
	t.Parallel()

	if _, err := catalog.PrepareSpriteSSH("Bad Name", "work", sprites.Prepared{Port: 22}); !errors.Is(err, catalog.ErrInvalidDestination) {
		t.Fatalf("PrepareSpriteSSH(invalid name) error = %v, want ErrInvalidDestination", err)
	}
	if _, err := catalog.PrepareSpriteSSH("alpha", "work", sprites.Prepared{}); !errors.Is(err, catalog.ErrInvalidDestination) {
		t.Fatalf("PrepareSpriteSSH(invalid port) error = %v, want ErrInvalidDestination", err)
	}
	if _, err := catalog.PrepareSpriteSSH("alpha", "", sprites.Prepared{Port: 22}); !errors.Is(err, catalog.ErrInvalidDestination) {
		t.Fatalf("PrepareSpriteSSH(empty session) error = %v, want ErrInvalidDestination", err)
	}
}

func TestSpritePreparationRequiresExplicitValidTransport(t *testing.T) {
	t.Parallel()

	rows, err := catalog.SpriteDestinations([]sprites.Sprite{{Name: "alpha", State: sprites.Running}})
	if err != nil {
		t.Fatal(err)
	}
	operations := catalog.NewSpriteOperations(herdr.New(), nil)
	if _, err := operations.Prepare(context.Background(), rows[0]); !errors.Is(err, catalog.ErrInvalidDestination) {
		t.Fatalf("Prepare(Sprite) error = %v, want ErrInvalidDestination", err)
	}
	endpoint, err := catalog.SelectSpriteEndpoint(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := catalog.SpriteSessionDestinations(endpoint, []herdr.Session{{Name: "work"}})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := catalog.SelectSpriteSession(sessions[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.PrepareSprite(context.Background(), selection, catalog.SpriteTransport(99)); !errors.Is(err, catalog.ErrInvalidDestination) {
		t.Fatalf("PrepareSprite(invalid transport) error = %v, want ErrInvalidDestination", err)
	}
	if _, err := operations.PrepareSprite(context.Background(), catalog.SpriteSessionSelection{}, catalog.NativePreview); !errors.Is(err, catalog.ErrInvalidDestination) {
		t.Fatalf("PrepareSprite(empty selection) error = %v, want ErrInvalidDestination", err)
	}
}

func TestSpriteSelectionConversionsRejectWrongRowKinds(t *testing.T) {
	t.Parallel()

	endpoints, err := catalog.SpriteDestinations([]sprites.Sprite{{Name: "alpha", State: sprites.Running}})
	if err != nil {
		t.Fatal(err)
	}
	tamperedEndpoint := endpoints[0]
	tamperedEndpoint.Session = "work"
	if _, err := catalog.SelectSpriteEndpoint(tamperedEndpoint); !errors.Is(err, catalog.ErrInvalidDestination) {
		t.Fatalf("SelectSpriteEndpoint(tampered) error = %v, want ErrInvalidDestination", err)
	}

	endpoint, err := catalog.SelectSpriteEndpoint(endpoints[0])
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := catalog.SpriteSessionDestinations(endpoint, []herdr.Session{{Name: "work"}})
	if err != nil {
		t.Fatal(err)
	}
	tamperedSession := sessions[0]
	tamperedSession.ID = endpoints[0].ID
	if _, err := catalog.SelectSpriteSession(tamperedSession); !errors.Is(err, catalog.ErrInvalidDestination) {
		t.Fatalf("SelectSpriteSession(tampered) error = %v, want ErrInvalidDestination", err)
	}
}

func spriteEndpointSelection(t *testing.T, name string, state sprites.State) catalog.SpriteEndpointSelection {
	t.Helper()
	rows, err := catalog.SpriteDestinations([]sprites.Sprite{{Name: name, State: state}})
	if err != nil {
		t.Fatalf("SpriteDestinations() error = %v", err)
	}
	selection, err := catalog.SelectSpriteEndpoint(rows[0])
	if err != nil {
		t.Fatalf("SelectSpriteEndpoint() error = %v", err)
	}
	return selection
}

func requireProcessAttach(t *testing.T, result catalog.PreparedAttach) catalog.PreparedProcessAttach {
	t.Helper()
	prepared, ok := result.(catalog.PreparedProcessAttach)
	if !ok {
		t.Fatalf("prepared result = %T, want PreparedProcessAttach", result)
	}
	return prepared
}

func TestNewLocalOperationsRejectsNilClient(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("NewLocalOperations(nil) did not panic")
		}
	}()
	catalog.NewLocalOperations(nil)
}
