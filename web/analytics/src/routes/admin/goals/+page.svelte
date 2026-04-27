<script lang="ts">
  import { onMount } from 'svelte';
  import { filters } from '$lib/filters';
  import { goals, type Goal, type GoalKind } from '$lib/api/goals';
  import { ApiError } from '$lib/api/analytics';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import Sheet from '$lib/components/Sheet.svelte';

  // Per-server goal CRUD. The server selector in the top filter bar
  // picks which server's goals we're editing.

  let loading = true;
  let error: unknown = null;
  let rows: Goal[] = [];
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
      rows = await goals.list(currentServer);
    } catch (e) {
      error = e;
    } finally {
      loading = false;
    }
  }

  // Reactively reload when the selected server changes.
  $: currentServer, load();

  // --- Create / edit sheet ---
  let sheetOpen = false;
  let editing: Goal = newEmptyGoal();
  let isEdit = false;
  let savingError: unknown = null;
  let testResult: { would_fire?: number; scanned_events?: number } | null = null;
  let testing = false;

  function newEmptyGoal(): Goal {
    return {
      kind: 'GOAL_KIND_URL_VISIT',
      enabled: true,
      name: '',
      description: '',
    };
  }

  function openCreate() {
    editing = newEmptyGoal();
    isEdit = false;
    savingError = null;
    testResult = null;
    sheetOpen = true;
  }

  function openEdit(g: Goal) {
    editing = { ...g };
    isEdit = true;
    savingError = null;
    testResult = null;
    sheetOpen = true;
  }

  async function doSave() {
    if (!currentServer || !editing.name?.trim() || !editing.kind) return;
    savingError = null;
    try {
      if (isEdit && editing.id) {
        await goals.update(currentServer, editing.id, editing);
      } else {
        await goals.create(currentServer, editing);
      }
      sheetOpen = false;
      await load();
    } catch (e) {
      savingError = e;
    }
  }

  async function doDelete(g: Goal) {
    if (!g.id || !currentServer) return;
    if (!confirm(`Delete goal "${g.name}"?`)) return;
    try {
      await goals.del(currentServer, g.id);
      await load();
    } catch (e) {
      alert(`Delete failed: ${String(e)}`);
    }
  }

  async function doTest() {
    if (!currentServer || !editing.kind) return;
    testing = true;
    testResult = null;
    try {
      testResult = await goals.test(currentServer, editing, 7);
    } catch (e) {
      if (e instanceof ApiError && e.status === 501) {
        testResult = { would_fire: -1 };
      } else {
        savingError = e;
      }
    } finally {
      testing = false;
    }
  }

  function kindLabel(k: GoalKind | undefined): string {
    switch (k) {
      case 'GOAL_KIND_URL_VISIT':
        return 'URL visit';
      case 'GOAL_KIND_EVENT':
        return 'Event';
      case 'GOAL_KIND_FORM':
        return 'Form submit';
      case 'GOAL_KIND_LANDER':
        return 'Lander hit';
    }
    return '—';
  }

  function ruleSummary(g: Goal): string {
    switch (g.kind) {
      case 'GOAL_KIND_URL_VISIT':
        return g.rule_url_regex ?? '';
      case 'GOAL_KIND_EVENT':
        return g.rule_event_code ? `code=${g.rule_event_code}` : '';
      case 'GOAL_KIND_FORM':
        return g.rule_form_id ?? '';
      case 'GOAL_KIND_LANDER':
        return g.rule_lander_id ?? '';
    }
    return '';
  }
</script>

<section class="space-y-6">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Goals</h1>
      <p class="text-sm text-muted-foreground">
        Rules that flag a visitor event as a conversion. Scoped to
        <span class="font-medium">{currentServer || '— select a server —'}</span>.
      </p>
    </div>
    <button
      type="button"
      class="rounded bg-primary px-3 py-1.5 text-sm text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
      on:click={openCreate}
      disabled={!currentServer}
    >
      + New goal
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
          <th class="px-3 py-2 font-medium">Rule</th>
          <th class="px-3 py-2 font-medium">Enabled</th>
          <th class="px-3 py-2 font-medium text-right">Actions</th>
        </tr>
      </thead>
      <tbody>
        {#if loading}
          {#each Array.from({ length: 2 }) as _}
            <tr class="border-t">
              <td colspan="5" class="px-3 py-3">
                <div class="h-4 w-24 animate-pulse rounded bg-muted"></div>
              </td>
            </tr>
          {/each}
        {:else if !currentServer}
          <tr>
            <td colspan="5" class="px-3 py-12 text-center text-muted-foreground">
              Select a server in the filter bar to manage its goals.
            </td>
          </tr>
        {:else if rows.length === 0}
          <tr>
            <td colspan="5" class="px-3 py-12 text-center text-muted-foreground">
              No goals yet. Click “New goal” to add one.
            </td>
          </tr>
        {:else}
          {#each rows as g (g.id)}
            <tr class="border-t hover:bg-accent/40">
              <td class="px-3 py-2 font-medium">{g.name}</td>
              <td class="px-3 py-2 text-muted-foreground">{kindLabel(g.kind)}</td>
              <td class="px-3 py-2 font-mono text-xs text-muted-foreground">{ruleSummary(g)}</td>
              <td class="px-3 py-2 text-xs">{g.enabled ? '✓' : '—'}</td>
              <td class="px-3 py-2 text-right">
                <button
                  type="button"
                  class="rounded border px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
                  on:click={() => openEdit(g)}
                >
                  Edit
                </button>
                <button
                  type="button"
                  class="ml-1 rounded border border-destructive/40 px-2 py-1 text-xs text-destructive hover:bg-destructive hover:text-destructive-foreground"
                  on:click={() => doDelete(g)}
                >
                  Delete
                </button>
              </td>
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>
  </article>
</section>

<Sheet bind:open={sheetOpen} width="w-[30rem]">
  <svelte:fragment let:close>
    <h2 class="mb-1 text-lg font-semibold">{isEdit ? 'Edit goal' : 'New goal'}</h2>
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
          placeholder="e.g. Signed up"
        />
      </label>
      <label class="block text-sm">
        <span class="text-muted-foreground">Description (optional)</span>
        <input
          type="text"
          class="mt-1 w-full rounded border bg-background px-2 py-1.5"
          bind:value={editing.description}
        />
      </label>
      <label class="block text-sm">
        <span class="text-muted-foreground">Kind</span>
        <select class="mt-1 w-full rounded border bg-background px-2 py-1.5" bind:value={editing.kind}>
          <option value="GOAL_KIND_URL_VISIT">URL visit</option>
          <option value="GOAL_KIND_EVENT">Event code</option>
          <option value="GOAL_KIND_FORM">Form submit</option>
          <option value="GOAL_KIND_LANDER">Lander hit</option>
        </select>
      </label>

      {#if editing.kind === 'GOAL_KIND_URL_VISIT'}
        <label class="block text-sm">
          <span class="text-muted-foreground">URL path regex</span>
          <input
            type="text"
            class="mt-1 w-full rounded border bg-background px-2 py-1.5 font-mono"
            bind:value={editing.rule_url_regex}
            placeholder="^/thank-you(/|$)"
          />
        </label>
      {:else if editing.kind === 'GOAL_KIND_EVENT'}
        <label class="block text-sm">
          <span class="text-muted-foreground">Event code</span>
          <input
            type="number"
            class="mt-1 w-full rounded border bg-background px-2 py-1.5"
            bind:value={editing.rule_event_code}
            placeholder="32"
          />
        </label>
      {:else if editing.kind === 'GOAL_KIND_FORM'}
        <label class="block text-sm">
          <span class="text-muted-foreground">Form ID</span>
          <input
            type="text"
            class="mt-1 w-full rounded border bg-background px-2 py-1.5 font-mono"
            bind:value={editing.rule_form_id}
            placeholder="contact"
          />
        </label>
      {:else if editing.kind === 'GOAL_KIND_LANDER'}
        <label class="block text-sm">
          <span class="text-muted-foreground">Lander ID</span>
          <input
            type="text"
            class="mt-1 w-full rounded border bg-background px-2 py-1.5 font-mono"
            bind:value={editing.rule_lander_id}
            placeholder="launch-2026"
          />
        </label>
      {/if}

      <label class="flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={editing.enabled} />
        <span>Enabled</span>
      </label>

      <div class="rounded border bg-muted/40 p-3 text-xs">
        <div class="flex items-center gap-2">
          <button
            type="button"
            class="rounded border bg-background px-2 py-1"
            on:click={doTest}
            disabled={testing || !editing.kind}
          >
            Test rule (7d)
          </button>
          {#if testResult}
            {#if testResult.would_fire === -1}
              <span class="text-muted-foreground">(TestGoal not implemented yet — stage 3.3b)</span>
            {:else}
              <span>would fire <strong>{testResult.would_fire}</strong> time(s) / {testResult.scanned_events ?? 0} events scanned</span>
            {/if}
          {/if}
        </div>
      </div>

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
