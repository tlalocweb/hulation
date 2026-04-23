<script lang="ts">
  import { filters, setServer } from '$lib/filters';
  import { onMount } from 'svelte';

  // Server list comes from window.hulaConfig, which hula renders at
  // /analytics/config.json (stage 2.8). During `pnpm dev` against a
  // local hula, the shim falls back to an env-derived default so the
  // dashboard still renders.
  let servers: Array<{ id: string; host: string; name?: string }> = [];

  onMount(() => {
    const cfg = (typeof window !== 'undefined' && window.hulaConfig) || null;
    if (cfg && cfg.servers?.length) {
      servers = cfg.servers;
    } else {
      // Dev-mode fallback. Match your local hula-config.yaml.
      servers = [
        { id: 'testsite', host: 'site.test.local', name: 'Test site' },
        { id: 'testsite-staging', host: 'staging.test.local', name: 'Test staging' },
        { id: 'testsite-seed', host: 'site.test.local', name: 'Test seed' },
      ];
    }
    // If the URL didn't specify a server, default to the first one so
    // the dashboard has something to render.
    const current = ($filters.server_id ?? '').trim();
    if (!current && servers.length > 0) {
      setServer(servers[0].id);
    }
  });

  function onChange(ev: Event) {
    const id = (ev.target as HTMLSelectElement).value;
    setServer(id);
  }

  $: current = $filters.server_id;
</script>

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
