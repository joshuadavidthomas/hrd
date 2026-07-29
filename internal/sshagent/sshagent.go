// Package sshagent manages the public credentials used for Sprite SSH access.
package sshagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	loadTimeout     = 5 * time.Second
	startupTimeout  = 2 * time.Second
	shutdownTimeout = time.Second
)

var (
	// ErrAgentUnavailable means no usable ssh-agent socket is available.
	ErrAgentUnavailable = errors.New("ssh-agent is unavailable")
	// ErrNoKeys means the selected ssh-agent is healthy but has no keys.
	ErrNoKeys = errors.New("ssh-agent has no keys")
	// ErrSelectedKeyUnavailable means the exact key chosen earlier disappeared.
	ErrSelectedKeyUnavailable = errors.New("selected SSH key is unavailable")
)

// Identity is safe public metadata for one agent-backed SSH key.
type Identity struct {
	Fingerprint string
	Comment     string
	Algorithm   string
}

// SelectionRequired means an agent exposed several keys and hrd must choose
// one before a provider request or remote mutation.
type SelectionRequired struct {
	Source     string
	Identities []Identity
}

func (e *SelectionRequired) Error() string {
	return fmt.Sprintf("%s exposes %d SSH keys; choose one", e.Source, len(e.Identities))
}

// SelectionChoices extracts safe public identities from err.
func SelectionChoices(err error) (string, []Identity, bool) {
	var selection *SelectionRequired
	if !errors.As(err, &selection) {
		return "", nil, false
	}
	return selection.Source, append([]Identity(nil), selection.Identities...), true
}

// KeyRequired reports whether ssh-add can recover from err before any Sprite
// provider request or remote mutation has occurred.
func KeyRequired(err error) bool {
	return errors.Is(err, ErrAgentUnavailable) || errors.Is(err, ErrNoKeys)
}

// Credentials owns the agent connection because its signers use it during SSH
// authentication. It contains no private key material.
type Credentials struct {
	PublicKeys []string
	Signers    []ssh.Signer
	connection net.Conn
}

func (c *Credentials) Close() error {
	if c == nil || c.connection == nil {
		return nil
	}
	err := c.connection.Close()
	c.connection = nil
	return err
}

func (c *Credentials) AuthorizedKeys() string {
	if c == nil || len(c.PublicKeys) == 0 {
		return ""
	}
	return strings.Join(c.PublicKeys, "\n") + "\n"
}

type socketCandidate struct {
	path    string
	source  string
	addable bool
}

// Manager discovers external agents and starts one foreground agent owned by
// hrd as a fallback. Its socket is explicit; process-wide environment is never
// changed.
type Manager struct {
	root       context.Context
	candidates []socketCandidate

	mu                  sync.Mutex
	active              socketCandidate
	selectedFingerprint string
	selectedSocket      string
	owned               *ownedAgent
	closed              bool
	closeOnce           sync.Once
}

type ownedAgent struct {
	command *exec.Cmd
	dir     string
	socket  string
	done    chan struct{}
}

func NewManager(root context.Context, environmentSocket string) *Manager {
	home, _ := os.UserHomeDir()
	return newManager(root, discoverCandidates(environmentSocket, home, runtime.GOOS))
}

func newManager(root context.Context, candidates []socketCandidate) *Manager {
	if root == nil {
		panic("sshagent: nil root context")
	}
	manager := &Manager{root: root, candidates: append([]socketCandidate(nil), candidates...)}
	go func() {
		<-root.Done()
		_ = manager.Close()
	}()
	return manager
}

// Load selects one agent and returns exactly one credential. A single exposed
// key is selected automatically; several keys require an explicit selection.
func (m *Manager) Load(ctx context.Context) (*Credentials, error) {
	candidates, selectedFingerprint, selectedSocket, closed := m.loadCandidates()
	if closed {
		return nil, ErrAgentUnavailable
	}
	if selectedFingerprint != "" {
		return m.loadSelected(ctx, candidates, selectedSocket, selectedFingerprint)
	}

	var empty *socketCandidate
	for _, candidate := range candidates {
		credentials, identities, err := load(ctx, candidate.path)
		if err != nil {
			switch {
			case errors.Is(err, ErrAgentUnavailable):
				continue
			case errors.Is(err, ErrNoKeys):
				if empty == nil || (!empty.addable && candidate.addable) {
					copy := candidate
					empty = &copy
				}
				continue
			default:
				m.setActive(candidate)
				return nil, err
			}
		}
		m.setActive(candidate)
		if len(identities) == 1 {
			m.setSelected(candidate.path, identities[0].Fingerprint)
			return credentials, nil
		}
		_ = credentials.Close()
		return nil, &SelectionRequired{Source: candidate.source, Identities: identities}
	}
	if empty != nil {
		m.setActive(*empty)
		return nil, ErrNoKeys
	}
	return nil, ErrAgentUnavailable
}

func (m *Manager) loadSelected(ctx context.Context, candidates []socketCandidate, socket, fingerprint string) (*Credentials, error) {
	for _, candidate := range candidates {
		if candidate.path != socket {
			continue
		}
		credentials, identities, err := load(ctx, candidate.path)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("%w: %v", ErrSelectedKeyUnavailable, err)
		}
		for index, identity := range identities {
			if identity.Fingerprint == fingerprint {
				m.setActive(candidate)
				return selectCredential(credentials, index), nil
			}
		}
		_ = credentials.Close()
		return nil, ErrSelectedKeyUnavailable
	}
	return nil, ErrSelectedKeyUnavailable
}

// Select remembers one public fingerprint for this hrd process. The next Load
// validates that the selected agent still exposes it.
func (m *Manager) Select(fingerprint string) error {
	if fingerprint == "" {
		return errors.New("empty SSH key fingerprint")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return context.Canceled
	}
	if m.active.path == "" {
		return errors.New("no active SSH agent")
	}
	m.selectedFingerprint = fingerprint
	m.selectedSocket = m.active.path
	return nil
}

// Socket returns the explicit socket selected for Sprite SSH children.
func (m *Manager) Socket() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active.path
}

// SSHAddCommand returns plain ssh-add with no key path arguments. It starts a
// private foreground agent when the active source cannot accept added keys.
func (m *Manager) SSHAddCommand(ctx context.Context) (*exec.Cmd, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, context.Canceled
	}
	if m.active.path != "" && m.active.addable && socketResponds(ctx, m.active.path) {
		socket := m.active.path
		m.mu.Unlock()
		return sshAddCommand(m.root, socket), nil
	}
	if m.owned != nil {
		select {
		case <-m.owned.done:
			_ = os.RemoveAll(m.owned.dir)
			m.owned = nil
			m.active = socketCandidate{}
			m.selectedFingerprint = ""
			m.selectedSocket = ""
		default:
			owned := m.owned
			m.mu.Unlock()
			if err := waitUntilReady(ctx, owned.socket, owned.done); err != nil {
				return nil, err
			}
			return sshAddCommand(m.root, owned.socket), nil
		}
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	owned, err := startAgent(m.root)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.owned = owned
	m.active = socketCandidate{path: owned.socket, source: "hrd ssh-agent", addable: true}
	m.selectedFingerprint = ""
	m.selectedSocket = ""
	m.mu.Unlock()

	if err := waitUntilReady(ctx, owned.socket, owned.done); err != nil {
		m.stopOwned(owned)
		return nil, err
	}
	return sshAddCommand(m.root, owned.socket), nil
}

func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		owned := m.owned
		m.owned = nil
		m.active = socketCandidate{}
		m.selectedFingerprint = ""
		m.selectedSocket = ""
		m.mu.Unlock()
		if owned != nil {
			stopAgent(owned)
		}
	})
	return nil
}

func (m *Manager) loadCandidates() ([]socketCandidate, string, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, "", "", true
	}
	result := make([]socketCandidate, 0, len(m.candidates)+1)
	seen := make(map[string]struct{}, len(m.candidates)+1)
	if m.owned != nil {
		candidate := socketCandidate{path: m.owned.socket, source: "hrd ssh-agent", addable: true}
		result = append(result, candidate)
		seen[candidate.path] = struct{}{}
	}
	if m.active.path != "" {
		if _, exists := seen[m.active.path]; !exists {
			result = append(result, m.active)
			seen[m.active.path] = struct{}{}
		}
	}
	for _, candidate := range m.candidates {
		if _, exists := seen[candidate.path]; !exists {
			result = append(result, candidate)
			seen[candidate.path] = struct{}{}
		}
	}
	return result, m.selectedFingerprint, m.selectedSocket, false
}

func (m *Manager) setActive(candidate socketCandidate) {
	m.mu.Lock()
	m.active = candidate
	m.mu.Unlock()
}

func (m *Manager) setSelected(socket, fingerprint string) {
	m.mu.Lock()
	m.selectedSocket = socket
	m.selectedFingerprint = fingerprint
	m.mu.Unlock()
}

func (m *Manager) stopOwned(owned *ownedAgent) {
	m.mu.Lock()
	if m.owned == owned {
		m.owned = nil
		m.active = socketCandidate{}
		m.selectedFingerprint = ""
		m.selectedSocket = ""
	}
	m.mu.Unlock()
	stopAgent(owned)
}

func discoverCandidates(environmentSocket, home, goos string) []socketCandidate {
	paths := onePasswordSocketPaths(home, goos)
	result := make([]socketCandidate, 0, len(paths)+1)
	seen := make(map[string]struct{}, len(paths)+1)
	if environmentSocket != "" {
		source, addable := "ssh-agent", true
		for _, path := range paths {
			if environmentSocket == path {
				source, addable = "1Password", false
			}
		}
		result = append(result, socketCandidate{path: environmentSocket, source: source, addable: addable})
		seen[environmentSocket] = struct{}{}
	}
	for _, path := range paths {
		if _, exists := seen[path]; exists || !isSocket(path) {
			continue
		}
		result = append(result, socketCandidate{path: path, source: "1Password", addable: false})
		seen[path] = struct{}{}
	}
	return result
}

func onePasswordSocketPaths(home, goos string) []string {
	if home == "" {
		return nil
	}
	switch goos {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Group Containers", "2BUA8C4S2C.com.1password", "t", "agent.sock")}
	case "linux":
		return []string{filepath.Join(home, ".1password", "agent.sock")}
	default:
		return nil
	}
}

func isSocket(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

func load(ctx context.Context, socket string) (*Credentials, []Identity, error) {
	loadCtx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(loadCtx, "unix", socket)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, nil, fmt.Errorf("%w: %v", ErrAgentUnavailable, err)
		}
		return nil, nil, fmt.Errorf("connect to ssh-agent: %w", err)
	}
	_ = connection.SetDeadline(time.Now().Add(loadTimeout))
	client := agent.NewClient(connection)
	keys, err := client.List()
	if err != nil {
		_ = connection.Close()
		return nil, nil, fmt.Errorf("read ssh-agent keys: %w", err)
	}
	signers, err := client.Signers()
	if err != nil {
		_ = connection.Close()
		return nil, nil, fmt.Errorf("read ssh-agent signers: %w", err)
	}
	if len(keys) == 0 || len(signers) == 0 {
		_ = connection.Close()
		return nil, nil, ErrNoKeys
	}
	lines := make([]string, 0, len(keys))
	identities := make([]Identity, 0, len(keys))
	orderedSigners := make([]ssh.Signer, 0, len(keys))
	for _, agentKey := range keys {
		publicKey, err := ssh.ParsePublicKey(agentKey.Blob)
		if err != nil {
			_ = connection.Close()
			return nil, nil, fmt.Errorf("decode ssh-agent public key: %w", err)
		}
		signer, ok := matchingSigner(publicKey, signers)
		if !ok {
			_ = connection.Close()
			return nil, nil, errors.New("ssh-agent signer does not match its public key")
		}
		lines = append(lines, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey))))
		orderedSigners = append(orderedSigners, signer)
		identities = append(identities, Identity{
			Fingerprint: ssh.FingerprintSHA256(publicKey),
			Comment:     agentKey.Comment,
			Algorithm:   publicKey.Type(),
		})
	}
	_ = connection.SetDeadline(time.Time{})
	return &Credentials{PublicKeys: lines, Signers: orderedSigners, connection: connection}, identities, nil
}

func matchingSigner(publicKey ssh.PublicKey, signers []ssh.Signer) (ssh.Signer, bool) {
	for _, signer := range signers {
		if bytes.Equal(publicKey.Marshal(), signer.PublicKey().Marshal()) {
			return signer, true
		}
	}
	return nil, false
}

func selectCredential(credentials *Credentials, index int) *Credentials {
	credentials.PublicKeys = []string{credentials.PublicKeys[index]}
	credentials.Signers = []ssh.Signer{credentials.Signers[index]}
	return credentials
}

func startAgent(root context.Context) (*ownedAgent, error) {
	dir, err := os.MkdirTemp("", "hrd-agent-")
	if err != nil {
		return nil, fmt.Errorf("create ssh-agent socket directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("protect ssh-agent socket directory: %w", err)
	}
	socket := filepath.Join(dir, "agent.sock")
	if len(socket) >= 100 {
		_ = os.RemoveAll(dir)
		return nil, errors.New("ssh-agent socket path is too long")
	}
	command := exec.CommandContext(root, "ssh-agent", "-D", "-a", socket)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start ssh-agent: %w", err)
	}
	owned := &ownedAgent{command: command, dir: dir, socket: socket, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		close(owned.done)
	}()
	return owned, nil
}

func waitUntilReady(ctx context.Context, socket string, done <-chan struct{}) error {
	startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if socketResponds(startupCtx, socket) {
			return nil
		}
		select {
		case <-startupCtx.Done():
			return fmt.Errorf("ssh-agent did not become ready: %w", startupCtx.Err())
		case <-done:
			return errors.New("ssh-agent exited before its socket became ready")
		case <-ticker.C:
		}
	}
}

func socketResponds(ctx context.Context, socket string) bool {
	if socket == "" {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(probeCtx, "unix", socket)
	if err != nil {
		return false
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(100 * time.Millisecond))
	_, err = agent.NewClient(connection).List()
	return err == nil
}

func sshAddCommand(root context.Context, socket string) *exec.Cmd {
	command := exec.CommandContext(root, "ssh-add")
	environment := withoutEnvironment(os.Environ(), "SPRITE_TOKEN")
	command.Env = withEnvironment(environment, "SSH_AUTH_SOCK", socket)
	return command
}

func withoutEnvironment(environment []string, key string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return result
}

func withEnvironment(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func stopAgent(owned *ownedAgent) {
	if owned == nil {
		return
	}
	select {
	case <-owned.done:
	default:
		_ = owned.command.Process.Signal(syscall.SIGTERM)
		select {
		case <-owned.done:
		case <-time.After(shutdownTimeout):
			_ = owned.command.Process.Kill()
			<-owned.done
		}
	}
	_ = os.RemoveAll(owned.dir)
}
