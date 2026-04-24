<script lang="ts" context="module">
  export interface Column<T> {
    /** Proto field name (also used as default header). */
    key: keyof T & string;
    /** Human-readable header. Falls back to `key`. */
    label?: string;
    /** Formatter — number, percent, duration, etc. */
    format?: (value: unknown, row: T) => string;
    /** Right-align numeric columns. */
    align?: 'left' | 'right';
    /** Display-only columns aren't sortable. */
    sortable?: boolean;
  }
</script>

<script lang="ts" generics="T">
  import { createEventDispatcher } from 'svelte';

  export let rows: T[] = [];
  export let columns: Column<T>[];
  /** Page size. 0 disables pagination. */
  export let pageSize = 50;
  /** Called when a header is clicked. Default: toggle sort direction on that column. */
  export let initialSort: { key: keyof T & string; dir: 'asc' | 'desc' } | null = null;
  export let loading = false;
  export let onExportCsv: (() => void) | null = null;
  export let emptyMessage = 'No rows for this filter.';

  const dispatch = createEventDispatcher<{ rowClick: { row: T } }>();

  let sort = initialSort;
  let page = 0;

  $: sortedRows = sort
    ? [...rows].sort((a, b) => {
        const av = (a as Record<string, unknown>)[sort!.key as string];
        const bv = (b as Record<string, unknown>)[sort!.key as string];
        const direction = sort!.dir === 'asc' ? 1 : -1;
        if (typeof av === 'number' && typeof bv === 'number') return (av - bv) * direction;
        return String(av ?? '').localeCompare(String(bv ?? '')) * direction;
      })
    : rows;

  $: pageCount = pageSize > 0 ? Math.max(1, Math.ceil(sortedRows.length / pageSize)) : 1;
  $: pagedRows = pageSize > 0 ? sortedRows.slice(page * pageSize, (page + 1) * pageSize) : sortedRows;

  function onHeaderClick(col: Column<T>) {
    if (col.sortable === false) return;
    if (sort?.key === col.key) {
      sort = { key: col.key, dir: sort.dir === 'asc' ? 'desc' : 'asc' };
    } else {
      sort = { key: col.key, dir: 'desc' };
    }
    page = 0;
  }

  function cellText(row: T, col: Column<T>): string {
    const v = (row as Record<string, unknown>)[col.key as string];
    if (col.format) return col.format(v, row);
    if (v === null || v === undefined) return '—';
    return String(v);
  }

  /** Stable key for an {#each} block — first-column value if present,
   * otherwise the row index. Kept as a helper because Svelte template
   * expressions can't contain `as` assertions. */
  function rowKey(row: T, idx: number): string | number {
    const k = (row as Record<string, unknown>)[columns[0].key as string];
    return (typeof k === 'string' || typeof k === 'number') ? k : idx;
  }
</script>

<div class="space-y-3">
  {#if onExportCsv}
    <div class="flex justify-end">
      <button
        type="button"
        class="rounded border bg-background px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground"
        on:click={onExportCsv}
      >
        Download CSV
      </button>
    </div>
  {/if}

  <div class="overflow-x-auto rounded-lg border bg-card">
    <table class="min-w-full text-sm">
      <thead class="sticky top-0 border-b bg-card">
        <tr>
          {#each columns as col (col.key)}
            <th
              scope="col"
              class="px-3 py-2 font-medium text-muted-foreground
                     {col.align === 'right' ? 'text-right' : 'text-left'}
                     {col.sortable !== false ? 'cursor-pointer select-none hover:text-foreground' : ''}"
              on:click={() => onHeaderClick(col)}
              on:keydown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') onHeaderClick(col);
              }}
              tabindex={col.sortable !== false ? 0 : -1}
              aria-sort={sort?.key === col.key ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'}
            >
              {col.label ?? col.key}
              {#if sort?.key === col.key}
                <span aria-hidden="true" class="text-xs">
                  {sort.dir === 'asc' ? '▲' : '▼'}
                </span>
              {/if}
            </th>
          {/each}
        </tr>
      </thead>
      <tbody>
        {#if loading}
          {#each Array.from({ length: 5 }) as _}
            <tr>
              {#each columns as col (col.key)}
                <td class="px-3 py-2">
                  <div class="h-4 w-16 animate-pulse rounded bg-muted"></div>
                </td>
              {/each}
            </tr>
          {/each}
        {:else if pagedRows.length === 0}
          <tr>
            <td colspan={columns.length} class="px-3 py-12 text-center text-muted-foreground">
              {emptyMessage}
            </td>
          </tr>
        {:else}
          {#each pagedRows as row, rowIdx (rowKey(row, rowIdx))}
            <tr
              class="cursor-pointer border-t transition-colors hover:bg-accent/50"
              on:click={() => dispatch('rowClick', { row })}
              on:keydown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') dispatch('rowClick', { row });
              }}
              tabindex="0"
            >
              {#each columns as col (col.key)}
                <td
                  class="px-3 py-2 {col.align === 'right' ? 'text-right tabular-nums' : ''}"
                >
                  {cellText(row, col)}
                </td>
              {/each}
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>
  </div>

  {#if pageSize > 0 && pageCount > 1}
    <div class="flex items-center justify-end gap-2 text-xs text-muted-foreground">
      <button
        type="button"
        class="rounded border px-2 py-1 disabled:opacity-50"
        on:click={() => (page = Math.max(0, page - 1))}
        disabled={page === 0}
      >
        ← Prev
      </button>
      <span>Page {page + 1} of {pageCount}</span>
      <button
        type="button"
        class="rounded border px-2 py-1 disabled:opacity-50"
        on:click={() => (page = Math.min(pageCount - 1, page + 1))}
        disabled={page >= pageCount - 1}
      >
        Next →
      </button>
    </div>
  {/if}
</div>
