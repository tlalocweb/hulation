<script lang="ts">
  import { page } from '$app/stores';
  import { base } from '$app/paths';

  // Left nav. Kept small; stage 2.2 ships five reports, Phases 3–4
  // append admin + realtime/visitor/events/forms items into the same
  // structure.
  const items = [
    { href: '', label: 'Overview', icon: 'dashboard' },
    { href: 'pages', label: 'Pages', icon: 'document' },
    { href: 'sources', label: 'Sources', icon: 'link' },
    { href: 'geography', label: 'Geography', icon: 'globe' },
    { href: 'devices', label: 'Devices', icon: 'device' },
  ];

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
    {#each items as item (item.href)}
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
  </ul>
  <div class="border-t px-5 py-3 text-xs text-muted-foreground">
    <p>Phase 2 · {$page.url.pathname}</p>
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
