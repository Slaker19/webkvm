<script>
  /**
   * Tabs — minimal accessible tab strip.
   *
   * Replaces the hand-rolled tab buttons in VmDetail's Identity
   * dialog. Each tab has an `id`, `label`, and an optional `icon`.
   * `active` is bindable.
   *
   *   <Tabs
   *     tabs={[
   *       { id: 'alias', label: 'Alias' },
   *       { id: 'network', label: 'Network' },
   *     ]}
   *     bind:active={identityTab}
   *   />
   */
  import { cn } from '$lib/utils.js';

  let {
    tabs, // [{ id, label, icon?, badge? }]
    active = $bindable(''),
    variant = 'underline', // underline | pill
    class: className = '',
  } = $props();

  const variantClass = $derived(
    variant === 'pill' ? 'border border-border rounded-lg p-0.5 bg-card' : 'border-b border-border'
  );
</script>

<div
  role="tablist"
  class={cn('flex items-center gap-0.5 overflow-x-auto', variantClass, className)}
>
  {#each tabs as tab (tab.id)}
    {@const isActive = active === tab.id}
    <button
      type="button"
      role="tab"
      aria-selected={isActive}
      onclick={() => (active = tab.id)}
      class={cn(
        'px-3 py-1.5 text-sm font-medium transition-colors whitespace-nowrap',
        variant === 'pill'
          ? isActive
            ? 'bg-accent/15 text-accent rounded'
            : 'text-muted-foreground hover:text-foreground rounded'
          : isActive
            ? 'text-accent border-b-2 border-accent -mb-px'
            : 'text-muted-foreground hover:text-foreground border-b-2 border-transparent -mb-px'
      )}
    >
      {tab.label}
      {#if tab.badge != null}
        <span class="ml-1.5 text-xs px-1.5 py-0.5 rounded bg-muted text-muted-foreground tnum"
          >{tab.badge}</span
        >
      {/if}
    </button>
  {/each}
</div>
