<script lang="ts">
  import { onMount } from 'svelte';
  import { filters } from '$lib/filters';
  import {
    reports,
    type ScheduledReport,
    type TemplateVariant,
    type ReportRun,
  } from '$lib/api/reports';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import Sheet from '$lib/components/Sheet.svelte';

  // Scheduled report authoring. Scoped to the currently-selected
  // server (top filter bar). CRUD + inline preview iframe + send-now
  // + last-runs timeline.

  let loading = true;
  let error: unknown = null;
  let rows: ScheduledReport[] = [];
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
      rows = await reports.list(currentServer);
    } catch (e) {
      error = e;
    } finally {
      loading = false;
    }
  }
  $: currentServer, load();

  // --- Edit/Create sheet ---
  let sheetOpen = false;
  let editing: ScheduledReport = newReport();
  let isEdit = false;
  let savingError: unknown = null;
  let recipientsStr = '';

  function newReport(): ScheduledReport {
    return {
      name: '',
      cron: '0 9 * * 1', // Mondays at 09:00
      timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
      recipients: [],
      template_variant: 'TEMPLATE_VARIANT_SUMMARY',
      enabled: true,
    };
  }

  function openCreate() {
    editing = newReport();
    recipientsStr = '';
    isEdit = false;
    savingError = null;
    sheetOpen = true;
  }

  function openEdit(r: ScheduledReport) {
    editing = { ...r };
    recipientsStr = (r.recipients ?? []).join(', ');
    isEdit = true;
    savingError = null;
    sheetOpen = true;
  }

  async function doSave() {
    if (!currentServer) return;
    editing.recipients = recipientsStr
      .split(/[,\s]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    savingError = null;
    try {
      if (isEdit && editing.id) {
        await reports.update(currentServer, editing.id, editing);
      } else {
        await reports.create(currentServer, editing);
      }
      sheetOpen = false;
      await load();
    } catch (e) {
      savingError = e;
    }
  }

  async function doDelete(r: ScheduledReport) {
    if (!r.id || !currentServer) return;
    if (!confirm(`Delete report "${r.name}"?`)) return;
    try {
      await reports.del(currentServer, r.id);
      await load();
    } catch (e) {
      alert(`Delete failed: ${String(e)}`);
    }
  }

  // --- Preview ---
  let previewOpen = false;
  let previewHtml = '';
  let previewSubject = '';
  let previewError: unknown = null;

  async function openPreview(r: ScheduledReport) {
    if (!r.id || !currentServer) return;
    previewOpen = true;
    previewHtml = '';
    previewSubject = '';
    previewError = null;
    try {
      const p = await reports.preview(currentServer, r.id);
      previewHtml = p.html ?? '';
      previewSubject = p.subject ?? '';
    } catch (e) {
      previewError = e;
    }
  }

  // --- Send now ---
  async function doSendNow(r: ScheduledReport) {
    if (!r.id || !currentServer) return;
    try {
      const resp = await reports.sendNow(currentServer, r.id);
      alert(`Queued for delivery. run_id=${resp.run_id ?? 'n/a'}`);
      if (runsFor === r.id) await loadRuns(r);
    } catch (e) {
      alert(`Send failed: ${String(e)}`);
    }
  }

  // --- Runs panel (inline) ---
  let runsFor: string | undefined = undefined;
  let runsLoading = false;
  let runRows: ReportRun[] = [];

  async function loadRuns(r: ScheduledReport) {
    if (!r.id || !currentServer) return;
    runsFor = r.id;
    runsLoading = true;
    runRows = [];
    try {
      runRows = await reports.listRuns(currentServer, r.id, 25);
    } catch (e) {
      // Soft failure — show empty panel with an error line.
      runRows = [];
    } finally {
      runsLoading = false;
    }
  }

  function variantLabel(v: TemplateVariant | undefined): string {
    return v === 'TEMPLATE_VARIANT_DETAILED' ? 'detailed' : 'summary';
  }
</script>

<section class="space-y-6">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Scheduled reports</h1>
      <p class="text-sm text-muted-foreground">
        Cron-scheduled email reports for
        <span class="font-medium">{currentServer || '— select a server —'}</span>.
      </p>
    </div>
    <button
      type="button"
      class="rounded bg-primary px-3 py-1.5 text-sm text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
      on:click={openCreate}
      disabled={!currentServer}
    >
      + New report
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
          <th class="px-3 py-2 font-medium">Cron</th>
          <th class="px-3 py-2 font-medium">Timezone</th>
          <th class="px-3 py-2 font-medium">Recipients</th>
          <th class="px-3 py-2 font-medium">Next fire</th>
          <th class="px-3 py-2 font-medium">Enabled</th>
          <th class="px-3 py-2 font-medium text-right">Actions</th>
        </tr>
      </thead>
      <tbody>
        {#if loading}
          {#each Array.from({ length: 2 }) as _}
            <tr class="border-t">
              <td colspan="7" class="px-3 py-3">
                <div class="h-4 w-32 animate-pulse rounded bg-muted"></div>
              </td>
            </tr>
          {/each}
        {:else if !currentServer}
          <tr>
            <td colspan="7" class="px-3 py-12 text-center text-muted-foreground">
              Select a server in the filter bar to manage its reports.
            </td>
          </tr>
        {:else if rows.length === 0}
          <tr>
            <td colspan="7" class="px-3 py-12 text-center text-muted-foreground">
              No scheduled reports yet.
            </td>
          </tr>
        {:else}
          {#each rows as r (r.id)}
            <tr class="border-t hover:bg-accent/40">
              <td class="px-3 py-2 font-medium">{r.name}</td>
              <td class="px-3 py-2 font-mono text-xs">{r.cron}</td>
              <td class="px-3 py-2 text-xs text-muted-foreground">{r.timezone}</td>
              <td class="px-3 py-2 text-xs text-muted-foreground">
                {(r.recipients ?? []).slice(0, 2).join(', ') + ((r.recipients ?? []).length > 2 ? ` +${(r.recipients ?? []).length - 2}` : '')}
              </td>
              <td class="px-3 py-2 text-xs text-muted-foreground">
                {r.next_fire_at?.replace('T', ' ').slice(0, 16) ?? '—'}
              </td>
              <td class="px-3 py-2 text-xs">{r.enabled ? '✓' : '—'}</td>
              <td class="px-3 py-2 text-right whitespace-nowrap">
                <button
                  type="button"
                  class="rounded border px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
                  on:click={() => openPreview(r)}
                >
                  Preview
                </button>
                <button
                  type="button"
                  class="ml-1 rounded border px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
                  on:click={() => doSendNow(r)}
                >
                  Send now
                </button>
                <button
                  type="button"
                  class="ml-1 rounded border px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
                  on:click={() => loadRuns(r)}
                >
                  Runs
                </button>
                <button
                  type="button"
                  class="ml-1 rounded border px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
                  on:click={() => openEdit(r)}
                >
                  Edit
                </button>
                <button
                  type="button"
                  class="ml-1 rounded border border-destructive/40 px-2 py-1 text-xs text-destructive hover:bg-destructive hover:text-destructive-foreground"
                  on:click={() => doDelete(r)}
                >
                  Delete
                </button>
              </td>
            </tr>
            {#if runsFor === r.id}
              <tr class="border-t bg-muted/20">
                <td colspan="7" class="px-3 py-3">
                  <div class="mb-2 flex items-center justify-between text-xs text-muted-foreground">
                    <span class="font-medium">Last runs</span>
                    <button
                      type="button"
                      class="text-xs underline"
                      on:click={() => (runsFor = undefined)}
                    >
                      hide
                    </button>
                  </div>
                  {#if runsLoading}
                    <p class="text-xs text-muted-foreground">Loading…</p>
                  {:else if runRows.length === 0}
                    <p class="text-xs text-muted-foreground">No runs yet. Use “Send now” to trigger one.</p>
                  {:else}
                    <ul class="space-y-1 text-xs">
                      {#each runRows as run (run.id)}
                        <li class="flex items-center gap-3 rounded border bg-card px-2 py-1.5">
                          <span
                            class="inline-block size-2 rounded-full
                              {run.status === 'success'
                                ? 'bg-primary'
                                : run.status === 'retrying'
                                  ? 'bg-accent'
                                  : 'bg-destructive'}"
                          ></span>
                          <span class="font-mono">{run.started_at?.replace('T', ' ').slice(0, 19)}</span>
                          <span class="text-muted-foreground">attempt {run.attempt}</span>
                          <span class="flex-1 truncate">{run.status}{run.error ? ` — ${run.error}` : ''}</span>
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

<!-- Create/Edit sheet -->
<Sheet bind:open={sheetOpen} width="w-[34rem]">
  <svelte:fragment let:close>
    <h2 class="mb-1 text-lg font-semibold">{isEdit ? 'Edit report' : 'New report'}</h2>
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
          placeholder="Weekly overview"
        />
      </label>

      <div class="grid grid-cols-2 gap-3">
        <label class="block text-sm">
          <span class="text-muted-foreground">Cron (min hr dom mon dow)</span>
          <input
            type="text"
            required
            class="mt-1 w-full rounded border bg-background px-2 py-1.5 font-mono"
            bind:value={editing.cron}
            placeholder="0 9 * * 1"
          />
        </label>
        <label class="block text-sm">
          <span class="text-muted-foreground">Timezone (IANA)</span>
          <input
            type="text"
            required
            class="mt-1 w-full rounded border bg-background px-2 py-1.5 font-mono"
            bind:value={editing.timezone}
            placeholder="America/Los_Angeles"
          />
        </label>
      </div>

      <label class="block text-sm">
        <span class="text-muted-foreground">Recipients (comma- or space-separated)</span>
        <textarea
          class="mt-1 w-full rounded border bg-background px-2 py-1.5 font-mono text-xs"
          rows="2"
          bind:value={recipientsStr}
          placeholder="alice@example.com, bob@example.com"
        ></textarea>
      </label>

      <label class="block text-sm">
        <span class="text-muted-foreground">Template</span>
        <select
          class="mt-1 w-full rounded border bg-background px-2 py-1.5"
          bind:value={editing.template_variant}
        >
          <option value="TEMPLATE_VARIANT_SUMMARY">Summary — four KPI boxes</option>
          <option value="TEMPLATE_VARIANT_DETAILED">Detailed — KPIs + top pages</option>
        </select>
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

<!-- Preview sheet -->
<Sheet bind:open={previewOpen} width="w-[42rem]">
  <svelte:fragment let:close>
    <h2 class="mb-1 text-lg font-semibold">Preview</h2>
    {#if previewSubject}
      <p class="mb-3 text-sm text-muted-foreground">
        Subject: <span class="font-medium text-foreground">{previewSubject}</span>
      </p>
    {/if}
    {#if previewError}
      <ErrorCard error={previewError} />
    {:else if previewHtml}
      <iframe
        title="Report preview"
        class="h-[70vh] w-full rounded border bg-white"
        sandbox=""
        srcdoc={previewHtml}
      ></iframe>
    {:else}
      <p class="text-sm text-muted-foreground">Loading preview…</p>
    {/if}
    <button type="button" class="mt-4 w-full rounded border px-3 py-1.5 text-sm" on:click={close}>
      Close
    </button>
  </svelte:fragment>
</Sheet>
