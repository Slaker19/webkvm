<script>
  import { onMount } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';

  let { vm = {}, vmId = '', load } = $props();

  let hostUSBDevices = $state([]);
  let hostUSBLoaded = false;
  let actionLoading = $state('');

  async function loadHostUSBDevices(force = false) {
    if (hostUSBLoaded && !force) return;
    hostUSBLoaded = true;
    try {
      hostUSBDevices = await api.listHostUSBDevices();
    } catch (e) {
      hostUSBLoaded = false;
      toast.error(e.message);
    }
  }

  async function attachUSB(vendorId, productId) {
    actionLoading = 'usb';
    try {
      await api.attachUSBDevice(vmId, vendorId, productId);
      toast.success(t('vmDetail.usbDeviceAttached'));
      await load?.();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  async function detachUSB(vendorId, productId) {
    actionLoading = 'usb';
    try {
      await api.detachUSBDevice(vmId, vendorId, productId);
      toast.success(t('vmDetail.usbDeviceDetached'));
      await load?.();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  onMount(() => {
    if (auth.isAdmin()) loadHostUSBDevices();
  });
</script>

<BlockCard bid="usb" title={t('vmDetail.usbDevices')}>
  <div class="space-y-3">
    <div>
      <div class="text-xs font-medium text-muted-foreground mb-1.5">
        {t('vmDetail.usbAttached')}
      </div>
      {#if !vm?.usb_devices || vm.usb_devices.length === 0}
        <EmptyState compact icon="usb" title={t('vmDetail.usbNoneAttached')} />
      {:else}
        <div class="space-y-1.5">
          {#each vm.usb_devices as dev, i (i)}
            <div
              class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
            >
              <span class="text-xs font-mono text-muted-foreground"
                >{dev.vendor_id}:{dev.product_id}</span
              >
              <button
                onclick={() => detachUSB(dev.vendor_id, dev.product_id)}
                disabled={actionLoading === 'usb'}
                class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10 disabled:opacity-50 disabled:cursor-not-allowed"
                >{t('vmDetail.usbDetach')}</button
              >
            </div>
          {/each}
        </div>
      {/if}
    </div>
    <div>
      <div class="flex items-center justify-between mb-1.5">
        <div class="text-xs font-medium text-muted-foreground">
          {t('vmDetail.usbAvailable')}
        </div>
        <button
          onclick={() => loadHostUSBDevices(true)}
          class="text-xs text-accent hover:text-accent-hover">{t('common.refresh')}</button
        >
      </div>
      {#if hostUSBDevices.length === 0}
        <EmptyState compact icon="usb" title={t('vmDetail.usbNoneFound')} />
      {:else}
        <div class="space-y-1.5">
          {#each hostUSBDevices as dev (`${dev.bus}:${dev.device}:${dev.vendor_id}:${dev.product_id}`)}
            <div
              class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
            >
              <div class="flex items-center gap-2 min-w-0">
                <span class="text-sm truncate">{dev.name}</span>
                <span class="text-xs font-mono text-muted-foreground"
                  >{dev.vendor_id}:{dev.product_id} · bus {dev.bus} dev {dev.device}</span
                >
              </div>
              <button
                onclick={() => attachUSB(dev.vendor_id, dev.product_id)}
                disabled={actionLoading === 'usb'}
                class="text-xs text-accent hover:text-accent-hover px-2 py-1 rounded hover:bg-muted disabled:opacity-50 disabled:cursor-not-allowed"
                >{t('vmDetail.usbAttach')}</button
              >
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>
</BlockCard>
