import { useIsRouting } from "@solidjs/router";
import type { JSX } from "@solidjs/web";
import { Errored, Loading, Show } from "solid-js";

import { unreachable } from "../api";
import { Banner, ErrorState, LoadingState } from "../ui";
import { AddLibraryDialog } from "./AddLibraryDialog";
import { AppSidebar } from "./AppSidebar";

/** One line above every view while the daemon does not answer. Data on screen is the last known. */
export function ConnectionBanner() {
  return (
    <Show when={unreachable()}>
      <Banner tone="warning">The anvil daemon is not answering. What you see is the last known state.</Banner>
    </Show>
  );
}

/**
 * A page region with its own boundaries: a slow or failing page never takes
 * the navigation down with it.
 */
export function PageBoundary(props: { children: JSX.Element }) {
  return (
    <Errored fallback={(error, reset) => <ErrorState title="This page could not be loaded" error={error()} onRetry={reset} />}>
      <Loading fallback={<LoadingState />}>{props.children}</Loading>
    </Errored>
  );
}

/** The app frame: Sidebar on the left, the routed view on the right. */
export function AppShell(props: { children?: JSX.Element }) {
  // A navigation keeps the old view until the next one is ready; aria-busy draws the accent line that says so.
  const routing = useIsRouting();
  return (
    <div class="av-app">
      <AppSidebar />
      <div class="av-main" aria-busy={routing() ? "true" : undefined}>
        <ConnectionBanner />
        <PageBoundary>{props.children}</PageBoundary>
      </div>
      <AddLibraryDialog />
    </div>
  );
}

/** The frame of a full-bleed view such as the flow editor: no Sidebar, the Topbar carries a back link. */
export function BareShell(props: { children?: JSX.Element }) {
  const routing = useIsRouting();
  return (
    <div class="av-main av-main-full" aria-busy={routing() ? "true" : undefined}>
      <ConnectionBanner />
      <PageBoundary>{props.children}</PageBoundary>
    </div>
  );
}
