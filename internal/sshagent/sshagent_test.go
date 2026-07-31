package sshagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestDiscoverCandidatesFindsOnePasswordSocket(t *testing.T) {
	t.Parallel()
	home := shortTempDir(t)
	dir := filepath.Join(home, ".1password")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	candidates := discoverCandidates("", home, "linux")
	if len(candidates) != 1 || candidates[0].source != "1Password" || candidates[0].addable {
		t.Fatalf("candidates = %#v, want one read-only 1Password socket", candidates)
	}
}

func TestManagerRequiresSelectionAndReturnsOnlyChosenKey(t *testing.T) {
	t.Parallel()
	dir := shortTempDir(t)
	socket := filepath.Join(dir, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	for _, comment := range []string{"work", "personal"} {
		_, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := keyring.Add(agent.AddedKey{PrivateKey: privateKey, Comment: comment}); err != nil {
			t.Fatal(err)
		}
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() { defer workers.Done(); _ = agent.ServeAgent(keyring, connection); _ = connection.Close() }()
		}
	}()
	root, cancel := context.WithCancel(context.Background())
	manager := newManager(root, []socketCandidate{{path: socket, source: "1Password"}})
	_, selectionErr := manager.Load(context.Background())
	source, choices, ok := SelectionChoices(selectionErr)
	if !ok || source != "1Password" || len(choices) != 2 {
		t.Fatalf("selection = (%q, %#v, %v), error %v", source, choices, ok, selectionErr)
	}
	chosen := choices[1]
	if err := manager.Select(chosen.Fingerprint); err != nil {
		t.Fatal(err)
	}
	credentials, err := manager.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() after selection error = %v", err)
	}
	if len(credentials.PublicKeys) != 1 || len(credentials.Signers) != 1 {
		t.Fatalf("selected credentials = (%d keys, %d signers)", len(credentials.PublicKeys), len(credentials.Signers))
	}
	if got := ssh.FingerprintSHA256(credentials.Signers[0].PublicKey()); got != chosen.Fingerprint {
		t.Fatalf("selected fingerprint = %q, want %q", got, chosen.Fingerprint)
	}
	_ = credentials.Close()
	if err := keyring.RemoveAll(); err != nil {
		t.Fatal(err)
	}
	_, replacement, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := keyring.Add(agent.AddedKey{PrivateKey: replacement, Comment: "replacement"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Load(context.Background()); !errors.Is(err, ErrSelectedKeyUnavailable) {
		t.Fatalf("Load() after chosen key disappeared error = %v, want ErrSelectedKeyUnavailable", err)
	}
	_ = manager.Close()
	cancel()
	_ = listener.Close()
	workers.Wait()
}

func TestEmptyEnvironmentAgentFallsThroughToOnePassword(t *testing.T) {
	t.Parallel()
	dir := shortTempDir(t)
	emptyKeyring := agent.NewKeyring()
	emptyServer := startTestAgentServer(t, filepath.Join(dir, "empty.sock"), emptyKeyring)
	defer emptyServer.close()
	onePasswordKeyring := agent.NewKeyring()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := onePasswordKeyring.Add(agent.AddedKey{PrivateKey: privateKey, Comment: "1Password key"}); err != nil {
		t.Fatal(err)
	}
	onePasswordServer := startTestAgentServer(t, filepath.Join(dir, "onepassword.sock"), onePasswordKeyring)
	defer onePasswordServer.close()
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := newManager(root, []socketCandidate{
		{path: emptyServer.path, source: "ssh-agent", addable: true},
		{path: onePasswordServer.path, source: "1Password", addable: false},
	})
	defer func() { _ = manager.Close() }()
	credentials, err := manager.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	defer func() { _ = credentials.Close() }()
	if manager.Socket() != onePasswordServer.path || len(credentials.PublicKeys) != 1 {
		t.Fatalf("selected socket/keys = (%q, %d), want populated 1Password agent", manager.Socket(), len(credentials.PublicKeys))
	}
}

func TestManagerStartsPrivateAgentAndBuildsPlainSSHAdd(t *testing.T) {
	t.Setenv("SPRITE_TOKEN", "must-not-reach-ssh-add")
	if _, err := exec.LookPath("ssh-agent"); err != nil {
		t.Skip("ssh-agent is not installed")
	}
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := newManager(root, nil)
	defer func() { _ = manager.Close() }()

	command, err := manager.SSHAddCommand(context.Background())
	if err != nil {
		t.Fatalf("SSHAddCommand() error = %v", err)
	}
	if want := []string{"ssh-add"}; !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("ssh-add args = %#v, want %#v", command.Args, want)
	}
	if token := environmentValue(command.Env, "SPRITE_TOKEN"); token != "" {
		t.Fatalf("ssh-add inherited SPRITE_TOKEN = %q", token)
	}
	socket := environmentValue(command.Env, "SSH_AUTH_SOCK")
	if socket == "" || socket != manager.Socket() {
		t.Fatalf("ssh-add socket = %q, manager socket = %q", socket, manager.Socket())
	}
	if _, err := os.Stat(socket); err != nil {
		t.Fatalf("managed socket does not exist: %v", err)
	}
	if _, err := manager.Load(context.Background()); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("Load() error = %v, want ErrNoKeys", err)
	}

	dir := filepath.Dir(socket)
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed socket directory remains after close: %v", err)
	}
}

func TestRootCancellationStopsManagedAgent(t *testing.T) {
	if _, err := exec.LookPath("ssh-agent"); err != nil {
		t.Skip("ssh-agent is not installed")
	}
	root, cancel := context.WithCancel(context.Background())
	manager := newManager(root, nil)
	command, err := manager.SSHAddCommand(context.Background())
	if err != nil {
		t.Fatalf("SSHAddCommand() error = %v", err)
	}
	dir := filepath.Dir(environmentValue(command.Env, "SSH_AUTH_SOCK"))
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := os.Stat(dir)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed agent directory remains after root cancellation: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManagerWithoutSocketNeedsAKey(t *testing.T) {
	t.Parallel()
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := newManager(root, nil)
	defer func() { _ = manager.Close() }()
	_, err := manager.Load(context.Background())
	if !errors.Is(err, ErrAgentUnavailable) || !KeyRequired(err) {
		t.Fatalf("Load() error = %v, want key-required unavailable agent", err)
	}
}

func TestWithEnvironmentReplacesSocket(t *testing.T) {
	t.Parallel()
	got := withEnvironment([]string{"A=1", "SSH_AUTH_SOCK=old", "B=2"}, "SSH_AUTH_SOCK", "new")
	if value := environmentValue(got, "SSH_AUTH_SOCK"); value != "new" {
		t.Fatalf("SSH_AUTH_SOCK = %q, want new", value)
	}
	count := 0
	for _, entry := range got {
		if strings.HasPrefix(entry, "SSH_AUTH_SOCK=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("environment has %d SSH_AUTH_SOCK entries, want 1: %#v", count, got)
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hrd-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type testAgentServer struct {
	path     string
	listener net.Listener
	workers  sync.WaitGroup
}

func startTestAgentServer(t *testing.T, path string, keyring agent.Agent) *testAgentServer {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &testAgentServer{path: path, listener: listener}
	server.workers.Add(1)
	go func() {
		defer server.workers.Done()
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			server.workers.Add(1)
			go func() {
				defer server.workers.Done()
				_ = agent.ServeAgent(keyring, connection)
				_ = connection.Close()
			}()
		}
	}()
	return server
}

func (s *testAgentServer) close() {
	_ = s.listener.Close()
	s.workers.Wait()
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
