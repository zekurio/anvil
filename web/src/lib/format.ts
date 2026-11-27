// Formatting for everything the UI prints, following the design system's
// content rules: units always, lowercase, with a space ("12.4 GB", "84 fps",
// "6000 kb/s"); durations as 6:12 or 1:04:30; figures joined with a spaced
// middle dot; counts with thousands separators.

import type { Progress } from "../api/types";

const DOT = " · ";

/** Joins figures with a spaced middle dot, skipping empty ones: "84 fps · 1.9x · ETA 6:12". */
export function joinDot(...parts: (string | false | null | undefined)[]): string {
  return parts.filter(Boolean).join(DOT);
}

const count = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 });

/** A count with thousands separators: "4,812". */
export function formatCount(value: number): string {
  return count.format(value);
}

/** "1 file", "312 files". */
export function plural(value: number, one: string, many = `${one}s`): string {
  return `${formatCount(value)} ${value === 1 ? one : many}`;
}

const BYTE_UNITS = ["B", "kB", "MB", "GB", "TB", "PB"] as const;

/**
 * A size in decimal units, as disks and ffmpeg report them: "12.4 GB",
 * "2.13 TB", "200 MB". GB and up keep three significant digits below 100;
 * MB and below are whole numbers.
 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  let value = bytes;
  let unit = 0;
  while (value >= 999.5 && unit < BYTE_UNITS.length - 1) {
    value /= 1000;
    unit += 1;
  }
  const decimals = unit < 3 || value >= 99.95 ? 0 : value >= 9.995 || unit === 3 ? 1 : 2;
  return `${value.toFixed(decimals)} ${BYTE_UNITS[unit]}`;
}

/** A bitrate in kb/s, digits ungrouped as ffmpeg prints them: "6000 kb/s". */
export function formatBitrate(kbps: number): string {
  return `${Math.round(kbps)} kb/s`;
}

/** "84 fps". */
export function formatFps(fps: number): string {
  return `${fps >= 10 ? Math.round(fps) : fps.toFixed(1)} fps`;
}

/** Encoder speed relative to realtime: "1.9x". */
export function formatSpeed(speed: number): string {
  return `${speed.toFixed(1)}x`;
}

/** A whole percentage, or with `decimals`: "62%". */
export function formatPercent(value: number, decimals = 0): string {
  return `${value.toFixed(decimals)}%`;
}

/** A length of time as m:ss or h:mm:ss: "6:12", "1:04:30". */
export function formatDuration(seconds: number): string {
  const total = Math.max(0, Math.round(seconds));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = String(total % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}

/** The figures next to a progress bar: "84 fps · 1.9x · ETA 6:12". Unknown figures are left out. */
export function formatProgressDetail(progress: Pick<Progress, "fps" | "speed" | "eta_seconds">): string {
  return joinDot(progress.fps > 0 && formatFps(progress.fps), progress.speed > 0 && formatSpeed(progress.speed), progress.eta_seconds >= 0 && `ETA ${formatDuration(progress.eta_seconds)}`);
}

/** Before and after sizes: "14.7 GB → 6.1 GB". */
export function formatSizeChange(before: number, after: number): string {
  return `${formatBytes(before)} → ${formatBytes(after)}`;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** A local timestamp for tooltips and logs: "2026-11-27 13:04". */
export function formatDateTime(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/**
 * How long ago something happened: "just now", "4 min ago", "3 h ago",
 * "5 d ago", then the date. `now` is injectable so callers can tick it.
 */
export function formatRelative(iso: string, now: number = Date.now()): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";
  const seconds = Math.max(0, (now - then) / 1000);
  if (seconds < 45) return "just now";
  if (seconds < 3570) return `${Math.max(1, Math.round(seconds / 60))} min ago`;
  if (seconds < 84_600) return `${Math.round(seconds / 3600)} h ago`;
  if (seconds < 30 * 86_400) return `${Math.round(seconds / 86_400)} d ago`;
  return formatDateTime(iso).slice(0, 10);
}

const GO_UNITS: Record<string, string> = { h: "h", m: "min", s: "s", ms: "ms" };

/** A Go duration string in the UI's units: "1h30m" becomes "1 h 30 min". Empty stays empty. */
export function formatGoDuration(value: string): string {
  const parts = [...value.matchAll(/(\d+(?:\.\d+)?)(ms|h|m|s)/g)].filter(([, amount]) => Number(amount) !== 0);
  return parts.map(([, amount, unit]) => `${amount} ${GO_UNITS[unit ?? ""] ?? unit}`).join(" ");
}
