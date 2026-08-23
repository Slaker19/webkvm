<script>
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { formatBytes } from '$lib/format.js';
  import { upsertTask, updateTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { onMount } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import ErrorModal from '$lib/components/ErrorModal.svelte';
  import * as Dialog from '$lib/components/ui/dialog';
  import { navigate } from '$lib/router.svelte.js';
  import { t } from '../lib/i18n.svelte.js';

  let pools = $state([]);
  let volumes = $state([]);
  let selectedPool = $state('');
  let loading = $state(true);
  let error = $state('');
  let isos = $state([]);
  let uploading = $state(false);
  let uploadProgress = $state(0);
  let downloadProgress = $state(0);
  let downloading = $state(false);
  let downloadMessage = $state('');

  let showCreatePool = $state(false);
  let poolName = $state('');
  let poolPath = $state('');
  let poolPurpose = $state('disk');

  let showCreateVol = $state(false);
  let volName = $state('');
  let volSize = $state(20);
  let volFormat = $state('qcow2');

  let showResizeVol = $state(false);
  let resizeVolName = $state('');
  let resizeVolSize = $state(20);
  let resizeVolCurrent = $state(0);
  let resizeVolPool = $state('');

  let showDownloadISO = $state(false);
  let downloadURL = $state('');
  let downloadName = $state('');
  let selectedISOPool = $state('ISOS');

  let showRenameISO = $state(false);
  let renameOldName = $state('');
  let renameNewName = $state('');
  let renaming = $state(false);

  let selectedPoolInfo = $derived(pools.find((p) => p.name === selectedPool));
  let selectedPoolIsISO = $derived(selectedPoolInfo?.purpose === 'iso');

  // Confirm dialog state
  // Persistent error pop-up for destructive storage operations.
  let showStorageError = $state(false);
  let storageErrorTitle = $state('');
  let storageErrorMessage = $state('');

  function showStorageErr(title, e) {
    storageErrorTitle = title;
    storageErrorMessage = e?.message || String(e);
    showStorageError = true;
  }

  let confirmState = $state({
    open: false,
    title: '',
    description: '',
    confirmLabel: t('common.delete'),
    variant: 'destructive',
    onConfirm: () => {},
    loading: false,
  });

  onMount(() => load());

  async function load() {
    loading = true;
    error = '';
    try {
      pools = (await api.listPools()) || [];
      const diskPools = pools.filter((p) => p.purpose !== 'iso');
      if (!selectedPool || !pools.find((p) => p.name === selectedPool)) {
        selectedPool = diskPools[0]?.name || pools[0]?.name || '';
      }
      volumes = (selectedPool ? await api.listVolumes(selectedPool) : []) || [];
      const isoPools = pools.filter((p) => p.purpose === 'iso');
      if (!selectedISOPool || !isoPools.find((p) => p.name === selectedISOPool)) {
        selectedISOPool = isoPools[0]?.name || 'ISOS';
      }
      isos = (await api.listISOs(selectedISOPool)) || [];
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  function askConfirm(opts) {
    confirmState = { ...opts, open: true, loading: false };
  }

  async function createPool() {
    if (!poolName || !poolPath) return;
    try {
      await api.createPool({
        name: poolName,
        type: 'dir',
        path: poolPath,
        purpose: poolPurpose,
      });
      const createdName = poolName;
      poolName = '';
      poolPath = '';
      poolPurpose = 'disk';
      showCreatePool = false;
      toast.success(t('storage.poolCreated', { name: createdName }));
      await load();
    } catch (e) {
      toast.error(e.message);
    }
  }

  async function deletePool(name) {
    askConfirm({
      title: t('storage.deletePoolTitle', { name }),
      description: t('storage.deletePoolDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.deletePool(name);
          confirmState.open = false;
          toast.success(t('storage.poolDeleted', { name }));
          if (!pools.find((p) => p.name === selectedPool)) {
            selectedPool = pools[0]?.name || '';
          }
          await load();
        } catch (e) {
          confirmState.loading = false;
          showStorageErr(t('storage.deletePoolTitle', { name }), e);
        }
      },
    });
  }

  async function createVolume() {
    if (!volName) return;
    try {
      await api.createVolume({
        name: volName,
        pool: selectedPool,
        capacity: volSize,
        format: volFormat,
      });
      volName = '';
      volSize = 20;
      volFormat = 'qcow2';
      showCreateVol = false;
      toast.success(t('storage.volumeCreated', { name: volName || '' }));
      await load();
    } catch (e) {
      toast.error(e.message);
    }
  }

  async function deleteVolume(pool, name) {
    askConfirm({
      title: t('storage.deleteVolumeTitle', { name }),
      description: t('storage.deleteVolumeDesc', { pool }),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.deleteVolume(pool, name);
          confirmState.open = false;
          toast.success(t('storage.volumeDeleted', { name }));
          await load();
        } catch (e) {
          confirmState.loading = false;
          showStorageErr(t('storage.deleteVolumeTitle', { name }), e);
        }
      },
    });
  }

  async function resizeVolume() {
    if (!resizeVolName) return;
    try {
      await api.resizeVolume(resizeVolPool, resizeVolName, resizeVolSize);
      showResizeVol = false;
      resizeVolName = '';
      toast.success(t('storage.volumeResized'));
      await load();
    } catch (e) {
      toast.error(e.message);
    }
  }

  async function deleteISO(name) {
    askConfirm({
      title: t('storage.deleteIsoTitle', { name }),
      description: t('storage.deleteIsoDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.deleteISO(name, selectedISOPool);
          confirmState.open = false;
          toast.success(t('storage.isoDeleted', { name }));
          await load();
        } catch (e) {
          confirmState.loading = false;
          showStorageErr(t('storage.deleteIsoTitle', { name }), e);
        }
      },
    });
  }

  function openRenameISO(iso) {
    renameOldName = iso.name;
    renameNewName = iso.name;
    showRenameISO = true;
  }

  async function doRenameISO() {
    if (!renameOldName || !renameNewName || renameNewName === renameOldName) {
      showRenameISO = false;
      return;
    }
    renaming = true;
    try {
      await api.renameISO(renameOldName, renameNewName, selectedISOPool);
      showRenameISO = false;
      renameOldName = '';
      renameNewName = '';
      toast.success(t('storage.isoRenamed'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      renaming = false;
    }
  }

  async function handleDownloadISO() {
    if (!downloadURL) return;
    downloading = true;
    downloadProgress = 0;
    downloadMessage = t('storage.startingDownload');
    let intervalId;
    let taskId = null;
    try {
      const data = await api.downloadISO(downloadURL, downloadName || undefined, selectedISOPool);
      const jobId = data.job_id;
      if (!jobId) throw new Error(t('storage.noJobId'));
      taskId = 'download:' + jobId;
      upsertTask({
        id: taskId,
        kind: 'download',
        title: downloadName || t('storage.isoSectionTitle'),
        pct: 0,
        message: t('storage.startingDownload'),
        status: 'running',
      });

      await new Promise((resolve) => {
        intervalId = setInterval(async () => {
          try {
            const job = await api.getDownloadJob(jobId);
            if (!job) return;
            if (job.status === 'queued') {
              downloadMessage = t('storage.waitingInQueue');
              updateTask(taskId, { message: downloadMessage });
            } else if (job.status === 'downloading') {
              const pct =
                job.progress > 0 && job.progress < 0.01 ? 0 : Math.round(job.progress || 0);
              downloadProgress = pct;
              downloadMessage = t('storage.downloadingName', { name: job.name, pct });
              updateTask(taskId, { pct, message: downloadMessage });
            } else if (job.status === 'completed') {
              downloadProgress = 100;
              downloadMessage = t('storage.downloadComplete');
              clearInterval(intervalId);
              intervalId = null;
              downloadURL = '';
              downloadName = '';
              showDownloadISO = false;
              finishTask(taskId, 'success', downloadMessage, 100);
              toast.success(t('storage.downloadComplete'));
              resolve();
            } else if (job.status === 'error') {
              toast.error(job.error || t('storage.downloadFailed'));
              clearInterval(intervalId);
              intervalId = null;
              downloadMessage = '';
              downloadProgress = 0;
              finishTask(taskId, 'error', job.error || t('storage.downloadFailed'), 0);
              resolve();
            }
          } catch {
            /* ignore poll errors */
          }
        }, 500);
      });
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      if (intervalId) clearInterval(intervalId);
      downloading = false;
    }
  }

  let uploadFiles = $state(null);

  async function handleUpload() {
    const file = uploadFiles?.[0];
    if (!file) return;
    uploading = true;
    uploadProgress = 0;
    const taskId = 'upload:' + file.name;
    upsertTask({
      id: taskId,
      kind: 'upload',
      title: file.name,
      pct: 0,
      message: t('storage.uploadProgress'),
      status: 'running',
    });
    try {
      await api.uploadISO(
        file,
        (pct) => {
          uploadProgress = pct;
          updateTask(taskId, { pct });
        },
        selectedISOPool
      );
      uploadFiles = null;
      finishTask(taskId, 'success', t('storage.isoUploaded'), 100);
      toast.success(t('storage.isoUploaded'));
      await load();
    } catch (e) {
      finishTask(taskId, 'error', e.message, uploadProgress || 0);
      toast.error(e.message);
    } finally {
      uploading = false;
    }
  }

  function bytesToStr(b) {
    if (!b) return '0 B';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (b >= 1024 && i < u.length - 1) {
      b /= 1024;
      i++;
    }
    return b.toFixed(i > 0 ? 1 : 0) + ' ' + u[i];
  }

  function selectPool(name) {
    selectedPool = name;
    load();
  }

  // Group volumes into roots (real files) and snapshot children
  // (internal qcow2 snapshot views). Snapshots are rendered as
  // sub-rows beneath their parent disk, with a "Manage in <vm>" link
  // to the VmDetail snapshot tree instead of resize/delete.
  const volumeTree = $derived.by(() => buildVolumeTree(volumes));
  const vmNameById = $derived.by(() => buildVmNameById(volumes));

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
    // Stable sort by name so the UI doesn't shuffle on every refresh.
    const sortRec = (nodes) => {
      nodes.sort((a, b) => a.name.localeCompare(b.name));
      nodes.forEach((n) => sortRec(n.children));
    };
    sortRec(roots);
    return roots;
  }

  function buildVmNameById(_vols) {
    // volumes only carry snapshot_of_vm_id (UUID), not the name.
    // Fetch lazily; this is cheap because the VM list is already
    // in flight for the dashboard pages. We resolve names on demand
    // and cache the result for the lifetime of the page.
    return _vmNameByIdCache;
  }
  let _vmNameByIdCache = $state({});

  $effect(() => {
    // Whenever we see a new snapshot_of_vm_id, fetch the VM name
    // once and remember it.
    const ids = new Set();
    for (const v of volumes) {
      if (v.is_snapshot && v.snapshot_of_vm_id) ids.add(v.snapshot_of_vm_id);
    }
    for (const id of ids) {
      if (_vmNameByIdCache[id]) continue;
      api
        .getVM(id)
        .then((vm) => {
          if (vm && vm.name) _vmNameByIdCache = { ..._vmNameByIdCache, [id]: vm.name };
        })
        .catch(() => {});
    }
  });
</script>

<div class="p-6 max-w-6xl">
  <PageHeader title={t('storage.title')} subtitle={t('storage.subtitle')} />

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if loading}
    <div class="flex items-center justify-center py-24"><Spinner size="lg" /></div>
  {:else}
    <!-- Storage Pools -->
    <div class="border border-border rounded-lg bg-card p-5 mb-4">
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
          {t('storage.storagePools')}
        </h2>
        <Button size="sm" variant="outline" onclick={() => (showCreatePool = !showCreatePool)}>
          {showCreatePool ? t('common.cancel') : '+ ' + t('storage.createPool')}
        </Button>
      </div>

      {#if showCreatePool}
        <div class="bg-muted/30 rounded-md p-3 mb-3 border border-border space-y-3">
          <div class="text-sm font-medium">{t('storage.newPool')}</div>
          <div class="flex flex-wrap gap-2">
            <Input
              bind:value={poolName}
              placeholder={t('storage.poolName')}
              class="flex-1 min-w-[150px]"
            />
            <select bind:value={poolPurpose} class="input w-32">
              <option value="disk">{t('storage.vdi')}</option>
              <option value="iso">{t('storage.iso')}</option>
            </select>
          </div>
          <p class="text-xs text-muted-foreground">{t('storage.dirHint')}</p>
          <div class="flex flex-wrap gap-2">
            <Input
              bind:value={poolPath}
              placeholder="/path/to/pool (or an NFS/SMB mountpoint)"
              class="flex-1 min-w-[200px]"
            />
            <Button onclick={createPool}>{t('common.create')}</Button>
          </div>
        </div>
      {/if}

      <div class="space-y-1.5">
        {#if pools.filter((p) => p.purpose !== 'iso').length > 0}
          <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider mt-2 mb-1">
            {t('storage.vdiPools')}
          </div>
          {#each pools.filter((p) => p.purpose !== 'iso') as p (p.name)}
            <div
              class="flex items-center justify-between px-3 py-2 rounded-md border cursor-pointer transition-colors {selectedPool ===
              p.name
                ? 'border-accent/50 bg-accent/5'
                : 'border-border bg-background hover:bg-muted/30'}"
              onclick={() => selectPool(p.name)}
              onkeydown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault();
                  selectPool(p.name);
                }
              }}
              role="button"
              tabindex="0"
            >
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <span class="text-sm font-medium {selectedPool === p.name ? 'text-accent' : ''}"
                    >{p.name}</span
                  >
                  <span
                    class="text-[10px] px-1.5 py-0.5 rounded border border-accent/30 bg-accent/10 text-accent uppercase tracking-wide"
                    >VDI</span
                  >
                </div>
                {#if auth.role === 'admin'}
                  <p class="text-xs text-muted-foreground font-mono mt-0.5 truncate">{p.path}</p>
                {/if}
                {#if p.capacity > 0}
                  <div class="mt-1.5 max-w-[300px]">
                    <ProgressBar value={(p.allocated / p.capacity) * 100} size="sm" />
                    <div
                      class="flex items-center justify-between text-[10px] text-muted-foreground tnum mt-0.5"
                    >
                      <span>{formatBytes(p.allocated)} {t('storage.usedSpace')}</span>
                      <span>{formatBytes(p.capacity)}</span>
                    </div>
                  </div>
                {/if}
              </div>
              {#if auth.role === 'admin' || auth.role === 'operator'}
                <div class="flex items-center gap-1 shrink-0">
                  {#if auth.role === 'admin'}
                    <button
                      onclick={(e) => {
                        e.stopPropagation();
                        deletePool(p.name);
                      }}
                      class="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors"
                      aria-label={`${t('common.delete')} ${p.name}`}
                    >
                      <svg
                        class="w-4 h-4"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="2"
                        viewBox="0 0 24 24"
                      >
                        <path
                          d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                          stroke-linecap="round"
                          stroke-linejoin="round"
                        />
                      </svg>
                    </button>
                  {/if}
                </div>
              {/if}
            </div>
          {/each}
        {/if}
        {#if pools.filter((p) => p.purpose === 'iso').length > 0}
          <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider mt-3 mb-1">
            {t('storage.isoPools')}
          </div>
          {#each pools.filter((p) => p.purpose === 'iso') as p (p.name)}
            <div
              class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
            >
              <div class="min-w-0">
                <div class="flex items-center gap-2">
                  <span class="text-sm font-medium">{p.name}</span>
                  <span
                    class="text-[10px] px-1.5 py-0.5 rounded border border-warning/30 bg-warning/10 text-warning uppercase tracking-wide"
                    >ISO</span
                  >
                </div>
                {#if auth.role === 'admin'}
                  <p class="text-xs text-muted-foreground font-mono mt-0.5 truncate">{p.path}</p>
                {/if}
              </div>
              {#if auth.role === 'admin'}
                <button
                  onclick={(e) => {
                    e.stopPropagation();
                    deletePool(p.name);
                  }}
                  class="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors shrink-0"
                  aria-label={`${t('common.delete')} ${p.name}`}
                >
                  <svg
                    class="w-4 h-4"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"
                    viewBox="0 0 24 24"
                  >
                    <path
                      d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                      stroke-linecap="round"
                      stroke-linejoin="round"
                    />
                  </svg>
                </button>
              {/if}
            </div>
          {/each}
        {/if}
        {#if pools.length === 0}
          <EmptyState
            icon="disk"
            title={t('storage.noPools')}
            description={t('storage.noPoolsHint')}
          >
            {#snippet action()}
              <Button size="sm" onclick={() => (showCreatePool = true)}>
                {t('storage.createPool')}
              </Button>
            {/snippet}
          </EmptyState>
        {/if}
      </div>
    </div>

    <!-- Volumes -->
    <div class="border border-border rounded-lg bg-card p-5 mb-4">
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
          {t('storage.volumesTitle', { pool: selectedPool || t('storage.none') })}
          {#if selectedPoolIsISO}
            <span
              class="text-[10px] px-1.5 py-0.5 rounded border border-warning/30 bg-warning/10 text-warning uppercase tracking-wide ml-2"
              >{t('storage.isoPoolReadOnly')}</span
            >
          {/if}
        </h2>
        {#if !selectedPoolIsISO}
          <Button
            size="sm"
            variant="outline"
            onclick={() => {
              volName = '';
              volSize = 20;
              showCreateVol = true;
            }}>+ {t('storage.volumes')}</Button
          >
        {/if}
      </div>

      {#if showCreateVol && !selectedPoolIsISO}
        <div class="bg-muted/30 rounded-md p-3 mb-3 border border-border space-y-2">
          <div class="text-sm font-medium">{t('storage.newVolume')}</div>
          <div class="flex flex-wrap gap-2 items-end">
            <Input
              bind:value={volName}
              placeholder={t('storage.volumeName')}
              class="flex-1 min-w-[200px]"
            />
            <Input type="number" bind:value={volSize} min="1" class="w-24 tnum" />
            <span class="text-xs text-muted-foreground">GB</span>
            <select bind:value={volFormat} class="input w-32">
              <option value="qcow2">qcow2</option>
              <option value="raw">raw</option>
            </select>
            <Button onclick={createVolume}>{t('common.create')}</Button>
          </div>
        </div>
      {/if}

      {#if showResizeVol && !selectedPoolIsISO}
        <div class="bg-muted/30 rounded-md p-3 mb-3 border border-border space-y-2">
          <div class="text-sm font-medium">
            {t('storage.resizeVolume', { name: resizeVolName, current: resizeVolCurrent })}
          </div>
          <div class="flex flex-wrap gap-2 items-end">
            <Input
              type="number"
              min={resizeVolCurrent}
              bind:value={resizeVolSize}
              class="w-24 tnum"
            />
            <span class="text-xs text-muted-foreground">GB</span>
            <Button onclick={resizeVolume}>{t('storage.resize')}</Button>
            <Button variant="outline" onclick={() => (showResizeVol = false)}
              >{t('common.cancel')}</Button
            >
          </div>
        </div>
      {/if}

      {#if volumes.length === 0}
        <p class="text-sm text-muted-foreground">{t('storage.noVolumesInPool')}</p>
      {:else}
        <div class="space-y-1">
          {#each volumeTree as vol (vol.name)}
            <div class="border border-border rounded-md bg-background">
              <div class="flex items-center justify-between px-3 py-2">
                <div class="flex items-center gap-3 min-w-0">
                  <span class="text-sm truncate">{vol.name}</span>
                  <span class="text-xs text-muted-foreground tnum">{bytesToStr(vol.capacity)}</span>
                  {#if vol.allocation != null}
                    <span class="text-xs text-muted-foreground tnum"
                      >{t('storage.used', { size: bytesToStr(vol.allocation) })}</span
                    >
                  {/if}
                  {#if vol.children.length > 0}
                    <span
                      class="text-[10px] px-1.5 py-0.5 rounded border border-accent/30 bg-accent/10 text-accent uppercase tracking-wider"
                    >
                      {t('storage.snapshotCount', {
                        n: vol.children.length,
                        s: vol.children.length !== 1 ? 's' : '',
                      })}
                    </span>
                  {/if}
                </div>
                <div class="flex items-center gap-1 shrink-0">
                  {#if !selectedPoolIsISO}
                    <button
                      onclick={() => {
                        resizeVolName = vol.name;
                        resizeVolSize = vol.capacity / (1024 * 1024 * 1024);
                        resizeVolCurrent = vol.capacity / (1024 * 1024 * 1024);
                        resizeVolPool = selectedPool;
                        showResizeVol = true;
                      }}
                      class="text-xs text-accent hover:text-accent-hover px-2 py-1 rounded hover:bg-muted"
                      >{t('storage.resize')}</button
                    >
                  {/if}
                  <button
                    onclick={() => deleteVolume(selectedPool, vol.name)}
                    class="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                    aria-label={`${t('common.delete')} ${vol.name}`}
                  >
                    <svg
                      class="w-4 h-4"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="2"
                      viewBox="0 0 24 24"
                      ><polyline points="3 6 5 6 21 6" /><path
                        d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"
                      /></svg
                    >
                  </button>
                </div>
              </div>
              {#if vol.children.length > 0}
                <div class="ml-4 mr-3 mb-2 pl-3 border-l-2 border-dashed border-border space-y-1">
                  {#each vol.children as snap (snap.name)}
                    <div
                      class="flex items-center justify-between px-2 py-1.5 rounded bg-muted/30 text-xs"
                    >
                      <div class="flex items-center gap-2 min-w-0">
                        <svg
                          class="w-3 h-3 text-muted-foreground shrink-0"
                          fill="none"
                          stroke="currentColor"
                          stroke-width="2"
                          viewBox="0 0 24 24"
                          ><circle cx="12" cy="12" r="10" /><polyline
                            points="12 6 12 12 16 14"
                          /></svg
                        >
                        <span class="font-mono truncate" title={snap.name}>{snap.name}</span>
                        <span
                          class="text-[10px] px-1.5 py-0.5 rounded border border-border bg-background text-muted-foreground uppercase tracking-wider"
                          >{t('storage.internalSnapshot')}</span
                        >
                        <span class="text-muted-foreground tnum">{bytesToStr(snap.allocation)}</span
                        >
                      </div>
                      <button
                        onclick={() =>
                          navigate(`/vms/${snap.snapshot_of_vm_id}`, {
                            query: { tab: 'snapshots' },
                          })}
                        class="text-xs text-accent hover:text-accent-hover px-2 py-1 rounded hover:bg-muted shrink-0"
                        title={t('storage.openVm', {
                          vm: vmNameById[snap.snapshot_of_vm_id] || 'VM',
                        })}
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

    <!-- ISO Library -->
    <div class="border border-border rounded-lg bg-card p-5">
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
          {t('storage.isoLibrary')}
        </h2>
        <div class="flex items-center gap-2">
          <select
            bind:value={selectedISOPool}
            onchange={() =>
              api
                .listISOs(selectedISOPool)
                .then((data) => (isos = data || []))
                .catch((e) => (error = e.message))}
            class="input !py-1 !text-xs !w-auto"
          >
            {#each pools.filter((p) => p.purpose === 'iso') as p}
              <option value={p.name}>{p.name}</option>
            {/each}
          </select>
          <Button size="sm" variant="outline" onclick={() => (showDownloadISO = !showDownloadISO)}>
            {t('storage.downloadUrl')}
          </Button>
          <Input
            type="file"
            accept=".iso"
            bind:files={uploadFiles}
            onchange={handleUpload}
            class="hidden"
            id="iso-upload"
          />
          <label for="iso-upload" class="btn btn-primary !text-xs !h-7 cursor-pointer">
            {uploading ? t('storage.uploadProgress') : t('storage.uploadIso')}
          </label>
        </div>
      </div>

      {#if showDownloadISO}
        <div class="bg-muted/30 rounded-md p-3 mb-3 border border-border space-y-2">
          <div class="text-sm text-muted-foreground">{t('storage.downloadIsoFromUrl')}</div>
          <div class="flex flex-wrap gap-2 items-end">
            <Input
              bind:value={downloadURL}
              placeholder="https://releases.ubuntu.com/24.04/ubuntu-24.04-desktop-amd64.iso"
              class="flex-1 min-w-[300px]"
            />
            <Input
              bind:value={downloadName}
              placeholder={t('storage.filenameOptional')}
              class="w-48"
            />
            <Button onclick={handleDownloadISO} disabled={!downloadURL || downloading}>
              {downloading ? t('storage.downloadingButton') : t('common.download')}
            </Button>
          </div>
        </div>
      {/if}

      {#if uploading || downloading}
        <div class="mb-3 bg-muted/30 rounded-md p-3 border border-border space-y-3">
          {#if uploading}
            <ProgressBar
              value={uploadProgress}
              label={t('storage.uploadProgress')}
              showValue
              size="sm"
            />
          {/if}
          {#if downloading}
            <ProgressBar
              value={downloadProgress}
              label={downloadMessage || t('storage.downloadingButton')}
              showValue
              size="sm"
              variant="success"
            />
          {/if}
        </div>
      {/if}

      {#if isos.length === 0}
        <p class="text-sm text-muted-foreground">{t('storage.noIsos')}</p>
      {:else}
        <div class="space-y-1 max-h-72 overflow-y-auto">
          {#each isos as iso (iso.name)}
            <div
              class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
            >
              <div class="flex items-center gap-3 min-w-0">
                <svg
                  class="w-4 h-4 text-warning shrink-0"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.5"
                  viewBox="0 0 24 24"
                >
                  <circle cx="12" cy="12" r="10" /><circle cx="12" cy="12" r="6" /><circle
                    cx="12"
                    cy="12"
                    r="2"
                  />
                </svg>
                <div class="min-w-0">
                  <span class="text-sm truncate">{iso.name}</span>
                  <span class="text-xs text-muted-foreground ml-2 tnum">{bytesToStr(iso.size)}</span
                  >
                </div>
              </div>
              <div class="flex items-center gap-1">
                <button
                  onclick={() => openRenameISO(iso)}
                  class="p-1.5 rounded-md text-muted-foreground hover:text-accent hover:bg-muted"
                  aria-label={`${t('storage.rename')} ${iso.name}`}
                >
                  <svg
                    class="w-4 h-4"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"
                    viewBox="0 0 24 24"
                  >
                    <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7" />
                    <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" />
                  </svg>
                </button>
                <button
                  onclick={() => deleteISO(iso.name)}
                  class="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                  aria-label={`${t('common.delete')} ${iso.name}`}
                >
                  <svg
                    class="w-4 h-4"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="2"
                    viewBox="0 0 24 24"
                    ><polyline points="3 6 5 6 21 6" /><path
                      d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"
                    /></svg
                  >
                </button>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
</div>

<!-- Rename ISO Dialog -->
<Dialog.Root bind:open={showRenameISO}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('storage.renameIso')}</Dialog.Title>
      <Dialog.Description>{t('storage.currentIso', { name: renameOldName })}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-2">
      <label for="rename-iso-input" class="block text-sm font-medium">{t('storage.newName')}</label>
      <Input id="rename-iso-input" bind:value={renameNewName} placeholder="new-name.iso" />
      <p class="text-xs text-muted-foreground">
        {@html t('storage.mustEndWithIso', { code: '<code>.iso</code>' })}
      </p>
    </div>
    <Dialog.Footer class="gap-2">
      <Button
        variant="outline"
        onclick={() => {
          showRenameISO = false;
          renameOldName = '';
          renameNewName = '';
        }}
        disabled={renaming}>{t('common.cancel')}</Button
      >
      <Button
        onclick={doRenameISO}
        disabled={renaming || !renameNewName || renameNewName === renameOldName}
      >
        {renaming ? t('storage.renaming') : t('storage.rename')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Confirm Dialog -->
<ConfirmDialog
  bind:open={confirmState.open}
  title={confirmState.title}
  description={confirmState.description}
  confirmLabel={confirmState.confirmLabel}
  variant={confirmState.variant}
  loading={confirmState.loading}
  onConfirm={confirmState.onConfirm}
/>

<ErrorModal bind:open={showStorageError} title={storageErrorTitle} message={storageErrorMessage} />
