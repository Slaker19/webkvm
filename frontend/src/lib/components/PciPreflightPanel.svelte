<script>
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let { info = null, loading = false, onrefresh = null } = $props();

  let expanded = $state(false);

  const grubParam = $derived(
    info?.iommu_vendor === 'intel' ? 'intel_iommu=on iommu=pt' : 'amd_iommu=on iommu=pt'
  );

  function formatGroupsCount(n) {
    return n === 1
      ? t('vmDetail.pciPreflightGroupsCountOne', { n: 1 })
      : t('vmDetail.pciPreflightGroupsCountOther', { n });
  }

  function formatAssignable(n) {
    return n === 1
      ? t('vmDetail.pciPreflightAssignableOne', { n: 1 })
      : t('vmDetail.pciPreflightAssignableOther', { n });
  }
</script>

{#if info}
  <div class="rounded-xl border border-border bg-card/60 overflow-hidden text-xs">
    <div class="flex items-center justify-between p-3 gap-2 bg-muted/20 border-b border-border/50">
      <div class="flex items-center gap-2 flex-wrap min-w-0">
        <Icon name="circuitBoard" size={15} class="text-accent shrink-0" />
        <span class="font-semibold text-foreground">{t('vmDetail.pciPreflightTitle')}</span>

        <!-- IOMMU status badge -->
        <span
          class="px-2 py-0.5 rounded-full font-mono text-[11px] font-medium border {info.iommu_enabled
            ? 'bg-success/15 text-success border-success/30'
            : 'bg-destructive/15 text-destructive border-destructive/30'}"
        >
          {t('vmDetail.pciPreflightIommu')}: {info.iommu_enabled
            ? info.iommu_vendor === 'amd'
              ? 'AMD-Vi'
              : info.iommu_vendor === 'intel'
                ? 'Intel VT-d'
                : t('status.platformEnabled')
            : t('status.platformDisabled')}
        </span>

        <!-- Mode badge -->
        {#if info.iommu_enabled}
          <span
            class="px-2 py-0.5 rounded-full font-mono text-[11px] border border-border bg-muted/40 text-muted-foreground"
          >
            {t('vmDetail.pciPreflightMode')}: {info.iommu_mode === 'passthrough'
              ? t('vmDetail.pciPreflightModePassthrough')
              : t('vmDetail.pciPreflightModeTranslated')}
          </span>
        {/if}

        <!-- Quick count of assignable -->
        {#if info.iommu_enabled}
          <span
            class="px-2 py-0.5 rounded-full text-[11px] font-medium border {info.groups_assignable >
            0
              ? 'bg-accent/15 text-accent border-accent/30'
              : 'bg-muted/40 text-muted-foreground border-border'}"
          >
            {formatAssignable(info.groups_assignable)}
          </span>
        {/if}
      </div>

      <div class="flex items-center gap-1.5 shrink-0 ml-auto">
        {#if onrefresh}
          <button
            type="button"
            onclick={onrefresh}
            disabled={loading}
            title={t('common.refresh')}
            class="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground cursor-pointer disabled:opacity-50"
          >
            {#if loading}
              <Spinner size="xs" />
            {:else}
              <Icon name="refreshCw" size={13} />
            {/if}
          </button>
        {/if}
        <button
          type="button"
          onclick={() => (expanded = !expanded)}
          class="flex items-center gap-1 px-2 py-1 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted font-medium cursor-pointer"
        >
          <span>{expanded ? t('common.collapse') : t('common.expand')}</span>
          <Icon name={expanded ? 'chevronUp' : 'chevronDown'} size={13} />
        </button>
      </div>
    </div>

    {#if expanded}
      <div class="p-3.5 space-y-3">
        {#if !info.iommu_enabled}
          <div
            class="p-3 rounded-lg border border-destructive/30 bg-destructive/10 text-destructive text-xs space-y-1"
          >
            <div class="flex items-center gap-1.5 font-semibold">
              <Icon name="alertTriangle" size={14} class="shrink-0" />
              <span>{t('vmDetail.pciHint')}</span>
            </div>
            <p>
              {t('vmDetail.pciPreflightIommuDisabledHelp', { param: grubParam })}
            </p>
          </div>
        {/if}

        <div class="grid grid-cols-2 sm:grid-cols-4 gap-2 text-xs">
          <!-- Total groups -->
          <div class="p-2.5 rounded-lg border border-border/70 bg-muted/15">
            <div class="text-muted-foreground text-[11px] mb-0.5">
              {t('vmDetail.pciPreflightTotalLabel')}
            </div>
            <div class="font-semibold text-foreground text-sm">
              {formatGroupsCount(info.groups_total)}
            </div>
          </div>

          <!-- Assignable groups -->
          <div class="p-2.5 rounded-lg border border-border/70 bg-muted/15">
            <div class="text-muted-foreground text-[11px] mb-0.5">
              {t('vmDetail.pciPreflightAssignableLabel')}
            </div>
            <div class="font-semibold text-accent text-sm">
              {formatGroupsCount(info.groups_assignable)}
            </div>
          </div>

          <!-- Host critical groups -->
          <div class="p-2.5 rounded-lg border border-border/70 bg-muted/15">
            <div class="text-muted-foreground text-[11px] mb-0.5">
              {t('vmDetail.pciPreflightCriticalLabel')}
            </div>
            <div class="font-semibold text-warning text-sm">
              {formatGroupsCount(info.groups_host_critical)}
            </div>
          </div>

          <!-- Bridge / System groups -->
          <div class="p-2.5 rounded-lg border border-border/70 bg-muted/15">
            <div class="text-muted-foreground text-[11px] mb-0.5">
              {t('vmDetail.pciPreflightBridgeLabel')}
            </div>
            <div class="font-semibold text-muted-foreground text-sm">
              {formatGroupsCount(info.groups_bridge_only)}
            </div>
          </div>
        </div>

        <!-- VFIO driver details -->
        <div
          class="flex items-center justify-between text-xs pt-1 border-t border-border/40 text-muted-foreground"
        >
          <span>{t('vmDetail.pciPreflightVfio')}:</span>
          <span class="font-mono text-foreground">
            {info.vfio_module_loaded
              ? t('vmDetail.pciPreflightVfioLoaded')
              : info.vfio_available
                ? t('vmDetail.pciPreflightVfioAvailable')
                : t('vmDetail.pciPreflightVfioNotLoaded')}
          </span>
        </div>
      </div>
    {/if}
  </div>
{/if}
