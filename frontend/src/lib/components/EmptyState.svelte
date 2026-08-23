<script>
  /**
   * EmptyState — illustration + title + description + optional CTA.
   *
   * Replaces the bespoke empty illustrations on VmList, Storage,
   * Networks, Users, etc. Now consistent across the app.
   *
   *   <EmptyState title="No VMs yet" description="Import an OVA or create one.">
   *     {#snippet action()}
   *       <Button>Create VM</Button>
   *     {/snippet}
   *   </EmptyState>
   */
  let {
    icon = 'empty', // 'empty' | 'search' | 'server' | 'disk' | 'network'
    title,
    description = '',
    action,
  } = $props();

  const iconPath = $derived(
    {
      empty:
        'M21 21l-4.35-4.35M11 18a7 7 0 1 1 14 0 7 7 0 0 1-14 0M3 12a9 9 0 1 1 18 0 9 9 0 0 1-18 0M3 12V6a3 3 0 0 1 3-3h6',
      search: 'M21 21l-4.35-4.35M11 18a7 7 0 1 1 14 0 7 7 0 0 1-14 0',
      server:
        'M3 7h18v4H3zM3 13h18v4H3zM3 5v2M3 11v2M3 17v2M21 5v2M21 11v2M21 17v2M7 9h.01M7 15h.01M11 9h6M11 15h6',
      disk: 'M21 12V7a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-3M16 12h6M19 9l3 3-3 3',
      network: 'M12 2l3 7h6l-5 4 2 8-6-4-6 4 2-8-5-4h6z',
    }[icon] || ''
  );
</script>

<div class="flex flex-col items-center justify-center text-center py-12 px-4">
  <div
    class="w-12 h-12 rounded-xl bg-gradient-to-br from-accent/20 via-muted to-muted border border-border flex items-center justify-center text-accent mb-4 shadow-sm"
  >
    <svg class="w-6 h-6" fill="none" stroke="currentColor" stroke-width="1.5" viewBox="0 0 32 32">
      <path d={iconPath} />
    </svg>
  </div>
  <h3 class="text-sm font-semibold mb-1">{title}</h3>
  {#if description}
    <p class="text-sm text-muted-foreground max-w-sm">{description}</p>
  {/if}
  {#if action}
    <div class="mt-4">
      {@render action()}
    </div>
  {/if}
</div>
