<script>
  /**
   * EmptyState — illustration + title + description + optional CTA.
   *
   * Replaces the bespoke empty illustrations on VmList, Storage,
   * Networks, Users, etc. Now consistent across the app.
   *
   *   <EmptyState icon="server" title="No VMs yet" description="Import an OVA or create one.">
   *     {#snippet action()}
   *       <Button>Create VM</Button>
   *     {/snippet}
   *   </EmptyState>
   *
   * `icon` accepts any name registered in Icon.svelte (not a fixed
   * enum) — this avoids maintaining a second, parallel icon map
   * that silently falls back to a blank glyph when a name is missing.
   *
   * Pass `compact` for dense contexts (inside a table cell, a small
   * panel section, a details tab) where the full 48px icon box and
   * generous padding would overwhelm the surrounding layout.
   */
  import Icon from './Icon.svelte';

  let { icon = 'search', title, description = '', compact = false, action } = $props();
</script>

{#if compact}
  <div class="flex flex-col items-center justify-center text-center py-6 px-4">
    <Icon name={icon} size={20} class="text-muted-foreground/50 mb-2" />
    <p class="text-sm text-muted-foreground">{title}</p>
    {#if description}
      <p class="text-xs text-muted-foreground/70 mt-1 max-w-xs">{description}</p>
    {/if}
    {#if action}
      <div class="mt-3">
        {@render action()}
      </div>
    {/if}
  </div>
{:else}
  <div class="flex flex-col items-center justify-center text-center py-12 px-4">
    <div
      class="w-12 h-12 rounded-xl bg-gradient-to-br from-accent/20 via-muted to-muted border border-border flex items-center justify-center text-accent mb-4 shadow-sm"
    >
      <Icon name={icon} size={22} />
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
{/if}
