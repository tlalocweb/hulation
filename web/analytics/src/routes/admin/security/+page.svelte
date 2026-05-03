<script lang="ts">
  import { onMount } from 'svelte';
  import {
    badactor,
    type BadActorEntry,
    type BadActorStats,
  } from '$lib/api/badactor';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import Sheet from '$lib/components/Sheet.svelte';

  // Site-wide bad-actor view — score table + allowlist management +
  // manual block. Admin-only; the sidebar hides the entry point and
  // the server-side admin middleware enforces it for direct hits.

  let loadingList = true;
  let listError: unknown = null;
  let entries: BadActorEntry[] = [];

  let stats: BadActorStats | null = null;
  let statsError: unknown = null;

  let allowlist: string[] = [];
  let allowlistError: unknown = null;

  // Refresh cadence — reasonable balance between "table feels live"
  // and "we don't hammer the radix tree". Set to 0 to disable.
  const refreshIntervalMs = 15_000;
  let refreshTimer: ReturnType<typeof setInterval> | null = null;

  async function loadAll() {
    await Promise.all([loadList(), loadStats(), loadAllowlist()]);
  }

  async function loadList() {
    loadingList = true;
    listError = null;
    try {
      entries = (await badactor.list()) ?? [];
    } catch (e) {
      listError = e;
      entries = [];
    } finally {
      loadingList = false;
    }
  }

  async function loadStats() {
    statsError = null;
    try {
      stats = await badactor.stats();
    } catch (e) {
      statsError = e;
    }
  }

  async function loadAllowlist() {
    allowlistError = null;
    try {
      allowlist = (await badactor.allowlist.list()) ?? [];
    } catch (e) {
      allowlistError = e;
    }
  }

  onMount(() => {
    loadAll();
    if (refreshIntervalMs > 0) {
      refreshTimer = setInterval(loadAll, refreshIntervalMs);
    }
    return () => {
      if (refreshTimer) clearInterval(refreshTimer);
    };
  });

  // --- Row actions ---

  async function evict(ip: string) {
    if (!confirm(`Evict ${ip} from the bad-actor list? Their score resets to 0.`)) return;
    try {
      await badactor.evict(ip);
      await loadAll();
    } catch (e) {
      alert(`Evict failed: ${String(e)}`);
    }
  }

  async function addToAllowlist(ip: string) {
    const reason = prompt(`Allow ${ip} permanently? Add a note (visible in audit log).`, '') ?? '';
    try {
      await badactor.allowlist.add(ip, reason);
      await loadAll();
    } catch (e) {
      alert(`Allowlist add failed: ${String(e)}`);
    }
  }

  async function removeFromAllowlist(ip: string) {
    if (!confirm(`Remove ${ip} from the allowlist?`)) return;
    try {
      await badactor.allowlist.remove(ip);
      await loadAllowlist();
    } catch (e) {
      alert(`Allowlist remove failed: ${String(e)}`);
    }
  }

  // --- Manual block sheet ---

  let showBlock = false;
  let blockIP = '';
  let blockReason = '';
  let blocking = false;
  let blockError: unknown = null;

  async function doBlock() {
    if (!blockIP.trim()) return;
    blocking = true;
    blockError = null;
    try {
      await badactor.manualBlock(blockIP.trim(), blockReason.trim() || 'manually blocked');
      showBlock = false;
      blockIP = '';
      blockReason = '';
      await loadAll();
    } catch (e) {
      blockError = e;
    } finally {
      blocking = false;
    }
  }

  // --- Display helpers ---

  function fmtTime(iso: string | undefined): string {
    if (!iso) return '—';
    try {
      return new Date(iso).toLocaleString();
    } catch {
      return iso;
    }
  }

  // Time-until or "expired" for the ExpiresAt column. Bucketing
  // (m / h / d) keeps the column narrow and the eye relaxed.
  function fmtRelative(iso: string | undefined): string {
    if (!iso) return '—';
    const t = new Date(iso).getTime();
    if (Number.isNaN(t)) return iso;
    const ms = t - Date.now();
    if (ms <= 0) return 'expired';
    const sec = Math.floor(ms / 1000);
    if (sec < 60) return `${sec}s`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m`;
    const hr = Math.floor(min / 60);
    if (hr < 24) return `${hr}h`;
    const day = Math.floor(hr / 24);
    return `${day}d`;
  }
</script>

<section class="space-y-6">
  <header class="flex items-center justify-between">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Security</h1>
      <p class="text-sm text-muted-foreground">
        Site-wide bad-actor scoreboard. Covers all virtual hosts.
      </p>
    </div>
    <div class="flex gap-2">
      <button
        type="button"
        class="rounded border px-3 py-1.5 text-sm hover:bg-accent"
        on:click={loadAll}
      >
        Refresh
      </button>
      <button
        type="button"
        class="rounded bg-primary px-3 py-1.5 text-sm text-primary-foreground"
        on:click={() => (showBlock = true)}
      >
        Manual block
      </button>
    </div>
  </header>

  <!-- Stats -->
  {#if statsError}
    <ErrorCard error={statsError} />
  {:else if stats}
    <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
      <article class="rounded-lg border bg-card p-4">
        <div class="text-xs uppercase text-muted-foreground">Blocked</div>
        <div class="mt-1 text-2xl font-semibold">{stats.blocked_ips}</div>
      </article>
      <article class="rounded-lg border bg-card p-4">
        <div class="text-xs uppercase text-muted-foreground">Allowlisted</div>
        <div class="mt-1 text-2xl font-semibold">{stats.allowlisted_ips}</div>
      </article>
      <article class="rounded-lg border bg-card p-4">
        <div class="text-xs uppercase text-muted-foreground">Signatures</div>
        <div class="mt-1 text-2xl font-semibold">{stats.signatures}</div>
      </article>
      <article class="rounded-lg border bg-card p-4">
        <div class="text-xs uppercase text-muted-foreground">Threshold</div>
        <div class="mt-1 text-2xl font-semibold">{stats.block_threshold}</div>
        <div class="mt-0.5 text-xs text-muted-foreground">
          ttl {stats.ttl}{stats.dry_run ? ' · dry-run' : ''}{!stats.enabled ? ' · disabled' : ''}
        </div>
      </article>
    </div>
  {/if}

  <!-- Bad actor list -->
  <article class="rounded-lg border bg-card">
    <header class="flex items-center justify-between border-b px-4 py-3">
      <h2 class="text-sm font-medium">Scored IPs</h2>
      <span class="text-xs text-muted-foreground">
        {#if loadingList}Loading…{:else}{entries.length} entries{/if}
      </span>
    </header>

    {#if listError}
      <div class="p-4">
        <ErrorCard error={listError} onRetry={loadList} />
      </div>
    {:else}
      <table class="min-w-full text-sm">
        <thead class="border-b">
          <tr class="text-left text-muted-foreground">
            <th class="px-3 py-2 font-medium">IP</th>
            <th class="px-3 py-2 font-medium text-right">Score</th>
            <th class="px-3 py-2 font-medium">Status</th>
            <th class="px-3 py-2 font-medium">Reason</th>
            <th class="px-3 py-2 font-medium">Detected</th>
            <th class="px-3 py-2 font-medium">Expires</th>
            <th class="px-3 py-2 font-medium text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          {#if loadingList && entries.length === 0}
            {#each Array.from({ length: 4 }) as _}
              <tr class="border-t">
                <td colspan="7" class="px-3 py-3">
                  <div class="h-4 w-32 animate-pulse rounded bg-muted"></div>
                </td>
              </tr>
            {/each}
          {:else if entries.length === 0}
            <tr>
              <td colspan="7" class="px-3 py-12 text-center text-muted-foreground">
                No scored IPs. The radix tree is empty — nothing has tripped a signature in the
                current TTL window.
              </td>
            </tr>
          {:else}
            {#each entries as e (e.ip)}
              <tr class="border-t transition-colors hover:bg-accent/40">
                <td class="px-3 py-2 font-mono text-xs">{e.ip}</td>
                <td class="px-3 py-2 text-right tabular-nums">{e.score}</td>
                <td class="px-3 py-2">
                  {#if e.blocked}
                    <span class="rounded bg-destructive/10 px-2 py-0.5 text-xs font-medium text-destructive">
                      blocked
                    </span>
                  {:else}
                    <span class="rounded bg-muted px-2 py-0.5 text-xs text-muted-foreground">
                      flagged
                    </span>
                  {/if}
                </td>
                <td class="px-3 py-2 max-w-xs truncate text-xs text-muted-foreground" title={e.last_reason}>
                  {e.last_reason || '—'}
                </td>
                <td class="px-3 py-2 text-xs text-muted-foreground">{fmtTime(e.detected_at)}</td>
                <td class="px-3 py-2 text-xs text-muted-foreground">{fmtRelative(e.expires_at)}</td>
                <td class="px-3 py-2 text-right whitespace-nowrap">
                  <button
                    type="button"
                    class="rounded border px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
                    on:click={() => addToAllowlist(e.ip)}
                  >
                    Allow
                  </button>
                  <button
                    type="button"
                    class="ml-1 rounded border border-destructive/40 px-2 py-1 text-xs text-destructive hover:bg-destructive hover:text-destructive-foreground"
                    on:click={() => evict(e.ip)}
                  >
                    Evict
                  </button>
                </td>
              </tr>
            {/each}
          {/if}
        </tbody>
      </table>
    {/if}
  </article>

  <!-- Allowlist -->
  <article class="rounded-lg border bg-card">
    <header class="flex items-center justify-between border-b px-4 py-3">
      <h2 class="text-sm font-medium">Allowlist</h2>
      <span class="text-xs text-muted-foreground">{allowlist.length} entries</span>
    </header>
    {#if allowlistError}
      <div class="p-4">
        <ErrorCard error={allowlistError} onRetry={loadAllowlist} />
      </div>
    {:else if allowlist.length === 0}
      <p class="px-4 py-6 text-center text-sm text-muted-foreground">
        No allowlisted IPs. Use “Allow” on a scored IP, or block it manually with a note.
      </p>
    {:else}
      <ul class="divide-y">
        {#each allowlist as ip (ip)}
          <li class="flex items-center justify-between px-4 py-2 text-sm">
            <span class="font-mono text-xs">{ip}</span>
            <button
              type="button"
              class="rounded border border-destructive/40 px-2 py-1 text-xs text-destructive hover:bg-destructive hover:text-destructive-foreground"
              on:click={() => removeFromAllowlist(ip)}
            >
              Remove
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </article>
</section>

<!-- Manual block sheet -->
<Sheet bind:open={showBlock}>
  <h2 class="mb-1 text-lg font-semibold">Manual block</h2>
  <p class="mb-5 text-sm text-muted-foreground">
    Insert an IP at the block threshold so the next request is rejected. The TTL still applies — the
    block expires unless something else (a real signature hit) keeps the score above threshold.
  </p>
  {#if blockError}
    <ErrorCard error={blockError} />
  {/if}
  <form class="space-y-4" on:submit|preventDefault={doBlock}>
    <label class="block text-sm">
      <span class="text-muted-foreground">IP</span>
      <input
        type="text"
        required
        class="mt-1 w-full rounded border bg-background px-2 py-1.5 font-mono text-sm"
        bind:value={blockIP}
        placeholder="203.0.113.10"
      />
    </label>
    <label class="block text-sm">
      <span class="text-muted-foreground">Reason (optional)</span>
      <input
        type="text"
        class="mt-1 w-full rounded border bg-background px-2 py-1.5"
        bind:value={blockReason}
        placeholder="Manual block from security review"
      />
    </label>
    <div class="flex justify-end gap-2 pt-2">
      <button
        type="button"
        class="rounded border px-3 py-1.5 text-sm"
        on:click={() => (showBlock = false)}
      >
        Cancel
      </button>
      <button
        type="submit"
        class="rounded bg-primary px-3 py-1.5 text-sm text-primary-foreground disabled:opacity-50"
        disabled={blocking || !blockIP.trim()}
      >
        Block
      </button>
    </div>
  </form>
</Sheet>
