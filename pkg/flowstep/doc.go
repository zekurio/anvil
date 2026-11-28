// Package flowstep is the pipeline step that evaluates a library's flow for
// one file. It runs right after the probe, turns the flow snapshot of the
// attempt into the profile every later step works with, and ends the run
// when the flow says the file is to be skipped. The flow model itself lives
// in pkg/flow, which stays free of pipeline concerns.
package flowstep
