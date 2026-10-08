package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// isTerminal reports whether w is an interactive terminal. Progress redraws a
// single line with carriage returns, which would turn a log file or a pipe
// into garbage, so it is shown only when someone is watching.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// progressLine returns a callback for transfer.Copy and a finish function, or
// (nil, no-op) unless --progress was given (and -q is not). On a terminal it
// redraws one line in place; elsewhere (a log, a pipe) it appends a plain line
// every few seconds instead, so the output stays readable. done is the
// absolute byte position, so a resumed transfer starts mid-bar.
func (a App) progressLine(c *connFlags, label string, size, offset int64) (func(done int64), func()) {
	if !c.progress || c.quiet {
		return nil, func() {}
	}
	tty := isTerminal(a.Stderr)
	interval := 250 * time.Millisecond
	if !tty {
		interval = 5 * time.Second
	}
	started := time.Now()
	var last time.Time
	drawn := false
	draw := func(done int64) {
		now := time.Now()
		if drawn && now.Sub(last) < interval {
			return
		}
		last = now
		drawn = true
		moved := done - offset
		var rate float64
		if secs := now.Sub(started).Seconds(); secs > 0.5 && moved > 0 {
			rate = float64(moved) / secs
		}
		line := formatProgress(label, done, size, rate)
		outMu.Lock()
		if tty {
			_, _ = fmt.Fprintf(a.Stderr, "\r\033[K%s", line)
		} else {
			_, _ = fmt.Fprintln(a.Stderr, line)
		}
		outMu.Unlock()
	}
	finish := func() {
		if !drawn || !tty {
			return
		}
		outMu.Lock()
		_, _ = fmt.Fprint(a.Stderr, "\r\033[K")
		outMu.Unlock()
	}
	return draw, finish
}

// formatProgress renders "name  42%  3.8 GiB / 9.0 GiB  41.2 MiB/s  eta 2m10s".
func formatProgress(label string, done, size int64, rate float64) string {
	if len(label) > 28 {
		label = "…" + label[len(label)-27:]
	}
	var b strings.Builder
	b.WriteString(label)
	if size > 0 {
		pct := float64(done) / float64(size) * 100
		fmt.Fprintf(&b, "  %3.0f%%  %s / %s", min(pct, 100), humanBytes(done), humanBytes(size))
	} else {
		fmt.Fprintf(&b, "  %s", humanBytes(done))
	}
	if rate > 0 {
		fmt.Fprintf(&b, "  %s/s", humanBytes(int64(rate)))
		if size > done {
			eta := time.Duration(float64(size-done)/rate) * time.Second
			fmt.Fprintf(&b, "  eta %s", shortDuration(eta))
		}
	}
	return b.String()
}

func shortDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
