<script>
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let preflight = $state(null);
  let groups = $state([]);
  let loading = $state(true);
  let actionLoading = $state({});
  let search = $state('');

  async function loadData() {
    loading = true;
    try {
      const [pf, pci] = await Promise.all([api.getHostPCIPreflight(), api.listHostPCIDevices()]);
      preflight = pf;
      groups = pci || [];
    } catch (e) {
      toast.error(t('hardwareVfio.loadError', { error: e.message }));
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    loadData();
  });

  async function bindVFIO(address) {
    actionLoading[address] = true;
    try {
      await api.bindHostPCIDeviceVFIO(address);
      toast.success(t('hardwareVfio.bindSuccess', { address }));
      await loadData();
    } catch (e) {
      toast.error(t('hardwareVfio.bindError', { address, error: e.message }));
    } finally {
      actionLoading[address] = false;
    }
  }

  async function unbindVFIO(address) {
    actionLoading[address] = true;
    try {
      await api.unbindHostPCIDeviceVFIO(address);
      toast.success(t('hardwareVfio.unbindSuccess', { address }));
      await loadData();
    } catch (e) {
      toast.error(t('hardwareVfio.unbindError', { address, error: e.message }));
    } finally {
      actionLoading[address] = false;
    }
  }

  const filteredGroups = $derived(
    groups.filter((g) => {
      if (!search.trim()) return true;
      const q = search.toLowerCase();
      if (`group ${g.group}`.includes(q)) return true;
      return g.devices?.some(
        (d) =>
          d.name?.toLowerCase().includes(q) ||
          d.address?.toLowerCase().includes(q) ||
          d.driver?.toLowerCase().includes(q) ||
          d.vendor_name?.toLowerCase().includes(q) ||
          d.product_name?.toLowerCase().includes(q)
      );
    })
  );
</script>

<div class="space-y-6">
  <!-- Preflight diagnostics header -->
  {#if preflight}
    <div class="rounded-xl border border-border bg-card/60 p-4 space-y-4">
      <div class="flex items-center justify-between flex-wrap gap-3">
        <div class="flex items-center gap-2">
          <Icon name="circuitBoard" size={18} class="text-accent shrink-0" />
          <h3 class="font-semibold text-foreground text-sm">
            {t('hardwareVfio.diagnosticsTitle')}
          </h3>
        </div>
        <Button variant="outline" size="sm" onclick={loadData} disabled={loading} class="gap-1.5">
          {#if loading}
            <Spinner size="xs" />
          {:else}
            <Icon name="refreshCw" size={13} />
          {/if}
          {t('common.refresh')}
        </Button>
      </div>

      <div class="grid grid-cols-2 sm:grid-cols-4 gap-3 text-xs">
        <div class="p-3 rounded-lg border border-border/60 bg-muted/20">
          <p class="text-muted-foreground">{t('hardwareVfio.iommuStatus')}</p>
          <p class="font-medium mt-1 flex items-center gap-1.5">
            <span
              class="inline-block w-2 h-2 rounded-full {preflight.iommu_enabled
                ? 'bg-success'
                : 'bg-destructive'}"
            ></span>
            {preflight.iommu_enabled
              ? preflight.iommu_vendor === 'intel'
                ? 'Intel VT-d'
                : preflight.iommu_vendor === 'amd'
                  ? 'AMD-Vi'
                  : t('status.platformEnabled')
              : t('status.platformDisabled')}
          </p>
        </div>

        <div class="p-3 rounded-lg border border-border/60 bg-muted/20">
          <p class="text-muted-foreground">{t('hardwareVfio.iommuMode')}</p>
          <p class="font-medium mt-1 font-mono">
            {preflight.iommu_mode === 'passthrough'
              ? t('vmDetail.pciPreflightModePassthrough')
              : t('vmDetail.pciPreflightModeTranslated')}
          </p>
        </div>

        <div class="p-3 rounded-lg border border-border/60 bg-muted/20">
          <p class="text-muted-foreground">{t('hardwareVfio.vfioModule')}</p>
          <p class="font-medium mt-1 flex items-center gap-1.5">
            <span
              class="inline-block w-2 h-2 rounded-full {preflight.vfio_module_loaded ||
              preflight.vfio_available
                ? 'bg-success'
                : 'bg-warning'}"
            ></span>
            {preflight.vfio_module_loaded
              ? t('hardwareVfio.moduleLoaded')
              : preflight.vfio_available
                ? t('hardwareVfio.moduleAvailable')
                : t('hardwareVfio.moduleNotLoaded')}
          </p>
        </div>

        <div class="p-3 rounded-lg border border-border/60 bg-muted/20">
          <p class="text-muted-foreground">{t('hardwareVfio.assignableGroups')}</p>
          <p class="font-medium mt-1 font-mono">
            {preflight.groups_assignable ?? 0} / {preflight.groups_total ?? groups.length}
          </p>
        </div>
      </div>
    </div>
  {/if}

  <!-- Device filter and counts -->
  <div class="flex items-center justify-between gap-3 flex-wrap">
    <div class="relative w-full max-w-sm">
      <Input
        bind:value={search}
        placeholder={t('hardwareVfio.searchPlaceholder')}
        class="text-xs h-9"
      />
    </div>
    <div class="text-xs text-muted-foreground">
      {t('hardwareVfio.totalGroups', { count: filteredGroups.length })}
    </div>
  </div>

  <!-- IOMMU Groups list -->
  {#if loading && groups.length === 0}
    <div class="flex items-center justify-center p-12">
      <Spinner size="md" />
    </div>
  {:else if filteredGroups.length === 0}
    <div
      class="text-center p-8 border border-border rounded-xl bg-card/40 text-muted-foreground text-sm"
    >
      {t('hardwareVfio.noDevicesFound')}
    </div>
  {:else}
    <div class="space-y-4">
      {#each filteredGroups as group (group.group)}
        <div class="border border-border rounded-xl bg-card overflow-hidden">
          <div
            class="flex items-center justify-between px-4 py-2.5 bg-muted/30 border-b border-border/60 text-xs font-medium"
          >
            <div class="flex items-center gap-2">
              <span class="font-mono font-semibold text-foreground">
                {t('hardwareVfio.groupTitle', { n: group.group })}
              </span>
              {#if group.all_vfio_bound}
                <span
                  class="px-1.5 py-0.5 rounded text-[10px] bg-success/15 text-success font-medium"
                >
                  {t('hardwareVfio.allVFIO')}
                </span>
              {/if}
            </div>
            <span class="text-muted-foreground text-[11px]">
              {t('hardwareVfio.deviceCount', { count: group.devices?.length || 0 })}
            </span>
          </div>

          <div class="divide-y divide-border/60">
            {#each group.devices || [] as dev (dev.address)}
              <div
                class="p-4 flex flex-col md:flex-row md:items-center justify-between gap-3 text-xs"
              >
                <div class="space-y-1 min-w-0 flex-1">
                  <div class="flex items-center gap-2 flex-wrap">
                    <span class="font-mono font-semibold text-foreground text-sm">
                      {dev.address}
                    </span>
                    <span
                      class="px-2 py-0.5 rounded text-[11px] font-mono {dev.driver === 'vfio-pci'
                        ? 'bg-accent/20 text-accent font-semibold border border-accent/30'
                        : 'bg-muted text-muted-foreground'}"
                    >
                      {dev.driver ? `driver: ${dev.driver}` : t('hardwareVfio.noDriver')}
                    </span>

                    {#if dev.in_use}
                      <span class="px-2 py-0.5 rounded text-[11px] bg-warning/20 text-warning">
                        {t('hardwareVfio.inUse')}
                      </span>
                    {:else if dev.host_critical}
                      <span
                        class="px-2 py-0.5 rounded text-[11px] bg-destructive/20 text-destructive font-medium"
                      >
                        {t('hardwareVfio.hostCritical')}: {dev.block_reason || t('common.error')}
                      </span>
                    {:else if dev.block_reason === 'pci_bridge'}
                      <span class="px-2 py-0.5 rounded text-[11px] bg-muted text-muted-foreground">
                        {t('hardwareVfio.bridge')}
                      </span>
                    {/if}
                  </div>

                  <p class="text-sm font-medium text-foreground truncate">
                    {dev.product_name || dev.name || dev.product_id}
                  </p>
                  {#if dev.vendor_name}
                    <p class="text-xs text-muted-foreground truncate">
                      {dev.vendor_name} ({dev.vendor_id}:{dev.product_id})
                    </p>
                  {/if}
                </div>

                <!-- Actions -->
                <div class="flex items-center gap-2 shrink-0">
                  {#if dev.in_use}
                    <span class="text-xs text-muted-foreground italic">
                      {t('hardwareVfio.inUse')}
                    </span>
                  {:else if !dev.host_critical && dev.block_reason !== 'pci_bridge'}
                    {#if dev.vfio_bound}
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={actionLoading[dev.address]}
                        onclick={() => unbindVFIO(dev.address)}
                        class="text-xs text-warning hover:text-warning"
                        title={t('hardwareVfio.revertToHostHint')}
                      >
                        {#if actionLoading[dev.address]}
                          <Spinner size="xs" />
                        {/if}
                        {t('hardwareVfio.revertToHost')}
                      </Button>
                    {:else}
                      <Button
                        variant="secondary"
                        size="sm"
                        disabled={actionLoading[dev.address] ||
                          (!preflight?.vfio_module_loaded && !preflight?.vfio_available)}
                        onclick={() => bindVFIO(dev.address)}
                        class="text-xs font-medium"
                        title={!preflight?.vfio_module_loaded && !preflight?.vfio_available
                          ? t('hardwareVfio.moduleNotLoaded')
                          : ''}
                      >
                        {#if actionLoading[dev.address]}
                          <Spinner size="xs" />
                        {/if}
                        {t('hardwareVfio.isolateVFIO')}
                      </Button>
                    {/if}
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>
