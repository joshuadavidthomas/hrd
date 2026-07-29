// Package sshagent manages the public credentials used for Sprite SSH access.
package sshagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
)

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

// Manager reuses a caller-provided agent or starts one foreground agent owned
// by hrd. Its socket is explicit; it never changes process-wide environment.
type Manager struct {
	root           context.Context
	originalSocket string

	mu        sync.Mutex
	socket    string
	owned     *ownedAgent
	closed    bool
	closeOnce sync.Once
}

type ownedAgent struct {
	command *exec.Cmd
	dir     string
	socket  string
	done    chan struct{}
}

func NewManager(root context.Context, originalSocket string) *Manager {
	if root == nil {
		panic("sshagent: nil root context")
	}
	manager := &Manager{root: root, originalSocket: originalSocket, socket: originalSocket}
	go func() {
		<-root.Done()
		_ = manager.Close()
	}()
	return manager
}

// Load reads public keys from the selected agent and retains agent-backed
// signers until Credentials.Close.
func (m *Manager) Load(ctx context.Context) (*Credentials, error) {
	m.mu.Lock()
	socket := m.socket
	closed := m.closed
	m.mu.Unlock()
	if closed || socket == "" {
		return nil, ErrAgentUnavailable
	}
	return load(ctx, socket)
}

// Socket returns the explicit socket selected for Sprite SSH children.
func (m *Manager) Socket() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.socket
}

// SSHAddCommand returns plain ssh-add with no key path arguments. It starts a
// private foreground agent first when the inherited socket is absent or stale.
func (m *Manager) SSHAddCommand(ctx context.Context) (*exec.Cmd, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, context.Canceled
	}
	if m.socket != "" && socketResponds(ctx, m.socket) {
		socket := m.socket
		m.mu.Unlock()
		return sshAddCommand(m.root, socket), nil
	}
	if m.owned != nil {
		select {
		case <-m.owned.done:
			_ = os.RemoveAll(m.owned.dir)
			m.owned = nil
			m.socket = m.originalSocket
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
	m.socket = owned.socket
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
		m.socket = ""
		m.mu.Unlock()
		if owned != nil {
			stopAgent(owned)
		}
	})
	return nil
}

func (m *Manager) stopOwned(owned *ownedAgent) {
	m.mu.Lock()
	if m.owned == owned {
		m.owned = nil
		m.socket = m.originalSocket
	}
	m.mu.Unlock()
	stopAgent(owned)
}

func load(ctx context.Context, socket string) (*Credentials, error) {
	loadCtx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(loadCtx, "unix", socket)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("%w: %v", ErrAgentUnavailable, err)
		}
		return nil, fmt.Errorf("connect to ssh-agent: %w", err)
	}
	_ = connection.SetDeadline(time.Now().Add(loadTimeout))
	client := agent.NewClient(connection)
	keys, err := client.List()
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("read ssh-agent keys: %w", err)
	}
	signers, err := client.Signers()
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("read ssh-agent signers: %w", err)
	}
	if len(keys) == 0 || len(signers) == 0 {
		_ = connection.Close()
		return nil, ErrNoKeys
	}
	lines := make([]string, 0, len(keys))
	for _, identity := range keys {
		key, err := ssh.ParsePublicKey(identity.Blob)
		if err != nil {
			_ = connection.Close()
			return nil, fmt.Errorf("decode ssh-agent public key: %w", err)
		}
		lines = append(lines, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))))
	}
	_ = connection.SetDeadline(time.Time{})
	return &Credentials{PublicKeys: lines, Signers: signers, connection: connection}, nil
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
