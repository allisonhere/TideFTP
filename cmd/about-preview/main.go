// Command about-preview shows TideFTP's About screen on its own, so it can be
// looked at and approved before it is wired into the app.
//
//	go run ./cmd/about-preview                 # animated, full screen
//	go run ./cmd/about-preview --static        # no animation (reduced motion)
//	go run ./cmd/about-preview --print 140     # print one frame and exit
//	go run ./cmd/about-preview --message "…" --author "…" --version v1.0.0
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"

	"tideftp/internal/about"
)

type host struct{ m about.Model }

func (h host) Init() tea.Cmd { return h.m.Init() }
func (h host) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(about.CloseMsg); ok {
		return h, tea.Quit
	}
	var cmd tea.Cmd
	h.m, cmd = h.m.Update(msg)
	return h, cmd
}
func (h host) View() string { return h.m.View() }

func main() {
	opt := about.Options{
		Version:   "v0.4.0",
		Commit:    "6690fc2",
		BuildDate: "2026-10-08",
		Message:   about.DefaultMessage,
		Author:    about.DefaultAuthor,
		Heart:     true,
		Links: []about.Link{
			{Label: "source", URL: "github.com/allisonhere/TideFTP"},
		},
	}
	flag.StringVar(&opt.Version, "version", opt.Version, "version to show")
	flag.StringVar(&opt.Commit, "commit", opt.Commit, "commit hash to show")
	flag.StringVar(&opt.BuildDate, "built", opt.BuildDate, "build date to show")
	flag.StringVar(&opt.Message, "message", opt.Message, "the personal message")
	flag.StringVar(&opt.Author, "author", opt.Author, "who signs the message")
	flag.BoolVar(&opt.Heart, "heart", opt.Heart, "red heart after the signature")
	flag.IntVar(&opt.Palette, "palette", 0, "starting palette: 0 tide-night, 1 aurora, 2 sunset, 3 mono")
	flag.BoolVar(&opt.ReduceMotion, "static", false, "no animation")
	printFrame := flag.Int("print", -1, "print this frame to stdout (no terminal needed) and exit")
	plain := flag.Bool("plain", false, "with --print: strip colour")
	width := flag.Int("width", 80, "with --print: width")
	height := flag.Int("height", 30, "with --print: height")
	flag.Parse()

	if *printFrame >= 0 {
		out := about.New(opt).WithFrame(*printFrame).Render(*width, *height)
		if *plain {
			out = ansi.Strip(out)
		}
		fmt.Println(out)
		return
	}
	if !term.IsTerminal(os.Stdout.Fd()) {
		fmt.Fprintln(os.Stderr, "about-preview needs a terminal; use --print N to render a single frame")
		os.Exit(2)
	}
	if _, err := tea.NewProgram(host{about.New(opt)}, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
