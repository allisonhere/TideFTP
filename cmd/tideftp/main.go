package main

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/fakefs"
	"tideftp/internal/faketransfer"
	"tideftp/internal/localfs"
	"tideftp/internal/ui"
)

// version is set via -ldflags "-X main.version=$(VERSION)" at build time.
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-v" || os.Args[1] == "--version") {
		fmt.Println("tideftp " + version)
		return
	}
	engine := faketransfer.New()
	defer engine.Close()

	// A little fake latency so the panes' loading state is visible when
	// running the app by hand; real adapters will supply their own.
	remote := fakefs.NewRemoteWithLatency(150 * time.Millisecond)

	program := tea.NewProgram(ui.NewModel(localfs.New(), remote, engine), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: %v\n", err)
		os.Exit(1)
	}
}
