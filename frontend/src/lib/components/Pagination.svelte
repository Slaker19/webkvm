<script>
  /**
   * Pagination — page selector for DataTable (or any list).
   *
   *   <Pagination
   *     total={filtered.length}
   *     pageSize={PAGE_SIZE}
   *     bind:page
   *   />
   */
  // `page` is 0-based. Bind it with `bind:page`, or pass
  // `onpagechange` to be notified without owning the state.
  //
  // onpagechange exists because callers were passing the prop names
  // `current` and `onchange` (which Svelte silently drops) and then
  // wondering why clicking Next never reloaded the data.
  let {
    total = 0,
    pageSize = 10,
    page = $bindable(0),
    itemName = 'item',
    onpagechange = null,
  } = $props();

  // Notify only on a real page change, never on the first render and
  // never while the clamp below is still correcting the index — a
  // callback that re-enters with the same value would loop.
  let lastNotified = $state(-1);
  $effect(() => {
    const p = page;
    if (onpagechange && p !== lastNotified) {
      lastNotified = p;
      onpagechange(p);
    }
  });

  const totalPages = $derived(Math.max(1, Math.ceil(total / pageSize)));
  const start = $derived(total === 0 ? 0 : page * pageSize + 1);
  const end = $derived(Math.min((page + 1) * pageSize, total));

  $effect(() => {
    if (page > totalPages - 1) page = totalPages - 1;
    if (page < 0) page = 0;
  });

  function go(p) {
    if (p < 0 || p >= totalPages) return;
    page = p;
  }
</script>

{#if total > 0}
  <div class="flex items-center justify-between mt-3 text-sm">
    <span class="text-muted-foreground tnum">
      Showing {start}–{end} of {total}
      {itemName}{total !== 1 ? 's' : ''}
    </span>
    <div class="flex items-center gap-1">
      <button
        type="button"
        disabled={page === 0}
        onclick={() => go(page - 1)}
        class="px-2.5 py-1 text-sm rounded border border-border bg-card text-muted-foreground hover:text-foreground hover:bg-muted disabled:opacity-40 disabled:cursor-not-allowed"
      >
        Previous
      </button>
      <span class="px-3 text-muted-foreground tnum">
        {page + 1} / {totalPages}
      </span>
      <button
        type="button"
        disabled={page >= totalPages - 1}
        onclick={() => go(page + 1)}
        class="px-2.5 py-1 text-sm rounded border border-border bg-card text-muted-foreground hover:text-foreground hover:bg-muted disabled:opacity-40 disabled:cursor-not-allowed"
      >
        Next
      </button>
    </div>
  </div>
{/if}
