// Package catalog translates source records into selectable destinations and
// prepares the fixed Herdr arguments for a selection.
package catalog

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/joshuadavidthomas/hrd/internal/herdr"
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

// ObservedStatus is status obtained from a destination's source.
type ObservedStatus interface {
	observedStatus()
}

// LocalSessionStatus reports whether Herdr observed a local session as running.
type LocalSessionStatus struct {
	Running bool
}

func (LocalSessionStatus) observedStatus() {}

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
	herdrArgs []string
}

// HerdrArgs returns a fresh copy of the arguments to pass to herdr.
func (p PreparedAttach) HerdrArgs() []string {
	return append([]string(nil), p.herdrArgs...)
}

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
	// The endpoint kind has its own field. A later Sprite ID can include its
	// endpoint name in the middle field without sharing local identity.
	encodedSession := base64.RawURLEncoding.EncodeToString([]byte(session))
	return DestinationID("v1:local::" + encodedSession)
}
