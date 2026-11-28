// A fake filesystem for GET /fs. Library roots list their mock files; the
// rest is a plausible server layout.

import type { Directory, DirEntry } from "../src/api/types.ts";
import { db } from "./db.ts";

const STATIC_DIRS = [
  "/",
  "/downloads",
  "/downloads/complete",
  "/downloads/import",
  "/downloads/incomplete",
  "/home",
  "/home/anvil",
  "/media",
  "/media/anime",
  "/media/movies",
  "/media/music",
  "/media/tv",
  "/mnt",
  "/mnt/backup",
  "/srv",
  "/srv/imports",
  "/srv/imports/movies",
  "/srv/imports/tv",
  "/var",
  "/var/lib",
  "/var/lib/anvil",
  "/var/lib/anvil/staging",
];

interface Tree {
  dirs: Set<string>;
  files: Map<string, number>;
}

function tree(): Tree {
  const dirs = new Set(STATIC_DIRS);
  const files = new Map<string, number>();
  for (const file of db.files) {
    const root = db.libraries.find((l) => l.name === file.library)?.path;
    if (!root) continue;
    const full = `${root}/${file.path}`;
    files.set(full, file.size_bytes);
    for (let cut = full.lastIndexOf("/"); cut > 0; cut = full.lastIndexOf("/", cut - 1)) dirs.add(full.slice(0, cut));
  }
  return { dirs, files };
}

export function normalize(path: string): string {
  const parts: string[] = [];
  for (const part of path.split("/")) {
    if (part === "" || part === ".") continue;
    if (part === "..") parts.pop();
    else parts.push(part);
  }
  return `/${parts.join("/")}`;
}

const parentOf = (path: string) => (path === "/" ? "" : normalize(path.slice(0, path.lastIndexOf("/")) || "/"));
const nameOf = (path: string) => path.slice(path.lastIndexOf("/") + 1);

export function listDirectory(path: string, withFiles: boolean): Directory | undefined {
  const { dirs, files } = tree();
  const at = normalize(path || "/");
  if (!dirs.has(at)) return undefined;
  const entries: DirEntry[] = [];
  for (const dir of dirs) {
    if (dir !== at && parentOf(dir) === at) entries.push({ name: nameOf(dir), path: dir, dir: true });
  }
  if (withFiles) {
    for (const [file, size_bytes] of files) {
      if (parentOf(file) === at) entries.push({ name: nameOf(file), path: file, dir: false, size_bytes });
    }
  }
  entries.sort((a, b) => a.name.localeCompare(b.name));
  return { path: at, parent: parentOf(at), entries };
}
