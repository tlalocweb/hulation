<script lang="ts">
  import { onMount } from 'svelte';
  import { login, setToken } from '$lib/api/auth';
  import { ApiError } from '$lib/api/analytics';
  import ErrorCard from '$lib/components/ErrorCard.svelte';

  // OIDC redirect target. The provider redirects here with
  //   ?code=<auth_code>&state=<onetimetoken>
  // (state may be empty for simple flows). We POST to /auth/code,
  // stash the returned JWT in localStorage, and bounce to /analytics/.

  let error: unknown = null;
  let done = false;

  onMount(async () => {
    const params = new URLSearchParams(window.location.search);
    const code = params.get('code') ?? '';
    // The backend supports `onetimetoken` as the nonce carried through
    // the OIDC roundtrip. Different providers stash it under `state`
    // or `nonce`; we accept both.
    const onetimetoken = params.get('state') ?? params.get('onetimetoken') ?? '';
    const provider = params.get('provider') ?? '';

    if (!code) {
      error = new Error('missing auth code in callback URL');
      return;
    }

    try {
      const r = await login.withCode(code, onetimetoken, provider);
      if (r.error) throw new Error(r.error);
      if (!r.token) throw new Error('login returned no token');
      setToken(r.token);
      done = true;
      // Brief pause so the success message is visible; then bounce.
      setTimeout(() => (window.location.href = '/analytics/'), 400);
    } catch (e) {
      if (e instanceof ApiError) {
        error = new Error(`sign-in failed (HTTP ${e.status})`);
      } else {
        error = e;
      }
    }
  });
</script>

<div class="mx-auto flex min-h-screen max-w-md flex-col items-center justify-center px-4 py-10">
  <div class="w-full space-y-4">
    {#if error}
      <ErrorCard {error} />
      <a
        href="/analytics/login"
        class="block rounded border bg-card px-3 py-2 text-center text-sm font-medium hover:bg-accent"
      >
        Return to sign-in
      </a>
    {:else if done}
      <p class="rounded border bg-card px-3 py-2 text-center text-sm">
        Signed in — redirecting…
      </p>
    {:else}
      <p class="rounded border bg-card px-3 py-2 text-center text-sm text-muted-foreground">
        Completing sign-in…
      </p>
    {/if}
  </div>
</div>
