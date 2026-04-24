<script lang="ts">
  import '../app.css';
  import Sidebar from '$lib/components/Sidebar.svelte';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { base } from '$app/paths';
  import { hydrateFromURL, presetToRange, setDateRange, filters } from '$lib/filters';
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
    hydrateFromURL($page.url);
    const state = $filters;
    if (!state.filters.from || !state.filters.to) {
      const { from, to } = presetToRange('7d');
      setDateRange(from, to);
    }
  });

  $: if (browser && !isPublicRoute && $page.url) hydrateFromURL($page.url);
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
