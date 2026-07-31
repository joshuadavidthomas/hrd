// Package catalog translates source records into selectable destinations and
// prepares the fixed Herdr arguments for a selection.
package catalog

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"sort"
	"sync"

	"github.com/joshuadavidthomas/hrd/internal/herdr"
	spriteapi "github.com/joshuadavidthomas/hrd/internal/sprites"
	"github.com/joshuadavidthomas/hrd/internal/sshagent"
)

// DestinationID identifies an endpoint row or one session on an endpoint.
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

// SpriteEndpointSelection is a validated top-level Sprite destination.
// Its fields stay private so callers must cross the catalog validation seam.
type SpriteEndpointSelection struct {
	name string
}

// SpriteSessionSelection is a validated Herdr session on one Sprite.
// Its fields stay private so endpoint and session identities cannot be mixed.
type SpriteSessionSelection struct {
	name    string
	session string
}

// ObservedStatus is status obtained from a destination's source.
type ObservedStatus interface {
	observedStatus()
}

// LocalSessionStatus reports whether Herdr observed a local session as running.
type LocalSessionStatus struct {
	Running bool
}

func (LocalSessionStatus) observedStatus() {}

// SpriteStatus reports provider lifecycle for a top-level Sprite endpoint.
type SpriteStatus struct{ Lifecycle spriteapi.State }

func (SpriteStatus) observedStatus() {}

// SpriteSessionStatus reports whether Herdr observed a Sprite session as running.
type SpriteSessionStatus struct {
	Running bool
}

func (SpriteSessionStatus) observedStatus() {}

// Destination is an endpoint plus a Herdr session.
type Destination struct {
	ID       DestinationID
	Endpoint Endpoint
	Session  string
	Label    string
	Status   ObservedStatus
}

// TerminalCommand is a provider-owned interactive command. Its shape matches
// Bubble Tea's ExecCommand without coupling the catalog to the UI package.
type TerminalCommand interface {
	Run() error
	SetStdin(io.Reader)
	SetStdout(io.Writer)
	SetStderr(io.Writer)
}

// PreparedAttach is one closed attachment result: a local process or a native
// provider terminal. Both variants have the same cleanup contract.
type PreparedAttach interface {
	preparedAttach()
	Close() error
}

// PreparedProcessAttach runs the fixed local herdr executable. HerdrArgs
// returns a copy so callers cannot mutate stored preparation state.
type PreparedProcessAttach struct {
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

func (PreparedProcessAttach) preparedAttach() {}

func (p PreparedProcessAttach) Close() error {
	if p.close == nil {
		return nil
	}
	p.close.once.Do(func() { p.close.err = p.close.fn() })
	return p.close.err
}

func (p PreparedProcessAttach) HerdrArgs() []string {
	return append([]string(nil), p.herdrArgs...)
}

// SSHAuthSocket returns the explicit agent socket for an SSH fallback child.
// Local attachments return an empty string and inherit the caller environment.
func (p PreparedProcessAttach) SSHAuthSocket() string { return p.sshAuthSocket }

// PreparedNativeAttach runs the fixed Herdr executable inside a Sprite PTY.
type PreparedNativeAttach struct {
	Command TerminalCommand
}

func (PreparedNativeAttach) preparedAttach() {}
func (PreparedNativeAttach) Close() error    { return nil }

// SpriteTransport is one fixed Sprite attachment path.
type SpriteTransport uint8

const (
	NativePreview SpriteTransport = iota + 1
	SSHFallback
)

var (
	// ErrInvalidDestination means source data or a selected destination broke a
	// catalog invariant.
	ErrInvalidDestination = errors.New("invalid destination")
	// ErrDuplicateDestination means two source rows produced the same identity.
	ErrDuplicateDestination = errors.New("duplicate destination")
	// ErrSpriteHerdrMissing means the selected Sprite does not have the fixed
	// Herdr executable. Installing it requires an explicit operation.
	ErrSpriteHerdrMissing = SpriteHerdrMissingError{}
)

// SpriteHerdrMissingError is the typed missing-Herdr result at the catalog seam.
type SpriteHerdrMissingError struct{}

func (SpriteHerdrMissingError) Error() string {
	return "Herdr is not installed on the selected Sprite"
}

// LocalOperations provides the UI's inventory and preparation operations from
// a Herdr client.
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

// Inventory returns the local result and marks the unavailable Sprite source
// as skipped.
func (o *LocalOperations) Inventory(ctx context.Context) Inventory {
	sessions, err := o.herdr.ListSessions(ctx)
	var rows []Destination
	if err == nil {
		rows, err = LocalDestinations(sessions)
	}
	return Inventory{
		Local:   SourceResult{Rows: rows, Err: err},
		Sprites: SourceResult{Skipped: true},
	}
}

// Prepare validates a selected destination and returns fixed Herdr arguments.
// The context check lets cancellation win before preparation begins.
func (*LocalOperations) Prepare(ctx context.Context, destination Destination) (PreparedAttach, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
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
		return nil, fmt.Errorf("%w: empty session name", ErrInvalidDestination)
	}
	if _, ok := destination.Endpoint.(LocalEndpoint); !ok {
		return nil, fmt.Errorf("%w: unsupported endpoint", ErrInvalidDestination)
	}
	if _, ok := destination.Status.(LocalSessionStatus); !ok {
		return nil, fmt.Errorf("%w: local destination has non-local status", ErrInvalidDestination)
	}
	if destination.ID != localDestinationID(destination.Session) {
		return nil, fmt.Errorf("%w: ID does not match local session", ErrInvalidDestination)
	}

	if destination.Session == "default" {
		return PreparedProcessAttach{}, nil
	}
	return PreparedProcessAttach{herdrArgs: []string{"--session", destination.Session}}, nil
}

func localDestinationID(session string) DestinationID {
	return DestinationID("v1:local::" + base64.RawURLEncoding.EncodeToString([]byte(session)))
}
func spriteDestinationID(name string) DestinationID {
	return DestinationID("v1:sprite:" + base64.RawURLEncoding.EncodeToString([]byte(name)))
}

func spriteSessionDestinationID(name, session string) DestinationID {
	return DestinationID(string(spriteDestinationID(name)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(session)))
}

// SpriteDestinations translates provider inventory into endpoint rows. Session
// selection happens only after Herdr inventory has been read from that Sprite.
func SpriteDestinations(records []spriteapi.Sprite) ([]Destination, error) {
	rows := make([]Destination, 0, len(records))
	seen := map[DestinationID]struct{}{}
	for _, record := range records {
		if err := spriteapi.ValidName(record.Name); err != nil {
			return nil, fmt.Errorf("%w: Sprite has an invalid name", ErrInvalidDestination)
		}
		id := spriteDestinationID(record.Name)
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateDestination, id)
		}
		seen[id] = struct{}{}
		rows = append(rows, Destination{ID: id, Endpoint: SpriteEndpoint{Name: record.Name}, Label: record.Name, Status: SpriteStatus{Lifecycle: record.State}})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Label < rows[j].Label })
	return rows, nil
}

// SpriteSessionDestinations translates validated remote Herdr sessions into
// session rows under one selected Sprite endpoint.
func SpriteSessionDestinations(endpoint SpriteEndpointSelection, sessions []herdr.Session) ([]Destination, error) {
	if err := endpoint.validate(); err != nil {
		return nil, err
	}
	rows := make([]Destination, 0, len(sessions))
	seen := make(map[DestinationID]struct{}, len(sessions))
	for i, session := range sessions {
		if session.Name == "" {
			return nil, fmt.Errorf("%w: Sprite session %d has an empty name", ErrInvalidDestination, i)
		}
		if session.Default != (session.Name == "default") {
			return nil, fmt.Errorf("%w: Sprite session %q has inconsistent default status", ErrInvalidDestination, session.Name)
		}
		id := spriteSessionDestinationID(endpoint.name, session.Name)
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateDestination, id)
		}
		seen[id] = struct{}{}
		rows = append(rows, Destination{
			ID:       id,
			Endpoint: SpriteEndpoint{Name: endpoint.name},
			Session:  session.Name,
			Label:    session.Name,
			Status:   SpriteSessionStatus{Running: session.Running},
		})
	}
	return rows, nil
}

func PrepareSpriteSSH(name, session string, prepared spriteapi.Prepared) (PreparedAttach, error) {
	if spriteapi.ValidName(name) != nil || session == "" || prepared.Port <= 0 {
		_ = prepared.Close()
		return nil, fmt.Errorf("%w: invalid Sprite SSH preparation", ErrInvalidDestination)
	}
	target := (&url.URL{Scheme: "ssh", User: url.User("sprite"), Host: fmt.Sprintf("127.0.0.1:%d", prepared.Port)}).String()
	return PreparedProcessAttach{herdrArgs: []string{"--remote", target, "--session", session}, sshAuthSocket: prepared.SSHAuthSocket, Fingerprint: prepared.Fingerprint, close: &attachCloseState{fn: prepared.Close}}, nil
}

type spriteOperationBackend interface {
	HerdrInstalled(context.Context, string) (bool, error)
	PrepareHerdr(context.Context, string) (bool, error)
	SyncHerdrConfig(context.Context, string) error
	ListHerdrSessions(context.Context, string) ([]herdr.Session, error)
	StopHerdrSession(context.Context, string, string) error
	PrepareNative(context.Context, string, string) (*spriteapi.NativeCommand, error)
	PrepareSSH(context.Context, string) (spriteapi.Prepared, error)
}

type SpriteOperations struct {
	local      *LocalOperations
	sprites    *spriteapi.Client
	operations spriteOperationBackend
}

func NewSpriteOperations(local *herdr.Client, sprites *spriteapi.Client) *SpriteOperations {
	var operations spriteOperationBackend
	if sprites != nil {
		operations = sprites
	}
	return &SpriteOperations{local: NewLocalOperations(local), sprites: sprites, operations: operations}
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
	go func() {
		defer wait.Done()
		result.Local = o.local.Inventory(ctx).Local
	}()
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
func (o *SpriteOperations) Prepare(ctx context.Context, destination Destination) (PreparedAttach, error) {
	switch destination.Endpoint.(type) {
	case LocalEndpoint:
		return o.local.Prepare(ctx, destination)
	case SpriteEndpoint:
		return nil, fmt.Errorf("%w: Sprite transport is required", ErrInvalidDestination)
	default:
		return nil, fmt.Errorf("%w: unsupported endpoint", ErrInvalidDestination)
	}
}
func (o *SpriteOperations) Create(ctx context.Context, name string) spriteapi.CreateResult {
	return o.sprites.Create(ctx, name)
}

func (o *SpriteOperations) SSHAddCommand(ctx context.Context) (*exec.Cmd, error) {
	return o.sprites.SSHAddCommand(ctx)
}

func (o *SpriteOperations) SelectSSHKey(fingerprint string) error {
	return o.sprites.SelectSSHKey(fingerprint)
}

// SelectSpriteEndpoint validates list rendering data as a top-level Sprite
// selection. All Sprite endpoint operations accept only the returned value.
func SelectSpriteEndpoint(destination Destination) (SpriteEndpointSelection, error) {
	endpoint, endpointOK := destination.Endpoint.(SpriteEndpoint)
	_, statusOK := destination.Status.(SpriteStatus)
	selection := SpriteEndpointSelection{name: endpoint.Name}
	if !endpointOK || !statusOK || destination.Session != "" || destination.ID != spriteDestinationID(endpoint.Name) || selection.validate() != nil {
		return SpriteEndpointSelection{}, fmt.Errorf("%w: invalid Sprite endpoint", ErrInvalidDestination)
	}
	return selection, nil
}

// SelectSpriteSession validates list rendering data as one Sprite session.
// Stop and attachment operations accept only the returned value.
func SelectSpriteSession(destination Destination) (SpriteSessionSelection, error) {
	endpoint, endpointOK := destination.Endpoint.(SpriteEndpoint)
	_, statusOK := destination.Status.(SpriteSessionStatus)
	selection := SpriteSessionSelection{name: endpoint.Name, session: destination.Session}
	if !endpointOK || !statusOK || destination.ID != spriteSessionDestinationID(endpoint.Name, destination.Session) || selection.validate() != nil {
		return SpriteSessionSelection{}, fmt.Errorf("%w: invalid Sprite session", ErrInvalidDestination)
	}
	return selection, nil
}

func (s SpriteEndpointSelection) validate() error {
	if spriteapi.ValidName(s.name) != nil {
		return fmt.Errorf("%w: invalid Sprite endpoint selection", ErrInvalidDestination)
	}
	return nil
}

func (s SpriteSessionSelection) validate() error {
	if spriteapi.ValidName(s.name) != nil || s.session == "" {
		return fmt.Errorf("%w: invalid Sprite session selection", ErrInvalidDestination)
	}
	return nil
}

func (o *SpriteOperations) operationBackend() (spriteOperationBackend, error) {
	if o.operations == nil {
		return nil, errors.New("sprite access is disabled")
	}
	return o.operations, nil
}

// ListSpriteSessions checks for the fixed Herdr executable, then lists its
// sessions. It never installs Herdr.
func (o *SpriteOperations) ListSpriteSessions(ctx context.Context, endpoint SpriteEndpointSelection) ([]Destination, error) {
	if err := endpoint.validate(); err != nil {
		return nil, err
	}
	backend, err := o.operationBackend()
	if err != nil {
		return nil, err
	}
	installed, err := backend.HerdrInstalled(ctx, endpoint.name)
	if err != nil {
		return nil, err
	}
	if !installed {
		return nil, ErrSpriteHerdrMissing
	}
	sessions, err := backend.ListHerdrSessions(ctx, endpoint.name)
	if err != nil {
		return nil, err
	}
	return SpriteSessionDestinations(endpoint, sessions)
}

// InstallSpriteHerdr explicitly runs the Herdr preparation and installer flow.
func (o *SpriteOperations) InstallSpriteHerdr(ctx context.Context, endpoint SpriteEndpointSelection) (bool, error) {
	if err := endpoint.validate(); err != nil {
		return false, err
	}
	backend, err := o.operationBackend()
	if err != nil {
		return false, err
	}
	return backend.PrepareHerdr(ctx, endpoint.name)
}

// SyncSpriteHerdrConfig copies local config to an existing Sprite without
// installing Herdr.
func (o *SpriteOperations) SyncSpriteHerdrConfig(ctx context.Context, endpoint SpriteEndpointSelection) error {
	if err := endpoint.validate(); err != nil {
		return err
	}
	backend, err := o.operationBackend()
	if err != nil {
		return err
	}
	return backend.SyncHerdrConfig(ctx, endpoint.name)
}

// StopSpriteSession stops the selected remote Herdr session.
func (o *SpriteOperations) StopSpriteSession(ctx context.Context, selection SpriteSessionSelection) error {
	if err := selection.validate(); err != nil {
		return err
	}
	backend, err := o.operationBackend()
	if err != nil {
		return err
	}
	return backend.StopHerdrSession(ctx, selection.name, selection.session)
}

// SSHIdentity is safe public metadata rendered by the key picker.
type SSHIdentity struct {
	Fingerprint string
	Comment     string
	Algorithm   string
}

func SSHKeyChoices(err error) (string, []SSHIdentity, bool) {
	source, identities, ok := sshagent.SelectionChoices(err)
	if !ok {
		return "", nil, false
	}
	choices := make([]SSHIdentity, len(identities))
	for index, identity := range identities {
		choices[index] = SSHIdentity{Fingerprint: identity.Fingerprint, Comment: identity.Comment, Algorithm: identity.Algorithm}
	}
	return source, choices, true
}

// NeedsSSHKey reports the one pre-effect failure that hrd can repair with
// plain ssh-add before retrying the pending operation.
func NeedsSSHKey(err error) bool { return sshagent.KeyRequired(err) }

func (o *SpriteOperations) PrepareSprite(ctx context.Context, selection SpriteSessionSelection, transport SpriteTransport) (PreparedAttach, error) {
	if err := selection.validate(); err != nil {
		return nil, err
	}
	if transport != NativePreview && transport != SSHFallback {
		return nil, fmt.Errorf("%w: unsupported Sprite transport", ErrInvalidDestination)
	}
	backend, err := o.operationBackend()
	if err != nil {
		return nil, err
	}
	switch transport {
	case NativePreview:
		command, err := backend.PrepareNative(ctx, selection.name, selection.session)
		if err != nil {
			return nil, err
		}
		if command == nil {
			return nil, fmt.Errorf("%w: native Sprite preparation returned no command", ErrInvalidDestination)
		}
		return PreparedNativeAttach{Command: command}, nil
	case SSHFallback:
		prepared, err := backend.PrepareSSH(ctx, selection.name)
		if err != nil {
			return nil, err
		}
		return PrepareSpriteSSH(selection.name, selection.session, prepared)
	}
	panic("unreachable Sprite transport")
}
