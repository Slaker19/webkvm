<script>
  /**
   * TableSkeleton — loading placeholder shaped like DataTable's grid rows.
   *
   * Mirrors the real column widths so the transition from skeleton to
   * data doesn't cause layout shift. Used internally by DataTable when
   * `loading` is true; can also be dropped in standalone above a plain
   * `<table>` (e.g. Firewall.svelte) before the real rows are rendered.
   *
   *   <TableSkeleton columns={[{width: '1fr'}, {width: '120px'}]} rows={5} />
   */
  let { columns = [], rows = 5, selectable = false } = $props();

  const gridTemplate = $derived(
    (selectable ? ['40px '] : [])
      .concat((columns.length ? columns : [{}, {}, {}]).map((c) => c.width || '1fr'))
      .join(' ')
  );

  const colCount = $derived(columns.length || 3);
</script>

<div role="presentation" aria-hidden="true">
  {#each Array(rows) as _, r (r)}
    <div
      class="grid items-center gap-3 border-b border-border last:border-0 px-3 py-3"
      style="grid-template-columns: {gridTemplate};"
    >
      {#if selectable}
        <div class="h-4 w-4 rounded bg-muted animate-pulse"></div>
      {/if}
      {#each Array(colCount) as _, c (c)}
        <div
          class="h-3.5 rounded bg-muted animate-pulse"
          style="width: {c === 0 ? '70%' : '45%'}"
        ></div>
      {/each}
    </div>
  {/each}
</div>
