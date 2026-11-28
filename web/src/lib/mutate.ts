import { ApiError, errorMessage } from "../api/client";
import { invalidate, type QueryKey } from "../api/query";
import { toast } from "../ui";

export interface RunOptions<T> {
  /** Query key prefixes to refetch once the mutation succeeded. `run` resolves after the fresh data is on screen. */
  invalidate?: QueryKey[];
  /** Toast shown on success: a sentence naming what happened. */
  success?: string | ((result: T) => string);
  /** What failed, as the toast title: "The library could not be saved." The daemon's message becomes the detail. */
  failure?: string;
  /** Return true when the error was shown elsewhere, e.g. on its form field. No toast then. */
  onError?: (error: ApiError) => boolean | void;
}

/**
 * Runs one mutation the way every action in the app does: await the request,
 * refetch what it changed, and say what happened. Resolves with the result,
 * or undefined when it failed.
 */
export async function run<T>(work: () => Promise<T>, options: RunOptions<T> = {}): Promise<T | undefined> {
  try {
    const result = await work();
    if (options.invalidate) await invalidate(...options.invalidate);
    if (options.success) toast.success(typeof options.success === "function" ? options.success(result) : options.success);
    return result;
  } catch (err) {
    // A 401 swaps the app for the sign-in screen; a toast behind it would only confuse.
    if (err instanceof ApiError && err.code === "unauthenticated") return undefined;
    if (err instanceof ApiError && options.onError?.(err)) return undefined;
    if (options.failure) toast.error(options.failure, { description: errorMessage(err) });
    else toast.error(errorMessage(err));
    return undefined;
  }
}
