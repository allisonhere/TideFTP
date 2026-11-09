package update

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// slowBody yields its bytes after a delay, failing instead if the request's
// context ends first — the way a real body read on a slow link behaves under
// http.Client.Timeout.
type slowBody struct {
	ctx   context.Context
	r     io.Reader
	delay time.Duration
	once  bool
}

func (b *slowBody) Read(p []byte) (int, error) {
	if !b.once {
		b.once = true
		select {
		case <-time.After(b.delay):
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
	}
	return b.r.Read(p)
}

func (b *slowBody) Close() error { return nil }

func slowArchiveServer(archive []byte, sums string, delay time.Duration) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case testDownloadURL:
			return &http.Response{StatusCode: http.StatusOK, Body: &slowBody{ctx: req.Context(), r: bytes.NewReader(archive), delay: delay}}, nil
		case testChecksumsURL:
			return responseWithBody(http.StatusOK, sums), nil
		}
		return responseWithBody(http.StatusNotFound, "not found"), nil
	})
}

// The archive download must not be held to the release check's short
// timeout: on a slow link the body takes longer to arrive than the API call.
func TestDownloadOutlastsTheCheckTimeout(t *testing.T) {
	archive := testArchive(t, "tideftp-linux-x86_64", "#!/bin/sh\necho tide\n")
	sums := sha256Hex(archive) + "  tideftp-linux-x86_64.tar.gz\n"
	transport := slowArchiveServer(archive, sums, 200*time.Millisecond)
	updater := &Updater{
		HTTPClient:         &http.Client{Transport: transport, Timeout: 50 * time.Millisecond},
		DownloadHTTPClient: &http.Client{Transport: transport, Timeout: 10 * time.Second},
	}
	asset, err := updater.Download(ReleaseInfo{
		Version: "v1.2.3", AssetName: "tideftp-linux-x86_64",
		DownloadURL: testDownloadURL, ChecksumsURL: testChecksumsURL,
	})
	if err != nil {
		t.Fatalf("Download on a slow link: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(asset.BinaryPath)) })
}

func TestNewUsesALongerTimeoutForDownloads(t *testing.T) {
	u := New()
	if u.DownloadHTTPClient == nil || u.DownloadHTTPClient == u.HTTPClient {
		t.Fatal("New shares the release-check client with downloads")
	}
	if u.DownloadHTTPClient.Timeout < 10*time.Minute {
		t.Fatalf("download timeout = %v, want room for a slow link", u.DownloadHTTPClient.Timeout)
	}
}

// A failed download leaves no temp directory behind.
func TestFailedDownloadRemovesItsTempDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	archive := testArchive(t, "tideftp-linux-x86_64", "real")
	updater := &Updater{HTTPClient: archiveServer(archive, "")}
	if _, err := updater.Download(ReleaseInfo{
		Version: "v1.2.3", AssetName: "tideftp-linux-x86_64",
		DownloadURL: testDownloadURL, ChecksumsURL: testChecksumsURL,
	}); err == nil {
		t.Fatal("Download with no checksum entry succeeded")
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("temp dir left behind after a failed download: %v", left)
	}
}
