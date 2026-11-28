//go:build race

package store

// raceDetector reports whether the tests run under the race detector, which
// slows the pure-Go SQLite driver far too much for a time budget to mean
// anything.
const raceDetector = true
