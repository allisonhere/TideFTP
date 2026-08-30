package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/lipgloss"
)

// Syntax highlighting for the preview overlay.
//
// chroma supplies the lexers — some two hundred languages, which is not a
// thing worth hand-rolling — but none of its colour schemes. A bundled theme
// would paint its own idea of "background" into a panel that already has one,
// and would look identical whichever TideFTP theme is active. So only the
// tokeniser is used, and the colours below are this package's own.

// highlightRole is the handful of things preview actually colours. chroma
// distinguishes far more token types than a 128 KB peek needs; collapsing
// them here is what keeps a previewed file looking calm rather than like a
// ransom note.
type highlightRole int

const (
	roleText highlightRole = iota
	roleComment
	roleKeyword
	roleString
	roleNumber
	roleName
	roleOperator
	roleError
)

// analyseMaxBytes is how much of a file is offered to chroma's content
// sniffing when the filename matched no lexer. See tokenise.
const analyseMaxBytes = 4 << 10

// head returns at most n bytes from the front of value, splitting on a rune
// boundary so a sniffer is never handed half a character.
func head(value string, n int) string {
	if len(value) <= n {
		return value
	}
	for n > 0 && !utf8.RuneStart(value[n]) {
		n--
	}
	return value[:n]
}

// previewSpan is a run of characters in one preview line sharing a role.
type previewSpan struct {
	text string
	role highlightRole
}

// syntaxPalette is a fixed set of hues, in the spirit of the Stats tab's
// fixed palette: a language's keywords should look like keywords whichever
// theme is loaded, and the small tideui palette has no six distinct hues to
// spare anyway. Only foregrounds are fixed — the background always stays the
// panel's — and every one of these is run through readableOn before it is
// painted, so a theme these clash with loses the colour rather than the text.
var syntaxPalette = map[highlightRole]lipgloss.Color{
	roleKeyword:  lipgloss.Color("#c678dd"),
	roleString:   lipgloss.Color("#98c379"),
	roleNumber:   lipgloss.Color("#d19a66"),
	roleName:     lipgloss.Color("#61afef"),
	roleOperator: lipgloss.Color("#56b6c2"),
}

// roleColor resolves a role to a foreground that is legible on bg. Comments
// deliberately take the theme's own dimmed colour and the lower contrast
// floor that goes with it: a comment that competes with the code for
// attention is a comment rendered wrong.
func roleColor(theme lipgloss.Color, dimmed, errColor lipgloss.Color, role highlightRole, bg lipgloss.Color) lipgloss.Color {
	switch role {
	case roleText:
		return theme
	case roleComment:
		return readableOn(dimmed, bg, dimMinContrast)
	case roleError:
		return readableOn(errColor, bg, textMinContrast)
	}
	if color, ok := syntaxPalette[role]; ok {
		return readableOn(color, bg, textMinContrast)
	}
	return theme
}

// spanLines is highlight without the language, for callers that only draw.
func spanLines(name string, data []byte) [][]previewSpan {
	lines, _ := highlight(name, data)
	return lines
}

// highlight turns file content into display lines of coloured spans, and
// names the language it recognised ("" when it recognised none). When a lexer
// matches name it drives the colouring; with no match — or no name, as for
// binary content shown in the text pane — every line becomes a single
// roleText span, which renders exactly as plain text always did.
//
// Cost, measured by BenchmarkSpanLines: about 8ms for a 4 KB config, ~110ms
// for a full 128 KB peek at one, up to half a second for a denser language at
// that size, and ~4ms for a file no lexer recognises. That is why it happens
// once, on the command goroutine that read the file, and never in Update.
//
// Sanitising happens here rather than before tokenising, so the lexer sees
// the file as it really is: expanding tabs first would corrupt the one
// language where indentation is syntax.
func highlight(name string, data []byte) ([][]previewSpan, string) {
	body := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := [][]previewSpan{{}}

	emit := func(text string, role highlightRole) {
		for i, piece := range strings.Split(text, "\n") {
			if i > 0 {
				lines = append(lines, []previewSpan{})
			}
			if piece == "" {
				continue
			}
			current := len(lines) - 1
			lines[current] = append(lines[current], previewSpan{text: sanitizePreviewText(piece), role: role})
		}
	}

	tokens, language := tokenise(name, body)
	if language != "" {
		for _, token := range tokens {
			emit(token.Value, roleFor(token.Type))
		}
	} else {
		emit(body, roleText)
	}

	// A file ending in a newline leaves a trailing empty element that is not
	// a line of the file, only the terminator of the last one.
	if len(lines) > 1 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines, language
}

// tokenise runs body through the lexer chroma picks for name, returning the
// language's name — empty when there is nothing better than plain text to
// do. The preview only ever holds the head of a file, so the tail is
// routinely mid-string or mid-block; chroma emits Error tokens for that
// rather than failing, which is why a truncated preview still highlights
// everything above the cut.
func tokenise(name, body string) ([]chroma.Token, string) {
	if strings.TrimSpace(name) == "" {
		return nil, ""
	}
	lexer := lexers.Match(name)
	if lexer == nil {
		// No filename match: fall back to sniffing the content, which is what
		// catches a shebang script with no extension. Only the head is
		// offered — Analyse runs every lexer's analyser over whatever it is
		// given, so handing it a full 128 KB peek costs far more than the
		// answer is worth, and everything that identifies a file this way (a
		// shebang, an XML declaration, an opening tag) is in the first few
		// lines anyway.
		lexer = lexers.Analyse(head(body, analyseMaxBytes))
	}
	if lexer == nil {
		return nil, ""
	}
	// Coalesce merges adjacent tokens of the same type, so a line of plain
	// code is one span rather than one per identifier.
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, body)
	if err != nil {
		return nil, ""
	}
	config := lexer.Config()
	if config == nil || config.Name == "" || config.Name == "plaintext" {
		return nil, ""
	}
	return iterator.Tokens(), config.Name
}

// roleFor collapses a chroma token type into one of the roles preview paints.
func roleFor(token chroma.TokenType) highlightRole {
	switch {
	case token.InCategory(chroma.Comment):
		return roleComment
	case token.InCategory(chroma.Keyword):
		return roleKeyword
	case token.InSubCategory(chroma.LiteralString):
		return roleString
	case token.InSubCategory(chroma.LiteralNumber):
		return roleNumber
	case token == chroma.Error:
		return roleError
	case token.InCategory(chroma.Operator):
		return roleOperator
	}
	switch token {
	// Names worth colouring are the ones that label something: a function, a
	// type, a tag, a config key. Colouring every variable reference too would
	// leave most of a file tinted and none of it emphasised.
	case chroma.NameFunction, chroma.NameClass, chroma.NameTag, chroma.NameBuiltin,
		chroma.NameDecorator, chroma.NameAttribute, chroma.NameNamespace,
		chroma.NameException, chroma.NameConstant, chroma.GenericHeading, chroma.GenericSubheading:
		return roleName
	}
	return roleText
}

// sanitizePreviewText makes one run of file content safe to draw: tabs expand
// to a fixed indent, and every other control character becomes a middle dot
// rather than being handed to the terminal to act on. A preview must never
// let a file's own escape sequences repaint the screen.
func sanitizePreviewText(value string) string {
	if !strings.ContainsFunc(value, needsSanitizing) {
		return value
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r == '\t':
			b.WriteString(strings.Repeat(" ", previewTabWidth))
		case needsSanitizing(r):
			b.WriteRune('·')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func needsSanitizing(r rune) bool {
	return r == '\t' || r == '�' || (r < 0x20 && r != '\n') || r == 0x7f
}

// spanText is the plain text of a line of spans — what the scroll maths and
// the width clamp measure, and what a golden file records.
func spanText(spans []previewSpan) string {
	if len(spans) == 1 {
		return spans[0].text
	}
	var b strings.Builder
	for _, span := range spans {
		b.WriteString(span.text)
	}
	return b.String()
}
