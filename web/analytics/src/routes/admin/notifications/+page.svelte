<script lang="ts">
  import { onMount } from 'svelte';
  import {
    notify,
    devices,
    type NotificationPrefs,
    type TestChannelResult,
    type Device,
  } from '$lib/api/notify';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import Sheet from '$lib/components/Sheet.svelte';

  // /admin/notifications: per-user prefs + TestNotification.
  //
  // ListNotificationPrefs returns every row. Admin can edit any row;
  // non-admin sessions don't see this page in the first place
  // (sidebar gates it behind window.hulaConfig.isAdmin).

  let rows: NotificationPrefs[] = [];
  let loading = true;
  let error: unknown = null;

  async function load() {
    loading = true;
    error = null;
    try {
      rows = await notify.list();
    } catch (e) {
      error = e;
    } finally {
      loading = false;
    }
  }
  onMount(load);

  // --- Edit sheet ---
  let sheetOpen = false;
  let editing: NotificationPrefs | null = null;
  let savingError: unknown = null;
  let testResults: TestChannelResult[] | null = null;
  let testing = false;
  let myDevices: Device[] = [];

  async function openEdit(p: NotificationPrefs) {
    editing = { ...p };
    savingError = null;
    testResults = null;
    sheetOpen = true;
    try {
      // The /api/mobile/v1/devices endpoint returns the caller's
      // own devices. For admin editing of another user's row we'd
      // need an admin-as-other-user variant — flagged as follow-up.
      myDevices = await devices.listMine();
    } catch {
      myDevices = [];
    }
  }

  async function doSave() {
    if (!editing || !editing.user_id) return;
    savingError = null;
    try {
      await notify.set(editing.user_id, editing);
      sheetOpen = false;
      await load();
    } catch (e) {
      savingError = e;
    }
  }

  async function doTest() {
    if (!editing || !editing.user_id) return;
    testing = true;
    testResults = null;
    try {
      testResults = await notify.test(editing.user_id);
    } catch (e) {
      savingError = e;
    } finally {
      testing = false;
    }
  }

  async function doForgetDevice(d: Device) {
    if (!d.id) return;
    if (!confirm(`Forget device "${d.label || d.id}"? The device will stop receiving pushes.`)) return;
    try {
      await devices.unregister(d.id);
      myDevices = myDevices.filter((x) => x.id !== d.id);
    } catch (e) {
      alert(`Failed: ${String(e)}`);
    }
  }

  function platformLabel(p: Device['platform']): string {
    if (p === 'PLATFORM_APNS') return 'iOS';
    if (p === 'PLATFORM_FCM') return 'Android';
    return '—';
  }
</script>

<section class="space-y-6">
  <header>
    <h1 class="text-2xl font-semibold tracking-tight">Notifications</h1>
    <p class="text-sm text-muted-foreground">
      Per-user delivery preferences (email + push) with optional quiet hours.
    </p>
  </header>

  {#if error}
    <ErrorCard {error} onRetry={load} />
  {/if}

  <article class="rounded-lg border bg-card">
    <table class="min-w-full text-sm">
      <thead class="border-b bg-card">
        <tr class="text-left text-muted-foreground">
          <th class="px-3 py-2 font-medium">User</th>
          <th class="px-3 py-2 font-medium">Email</th>
          <th class="px-3 py-2 font-medium">Push</th>
          <th class="px-3 py-2 font-medium">Quiet hours</th>
          <th class="px-3 py-2 font-medium">Timezone</th>
          <th class="px-3 py-2 font-medium">Updated</th>
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
        {:else if rows.length === 0}
          <tr>
            <td colspan="7" class="px-3 py-12 text-center text-muted-foreground">
              No notification preferences stored yet. Rows will appear after any user opens the app or an admin edits their prefs here.
            </td>
          </tr>
        {:else}
          {#each rows as p (p.user_id)}
            <tr class="border-t hover:bg-accent/40">
              <td class="px-3 py-2 font-medium">{p.user_id}</td>
              <td class="px-3 py-2 text-xs">{p.email_enabled ? '✓' : '—'}</td>
              <td class="px-3 py-2 text-xs">{p.push_enabled ? '✓' : '—'}</td>
              <td class="px-3 py-2 text-xs text-muted-foreground">
                {p.quiet_hours_start && p.quiet_hours_end
                  ? `${p.quiet_hours_start} → ${p.quiet_hours_end}`
                  : '—'}
              </td>
              <td class="px-3 py-2 text-xs text-muted-foreground">{p.timezone || '—'}</td>
              <td class="px-3 py-2 text-xs text-muted-foreground">
                {p.updated_at?.replace('T', ' ').slice(0, 16) ?? '—'}
              </td>
              <td class="px-3 py-2 text-right">
                <button
                  type="button"
                  class="rounded border px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
                  on:click={() => openEdit(p)}
                >
                  Edit
                </button>
              </td>
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>
  </article>
</section>

<Sheet bind:open={sheetOpen} width="w-[32rem]">
  <svelte:fragment let:close>
    {#if editing}
      <h2 class="mb-1 text-lg font-semibold">Edit preferences</h2>
      <p class="mb-4 text-sm text-muted-foreground">{editing.user_id}</p>

      {#if savingError}
        <ErrorCard error={savingError} />
      {/if}

      <form class="space-y-3" on:submit|preventDefault={doSave}>
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" bind:checked={editing.email_enabled} />
          <span>Email enabled</span>
        </label>
        <label class="flex items-center gap-2 text-sm">
          <input type="checkbox" bind:checked={editing.push_enabled} />
          <span>Push enabled</span>
        </label>

        <label class="block text-sm">
          <span class="text-muted-foreground">Timezone (IANA)</span>
          <input
            type="text"
            class="mt-1 w-full rounded border bg-background px-2 py-1.5 font-mono"
            bind:value={editing.timezone}
            placeholder="America/Los_Angeles"
          />
        </label>

        <div class="grid grid-cols-2 gap-3">
          <label class="block text-sm">
            <span class="text-muted-foreground">Quiet hours start</span>
            <input
              type="time"
              class="mt-1 w-full rounded border bg-background px-2 py-1.5"
              bind:value={editing.quiet_hours_start}
            />
          </label>
          <label class="block text-sm">
            <span class="text-muted-foreground">Quiet hours end</span>
            <input
              type="time"
              class="mt-1 w-full rounded border bg-background px-2 py-1.5"
              bind:value={editing.quiet_hours_end}
            />
          </label>
        </div>

        <div class="flex justify-end gap-2 pt-1">
          <button type="button" class="rounded border px-3 py-1.5 text-sm" on:click={close}>
            Cancel
          </button>
          <button
            type="submit"
            class="rounded bg-primary px-3 py-1.5 text-sm text-primary-foreground"
          >
            Save
          </button>
        </div>
      </form>

      <section class="mt-6 rounded border bg-muted/30 p-3 text-xs">
        <div class="flex items-center justify-between">
          <h3 class="font-semibold">Test delivery</h3>
          <button
            type="button"
            class="rounded border bg-background px-2 py-1"
            on:click={doTest}
            disabled={testing}
          >
            {testing ? 'Sending…' : 'Send test notification'}
          </button>
        </div>
        {#if testResults}
          <ul class="mt-2 space-y-1">
            {#each testResults as r (r.channel)}
              <li class="flex items-center gap-2">
                <span
                  class="inline-block size-2 rounded-full {r.ok ? 'bg-primary' : 'bg-destructive'}"
                ></span>
                <span class="font-mono">{r.channel}</span>
                {#if r.error}
                  <span class="text-muted-foreground">{r.error}</span>
                {:else}
                  <span class="text-muted-foreground">ok</span>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}
      </section>

      {#if myDevices.length > 0}
        <section class="mt-4">
          <h3 class="mb-2 text-xs font-semibold">My registered devices</h3>
          <ul class="space-y-1 text-xs">
            {#each myDevices as d (d.id)}
              <li class="flex items-center justify-between rounded border bg-card px-2 py-1.5">
                <span>
                  <strong class="font-mono">{d.label || d.id.slice(0, 8)}</strong>
                  <span class="ml-2 text-muted-foreground">{platformLabel(d.platform)}</span>
                  <span class="ml-2 text-muted-foreground">
                    last seen {d.last_seen_at?.replace('T', ' ').slice(0, 16)}
                  </span>
                </span>
                <button
                  type="button"
                  class="rounded border px-2 py-0.5 text-xs text-muted-foreground hover:text-destructive"
                  on:click={() => doForgetDevice(d)}
                >
                  Forget
                </button>
              </li>
            {/each}
          </ul>
        </section>
      {/if}
    {/if}
  </svelte:fragment>
</Sheet>
