package sshagent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestManagerStartsPrivateAgentAndBuildsPlainSSHAdd(t *testing.T) {
	t.Setenv("SPRITE_TOKEN", "must-not-reach-ssh-add")
	if _, err := exec.LookPath("ssh-agent"); err != nil {
		t.Skip("ssh-agent is not installed")
	}
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewManager(root, "")
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
	manager := NewManager(root, "")
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
	manager := NewManager(root, "")
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

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
