<script lang="ts">
  export let error: unknown;
  export let onRetry: (() => void) | null = null;

  $: message = describe(error);

  function describe(e: unknown): string {
    if (!e) return 'Unknown error';
    if (typeof e === 'string') return e;
    if (e instanceof Error) return e.message;
    if (typeof e === 'object' && 'body' in e) {
      const b = (e as { body: unknown }).body;
      if (typeof b === 'string') return b;
      if (b && typeof b === 'object' && 'message' in b) {
        return String((b as { message: unknown }).message);
      }
    }
    return String(e);
  }
</script>

<article class="rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive" role="alert">
  <p class="font-medium">Couldn’t load this view.</p>
  <p class="mt-1 text-destructive/80">{message}</p>
  {#if onRetry}
    <button
      type="button"
      class="mt-2 rounded border border-destructive/40 px-3 py-1 text-xs text-destructive hover:bg-destructive hover:text-destructive-foreground"
      on:click={onRetry}
    >
      Retry
    </button>
  {/if}
</article>
