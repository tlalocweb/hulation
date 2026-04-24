<script lang="ts">
  import { page } from '$app/stores';
  import { base } from '$app/paths';
  import { onMount } from 'svelte';
  import { clearToken } from '$lib/api/auth';

  function signOut() {
    clearToken();
    window.location.href = `${base}/login`;
  }

  // Left nav. Reports are always visible; the Admin section only
  // surfaces when window.hulaConfig reports isAdmin=true (populated
  // by hula at /analytics/config.json). Non-admin callers just see
  // the analytics pages.
  const reportItems = [
    { href: '', label: 'Overview' },
    { href: 'realtime', label: 'Realtime' },
    { href: 'pages', label: 'Pages' },
    { href: 'sources', label: 'Sources' },
    { href: 'geography', label: 'Geography' },
    { href: 'devices', label: 'Devices' },
    { href: 'events', label: 'Events' },
    { href: 'forms', label: 'Forms' },
    { href: 'visitors', label: 'Visitors' },
  ];

  const adminItems = [
    { href: 'admin/users', label: 'Users' },
    { href: 'admin/goals', label: 'Goals' },
    { href: 'admin/reports', label: 'Reports' },
    { href: 'admin/alerts', label: 'Alerts' },
    { href: 'admin/notifications', label: 'Notifications' },
  ];

  let isAdmin = false;
  onMount(() => {
    isAdmin = Boolean(window.hulaConfig?.isAdmin);
  });

  $: currentPath = $page.url.pathname.replace(/\/+$/, '');
  $: baseNoTrailingSlash = base.replace(/\/+$/, '');

  function isActive(href: string): boolean {
    const full = href ? `${baseNoTrailingSlash}/${href}` : baseNoTrailingSlash;
    return currentPath === full;
  }
</script>

<nav class="flex h-full w-56 shrink-0 flex-col border-r bg-background" aria-label="Main">
  <div class="flex h-14 items-center gap-2 border-b px-5">
    <span class="text-lg font-semibold tracking-tight">Hula</span>
    <span class="text-sm text-muted-foreground">analytics</span>
  </div>
  <ul class="flex-1 space-y-0.5 p-3">
    {#each reportItems as item (item.href)}
      <li>
        <a
          href={`${base}/${item.href}`}
          class="flex items-center gap-2 rounded px-3 py-2 text-sm transition hover:bg-accent hover:text-accent-foreground
                 {isActive(item.href) ? 'bg-secondary text-secondary-foreground font-medium' : 'text-muted-foreground'}"
          aria-current={isActive(item.href) ? 'page' : undefined}
        >
          <span class="icon-dot" aria-hidden="true"></span>
          {item.label}
        </a>
      </li>
    {/each}

    {#if isAdmin}
      <li class="mt-5 px-3 text-xs font-medium uppercase tracking-wide text-muted-foreground/70">
        Admin
      </li>
      {#each adminItems as item (item.href)}
        <li>
          <a
            href={`${base}/${item.href}`}
            class="flex items-center gap-2 rounded px-3 py-2 text-sm transition hover:bg-accent hover:text-accent-foreground
                   {isActive(item.href) ? 'bg-secondary text-secondary-foreground font-medium' : 'text-muted-foreground'}"
            aria-current={isActive(item.href) ? 'page' : undefined}
          >
            <span class="icon-dot" aria-hidden="true"></span>
            {item.label}
          </a>
        </li>
      {/each}
    {/if}
  </ul>
  <div class="flex items-center justify-between border-t px-5 py-3 text-xs text-muted-foreground">
    <span>Phase 3</span>
    <button
      type="button"
      class="rounded border px-2 py-1 hover:bg-accent hover:text-accent-foreground"
      on:click={signOut}
    >
      Sign out
    </button>
  </div>
</nav>

<style>
  .icon-dot {
    display: inline-block;
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 9999px;
    background: hsl(var(--muted-foreground) / 0.5);
  }
</style>
