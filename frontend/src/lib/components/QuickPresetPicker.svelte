<script>
  /**
   * QuickPresetPicker — compact button group for common numeric choices (vCPU, RAM, etc).
   */
  import { t } from '$lib/i18n.svelte.js';

  let {
    value = $bindable(),
    options = [],
    formatLabel = null,
    ariaLabel = '',
    onselect = null,
  } = $props();

  const groupLabel = $derived(ariaLabel || t('vmCreate.quickPresetsLabel'));

  function getVal(opt) {
    return opt && typeof opt === 'object' && 'value' in opt ? opt.value : opt;
  }

  function getLabel(opt) {
    if (formatLabel) return formatLabel(opt);
    if (opt && typeof opt === 'object' && 'label' in opt) return opt.label;
    return String(opt);
  }
</script>

<div
  role="group"
  aria-label={groupLabel}
  class="inline-flex rounded-md border border-border p-0.5 bg-muted/40 text-xs shrink-0"
>
  {#each options as opt (getVal(opt))}
    {@const optVal = getVal(opt)}
    {@const isSelected = value === optVal}
    <button
      type="button"
      aria-pressed={isSelected}
      onclick={() => {
        value = optVal;
        if (onselect) onselect(optVal);
      }}
      class="px-2 py-0.5 rounded transition-colors cursor-pointer {isSelected
        ? 'bg-accent text-accent-foreground font-medium shadow-xs'
        : 'text-muted-foreground hover:text-foreground'}"
    >
      {getLabel(opt)}
    </button>
  {/each}
</div>
