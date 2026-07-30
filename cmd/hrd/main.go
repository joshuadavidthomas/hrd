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

Run hrd from a terminal outside Herdr; nested launches are rejected.

Sprite support:
  SPRITE_TOKEN  enable Sprite inventory, creation, and attach
  Native        default; Herdr and its config run inside the Sprite
  SSH fallback  local Herdr uses SSH_AUTH_SOCK, 1Password, or hrd-managed ssh-add

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
	if err := validateLaunchEnvironment(os.Getenv); err != nil {
		_, _ = fmt.Fprintf(stderr, "hrd: %v\n", err)
		return 1
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
		provider := sprites.New(root, token, agentManager)
		defer func() {
			cancel()
			_ = provider.Close()
		}()
		operations = catalog.NewSpriteOperations(local, provider)
	}
	program := tea.NewProgram(ui.New(root, operations), tea.WithContext(root))
	if _, err := program.Run(); err != nil {
		_, _ = fmt.Fprintf(stderr, "hrd: %v\n", err)
		return 1
	}
	return 0
}

func validateLaunchEnvironment(getenv func(string) string) error {
	if getenv("HERDR_ENV") != "" {
		return fmt.Errorf("cannot run inside a Herdr pane; open a terminal outside Herdr")
	}
	return nil
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
