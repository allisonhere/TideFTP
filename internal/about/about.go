// Package about is TideFTP's About screen: an animated terminal-window logo
// with rolling waves, a shimmering wordmark, a typed-out tagline, the version
// and build, a short personal message, and links.
//
// It is self-contained — it imports only Bubble Tea, lipgloss and x/ansi, never
// internal/ui — so it can be previewed on its own (`go run ./cmd/about-preview`)
// and approved before it touches the app.
//
// # Plugging it in
//
// internal/ui hosts it as the About overlay (see internal/ui/about.go). The
// shape, for any other host:
//
//  1. Keep an `about.Model` and an overlay flag.
//  2. Open it with m.about = about.New(opts); return m.about.Init().
//  3. While open, forward keys and about.TickMsg to Update, and treat
//     about.CloseMsg as "hide it".
//  4. Draw m.about.Render(width, height) — it returns exactly `height` rows of
//     exactly `width` cells, on a solid background — inside a modal or full screen.
//
// Pass the user's theme with Options.Custom (see [PaletteFromColors]).
package about

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Link is a labelled URL shown at the bottom of the screen.
type Link struct{ Label, URL string }

// Options is everything the screen shows. All of it is optional.
type Options struct {
	Version   string // e.g. "v0.4.0" or "dev"
	Commit    string // short hash
	BuildDate string // e.g. "2026-10-08"
	Message   string // a short note from the author
	Author    string // signs the message
	Heart     bool   // a red heart after the signature
	Links     []Link
	// Custom, when set, is offered first, ahead of the built-in palettes — this
	// is how a host passes its own theme colours. The user can cycle with `t`.
	Custom *Palette
	// Palette picks the starting palette (0 is Custom when set, else the first
	// built-in).
	Palette int
	// ReduceMotion shows the finished screen with no animation and no ticking.
	ReduceMotion bool
}

// DefaultMessage is the note from the author shown unless the host overrides it.
const DefaultMessage = "Thank you for using TideFTP. I built it because moving files should feel calm: steady, resumable, and never in your way. If it ever saves you a long night or a lost transfer, then it did its job. Be kind to your servers, and may your connections always hold."

// DefaultAuthor signs [DefaultMessage].
const DefaultAuthor = "Allie"

// CloseMsg is sent when the user dismisses the screen. The host should hide it.
type CloseMsg struct{}

// frameInterval is 10 fps: smooth enough for waves, cheap over SSH. The wave
// speeds are tuned for it.
const frameInterval = 100 * time.Millisecond

// settleFrame is when the animation stops on its own (~40s): a screen left open
// should not keep streaming colour codes over an SSH link. Any key resumes it.
const settleFrame = 400

// finalFrame is "the intro is over": every reveal is complete.
const finalFrame = 140

// TickMsg is the animation clock. A host that embeds the screen must forward
// it to [Model.Update] while the screen is open (and may drop it otherwise).
type TickMsg struct{}

// Model is the About screen's state.
type Model struct {
	opt      Options
	palettes []Palette
	frame    int
	width    int
	height   int
	palette  int
	settled  bool // the clock has stopped; a key press restarts it
}

// New returns a model that starts its intro from frame 0 (or fully drawn when
// ReduceMotion is set).
func New(o Options) Model {
	m := Model{opt: o, palettes: append([]Palette(nil), Palettes...)}
	if o.Custom != nil {
		m.palettes = append([]Palette{*o.Custom}, m.palettes...)
	}
	n := len(m.palettes)
	m.palette = ((o.Palette % n) + n) % n
	if o.ReduceMotion {
		m.frame = finalFrame
	}
	return m
}

// WithFrame returns a copy parked on frame n. It exists for tests and for the
// preview tool's --frame flag.
func (m Model) WithFrame(n int) Model { m.frame = n; return m }

// Init starts the animation clock.
func (m Model) Init() tea.Cmd {
	if m.opt.ReduceMotion {
		return nil
	}
	return tick()
}

func tick() tea.Cmd {
	return tea.Tick(frameInterval, func(time.Time) tea.Msg { return TickMsg{} })
}

// Update handles the clock, resizes and keys.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case TickMsg:
		if m.opt.ReduceMotion {
			return m, nil
		}
		m.frame++
		if m.frame >= settleFrame {
			m.settled = true // hold this frame; no more ticks
			return m, nil
		}
		return m, tick()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		var wake tea.Cmd
		if m.settled && !m.opt.ReduceMotion {
			// Any key brings the waves back, for another stretch.
			m.settled, m.frame = false, finalFrame
			wake = tick()
		}
		switch msg.String() {
		case "q", "esc", "enter", "ctrl+c":
			return m, func() tea.Msg { return CloseMsg{} }
		case " ", "r":
			// Replay the intro.
			if !m.opt.ReduceMotion {
				m.frame = 0
			}
		case "t", "tab":
			m.palette = (m.palette + 1) % len(m.palettes)
		}
		return m, wake
	}
	return m, nil
}

// View renders at the size last reported by tea.WindowSizeMsg.
func (m Model) View() string { return m.Render(m.width, m.height) }

// Render draws the screen into a width×height box. Every line is at most
// width cells wide, and there are at most height lines.
func (m Model) Render(width, height int) string {
	if width < 1 || height < 1 {
		return ""
	}
	pal := m.palettes[m.palette]
	f := m.frame
	r := renderer{pal: pal, frame: f, width: width}

	hint := "space replay  ·  t colours  ·  q close"
	for _, shorter := range []string{"space replay · t colours · q close", "t colours · q close", "q close"} {
		if utf8.RuneCountInString(hint) <= width {
			break
		}
		hint = shorter
	}
	hints := r.center(r.fg(pal.Dim, hint))
	footer := r.waves(3)
	reserved := len(footer) + 1 // waves + the hint line
	budget := height - reserved
	if budget < 0 {
		budget = 0
	}

	// Sections in drop order: the last ones go first when the terminal is short.
	type section struct {
		lines []string
		prio  int // higher survives longer
	}
	logo := r.logo()
	sections := []section{
		{logo, 3},
		{[]string{""}, 1},
		{r.wordmark(), 6},
		{[]string{r.center(r.tagline())}, 5},
		{[]string{""}, 1},
		{r.versionLines(m.opt), 7},
		{[]string{""}, 1},
		{r.message(m.opt), 4},
		{[]string{""}, 1},
		{r.links(m.opt.Links), 2},
	}
	total := func() int {
		n := 0
		for _, s := range sections {
			n += len(s.lines)
		}
		return n
	}
	// A short modal gets the small window mark before it loses the logo.
	if total() > budget && len(logo) > 0 {
		if small := r.miniLogo(); len(small) > 0 {
			sections[0].lines = small
		}
	}
	for total() > budget {
		lowest := -1
		for i, s := range sections {
			if len(s.lines) == 0 {
				continue
			}
			if lowest < 0 || s.prio < sections[lowest].prio {
				lowest = i
			}
		}
		if lowest < 0 {
			break
		}
		sections[lowest].lines = nil
	}

	var body []string
	for _, s := range sections {
		body = append(body, s.lines...)
	}
	// Float the content in the space above the waves.
	gap := budget - len(body)
	if gap < 0 {
		gap = 0
	}
	top := gap / 2
	var out []string
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	out = append(out, body...)
	for len(out) < budget {
		out = append(out, "")
	}
	out = append(out, footer...)
	out = append(out, hints)

	if len(out) > height {
		out = out[len(out)-height:]
	}
	seq := bgSequence(pal.Bg)
	for i, line := range out {
		out[i] = paintBackground(ansi.Truncate(line, width, ""), width, seq)
	}
	return strings.Join(out, "\n")
}

// ---------------------------------------------------------------------------

type renderer struct {
	pal   Palette
	frame int
	width int
}

func (r renderer) fg(c lipgloss.Color, s string) string {
	return lipgloss.NewStyle().Foreground(c).Render(s)
}

func (r renderer) bold(c lipgloss.Color, s string) string {
	return lipgloss.NewStyle().Foreground(c).Bold(true).Render(s)
}

func (r renderer) center(s string) string {
	w := lipgloss.Width(s)
	if w >= r.width {
		return s
	}
	return strings.Repeat(" ", (r.width-w)/2) + s
}

// ease is a smooth 0→1 over frames [from, to].
func (r renderer) ease(from, to int) float64 {
	if r.frame <= from {
		return 0
	}
	if r.frame >= to {
		return 1
	}
	t := float64(r.frame-from) / float64(to-from)
	return t * t * (3 - 2*t)
}

// logo is the terminal window from the TIDE mark: three traffic-light dots, a
// blinking prompt, and a swell of waves that rises as the screen opens.
func (r renderer) logo() []string {
	const inner = 24
	if r.width < inner+6 {
		return nil
	}
	border := r.pal.Border
	line := func(content string) string {
		return r.center(r.fg(border, "│") + content + r.fg(border, "│"))
	}
	pad := func(plain string, styled string) string {
		n := inner - utf8.RuneCountInString(plain)
		if n < 0 {
			n = 0
		}
		return styled + strings.Repeat(" ", n)
	}
	var out []string
	out = append(out, r.center(r.fg(border, "╭"+strings.Repeat("─", inner)+"╮")))

	dots := " " + r.fg("#ff5f56", "●") + " " + r.fg("#ffbd2e", "●") + " " + r.fg("#27c93f", "●")
	out = append(out, line(pad(" ● ● ●", dots)))
	out = append(out, line(strings.Repeat(" ", inner)))

	cursor := "_"
	if (r.frame/6)%2 == 1 && r.frame < finalFrame {
		cursor = " "
	}
	prompt := " " + r.bold(r.pal.Fg, ">") + " " + r.bold(r.pal.Fg, cursor)
	out = append(out, line(pad(" > "+cursor, prompt)))
	out = append(out, line(strings.Repeat(" ", inner)))

	// Waves inside the window: 3 rows, rising during the intro.
	level := 0.25 + 0.75*r.ease(0, 36)
	for row := 2; row >= 0; row-- {
		out = append(out, r.center(r.fg(border, "│")+r.waveRow(inner, row, 3, level, 0.6)+r.fg(border, "│")))
	}
	out = append(out, r.center(r.fg(border, "╰"+strings.Repeat("─", inner)+"╯")))
	return out
}

// waveRow draws one row (0 = bottom) of a layered wave field `rows` tall.
//
// Neighbouring cells of the same colour are emitted as one styled run: a colour
// escape per character would make a full-width row cost ~20 bytes a cell, and
// this screen redraws its waves a dozen times a second, often over SSH.
func (r renderer) waveRow(cols, row, rows int, level, speed float64) string {
	var b strings.Builder
	t := float64(r.frame) * speed * 1.25
	var run strings.Builder
	var runColor lipgloss.Color
	flush := func() {
		if run.Len() == 0 {
			return
		}
		if runColor == "" {
			b.WriteString(run.String()) // unstyled spaces
		} else {
			b.WriteString(r.fg(runColor, run.String()))
		}
		run.Reset()
	}
	for x := 0; x < cols; x++ {
		ch, col := " ", lipgloss.Color("")
		// Three layers, back to front; the front-most one that fills this cell wins.
		for layer := 2; layer >= 0; layer-- {
			h := r.waveHeight(x, t, layer, rows, level)
			if fill := h - float64(row); fill > 0 {
				ch, col = blockFor(fill), r.pal.Waves[layer]
				break
			}
		}
		if col != runColor {
			flush()
			runColor = col
		}
		run.WriteString(ch)
	}
	flush()
	return b.String()
}

// waveHeight is the height, in rows, of wave layer `layer` at column x.
func (r renderer) waveHeight(x int, t float64, layer, rows int, level float64) float64 {
	fx := float64(x)
	switch layer {
	case 0: // back: slow, broad
		return level * (float64(rows)*0.75 + 0.50*math.Sin(fx*0.20+t*0.10))
	case 1: // middle
		return level * (float64(rows)*0.58 + 0.45*math.Sin(fx*0.29-t*0.16+1.7))
	default: // front: fast, tight; its troughs still fill the bottom row
		return level * (float64(rows)*0.42 + 0.40*math.Sin(fx*0.45+t*0.23+3.1))
	}
}

var blocks = []string{" ", "▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

func blockFor(fill float64) string {
	if fill >= 1 {
		return "█"
	}
	i := int(fill * 8)
	if i < 1 {
		i = 1
	}
	return blocks[i]
}

// waves is the full-width footer.
func (r renderer) waves(rows int) []string {
	level := 0.35 + 0.65*r.ease(0, 40)
	out := make([]string, 0, rows)
	for row := rows - 1; row >= 0; row-- {
		out = append(out, r.waveRow(r.width, row, rows, level, 1))
	}
	return out
}

// ---------------------------------------------------------------------------

var glyphs = map[rune][5]string{
	'T': {"█████", "  █  ", "  █  ", "  █  ", "  █  "},
	'I': {"███", " █ ", " █ ", " █ ", "███"},
	'D': {"████ ", "█   █", "█   █", "█   █", "████ "},
	'E': {"█████", "█    ", "████ ", "█    ", "█████"},
	'F': {"█████", "█    ", "████ ", "█    ", "█    "},
	'P': {"████ ", "█   █", "████ ", "█    ", "█    "},
}

const wordmarkText = "TIDEFTP"

// wordmark is big block lettering that appears letter by letter, then keeps a
// highlight sweeping across it.
func (r renderer) wordmark() []string {
	total := 0
	for _, ch := range wordmarkText {
		total += utf8.RuneCountInString(glyphs[ch][0]) + 1
	}
	total--
	if r.width < total+2 {
		// Too narrow for the block letters: a plain styled name.
		return []string{r.center(r.bold(r.pal.Accent, "T I D E F T P"))}
	}

	shown := int(float64(len([]rune(wordmarkText))) * r.ease(6, 40))
	sweep := float64((r.frame*2)%(total+30)) - 12

	// Each row is built as runs of one colour. The shimmer is quantised to a few
	// shades so most of a row is a single run: a colour escape per block would
	// cost thousands of bytes a frame, and this redraws constantly.
	type cell struct {
		ch  string
		col lipgloss.Color
	}
	grid := [5][]cell{}
	col := 0
	for i, ch := range wordmarkText {
		g := glyphs[ch]
		w := utf8.RuneCountInString(g[0])
		base := r.pal.Accent
		if i >= 4 {
			base = r.pal.Accent2 // FTP is lighter than TIDE
		}
		for y := 0; y < 5; y++ {
			runes := []rune(g[y])
			for x := 0; x < w; x++ {
				if i >= shown || runes[x] == ' ' {
					grid[y] = append(grid[y], cell{" ", ""})
					continue
				}
				d := math.Abs(float64(col+x) - sweep)
				mix := math.Round(math.Max(0, 1-d/7)*5) / 5 // 6 shades
				grid[y] = append(grid[y], cell{"█", blend(base, r.pal.Spark, mix*0.85)})
			}
			grid[y] = append(grid[y], cell{" ", ""})
		}
		col += w + 1
	}
	rows := [5]string{}
	for y := range grid {
		var b strings.Builder
		var run strings.Builder
		var runColor lipgloss.Color
		flush := func() {
			if run.Len() == 0 {
				return
			}
			if runColor == "" {
				b.WriteString(run.String())
			} else {
				b.WriteString(r.fg(runColor, run.String()))
			}
			run.Reset()
		}
		for _, c := range grid[y] {
			if c.col != runColor {
				flush()
				runColor = c.col
			}
			run.WriteString(c.ch)
		}
		flush()
		rows[y] = b.String()
	}
	// One shared left margin: centring each row on its own trimmed width
	// would stagger the letters, since the glyphs end in different columns.
	margin := strings.Repeat(" ", max(0, (r.width-total)/2))
	out := make([]string, 5)
	for y := range out {
		out[y] = margin + strings.TrimRight(rows[y], " ")
	}
	return out
}

const tagline = "Terminal Information Delivery Engine"

func (r renderer) tagline() string {
	n := int(float64(utf8.RuneCountInString(tagline)) * r.ease(34, 84))
	text := string([]rune(tagline)[:n])
	// Pad to full width so the line doesn't jitter as it types.
	pad := strings.Repeat(" ", utf8.RuneCountInString(tagline)-n)
	cursor := ""
	if n < utf8.RuneCountInString(tagline) {
		cursor = r.fg(r.pal.Accent, "▌")
		pad = pad[:max(0, len(pad)-1)]
	}
	return r.fg(r.pal.Dim, text) + cursor + pad
}

func (r renderer) versionLines(o Options) []string {
	v := o.Version
	if v == "" {
		v = "dev"
	}
	parts := []string{r.bold(r.pal.Fg, "TideFTP "+v)}
	if o.Commit != "" {
		parts = append(parts, r.fg(r.pal.Dim, "commit "+o.Commit))
	}
	if o.BuildDate != "" {
		parts = append(parts, r.fg(r.pal.Dim, "built "+o.BuildDate))
	}
	sep := r.fg(r.pal.Dim, "  ·  ")
	fade := r.ease(30, 56)
	line := strings.Join(parts, sep)
	if fade < 0.4 {
		line = r.fg(r.pal.Dim, ansi.Strip(line))
	}
	if fade <= 0 {
		return []string{""}
	}
	return []string{r.center(line)}
}

// message is the personal note, in a quote bar, wrapped to a readable width.
func (r renderer) message(o Options) []string {
	if strings.TrimSpace(o.Message) == "" {
		return nil
	}
	fade := r.ease(60, 90)
	if fade <= 0 {
		return []string{""}
	}
	width := min(56, r.width-8)
	if width < 16 {
		return nil
	}
	col := blend(r.pal.Dim, r.pal.Fg, fade)
	bar := r.fg(r.pal.Accent, "┃ ")
	var out []string
	for _, l := range wrap(o.Message, width-2) {
		out = append(out, r.center(bar+lipgloss.NewStyle().Foreground(col).Italic(true).Render(l+strings.Repeat(" ", width-2-utf8.RuneCountInString(l)))))
	}
	if strings.TrimSpace(o.Author) != "" {
		sig := "— " + strings.TrimSpace(o.Author)
		styled := r.fg(r.pal.Dim, sig)
		plainLen := utf8.RuneCountInString(sig)
		if o.Heart {
			styled += " " + r.fg("#ff4d6d", "♥")
			plainLen += 2
		}
		out = append(out, r.center(bar+strings.Repeat(" ", max(0, width-2-plainLen))+styled))
	}
	return out
}

func (r renderer) links(ls []Link) []string {
	if len(ls) == 0 || r.ease(70, 100) <= 0 {
		return nil
	}
	var parts []string
	for _, l := range ls {
		parts = append(parts, r.fg(r.pal.Dim, l.Label+" ")+r.fg(r.pal.Accent, l.URL))
	}
	return []string{r.center(strings.Join(parts, r.fg(r.pal.Dim, "   ")))}
}

// wrap breaks text on spaces so no line exceeds width runes.
func wrap(s string, width int) []string {
	var lines []string
	for _, para := range strings.Split(strings.TrimSpace(s), "\n") {
		cur := ""
		for _, word := range strings.Fields(para) {
			for utf8.RuneCountInString(word) > width {
				r := []rune(word)
				if cur != "" {
					lines = append(lines, cur)
					cur = ""
				}
				lines = append(lines, string(r[:width]))
				word = string(r[width:])
			}
			switch {
			case cur == "":
				cur = word
			case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(word) <= width:
				cur += " " + word
			default:
				lines = append(lines, cur)
				cur = word
			}
		}
		if cur != "" {
			lines = append(lines, cur)
		}
	}
	return lines
}

// String summarises the model for debugging.
func (m Model) String() string {
	return fmt.Sprintf("about{frame=%d palette=%s}", m.frame, m.palettes[m.palette].Name)
}

// miniLogo is the window mark at a quarter of the size, for modals too short
// for the full one: dots, a prompt, and a few animated waves.
func (r renderer) miniLogo() []string {
	const inner = 14
	if r.width < inner+4 {
		return nil
	}
	b := r.pal.Border
	cursor := "_"
	if (r.frame/6)%2 == 1 && r.frame < finalFrame {
		cursor = " "
	}
	dots := " " + r.fg("#ff5f56", "●") + " " + r.fg("#ffbd2e", "●") + " " + r.fg("#27c93f", "●") + strings.Repeat(" ", inner-6)
	prompt := " " + r.bold(r.pal.Fg, ">") + " " + r.bold(r.pal.Fg, cursor) + " "
	level := 0.25 + 0.75*r.ease(0, 36)
	return []string{
		r.center(r.fg(b, "╭"+strings.Repeat("─", inner)+"╮")),
		r.center(r.fg(b, "│") + dots + r.fg(b, "│")),
		r.center(r.fg(b, "│") + prompt + r.waveRow(inner-5, 1, 2, level, 0.6) + r.fg(b, "│")),
		r.center(r.fg(b, "│") + r.waveRow(inner, 0, 2, level, 0.6) + r.fg(b, "│")),
		r.center(r.fg(b, "╰"+strings.Repeat("─", inner)+"╯")),
	}
}

// bgSequence returns the escape that switches the background on, or "" when
// there is no background or the terminal can't show colour (so tests and
// piped output stay plain).
func bgSequence(bg lipgloss.Color) string {
	if bg == "" {
		return ""
	}
	rendered := lipgloss.NewStyle().Background(bg).Render(" ")
	if i := strings.IndexByte(rendered, ' '); i > 0 {
		return rendered[:i]
	}
	return ""
}

// paintBackground makes line exactly width cells wide on a solid background.
// Styled runs end with a reset, which would drop the background mid-line, so
// it is switched back on after every reset.
func paintBackground(line string, width int, seq string) string {
	pad := strings.Repeat(" ", max(0, width-lipgloss.Width(line)))
	if seq == "" {
		return line + pad
	}
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+seq)
	return seq + line + pad + "\x1b[0m"
}
