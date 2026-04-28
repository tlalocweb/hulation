<script lang="ts">
  import ServerSelect from './ServerSelect.svelte';
  import DateRangePicker from './DateRangePicker.svelte';
  import CompareToggle from './CompareToggle.svelte';
  import FilterChips from './FilterChips.svelte';
  import ThemeToggle from './ThemeToggle.svelte';
</script>

<header
  class="sticky top-0 z-20 flex flex-col gap-2 border-b bg-background/95 px-6 py-3 backdrop-blur"
  aria-label="Filter bar"
>
  <div class="flex flex-wrap items-center gap-3">
    <ServerSelect />
    <DateRangePicker />
    <CompareToggle />
    <div class="flex-1"></div>
    <ThemeToggle />
  </div>
  <FilterChips />
</header>
