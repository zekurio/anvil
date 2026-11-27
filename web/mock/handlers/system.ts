// Session, status, settings and filesystem routes.

import type { DaemonKey, DaemonKeyName, DaemonValues, Session, Settings, Status } from "../../src/api/types.ts";
import { db, progressOf, queueCounts, VERSION, WORKERS } from "../db.ts";
import { parseDuration } from "../flowcheck.ts";
import { listDirectory } from "../fs.ts";
import { HttpError, invalid, noContent, notFound, objectBody, type Router } from "../http.ts";

export const MOCK_PASSWORD = "anvil";

/** The [daemon] defaults of pkg/config, on a machine with 16 CPUs. */
const DAEMON_DEFAULTS: DaemonValues = {
  worker_count: 16,
  total_threads: 16,
  max_threads_per_job: 0,
  max_attempts: 3,
  scan_interval: "30m",
  filesystem_event_debounce: "2s",
  scheduler_interval: "5s",
  lease_duration: "30m",
  shutdown_policy: "drain",
  shutdown_timeout: "0s",
  staging_cleanup_age: "0s",
  log_level: "info",
};

/** What the mock's anvil.toml sets. These keys are pinned: the UI shows them read-only. */
const DAEMON_FILE: Partial<DaemonValues> = { worker_count: WORKERS, total_threads: 16 };

/** The values of the enumerated keys, and how the daemon lists them in an error. */
const CHOICES: Partial<Record<DaemonKeyName, { values: string[]; words: string }>> = {
  shutdown_policy: { values: ["drain", "cancel"], words: "drain or cancel" },
  log_level: { values: ["debug", "info", "warn", "error"], words: "debug, info, warn, or error" },
};
/** Durations that must be above zero; the others may be zero. */
const POSITIVE = new Set<DaemonKeyName>(["scan_interval", "scheduler_interval", "lease_duration"]);
const MIN_ONE = new Set<DaemonKeyName>(["worker_count", "max_attempts"]);

/** Checks one value of PUT /settings/daemon the way pkg/config does. */
function daemonValue(key: DaemonKeyName, value: unknown): number | string {
  if (typeof DAEMON_DEFAULTS[key] === "number") {
    const min = MIN_ONE.has(key) ? 1 : 0;
    if (typeof value !== "number" || !Number.isInteger(value) || value < min) throw invalid(`Enter a whole number of ${min} or more.`, key);
    return value;
  }
  const text = String(value).trim();
  const choices = CHOICES[key];
  if (choices) {
    const choice = text.toLowerCase();
    if (!choices.values.includes(choice)) throw invalid(`Choose ${choices.words}, not "${text}".`, key);
    return choice;
  }
  const seconds = parseDuration(text);
  if (seconds === undefined) throw invalid(`"${text}" is not a duration. Write one such as "30m", "90s", or "2h".`, key);
  if (POSITIVE.has(key) && seconds <= 0) throw invalid('Enter a duration above zero, such as "30s" or "10m".', key);
  if (seconds < 0) throw invalid('Enter a duration of zero or more, such as "0s" or "10m".', key);
  return text;
}

function settings(auth: boolean): Settings {
  const values = { ...DAEMON_DEFAULTS, ...db.daemonSettings, ...DAEMON_FILE };
  const keys = Object.fromEntries(
    (Object.keys(DAEMON_DEFAULTS) as DaemonKeyName[]).map((key): [DaemonKeyName, DaemonKey] => {
      const source = key in DAEMON_FILE ? "file" : key in db.daemonSettings ? "store" : "default";
      return [key, { source, editable: source !== "file", at_start: key === "staging_cleanup_age", choices: CHOICES[key]?.values ?? [] }];
    }),
  ) as Record<DaemonKeyName, DaemonKey>;
  return {
    version: VERSION,
    config_path: "/etc/anvil/anvil.toml",
    daemon: {
      media_roots: [],
      temp_dir: "/var/lib/anvil/staging",
      store_path: "/var/lib/anvil/anvil.db",
      control_socket: "/run/anvil/anvil.sock",
      ...values,
      defaults: DAEMON_DEFAULTS,
      keys,
    },
    // VITE_MOCK_LISTEN=0.0.0.0:8484 shows the Settings page's warning about an open listen address.
    web: { listen: process.env.VITE_MOCK_LISTEN ?? "127.0.0.1:8484", auth_required: auth },
  };
}

export function systemRoutes(router: Router, auth: boolean): void {
  const session = (): Session => ({ auth_required: auth, authenticated: !auth || db.authenticated });

  router.on("GET", "/session", session);
  router.on("POST", "/session", (ctx) => {
    const { password } = objectBody<{ password?: unknown }>(ctx);
    if (auth && password !== MOCK_PASSWORD) throw new HttpError(401, "unauthenticated", "That password is not correct.", "password");
    db.authenticated = true;
    return session();
  });
  router.on("DELETE", "/session", () => {
    db.authenticated = false;
    return noContent();
  });

  router.on("GET", "/status", (): Status => {
    const running = db.files.flatMap((f) => progressOf(f) ?? []);
    return {
      version: VERSION,
      server_time: new Date().toISOString(),
      started_at: new Date(db.startedAt).toISOString(),
      workers: { configured: WORKERS, active: running.length },
      queue: queueCounts(),
      running,
      libraries: db.libraries.length,
      flows: db.flows.length,
    };
  });

  router.on("GET", "/settings", () => settings(auth));
  router.on("PUT", "/settings/daemon", (ctx) => {
    const write = objectBody<Record<string, unknown>>(ctx);
    const next: Partial<DaemonValues> = {};
    for (const [key, value] of Object.entries(write)) {
      if (!(key in DAEMON_DEFAULTS)) throw invalid(`The request body is not valid: unknown field "${key}".`);
      if (value === null || value === undefined || value === "") continue;
      const name = key as DaemonKeyName;
      if (name in DAEMON_FILE) throw invalid("This key is set in anvil.toml, so only the file can change it. Remove it from the file to manage it here.", name);
      Object.assign(next, { [name]: daemonValue(name, value) });
    }
    db.daemonSettings = next;
    return settings(auth);
  });

  router.on("GET", "/fs", (ctx) => {
    const path = ctx.query.get("path") ?? "";
    if (path !== "" && !path.startsWith("/")) throw invalid("Must be an absolute path.", "path");
    const files = ctx.query.get("files");
    const directory = listDirectory(path, files === "1" || files === "true");
    if (!directory) throw notFound(`The directory ${path}`);
    return directory;
  });
}
