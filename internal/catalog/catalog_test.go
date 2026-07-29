package catalog_test

import (
	"context"
	"errors"
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

	defaultAttach, err := catalog.Prepare(destinations[0])
	if err != nil {
		t.Fatalf("Prepare(default) error = %v", err)
	}
	if got := defaultAttach.HerdrArgs(); len(got) != 0 {
		t.Fatalf("default args = %#v, want no args", got)
	}

	namedAttach, err := catalog.Prepare(destinations[1])
	if err != nil {
		t.Fatalf("Prepare(named) error = %v", err)
	}
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
	prepared, err := catalog.Prepare(destinations[0])
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

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
	prepared, err := catalog.Prepare(destinations[0])
	if err != nil {
		t.Fatalf("Prepare(stopped) error = %v", err)
	}
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

func TestLocalOperationsPreparesAndObservesCancellation(t *testing.T) {
	t.Parallel()

	destinations, err := catalog.LocalDestinations([]herdr.Session{{Name: "work"}})
	if err != nil {
		t.Fatalf("LocalDestinations() error = %v", err)
	}
	operations := catalog.NewLocalOperations(herdr.New())
	prepared, err := operations.Prepare(context.Background(), destinations[0])
	if err != nil {
		t.Fatalf("LocalOperations.Prepare() error = %v", err)
	}
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
	if rows[0].Label != "alpha" || rows[0].Session != "default" {
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

func TestPrepareSpriteUsesExactRemoteURI(t *testing.T) {
	t.Parallel()
	prepared, err := catalog.PrepareSprite("alpha", sprites.Prepared{Port: 41234, Fingerprint: "SHA256:test"})
	if err != nil {
		t.Fatalf("PrepareSprite() error = %v", err)
	}
	if got, want := prepared.HerdrArgs(), []string{"--remote", "ssh://sprite@127.0.0.1:41234"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	if err := prepared.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
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
