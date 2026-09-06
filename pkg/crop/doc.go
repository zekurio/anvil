// Package crop detects ffmpeg crop filters for worker jobs.
// It keeps the union of picture bounds in each sample and across samples.
// At least two usable samples must agree within 16 pixels on every edge.
// Failed samples, larger differences, and uneven opposite borders cause a
// no-crop fallback. Empty samples have no vote. Small edge strips of at most
// 16 pixels are kept. These limits favor picture safety over border removal.
package crop
