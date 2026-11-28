package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/zekurio/anvil/internal/textout"
	"github.com/zekurio/anvil/pkg/control"
)

// Libraries and flows are created and edited in the web UI. These two
// commands only show them, which is what a shell needs: the names scan,
// jobs, and requeue take, and which flow a library runs.

func runLibraries(ctx context.Context, e *env, args []string) error {
	flags, positional, err := e.subcommand(librariesHelp, args, nil)
	if flags == nil {
		return err
	}
	if err := noArguments("libraries", positional); err != nil {
		return err
	}
	response, err := e.client.ListLibraries(ctx)
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeLibraries(e.out, response)
}

func runFlows(ctx context.Context, e *env, args []string) error {
	flags, positional, err := e.subcommand(flowsHelp, args, nil)
	if flags == nil {
		return err
	}
	if err := noArguments("flows", positional); err != nil {
		return err
	}
	response, err := e.client.ListFlows(ctx)
	if err != nil {
		return err
	}
	if e.json {
		return writeJSON(e.out, response)
	}
	return writeFlows(e.out, response)
}

func writeLibraries(out io.Writer, response control.LibraryListResponse) error {
	return textout.WriteReport(out, func(w *textout.Writer) {
		if len(response.Libraries) == 0 {
			w.Println("No libraries yet. Add one in the web UI.")
			return
		}
		w.Heading("Libraries")
		var rows [][]string
		for _, library := range response.Libraries {
			state := "active"
			if library.Paused {
				state = "paused"
			}
			reprocess := "no"
			if library.Reprocess {
				reprocess = "yes"
			}
			queued := library.Jobs["pending"] + library.Jobs["leased"] + library.Jobs["retrying"]
			running := library.Jobs["running"] + library.Jobs["validating"] + library.Jobs["replacing"]
			rows = append(rows, []string{
				library.Name, state, reprocess, library.FlowName,
				strconv.FormatInt(library.Files, 10),
				strconv.FormatInt(queued, 10),
				strconv.FormatInt(running, 10),
				strconv.FormatInt(library.Jobs["complete"], 10),
				strconv.FormatInt(library.Jobs["failed"], 10),
			})
		}
		w.Table([]string{"Name", "State", "Reprocess", "Flow", "Files", "Queued", "Running", "Done", "Failed"}, rows)
		// Paths are as long as they are; in the table above they would squeeze
		// every count next to them.
		w.Println()
		var paths [][]string
		for _, library := range response.Libraries {
			paths = append(paths, []string{library.Name, library.Path})
		}
		w.Table([]string{"Name", "Path"}, paths)
	})
}

func writeFlows(out io.Writer, response control.FlowListResponse) error {
	return textout.WriteReport(out, func(w *textout.Writer) {
		if len(response.Flows) == 0 {
			w.Println("No flows.")
			return
		}
		w.Heading("Flows")
		var rows [][]string
		for _, stored := range response.Flows {
			// A flow with errors fails every job that runs it, so the count is
			// in the table and not left to --json.
			check := "ok"
			switch {
			case stored.Errors > 0:
				check = fmt.Sprintf("%d errors", stored.Errors)
			case stored.Warnings > 0:
				check = fmt.Sprintf("%d warnings", stored.Warnings)
			}
			rows = append(rows, []string{
				strconv.FormatInt(stored.ID, 10), stored.Name, strconv.Itoa(stored.Revision),
				strconv.Itoa(stored.NodeCount), check, textout.OrNone(strings.Join(stored.Libraries, ", ")),
			})
		}
		w.Table([]string{"ID", "Name", "Revision", "Nodes", "Check", "Libraries"}, rows)
	})
}
