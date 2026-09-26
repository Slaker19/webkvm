<script>
  /**
   * CpuPrioritySelector — intuitive CPU cgroups share controller.
   *
   * Hides raw cgroup weight numbers (1024) behind clear human presets:
   * - Normal / Automatic (1024): Standard balanced Linux scheduling.
   * - Low (512): Yields CPU time to other VMs under heavy load.
   * - High (2048): Higher priority when physical CPU is saturated.
   * - Custom: Direct numeric input (100 - 500000) for edge cases.
   */
  import { t } from '../i18n.svelte.js';

  let { value = $bindable(1024), disabled = false } = $props();

  let isCustom = $state(false);

  // Sync isCustom when value changes externally to non-standard preset
  $effect(() => {
    if (value !== 1024 && value !== 512 && value !== 2048) {
      isCustom = true;
    }
  });

  const selectedPreset = $derived.by(() => {
    if (isCustom) return 'custom';
    if (value === 512) return 'low';
    if (value === 2048) return 'high';
    return 'normal';
  });

  function selectPreset(preset) {
    if (disabled) return;
    if (preset === 'normal') {
      isCustom = false;
      value = 1024;
    } else if (preset === 'low') {
      isCustom = false;
      value = 512;
    } else if (preset === 'high') {
      isCustom = false;
      value = 2048;
    } else if (preset === 'custom') {
      isCustom = true;
      if (!value || value <= 0) value = 1024;
    }
  }
</script>

<div class="space-y-1.5 w-full">
  <!-- Presets Pills -->
  <div class="inline-flex flex-wrap p-0.5 bg-muted/60 rounded-lg border border-border gap-0.5">
    <button
      type="button"
      {disabled}
      onclick={() => selectPreset('normal')}
      class="px-2.5 py-1 text-xs font-medium rounded-md transition-all cursor-pointer {selectedPreset ===
      'normal'
        ? 'bg-background text-foreground shadow-xs font-semibold'
        : 'text-muted-foreground hover:text-foreground'}"
    >
      {t('vmDetail.cpuPriorityNormal')}
    </button>
    <button
      type="button"
      {disabled}
      onclick={() => selectPreset('low')}
      class="px-2.5 py-1 text-xs font-medium rounded-md transition-all cursor-pointer {selectedPreset ===
      'low'
        ? 'bg-background text-foreground shadow-xs font-semibold'
        : 'text-muted-foreground hover:text-foreground'}"
    >
      {t('vmDetail.cpuPriorityLow')}
    </button>
    <button
      type="button"
      {disabled}
      onclick={() => selectPreset('high')}
      class="px-2.5 py-1 text-xs font-medium rounded-md transition-all cursor-pointer {selectedPreset ===
      'high'
        ? 'bg-background text-foreground shadow-xs font-semibold'
        : 'text-muted-foreground hover:text-foreground'}"
    >
      {t('vmDetail.cpuPriorityHigh')}
    </button>
    <button
      type="button"
      {disabled}
      onclick={() => selectPreset('custom')}
      class="px-2.5 py-1 text-xs font-medium rounded-md transition-all cursor-pointer {selectedPreset ===
      'custom'
        ? 'bg-background text-foreground shadow-xs font-semibold'
        : 'text-muted-foreground hover:text-foreground'}"
    >
      {t('vmDetail.cpuPriorityCustom')}
    </button>
  </div>

  <!-- Description / Helper per selected mode -->
  <div class="text-xs text-muted-foreground">
    {#if selectedPreset === 'normal'}
      <p class="flex items-center gap-1.5 text-foreground/80">
        <span class="inline-block w-2 h-2 rounded-full bg-success"></span>
        {t('vmDetail.cpuPriorityNormalDesc')}
      </p>
    {:else if selectedPreset === 'low'}
      <p class="flex items-center gap-1.5 text-foreground/80">
        <span class="inline-block w-2 h-2 rounded-full bg-warning"></span>
        {t('vmDetail.cpuPriorityLowDesc')}
      </p>
    {:else if selectedPreset === 'high'}
      <p class="flex items-center gap-1.5 text-foreground/80">
        <span class="inline-block w-2 h-2 rounded-full bg-accent"></span>
        {t('vmDetail.cpuPriorityHighDesc')}
      </p>
    {:else}
      <div class="flex items-center gap-2 pt-1">
        <input
          type="number"
          bind:value
          min="100"
          max="500000"
          step="128"
          class="input w-28 tnum"
          {disabled}
        />
        <span class="text-[11px] text-muted-foreground">
          {t('vmDetail.cpuPriorityCustomShares')}
        </span>
      </div>
    {/if}
  </div>
</div>
