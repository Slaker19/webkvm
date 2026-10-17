<script>
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import { Button } from '$lib/components/ui/button';
  import { Pencil } from '@lucide/svelte';

  let {
    vm = {},
    vmId = '',
    isContainerVm = false,
    bootDevice = $bindable('hd'),
    openEditTab,
    load,
  } = $props();

  function formatUptime(sec) {
    if (!sec || sec < 0) return '—';
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    if (d > 0) return `${d}d ${h}h ${m}m`;
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
  }
</script>

<BlockCard bid="overview" title={t('vmDetail.overview')}>
  {#snippet headerActions()}
    {#if auth.canMutate()}
      <Button
        size="sm"
        variant="outline"
        class="!h-7 !text-xs"
        onclick={() => openEditTab?.('general')}
      >
        <Pencil class="w-3 h-3 mr-1.5" />
        {t('vmDetail.editSettings')}
      </Button>
    {/if}
  {/snippet}

  {#if vm?.cover}
    <div
      class="relative w-full h-32 rounded-xl overflow-hidden border border-border bg-muted/40 shadow-2xs mb-4"
    >
      <img src={vm.cover} alt="" class="w-full h-full object-cover" />
      <div
        class="absolute inset-0 bg-gradient-to-t from-background/90 via-background/20 to-transparent flex items-end p-3"
      >
        <div class="flex items-center gap-2">
          <span
            class="text-xs font-semibold px-2.5 py-0.5 rounded-full bg-background/80 backdrop-blur border border-border/80"
          >
            {vm.alias || vm.name}
          </span>
          {#if vm.os_type}
            <span
              class="text-[11px] px-2 py-0.5 rounded-full bg-accent/20 text-accent font-medium backdrop-blur"
            >
              {vm.os_type}
            </span>
          {/if}
        </div>
      </div>
    </div>
  {/if}

  <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 mb-4">
    <div class="border border-border rounded-md p-3 bg-background">
      <p class="text-2xl font-semibold tnum">{vm?.vcpus ?? 0}</p>
      <p class="text-xs text-muted-foreground mt-0.5">{t('common.vcpu')}</p>
      {#if vm?.cpu_usage != null}<p class="text-xs text-accent mt-0.5 tnum">
          {vm.cpu_usage.toFixed(1)}% used
        </p>{/if}
    </div>
    <div class="border border-border rounded-md p-3 bg-background">
      <p class="text-2xl font-semibold tnum">{vm?.ram_mb ?? 0}</p>
      <p class="text-xs text-muted-foreground mt-0.5">{t('vmDetail.ramLabel')}</p>
      {#if vm?.ram_used_mb != null}<p class="text-xs text-accent mt-0.5 tnum">
          {vm.ram_used_mb} MB used
        </p>{/if}
    </div>
    <div class="border border-border rounded-md p-3 bg-background">
      <p class="text-lg font-semibold tnum">{formatUptime(vm?.uptime_sec)}</p>
      <p class="text-xs text-muted-foreground mt-0.5">{t('vmDetail.uptime')}</p>
    </div>
  </div>
  <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-1.5 text-sm">
    <div class="flex gap-2">
      <span class="text-muted-foreground shrink-0">{t('vmDetail.typeLabel')}</span>
      <span
        class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded border text-[10px] uppercase tracking-wider font-medium {isContainerVm
          ? 'border-warning/30 bg-warning/10 text-warning'
          : 'border-accent/30 bg-accent/10 text-accent'}"
      >
        {isContainerVm ? 'Incus' : 'KVM'}
      </span>
    </div>
    {#if vm?.os_type}
      <div class="flex gap-2">
        <span class="text-muted-foreground shrink-0">{t('vmDetail.os')}</span><span class="truncate"
          >{vm.os_type}{vm.os_version ? ' ' + vm.os_version : ''}</span
        >
      </div>
    {/if}
    {#if !isContainerVm}
      <div class="flex gap-2">
        <span class="text-muted-foreground shrink-0">{t('vmDetail.chipset')}</span><span
          >{vm?.chipset || ''}</span
        >
      </div>
      <div class="flex gap-2">
        <span class="text-muted-foreground shrink-0">{t('vmDetail.secureBoot')}</span><span
          >{vm?.secure_boot ? t('common.yes') : t('common.no')}</span
        >
      </div>
      <div class="flex gap-2">
        <span class="text-muted-foreground shrink-0">{t('vmDetail.tpm')}</span><span
          >{vm?.tpm_enabled ? t('common.yes') : t('common.no')}</span
        >
      </div>
      <div class="flex gap-2">
        <span class="text-muted-foreground shrink-0">{t('vmDetail.bios')}</span><span
          class="capitalize">{vm?.firmware || '?'}</span
        >
      </div>
      <div class="flex gap-2">
        <span class="text-muted-foreground shrink-0">{t('vmDetail.cpuMode')}</span><span
          >{vm?.cpu_mode || 'host-passthrough'}</span
        >
      </div>
      <div class="flex gap-2">
        <span class="text-muted-foreground shrink-0">{t('vmDetail.video')}</span><span
          >{vm?.video_model || 'virtio'}</span
        >
      </div>
      <div class="flex items-center gap-2">
        <span class="text-muted-foreground shrink-0">{t('vmDetail.boot')}</span>
        <select
          bind:value={bootDevice}
          onchange={() =>
            api
              .setBootDevice(vmId, bootDevice)
              .then(() => load?.())
              .catch((e) => toast.error(e.message))}
          class="input !py-1 !text-xs w-auto"
        >
          <option value="hd">{t('vmDetail.hardDisk')}</option>
          <option value="cdrom">{t('vmDetail.cdrom')}</option>
          <option value="network">{t('vmDetail.network')}</option>
        </select>
      </div>
    {/if}
  </div>
</BlockCard>
