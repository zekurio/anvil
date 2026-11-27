import { Errored, Loading, Show } from "solid-js";

import { api, createQuery, keys, unauthenticated } from "./api";
import { Router } from "./routes";
import { AppDataProvider } from "./shell/data";
import { SignIn } from "./shell/SignIn";
import { ConfirmDialog, ErrorState, LoadingState, Toaster } from "./ui";

/**
 * Starts the app and decides whether the sign-in screen covers it: when
 * GET /session says a password is required and missing, or after any request
 * answered 401.
 *
 * The app does not wait for that decision. Its requests start together with
 * the session probe, and a read that answers 401 waits for the sign-in and
 * asks again (see request in api/client.ts). So nothing is fetched in sequence
 * that could be fetched at once, and a session that ends in the middle of
 * work puts the sign-in screen over the app without unmounting it: the page,
 * its drafts and its open dialogs are still there afterwards.
 */
function Gate() {
  const session = createQuery(() => keys.session, api.session.get);
  const locked = () => unauthenticated() || (session().auth_required && !session().authenticated);
  return (
    <>
      {/* Its own boundary: while the app waits for a sign-in, its loading state must not hide the sign-in screen. */}
      <Loading fallback={<LoadingState label="Starting anvil" />}>
        <Router>{(route) => <AppDataProvider session={session}>{route.children}</AppDataProvider>}</Router>
      </Loading>
      <Show when={locked()}>
        <SignIn />
      </Show>
    </>
  );
}

/** The root: the last error boundary, the first loading state, and the overlay hosts every view shares. */
export function App() {
  return (
    <>
      <Errored fallback={(error, reset) => <ErrorState title="anvil could not start" error={error()} onRetry={reset} />}>
        <Loading fallback={<LoadingState label="Starting anvil" />}>
          <Gate />
        </Loading>
      </Errored>
      <Toaster />
      <ConfirmDialog />
    </>
  );
}
