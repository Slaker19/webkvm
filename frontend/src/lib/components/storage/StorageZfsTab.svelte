<script>
  import * as Dialog from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { formatBytes } from '$lib/utils/format.js';
  import { toast } from '$lib/components/ui/toast';
  import { upsertTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { auth, api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';

  let {
    zpools = [],
    zvols = [],
    zfsLoading = false,
    hostDisks = [],
    onRefresh = () => {},
    showStorageErr = () => {},
    askConfirm = () => {},
  } = $props();

  let scrubbingPool = $state({});

  // Create ZPool State
  let showCreateZPool = $state(false);
  let createZPoolName = $state('');
  let createZPoolTopology = $state('stripe');
  let createZPoolDisks = $state([]);
  let createZPoolAshift = $state(12);
  let createZPoolCompression = $state('lz4');
  let createZPoolMountPoint = $state('');
  let createZPoolAutoUnmount = $state(true);
  let creatingZPool = $state(false);

  // Create ZVol State
  let showCreateZVol = $state(false);
  let createZVolPool = $state('');
  let createZVolName = $state('');
  let createZVolSizeGB = $state(20);
  let createZVolSparse = $state(true);
  let creatingZVol = $state(false);

  function openCreateZPoolDialog() {
    createZPoolName = '';
    createZPoolTopology = 'stripe';
    createZPoolDisks = [];
    createZPoolAshift = 12;
    createZPoolCompression = 'lz4';
    createZPoolMountPoint = '';
    createZPoolAutoUnmount = true;
    showCreateZPool = true;
  }

  function minDisksForTopology(topo) {
    switch (topo) {
      case 'mirror':
        return 2;
      case 'raidz1':
        return 3;
      case 'raidz2':
        return 4;
      case 'raidz3':
        return 5;
      default:
        return 1;
    }
  }

  async function doCreateZPool() {
    const min = minDisksForTopology(createZPoolTopology);
    if (!createZPoolName || createZPoolDisks.length < min) return;
    creatingZPool = true;
    const taskId = 'zpool-create:' + createZPoolName.trim();
    upsertTask({
      id: taskId,
      kind: 'storage',
      title: `Creando ZPool: ${createZPoolName.trim()} (${createZPoolTopology})`,
      pct: 35,
      message: 'Creando conjunto ZFS y configuración...',
      status: 'running',
    });
    try {
      await api.createHostZpool({
        name: createZPoolName.trim(),
        topology: createZPoolTopology,
        devices: createZPoolDisks,
        ashift: Number(createZPoolAshift) || 12,
        compression: createZPoolCompression || 'lz4',
        mount_point: createZPoolMountPoint.trim() || undefined,
        auto_unmount: createZPoolAutoUnmount,
      });
      finishTask(taskId, 'success', `ZPool ${createZPoolName.trim()} creado con éxito`, 100);
      showCreateZPool = false;
      onRefresh();
    } catch (e) {
      finishTask(taskId, 'error', e.message || 'Error al crear ZPool', 35);
      showStorageErr('Create ZFS Pool Error', e);
    } finally {
      creatingZPool = false;
    }
  }

  function doDestroyZPool(poolName) {
    askConfirm({
      title: t('storage.destroyZPoolTitle', { name: poolName }),
      description: t('storage.destroyZPoolDesc'),
      confirmLabel: t('storage.destroyZPool'),
      variant: 'destructive',
      onConfirm: async () => {
        try {
          await api.destroyHostZpool(poolName);
          toast.success(t('storage.zpoolDestroyed'));
          onRefresh();
        } catch (e) {
          toast.error(e.message || 'Error al destruir ZPool');
        }
      },
    });
  }

  function openCreateZVolDialog(defaultPool = '') {
    createZVolPool = defaultPool || zpools[0]?.name || '';
    createZVolName = '';
    createZVolSizeGB = 20;
    createZVolSparse = true;
    showCreateZVol = true;
  }

  async function doCreateZVol() {
    if (!createZVolPool || !createZVolName || createZVolSizeGB <= 0) return;
    creatingZVol = true;
    try {
      await api.createHostZvol({
        pool: createZVolPool,
        name: createZVolName.trim(),
        size_gb: Number(createZVolSizeGB),
        sparse: createZVolSparse,
      });
      showCreateZVol = false;
      onRefresh();
    } catch (e) {
      showStorageErr('Create ZVol Error', e);
    } finally {
      creatingZVol = false;
    }
  }

  async function handleScrub(poolName, action = 'start') {
    const taskId = `zpool-scrub:${poolName}`;
    upsertTask({
      id: taskId,
      kind: 'storage',
      title: `ZFS Scrub: ${poolName}`,
      pct: 50,
      message:
        action === 'stop'
          ? 'Deteniendo verificación...'
          : 'Ejecutando verificación de integridad...',
      status: 'running',
    });
    scrubbingPool[poolName] = true;
    try {
      await api.scrubZpool(poolName, action);
      finishTask(
        taskId,
        'success',
        action === 'stop'
          ? `Scrub cancelado para ${poolName}`
          : `Scrub en progreso para ${poolName}`,
        100
      );
      toast.success(
        action === 'stop' ? `Scrub cancelado para ${poolName}` : `Scrub iniciado para ${poolName}`
      );
      onRefresh();
    } catch (e) {
      finishTask(taskId, 'error', e.message || 'Error en ZFS scrub', 50);
      toast.error('Error en ZFS scrub: ' + e.message);
    } finally {
      scrubbingPool[poolName] = false;
    }
  }
</script>

<div class="p-4 sm:p-5 space-y-6">
  <!-- ZFS POOLS SECTION -->
  <div class="space-y-3">
    <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
      <div>
        <h3 class="text-sm font-bold text-foreground">{t('storage.zfsPoolsTitle')}</h3>
        <p class="text-xs text-muted-foreground">{t('storage.zfsPoolsDesc')}</p>
      </div>
      <div class="flex items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          class="!h-8 !text-xs"
          onclick={onRefresh}
          disabled={zfsLoading}
        >
          <Icon name="refresh" size={13} class={zfsLoading ? 'animate-spin mr-1' : 'mr-1'} />
          {t('common.refresh')}
        </Button>
        {#if auth.isAdmin()}
          <Button size="sm" class="!h-8 !text-xs gap-1.5" onclick={openCreateZPoolDialog}>
            <Icon name="plus" size={13} />
            {t('storage.createZPoolBtn')}
          </Button>
        {/if}
      </div>
    </div>

    {#if zfsLoading}
      <div class="flex items-center justify-center p-8 text-xs text-muted-foreground">
        <Icon name="refresh" size={16} class="animate-spin mr-2" />
        {t('common.loading')}
      </div>
    {:else if zpools.length === 0}
      <div
        class="p-8 text-center text-xs text-muted-foreground bg-muted/10 rounded-xl border border-dashed border-border"
      >
        <p>{t('storage.noZPools')}</p>
      </div>
    {:else}
      <div class="border border-border rounded-xl bg-card overflow-hidden">
        <div class="overflow-x-auto">
          <table class="w-full text-left text-xs border-collapse">
            <thead>
              <tr class="border-b border-border bg-muted/30 text-muted-foreground font-semibold">
                <th class="p-3">{t('common.name')}</th>
                <th class="p-3">{t('storage.colHealth')}</th>
                <th class="p-3">{t('common.size')}</th>
                <th class="p-3">{t('storage.colAllocated')}</th>
                <th class="p-3">{t('storage.colFree')}</th>
                <th class="p-3">{t('storage.colDisks')}</th>
                {#if auth.isAdmin()}
                  <th class="p-3 text-right">{t('storage.colActions')}</th>
                {/if}
              </tr>
            </thead>
            <tbody class="divide-y divide-border">
              {#each zpools as pool (pool.name)}
                <tr class="hover:bg-muted/10 transition-colors">
                  <td class="p-3 font-mono font-medium text-foreground">{pool.name}</td>
                  <td class="p-3">
                    <span
                      class="inline-flex items-center px-2 py-0.5 rounded text-[11px] font-medium {pool.health ===
                      'ONLINE'
                        ? 'bg-success/15 text-success'
                        : pool.health === 'DEGRADED'
                          ? 'bg-warning/15 text-warning font-semibold'
                          : 'bg-destructive/15 text-destructive font-semibold'}"
                    >
                      {pool.health}
                    </span>
                  </td>
                  <td class="p-3 font-mono">{pool.size_human}</td>
                  <td class="p-3 font-mono">{pool.alloc_human}</td>
                  <td class="p-3 font-mono">{pool.free_human}</td>
                  <td class="p-3 font-mono text-[11px] text-muted-foreground">
                    {pool.devices && pool.devices.length > 0 ? pool.devices.join(', ') : '-'}
                  </td>
                  {#if auth.isAdmin()}
                    <td class="p-3 text-right">
                      <div class="flex items-center justify-end gap-1.5">
                        <Button
                          variant="outline"
                          size="sm"
                          class="!h-7 !text-[11px] gap-1"
                          disabled={scrubbingPool[pool.name]}
                          onclick={() => handleScrub(pool.name, 'start')}
                        >
                          Scrub
                        </Button>
                        <Button
                          variant="outline"
                          size="sm"
                          class="!h-7 !text-[11px] gap-1"
                          onclick={() => openCreateZVolDialog(pool.name)}
                        >
                          <Icon name="plus" size={11} />
                          {t('storage.createZVolBtn')}
                        </Button>
                        <Button
                          variant="destructive"
                          size="sm"
                          class="!h-7 !text-[11px] gap-1"
                          onclick={() => doDestroyZPool(pool.name)}
                        >
                          <Icon name="trash" size={11} />
                          {t('storage.destroyZPool')}
                        </Button>
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

  <!-- ZFS VOLUMES (ZVOLS) SECTION -->
  <div class="space-y-3 pt-2 border-t border-border">
    <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
      <div>
        <h3 class="text-sm font-bold text-foreground">{t('storage.zfsVolsTitle')}</h3>
        <p class="text-xs text-muted-foreground">{t('storage.zfsVolsDesc')}</p>
      </div>
      {#if auth.isAdmin() && zpools.length > 0}
        <Button size="sm" class="!h-8 !text-xs gap-1.5" onclick={() => openCreateZVolDialog('')}>
          <Icon name="plus" size={13} />
          {t('storage.createZVolBtn')}
        </Button>
      {/if}
    </div>

    {#if zfsLoading}
      <div class="flex items-center justify-center p-8 text-xs text-muted-foreground">
        <Icon name="refresh" size={16} class="animate-spin mr-2" />
        {t('common.loading')}
      </div>
    {:else if zvols.length === 0}
      <div
        class="p-8 text-center text-xs text-muted-foreground bg-muted/10 rounded-xl border border-dashed border-border"
      >
        <p>{t('storage.noZVols')}</p>
      </div>
    {:else}
      <div class="border border-border rounded-xl bg-card overflow-hidden">
        <div class="overflow-x-auto">
          <table class="w-full text-left text-xs border-collapse">
            <thead>
              <tr class="border-b border-border bg-muted/30 text-muted-foreground font-semibold">
                <th class="p-3">{t('common.name')}</th>
                <th class="p-3">{t('storage.targetPoolLabel')}</th>
                <th class="p-3">{t('common.size')}</th>
                <th class="p-3">{t('storage.colDevice')}</th>
                <th class="p-3">{t('common.status')}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-border">
              {#each zvols as zvol (zvol.name)}
                <tr class="hover:bg-muted/10 transition-colors">
                  <td class="p-3 font-mono font-medium text-foreground">{zvol.name}</td>
                  <td class="p-3 font-mono text-muted-foreground">{zvol.pool}</td>
                  <td class="p-3 font-mono">{formatBytes(zvol.volsize)}</td>
                  <td class="p-3 font-mono text-[11px] text-muted-foreground">{zvol.device}</td>
                  <td class="p-3">
                    {#if zvol.used_by}
                      <span
                        class="inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] bg-warning/15 text-warning font-medium"
                      >
                        <Icon name="computer" size={11} />
                        {zvol.used_by.vm_name} ({zvol.used_by.target})
                      </span>
                    {:else}
                      <span
                        class="inline-flex items-center px-2 py-0.5 rounded text-[11px] bg-success/15 text-success font-medium"
                      >
                        Free
                      </span>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </div>
    {/if}
  </div>
</div>

<!-- Create ZFS Pool Dialog -->
<Dialog.Root bind:open={showCreateZPool}>
  <Dialog.Content class="sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>{t('storage.createZPoolTitle')}</Dialog.Title>
      <Dialog.Description>{t('storage.createZPoolDesc')}</Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3.5 my-2 text-xs">
      <div>
        <label for="zpool-name" class="block font-semibold mb-1">{t('storage.poolNameLabel')}</label
        >
        <Input
          id="zpool-name"
          bind:value={createZPoolName}
          placeholder={t('storage.zpoolNamePlaceholder')}
          class="w-full font-mono"
        />
      </div>

      <div>
        <label for="zpool-topology" class="block font-semibold mb-1"
          >{t('storage.topologyLabel')}</label
        >
        <select
          id="zpool-topology"
          bind:value={createZPoolTopology}
          class="input w-full font-medium text-xs"
        >
          <option value="stripe">{t('storage.topologyStripe')}</option>
          <option value="mirror">{t('storage.topologyMirror')}</option>
          <option value="raidz1">{t('storage.topologyRaidz1')}</option>
          <option value="raidz2">{t('storage.topologyRaidz2')}</option>
          <option value="raidz3">{t('storage.zfsRaidz3')}</option>
        </select>
      </div>

      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="zpool-ashift" class="block font-semibold mb-1"
            >{t('storage.zfsAshift')}</label
          >
          <select id="zpool-ashift" bind:value={createZPoolAshift} class="input w-full font-mono text-xs">
            <option value={12}>12 (4 KB — Estándar SSD/HDD)</option>
            <option value={9}>9 (512 B — Discos legados)</option>
            <option value={13}>13 (8 KB — NVMe avanzado)</option>
          </select>
        </div>
        <div>
          <label for="zpool-compression" class="block font-semibold mb-1"
            >{t('storage.zfsCompression')}</label
          >
          <select id="zpool-compression" bind:value={createZPoolCompression} class="input w-full font-mono text-xs">
            <option value="lz4">lz4 (Recomendado, rápido)</option>
            <option value="zstd">zstd (Alta compresión)</option>
            <option value="gzip">gzip</option>
            <option value="off">off (Desactivada)</option>
          </select>
        </div>
      </div>

      <div>
        <label for="zpool-mount" class="block font-semibold mb-1"
          >{t('storage.zfsMountPoint')}</label
        >
        <Input
          id="zpool-mount"
          bind:value={createZPoolMountPoint}
          placeholder="/mnt/{createZPoolName.trim() || 'zpool'}"
          class="w-full font-mono text-xs"
        />
      </div>

      <div>
        <span class="block font-semibold mb-1">{t('storage.selectDisksLabel')}</span>
        <div
          class="max-h-48 overflow-y-auto space-y-1.5 p-2 rounded-lg border border-border bg-muted/10"
        >
          {#each hostDisks.filter((d) => !d.is_system) as disk (disk.path)}
            <label
              class="flex items-center gap-2.5 p-1.5 rounded hover:bg-muted/20 cursor-pointer select-none"
            >
              <input
                type="checkbox"
                value={disk.path}
                checked={createZPoolDisks.includes(disk.path)}
                onchange={(e) => {
                  if (e.target.checked) {
                    createZPoolDisks = [...createZPoolDisks, disk.path];
                  } else {
                    createZPoolDisks = createZPoolDisks.filter((p) => p !== disk.path);
                  }
                }}
                class="w-4 h-4 rounded border-border"
              />
              <span class="font-mono text-xs font-medium text-foreground">{disk.path}</span>
              <span class="text-[11px] text-muted-foreground"
                >({disk.size_human} · {disk.model || 'Disk'})</span
              >
            </label>
          {:else}
            <p class="text-xs text-muted-foreground p-2">{t('storage.noHostDisks')}</p>
          {/each}
        </div>
        {#if createZPoolDisks.length < minDisksForTopology(createZPoolTopology)}
          <p class="text-[11px] text-warning mt-1">
            {t('storage.minDisksRequired', {
              topology: createZPoolTopology,
              min: minDisksForTopology(createZPoolTopology),
            })}
          </p>
        {/if}

        <label class="flex items-center gap-2 mt-2 cursor-pointer select-none">
          <input
            type="checkbox"
            bind:checked={createZPoolAutoUnmount}
            class="w-4 h-4 rounded border-border text-accent focus:ring-accent"
          />
          <span class="text-xs text-foreground font-medium">{t('storage.raidAutoUnmount')}</span>
        </label>
      </div>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showCreateZPool = false)} disabled={creatingZPool}>
        {t('common.cancel')}
      </Button>
      <Button
        onclick={doCreateZPool}
        disabled={creatingZPool ||
          !createZPoolName.trim() ||
          createZPoolDisks.length < minDisksForTopology(createZPoolTopology)}
      >
        {#if creatingZPool}
          <Spinner size="sm" color="text-white" /> {t('storage.creatingZPool')}
        {:else}
          <Icon name="check" size={14} class="mr-1" />
          {t('storage.createZPoolBtn')}
        {/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Create ZFS Volume (zvol) Dialog -->
<Dialog.Root bind:open={showCreateZVol}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('storage.createZVolTitle')}</Dialog.Title>
      <Dialog.Description>{t('storage.createZVolDesc')}</Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3.5 my-2 text-xs">
      <div>
        <label for="zvol-pool" class="block font-semibold mb-1"
          >{t('storage.targetPoolLabel')}</label
        >
        <select id="zvol-pool" bind:value={createZVolPool} class="input w-full font-mono text-xs">
          {#each zpools as pool (pool.name)}
            <option value={pool.name}>{pool.name} ({pool.free_human} free)</option>
          {/each}
        </select>
      </div>

      <div>
        <label for="zvol-name" class="block font-semibold mb-1">{t('storage.zvolNameLabel')}</label>
        <Input
          id="zvol-name"
          bind:value={createZVolName}
          placeholder={t('storage.zvolNamePlaceholder')}
          class="w-full font-mono"
        />
      </div>

      <div>
        <label for="zvol-size" class="block font-semibold mb-1">{t('storage.sizeGbLabel')}</label>
        <Input
          id="zvol-size"
          type="number"
          min="1"
          bind:value={createZVolSizeGB}
          class="w-full font-mono"
        />
      </div>

      <div class="pt-2 border-t border-border">
        <label class="flex items-start gap-2.5 cursor-pointer select-none">
          <input
            type="checkbox"
            bind:checked={createZVolSparse}
            class="w-4 h-4 rounded border-border mt-0.5"
          />
          <div class="min-w-0">
            <span class="text-xs font-medium">{t('storage.sparseLabel')}</span>
            <span class="text-[11px] text-muted-foreground block">{t('storage.sparseHint')}</span>
          </div>
        </label>
      </div>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showCreateZVol = false)} disabled={creatingZVol}>
        {t('common.cancel')}
      </Button>
      <Button
        onclick={doCreateZVol}
        disabled={creatingZVol ||
          !createZVolPool ||
          !createZVolName.trim() ||
          createZVolSizeGB <= 0}
      >
        {#if creatingZVol}
          <Spinner size="sm" color="text-white" /> {t('storage.creatingZVol')}
        {:else}
          <Icon name="check" size={14} class="mr-1" />
          {t('storage.createZVolBtn')}
        {/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
