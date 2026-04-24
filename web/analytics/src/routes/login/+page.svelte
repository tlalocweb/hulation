<script lang="ts">
  import { onMount } from 'svelte';
  import { login, setToken, type AuthProviderInfo } from '$lib/api/auth';
  import { ApiError } from '$lib/api/analytics';
  import ErrorCard from '$lib/components/ErrorCard.svelte';

  // Static-SPA page. Path under the analytics base is /analytics/login,
  // but from SvelteKit's perspective the base is transparent so we use
  // /login for the route.

  let providers: AuthProviderInfo[] = [];
  let providersError: unknown = null;
  let providersLoading = true;

  let username = 'admin';
  let password = '';
  let adminError: unknown = null;
  let adminSubmitting = false;

  onMount(async () => {
    try {
      providers = await login.providers();
    } catch (e) {
      // A missing /auth/providers endpoint is not fatal — the break-
      // glass admin form below still works.
      providers = [];
      providersError = e;
    } finally {
      providersLoading = false;
    }
  });

  function goProvider(p: AuthProviderInfo) {
    if (p.auth_url) {
      window.location.href = p.auth_url;
    }
  }

  async function doAdminLogin() {
    if (!username || !password) return;
    adminError = null;
    adminSubmitting = true;
    try {
      const r = await login.admin(username, password);
      if (r.error) throw new Error(r.error);
      if (!r.admintoken) throw new Error('login returned no token');
      setToken(r.admintoken);
      window.location.href = '/analytics/';
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        adminError = new Error('Invalid credentials');
      } else {
        adminError = e;
      }
    } finally {
      adminSubmitting = false;
    }
  }
</script>

<div class="mx-auto flex min-h-screen max-w-md flex-col items-center justify-center px-4 py-10">
  <div class="w-full space-y-6">
    <header class="text-center">
      <h1 class="text-2xl font-semibold tracking-tight">Sign in to Hula</h1>
      <p class="mt-1 text-sm text-muted-foreground">
        Pick a provider below or use the admin form.
      </p>
    </header>

    <section class="space-y-3">
      {#if providersLoading}
        <div class="h-10 animate-pulse rounded border bg-muted/30"></div>
        <div class="h-10 animate-pulse rounded border bg-muted/30"></div>
      {:else if providers.length === 0}
        <p class="rounded border bg-muted/30 px-3 py-2 text-center text-xs text-muted-foreground">
          No SSO providers configured.
        </p>
      {:else}
        {#each providers as p (p.name)}
          <button
            type="button"
            class="flex w-full items-center justify-center gap-2 rounded-lg border bg-card px-3 py-2.5 text-sm font-medium hover:bg-accent"
            on:click={() => goProvider(p)}
            disabled={!p.auth_url}
          >
            {#if p.icon_url}
              <img src={p.icon_url} alt="" class="size-4" />
            {/if}
            <span>Sign in with {p.display_name ?? p.name}</span>
          </button>
        {/each}
      {/if}
    </section>

    <div class="relative text-center">
      <span class="absolute inset-x-0 top-1/2 h-px bg-border" aria-hidden="true"></span>
      <span class="relative inline-block bg-background px-3 text-xs uppercase tracking-wider text-muted-foreground">
        or
      </span>
    </div>

    <section class="rounded-lg border bg-card p-4">
      <h2 class="mb-3 text-sm font-semibold">Administrator</h2>
      {#if adminError}
        <div class="mb-3">
          <ErrorCard error={adminError} />
        </div>
      {/if}
      <form class="space-y-3" on:submit|preventDefault={doAdminLogin}>
        <label class="block text-sm">
          <span class="text-muted-foreground">Username</span>
          <input
            type="text"
            class="mt-1 w-full rounded border bg-background px-2 py-1.5"
            autocomplete="username"
            bind:value={username}
            required
          />
        </label>
        <label class="block text-sm">
          <span class="text-muted-foreground">Password</span>
          <input
            type="password"
            class="mt-1 w-full rounded border bg-background px-2 py-1.5"
            autocomplete="current-password"
            bind:value={password}
            required
          />
        </label>
        <button
          type="submit"
          class="w-full rounded bg-primary px-3 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50"
          disabled={adminSubmitting || !username || !password}
        >
          {adminSubmitting ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
      {#if providersError}
        <p class="mt-3 text-center text-xs text-muted-foreground">
          (SSO provider list unavailable — admin login only)
        </p>
      {/if}
    </section>
  </div>
</div>
