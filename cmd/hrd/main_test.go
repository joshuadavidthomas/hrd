package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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

func TestScreenOutputFiltersOnlyAlternateScreenResetAcrossSplitWrites(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sequence string
		filtered bool
	}{
		{name: "alternate screen", sequence: ansi.ResetModeAltScreenSaveCursor, filtered: true},
		{name: "window title", sequence: ansi.SetWindowTitle("agent · working")},
		{name: "window title clear", sequence: ansi.SetWindowTitle("")},
	} {
		for split := 1; split < len(tc.sequence); split++ {
			t.Run(fmt.Sprintf("%s/split-%d", tc.name, split), func(t *testing.T) {
				output, err := os.CreateTemp(t.TempDir(), "terminal")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = output.Close() }()
				screen := newScreenOutput(output)
				for _, chunk := range []string{"before", tc.sequence[:split], tc.sequence[split:], "after"} {
					if _, err := screen.Write([]byte(chunk)); err != nil {
						t.Fatal(err)
					}
				}
				if err := screen.Restore(); err != nil {
					t.Fatal(err)
				}
				if _, err := output.Seek(0, 0); err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(output)
				if err != nil {
					t.Fatal(err)
				}
				forwarded := tc.sequence
				if tc.filtered {
					forwarded = ansi.EraseEntireScreen + ansi.CursorHomePosition
				}
				want := "before" + forwarded + "after" + ansi.ShowCursor + ansi.ResetModeAltScreenSaveCursor
				if string(got) != want {
					t.Fatalf("output = %q, want %q", got, want)
				}
			})
		}
	}
}

func TestScreenOutputKeepsShellHiddenThroughAttachAndDetach(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	screen := newScreenOutput(output)
	title := ansi.SetWindowTitle("hrd · devbox · work")
	trace := []string{
		ansi.SetModeAltScreenSaveCursor + title + "picker",
		ansi.ResetModeAltScreenSaveCursor + ansi.SetWindowTitle(""),
		ansi.SetModeAltScreenSaveCursor + "remote",
		ansi.ResetModeAltScreenSaveCursor + ansi.SetWindowTitle(""),
		ansi.SetModeAltScreenSaveCursor + "picker-restored",
	}
	for _, chunk := range trace {
		if _, err := screen.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := screen.Restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(got), ansi.ResetModeAltScreenSaveCursor); count != 1 {
		t.Fatalf("alternate-screen resets = %d, want only final restore: %q", count, got)
	}
	clear := ansi.EraseEntireScreen + ansi.CursorHomePosition
	if count := strings.Count(string(got), clear); count != 2 {
		t.Fatalf("alternate-screen clears = %d, want one per filtered reset: %q", count, got)
	}
	if !strings.Contains(string(got), "remote"+clear) {
		t.Fatalf("remote final frame was not cleared before picker restore: %q", got)
	}
	if count := strings.Count(string(got), ansi.SetWindowTitle("")); count != 2 {
		t.Fatalf("forwarded window-title resets = %d, want 2: %q", count, got)
	}
	if !strings.Contains(string(got), title) {
		t.Fatalf("terminal trace lost forwarded title: %q", got)
	}
	if !strings.HasSuffix(string(got), ansi.ShowCursor+ansi.ResetModeAltScreenSaveCursor) {
		t.Fatalf("terminal trace lacks final restore: %q", got)
	}
	for _, want := range []string{"picker", "remote", "picker-restored"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("terminal trace lacks %q: %q", want, got)
		}
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
	for _, want := range []string{"Usage:", "SPRITE_TOKEN", "Native", "default", "SSH", "1Password", "enter", "session", "restart"} {
		if !strings.Contains(usageText, want) {
			t.Errorf("usage text does not contain %q", want)
		}
	}
}
