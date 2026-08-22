package ftpsession

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jlaffaye/ftp"

	"tideftp/internal/domain"
)

var errStub = errors.New("stub dial")

func names(entries []domain.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name)
	}
	return out
}

func TestConvertEntriesDropsDotDirectories(t *testing.T) {
	// LIST includes "." and ".." on most servers. The UI navigates with
	// backspace and would render them as ordinary rows.
	listing := []*ftp.Entry{
		{Name: ".", Type: ftp.EntryTypeFolder},
		{Name: "..", Type: ftp.EntryTypeFolder},
		{Name: "readme.txt", Type: ftp.EntryTypeFile, Size: 12},
	}
	got := names(convertEntries(listing, true))
	if len(got) != 1 || got[0] != "readme.txt" {
		t.Fatalf("entries = %v, want just readme.txt", got)
	}
}

func TestConvertEntriesSortsAndFiltersHidden(t *testing.T) {
	listing := []*ftp.Entry{
		{Name: "beta.txt", Type: ftp.EntryTypeFile},
		{Name: "Alpha.txt", Type: ftp.EntryTypeFile},
		{Name: ".hidden", Type: ftp.EntryTypeFile},
		{Name: "sub", Type: ftp.EntryTypeFolder},
		{Name: "link", Type: ftp.EntryTypeLink},
	}

	visible := names(convertEntries(listing, false))
	want := []string{"sub", "Alpha.txt", "beta.txt", "link"}
	if len(visible) != len(want) {
		t.Fatalf("entries = %v, want %v", visible, want)
	}
	for i := range want {
		if visible[i] != want[i] {
			t.Fatalf("entries = %v, want %v (dirs first, then case-insensitive)", visible, want)
		}
	}

	if all := names(convertEntries(listing, true)); len(all) != 5 {
		t.Fatalf("showHidden returned %v, want all five", all)
	}
}

func TestConvertEntriesMapsKinds(t *testing.T) {
	listing := []*ftp.Entry{
		{Name: "d", Type: ftp.EntryTypeFolder},
		{Name: "f", Type: ftp.EntryTypeFile, Size: 42},
		{Name: "l", Type: ftp.EntryTypeLink},
	}
	entries := convertEntries(listing, true)
	if entries[0].Kind != domain.EntryDir {
		t.Fatalf("folder mapped to %v", entries[0].Kind)
	}
	if entries[1].Kind != domain.EntryFile || entries[1].Size != 42 {
		t.Fatalf("file mapped to %v size %d", entries[1].Kind, entries[1].Size)
	}
	if entries[2].Kind != domain.EntrySymlink {
		t.Fatalf("link mapped to %v", entries[2].Kind)
	}
}

func TestConvertEntriesToleratesNilRows(t *testing.T) {
	if got := convertEntries([]*ftp.Entry{nil, {Name: "ok", Type: ftp.EntryTypeFile}}, true); len(got) != 1 {
		t.Fatalf("entries = %v, want the nil row skipped", names(got))
	}
}

func TestChildAndParent(t *testing.T) {
	fs := &FS{}
	if got := fs.Child("/ftp/ftp_test", "uploads"); got != "/ftp/ftp_test/uploads" {
		t.Fatalf("Child = %q", got)
	}
	if got := fs.Parent("/ftp/ftp_test/uploads"); got != "/ftp/ftp_test" {
		t.Fatalf("Parent = %q", got)
	}
	if got := fs.Parent("/"); got != "/" {
		t.Fatalf("Parent(\"/\") = %q, want / so the UI stops walking up", got)
	}
}

func TestPoolCapIsHonouredAndCloseIsRepeatable(t *testing.T) {
	dialed := 0
	connections := newPool(2, func(ctx context.Context) (*ftp.ServerConn, error) {
		dialed++
		return nil, errStub
	})

	// Both slots are taken by failed dials, which must release them again.
	for i := 0; i < 4; i++ {
		if _, err := connections.get(context.Background()); err == nil {
			t.Fatalf("stub dial returned no error")
		}
	}
	if dialed != 4 {
		t.Fatalf("dialled %d times, want 4 — a failed dial must release its slot", dialed)
	}

	if err := connections.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := connections.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := connections.get(context.Background()); err == nil {
		t.Fatalf("get on a closed pool returned no error")
	}
}

func TestPoolGetRespectsContext(t *testing.T) {
	// started fires once the only slot is held, so the second get below is
	// definitely the one waiting rather than the one dialling.
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	connections := newPool(1, func(ctx context.Context) (*ftp.ServerConn, error) {
		close(started)
		<-release
		return nil, errStub
	})
	go connections.get(context.Background())
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := connections.get(ctx); err == nil {
		t.Fatalf("get with a full pool and an expiring context returned no error")
	}
}
