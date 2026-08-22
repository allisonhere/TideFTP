package fakefs

import (
	"context"
	"testing"
	"time"
)

func TestListSortsDirsFirstAndHidesDotfiles(t *testing.T) {
	entries, err := NewRemote().List(context.Background(), "/", false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seenFile := false
	for _, entry := range entries {
		if entry.Hidden {
			t.Fatalf("hidden entry %q returned with showHidden off", entry.Name)
		}
		if entry.IsDir() && seenFile {
			t.Fatalf("directory %q sorted after a file", entry.Name)
		}
		if !entry.IsDir() {
			seenFile = true
		}
	}
}

func TestListReportsAnUnknownDirectory(t *testing.T) {
	if _, err := NewRemote().List(context.Background(), "/no/such/dir", false); err == nil {
		t.Fatalf("listing an unknown directory returned no error; a real server would fail")
	}
}

func TestListHonoursContextCancellationDuringLatency(t *testing.T) {
	remote := NewRemoteWithLatency(10 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := remote.List(ctx, "/", false); err == nil {
		t.Fatalf("a cancelled listing returned no error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v; latency should be interruptible", elapsed)
	}
}

func TestHiddenEntriesAppearWhenAsked(t *testing.T) {
	entries, err := NewRemote().List(context.Background(), "/", true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, entry := range entries {
		if entry.Name == ".server-note" {
			return
		}
	}
	t.Fatalf("showHidden did not include .server-note")
}
