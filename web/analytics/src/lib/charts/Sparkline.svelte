<script lang="ts">
  import { buildSparkline } from './utils';
  import { colorFor } from './palette';

  /** Numeric samples to draw. No dates required — the sparkline
   * assumes even spacing. Empty / single-value arrays render blank. */
  export let values: number[] = [];
  export let width = 120;
  export let height = 28;
  export let colorIndex = 0;
  export let strokeWidth = 1.5;

  $: d = buildSparkline(values, { width, height });
</script>

<svg
  class="inline-block align-middle"
  role="img"
  aria-label="Sparkline"
  {width}
  {height}
  viewBox={`0 0 ${width} ${height}`}
>
  <path
    d={d}
    fill="none"
    stroke={colorFor(colorIndex)}
    stroke-width={strokeWidth}
    stroke-linejoin="round"
    stroke-linecap="round"
  />
</svg>
