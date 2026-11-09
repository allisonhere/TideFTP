// Package tideftp holds files embedded from the repository root, which the
// binaries under cmd/ cannot reach with go:embed themselves.
package tideftp

import _ "embed"

// Changelog is CHANGELOG.md, embedded so Settings → Changelog always matches
// the running build and works offline.
//
//go:embed CHANGELOG.md
var Changelog string
