// The route table. One router for the whole app; every page is a lazy chunk
// with a default export, in its own directory under pages/.
//
// To add a route: create pages/<name>/index.tsx with a default-exported
// component, then add one entry below. Views with the Sidebar go under
// AppShell; full-bleed views go under BareShell.

import { createRouter, type RouteDefinition } from "@solidjs/router";
import { lazy } from "solid-js";

import { BASE_PATH } from "./lib/base";
import { AppShell, BareShell } from "./shell/AppShell";

export const LIBRARY_TABS = ["files", "rules", "history", "settings"] as const;
export type LibraryTab = (typeof LIBRARY_TABS)[number];

const INTEGER = /^\d+$/;

// The components gallery exists in dev builds only; the dead branch drops its chunk from production.
const devRoutes: RouteDefinition[] = import.meta.env.DEV ? [{ path: "/_dev/components", component: lazy(() => import("./pages/dev/components")) }] : [];

export const Router = createRouter({
  base: BASE_PATH,
  routes: [
    {
      path: "/flows",
      component: BareShell,
      children: [
        { path: "/new", component: lazy(() => import("./pages/flow-editor")) },
        { path: "/:id", matchFilters: { id: INTEGER }, component: lazy(() => import("./pages/flow-editor")) },
      ],
    },
    ...devRoutes,
    {
      path: "/",
      component: AppShell,
      children: [
        { path: "/", component: lazy(() => import("./pages/home")) },
        { path: "/libraries/:name/:tab?", matchFilters: { tab: [...LIBRARY_TABS] }, component: lazy(() => import("./pages/library")) },
        { path: "/flows", component: lazy(() => import("./pages/flows")) },
        { path: "/queue/:jobId?", matchFilters: { jobId: INTEGER }, component: lazy(() => import("./pages/queue")) },
        { path: "/settings", component: lazy(() => import("./pages/settings")) },
        { path: "*path", component: lazy(() => import("./pages/not-found")) },
      ],
    },
  ],
});
