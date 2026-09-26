<script>
  import { SvelteSet } from 'svelte/reactivity';
  import { cn } from '$lib/utils.js';
  import { t } from '../i18n.svelte.js';
  import TableSkeleton from './TableSkeleton.svelte';
  import EmptyState from './EmptyState.svelte';

  let {
    columns = [], // [{ key, label, align?, width?, minWidth?, class?, headerClass?, sortable?, sortAccessor?, render? }]
    rows = [],
    rowKey = 'id',
    rowClass = '',
    emptyMessage = 'No items',
    emptyIcon = 'search',
    loading = false,
    onRowClick = null,
    // sorting
    sortable = false,
    sortKey = $bindable(null),
    sortDir = $bindable('asc'),
    // selection
    selectable = false,
    selectedKeys = $bindable(new Set()),
    onSelectChange = () => {},
    // pagination
    pageSize = 0, // 0 = show all
    page = $bindable(0),
  } = $props();

  // ---- sort ----
  function toggleSort(col) {
    if (!col.sortable && !sortable) return;
    const key = col.key;
    if (sortKey === key) {
      sortDir = sortDir === 'asc' ? 'desc' : 'asc';
    } else {
      sortKey = key;
      sortDir = 'asc';
    }
  }

  function compare(a, b, col) {
    const acc = col.sortAccessor || ((r) => r[col.key]);
    const va = acc(a);
    const vb = acc(b);
    if (va == null && vb == null) return 0;
    if (va == null) return 1;
    if (vb == null) return -1;
    if (typeof va === 'number' && typeof vb === 'number') {
      return va - vb;
    }
    return String(va).localeCompare(String(vb), undefined, { numeric: true });
  }

  const sortedRows = $derived.by(() => {
    if (!sortKey) return rows;
    const col = columns.find((c) => c.key === sortKey);
    if (!col) return rows;
    const arr = [...rows];
    arr.sort((a, b) => {
      const c = compare(a, b, col);
      return sortDir === 'asc' ? c : -c;
    });
    return arr;
  });

  // ---- pagination ----
  // pageSize = 0 means "show all" (no pagination). Guard the math so an
  // empty table with pageSize 0 can't compute NaN (ceil(0/0)) and write
  // NaN back into `page` forever — that loop trips Svelte's
  // effect_update_depth_exceeded and kills ALL in-page reactivity.
  const totalPages = $derived(
    pageSize > 0 ? Math.max(1, Math.ceil(sortedRows.length / pageSize)) : 1
  );
  const safePage = $derived(Math.max(0, Math.min(page, totalPages - 1)));
  const pagedRows = $derived(
    pageSize > 0 ? sortedRows.slice(safePage * pageSize, (safePage + 1) * pageSize) : sortedRows
  );
  $effect(() => {
    if (page !== safePage) page = safePage;
  });

  // ---- selection ----
  const allSelected = $derived(
    selectable && pagedRows.length > 0 && pagedRows.every((r) => selectedKeys.has(r[rowKey]))
  );
  const someSelected = $derived(selectable && pagedRows.some((r) => selectedKeys.has(r[rowKey])));

  function toggleRow(row, checked) {
    const next = new SvelteSet(selectedKeys);
    if (checked) next.add(row[rowKey]);
    else next.delete(row[rowKey]);
    selectedKeys = next;
    onSelectChange(next);
  }

  function toggleAll(checked) {
    const next = new SvelteSet(selectedKeys);
    for (const r of pagedRows) {
      if (checked) next.add(r[rowKey]);
      else next.delete(r[rowKey]);
    }
    selectedKeys = next;
    onSelectChange(next);
  }

  const checkboxCol = $derived(selectable);
  // The flexible column(s) get a real minimum instead of minmax(0, 1fr):
  // with a 0 floor they collapsed to nothing on a phone (Users' username
  // column vanished, Snapshots' headers overlapped) because the fixed
  // columns alone already filled 400px. With a floor the grid gets wider
  // than the viewport and the wrapper below scrolls horizontally.
  // A column may override the floor with `minWidth`.
  const FR = /^(\d*\.?\d+)fr$/;
  const flexFloor = (c) => c.minWidth || '8rem';
  const gridTemplate = $derived(
    (checkboxCol ? ['40px '] : [])
      .concat(
        columns.map((c) => {
          const w = c.width || '1fr';
          return FR.test(w) ? `minmax(${flexFloor(c)}, ${w})` : w;
        })
      )
      .join(' ')
  );
  // Sum of every column's minimum, applied to the row wrapper so header
  // and rows share one scroll width (and full-width row borders) once the
  // container is narrower than the table can usefully get.
  const minTableWidth = $derived.by(() => {
    const parts = (checkboxCol ? ['40px'] : []).concat(
      columns.map((c) => {
        const w = c.width || '1fr';
        if (FR.test(w)) return flexFloor(c);
        return /^\d+(\.\d+)?(px|rem|em)$/.test(w) ? w : '0px';
      })
    );
    return parts.length ? `calc(${parts.join(' + ')})` : '0px';
  });
</script>

<div class="border border-border rounded-lg overflow-x-auto bg-card">
  <!-- Rows never shrink below the columns' minimums; past that point the
       outer wrapper scrolls horizontally instead of overlapping cells. -->
  <div style="min-width: {minTableWidth};">
    <!-- Header -->
    <div
      class="grid items-center border-b border-border bg-muted/30"
      style="grid-template-columns: {gridTemplate};"
    >
      {#if checkboxCol}
        <div class="px-3 py-2">
          <input
            type="checkbox"
            checked={allSelected}
            indeterminate={!allSelected && someSelected}
            onchange={(e) => toggleAll(e.currentTarget.checked)}
            class="h-4 w-4 rounded border-border bg-background text-accent focus:ring-2 focus:ring-accent/40"
            aria-label={t('common.selectAll')}
          />
        </div>
      {/if}
      {#each columns as col (col)}
        <button
          type="button"
          class={cn(
            'px-3 py-2 text-xs font-medium text-muted-foreground uppercase tracking-wider text-left',
            col.align === 'right' ? 'text-right' : col.align === 'center' ? 'text-center' : '',
            col.headerClass
          )}
          disabled={!col.sortable && !sortable}
          onclick={() => (col.sortable || sortable) && toggleSort(col)}
        >
          <span class="inline-flex items-center gap-1">
            {col.label}
            {#if (col.sortable || sortable) && sortKey === col.key}
              <svg
                class="w-3 h-3"
                fill="none"
                stroke="currentColor"
                stroke-width="2"
                viewBox="0 0 24 24"
              >
                {#if sortDir === 'asc'}
                  <polyline points="6 15 12 9 18 15" />
                {:else}
                  <polyline points="6 9 12 15 18 9" />
                {/if}
              </svg>
            {/if}
          </span>
        </button>
      {/each}
    </div>

    <!-- Rows -->
    {#if loading}
      <TableSkeleton {columns} rows={Math.min(pageSize || 5, 8)} selectable={checkboxCol} />
    {:else if pagedRows.length === 0}
      <EmptyState compact icon={emptyIcon} title={emptyMessage} />
    {:else}
      {#each pagedRows as row, _i (row[rowKey])}
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <div
          class={cn(
            'grid items-center border-b border-border last:border-0 transition-colors group',
            onRowClick ? 'cursor-pointer hover:bg-muted/40' : '',
            rowClass
          )}
          style="grid-template-columns: {gridTemplate};"
          onclick={() => onRowClick?.(row)}
          role={onRowClick ? 'button' : undefined}
          tabindex={onRowClick ? 0 : undefined}
          aria-label={onRowClick ? `Open ${row[rowKey]}` : undefined}
          onkeydown={(e) => {
            if (onRowClick && (e.key === 'Enter' || e.key === ' ')) {
              e.preventDefault();
              onRowClick(row);
            }
          }}
        >
          {#if checkboxCol}
            <div
              class="px-3 py-2.5"
              role="presentation"
              onclick={(e) => e.stopPropagation()}
              onkeydown={(e) => e.stopPropagation()}
            >
              <input
                type="checkbox"
                checked={selectedKeys.has(row[rowKey])}
                onchange={(e) => toggleRow(row, e.currentTarget.checked)}
                class="h-4 w-4 rounded border-border bg-background text-accent focus:ring-2 focus:ring-accent/40"
                aria-label="Select {row[rowKey]}"
              />
            </div>
          {/if}
          {#each columns as col (col)}
            <div
              class={cn(
                'px-3 py-2.5 text-sm min-w-0',
                col.align === 'right' ? 'text-right' : col.align === 'center' ? 'text-center' : '',
                col.class
              )}
            >
              {#if col.render}
                {@render col.render(row)}
              {:else}
                <span class="truncate block">{row[col.key] ?? ''}</span>
              {/if}
            </div>
          {/each}
        </div>
      {/each}
    {/if}
  </div>
</div>
