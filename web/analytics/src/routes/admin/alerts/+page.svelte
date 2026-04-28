<script lang="ts">
  import { filters } from '$lib/filters';
  import {
    alerts,
    type Alert,
    type AlertKind,
    type AlertEvent,
  } from '$lib/api/alerts';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import Sheet from '$lib/components/Sheet.svelte';

  // Per-server alert rule CRUD + recent-fires timeline.

  let loading = true;
  let error: unknown = null;
  let rows: Alert[] = [];
  $: currentServer = $filters.server_id;

  async function load() {
    if (!currentServer) {
      rows = [];
      loading = false;
      return;
    }
    loading = true;
    error = null;
    try {
      rows = await alerts.list(currentServer);
    } catch (e) {
      error = e;
    } finally {
      loading = false;
    }
  }
  $: currentServer, load();

  // --- Create / edit ---
  let sheetOpen = false;
  let editing: Alert = newAlert();
  let isEdit = false;
  let savingError: unknown = null;
  let recipientsStr = '';

  function newAlert(): Alert {
    return {
      name: '',
      kind: 'ALERT_KIND_GOAL_COUNT_ABOVE',
      threshold: 10,
      window_minutes: 60,
      cooldown_minutes: 60,
      recipients: [],
      enabled: true,
    };
  }

  function openCreate() {
    editing = newAlert();
    recipientsStr = '';
    isEdit = false;
    savingError = null;
    sheetOpen = true;
  }

  function openEdit(a: Alert) {
    editing = { ...a };
    recipientsStr = (a.recipients ?? []).join(', ');
    isEdit = true;
    savingError = null;
    sheetOpen = true;
  }

  async function doSave() {
    if (!currentServer || !editing.name?.trim()) return;
    editing.recipients = recipientsStr
      .split(/[,\s]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    savingError = null;
    try {
      if (isEdit && editing.id) {
        await alerts.update(currentServer, editing.id, editing);
      } else {
        await alerts.create(currentServer, editing);
      }
      sheetOpen = false;
      await load();
    } catch (e) {
      savingError = e;
    }
  }

  async function doDelete(a: Alert) {
    if (!a.id || !currentServer) return;
    if (!confirm(`Delete alert "${a.name}"?`)) return;
    try {
      await alerts.del(currentServer, a.id);
      await load();
    } catch (e) {
      alert(`Delete failed: ${String(e)}`);
    }
  }

  // --- Fires panel ---
  let firesFor: string | undefined = undefined;
  let firesLoading = false;
  let fireRows: AlertEvent[] = [];

  async function loadFires(a: Alert) {
    if (!a.id || !currentServer) return;
    firesFor = a.id;
    firesLoading = true;
    fireRows = [];
    try {
      fireRows = await alerts.listEvents(currentServer, a.id, 25);
    } catch {
      fireRows = [];
    } finally {
      firesLoading = false;
    }
  }

  function kindLabel(k: AlertKind | undefined): string {
    switch (k) {
      case 'ALERT_KIND_GOAL_COUNT_ABOVE':
        return 'Goal count >';
      case 'ALERT_KIND_PAGE_TRAFFIC_DELTA':
        return 'Page traffic Δ%';
      case 'ALERT_KIND_FORM_SUBMISSION_RATE':
        return 'Form rate';
      case 'ALERT_KIND_BAD_ACTOR_RATE':
        return 'Bad-actor rate';
      case 'ALERT_KIND_BUILD_FAILED':
        return 'Build failed';
    }
    return '—';
  }
</script>

<section class="space-y-6">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Alerts</h1>
      <p class="text-sm text-muted-foreground">
        Threshold alert rules for
        <span class="font-medium">{currentServer || '— select a server —'}</span>.
        Evaluator runs every minute; delivery via email (push comes in Phase 5a).
      </p>
    </div>
    <button
      type="button"
      class="rounded bg-primary px-3 py-1.5 text-sm text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
      on:click={openCreate}
      disabled={!currentServer}
    >
      + New alert
    </button>
  </header>

  {#if error}
    <ErrorCard {error} onRetry={load} />
  {/if}

  <article class="rounded-lg border bg-card">
    <table class="min-w-full text-sm">
      <thead class="border-b bg-card">
        <tr class="text-left text-muted-foreground">
          <th class="px-3 py-2 font-medium">Name</th>
          <th class="px-3 py-2 font-medium">Kind</th>
          <th class="px-3 py-2 font-medium">Threshold</th>
          <th class="px-3 py-2 font-medium">Window</th>
          <th class="px-3 py-2 font-medium">Last fired</th>
          <th class="px-3 py-2 font-medium">Enabled</th>
          <th class="px-3 py-2 font-medium text-right">Actions</th>
        </tr>
      </thead>
      <tbody>
        {#if loading}
          <tr class="border-t">
            <td colspan="7" class="px-3 py-3">
              <div class="h-4 w-32 animate-pulse rounded bg-muted"></div>
            </td>
          </tr>
        {:else if !currentServer}
          <tr>
            <td colspan="7" class="px-3 py-12 text-center text-muted-foreground">
              Select a server in the filter bar to manage its alerts.
            </td>
          </tr>
        {:else if rows.length === 0}
          <tr>
            <td colspan="7" class="px-3 py-12 text-center text-muted-foreground">
              No alert rules yet.
            </td>
          </tr>
        {:else}
          {#each rows as a (a.id)}
            <tr class="border-t hover:bg-accent/40">
              <td class="px-3 py-2 font-medium">{a.name}</td>
              <td class="px-3 py-2 text-muted-foreground">{kindLabel(a.kind)}</td>
              <td class="px-3 py-2 tabular-nums">{a.threshold ?? '—'}</td>
              <td class="px-3 py-2 text-xs text-muted-foreground">{a.window_minutes}m</td>
              <td class="px-3 py-2 text-xs text-muted-foreground">
                {a.last_fired_at?.replace('T', ' ').slice(0, 16) ?? '—'}
              </td>
              <td class="px-3 py-2 text-xs">{a.enabled ? '✓' : '—'}</td>
              <td class="px-3 py-2 text-right whitespace-nowrap">
                <button
                  type="button"
                  class="rounded border px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
                  on:click={() => loadFires(a)}
                >
                  Fires
                </button>
                <button
                  type="button"
                  class="ml-1 rounded border px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
                  on:click={() => openEdit(a)}
                >
                  Edit
                </button>
                <button
                  type="button"
                  class="ml-1 rounded border border-destructive/40 px-2 py-1 text-xs text-destructive hover:bg-destructive hover:text-destructive-foreground"
                  on:click={() => doDelete(a)}
                >
                  Delete
                </button>
              </td>
            </tr>
            {#if firesFor === a.id}
              <tr class="border-t bg-muted/20">
                <td colspan="7" class="px-3 py-3">
                  <div class="mb-2 flex items-center justify-between text-xs text-muted-foreground">
                    <span class="font-medium">Recent fires</span>
                    <button
                      type="button"
                      class="text-xs underline"
                      on:click={() => (firesFor = undefined)}
                    >
                      hide
                    </button>
                  </div>
                  {#if firesLoading}
                    <p class="text-xs text-muted-foreground">Loading…</p>
                  {:else if fireRows.length === 0}
                    <p class="text-xs text-muted-foreground">No fires yet.</p>
                  {:else}
                    <ul class="space-y-1 text-xs">
                      {#each fireRows as f (f.id)}
                        <li class="flex items-center gap-3 rounded border bg-card px-2 py-1.5">
                          <span
                            class="inline-block size-2 rounded-full
                              {f.delivery_status === 'DELIVERY_STATUS_SUCCESS'
                                ? 'bg-primary'
                                : f.delivery_status === 'DELIVERY_STATUS_RETRYING'
                                  ? 'bg-accent'
                                  : f.delivery_status === 'DELIVERY_STATUS_MAILER_UNCONFIGURED'
                                    ? 'bg-muted-foreground'
                                    : 'bg-destructive'}"
                          ></span>
                          <span class="font-mono">{f.fired_at?.replace('T', ' ').slice(0, 19)}</span>
                          <span class="text-muted-foreground">
                            observed {f.observed_value?.toFixed(2)} vs {f.threshold?.toFixed(2)}
                          </span>
                          {#if f.error}
                            <span class="flex-1 truncate text-destructive">{f.error}</span>
                          {/if}
                        </li>
                      {/each}
                    </ul>
                  {/if}
                </td>
              </tr>
            {/if}
          {/each}
        {/if}
      </tbody>
    </table>
  </article>
</section>

<Sheet bind:open={sheetOpen} width="w-[32rem]">
  <svelte:fragment let:close>
    <h2 class="mb-1 text-lg font-semibold">{isEdit ? 'Edit alert' : 'New alert'}</h2>
    <p class="mb-4 text-sm text-muted-foreground">{currentServer}</p>

    {#if savingError}
      <ErrorCard error={savingError} />
    {/if}

    <form class="space-y-3" on:submit|preventDefault={doSave}>
      <label class="block text-sm">
        <span class="text-muted-foreground">Name</span>
        <input
          type="text"
          required
          class="mt-1 w-full rounded border bg-background px-2 py-1.5"
          bind:value={editing.name}
          placeholder="Conversions &gt; 100/hr"
        />
      </label>

      <label class="block text-sm">
        <span class="text-muted-foreground">Kind</span>
        <select
          class="mt-1 w-full rounded border bg-background px-2 py-1.5"
          bind:value={editing.kind}
        >
          <option value="ALERT_KIND_GOAL_COUNT_ABOVE">Goal count above</option>
          <option value="ALERT_KIND_PAGE_TRAFFIC_DELTA">Page traffic delta</option>
          <option value="ALERT_KIND_FORM_SUBMISSION_RATE">Form submission rate</option>
          <option value="ALERT_KIND_BAD_ACTOR_RATE">Bad-actor rate</option>
          <option value="ALERT_KIND_BUILD_FAILED">Build failed</option>
        </select>
      </label>

      {#if editing.kind === 'ALERT_KIND_GOAL_COUNT_ABOVE'}
        <label class="block text-sm">
          <span class="text-muted-foreground">Goal ID</span>
          <input
            type="text"
            class="mt-1 w-full rounded border bg-background px-2 py-1.5 font-mono"
            bind:value={editing.target_goal_id}
          />
        </label>
      {:else if editing.kind === 'ALERT_KIND_PAGE_TRAFFIC_DELTA'}
        <label class="block text-sm">
          <span class="text-muted-foreground">URL path</span>
          <input
            type="text"
            class="mt-1 w-full rounded border bg-background px-2 py-1.5 font-mono"
            bind:value={editing.target_path}
            placeholder="/pricing"
          />
        </label>
      {:else if editing.kind === 'ALERT_KIND_FORM_SUBMISSION_RATE'}
        <label class="block text-sm">
          <span class="text-muted-foreground">Form ID</span>
          <input
            type="text"
            class="mt-1 w-full rounded border bg-background px-2 py-1.5 font-mono"
            bind:value={editing.target_form_id}
          />
        </label>
      {/if}

      <div class="grid grid-cols-2 gap-3">
        <label class="block text-sm">
          <span class="text-muted-foreground">Threshold</span>
          <input
            type="number"
            step="0.01"
            class="mt-1 w-full rounded border bg-background px-2 py-1.5"
            bind:value={editing.threshold}
          />
        </label>
        <label class="block text-sm">
          <span class="text-muted-foreground">Window (minutes)</span>
          <input
            type="number"
            min="1"
            class="mt-1 w-full rounded border bg-background px-2 py-1.5"
            bind:value={editing.window_minutes}
          />
        </label>
      </div>

      <label class="block text-sm">
        <span class="text-muted-foreground">Cooldown (minutes)</span>
        <input
          type="number"
          min="0"
          class="mt-1 w-full rounded border bg-background px-2 py-1.5"
          bind:value={editing.cooldown_minutes}
        />
      </label>

      <label class="block text-sm">
        <span class="text-muted-foreground">Recipients (comma- or space-separated)</span>
        <textarea
          class="mt-1 w-full rounded border bg-background px-2 py-1.5 font-mono text-xs"
          rows="2"
          bind:value={recipientsStr}
          placeholder="alice@example.com"
        ></textarea>
      </label>

      <label class="flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={editing.enabled} />
        <span>Enabled</span>
      </label>

      <div class="flex justify-end gap-2 pt-2">
        <button type="button" class="rounded border px-3 py-1.5 text-sm" on:click={close}>
          Cancel
        </button>
        <button
          type="submit"
          class="rounded bg-primary px-3 py-1.5 text-sm text-primary-foreground disabled:opacity-50"
          disabled={!editing.name?.trim()}
        >
          {isEdit ? 'Save' : 'Create'}
        </button>
      </div>
    </form>
  </svelte:fragment>
</Sheet>
