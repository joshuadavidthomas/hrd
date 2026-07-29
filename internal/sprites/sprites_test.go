package sprites

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	sdk "github.com/superfly/sprites-go"

	"github.com/joshuadavidthomas/hrd/internal/sshagent"
)

func TestValidName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"a", "sprite-1", "a123", "two--hyphens"} {
		if err := ValidName(name); err != nil {
			t.Errorf("ValidName(%q) error = %v", name, err)
		}
	}
	for _, name := range []string{"", "-sprite", "sprite-", "Sprite", "a_b", "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijkl"} {
		if err := ValidName(name); err == nil {
			t.Errorf("ValidName(%q) succeeded", name)
		}
	}
}

func TestCreateMakesOneProviderRequestWithoutAgentAccess(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sprites" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"name":"valid-name"}`)
	}))
	defer server.Close()

	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := sshagent.NewManager(root, "")
	_ = manager.Close()
	client := &Client{sdk: sdk.New("token", sdk.WithBaseURL(server.URL)), agent: manager}
	result := client.Create(context.Background(), "valid-name")
	if !result.Confirmed || result.Err != nil {
		t.Fatalf("Create() result = %#v, want confirmed creation", result)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider requests = %d, want 1", got)
	}
}

func TestSSHProbeUsesTheOpenSSHEd25519HostKey(t *testing.T) {
	t.Parallel()
	if want := []string{"ssh-ed25519"}; !slices.Equal(spriteHostKeyAlgorithms, want) {
		t.Fatalf("host key algorithms = %#v, want %#v", spriteHostKeyAlgorithms, want)
	}
}

func TestWaitForServiceRunningAcceptsAnUpdatedRunningService(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sprites/devbox/services/hrd-sshd" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		status := "starting"
		if requests.Add(1) > 1 {
			status = "running"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"name":"hrd-sshd","cmd":"/usr/sbin/sshd","state":{"name":"hrd-sshd","status":%q}}`, status)
	}))
	defer server.Close()

	client := &Client{sdk: sdk.New("token", sdk.WithBaseURL(server.URL))}
	if err := client.waitForServiceRunning(context.Background(), "devbox"); err != nil {
		t.Fatalf("waitForServiceRunning() error = %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("service requests = %d, want 2", got)
	}
}

func TestPaginationStopsWhenProviderOmitsContinuationToken(t *testing.T) {
	t.Parallel()
	if !terminalPage(true, "") {
		t.Fatal("empty continuation token must stop pagination")
	}
	if terminalPage(true, "next") {
		t.Fatal("non-empty continuation token must continue pagination")
	}
	if !terminalPage(false, "next") {
		t.Fatal("has_more=false must stop pagination")
	}
}

func TestLifecycleOnlyMapsProviderState(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]State{"running": Running, "warm": Warm, "cold": Cold, "other": Unknown} {
		if got := lifecycle(input); got != want {
			t.Errorf("lifecycle(%q) = %q, want %q", input, got, want)
		}
	}
}
