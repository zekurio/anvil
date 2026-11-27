import { api } from "../api";
import { run } from "../lib/mutate";

/**
 * Ends the session, then reloads the page: the sign-in screen only covers the
 * app, and what the app had loaded must not stay in the tab for the next
 * person at this machine.
 */
export async function signOut(): Promise<void> {
  const done = await run(() => api.session.logout().then(() => true), { failure: "Signing out failed." });
  if (done) window.location.reload();
}
