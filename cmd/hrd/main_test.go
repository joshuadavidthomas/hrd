package main

import (
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

func TestUsageExplainsSpriteRequirements(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"Usage:", "SPRITE_TOKEN", "ssh-agent", "enter", "attach"} {
		if !strings.Contains(usageText, want) {
			t.Errorf("usage text does not contain %q", want)
		}
	}
}
