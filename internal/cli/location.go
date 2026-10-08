package cli

import "strings"

// location is one side of a sync: a local directory, or a path on a server.
//
//	./www            local
//	prod:/var/www    saved profile "prod"
//	:/var/www        the connection given by --host/--protocol/... flags
//
// A local path that really contains a colon in its first segment needs a
// leading "./" (or an absolute path) so it is not read as a profile. A single
// letter before the colon is a Windows drive, never a profile.
type location struct {
	remote  bool
	profile string // empty for ":" (flag-defined connection)
	path    string
}

func parseLocation(s string) location {
	i := strings.Index(s, ":")
	if i < 0 {
		return location{path: s}
	}
	prefix := s[:i]
	if strings.ContainsAny(prefix, `/\`) || len(prefix) == 1 {
		return location{path: s}
	}
	return location{remote: true, profile: prefix, path: s[i+1:]}
}
