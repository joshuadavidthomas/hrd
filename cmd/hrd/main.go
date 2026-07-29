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

func main() {
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
