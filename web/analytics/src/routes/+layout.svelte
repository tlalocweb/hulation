<script lang="ts">
  import '../app.css';
  import Sidebar from '$lib/components/Sidebar.svelte';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { base } from '$app/paths';
  import {
    hydrateFromURL,
    presetToRange,
    setDateRange,
    setServer,
    filters,
  } from '$lib/filters';
  import { browser } from '$app/environment';
  import { getToken } from '$lib/api/auth';

  // Public routes — no chrome (no sidebar, no filter bar) and no
  // auth gate. Login + callback land here; everything else is
  // protected.
  $: pathname = $page.url.pathname;
  $: isPublicRoute =
    pathname === `${base}/login` ||
    pathname === `${base}/login/` ||
    pathname.startsWith(`${base}/login/`);

  // Hydrate the filter store from a URL and re-apply defaults for
  // anything the URL didn't specify. Called both from onMount and on
  // every client-side navigation — sidebar links use bare paths
  // (no ?server_id=...) so without the re-default step, switching
  // tabs would clear the server selection and every per-page query
  // would dismiss itself to "no data" until refresh.
  function hydrateAndApplyDefaults(url: URL): void {
    hydrateFromURL(url);
    const state = $filters;
    if (!state.server_id) {
      const def = (window.hulaConfig?.currentServerId ?? '').trim();
      if (def) setServer(def);
    }
    if (!state.filters.from || !state.filters.to) {
      const { from, to } = presetToRange('7d');
      setDateRange(from, to);
    }
  }

  // Auth-gate the protected surface client-side. The APIs already
  // 401 without a token, but the sidebar shouldn't tease pages the
  // caller can't load.
  onMount(() => {
    if (!browser) return;
    if (isPublicRoute) return;
    if (!getToken()) {
      window.location.href = `${base}/login`;
      return;
    }
    hydrateAndApplyDefaults($page.url);
  });

  $: if (browser && !isPublicRoute && $page.url) hydrateAndApplyDefaults($page.url);
</script>

{#if isPublicRoute}
  <slot />
{:else}
  <div class="flex h-screen overflow-hidden">
    <Sidebar />
    <div class="flex flex-1 flex-col overflow-hidden">
      <FilterBar />
      <main class="flex-1 overflow-auto p-6">
        <slot />
      </main>
    </div>
  </div>
{/if}
