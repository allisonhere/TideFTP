package cli

import (
	"strings"
	"testing"
	"time"
)

func TestFormatProgress(t *testing.T) {
	got := formatProgress("big.7z", 3<<30, 9<<30, 50<<20)
	for _, want := range []string{"big.7z", " 33%", "3.0 GiB / 9.0 GiB", "50.0 MiB/s", "eta "} {
		if !strings.Contains(got, want) {
			t.Errorf("progress line %q missing %q", got, want)
		}
	}
	// No known size: no percentage or ETA, just bytes.
	if got := formatProgress("x", 2048, 0, 0); strings.Contains(got, "%") || strings.Contains(got, "eta") {
		t.Errorf("sizeless progress should be bytes only: %q", got)
	}
	// A long name is shortened from the left, keeping the extension.
	long := formatProgress(strings.Repeat("a", 60)+".7z", 1, 2, 0)
	if !strings.HasPrefix(long, "…") || !strings.Contains(long, ".7z") {
		t.Errorf("long name not shortened sensibly: %q", long)
	}
	// Never over 100% even if the source grew.
	if got := formatProgress("x", 12, 10, 0); !strings.Contains(got, "100%") {
		t.Errorf("progress should cap at 100%%: %q", got)
	}
}

func TestShortDuration(t *testing.T) {
	cases := map[time.Duration]string{
		9 * time.Second: "9s", 130 * time.Second: "2m10s", 3*time.Hour + 5*time.Minute: "3h05m",
	}
	for d, want := range cases {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestProgressIsOffByDefault(t *testing.T) {
	app, _, errOut := testApp(t, fakefsRemote())
	cb, finish := app.progressLine(&connFlags{}, "f", 10, 0)
	if cb != nil {
		t.Fatalf("progress must be opt-in")
	}
	finish()
	if cb, _ := app.progressLine(&connFlags{progress: true, quiet: true}, "f", 10, 0); cb != nil {
		t.Fatalf("-q must win over --progress")
	}
	if errOut.Len() != 0 {
		t.Fatalf("wrote %q without --progress", errOut.String())
	}
}

// Off a terminal, --progress appends plain lines (no carriage returns) and does
// not redraw, so logs stay readable.
func TestProgressOffATerminalWritesPlainLines(t *testing.T) {
	app, _, errOut := testApp(t, fakefsRemote())
	cb, finish := app.progressLine(&connFlags{progress: true}, "f.bin", 100, 0)
	if cb == nil {
		t.Fatalf("--progress should enable the callback")
	}
	cb(50)
	finish()
	got := errOut.String()
	if !strings.Contains(got, "f.bin") || !strings.HasSuffix(got, "\n") || strings.Contains(got, "\r") {
		t.Fatalf("expected one plain progress line, got %q", got)
	}
}
