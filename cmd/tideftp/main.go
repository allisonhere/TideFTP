package main

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tideftp/internal/fakesession"
	"tideftp/internal/localfs"
	"tideftp/internal/session"
	"tideftp/internal/ui"
)

// version is set via -ldflags "-X main.version=$(VERSION)" at build time.
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-v" || os.Args[1] == "--version") {
		fmt.Println("tideftp " + version)
		return
	}
	// Demo profiles until saved profiles exist. unreachable.invalid is
	// deliberately absent from the dialer's known hosts, so picking it
	// exercises the connect-failure path by hand.
	targets := []session.Target{
		{Name: "demo sftp", Protocol: "sftp", Host: "demo-sftp.local", User: "allie", StartPath: "/public_html"},
		{Name: "demo ftps", Protocol: "ftps", Host: "demo-ftps.local", User: "allie"},
		{Name: "unreachable", Protocol: "sftp", Host: "unreachable.invalid", User: "allie"},
	}
	// Fake latency so the connecting and loading states are visible when
	// running by hand; real adapters will supply their own.
	dialer := fakesession.New(600*time.Millisecond, 150*time.Millisecond, "demo-sftp.local", "demo-ftps.local")

	program := tea.NewProgram(ui.NewModel(localfs.New(), dialer, targets), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tideftp: %v\n", err)
		os.Exit(1)
	}
}
