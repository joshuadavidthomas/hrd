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
	"github.com/joshuadavidthomas/hrd/internal/sshagent"
	"github.com/joshuadavidthomas/hrd/internal/ui"
)

const usageText = `hrd — choose a Herdr destination

Usage:
  hrd

Sprite support:
  SPRITE_TOKEN  enable Sprite inventory, creation, and attach
  ssh-agent     reused or started by hrd; ssh-add loads default identities

Keys:
  ↑/↓, j/k  move
  enter     attach
  n         create a Sprite
  /         search
  r         refresh
  ?         help
  q         quit
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr *os.File) int {
	help, err := parseArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hrd: %v\nRun 'hrd --help' for usage.\n", err)
		return 2
	}
	if help {
		_, _ = fmt.Fprint(stdout, usageText)
		return 0
	}

	root, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	local := herdr.New()
	var operations ui.Operations = catalog.NewLocalOperations(local)
	token := os.Getenv("SPRITE_TOKEN")
	_ = os.Unsetenv("SPRITE_TOKEN")
	if token != "" {
		agentManager := sshagent.NewManager(root, os.Getenv("SSH_AUTH_SOCK"))
		defer func() { _ = agentManager.Close() }()
		operations = catalog.NewSpriteOperations(local, sprites.New(root, token, agentManager))
	}
	program := tea.NewProgram(ui.New(root, operations), tea.WithContext(root))
	if _, err := program.Run(); err != nil {
		_, _ = fmt.Fprintf(stderr, "hrd: %v\n", err)
		return 1
	}
	return 0
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
