<script>
  import { onMount } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import PciPreflightPanel from '$lib/components/PciPreflightPanel.svelte';
  import PciGroupCard from '$lib/components/PciGroupCard.svelte';

  let { vm = {}, vmId = '', _isContainerVm = false, load } = $props();

  let hostPCIGroups = $state([]);
  let hostPCIError = $state('');
  let hostPCILoaded = false;
  let hostPCIPreflight = $state(null);
  let pciPreflightLoading = $state(false);
  let actionLoading = $state('');

  async function loadHostPCIPreflight() {
    pciPreflightLoading = true;
    try {
      hostPCIPreflight = await api.getHostPCIPreflight();
    } catch {
      hostPCIPreflight = null;
    } finally {
      pciPreflightLoading = false;
    }
  }

  async function loadHostPCIDevices(force = false) {
    if (hostPCILoaded && !force) return;
    hostPCILoaded = true;
    try {
      hostPCIGroups = await api.listHostPCIDevices();
      hostPCIError = '';
    } catch (e) {
      hostPCIGroups = [];
      hostPCIError = e.message;
      hostPCILoaded = false;
    }
  }

  async function attachPCIGroup(group) {
    actionLoading = 'pci';
    try {
      await api.attachPCIDevices(
        vmId,
        group.devices.map((d) => d.address)
      );
      toast.success(t('vmDetail.pciDeviceAttached'));
      await load?.();
      await loadHostPCIDevices(true);
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  async function detachPCI(address) {
    actionLoading = 'pci';
    try {
      await api.detachPCIDevice(vmId, address);
      toast.success(t('vmDetail.pciDeviceDetached'));
      await load?.();
      await loadHostPCIDevices(true);
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  async function unbindPCIDevice(address) {
    actionLoading = 'pci';
    try {
      await api.unbindHostPCIDeviceVFIO(address);
      toast.success(t('hardwareVfio.unbindSuccess', { address }));
      await loadHostPCIDevices(true);
      await loadHostPCIPreflight();
    } catch (e) {
      toast.error(t('hardwareVfio.unbindError', { address, error: e.message }));
    } finally {
      actionLoading = '';
    }
  }

  onMount(() => {
    if (auth.isAdmin()) {
      loadHostPCIDevices();
      loadHostPCIPreflight();
    }
  });
</script>

<BlockCard bid="pci" title={t('vmDetail.pciDevices')}>
  <div class="space-y-3">
    <p class="text-xs text-muted-foreground">{t('vmDetail.pciHint')}</p>
    <div>
      <div class="text-xs font-medium text-muted-foreground mb-1.5">
        {t('vmDetail.pciAttached')}
      </div>
      {#if !vm?.pci_devices || vm.pci_devices.length === 0}
        <p class="text-sm text-muted-foreground">{t('vmDetail.pciNoneAttached')}</p>
      {:else}
        <div class="space-y-1.5">
          {#each vm.pci_devices as dev (dev.address)}
            <div
              class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
            >
              <span class="text-xs font-mono text-muted-foreground">{dev.address}</span>
              <button
                onclick={() => detachPCI(dev.address)}
                disabled={actionLoading === 'pci' || vm.state !== 'shutoff'}
                title={vm.state !== 'shutoff' ? t('vmDetail.pciRequiresShutoff') : ''}
                class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10 disabled:opacity-50 disabled:cursor-not-allowed"
                >{t('vmDetail.pciDetach')}</button
              >
            </div>
          {/each}
        </div>
      {/if}
    </div>
    <div class="space-y-3">
      <PciPreflightPanel
        info={hostPCIPreflight}
        loading={pciPreflightLoading}
        onrefresh={() => {
          loadHostPCIPreflight();
          loadHostPCIDevices(true);
        }}
      />

      <div>
        <div class="flex items-center justify-between mb-2">
          <div class="text-xs font-medium text-muted-foreground">
            {t('vmDetail.pciAvailable')}
          </div>
          <button
            onclick={() => {
              loadHostPCIDevices(true);
              loadHostPCIPreflight();
            }}
            class="text-xs text-accent hover:text-accent-hover">{t('common.refresh')}</button
          >
        </div>
        {#if hostPCIError}
          <p class="text-sm text-destructive">{hostPCIError}</p>
        {:else if hostPCIGroups.length === 0}
          <EmptyState compact icon="circuitBoard" title={t('vmDetail.pciNoneFound')} />
        {:else}
          <div class="space-y-2">
            {#each hostPCIGroups as group (group.group)}
              <PciGroupCard
                {group}
                vmState={vm?.state}
                loading={actionLoading === 'pci'}
                onattach={attachPCIGroup}
                onunbind={unbindPCIDevice}
              />
            {/each}
          </div>
        {/if}
      </div>
    </div>
  </div>
</BlockCard>
