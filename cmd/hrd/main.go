// Command hrd opens a picker for Herdr destinations.
package main

import (
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	"github.com/joshuadavidthomas/hrd/internal/herdr"
	"github.com/joshuadavidthomas/hrd/internal/ui"
)

func main() {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()

	operations := catalog.NewLocalOperations(herdr.New())
	program := tea.NewProgram(ui.New(root, operations), tea.WithContext(root))
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "hrd: %v\n", err)
		os.Exit(1)
	}
}
