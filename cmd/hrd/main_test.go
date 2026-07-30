package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestParseArgsRecognizesHelp(t *testing.T) {
	t.Parallel()
	for _, argument := range []string{"-h", "--help"} {
		help, err := parseArgs([]string{argument})
		if err != nil || !help {
			t.Errorf("parseArgs(%q) = (%v, %v), want (true, nil)", argument, help, err)
		}
	}
}

func TestParseArgsRejectsUnexpectedArguments(t *testing.T) {
	t.Parallel()
	if _, err := parseArgs([]string{"--wat"}); err == nil {
		t.Fatal("parseArgs accepted an unexpected argument")
	}
}

func TestRunRejectsNestedHerdr(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	stdout, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()

	if code := run(nil, stdout, stderr); code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if _, err := stderr.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "outside Herdr") {
		t.Fatalf("stderr = %q, want nested Herdr guidance", output)
	}
}

func TestValidateLaunchEnvironmentAcceptsAnOrdinaryTerminal(t *testing.T) {
	t.Parallel()
	if err := validateLaunchEnvironment(func(string) string { return "" }); err != nil {
		t.Fatalf("validateLaunchEnvironment() error = %v", err)
	}
}

func TestUsageExplainsSpriteRequirements(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"Usage:", "SPRITE_TOKEN", "Native", "default", "SSH fallback", "1Password", "enter", "attach"} {
		if !strings.Contains(usageText, want) {
			t.Errorf("usage text does not contain %q", want)
		}
	}
}
