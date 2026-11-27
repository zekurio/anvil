// Session, status, settings and filesystem routes.

import type { Session, Settings, Status } from "../../src/api/types.ts";
import { db, progressOf, queueCounts, VERSION, WORKERS } from "../db.ts";
import { listDirectory } from "../fs.ts";
import { HttpError, invalid, noContent, notFound, objectBody, type Router } from "../http.ts";

export const MOCK_PASSWORD = "anvil";

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

  router.on("GET", "/settings", (): Settings => ({
    version: VERSION,
    config_path: "/etc/anvil/anvil.toml",
    daemon: {
      temp_dir: "/var/lib/anvil/staging",
      store_path: "/var/lib/anvil/anvil.db",
      control_socket: "/run/anvil/anvil.sock",
      worker_count: WORKERS,
      total_threads: 16,
      max_threads_per_job: 8,
      max_attempts: 3,
      scan_interval: "30m",
      lease_duration: "2m",
      shutdown_policy: "drain",
      staging_cleanup_age: "24h",
      log_level: "info",
    },
    // VITE_MOCK_LISTEN=0.0.0.0:8484 shows the Settings page's warning about an open listen address.
    web: { listen: process.env.VITE_MOCK_LISTEN ?? "127.0.0.1:8484", auth_required: auth },
  }));

  router.on("GET", "/fs", (ctx) => {
    const path = ctx.query.get("path") ?? "";
    if (path !== "" && !path.startsWith("/")) throw invalid("Must be an absolute path.", "path");
    const files = ctx.query.get("files");
    const directory = listDirectory(path, files === "1" || files === "true");
    if (!directory) throw notFound(`The directory ${path}`);
    return directory;
  });
}
