<script>
  /**
   * Reorderable / collapsible card shell used by the VM detail blocks.
   *
   * - Drag the header grip to reorder (HTML5 DnD, desktop).
   * - ▲ ▼ ⤒ arrows move it a step / to the top.
   * - Chevron minimizes: body is CSS-hidden (NOT unmounted) so live
   *   connections inside (VNC iframe, serial WebSocket) stay alive.
   */
  import {
    layout,
    moveBlock,
    blockToTop,
    toggleCollapsed,
    dragStart,
    dragEnd,
    dragOver,
    dropCommit,
  } from '$lib/stores/vmLayout.svelte.js';
  import {
    ChevronUp,
    ChevronDown,
    ChevronRight,
    ArrowUpToLine,
    GripVertical,
  } from '@lucide/svelte';

  let { bid, title, children } = $props();

  const collapsed = $derived(!!layout.collapsed[bid]);
  const isDragging = $derived(layout.dragging === bid);
  // Insertion indicator when another block hovers over us.
  const dropPos = $derived(layout.drop?.over === bid ? layout.drop.pos : null);

  function handleDragStart(e) {
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', bid);
    dragStart(bid);
  }
  function handleDragEnd() {
    dragEnd();
  }
  function handleDragOver(e) {
    if (!layout.dragging || layout.dragging === bid) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    const r = e.currentTarget.getBoundingClientRect();
    dragOver(bid, e.clientY < r.top + r.height / 2 ? 'before' : 'after');
  }
</script>

<section
  class="border border-border rounded-lg bg-card p-5 scroll-mt-6 transition-opacity
         {(isDragging && 'opacity-40') || ''}
         {dropPos === 'before' ? 'border-t-2 border-t-primary' : ''}
         {dropPos === 'after' ? 'border-b-2 border-b-primary' : ''}"
  ondragover={handleDragOver}
  ondrop={(e) => {
    e.preventDefault();
    dropCommit();
  }}
>
  <div class="flex items-center justify-between mb-3 gap-2">
    <div class="flex items-center gap-1.5 min-w-0">
      <span
        class="cursor-grab active:cursor-grabbing text-muted-foreground/60 hover:text-muted-foreground p-0.5"
        draggable="true"
        ondragstart={handleDragStart}
        ondragend={handleDragEnd}
        title="Arrastra para reordenar"
      >
        <GripVertical class="w-4 h-4" />
      </span>
      <h2 class="text-sm font-semibold uppercase tracking-wider text-muted-foreground truncate">
        {title}
      </h2>
    </div>
    <div class="flex items-center gap-0.5 shrink-0">
      <button
        type="button"
        class="p-1 rounded hover:bg-muted text-muted-foreground hover:text-foreground disabled:opacity-30"
        title="Llevar arriba"
        onclick={() => blockToTop(bid)}
      >
        <ArrowUpToLine class="w-3.5 h-3.5" />
      </button>
      <button
        type="button"
        class="p-1 rounded hover:bg-muted text-muted-foreground hover:text-foreground"
        title="Subir"
        onclick={() => moveBlock(bid, -1)}
      >
        <ChevronUp class="w-4 h-4" />
      </button>
      <button
        type="button"
        class="p-1 rounded hover:bg-muted text-muted-foreground hover:text-foreground"
        title="Bajar"
        onclick={() => moveBlock(bid, 1)}
      >
        <ChevronDown class="w-4 h-4" />
      </button>
      <button
        type="button"
        class="p-1 rounded hover:bg-muted text-muted-foreground hover:text-foreground"
        title={collapsed ? 'Maximizar' : 'Minimizar'}
        onclick={() => toggleCollapsed(bid)}
      >
        {#if collapsed}<ChevronRight class="w-4 h-4" />{:else}<ChevronDown class="w-4 h-4" />{/if}
      </button>
    </div>
  </div>

  <!-- Body kept mounted while minimized (CSS-hidden) so VNC/WS survive -->
  <div class={collapsed ? 'hidden' : ''}>
    {@render children?.()}
  </div>
</section>
