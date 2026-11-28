/** One page of a list as the daemon answers it, and which page that turned out to be. */
export type Paged<T> = T & {
  /** The zero-based page the rows belong to. It is lower than the page asked for when the list shrank under it. */
  page: number;
};

/**
 * Loads one page of a paged list. The list can shrink under the page that is
 * open: jobs finish, files leave a filter. The daemon answers a page past the
 * end with no rows and the real total, and this then loads the last page that
 * has rows instead, so a table never sits on an empty page of a list that is
 * not empty.
 */
export async function loadPage<T extends { total: number }>(page: number, pageSize: number, load: (offset: number) => Promise<T>): Promise<Paged<T>> {
  const answer = await load(page * pageSize);
  const last = Math.max(0, Math.ceil(answer.total / pageSize) - 1);
  if (page <= last) return { ...answer, page };
  return { ...(await load(last * pageSize)), page: last };
}
