<script>
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let { group, vmState = 'running', loading = false, onattach = null } = $props();

  const isShutoff = $derived(vmState === 'shutoff');
  const isContaminated = $derived(group.devices && group.devices.length > 1);

  function getDeviceBadge(dev) {
    if (dev.boot_vga) {
      return { text: t('vmDetail.pciBootVGA'), variant: 'destructive' };
    }
    if (dev.block_reason === 'host_root_disk') {
      return { text: t('vmDetail.pciHostRootDisk'), variant: 'destructive' };
    }
    if (dev.block_reason === 'host_uplink') {
      return { text: t('vmDetail.pciHostUplink'), variant: 'destructive' };
    }
    if (dev.block_reason === 'mounted_storage') {
      return { text: t('vmDetail.pciMountedStorage'), variant: 'warning' };
    }
    if (dev.in_use) {
      return { text: t('vmDetail.pciInUse'), variant: 'muted' };
    }
    if (dev.block_reason === 'pci_bridge') {
      return { text: t('vmDetail.pciBridgeDevice'), variant: 'muted' };
    }
    if (dev.vfio_bound) {
      return { text: t('vmDetail.pciVfioBoundBadge'), variant: 'success' };
    }
    return null;
  }

  const blockReasonText = $derived.by(() => {
    if (group.assignable) return '';
    const r = group.block_reason;
    if (r === 'boot_vga') return t('vmDetail.pciBootVGA');
    if (r === 'host_root_disk') return t('vmDetail.pciHostRootDisk');
    if (r === 'host_uplink') return t('vmDetail.pciHostUplink');
    if (r === 'mounted_storage') return t('vmDetail.pciMountedStorage');
    if (r === 'pci_bridge') return t('vmDetail.pciBridgeDevice');
    if (r === 'in_use') return t('vmDetail.pciInUse');
    return r || t('vmDetail.pciBlocked');
  });

  const buttonTooltip = $derived.by(() => {
    if (!isShutoff) return t('vmDetail.pciRequiresShutoff');
    return blockReasonText;
  });
</script>

<div
  class="rounded-xl border transition-all {group.assignable
    ? 'border-border bg-card shadow-sm hover:border-accent/40'
    : group.host_critical
      ? 'border-destructive/30 bg-destructive/5'
      : 'border-border/60 bg-muted/20 opacity-85'}"
>
  <div class="flex items-center justify-between p-3 border-b border-border/50 gap-2">
    <div class="flex items-center gap-2 min-w-0">
      <span class="font-semibold text-xs text-foreground font-mono">
        {t('vmDetail.pciIommuGroup', { group: group.group })}
      </span>

      {#if group.assignable}
        <span
          class="px-2 py-0.5 rounded-full text-[10px] font-medium bg-success/15 text-success border border-success/30"
        >
          {t('status.platformEnabled')}
        </span>
      {:else if group.host_critical}
        <span
          class="px-2 py-0.5 rounded-full text-[10px] font-medium bg-destructive/15 text-destructive border border-destructive/30"
        >
          {t('vmDetail.pciCriticalNotice')}
        </span>
      {:else if group.block_reason === 'pci_bridge'}
        <span
          class="px-2 py-0.5 rounded-full text-[10px] font-medium bg-muted/60 text-muted-foreground border border-border"
        >
          {t('vmDetail.pciBridgeDevice')}
        </span>
      {:else if group.block_reason === 'in_use'}
        <span
          class="px-2 py-0.5 rounded-full text-[10px] font-medium bg-accent/15 text-accent border border-accent/30"
        >
          {t('vmDetail.pciInUse')}
        </span>
      {:else if group.block_reason === 'mounted_storage'}
        <span
          class="px-2 py-0.5 rounded-full text-[10px] font-medium bg-warning/15 text-warning border border-warning/30"
        >
          {t('vmDetail.pciMountedStorage')}
        </span>
      {/if}
    </div>

    <button
      type="button"
      onclick={() => onattach?.(group)}
      disabled={loading || !isShutoff || !group.assignable}
      title={buttonTooltip}
      class="text-xs px-2.5 py-1 rounded-md font-medium transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed {group.assignable
        ? 'bg-accent text-accent-foreground hover:bg-accent/90 shadow-sm'
        : 'bg-muted text-muted-foreground'}"
    >
      {#if loading}
        <Spinner size="xs" />
      {:else}
        <Icon name="plus" size={13} class="inline mr-1" />
        {t('vmDetail.pciAttachGroup')}
      {/if}
    </button>
  </div>

  <div class="p-3 space-y-2">
    <!-- Notice if contaminated group -->
    {#if isContaminated}
      <div
        class="flex items-center gap-1.5 p-2 rounded-lg bg-accent/10 border border-accent/20 text-[11px] text-accent"
      >
        <Icon name="layers" size={13} class="shrink-0" />
        <span>{t('vmDetail.pciContaminatedGroup', { n: group.devices.length })}</span>
      </div>
    {/if}

    <!-- Notice if host critical group -->
    {#if group.host_critical}
      <div
        class="flex items-center gap-1.5 p-2 rounded-lg bg-destructive/10 border border-destructive/20 text-[11px] text-destructive"
      >
        <Icon name="alertTriangle" size={13} class="shrink-0" />
        <span>{blockReasonText}</span>
      </div>
    {/if}

    <div class="divide-y divide-border/30 text-xs">
      {#each group.devices as dev (dev.address)}
        {@const badge = getDeviceBadge(dev)}
        <div
          class="py-2 first:pt-0 last:pb-0 flex flex-col sm:flex-row sm:items-center justify-between gap-1.5"
        >
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <span class="font-medium text-foreground truncate"
                >{dev.name || dev.product_name || dev.address}</span
              >
              <span class="font-mono text-muted-foreground text-[11px] shrink-0">{dev.address}</span
              >
            </div>
            {#if dev.driver}
              <div class="text-[11px] text-muted-foreground flex items-center gap-1 mt-0.5">
                <span>{t('vmDetail.driverLabel')}:</span>
                <span class="font-mono text-foreground/80">{dev.driver}</span>
              </div>
            {/if}
          </div>

          {#if badge}
            <div class="shrink-0 sm:text-right">
              <span
                class="px-2 py-0.5 rounded text-[10px] font-medium border {badge.variant ===
                'destructive'
                  ? 'bg-destructive/15 text-destructive border-destructive/30'
                  : badge.variant === 'warning'
                    ? 'bg-warning/15 text-warning border-warning/30'
                    : badge.variant === 'success'
                      ? 'bg-success/15 text-success border-success/30'
                      : 'bg-muted/60 text-muted-foreground border-border'}"
              >
                {badge.text}
              </span>
            </div>
          {/if}
        </div>
      {/each}
    </div>
  </div>
</div>
