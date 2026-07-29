package sprites

import "testing"

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

func TestLifecycleOnlyMapsProviderState(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]State{"running": Running, "warm": Warm, "cold": Cold, "other": Unknown} {
		if got := lifecycle(input); got != want {
			t.Errorf("lifecycle(%q) = %q, want %q", input, got, want)
		}
	}
}
