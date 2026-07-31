// Command hrd opens a picker for Herdr destinations.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

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
  SPRITE_TOKEN  enable Sprite inventory, creation, and session management
  Native        default; Herdr and its config run inside the Sprite
  SSH           alternate transport using SSH_AUTH_SOCK, 1Password, or ssh-add

Keys:
  ↑/↓, j/k  move
  enter     open an endpoint or attach a session
  n         create a Sprite
  s / R     stop / restart a running Sprite session
  /         search
  r         refresh
  esc       return to endpoints
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
	programOutput := io.Writer(stdout)
	var screen *screenOutput
	if term.IsTerminal(stdout.Fd()) {
		screen = newScreenOutput(stdout)
		programOutput = screen
	}
	program := tea.NewProgram(ui.New(root, operations), tea.WithContext(root), tea.WithOutput(programOutput))
	_, runErr := program.Run()
	var restoreErr error
	if screen != nil {
		restoreErr = screen.Restore()
	}
	if err := errors.Join(runErr, restoreErr); err != nil {
		_, _ = fmt.Fprintf(stderr, "hrd: %v\n", err)
		return 1
	}
	return 0
}

// screenOutput keeps hrd's alternate screen selected while Bubble Tea lends the
// terminal to Herdr. It clears Herdr's final frame when either program tries to
// leave that screen. The final reset bypasses this filter in Restore.
type screenOutput struct {
	*os.File
	mu      sync.Mutex
	pending []byte
}

var _ interface {
	io.ReadWriteCloser
	Fd() uintptr
} = (*screenOutput)(nil)

func newScreenOutput(output *os.File) *screenOutput {
	return &screenOutput{File: output}
}

func (o *screenOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	target := []byte(ansi.ResetModeAltScreenSaveCursor)
	input := append(append([]byte(nil), o.pending...), p...)
	o.pending = nil
	filtered := make([]byte, 0, len(input))
	for len(input) > 0 {
		if bytes.HasPrefix(input, target) {
			input = input[len(target):]
			filtered = append(filtered, ansi.EraseEntireScreen...)
			filtered = append(filtered, ansi.CursorHomePosition...)
			continue
		}
		if len(input) < len(target) && bytes.HasPrefix(target, input) {
			o.pending = append(o.pending, input...)
			break
		}
		filtered = append(filtered, input[0])
		input = input[1:]
	}
	if err := writeAll(o.File, filtered); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (o *screenOutput) Restore() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	flushErr := writeAll(o.File, o.pending)
	o.pending = nil
	_, restoreErr := io.WriteString(o.File, ansi.ShowCursor+ansi.ResetModeAltScreenSaveCursor)
	return errors.Join(flushErr, restoreErr)
}

func writeAll(output io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := output.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
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
