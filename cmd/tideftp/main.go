package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/ui"
)

func main() {
	program := tea.NewProgram(ui.NewModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: %v\n", err)
		os.Exit(1)
	}
}
