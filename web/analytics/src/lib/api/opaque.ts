// OPAQUE PAKE login + register helpers — wraps @serenity-kit/opaque.
//
// CRITICAL: this module imports `@serenity-kit/opaque` via dynamic
// import (`await import(...)`) so Vite chunks it into its own
// JS bundle that is ONLY downloaded when the /login route mounts.
// Static `import` would pull the ~159 KB serenity-kit ESM into
// every dashboard chunk — that's not what we want.
//
// Server-side wire library is bytemare/opaque; round-trip interop
// verified end-to-end (OPAQUE_PLAN §18).

const SERVER_IDENTITY = 'hula';

// Opaque module shape (loaded lazily). Keeping the typing loose
// because the package's types live behind the dynamic import.
type SerenityOpaque = {
  ready: Promise<void>;
  client: {
    startRegistration: (p: { password: string }) => {
      clientRegistrationState: string;
      registrationRequest: string;
    };
    finishRegistration: (p: {
      password: string;
      clientRegistrationState: string;
      registrationResponse: string;
      identifiers?: { client?: string; server?: string };
    }) => { registrationRecord: string; exportKey: string };
    startLogin: (p: { password: string }) => {
      clientLoginState: string;
      startLoginRequest: string;
    };
    finishLogin: (p: {
      clientLoginState: string;
      loginResponse: string;
      password: string;
      identifiers?: { client?: string; server?: string };
    }) => null | {
      finishLoginRequest: string;
      sessionKey: string;
      exportKey: string;
    };
  };
};

let cached: Promise<SerenityOpaque> | null = null;

// loadOpaque returns the @serenity-kit/opaque module, lazily fetched
// + initialised. Subsequent calls re-use the same promise.
export async function loadOpaque(): Promise<SerenityOpaque> {
  if (cached) return cached;
  cached = (async () => {
    const m = (await import('@serenity-kit/opaque')) as unknown as SerenityOpaque;
    await m.ready;
    return m;
  })();
  return cached;
}

// Pre-warm the module without blocking the caller. Call from a
// `/login` route's onMount so the WASM is decoded by the time the
// user submits the form.
export function prewarmOpaque(): void {
  void loadOpaque();
}

// --- Login --------------------------------------------------------

export interface OpaqueLoginResult {
  jwt: string;
  totpRequired: boolean;
}

export interface OpaqueLoginInitResp {
  ke2_b64: string;
  session_id: string;
}

export interface OpaqueLoginFinishResp {
  admintoken?: string;
  token?: string;
  totp_required?: boolean;
  error?: string;
}

export class OpaqueAuthError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'OpaqueAuthError';
  }
}

async function postJSON<T>(path: string, body: unknown): Promise<T> {
  const r = await fetch(path, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!r.ok) {
    const t = await r.text();
    throw new Error(`HTTP ${r.status} on ${path}: ${t}`);
  }
  return (await r.json()) as T;
}

// loginAdmin — drives an admin login via OPAQUE. Throws
// OpaqueAuthError on invalid credentials or when the user has no
// OPAQUE record on the server (operator must bootstrap first).
// JWT lands in the result on success.
export async function loginAdmin(
  username: string,
  password: string,
): Promise<OpaqueLoginResult> {
  return loginInternal('admin', username, password);
}

// loginInternal — same as loginAdmin but for non-admin users
// stored under the "internal" provider. Returns the same shape
// (JWT lands under `jwt`).
export async function loginInternalUser(
  username: string,
  password: string,
): Promise<OpaqueLoginResult> {
  return loginInternal('internal', username, password);
}

async function loginInternal(
  provider: 'admin' | 'internal',
  username: string,
  password: string,
): Promise<OpaqueLoginResult> {
  const opaque = await loadOpaque();
  const r1 = opaque.client.startLogin({ password });

  const init = await postJSON<OpaqueLoginInitResp>(
    '/api/v1/auth/opaque/login/init',
    {
      username,
      provider,
      ke1_b64: r1.startLoginRequest,
    },
  );

  if (!init.ke2_b64) {
    throw new OpaqueAuthError(
      'No OPAQUE record on the server. The operator must bootstrap '
        + 'the admin password first (set-admin-password.sh on the '
        + 'deploy host).',
    );
  }

  const r2 = opaque.client.finishLogin({
    clientLoginState: r1.clientLoginState,
    loginResponse: init.ke2_b64,
    password,
    identifiers: { server: SERVER_IDENTITY, client: username },
  });
  if (!r2) {
    // serenity-kit returns null on server-identity verification
    // failure — same surface as wrong password from the user's
    // perspective.
    throw new OpaqueAuthError('Invalid credentials');
  }

  const finish = await postJSON<OpaqueLoginFinishResp>(
    '/api/v1/auth/opaque/login/finish',
    {
      session_id: init.session_id,
      ke3_b64: r2.finishLoginRequest,
    },
  );
  if (finish.error) {
    throw new OpaqueAuthError(finish.error);
  }
  const jwt = finish.admintoken ?? finish.token ?? '';
  if (!jwt) {
    throw new OpaqueAuthError('OPAQUE: server returned no token');
  }
  return { jwt, totpRequired: !!finish.totp_required };
}

// --- Registration -------------------------------------------------

export async function registerAdmin(
  username: string,
  password: string,
): Promise<void> {
  return registerInternal('admin', username, password);
}

export async function registerInternalUser(
  username: string,
  password: string,
): Promise<void> {
  return registerInternal('internal', username, password);
}

async function registerInternal(
  provider: 'admin' | 'internal',
  username: string,
  password: string,
): Promise<void> {
  const opaque = await loadOpaque();
  const r1 = opaque.client.startRegistration({ password });
  const init = await postJSON<{ m2_b64: string }>(
    '/api/v1/auth/opaque/register/init',
    {
      username,
      provider,
      m1_b64: r1.registrationRequest,
    },
  );
  const r3 = opaque.client.finishRegistration({
    password,
    registrationResponse: init.m2_b64,
    clientRegistrationState: r1.clientRegistrationState,
    identifiers: { server: SERVER_IDENTITY, client: username },
  });
  await postJSON<{ ok: boolean; error?: string }>(
    '/api/v1/auth/opaque/register/finish',
    {
      username,
      provider,
      m3_b64: r3.registrationRecord,
    },
  );
}
