<script lang="ts">
  // Devices placeholder — three Donuts land in stage 2.7.
</script>

<section class="space-y-4">
  <header>
    <h1 class="text-2xl font-semibold tracking-tight">Devices</h1>
    <p class="text-sm text-muted-foreground">
      Device category · Browser · OS · stage 2.7
    </p>
  </header>

  <div class="grid grid-cols-1 gap-4 md:grid-cols-3">
    {#each ['Device category', 'Browser', 'Operating system'] as label}
      <article class="rounded-lg border bg-card p-5 text-card-foreground">
        <h2 class="mb-2 text-base font-medium">{label}</h2>
        <div class="flex h-48 items-center justify-center text-sm text-muted-foreground">
          Donut placeholder
        </div>
      </article>
    {/each}
  </div>
</section>
