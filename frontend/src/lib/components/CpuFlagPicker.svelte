<script>
  /**
   * CpuFlagPicker — on/auto/off control for QEMU CPUID feature flags.
   *
   * By default, WebKVM passes all host CPU features directly to the guest
   * (host-passthrough), so flags = [] is the optimal and recommended setting.
   *
   * This component presents a clean Automatic State card by default.
   * The manual 3-way toggle pills and search box are only revealed when
   * the operator explicitly chooses to customize CPU flags or when existing
   * overrides are present.
   */
  import { flagState, withFlagState } from '$lib/utils/capabilities.js';
  import { t } from '../i18n.svelte.js';
  import Icon from './Icon.svelte';
  import { Button } from './ui/button';

  let { flags = $bindable([]), available = [], common = [] } = $props();

  let search = $state('');
  let isCustomizing = $state(false);

  // If there are existing flag overrides, open in customizing mode
  $effect(() => {
    if (flags && flags.length > 0) {
      isCustomizing = true;
    }
  });

  const availableSet = $derived(new Set((available || []).map((f) => f.toLowerCase())));
  const isKnown = (name) => availableSet.size === 0 || availableSet.has(name.toLowerCase());

  const filtered = $derived(
    search.trim()
      ? (available || [])
          .filter((f) => f.toLowerCase().includes(search.trim().toLowerCase()))
          .slice(0, 60)
      : []
  );

  const commonNames = $derived(new Set(common.map((f) => f.value.toLowerCase())));
  const customActive = $derived(
    (flags || [])
      .map((f) => f.replace(/^[+-]/, ''))
      .filter((name, i, arr) => arr.indexOf(name) === i)
      .filter((name) => !commonNames.has(name.toLowerCase()))
  );

  function setState(name, state) {
    flags = withFlagState(flags, name, state);
  }

  function cycleLabel(state) {
    if (state === 'on') return t('vmDetail.cpuFlagOn');
    if (state === 'off') return t('vmDetail.cpuFlagOff');
    return t('vmDetail.cpuFlagAuto');
  }

  function resetToAuto() {
    flags = [];
    isCustomizing = false;
    search = '';
  }
</script>

{#snippet pill(name, label, title)}
  {@const state = flagState(flags, name)}
  <div
    class="inline-flex items-stretch rounded-full border overflow-hidden border-border bg-card"
    {title}
  >
    <span
      class="text-xs font-mono px-2.5 py-1 flex items-center {state !== 'auto'
        ? 'bg-muted/80 font-semibold text-foreground'
        : 'text-muted-foreground'}"
    >
      {label}
    </span>
    {#each ['auto', 'on', 'off'] as s (s)}
      <button
        type="button"
        onclick={() => setState(name, s)}
        class="text-[10px] px-2 py-1 border-l border-border transition-colors cursor-pointer {state ===
        s
          ? s === 'on'
            ? 'bg-success text-white font-semibold'
            : s === 'off'
              ? 'bg-destructive text-white font-semibold'
              : 'bg-accent text-white font-semibold'
          : 'bg-card text-muted-foreground hover:bg-muted/40'}"
      >
        {cycleLabel(s)}
      </button>
    {/each}
  </div>
{/snippet}

{#if !isCustomizing && (!flags || flags.length === 0)}
  <!-- Estado Automático Limpio y Tranquilizador -->
  <div
    class="w-full rounded-xl border border-border bg-muted/20 p-3.5 flex flex-wrap items-center justify-between gap-3"
  >
    <div class="flex items-center gap-3 min-w-0 flex-1">
      <div class="p-2 rounded-lg bg-accent/10 text-accent shrink-0">
        <Icon name="cpu" size={18} />
      </div>
      <div class="min-w-0">
        <div class="flex items-center gap-2 flex-wrap">
          <p class="text-xs font-semibold text-foreground">
            {t('vmDetail.cpuFlagsAutoTitle')}
          </p>
          <span
            class="inline-flex items-center px-1.5 py-0.2 rounded-full text-[10px] font-medium bg-success/15 text-success shrink-0"
          >
            {t('vmDetail.cpuFlagAuto')}
          </span>
        </div>
        <p class="text-[11px] text-muted-foreground mt-0.5 leading-relaxed">
          {t('vmDetail.cpuFlagsAutoDesc')}
        </p>
      </div>
    </div>
    <Button
      type="button"
      variant="outline"
      size="sm"
      class="text-xs shrink-0 gap-1.5 cursor-pointer ml-auto sm:ml-0"
      onclick={() => (isCustomizing = true)}
    >
      <Icon name="settings" size={13} />
      <span>{t('vmDetail.cpuFlagsCustomize')}</span>
    </Button>
  </div>
{:else}
  <!-- Modo de Personalización de Flags -->
  <div class="w-full space-y-3 rounded-xl border border-border bg-muted/15 p-3.5">
    <div
      class="flex flex-col sm:flex-row sm:items-center justify-between gap-2 pb-2 border-b border-border/70"
    >
      <div class="flex items-center gap-2 text-xs">
        {#if flags && flags.length > 0}
          <span class="w-2 h-2 rounded-full bg-accent animate-pulse shrink-0"></span>
          <span class="font-medium text-foreground">
            {t('vmDetail.cpuFlagsCustomActiveNotice', { n: flags.length })}
          </span>
        {:else}
          <span class="w-2 h-2 rounded-full bg-muted-foreground shrink-0"></span>
          <span class="text-muted-foreground">
            Modo manual activado (sin anulaciones por ahora)
          </span>
        {/if}
      </div>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        class="text-xs text-accent hover:text-accent/80 hover:bg-accent/10 h-7 self-start sm:self-auto cursor-pointer"
        onclick={resetToAuto}
      >
        <Icon name="refresh" size={12} class="mr-1" />
        {t('vmDetail.cpuFlagsResetAuto')}
      </Button>
    </div>

    <!-- Curated common flags -->
    <div class="space-y-1.5">
      <p class="text-[11px] font-medium text-muted-foreground uppercase tracking-wider">
        Instrucciones comunes
      </p>
      <div class="flex flex-wrap gap-1.5">
        {#each common as flag (flag.value)}
          {@const known = isKnown(flag.value)}
          <div class={known ? '' : 'opacity-60'}>
            {@render pill(
              flag.value,
              flag.label,
              known ? flag.desc : `${flag.desc} — ${t('vmDetail.cpuFlagUnknown')}`
            )}
          </div>
        {/each}
      </div>
    </div>

    {#if customActive.length > 0}
      <div>
        <p class="text-[11px] uppercase tracking-wide text-muted-foreground mb-1">
          {t('vmDetail.cpuFlagActive')}
        </p>
        <div class="flex flex-wrap gap-1.5">
          {#each customActive as name (name)}
            {@render pill(name, name, '')}
          {/each}
        </div>
      </div>
    {/if}

    <div class="pt-1">
      <input
        type="text"
        bind:value={search}
        placeholder={t('vmDetail.cpuFlagSearchPlaceholder')}
        class="input max-w-sm text-xs"
      />
      {#if available && available.length > 0}
        <span class="text-xs text-muted-foreground ml-2">
          {t('vmDetail.cpuFlagCount', { n: available.length }) ||
            `${available.length} flags detected on this host`}
        </span>
      {/if}
    </div>

    {#if search.trim()}
      <div class="flex flex-wrap gap-1.5 max-h-48 overflow-y-auto pr-1">
        {#each filtered as name (name)}
          {@render pill(name, name, '')}
        {:else}
          <p class="text-xs text-muted-foreground">{t('vmDetail.cpuFlagNoMatch')}</p>
        {/each}
      </div>
    {/if}
  </div>
{/if}
