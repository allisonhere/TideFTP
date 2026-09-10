package session

import "testing"

// Target stopped being comparable with == when it gained a slice, so
// SameConnection carries that comparison. Bookmarks are deliberately excluded:
// they are what the user saved about a server, not which server it is, and a
// dial result must still match the target it was dialled for when a bookmark
// is added while the connection is being established.
func TestSameConnectionIgnoresBookmarks(t *testing.T) {
	base := Target{Name: "prod", Protocol: "sftp", Host: "web1.example.com", Port: 22, User: "deploy", StartPath: "/srv"}
	withBookmarks := base
	withBookmarks.Bookmarks = []string{"/var/www"}

	if !base.SameConnection(withBookmarks) {
		t.Fatal("a bookmark made a target stop matching itself")
	}
	if !withBookmarks.SameConnection(base) {
		t.Fatal("SameConnection is not symmetric")
	}
}

func TestSameConnectionDistinguishesTargets(t *testing.T) {
	base := Target{Name: "prod", Protocol: "sftp", Host: "web1.example.com", Port: 22, User: "deploy", StartPath: "/srv"}

	for name, mutate := range map[string]func(*Target){
		"host":          func(tg *Target) { tg.Host = "other.example.com" },
		"port":          func(tg *Target) { tg.Port = 2222 },
		"user":          func(tg *Target) { tg.User = "someone" },
		"protocol":      func(tg *Target) { tg.Protocol = "ftp" },
		"name":          func(tg *Target) { tg.Name = "renamed" },
		"startPath":     func(tg *Target) { tg.StartPath = "/elsewhere" },
		"hostKeyPolicy": func(tg *Target) { tg.HostKeyPolicy = HostKeyStrict },
	} {
		other := base
		mutate(&other)
		if base.SameConnection(other) {
			t.Errorf("a differing %s still matched", name)
		}
	}
}
