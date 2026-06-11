<script lang="ts">
  import { onMount } from 'svelte';
  import { pairedDevices, type PairedDevice } from '$lib/api/pairedDevices';
  import ErrorCard from '$lib/components/ErrorCard.svelte';

  // QR-paired devices, across all users. Admin-only — the sidebar hides the nav
  // item for non-admins, and the backend gates ?all=true on the admin role, so
  // this page is a convenience over server/pair_handlers.go.

  let loading = true;
  let error: unknown = null;
  let rows: PairedDevice[] = [];
  let q = '';
  let revoking: string | null = null;

  async function load() {
    loading = true;
    error = null;
    try {
      rows = await pairedDevices.listAll();
    } catch (e) {
      error = e;
    } finally {
      loading = false;
    }
  }
  onMount(load);

  // Newest first (created_at is RFC3339, so lexical sort == chronological),
  // then narrow by the free-text filter over user / server / device id.
  $: sorted = [...rows].sort((a, b) => (b.created_at ?? '').localeCompare(a.created_at ?? ''));
  $: needle = q.trim().toLowerCase();
  $: filtered = needle
    ? sorted.filter((d) =>
        [d.user_id, d.server_id, d.device_id].some((v) => (v ?? '').toLowerCase().includes(needle))
      )
    : sorted;

  async function doRevoke(d: PairedDevice) {
    if (!d.device_id) return;
    const who = d.user_id ? ` (user ${d.user_id})` : '';
    if (!confirm(`Revoke device ${d.device_id}${who}? It will have to pair again to reconnect.`)) {
      return;
    }
    revoking = d.device_id;
    try {
      await pairedDevices.revoke(d.device_id);
      await load();
    } catch (e) {
      alert(`Revoke failed: ${String(e)}`);
    } finally {
      revoking = null;
    }
  }

  function short(key: string | undefined): string {
    if (!key) return '—';
    return key.length > 16 ? `${key.slice(0, 12)}…` : key;
  }

  function day(ts: string | undefined): string {
    // Treat empty/whitespace as missing — `?? '—'` wouldn't fire for "" since
    // an empty string is "defined", so a blank created_at would render blank.
    if (!ts || !ts.trim()) return '—';
    return ts.replace('T', ' ').slice(0, 16);
  }
</script>

<section class="space-y-6">
  <header class="flex items-start justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Paired devices</h1>
      <p class="text-sm text-muted-foreground">
        Mobile devices paired to this installation via QR / pair code. Revoking a device
        drops its key — it must pair again to reconnect.
      </p>
    </div>
    <button
      type="button"
      class="shrink-0 rounded border bg-background px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground disabled:opacity-50"
      on:click={load}
      disabled={loading}
    >
      Refresh
    </button>
  </header>

  {#if error}
    <ErrorCard {error} onRetry={load} />
  {/if}

  <div class="flex items-center gap-3">
    <input
      type="search"
      placeholder="Filter by user, server, or device id…"
      class="w-72 rounded border bg-background px-2 py-1.5 text-sm"
      bind:value={q}
    />
    {#if !loading}
      <span class="text-xs text-muted-foreground">
        {filtered.length}{filtered.length !== rows.length ? ` of ${rows.length}` : ''}
        device{rows.length === 1 ? '' : 's'}
      </span>
    {/if}
  </div>

  <article class="rounded-lg border bg-card">
    <table class="min-w-full text-sm">
      <thead class="border-b bg-card">
        <tr class="text-left text-muted-foreground">
          <th class="px-3 py-2 font-medium">User</th>
          <th class="px-3 py-2 font-medium">Server</th>
          <th class="px-3 py-2 font-medium">Device ID</th>
          <th class="px-3 py-2 font-medium">Public key</th>
          <th class="px-3 py-2 font-medium">Paired</th>
          <th class="px-3 py-2 font-medium text-right">Actions</th>
        </tr>
      </thead>
      <tbody>
        {#if loading}
          {#each Array.from({ length: 3 }) as _}
            <tr class="border-t">
              <td colspan="6" class="px-3 py-3">
                <div class="h-4 w-40 animate-pulse rounded bg-muted"></div>
              </td>
            </tr>
          {/each}
        {:else if filtered.length === 0}
          <tr>
            <td colspan="6" class="px-3 py-12 text-center text-muted-foreground">
              {rows.length === 0 ? 'No paired devices yet.' : 'No devices match that filter.'}
            </td>
          </tr>
        {:else}
          {#each filtered as d (d.device_id)}
            <tr class="border-t transition-colors hover:bg-accent/40">
              <td class="px-3 py-2">{d.user_id || '—'}</td>
              <td class="px-3 py-2 text-muted-foreground">{d.server_id || '—'}</td>
              <td class="px-3 py-2 font-mono text-xs" title={d.device_id}>{d.device_id || '—'}</td>
              <td class="px-3 py-2 font-mono text-xs text-muted-foreground" title={d.public_key_b64}>
                {short(d.public_key_b64)}
              </td>
              <td class="px-3 py-2 text-xs text-muted-foreground">{day(d.created_at)}</td>
              <td class="px-3 py-2 text-right">
                <button
                  type="button"
                  class="rounded border border-destructive/40 px-2 py-1 text-xs text-destructive hover:bg-destructive hover:text-destructive-foreground disabled:opacity-50"
                  on:click={() => doRevoke(d)}
                  disabled={revoking === d.device_id}
                >
                  {revoking === d.device_id ? 'Revoking…' : 'Revoke'}
                </button>
              </td>
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>
  </article>
</section>
