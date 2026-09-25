package textout

import (
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
)

var headingStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan)
var labelStyle = lipgloss.NewStyle().Faint(true)

// WriteReport styles human output only. JSON always writes directly to its
// destination, without terminal detection or ANSI processing.
func WriteReport(out io.Writer, write func(*Writer)) error {
	output := colorprofile.NewWriter(out, os.Environ())
	width := 100
	file, ok := out.(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(file.Fd()) || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		output.Profile = colorprofile.NoTTY
	}
	if ok && term.IsTerminal(file.Fd()) {
		if columns, _, err := term.GetSize(file.Fd()); err == nil && columns > 0 {
			width = columns
		}
	}
	w := newWriter(output)
	w.width = width
	write(w)
	if w.err != nil {
		return fmt.Errorf("write output: %w", w.err)
	}
	return nil
}

func (w *Writer) Heading(title string) {
	w.Println(headingStyle.Render(title))
}

func (w *Writer) Field(label string, value any) {
	w.Paragraph(fmt.Sprintf("%s  %v", labelStyle.Render(label), value))
}

// Paragraph wraps indented report text to the available terminal width.
func (w *Writer) Paragraph(value string) {
	w.Println(lipgloss.NewStyle().PaddingLeft(2).Width(w.width).Render(value))
}

// State retains the text so status never depends on color alone.
func (w *Writer) State(state string) string {
	style := lipgloss.NewStyle()
	switch state {
	case "running", "ready", "pending":
		style = style.Foreground(lipgloss.Cyan)
	case "complete", "committed", "ok":
		style = style.Foreground(lipgloss.Green)
	case "failed", "conflict":
		style = style.Foreground(lipgloss.Red).Bold(true)
	case "canceled", "skipped", "blocked":
		style = style.Foreground(lipgloss.Yellow)
	}
	return style.Render(state)
}

// Table wraps long cells instead of silently losing paths or error details.
// On narrow terminals, labeled rows avoid squeezing columns beyond legibility.
func (w *Writer) Table(headers []string, rows [][]string) {
	if w.width > 0 && w.width < 72 {
		for i, row := range rows {
			if i > 0 {
				w.Println()
			}
			for j, value := range row {
				if strings.TrimSpace(value) != "" {
					w.Field(headers[j], value)
				}
			}
		}
		return
	}
	t := table.New().Headers(headers...).Rows(rows...).Border(lipgloss.NormalBorder()).
		BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false).
		BorderColumn(false).BorderHeader(true).BorderStyle(labelStyle).
		StyleFunc(func(row, col int) lipgloss.Style {
			style := lipgloss.NewStyle().PaddingRight(2)
			if row == table.HeaderRow {
				return style.Bold(true)
			}
			return style
		})
	if w.width > 0 && lipgloss.Width(t.Render()) > w.width {
		t.Width(w.width)
	}
	w.Println(t.Render())
}
