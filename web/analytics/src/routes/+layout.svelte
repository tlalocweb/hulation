<script lang="ts">
  import '../app.css';
  import Sidebar from '$lib/components/Sidebar.svelte';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { hydrateFromURL, presetToRange, setDateRange, filters } from '$lib/filters';
  import { browser } from '$app/environment';

  // Hydrate the filters store from the URL on first mount, then on
  // every navigation. SvelteKit SPA nav keeps the layout alive, so we
  // listen to $page.url changes.
  onMount(() => {
    hydrateFromURL($page.url);
    // If the URL didn't carry a from/to, default to 7 days so the
    // reports have a sane window.
    const state = $filters;
    if (!state.filters.from || !state.filters.to) {
      const { from, to } = presetToRange('7d');
      setDateRange(from, to);
    }
  });

  $: if (browser && $page.url) hydrateFromURL($page.url);
</script>

<div class="flex h-screen overflow-hidden">
  <Sidebar />
  <div class="flex flex-1 flex-col overflow-hidden">
    <FilterBar />
    <main class="flex-1 overflow-auto p-6">
      <slot />
    </main>
  </div>
</div>
