package clioutput

import (
	"fmt"
	"io"

	"github.com/charmbracelet/lipgloss"
)

// StatusKind identifies the semantic meaning of one rendered status line.
type StatusKind string

const (
	StatusOK     StatusKind = "OK"
	StatusAction StatusKind = "ACTION"
	StatusInfo   StatusKind = "INFO"
	StatusWarn   StatusKind = "WARN"
	StatusError  StatusKind = "ERROR"
)

// Renderer writes consistent plain-text CLI output with optional color.
type Renderer struct {
	out         io.Writer
	color       bool
	wroteOutput bool
}

// New returns a renderer that writes to out.
func New(out io.Writer, color bool) *Renderer {
	return &Renderer{out: out, color: color}
}

// Section prints a titled section and inserts a blank line between sections.
func (r *Renderer) Section(title string) {
	if r.wroteOutput {
		fmt.Fprintln(r.out)
	}
	fmt.Fprintln(r.out, title)
	r.wroteOutput = true
}

// Item prints a labeled fact line.
func (r *Renderer) Item(label string, value string) {
	if value == "" {
		fmt.Fprintf(r.out, "- %s\n", label)
		r.wroteOutput = true
		return
	}
	fmt.Fprintf(r.out, "- %s: %s\n", label, value)
	r.wroteOutput = true
}

// Status prints a line prefixed with a semantic status badge.
func (r *Renderer) Status(kind StatusKind, label string, detail string) {
	line := "- " + r.badge(kind)
	if label != "" {
		line += " " + label
	}
	if detail != "" {
		if label != "" {
			line += ": " + detail
		} else {
			line += " " + detail
		}
	}
	fmt.Fprintln(r.out, line)
	r.wroteOutput = true
}

// Line prints one raw line.
func (r *Renderer) Line(text string) {
	fmt.Fprintln(r.out, text)
	r.wroteOutput = true
}

func (r *Renderer) badge(kind StatusKind) string {
	label := "[" + string(kind) + "]"
	if !r.color {
		return label
	}

	switch kind {
	case StatusOK:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(label)
	case StatusAction:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render(label)
	case StatusInfo:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Render(label)
	case StatusWarn:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render(label)
	case StatusError:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(label)
	default:
		return label
	}
}
