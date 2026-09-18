// Package crop detects ffmpeg crop filters for worker jobs.
// Detection samples windows spread across the whole input, because a dark
// scene only reveals the part of the picture it lights up. The candidate is
// the union of the picture bounds those windows report, so a window that saw
// less never vetoes wider evidence, and every reported rectangle lies inside
// the real picture.
// ApplySafetyPolicy then rejects a candidate that is implausible for the
// source: it must keep enough of the source area, stay inside it, keep
// opposite borders even within 16 pixels, and remove more than a small edge
// strip. Rejected candidates fall back to no crop.
package crop
