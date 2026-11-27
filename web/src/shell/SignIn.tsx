import { createSignal } from "solid-js";

import { api, ApiError, errorMessage, invalidate, keys } from "../api";
import { markAuthenticated } from "../api/client";
import { useTitle } from "../lib/title";
import { Button, Field, Input, Logo } from "../ui";

/**
 * Shown instead of the app when the daemon wants a password: GET /session
 * says so, or any request answered 401.
 */
export function SignIn() {
  const [password, setPassword] = createSignal("");
  const [error, setError] = createSignal<string | null>(null);
  const [busy, setBusy] = createSignal(false);
  useTitle(() => "Sign in");

  const submit = async (event: SubmitEvent) => {
    event.preventDefault();
    if (password() === "") {
      setError("Enter the password.");
      return;
    }
    setBusy(true);
    try {
      await api.session.login(password());
      markAuthenticated();
      await invalidate(keys.session);
    } catch (err) {
      setError(err instanceof ApiError && err.status === 401 ? "That password is not correct." : errorMessage(err));
      setBusy(false);
    }
  };

  return (
    <main class="av-signin">
      <div class="av-signin-panel">
        <div class="av-signin-head">
          <Logo variant="wordmark" size={28} />
          <p class="av-signin-text">This daemon is protected by a password.</p>
        </div>
        <form class="av-signin-form" novalidate onSubmit={submit}>
          <Field label="Password" htmlFor="signin-password" error={error()}>
            <Input
              id="signin-password"
              type="password"
              autocomplete="current-password"
              autofocus
              invalid={!!error()}
              value={password()}
              onInput={(event) => {
                setPassword(event.currentTarget.value);
                setError(null);
              }}
            />
          </Field>
          <Button variant="primary" type="submit" busy={busy()}>
            Sign in
          </Button>
        </form>
      </div>
    </main>
  );
}
