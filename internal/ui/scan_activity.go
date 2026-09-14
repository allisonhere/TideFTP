package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// scanActivity is deliberately separate from a transfer or delete job. A
// recursive operation has to inspect the tree before it can offer a useful
// confirmation or queue rows, and that wait used to be represented only by a
// static status-bar message.
type scanActivity struct {
	title     string
	phase     string
	startedAt time.Time
	frame     int
	files     int
	folders   int
	bytes     int64
	current   string
}

type scanTickMsg struct{}

// scanProgressMsg is deliberately a snapshot, not an event that must be
// rendered individually. The producer may outpace the terminal while walking
// a local tree, so the pump keeps only the newest state.
type scanProgressMsg struct {
	token   int
	phase   string
	files   int
	folders int
	bytes   int64
	current string
}

type scanStreamClosed struct{}

const scanTickInterval = 120 * time.Millisecond

func scanTickCmd() tea.Cmd {
	return tea.Tick(scanTickInterval, func(time.Time) tea.Msg { return scanTickMsg{} })
}

func (m *Model) startScan(title, phase string) (context.Context, int, tea.Cmd) {
	m.scanToken++
	ctx, cancel := context.WithCancel(context.Background())
	m.scanCancel = cancel
	m.scanActivity = &scanActivity{title: title, phase: phase, startedAt: time.Now()}
	m.overlay = overlayScanning
	return ctx, m.scanToken, scanTickCmd()
}

func (m *Model) advanceScan() tea.Cmd {
	if m.scanActivity == nil {
		return nil
	}
	m.scanActivity.frame++
	return scanTickCmd()
}

func (m *Model) finishScan() {
	m.scanActivity = nil
	m.scanCancel = nil
}

func (m *Model) cancelScan() {
	if m.scanCancel != nil {
		m.scanCancel()
	}
	// Retire every message the worker may still deliver after cancellation.
	m.scanToken++
	m.scanEvents = nil
	m.finishScan()
	m.overlay = overlayNone
	m.setStatus("cancelled")
}

func (m Model) acceptsScan(token int) bool {
	return token == 0 || (m.scanActivity != nil && token == m.scanToken)
}

func (m *Model) applyScanProgress(progress scanProgressMsg) {
	if m.scanActivity == nil {
		return
	}
	m.scanActivity.phase = progress.phase
	m.scanActivity.files = progress.files
	m.scanActivity.folders = progress.folders
	m.scanActivity.bytes = progress.bytes
	m.scanActivity.current = progress.current
}

func waitForScanEvent(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-events
		if !ok {
			return scanStreamClosed{}
		}
		return msg
	}
}

func scanElapsed(start time.Time) string {
	seconds := int(time.Since(start).Round(time.Second).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("%ds", seconds)
}
