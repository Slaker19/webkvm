<script>
  /**
   * Collapsible card shell used by the VM detail blocks.
   *
   * Chevron minimizes: body is CSS-hidden (NOT unmounted) so live
   * connections inside (VNC iframe, serial WebSocket) stay alive.
   */
  import { layout, toggleCollapsed } from '$lib/stores/vmLayout.svelte.js';
  import { ChevronDown, ChevronRight } from '@lucide/svelte';

  let { bid, title, children } = $props();

  const collapsed = $derived(!!layout.collapsed[bid]);
</script>

<section class="border border-border rounded-lg bg-card p-5 scroll-mt-6">
  <div class="flex items-center justify-between mb-3 gap-2">
    <h2 class="text-sm font-semibold uppercase tracking-wider text-muted-foreground truncate">
      {title}
    </h2>
    <button
      type="button"
      class="p-1 rounded hover:bg-muted text-muted-foreground hover:text-foreground shrink-0"
      title={collapsed ? 'Maximizar' : 'Minimizar'}
      onclick={() => toggleCollapsed(bid)}
    >
      {#if collapsed}<ChevronRight class="w-4 h-4" />{:else}<ChevronDown class="w-4 h-4" />{/if}
    </button>
  </div>

  <!-- Body kept mounted while minimized (CSS-hidden) so VNC/WS survive -->
  <div class={collapsed ? 'hidden' : ''}>
    {@render children?.()}
  </div>
</section>
