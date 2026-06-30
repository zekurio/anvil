package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

type outputWriter struct {
	out io.Writer
	err error
}

func newOutputWriter(out io.Writer) *outputWriter {
	return &outputWriter{out: out}
}

func (w *outputWriter) printf(format string, args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintf(w.out, format, args...)
}

func (w *outputWriter) println(args ...any) {
	if w.err != nil {
		return
	}
	_, w.err = fmt.Fprintln(w.out, args...)
}

func writeOutput(out io.Writer, write func(*outputWriter)) error {
	w := newOutputWriter(out)
	write(w)
	if w.err != nil {
		return fmt.Errorf("write output: %w", w.err)
	}
	return nil
}

func writeIndentedJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write json output: %w", err)
	}
	return nil
}

func writeTable(out io.Writer, writeRows func(*outputWriter)) error {
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	w := newOutputWriter(table)
	writeRows(w)
	if w.err != nil {
		return fmt.Errorf("write table: %w", w.err)
	}
	if err := table.Flush(); err != nil {
		return fmt.Errorf("flush table: %w", err)
	}
	return nil
}
