// Shared HTTP helpers for the analytics API clients.
//
// Every client used to copy its own `authHeaders()` + `handle()` — which is how
// the `res.json()` → `res.text()` body double-read bug got pasted into nine
// files. Centralising them here fixes it in one place and stops it recurring.

/** localStorage key for the admin bearer token — the single source of truth.
 * `authHeaders()` reads it; the token writers import it so a writer and reader
 * can't drift to different keys: `setToken`/`clearToken` live in both auth.ts
 * and analytics.ts, and `getToken` in auth.ts. */
export const TOKEN_KEY = 'hula:token';

/** Thrown by `handle()` on a non-2xx response. `body` is the parsed JSON error
 * (or the raw text when it isn't JSON). Clients/UI parse `status` + `body`. */
export class ApiError extends Error {
  constructor(
    public status: number,
    public body: unknown
  ) {
    super(`API error ${status}`);
  }
}

/** Bearer auth header from the stored admin token (empty when not logged in). */
export function authHeaders(): Record<string, string> {
  const headers: Record<string, string> = {};
  if (typeof localStorage !== 'undefined') {
    const t = localStorage.getItem(TOKEN_KEY);
    if (t) headers.Authorization = `Bearer ${t}`;
  }
  return headers;
}

/** Resolve a JSON response, throwing `ApiError` on non-2xx.
 *
 * The error path reads the body **once** via `res.text()` then best-effort
 * `JSON.parse`s it. Calling `res.json()` first would consume the stream even
 * when parsing fails, so a subsequent `res.text()` throws "body stream already
 * read" and masks the real `ApiError`. */
export async function handle<T>(res: Response): Promise<T> {
  if (!res.ok) {
    const raw = await res.text().catch(() => '');
    let body: unknown = raw;
    try {
      body = raw ? JSON.parse(raw) : null;
    } catch {
      // not JSON — keep the raw text
    }
    throw new ApiError(res.status, body);
  }
  return (await res.json()) as T;
}
