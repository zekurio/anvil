import { Errored, Loading, Show } from "solid-js";

import { api, createQuery, keys, unauthenticated } from "./api";
import { Router } from "./routes";
import { AppDataProvider } from "./shell/data";
import { SignIn } from "./shell/SignIn";
import { ConfirmDialog, ErrorState, LoadingState, Toaster } from "./ui";

/**
 * Decides between the sign-in screen and the app. The sign-in screen shows
 * only when GET /session says a password is required and missing, or after
 * any request answered 401.
 */
function Gate() {
  const session = createQuery(() => keys.session, api.session.get);
  const locked = () => unauthenticated() || (session().auth_required && !session().authenticated);
  return (
    <Show when={!locked()} fallback={<SignIn />}>
      <Router>{(route) => <AppDataProvider session={session}>{route.children}</AppDataProvider>}</Router>
    </Show>
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
