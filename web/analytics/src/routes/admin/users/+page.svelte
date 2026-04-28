<script lang="ts">
  import { onMount } from 'svelte';
  import { users, access, type User, type ServerAccessEntry, type ServerAccessRole } from '$lib/api/auth';
  import ErrorCard from '$lib/components/ErrorCard.svelte';
  import Sheet from '$lib/components/Sheet.svelte';

  // Users directory + per-user Manage-access sheet. Admin-only (sidebar
  // hides the nav item for non-admins; the RPCs themselves also check
  // the admin role on the backend so this page is just a convenience).

  let loading = true;
  let error: unknown = null;
  let rows: User[] = [];

  async function load() {
    loading = true;
    error = null;
    try {
      rows = await users.list();
    } catch (e) {
      error = e;
    } finally {
      loading = false;
    }
  }
  onMount(load);

  // --- Create user ---
  let showCreate = false;
  let newEmail = '';
  let newUsername = '';
  let createError: unknown = null;
  let creating = false;

  async function doCreate() {
    if (!newEmail.trim()) return;
    creating = true;
    createError = null;
    try {
      await users.create({ email: newEmail.trim(), username: newUsername.trim() || undefined });
      showCreate = false;
      newEmail = '';
      newUsername = '';
      await load();
    } catch (e) {
      createError = e;
    } finally {
      creating = false;
    }
  }

  // --- Delete user ---
  async function doDelete(u: User) {
    if (!u.uuid) return;
    if (!confirm(`Delete ${u.email ?? u.uuid}? This cannot be undone.`)) return;
    try {
      await users.del(u.uuid);
      await load();
    } catch (e) {
      alert(`Delete failed: ${String(e)}`);
    }
  }

  // --- Manage access sheet ---
  let accessUser: User | null = null;
  let accessRows: ServerAccessEntry[] = [];
  let accessLoading = false;
  let accessError: unknown = null;
  let grantServerID = '';
  let grantRole: ServerAccessRole = 'SERVER_ACCESS_ROLE_VIEWER';
  let configuredServers: Array<{ id: string; host: string; name?: string }> = [];

  onMount(() => {
    configuredServers = window.hulaConfig?.servers ?? [];
  });

  async function openAccess(u: User) {
    accessUser = u;
    accessRows = [];
    accessError = null;
    accessLoading = true;
    try {
      if (u.uuid) {
        accessRows = await access.list({ user_id: u.uuid });
      }
    } catch (e) {
      accessError = e;
    } finally {
      accessLoading = false;
    }
  }

  async function doGrant() {
    if (!accessUser?.uuid || !grantServerID) return;
    try {
      await access.grant(accessUser.uuid, grantServerID, grantRole);
      await openAccess(accessUser);
    } catch (e) {
      accessError = e;
    }
  }

  async function doRevoke(entry: ServerAccessEntry) {
    try {
      await access.revoke(entry.user_id, entry.server_id);
      if (accessUser) await openAccess(accessUser);
    } catch (e) {
      accessError = e;
    }
  }

  function roleLabel(r: ServerAccessRole): string {
    return r === 'SERVER_ACCESS_ROLE_MANAGER'
      ? 'manager'
      : r === 'SERVER_ACCESS_ROLE_VIEWER'
        ? 'viewer'
        : '—';
  }
</script>

<section class="space-y-6">
  <header class="flex items-center justify-between gap-4">
    <div>
      <h1 class="text-2xl font-semibold tracking-tight">Users</h1>
      <p class="text-sm text-muted-foreground">
        Provision operator accounts and grant per-server access.
      </p>
    </div>
    <button
      type="button"
      class="rounded bg-primary px-3 py-1.5 text-sm text-primary-foreground hover:bg-primary/90"
      on:click={() => (showCreate = true)}
    >
      + New user
    </button>
  </header>

  {#if error}
    <ErrorCard {error} onRetry={load} />
  {/if}

  <article class="rounded-lg border bg-card">
    <table class="min-w-full text-sm">
      <thead class="border-b bg-card">
        <tr class="text-left text-muted-foreground">
          <th class="px-3 py-2 font-medium">Email</th>
          <th class="px-3 py-2 font-medium">Username</th>
          <th class="px-3 py-2 font-medium">Created</th>
          <th class="px-3 py-2 font-medium text-right">Actions</th>
        </tr>
      </thead>
      <tbody>
        {#if loading}
          {#each Array.from({ length: 3 }) as _}
            <tr class="border-t">
              <td colspan="4" class="px-3 py-3">
                <div class="h-4 w-32 animate-pulse rounded bg-muted"></div>
              </td>
            </tr>
          {/each}
        {:else if rows.length === 0}
          <tr>
            <td colspan="4" class="px-3 py-12 text-center text-muted-foreground">
              No users yet. Click “New user” to invite the first operator.
            </td>
          </tr>
        {:else}
          {#each rows as u (u.uuid)}
            <tr class="border-t transition-colors hover:bg-accent/40">
              <td class="px-3 py-2">{u.email ?? '—'}</td>
              <td class="px-3 py-2 text-muted-foreground">{u.username ?? '—'}</td>
              <td class="px-3 py-2 text-muted-foreground">{u.created_at?.slice(0, 10) ?? '—'}</td>
              <td class="px-3 py-2 text-right">
                <button
                  type="button"
                  class="rounded border px-2 py-1 text-xs text-muted-foreground hover:text-foreground"
                  on:click={() => openAccess(u)}
                >
                  Manage access
                </button>
                <button
                  type="button"
                  class="ml-1 rounded border border-destructive/40 px-2 py-1 text-xs text-destructive hover:bg-destructive hover:text-destructive-foreground"
                  on:click={() => doDelete(u)}
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

<!-- Create user sheet -->
<Sheet bind:open={showCreate}>
  <h2 class="mb-1 text-lg font-semibold">New user</h2>
  <p class="mb-5 text-sm text-muted-foreground">
    A new operator account. They’ll sign in via SSO or break-glass password.
  </p>
  {#if createError}
    <ErrorCard error={createError} />
  {/if}
  <form
    class="space-y-4"
    on:submit|preventDefault={doCreate}
  >
    <label class="block text-sm">
      <span class="text-muted-foreground">Email</span>
      <input
        type="email"
        required
        class="mt-1 w-full rounded border bg-background px-2 py-1.5"
        bind:value={newEmail}
        placeholder="alice@example.com"
      />
    </label>
    <label class="block text-sm">
      <span class="text-muted-foreground">Username (optional)</span>
      <input
        type="text"
        class="mt-1 w-full rounded border bg-background px-2 py-1.5"
        bind:value={newUsername}
        placeholder="alice"
      />
    </label>
    <div class="flex justify-end gap-2 pt-2">
      <button
        type="button"
        class="rounded border px-3 py-1.5 text-sm"
        on:click={() => (showCreate = false)}
      >
        Cancel
      </button>
      <button
        type="submit"
        class="rounded bg-primary px-3 py-1.5 text-sm text-primary-foreground disabled:opacity-50"
        disabled={creating || !newEmail.trim()}
      >
        Create
      </button>
    </div>
  </form>
</Sheet>

<!-- Manage access sheet -->
<Sheet
  open={accessUser !== null}
  on:close={() => (accessUser = null)}
  width="w-[28rem]"
>
  <svelte:fragment let:close>
    <h2 class="mb-1 text-lg font-semibold">Manage access</h2>
    <p class="mb-4 text-sm text-muted-foreground">
      {accessUser?.email ?? accessUser?.uuid ?? ''}
    </p>

    {#if accessError}
      <ErrorCard error={accessError} />
    {/if}

    <h3 class="mb-2 text-sm font-medium text-muted-foreground">Current grants</h3>
    {#if accessLoading}
      <p class="text-sm text-muted-foreground">Loading…</p>
    {:else if accessRows.length === 0}
      <p class="text-sm text-muted-foreground">No server grants yet.</p>
    {:else}
      <ul class="space-y-1.5 text-sm">
        {#each accessRows as e (e.server_id)}
          <li class="flex items-center justify-between rounded border px-3 py-2">
            <div>
              <div class="font-medium">{e.server_id}</div>
              <div class="text-xs text-muted-foreground">{roleLabel(e.role)}</div>
            </div>
            <button
              type="button"
              class="rounded border border-destructive/40 px-2 py-1 text-xs text-destructive hover:bg-destructive hover:text-destructive-foreground"
              on:click={() => doRevoke(e)}
            >
              Revoke
            </button>
          </li>
        {/each}
      </ul>
    {/if}

    <h3 class="mb-2 mt-6 text-sm font-medium text-muted-foreground">Grant new access</h3>
    <div class="flex items-end gap-2">
      <label class="flex-1 text-sm">
        <span class="text-muted-foreground">Server</span>
        <select class="mt-1 w-full rounded border bg-background px-2 py-1.5" bind:value={grantServerID}>
          <option value="">Select a server…</option>
          {#each configuredServers as s (s.id)}
            <option value={s.id}>{s.name ?? s.id}</option>
          {/each}
        </select>
      </label>
      <label class="text-sm">
        <span class="text-muted-foreground">Role</span>
        <select class="mt-1 rounded border bg-background px-2 py-1.5" bind:value={grantRole}>
          <option value="SERVER_ACCESS_ROLE_VIEWER">Viewer</option>
          <option value="SERVER_ACCESS_ROLE_MANAGER">Manager</option>
        </select>
      </label>
    </div>
    <button
      type="button"
      class="mt-3 w-full rounded bg-primary px-3 py-1.5 text-sm text-primary-foreground disabled:opacity-50"
      disabled={!grantServerID}
      on:click={doGrant}
    >
      Grant
    </button>

    <button
      type="button"
      class="mt-6 w-full rounded border px-3 py-1.5 text-sm"
      on:click={close}
    >
      Close
    </button>
  </svelte:fragment>
</Sheet>
