<script lang="ts">
  import { createEventDispatcher } from 'svelte';

  /** Two-way open state. Parent binds via `bind:open`. */
  export let open = false;
  /** Sheet width. Accepts any Tailwind width class (e.g., 'w-96'). */
  export let width: string = 'w-96';

  const dispatch = createEventDispatcher<{ close: void }>();

  function close() {
    open = false;
    dispatch('close');
  }

  function onBackdropClick(e: MouseEvent) {
    if (e.target === e.currentTarget) close();
  }
</script>

{#if open}
  <div
    class="fixed inset-0 z-50 bg-foreground/30 backdrop-blur-[1px]"
    role="presentation"
    on:click={onBackdropClick}
    on:keydown={(e) => {
      if (e.key === 'Escape') close();
    }}
  >
    <aside
      class="absolute right-0 top-0 h-full {width} overflow-y-auto border-l bg-background p-5 shadow-lg"
      role="dialog"
      aria-modal="true"
    >
      <slot {close} />
    </aside>
  </div>
{/if}
