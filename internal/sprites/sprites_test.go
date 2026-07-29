package sprites

import (
	"context"
	"testing"

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

func TestCreateRequiresAKeyBeforeCallingSDK(t *testing.T) {
	t.Parallel()
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := sshagent.NewManager(root, "")
	_ = manager.Close()
	client := &Client{agent: manager}
	result := client.Create(context.Background(), "valid-name")
	if !result.Rejected || !sshagent.KeyRequired(result.Err) {
		t.Fatalf("Create() result = %#v, want key-required rejection", result)
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
