import type { Library } from "../../api";

/** Where "/" leads: the first library that has files, else the first library, else nowhere. */
export function homeTarget(libraries: Library[]): string | undefined {
  const target = libraries.find((library) => library.stats.files > 0) ?? libraries[0];
  return target ? `/libraries/${encodeURIComponent(target.name)}` : undefined;
}
