<script>
  import { SvelteSet } from 'svelte/reactivity';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button, buttonVariants } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import * as Dialog from '$lib/components/ui/dialog';
  import Spinner from '$lib/components/Spinner.svelte';
  import CreateVolumeInlineForm from '$lib/components/CreateVolumeInlineForm.svelte';
  import MoveVolumeDialog from './MoveVolumeDialog.svelte';
  import { cn } from '$lib/utils.js';
  import { toast } from '$lib/components/ui/toast';
  import { upsertTask, updateTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { api } from '$lib/stores/auth.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { hasPurpose, movablePools } from '$lib/purpose.js';

  let {
    pools = [],
    selectedDiskPool = $bindable(''),
    askConfirm = () => {},
    showStorageErr = () => {},
  } = $props();

  let volumes = $state([]);
  let loadingVolumes = $state(false);
  let diskSearch = $state('');
  let showCreateVol = $state(false);

  // Upload Disk State
  let uploadDiskFiles = $state(null);
  let uploadingDisk = $state(false);
  let uploadDiskProgress = $state(0);

  // Resize Volume State
  let showResizeVol = $state(false);
  let resizeVolName = $state('');
  let resizeVolSize = $state(20);
  let resizeVolCurrent = $state(0);
  let resizeVolPool = $state('');
  let volResizing = $state(false);

  // Move Volume State
  let showMoveVol = $state(false);
  let moveVolName = $state('');
  let moveVolSrcPool = $state('');

  const kvmPools = $derived(pools.filter((p) => hasPurpose(p, 'disk') && p.name !== 'default'));

  $effect(() => {
    if (selectedDiskPool) {
      loadVolumes(selectedDiskPool);
    }
  });

  async function loadVolumes(pool) {
    if (!pool) return;
    loadingVolumes = true;
    try {
      volumes = (await api.listVolumes(pool).catch(() => [])) || [];
    } finally {
      loadingVolumes = false;
    }
  }

  function bytesToStr(b) {
    if (!b && b !== 0) return '-';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (b >= 1024 && i < u.length - 1) {
      b /= 1024;
      i++;
    }
    return b.toFixed(i > 0 ? 1 : 0) + ' ' + u[i];
  }

  function buildVolumeTree(vols) {
    const byName = {};
    for (const v of vols) byName[v.name] = { ...v, children: [] };
    const roots = [];
    for (const k of Object.keys(byName)) {
      const node = byName[k];
      if (node.is_snapshot && node.parent_volume && byName[node.parent_volume]) {
        byName[node.parent_volume].children.push(node);
      } else {
        roots.push(node);
      }
    }
    const sortRec = (nodes) => {
      nodes.sort((a, b) => a.name.localeCompare(b.name));
      nodes.forEach((n) => sortRec(n.children));
    };
    sortRec(roots);
    return roots;
  }

  const volumeTree = $derived.by(() => {
    const list = volumes.filter(
      (v) => !diskSearch.trim() || v.name.toLowerCase().includes(diskSearch.toLowerCase())
    );
    return buildVolumeTree(list);
  });

  let _vmNameByIdCache = $state({});
  const pendingOwnerLookups = new SvelteSet();
  const vmNameById = $derived.by(() => _vmNameByIdCache);

  $effect(() => {
    const ids = new SvelteSet();
    for (const v of volumes) {
      if (v.is_snapshot && v.snapshot_of_vm_id) ids.add(v.snapshot_of_vm_id);
    }
    for (const id of ids) {
      if (vmNameById[id] || pendingOwnerLookups.has(id)) continue;
      pendingOwnerLookups.add(id);
      api
        .getVM(id)
        .then((vm) => {
          if (vm && vm.name) _vmNameByIdCache = { ..._vmNameByIdCache, [id]: vm.name };
        })
        .catch(() => {})
        .finally(() => pendingOwnerLookups.delete(id));
    }
  });

  function canMoveTo(kind, currentPool) {
    return movablePools(pools, kind, currentPool).length > 0;
  }

  function openMoveVolume(pool, name) {
    moveVolName = name;
    moveVolSrcPool = pool;
    showMoveVol = true;
  }

  async function handleUploadDisk() {
    const file = uploadDiskFiles?.[0];
    if (!file || !selectedDiskPool) return;
    uploadingDisk = true;
    uploadDiskProgress = 0;
    const taskId = 'upload-disk:' + file.name;
    upsertTask({
      id: taskId,
      kind: 'upload',
      title: file.name,
      pct: 0,
      message: t('storage.uploadProgress'),
      status: 'running',
    });
    try {
      await api.uploadDisk(
        file,
        (pct) => {
          uploadDiskProgress = pct;
          updateTask(taskId, { pct });
        },
        selectedDiskPool
      );
      uploadDiskFiles = null;
      finishTask(taskId, 'success', t('storage.diskUploaded'), 100);
      toast.success(t('storage.diskUploaded'));
      loadVolumes(selectedDiskPool);
    } catch (e) {
      finishTask(taskId, 'error', e.message, uploadDiskProgress || 0);
      toast.error(e.message);
    } finally {
      uploadingDisk = false;
    }
  }

  async function resizeVolume() {
    if (volResizing || !resizeVolName) return;
    volResizing = true;
    try {
      await api.resizeVolume(resizeVolPool, resizeVolName, resizeVolSize);
      showResizeVol = false;
      resizeVolName = '';
      toast.success(t('storage.volumeResized'));
      loadVolumes(selectedDiskPool);
    } catch (e) {
      toast.error(e.message);
    } finally {
      volResizing = false;
    }
  }

  function deleteVolume(pool, name) {
    askConfirm({
      title: t('storage.deleteVolumeTitle', { name }),
      description: t('storage.deleteVolumeDesc', { pool }),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        try {
          await api.deleteVolume(pool, name);
          toast.success(t('storage.volumeDeleted', { name }));
          loadVolumes(pool);
        } catch (e) {
          showStorageErr(t('storage.deleteVolumeTitle', { name }), e);
        }
      },
    });
  }
</script>

<div class="p-4 sm:p-5 space-y-4">
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
    <div class="flex flex-wrap items-center gap-3">
      <div class="flex items-center gap-2">
        <span class="text-xs font-semibold text-muted-foreground"
          >{t('storage.storagePoolLabel')}</span
        >
        <select
          bind:value={selectedDiskPool}
          class="input !py-1 !text-xs !w-auto font-medium"
        >
          {#each kvmPools as p (p.name)}
            <option value={p.name}>{p.name} ({p.type || 'dir'})</option>
          {/each}
        </select>
      </div>

      <div class="relative w-48 sm:w-60">
        <Icon
          name="search"
          size={13}
          class="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
        />
        <input
          type="text"
          placeholder={t('storage.searchDiskImages')}
          bind:value={diskSearch}
          class="w-full pl-7 pr-2.5 py-1 rounded-lg bg-background border border-border text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
        />
      </div>
    </div>

    <div class="flex items-center gap-2">
      <Input
        type="file"
        accept=".qcow2,.img,.raw,.qed,.vmdk,.ova,.vdi,.vhdx"
        bind:files={uploadDiskFiles}
        onchange={handleUploadDisk}
        class="hidden"
        id="disk-upload"
      />
      <label
        for="disk-upload"
        class={cn(buttonVariants({ size: 'sm' }), 'cursor-pointer shadow-sm')}
      >
        <Icon name="upload" size={13} class="mr-1" />
        {uploadingDisk ? t('storage.uploadProgress') : t('storage.uploadDiskOva')}
      </label>

      <Button
        size="sm"
        variant="outline"
        class="!h-8 !text-xs"
        onclick={() => (showCreateVol = true)}
      >
        <Icon name="plus" size={13} class="mr-1" />
        {t('storage.createVolume')}
      </Button>
    </div>
  </div>

  {#if uploadingDisk}
    <div class="bg-muted/30 rounded-lg p-3 border border-border">
      <ProgressBar
        value={uploadDiskProgress}
        label={t('storage.uploadProgress')}
        showValue
        size="sm"
      />
    </div>
  {/if}

  <CreateVolumeInlineForm
    bind:open={showCreateVol}
    pool={selectedDiskPool}
    onCreated={() => loadVolumes(selectedDiskPool)}
  />

  {#if loadingVolumes}
    <div class="flex justify-center py-12"><Spinner size="lg" /></div>
  {:else if volumeTree.length === 0}
    <EmptyState compact icon="disk" title={t('storage.noVolumesInPool')} />
  {:else}
    <div class="space-y-1.5">
      {#each volumeTree as vol (vol.name)}
        <div class="border border-border rounded-xl bg-background p-3 flex flex-col gap-2">
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-3 min-w-0">
              <Icon name="hardDrive" size={16} class="text-accent shrink-0" />
              <span class="text-sm font-semibold truncate">{vol.name}</span>
              <span class="text-xs text-muted-foreground font-mono tnum"
                >{bytesToStr(vol.capacity)}</span
              >
              {#if vol.allocation != null}
                <span class="text-xs text-muted-foreground font-mono tnum">
                  ({t('storage.used', { size: bytesToStr(vol.allocation) })})
                </span>
              {/if}
              {#if vol.children.length > 0}
                <span
                  class="text-[10px] px-1.5 py-0.5 rounded bg-accent/15 text-accent font-mono"
                >
                  {vol.children.length} snap{vol.children.length > 1 ? 's' : ''}
                </span>
              {/if}
            </div>

            <div class="flex items-center gap-1 shrink-0">
              <button
                onclick={() => {
                  resizeVolName = vol.name;
                  resizeVolSize = vol.capacity / (1024 * 1024 * 1024);
                  resizeVolCurrent = vol.capacity / (1024 * 1024 * 1024);
                  resizeVolPool = selectedDiskPool;
                  showResizeVol = true;
                }}
                class="text-xs text-accent hover:text-accent-hover px-2.5 py-1 rounded hover:bg-muted"
              >
                {t('storage.resize')}
              </button>
              {#if canMoveTo('disk', selectedDiskPool)}
                <button
                  onclick={() => openMoveVolume(selectedDiskPool, vol.name)}
                  class="p-1.5 rounded-md text-muted-foreground hover:text-accent hover:bg-muted"
                  aria-label={`${t('storage.moveVolume')} ${vol.name}`}
                  title={t('storage.moveVolume')}
                >
                  <Icon name="arrowRightLeft" size={15} />
                </button>
              {/if}
              <button
                onclick={() => deleteVolume(selectedDiskPool, vol.name)}
                class="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                aria-label={`${t('common.delete')} ${vol.name}`}
              >
                <Icon name="trash" size={15} />
              </button>
            </div>
          </div>

          {#if vol.children.length > 0}
            <div class="ml-4 pl-3 border-l-2 border-dashed border-border space-y-1 mt-1">
              {#each vol.children as snap (snap.name)}
                <div
                  class="flex items-center justify-between px-2.5 py-1.5 rounded bg-muted/30 text-xs"
                >
                  <div class="flex items-center gap-2 min-w-0">
                    <Icon name="clock" size={12} class="text-muted-foreground shrink-0" />
                    <span class="font-mono truncate" title={snap.name}>{snap.name}</span>
                    <span
                      class="text-[10px] px-1.5 py-0.2 rounded bg-background border border-border text-muted-foreground uppercase font-mono"
                    >
                      {t('storage.internalSnapshot')}
                    </span>
                    <span class="text-muted-foreground font-mono"
                      >{bytesToStr(snap.allocation)}</span
                    >
                  </div>
                  <button
                    onclick={() =>
                      navigate(`/vms/${snap.snapshot_of_vm_id}`, {
                        query: { tab: 'snapshots' },
                      })}
                    class="text-xs text-accent hover:text-accent-hover px-2 py-1 rounded hover:bg-muted shrink-0"
                  >
                    {t('storage.manageIn', {
                      vm: vmNameById[snap.snapshot_of_vm_id] || 'VM',
                    })} →
                  </button>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Volume Resize Modal -->
<Dialog.Root bind:open={showResizeVol}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title
        >{t('storage.resizeVolume', {
          name: resizeVolName,
          current: resizeVolCurrent,
        })}</Dialog.Title
      >
      <Dialog.Description>
        {t('storage.resizeVolumeDesc')}
      </Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3 my-2">
      <div>
        <label for="resize-vol-size" class="block text-xs font-semibold mb-1"
          >{t('storage.newSizeGbLabel')}</label
        >
        <Input
          id="resize-vol-size"
          type="number"
          min={resizeVolCurrent}
          bind:value={resizeVolSize}
          class="w-full tnum font-mono"
        />
      </div>
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showResizeVol = false)}>{t('common.cancel')}</Button>
      <Button disabled={volResizing || resizeVolSize <= resizeVolCurrent} onclick={resizeVolume}>
        {#if volResizing}<Spinner size="xs" class="mr-1" />{/if}
        {t('storage.resize')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<MoveVolumeDialog
  bind:open={showMoveVol}
  name={moveVolName}
  srcPool={moveVolSrcPool}
  kind="disk"
  {pools}
  onMoved={() => loadVolumes(selectedDiskPool)}
  onError={showStorageErr}
/>
