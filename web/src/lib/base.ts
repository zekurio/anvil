// The daemon can serve the UI under a path such as /anvil (web.base_path). It
// writes that path into the page's <base href>, so one build works under any
// path: the build names its files relative to the base element, and the app
// takes its router and API base from it here. At the root, and in the dev
// server, the base is "/" and BASE_PATH is "".

/** The path the UI is served under, without a trailing slash: "" at the root, "/anvil" under a base path. */
export const BASE_PATH = new URL(document.baseURI).pathname.replace(/\/+$/, "");

/**
 * The href of an app path such as "/flows". navigate() adds the base itself;
 * a plain anchor needs this, or it leaves the app for the host's root.
 */
export function appHref(path: string): string {
  return BASE_PATH + path;
}

/** An app path such as "/flows" from a pathname the browser reports, which includes the base. */
export function appPath(pathname: string): string {
  if (!BASE_PATH || !pathname.startsWith(BASE_PATH)) return pathname;
  return pathname.slice(BASE_PATH.length) || "/";
}
