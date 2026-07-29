// Command hrd opens a picker for Herdr destinations.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	"github.com/joshuadavidthomas/hrd/internal/herdr"
	"github.com/joshuadavidthomas/hrd/internal/sprites"
	"github.com/joshuadavidthomas/hrd/internal/ui"
)

const usageText = `hrd — choose a Herdr destination

Usage:
  hrd

Sprite support:
  SPRITE_TOKEN  enable Sprite inventory, creation, and attach
  ssh-agent     must hold at least one key for Sprite attach

Keys:
  ↑/↓, j/k  move
  enter     attach
  n         create a Sprite
  /         search
  r         refresh
  ?         help
  q         quit
`

func main() {
	help, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "hrd: %v\nRun 'hrd --help' for usage.\n", err)
		os.Exit(2)
	}
	if help {
		_, _ = fmt.Fprint(os.Stdout, usageText)
		return
	}

	root, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	local := herdr.New()
	var operations ui.Operations = catalog.NewLocalOperations(local)
	token := os.Getenv("SPRITE_TOKEN")
	_ = os.Unsetenv("SPRITE_TOKEN")
	if spriteClient := sprites.New(root, token); spriteClient != nil {
		operations = catalog.NewSpriteOperations(local, spriteClient)
	}
	program := tea.NewProgram(ui.New(root, operations), tea.WithContext(root))
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "hrd: %v\n", err)
		os.Exit(1)
	}
}

func parseArgs(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		return true, nil
	}
	return false, fmt.Errorf("unexpected argument %q", args[0])
}
