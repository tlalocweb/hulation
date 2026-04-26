<script lang="ts">
  import { filters, setServer } from '$lib/filters';
  import { onMount } from 'svelte';

  // The server list and currentServerId come from window.hulaConfig,
  // which hula injects into index.html before hydration (see
  // server/unified_analytics_ui.go). The /analytics/config.json
  // endpoint mirrors the same payload but isn't read here — we rely
  // on the inline script so child components can read window.hulaConfig
  // synchronously in their own onMount handlers.
  //
  // If window.hulaConfig is missing or empty the server is misconfigured.
  // We surface that as a visible error rather than inventing fake server
  // IDs, which would mask the underlying problem and cause queries to
  // be issued against a server that doesn't exist.
  let servers: Array<{ id: string; host: string; name?: string }> = [];
  let configError = '';

  onMount(() => {
    const cfg = (typeof window !== 'undefined' && window.hulaConfig) || null;
    if (!cfg || !Array.isArray(cfg.servers) || cfg.servers.length === 0) {
      configError =
        'window.hulaConfig is missing — hula did not inject the analytics ' +
        'configuration. Check that /analytics/config.json is reachable and ' +
        'returns a non-empty servers list.';
      return;
    }
    servers = cfg.servers;

    // Default to the server matching the current request host (provided
    // by the backend as currentServerId). If none of the configured
    // servers matches the host we're being served at, that is itself a
    // misconfiguration — surface it.
    const current = ($filters.server_id ?? '').trim();
    if (!current) {
      const target = (cfg.currentServerId ?? '').trim();
      if (!target) {
        configError =
          'No server matches host ' +
          window.location.host +
          '. Configure this host as a Server (or alias) in the hula config.';
        return;
      }
      setServer(target);
    }
  });

  function onChange(ev: Event) {
    const id = (ev.target as HTMLSelectElement).value;
    const target = servers.find((s) => s.id === id);
    if (!target) return;
    // The dashboard at /analytics is scoped to the host we're served
    // from. Switching to a different server means navigating to that
    // server's host instead of mutating the in-page filter — otherwise
    // the page silently shows analytics for a host other than the one
    // in the URL bar, which is confusing and breaks back/forward nav.
    if (target.host && target.host !== window.location.host) {
      const url = new URL(window.location.href);
      url.host = target.host;
      window.location.assign(url.toString());
      return;
    }
    setServer(id);
  }

  $: current = $filters.server_id;
</script>

{#if configError}
  <span
    class="rounded border border-destructive bg-destructive/10 px-2 py-1 text-xs text-destructive"
    role="alert"
  >
    {configError}
  </span>
{:else}
  <label class="flex items-center gap-2 text-sm">
    <span class="text-muted-foreground">Server</span>
    <select
      class="rounded border bg-background px-2 py-1.5 text-sm"
      value={current}
      on:change={onChange}
      aria-label="Select server"
    >
      {#each servers as s (s.id)}
        <option value={s.id}>{s.name ?? s.id} ({s.host})</option>
      {/each}
    </select>
  </label>
{/if}
