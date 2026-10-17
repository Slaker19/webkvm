<script>
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import { toast } from '$lib/components/ui/toast';
  import { auth, api } from '$lib/stores/auth.svelte.js';
  import { upsertTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { t, htmlVar } from '$lib/i18n.svelte.js';
  import CreateRaidDialog from './CreateRaidDialog.svelte';
  import InitDiskDialog from './InitDiskDialog.svelte';

  let {
    hostDisks = [],
    hostDisksLoading = false,
    orphanMounts = [],
    raidArrays = [],
    raidLoading = false,
    onRefresh = () => {},
    onOpenSmart = () => {},
    askConfirm = () => {},
    showStorageErr = () => {},
  } = $props();

  let showCreateRaid = $state(false);
  let showInitDisk = $state(false);
  let selectedInitDisk = $state(null);

  function openCreateRaidDialog() {
    showCreateRaid = true;
  }

  function openInitDiskDialog(disk) {
    selectedInitDisk = disk;
    showInitDisk = true;
  }

  function openMountExistingRaid(arr) {
    const pseudoDisk = {
      path: arr.device,
      model: `Linux Software RAID (${arr.level})`,
      fstype: arr.fstype || '',
      size_human: arr.size_human,
      is_system: false,
    };
    openInitDiskDialog(pseudoDisk);
  }

  function hostDiskStatus(disk) {
    if (disk.is_system) {
      return { key: 'system', label: t('storage.diskProtected'), icon: 'shield' };
    }
    const mounts = [
      ...(disk.mountpoints || []),
      ...(disk.children || []).flatMap((c) => c.mountpoints || []),
    ].filter(Boolean);

    if (disk.mount_state === 'configured') {
      return {
        key: 'reserved',
        label: mounts.length
          ? t('storage.diskReserved', { path: mounts[0] })
          : t('storage.diskReservedNoPath'),
        icon: 'clock',
        title: disk.mount_detail || mounts.join(', '),
      };
    }

    if (disk.mount_state === 'active' || mounts.length > 0) {
      return {
        key: 'inuse',
        label: t('storage.diskInUse', { path: mounts[0] || '' }),
        icon: 'check',
        title: mounts.join(', '),
      };
    }
    return { key: 'free', label: t('storage.diskAvailable'), icon: 'hardDrive' };
  }

  async function doSyncRaid(device, action = 'check') {
    try {
      await api.syncHostRaid(device, action);
      onRefresh();
      toast.success(`Acción ${action} iniciada en ${device}`);
    } catch (e) {
      showStorageErr('RAID Sync Error', e);
    }
  }

  function doStopRaid(device) {
    askConfirm({
      title: t('storage.stopRaidTitle', { device }),
      description: t('storage.stopRaidDesc', { device }),
      confirmLabel: t('storage.raidStopAction'),
      variant: 'destructive',
      onConfirm: async () => {
        try {
          await api.stopHostRaid(device);
          onRefresh();
          toast.success(`Array RAID ${device} detenido`);
        } catch (e) {
          toast.error(e.message || 'Error al detener array RAID');
        }
      },
    });
  }

  function wipeDiskConfirm(disk) {
    askConfirm({
      title: `${t('storage.wipeDiskTitle')}: ${disk.path}`,
      description: t('storage.wipeDiskDesc'),
      confirmLabel: t('storage.wipe'),
      variant: 'destructive',
      onConfirm: async () => {
        const taskId = 'disk-wipe:' + disk.path;
        upsertTask({
          id: taskId,
          kind: 'storage',
          title: `Borrando disco: ${disk.path}`,
          pct: 20,
          message: 'Limpiando firmas de sistema de archivos y particiones...',
          status: 'running',
        });
        try {
          await api.wipeHostDisk(disk.path);
          finishTask(taskId, 'success', `Disco ${disk.path} borrado con éxito`, 100);
          toast.success(t('storage.diskWiped'));
          onRefresh();
        } catch (e) {
          finishTask(taskId, 'error', e.message || 'Error al borrar disco', 20);
          toast.error(e.message);
        }
      },
    });
  }
</script>

<div class="p-4 sm:p-5 space-y-4">
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
    <div>
      <h3 class="text-sm font-bold text-foreground">{t('storage.hostDisksTitle')}</h3>
      <p class="text-xs text-muted-foreground">
        {@html t('storage.hostDisksDesc', {
          code: htmlVar('<code class="text-foreground font-mono">/mnt/[nombre]</code>'),
        })}
      </p>
    </div>

    <div class="flex items-center gap-2">
      <Button
        variant="outline"
        size="sm"
        class="!h-8 !text-xs"
        onclick={onRefresh}
        disabled={hostDisksLoading}
      >
        <Icon
          name="refresh"
          size={13}
          class={hostDisksLoading ? 'animate-spin mr-1' : 'mr-1'}
        />
        {t('common.refresh')}
      </Button>
      {#if auth.isAdmin()}
        <Button
          variant="outline"
          size="sm"
          class="!h-8 !text-xs gap-1.5"
          onclick={openCreateRaidDialog}
        >
          <Icon name="hardDrive" size={13} />
          {t('storage.createRaidBtn')}
        </Button>
        <Button
          size="sm"
          class="!h-8 !text-xs gap-1.5"
          onclick={() => openInitDiskDialog(null)}
        >
          <Icon name="plus" size={13} />
          {t('storage.initDirButton')}
        </Button>
      {/if}
    </div>
  </div>

  {#if orphanMounts.length > 0}
    <div
      class="border border-destructive/40 bg-destructive/5 rounded-xl p-3 flex items-start gap-2.5"
    >
      <Icon name="alertTriangle" size={16} class="text-destructive shrink-0 mt-0.5" />
      <div class="min-w-0 space-y-1.5">
        <p class="text-xs font-bold text-destructive">{t('storage.orphanMountTitle')}</p>
        <p class="text-xs text-muted-foreground">{t('storage.orphanMountDesc')}</p>
        <ul class="space-y-1 pt-0.5">
          {#each orphanMounts as o (o.unit)}
            <li class="text-xs font-mono text-foreground break-all">
              {o.unit}{#if o.mountpoint}<span class="text-muted-foreground">
                  → {o.mountpoint}</span
                >{/if}{#if o.enabled}<span class="text-destructive">
                  · {t('storage.orphanMountEnabled')}</span
                >{/if}
            </li>
          {/each}
        </ul>
        <p class="text-xs text-muted-foreground pt-0.5">
          {t('storage.orphanMountFix')}
        </p>
      </div>
    </div>
  {/if}

  {#if raidArrays.length > 0}
    <div class="space-y-2">
      <h4
        class="text-xs font-bold flex items-center gap-1.5 uppercase tracking-wider text-muted-foreground"
      >
        <Icon name="layers" size={13} class="text-primary" />
        {t('storage.raidArraysTitle')} ({raidArrays.length})
      </h4>
      <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
        {#each raidArrays as arr (arr.device)}
          <div class="border border-border/70 rounded-xl bg-card p-3.5 space-y-2.5 shadow-xs">
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-2">
                <span class="font-mono font-bold text-sm text-foreground">{arr.device}</span>
                <span
                  class="px-1.5 py-0.5 rounded text-[10px] font-mono font-semibold uppercase border border-primary/30 bg-primary/10 text-primary"
                >
                  {arr.level}
                </span>
                <span
                  class="px-1.5 py-0.5 rounded text-[10px] font-semibold border {arr.degraded
                    ? 'bg-destructive/15 text-destructive border-destructive/30'
                    : 'bg-success/15 text-success border-success/30'}"
                >
                  {arr.degraded ? t('storage.raidDegraded') : t('storage.raidHealthy')}
                </span>
              </div>
              <span class="font-mono font-bold text-xs text-foreground">{arr.size_human}</span>
            </div>

            <div class="flex items-center justify-between text-xs text-muted-foreground">
              <div class="flex items-center gap-1.5">
                <span>{t('storage.membersLabel')}:</span>
                <span class="font-mono text-foreground font-medium"
                  >{arr.devices?.join(', ') || 'N/A'}</span
                >
              </div>
              <div class="flex items-center gap-1">
                <span>Sync:</span>
                <span
                  class="font-mono font-medium {arr.sync_action === 'idle'
                    ? 'text-muted-foreground'
                    : 'text-warning'}"
                >
                  {arr.sync_action}
                </span>
              </div>
            </div>

            {#if arr.mounted && arr.mountpoints?.length}
              <div
                class="text-[11px] font-mono text-accent flex items-center gap-1.5 bg-accent/10 p-1.5 rounded-lg border border-accent/20"
              >
                <Icon name="check" size={12} class="text-accent shrink-0" />
                <span>{t('storage.mountedAt', { path: arr.mountpoints.join(', ') })}</span>
                {#if arr.fstype}
                  <span class="text-muted-foreground">({arr.fstype})</span>
                {/if}
              </div>
            {/if}

            {#if auth.isAdmin()}
              <div class="pt-2 border-t border-border/50 flex items-center justify-end gap-2 flex-wrap">
                {#if !arr.mounted}
                  <Button
                    size="xs"
                    variant="default"
                    class="!h-7 !text-xs gap-1"
                    onclick={() => openMountExistingRaid(arr)}
                  >
                    <Icon name="plus" size={11} />
                    {t('storage.mountExistingRaid')}
                  </Button>
                {/if}
                <Button
                  size="xs"
                  variant="outline"
                  class="!h-7 !text-xs gap-1"
                  onclick={() => doSyncRaid(arr.device, 'check')}
                >
                  <Icon name="shield" size={11} />
                  {t('storage.raidCheckAction')}
                </Button>
                <Button
                  size="xs"
                  variant="destructive"
                  class="!h-7 !text-xs gap-1"
                  onclick={() => doStopRaid(arr.device)}
                >
                  <Icon name="power" size={11} />
                  {t('storage.raidStopAction')}
                </Button>
              </div>
            {/if}
          </div>
        {/each}
      </div>
    </div>
  {/if}

  {#if hostDisksLoading}
    <div class="flex justify-center py-16"><Spinner size="lg" /></div>
  {:else if hostDisks.length === 0}
    <EmptyState compact icon="hardDrive" title={t('storage.noHostDisks')} />
  {:else}
    <div class="border border-border rounded-xl bg-background overflow-hidden shadow-xs">
      <div class="overflow-x-auto">
        <table class="w-full text-xs text-left border-collapse">
          <thead>
            <tr
              class="border-b border-border bg-muted/30 text-muted-foreground font-semibold"
            >
              <th class="p-3 pl-4">{t('storage.colDevice')}</th>
              <th class="p-3">{t('storage.colModelSerial')}</th>
              <th class="p-3">{t('storage.colTypeBus')}</th>
              <th class="p-3 text-right">{t('storage.capacity')}</th>
              <th class="p-3">{t('storage.filesystemTitle')}</th>
              <th class="p-3">{t('storage.colMountPoint')}</th>
              <th class="p-3 pr-4 text-right">{t('storage.colActions')}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border/60">
            {#each hostDisks as disk (disk.path)}
              {@const status = hostDiskStatus(disk)}
              <tr class="hover:bg-muted/20 transition-colors">
                <td class="p-3 pl-4 font-mono font-medium flex items-center gap-2">
                  <Icon
                    name={disk.rotational ? 'hardDrive' : 'zap'}
                    size={15}
                    class={disk.rotational ? 'text-warning' : 'text-accent'}
                  />
                  <span class="font-bold text-foreground">{disk.path}</span>
                  <span
                    title={status.title || status.label}
                    class="px-1.5 py-0.5 rounded text-[10px] font-sans font-semibold border flex items-center gap-1 whitespace-nowrap {status.key ===
                    'system'
                      ? 'bg-success/15 text-success border-success/30'
                      : status.key === 'inuse'
                        ? 'bg-accent/15 text-accent border-accent/30'
                        : 'bg-muted text-muted-foreground border-border'}"
                  >
                    <Icon name={status.icon} size={10} />
                    {status.label}
                  </span>
                </td>
                <td class="p-3">
                  <div class="font-medium text-foreground">
                    {disk.model || t('storage.genericModel')}
                  </div>
                  {#if disk.serial}
                    <div class="text-[10px] font-mono text-muted-foreground">
                      {disk.serial}
                    </div>
                  {/if}
                </td>
                <td class="p-3">
                  <span
                    class="px-2 py-0.5 rounded uppercase font-mono text-[10px] font-semibold border {disk.rotational
                      ? 'border-warning/30 bg-warning/10 text-warning'
                      : 'border-accent/30 bg-accent/10 text-accent'}"
                  >
                    {disk.transport || (disk.rotational ? 'HDD' : 'SSD')}
                  </span>
                  {#if disk.smart}
                    <div class="mt-1 flex items-center gap-1.5 flex-wrap">
                      <span
                        class="px-1.5 py-0.2 rounded text-[9px] font-mono font-bold uppercase border {disk
                          .smart.status === 'PASSED'
                          ? 'bg-success/15 text-success border-success/30'
                          : disk.smart.status === 'WARNING'
                            ? 'bg-warning/15 text-warning border-warning/30'
                            : disk.smart.status === 'FAILED'
                              ? 'bg-destructive/15 text-destructive border-destructive/30'
                              : 'bg-muted text-muted-foreground border-border'}"
                        title={disk.smart.healthy
                          ? 'S.M.A.R.T. Saludable'
                          : 'Atención / Fallo S.M.A.R.T.'}
                      >
                        SMART: {disk.smart.status}
                      </span>
                      {#if disk.smart.temperature_c > 0}
                        <span
                          class="text-[10px] font-mono {disk.smart.temperature_c >= 55
                            ? 'text-destructive font-bold'
                            : disk.smart.temperature_c >= 45
                              ? 'text-warning font-semibold'
                              : 'text-muted-foreground'}"
                          title="Temperatura"
                        >
                          {disk.smart.temperature_c}°C
                        </span>
                      {/if}
                      {#if disk.smart.wear_percentage >= 0}
                        <span
                          class="text-[10px] font-mono text-muted-foreground"
                          title="Desgaste acumulado SSD"
                        >
                          {disk.smart.wear_percentage}% uso
                        </span>
                      {/if}
                    </div>
                  {/if}
                </td>
                <td class="p-3 text-right font-mono font-bold text-foreground"
                  >{disk.size_human}</td
                >
                <td class="p-3 font-mono text-muted-foreground">
                  {disk.fstype ||
                    (disk.children?.length
                      ? t('storage.partitionsCount', { n: disk.children.length })
                      : t('storage.noFilesystem'))}
                </td>
                <td class="p-3 font-mono text-xs">
                  {#if disk.mountpoints?.length}
                    <span
                      class={disk.mount_state === 'configured'
                        ? 'text-warning font-semibold'
                        : 'text-accent font-semibold'}>{disk.mountpoints.join(', ')}</span
                    >
                  {:else if disk.children?.some((c) => c.mountpoints?.length)}
                    {@const mnts = disk.children
                      .flatMap((c) => c.mountpoints)
                      .filter(Boolean)}
                    <span class="text-foreground font-medium">{mnts.join(', ')}</span>
                  {:else}
                    <span class="text-muted-foreground/60 italic"
                      >{t('storage.notMounted')}</span
                    >
                  {/if}
                  {#if disk.mount_state === 'configured' && disk.mount_detail}
                    <span
                      class="mt-1 flex items-start gap-1 text-[11px] font-sans not-italic text-warning"
                    >
                      <Icon name="clock" size={11} class="shrink-0 mt-px" />
                      <span>{disk.mount_detail}</span>
                    </span>
                  {/if}
                </td>
                {#if hostDiskStatus(disk).key === 'free'}
                  <td class="p-3 pr-4 text-right">
                    <div class="flex items-center justify-end gap-1.5">
                      {#if disk.type !== 'raid' && !disk.path.startsWith('/dev/md')}
                        <Button
                          size="xs"
                          variant="outline"
                          class="!h-7 !text-xs gap-1"
                          onclick={() => onOpenSmart(disk)}
                        >
                          SMART
                        </Button>
                      {/if}
                      {#if auth.isAdmin()}
                        <Button
                          size="xs"
                          variant="outline"
                          class="!h-7 !text-xs gap-1"
                          onclick={() => openInitDiskDialog(disk)}
                        >
                          <Icon name="plus" size={12} />
                          {t('storage.formatAndMount')}
                        </Button>
                        <Button
                          size="xs"
                          variant="destructive"
                          class="!h-7 !text-xs gap-1"
                          onclick={() => wipeDiskConfirm(disk)}
                        >
                          <Icon name="trash" size={12} />
                          {t('storage.wipeShort')}
                        </Button>
                      {:else}
                        <span class="text-[11px] text-muted-foreground/80 italic"
                          >{t('storage.diskAvailable')}</span
                        >
                      {/if}
                    </div>
                  </td>
                {:else}
                  <td class="p-3 pr-4 text-right">
                    <div class="flex items-center justify-end gap-2">
                      {#if disk.type !== 'raid' && !disk.path.startsWith('/dev/md')}
                        <Button
                          size="xs"
                          variant="outline"
                          class="!h-7 !text-xs gap-1"
                          onclick={() => onOpenSmart(disk)}
                        >
                          SMART
                        </Button>
                      {/if}
                      <span class="text-[11px] text-muted-foreground/80 italic">
                        {hostDiskStatus(disk).key === 'system'
                          ? t('storage.diskProtected')
                          : hostDiskStatus(disk).label}
                      </span>
                    </div>
                  </td>
                {/if}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {/if}
</div>

<CreateRaidDialog
  bind:open={showCreateRaid}
  {hostDisks}
  onCreated={onRefresh}
  onError={showStorageErr}
/>

<InitDiskDialog
  bind:open={showInitDisk}
  disk={selectedInitDisk}
  {hostDisks}
  onInitialized={onRefresh}
/>
