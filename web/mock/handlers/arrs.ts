// Arr connection routes.

import type { Arr, ArrList, ArrWrite } from "../../src/api/types.ts";
import { db, type StoredArr } from "../db.ts";
import { conflict, created, invalid, noContent, notFound, objectBody, type Context, type Router } from "../http.ts";

const view = (arr: StoredArr): Arr => ({ ...arr, libraries: db.libraries.filter((l) => l.arr === arr.name).map((l) => l.name) });

function find(ctx: Context): StoredArr {
  const arr = db.arrs.find((a) => a.name === ctx.params.name);
  if (!arr) throw notFound(`Arr connection "${ctx.params.name}"`);
  return arr;
}

function parse(write: Partial<ArrWrite>, existing?: StoredArr): StoredArr {
  const name = existing?.name ?? write.name;
  if (typeof name !== "string" || !/^[a-z0-9_-]{1,64}$/.test(name)) throw invalid('Must be 1 to 64 characters of a-z, 0-9, "-" and "_".', "name");
  if (write.type !== "radarr" && write.type !== "sonarr") throw invalid("Must be radarr or sonarr.", "type");
  if (typeof write.base_url !== "string" || !/^https?:\/\/\S+$/.test(write.base_url)) throw invalid("Must be a URL starting with http:// or https://.", "base_url");
  const api_key_file = existing?.api_key_file ?? "";
  // null keeps the stored key, "" clears it, anything else replaces it.
  const api_key_set = write.api_key === null || write.api_key === undefined ? (existing?.api_key_set ?? false) : write.api_key !== "";
  if (!api_key_set && api_key_file === "") throw invalid("An API key is required. Enter the key of the Radarr or Sonarr server.", "api_key");
  return { name, type: write.type, base_url: write.base_url.replace(/\/+$/, ""), api_key_file: api_key_set && write.api_key ? "" : api_key_file, api_key_set };
}

export function arrRoutes(router: Router): void {
  router.on("GET", "/arrs", (): ArrList => ({ arrs: [...db.arrs].sort((a, b) => a.name.localeCompare(b.name)).map(view) }));

  router.on("POST", "/arrs", (ctx) => {
    const arr = parse(objectBody<Partial<ArrWrite>>(ctx));
    if (db.arrs.some((a) => a.name === arr.name)) throw conflict(`An Arr connection named "${arr.name}" already exists. Connection names are unique: choose a different one.`, "name");
    db.arrs.push(arr);
    return created(view(arr));
  });

  router.on("PUT", "/arrs/:name", (ctx) => {
    const arr = find(ctx);
    Object.assign(arr, parse(objectBody<Partial<ArrWrite>>(ctx), arr));
    return view(arr);
  });

  router.on("DELETE", "/arrs/:name", (ctx) => {
    const arr = find(ctx);
    const users = db.libraries.filter((l) => l.arr === arr.name).map((l) => l.name);
    if (users.length > 0) throw conflict(`"${arr.name}" is still used by ${users.join(", ")}. Remove it from ${users.length === 1 ? "that library" : "those libraries"} first.`);
    db.arrs.splice(db.arrs.indexOf(arr), 1);
    return noContent();
  });
}
