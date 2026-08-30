package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// roleOf finds the role the highlighter gave the span whose text is exactly
// want. Exact rather than substring: "services" appears both in a comment
// and as a key, and a test that cannot tell those apart proves nothing.
// Asserting on roles rather than on rendered ANSI is deliberate too — the
// goldens strip colour, so this is the only place the syntax pass is checked.
func roleOf(t *testing.T, lines [][]previewSpan, want string) highlightRole {
	t.Helper()
	for _, line := range lines {
		for _, span := range line {
			if span.text == want {
				return span.role
			}
		}
	}
	t.Fatalf("no span %q in %v", want, plainOf(lines))
	return roleText
}

func plainOf(lines [][]previewSpan) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = spanText(line)
	}
	return out
}

func TestHighlightGo(t *testing.T) {
	lines := spanLines("main.go", []byte("package main\n\n// greet says hello.\nfunc greet() {\n\tprintln(\"hi\", 42)\n}\n"))

	if got := roleOf(t, lines, "// greet says hello."); got != roleComment {
		t.Errorf("comment role = %v, want roleComment", got)
	}
	if got := roleOf(t, lines, "func"); got != roleKeyword {
		t.Errorf("`func` role = %v, want roleKeyword", got)
	}
	if got := roleOf(t, lines, `"hi"`); got != roleString {
		t.Errorf("string role = %v, want roleString", got)
	}
	if got := roleOf(t, lines, "42"); got != roleNumber {
		t.Errorf("number role = %v, want roleNumber", got)
	}
	if got := roleOf(t, lines, "greet"); got != roleName {
		t.Errorf("function name role = %v, want roleName", got)
	}
}

func TestHighlightYAMLKeys(t *testing.T) {
	lines := spanLines("compose.yaml", []byte("# services\nservices:\n  web:\n    ports:\n      - 8080\n"))

	if got := roleOf(t, lines, "# services"); got != roleComment {
		t.Errorf("comment role = %v, want roleComment", got)
	}
	// The same word is a comment on one line and a key on the next.
	if got := roleOf(t, lines, "services"); got != roleName {
		t.Errorf("key role = %v, want roleName", got)
	}
	if got := roleOf(t, lines, "8080"); got != roleNumber {
		t.Errorf("value role = %v, want roleNumber", got)
	}
}

// TestHighlightServerConfig covers the file type this feature exists for:
// something you opened over FTP because it lives on a server.
func TestHighlightServerConfig(t *testing.T) {
	lines := spanLines("nginx.conf", []byte("server {\n    listen 443 ssl;\n    root \"/var/www\";\n}\n"))

	if got := roleOf(t, lines, "listen"); got != roleKeyword {
		t.Errorf("directive role = %v, want roleKeyword", got)
	}
	if got := roleOf(t, lines, "443"); got != roleNumber {
		t.Errorf("port role = %v, want roleNumber", got)
	}
	if got := roleOf(t, lines, `"/var/www"`); got != roleString {
		t.Errorf("path role = %v, want roleString", got)
	}
}

func TestHighlightFallsBackToContentSniffing(t *testing.T) {
	// No extension at all: only the shebang says what this is.
	lines := spanLines("deploy", []byte("#!/bin/sh\n# restart everything\nset -eu\n"))

	if got := roleOf(t, lines, "# restart everything"); got != roleComment {
		t.Errorf("comment role = %v, want roleComment — the shebang should have picked a lexer", got)
	}
}

func TestHighlightUnknownExtensionIsPlain(t *testing.T) {
	body := []byte("alpha beta\ngamma\n")

	lines := spanLines("data.zzzzz", body)

	for _, line := range lines {
		for _, span := range line {
			if span.role != roleText {
				t.Fatalf("span %q got role %v; an unrecognised file must render plain", span.text, span.role)
			}
		}
	}
	if got := plainOf(lines); len(got) != 2 || got[0] != "alpha beta" || got[1] != "gamma" {
		t.Fatalf("lines = %q, want the file's two lines", got)
	}
}

func TestHighlightUnnamedContentIsNeverLexed(t *testing.T) {
	lines := spanLines("", []byte("func main() {}\n"))

	if len(lines) != 1 || len(lines[0]) != 1 || lines[0][0].role != roleText {
		t.Fatalf("lines = %+v, want one plain span when there is no filename to match", lines)
	}
}

func TestHighlightPreservesTheExactText(t *testing.T) {
	// Whatever the lexer does with it, the characters on screen must still be
	// the file's own — highlighting colours text, it does not rewrite it.
	body := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(1 + 2) }\n"

	got := strings.Join(plainOf(spanLines("main.go", []byte(body))), "\n")

	if want := strings.TrimSuffix(body, "\n"); got != want {
		t.Fatalf("highlighted text = %q, want the original %q", got, want)
	}
}

func TestHighlightSanitisesInsideSpans(t *testing.T) {
	// A string literal carrying an escape sequence must not survive the
	// syntax pass with its control characters intact.
	lines := spanLines("main.go", []byte("var s = \"\x1b[31mred\x07\"\n"))

	joined := strings.Join(plainOf(lines), "\n")
	if strings.ContainsRune(joined, '\x1b') || strings.ContainsRune(joined, '\a') {
		t.Fatalf("line = %q, still carries control characters a terminal would act on", joined)
	}
}

func TestHighlightTruncatedContentStillColoursWhatItHas(t *testing.T) {
	// The head of a file, cut mid-string — exactly what a 128 KB peek at a
	// large file hands the lexer.
	lines := spanLines("main.go", []byte("// header\nfunc main() {\n\tprintln(\"unterminate"))

	if got := roleOf(t, lines, "// header"); got != roleComment {
		t.Errorf("comment role = %v, want roleComment despite the cut below it", got)
	}
	if got := roleOf(t, lines, "func"); got != roleKeyword {
		t.Errorf("`func` role = %v, want roleKeyword despite the cut below it", got)
	}
}

func TestRoleColorKeepsTextLegible(t *testing.T) {
	fg, dimmed, errColor := lipgloss.Color("#ffffff"), lipgloss.Color("#8a8a8a"), lipgloss.Color("#ff5555")

	// A near-black background: the palette's own hues should survive.
	dark := lipgloss.Color("#101216")
	if got := roleColor(fg, dimmed, errColor, roleString, dark); got != syntaxPalette[roleString] {
		t.Errorf("string colour on a dark background = %v, want the palette's %v", got, syntaxPalette[roleString])
	}

	// A white background: a light green string would be unreadable, so the
	// colour is what gets dropped, never the text.
	light := lipgloss.Color("#ffffff")
	got := roleColor(fg, dimmed, errColor, roleString, light)
	if got == syntaxPalette[roleString] {
		t.Errorf("string colour on white stayed %v, which fails the contrast floor", got)
	}
	if contrastRatio(got, light) < textMinContrast {
		t.Errorf("replacement colour %v still fails contrast against white", got)
	}
}

func TestRoleColorLeavesPlainTextAlone(t *testing.T) {
	fg := lipgloss.Color("#c8ccd4")

	if got := roleColor(fg, "#8a8a8a", "#ff5555", roleText, "#101216"); got != fg {
		t.Fatalf("plain text colour = %v, want the panel's own foreground %v", got, fg)
	}
}
