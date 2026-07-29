// Package textout renders operator-facing output for Anvil's two binaries.
// Both anvild and anvilctl print reports, so the writer that carries the first
// write error, the tab-aligned table, and the shared value formatting live here
// instead of being copied into each command tree.
package textout

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// Writer accumulates the first write error instead of forcing every print in a
// long report to be checked. Callers check once, at the end.
type Writer struct {
	out io.Writer
	err error
}

func NewWriter(out io.Writer) *Writer {
	return &Writer{out: out}
}

func (w *Writer) Printf(format string, args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintf(w.out, format, args...)
}

func (w *Writer) Println(args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintln(w.out, args...)
}

// Err reports the first write failure, if any.
func (w *Writer) Err() error {
	return w.err
}

func Write(out io.Writer, write func(*Writer)) error {
	w := NewWriter(out)
	write(w)
	if w.err != nil {
		return fmt.Errorf("write output: %w", w.err)
	}
	return nil
}

func WriteTable(out io.Writer, writeRows func(*Writer)) error {
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	w := NewWriter(table)
	writeRows(w)
	if w.err != nil {
		return fmt.Errorf("write table: %w", w.err)
	}
	if err := table.Flush(); err != nil {
		return fmt.Errorf("flush table: %w", err)
	}
	return nil
}

func WriteJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write json output: %w", err)
	}
	return nil
}

// OrNone keeps an absent value visible in a report, so a missing path reads as
// missing instead of as an empty column.
func OrNone(value string) string {
	if value == "" {
		return "<none>"
	}
	return value
}

// Bytes formats a byte count for humans. Negative values keep their sign,
// because a size delta that grew is a real and important result.
func Bytes(value int64) string {
	sign := ""
	size := float64(value)
	if value < 0 {
		sign = "-"
		size = -size
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	unit := 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%s%d %s", sign, int64(size), units[unit])
	}
	return fmt.Sprintf("%s%.1f %s", sign, size, units[unit])
}

func Percent(value float64) string {
	return fmt.Sprintf("%.1f%%", value)
}
