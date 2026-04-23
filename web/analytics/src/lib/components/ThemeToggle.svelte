<script lang="ts">
  import { theme, toggleTheme } from '$lib/theme';
  $: current = $theme;
</script>

<button
  type="button"
  class="rounded border bg-background px-3 py-1.5 text-sm text-muted-foreground hover:text-foreground"
  aria-label={`Theme: ${current}. Click to cycle.`}
  on:click={toggleTheme}
>
  {#if current === 'dark'}
    dark
  {:else if current === 'light'}
    light
  {:else}
    system
  {/if}
</button>
