package catalog

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/joshuadavidthomas/hrd/internal/herdr"
	spriteapi "github.com/joshuadavidthomas/hrd/internal/sprites"
)

type fakeSpriteOperationBackend struct {
	installed bool
	sessions  []herdr.Session
	calls     []string
}

func (f *fakeSpriteOperationBackend) HerdrInstalled(context.Context, string) (bool, error) {
	f.calls = append(f.calls, "check")
	return f.installed, nil
}

func (f *fakeSpriteOperationBackend) PrepareHerdr(context.Context, string) error {
	f.calls = append(f.calls, "install")
	return nil
}

func (f *fakeSpriteOperationBackend) ListHerdrSessions(context.Context, string) ([]herdr.Session, error) {
	f.calls = append(f.calls, "list")
	return append([]herdr.Session(nil), f.sessions...), nil
}

func (f *fakeSpriteOperationBackend) StopHerdrSession(context.Context, string, string) error {
	f.calls = append(f.calls, "stop")
	return nil
}

func (f *fakeSpriteOperationBackend) PrepareNative(context.Context, string, string) (*spriteapi.NativeCommand, error) {
	f.calls = append(f.calls, "native")
	return &spriteapi.NativeCommand{}, nil
}

func (f *fakeSpriteOperationBackend) PrepareSSH(context.Context, string) (spriteapi.Prepared, error) {
	f.calls = append(f.calls, "ssh")
	return spriteapi.Prepared{Port: 22}, nil
}

func TestListSpriteSessionsReturnsTypedMissingSentinelWithoutInstalling(t *testing.T) {
	backend := &fakeSpriteOperationBackend{}
	operations := testSpriteOperations(backend)
	endpoint := testSpriteEndpointSelection(t)

	rows, err := operations.ListSpriteSessions(context.Background(), endpoint)
	if rows != nil {
		t.Fatalf("ListSpriteSessions() rows = %#v, want nil", rows)
	}
	if !errors.Is(err, ErrSpriteHerdrMissing) {
		t.Fatalf("ListSpriteSessions() error = %v, want ErrSpriteHerdrMissing", err)
	}
	var missing SpriteHerdrMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("ListSpriteSessions() error = %T, want SpriteHerdrMissingError", err)
	}
	if want := []string{"check"}; !reflect.DeepEqual(backend.calls, want) {
		t.Fatalf("backend calls = %#v, want %#v", backend.calls, want)
	}
}

func TestSpriteHerdrInstallAndInventoryAreSeparateOperations(t *testing.T) {
	backend := &fakeSpriteOperationBackend{
		installed: true,
		sessions:  []herdr.Session{{Name: "work", Running: true}},
	}
	operations := testSpriteOperations(backend)
	endpoint := testSpriteEndpointSelection(t)

	rows, err := operations.ListSpriteSessions(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("ListSpriteSessions() error = %v", err)
	}
	if len(rows) != 1 || rows[0].Session != "work" {
		t.Fatalf("ListSpriteSessions() rows = %#v", rows)
	}
	if status, ok := rows[0].Status.(SpriteSessionStatus); !ok || !status.Running {
		t.Fatalf("session status = %#v", rows[0].Status)
	}
	if want := []string{"check", "list"}; !reflect.DeepEqual(backend.calls, want) {
		t.Fatalf("list calls = %#v, want %#v", backend.calls, want)
	}

	backend.calls = nil
	if err := operations.InstallSpriteHerdr(context.Background(), endpoint); err != nil {
		t.Fatalf("InstallSpriteHerdr() error = %v", err)
	}
	if want := []string{"install"}; !reflect.DeepEqual(backend.calls, want) {
		t.Fatalf("install calls = %#v, want %#v", backend.calls, want)
	}
}

func TestSpriteSessionOperationsUseValidatedSelection(t *testing.T) {
	backend := &fakeSpriteOperationBackend{}
	operations := testSpriteOperations(backend)
	endpoint := testSpriteEndpointSelection(t)
	rows, err := SpriteSessionDestinations(endpoint, []herdr.Session{{Name: "work"}})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := SelectSpriteSession(rows[0])
	if err != nil {
		t.Fatal(err)
	}

	if err := operations.StopSpriteSession(context.Background(), selection); err != nil {
		t.Fatalf("StopSpriteSession() error = %v", err)
	}
	if _, err := operations.PrepareSprite(context.Background(), selection, NativePreview); err != nil {
		t.Fatalf("PrepareSprite() error = %v", err)
	}
	if want := []string{"stop", "native"}; !reflect.DeepEqual(backend.calls, want) {
		t.Fatalf("backend calls = %#v, want %#v", backend.calls, want)
	}
}

func testSpriteOperations(backend spriteOperationBackend) *SpriteOperations {
	operations := NewSpriteOperations(herdr.New(), nil)
	operations.operations = backend
	return operations
}

func testSpriteEndpointSelection(t *testing.T) SpriteEndpointSelection {
	t.Helper()
	rows, err := SpriteDestinations([]spriteapi.Sprite{{Name: "alpha", State: spriteapi.Running}})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := SelectSpriteEndpoint(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	return selection
}
