// Package catalog translates source records into selectable destinations and
// prepares the fixed Herdr arguments for a selection.
package catalog

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"sort"
	"sync"

	"github.com/joshuadavidthomas/hrd/internal/herdr"
	spriteapi "github.com/joshuadavidthomas/hrd/internal/sprites"
	"github.com/joshuadavidthomas/hrd/internal/sshagent"
)

// DestinationID identifies both the endpoint and session of a destination.
type DestinationID string

// Endpoint identifies the machine that owns a Herdr server.
type Endpoint interface {
	endpoint()
}

// LocalEndpoint is the machine on which hrd is running.
type LocalEndpoint struct{}

func (LocalEndpoint) endpoint() {}

type SpriteEndpoint struct{ Name string }

func (SpriteEndpoint) endpoint() {}

// ObservedStatus is status obtained from a destination's source.
type ObservedStatus interface {
	observedStatus()
}

// LocalSessionStatus reports whether Herdr observed a local session as running.
type LocalSessionStatus struct {
	Running bool
}

func (LocalSessionStatus) observedStatus() {}

type SpriteStatus struct{ Lifecycle spriteapi.State }

func (SpriteStatus) observedStatus() {}

// Destination is an endpoint plus a Herdr session.
type Destination struct {
	ID       DestinationID
	Endpoint Endpoint
	Session  string
	Label    string
	Status   ObservedStatus
}

// PreparedAttach contains the arguments for the fixed herdr executable.
// HerdrArgs returns a copy so a caller cannot mutate stored preparation state.
type PreparedAttach struct {
	herdrArgs     []string
	sshAuthSocket string
	Fingerprint   string
	close         *attachCloseState
}
type attachCloseState struct {
	once sync.Once
	fn   func() error
	err  error
}

func (p PreparedAttach) Close() error {
	if p.close == nil {
		return nil
	}
	p.close.once.Do(func() { p.close.err = p.close.fn() })
	return p.close.err
}

// HerdrArgs returns a fresh copy of the arguments to pass to herdr.
func (p PreparedAttach) HerdrArgs() []string {
	return append([]string(nil), p.herdrArgs...)
}

// SSHAuthSocket returns the explicit agent socket for a Sprite Herdr child.
// Local attachments return an empty string and inherit the caller environment.
func (p PreparedAttach) SSHAuthSocket() string { return p.sshAuthSocket }

var (
	// ErrInvalidDestination means source data or a selected destination broke a
	// catalog invariant.
	ErrInvalidDestination = errors.New("invalid destination")
	// ErrDuplicateDestination means two source rows produced the same identity.
	ErrDuplicateDestination = errors.New("duplicate destination")
)

// LocalOperations provides the UI's list and preparation operations from a
// Herdr client.
type LocalOperations struct {
	herdr *herdr.Client
}

// NewLocalOperations creates local catalog operations over client.
func NewLocalOperations(client *herdr.Client) *LocalOperations {
	if client == nil {
		panic("catalog: nil Herdr client")
	}
	return &LocalOperations{herdr: client}
}

// List returns the complete local destination inventory.
func (o *LocalOperations) List(ctx context.Context) ([]Destination, error) {
	sessions, err := o.herdr.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	return LocalDestinations(sessions)
}

// Prepare validates a selected destination and returns fixed Herdr arguments.
// The context check lets cancellation win before preparation begins.
func (*LocalOperations) Prepare(ctx context.Context, destination Destination) (PreparedAttach, error) {
	if err := ctx.Err(); err != nil {
		return PreparedAttach{}, err
	}
	return Prepare(destination)
}

// LocalDestinations translates a complete Herdr session inventory. Input order
// is retained: Herdr's default stays first and its named sessions stay sorted.
func LocalDestinations(sessions []herdr.Session) ([]Destination, error) {
	destinations := make([]Destination, 0, len(sessions))
	seen := make(map[DestinationID]struct{}, len(sessions))
	for i, session := range sessions {
		if session.Name == "" {
			return nil, fmt.Errorf("%w: local session %d has an empty name", ErrInvalidDestination, i)
		}
		if session.Default != (session.Name == "default") {
			return nil, fmt.Errorf("%w: local session %q has inconsistent default status", ErrInvalidDestination, session.Name)
		}

		id := localDestinationID(session.Name)
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateDestination, id)
		}
		seen[id] = struct{}{}

		destinations = append(destinations, Destination{
			ID:       id,
			Endpoint: LocalEndpoint{},
			Session:  session.Name,
			Label:    session.Name,
			Status:   LocalSessionStatus{Running: session.Running},
		})
	}
	return destinations, nil
}

// Prepare returns the exact Herdr arguments for a local destination. The
// program name remains the caller's fixed responsibility.
func Prepare(destination Destination) (PreparedAttach, error) {
	if destination.Session == "" {
		return PreparedAttach{}, fmt.Errorf("%w: empty session name", ErrInvalidDestination)
	}
	if _, ok := destination.Endpoint.(LocalEndpoint); !ok {
		return PreparedAttach{}, fmt.Errorf("%w: unsupported endpoint", ErrInvalidDestination)
	}
	if _, ok := destination.Status.(LocalSessionStatus); !ok {
		return PreparedAttach{}, fmt.Errorf("%w: local destination has non-local status", ErrInvalidDestination)
	}
	if destination.ID != localDestinationID(destination.Session) {
		return PreparedAttach{}, fmt.Errorf("%w: ID does not match local session", ErrInvalidDestination)
	}

	if destination.Session == "default" {
		return PreparedAttach{}, nil
	}
	return PreparedAttach{herdrArgs: []string{"--session", destination.Session}}, nil
}

func localDestinationID(session string) DestinationID {
	return DestinationID("v1:local::" + base64.RawURLEncoding.EncodeToString([]byte(session)))
}
func spriteDestinationID(name string) DestinationID {
	return DestinationID("v1:sprite:" + base64.RawURLEncoding.EncodeToString([]byte(name)) + ":default")
}

func SpriteDestinations(records []spriteapi.Sprite) ([]Destination, error) {
	rows := make([]Destination, 0, len(records))
	seen := map[DestinationID]struct{}{}
	for _, record := range records {
		if record.Name == "" {
			return nil, fmt.Errorf("%w: Sprite has an empty name", ErrInvalidDestination)
		}
		id := spriteDestinationID(record.Name)
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateDestination, id)
		}
		seen[id] = struct{}{}
		rows = append(rows, Destination{ID: id, Endpoint: SpriteEndpoint{Name: record.Name}, Session: "default", Label: record.Name, Status: SpriteStatus{Lifecycle: record.State}})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Label < rows[j].Label })
	return rows, nil
}
func PrepareSprite(name string, prepared spriteapi.Prepared) (PreparedAttach, error) {
	if name == "" || prepared.Port <= 0 {
		_ = prepared.Close()
		return PreparedAttach{}, fmt.Errorf("%w: invalid Sprite preparation", ErrInvalidDestination)
	}
	target := (&url.URL{Scheme: "ssh", User: url.User("sprite"), Host: fmt.Sprintf("127.0.0.1:%d", prepared.Port)}).String()
	return PreparedAttach{herdrArgs: []string{"--remote", target}, sshAuthSocket: prepared.SSHAuthSocket, Fingerprint: prepared.Fingerprint, close: &attachCloseState{fn: prepared.Close}}, nil
}

type SpriteOperations struct {
	local   *LocalOperations
	sprites *spriteapi.Client
}

func NewSpriteOperations(local *herdr.Client, sprites *spriteapi.Client) *SpriteOperations {
	return &SpriteOperations{local: NewLocalOperations(local), sprites: sprites}
}

type SourceResult struct {
	Rows    []Destination
	Skipped bool
	Err     error
}
type Inventory struct {
	Local   SourceResult
	Sprites SourceResult
}

func (o *SpriteOperations) Inventory(ctx context.Context) Inventory {
	var result Inventory
	var wait sync.WaitGroup
	wait.Add(1)
	go func() { defer wait.Done(); result.Local.Rows, result.Local.Err = o.local.List(ctx) }()
	if o.sprites == nil {
		result.Sprites.Skipped = true
	} else {
		wait.Add(1)
		go func() {
			defer wait.Done()
			records, err := o.sprites.List(ctx)
			if err != nil {
				result.Sprites.Err = err
				return
			}
			result.Sprites.Rows, result.Sprites.Err = SpriteDestinations(records)
		}()
	}
	wait.Wait()
	return result
}
func (o *SpriteOperations) List(ctx context.Context) ([]Destination, error) {
	inventory := o.Inventory(ctx)
	rows := append(append([]Destination(nil), inventory.Local.Rows...), inventory.Sprites.Rows...)
	if inventory.Local.Err != nil && inventory.Sprites.Err != nil {
		return nil, fmt.Errorf("local: %w; Sprites: %v", inventory.Local.Err, inventory.Sprites.Err)
	}
	return rows, nil
}
func (o *SpriteOperations) Prepare(ctx context.Context, destination Destination) (PreparedAttach, error) {
	switch endpoint := destination.Endpoint.(type) {
	case LocalEndpoint:
		return o.local.Prepare(ctx, destination)
	case SpriteEndpoint:
		if destination.Session != "default" || destination.ID != spriteDestinationID(endpoint.Name) {
			return PreparedAttach{}, fmt.Errorf("%w: invalid Sprite destination", ErrInvalidDestination)
		}
		return o.PrepareSprite(ctx, endpoint.Name)
	default:
		return PreparedAttach{}, fmt.Errorf("%w: unsupported endpoint", ErrInvalidDestination)
	}
}
func (o *SpriteOperations) Create(ctx context.Context, name string) spriteapi.CreateResult {
	return o.sprites.Create(ctx, name)
}

func (o *SpriteOperations) SSHAddCommand(ctx context.Context) (*exec.Cmd, error) {
	return o.sprites.SSHAddCommand(ctx)
}

// NeedsSSHKey reports the one pre-effect failure that hrd can repair with
// plain ssh-add before retrying the pending operation.
func NeedsSSHKey(err error) bool { return sshagent.KeyRequired(err) }
func (o *SpriteOperations) PrepareSprite(ctx context.Context, name string) (PreparedAttach, error) {
	prepared, err := o.sprites.Prepare(ctx, name)
	if err != nil {
		return PreparedAttach{}, err
	}
	return PrepareSprite(name, prepared)
}
