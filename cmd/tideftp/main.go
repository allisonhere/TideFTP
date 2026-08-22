package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/fakefs"
	"tideftp/internal/ui"
)

// version is set via -ldflags "-X main.version=$(VERSION)" at build time.
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-v" || os.Args[1] == "--version") {
		fmt.Println("tideftp " + version)
		return
	}
	program := tea.NewProgram(ui.NewModel(fakefs.NewRemote()), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: %v\n", err)
		os.Exit(1)
	}
}
