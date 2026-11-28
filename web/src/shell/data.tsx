import type { JSX } from "@solidjs/web";
import { createContext, useContext, type Accessor } from "solid-js";

import { api, createQuery, keys, POLL_LIST, POLL_STATUS, type FlowSummary, type Library, type Session, type Status } from "../api";

/**
 * Server state every view needs, fetched once and kept fresh by polling:
 * status every 2 s, lists every 5 s. Read these instead of fetching the same
 * resource again in a page.
 */
export interface AppData {
  session: Accessor<Session>;
  status: Accessor<Status>;
  /** Every library with its stats, sorted by name. */
  libraries: Accessor<Library[]>;
  /** Every flow without its graph. */
  flows: Accessor<FlowSummary[]>;
}

const AppDataContext = createContext<AppData>();

export function AppDataProvider(props: { session: Accessor<Session>; children: JSX.Element }) {
  const data: AppData = {
    session: props.session,
    status: createQuery(() => keys.status, api.status, { poll: POLL_STATUS }),
    libraries: createQuery(() => keys.libraryList, async () => (await api.libraries.list()).libraries, { poll: POLL_LIST }),
    flows: createQuery(() => keys.flowList, async () => (await api.flows.list()).flows, { poll: POLL_LIST }),
  };
  return <AppDataContext value={data}>{props.children}</AppDataContext>;
}

export function useAppData(): AppData {
  return useContext(AppDataContext);
}
