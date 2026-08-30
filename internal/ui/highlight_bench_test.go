package ui

import (
	"strings"
	"testing"
)

// BenchmarkSpanLines measures the syntax pass at the two sizes that matter:
// the config file a preview is usually pointed at, and the 128 KB ceiling
// previewMaxBytes allows. It is what the cost note in highlight.go's doc
// comment is based on, and what would catch a lexer change making the pass
// expensive enough to be worth moving or capping.
func BenchmarkSpanLines(b *testing.B) {
	unit := "server {\n    listen 443 ssl;\n    root \"/var/www/app\";\n    # tuned\n}\n"
	for _, size := range []struct {
		name  string
		bytes int
	}{
		{"4KB", 4 << 10},
		{"128KB", previewMaxBytes},
	} {
		body := []byte(strings.Repeat(unit, size.bytes/len(unit)))
		b.Run(size.name, func(b *testing.B) {
			for b.Loop() {
				spanLines("nginx.conf", body)
			}
		})
	}
}

// BenchmarkSpanLinesUnrecognised is the path that has no lexer to match:
// chroma's content sniffing runs every analyser it has, so this is the case
// that most easily gets slow. tokenise bounds it to the file's head.
func BenchmarkSpanLinesUnrecognised(b *testing.B) {
	body := []byte(strings.Repeat("some plain text with no syntax at all\n", previewMaxBytes/38))
	b.ResetTimer()
	for b.Loop() {
		spanLines("notes.zzzzz", body)
	}
}
