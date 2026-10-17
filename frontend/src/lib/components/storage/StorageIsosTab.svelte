<script>
  import { onDestroy } from 'svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button, buttonVariants } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import * as Dialog from '$lib/components/ui/dialog';
  import Spinner from '$lib/components/Spinner.svelte';
  import MoveVolumeDialog from './MoveVolumeDialog.svelte';
  import { cn } from '$lib/utils.js';
  import { toast } from '$lib/components/ui/toast';
  import { upsertTask, updateTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t, htmlVar } from '$lib/i18n.svelte.js';
  import { hasPurpose, purposeLabel, movablePools } from '$lib/purpose.js';

  let {
    pools = [],
    selectedISOPool = $bindable('__all__'),
    askConfirm = () => {},
    showStorageErr = () => {},
  } = $props();

  let isos = $state([]);
  let loadingIsos = $state(false);
  let isoSearch = $state('');
  let isoTargetPool = $state('');

  // ISO Download Dialog & State
  let showDownloadISO = $state(false);
  let downloadURL = $state('');
  let downloadName = $state('');
  let downloading = $state(false);
  let downloadProgress = $state(0);
  let downloadMessage = $state('');
  let downloadInterval = $state(null);

  // Upload ISO State
  let uploadFiles = $state(null);
  let uploading = $state(false);
  let uploadProgress = $state(0);

  // Rename ISO State
  let showRenameISO = $state(false);
  let renameOldName = $state('');
  let renameNewName = $state('');
  let renamePool = $state('');
  let renaming = $state(false);

  // Move ISO State
  let showMoveVol = $state(false);
  let moveVolName = $state('');
  let moveVolSrcPool = $state('');

  const isoPools = $derived(pools.filter((p) => hasPurpose(p, 'iso')));

  $effect(() => {
    if (!isoTargetPool || !isoPools.find((p) => p.name === isoTargetPool)) {
      isoTargetPool = isoPools[0]?.name || pools[0]?.name || '';
    }
  });

  $effect(() => {
    loadIsos(selectedISOPool);
  });

  onDestroy(() => {
    if (downloadInterval) clearInterval(downloadInterval);
  });

  async function loadIsos(pool) {
    loadingIsos = true;
    try {
      isos =
        (await api
          .listISOs(pool === '__all__' ? undefined : pool)
          .catch(() => [])) || [];
    } finally {
      loadingIsos = false;
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

  const filteredISOs = $derived(
    isos.filter(
      (iso) => !isoSearch.trim() || iso.name.toLowerCase().includes(isoSearch.toLowerCase())
    )
  );

  function canMoveTo(kind, currentPool) {
    return movablePools(pools, kind, currentPool).length > 0;
  }

  function openRenameISO(iso) {
    renameOldName = iso.name;
    renameNewName = iso.name;
    renamePool = iso.pool || isoTargetPool;
    showRenameISO = true;
  }

  async function doRenameISO() {
    if (!renameOldName || !renameNewName || renameNewName === renameOldName) {
      showRenameISO = false;
      return;
    }
    renaming = true;
    try {
      await api.renameISO(renameOldName, renameNewName, renamePool);
      showRenameISO = false;
      renameOldName = '';
      renameNewName = '';
      toast.success(t('storage.isoRenamed'));
      loadIsos(selectedISOPool);
    } catch (e) {
      toast.error(e.message);
    } finally {
      renaming = false;
    }
  }

  function openMoveIso(pool, name) {
    moveVolName = name;
    moveVolSrcPool = pool;
    showMoveVol = true;
  }

  function deleteISO(iso) {
    const name = iso.name;
    askConfirm({
      title: t('storage.deleteIsoTitle', { name }),
      description: t('storage.deleteIsoDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        try {
          await api.deleteISO(name, iso.pool || isoTargetPool);
          toast.success(t('storage.isoDeleted', { name }));
          loadIsos(selectedISOPool);
        } catch (e) {
          showStorageErr(t('storage.deleteIsoTitle', { name }), e);
        }
      },
    });
  }

  async function handleDownloadISO() {
    if (!downloadURL) return;
    if (!isoTargetPool) {
      toast.error(t('storage.noIsoPool'));
      return;
    }
    downloading = true;
    downloadProgress = 0;
    downloadMessage = t('storage.startingDownload');
    let taskId = null;
    try {
      const data = await api.downloadISO(downloadURL, downloadName || undefined, isoTargetPool);
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
        downloadInterval = setInterval(async () => {
          try {
            const job = await api.getDownloadJob(jobId);
            if (!job) return;
            if (job.status === 'queued') {
              downloadMessage = t('storage.waitingInQueue');
            } else if (job.status === 'downloading') {
              downloadProgress = job.progress || 0;
              downloadMessage = `${t('storage.downloadingButton')}... ${Math.round(downloadProgress)}%`;
              updateTask(taskId, { pct: downloadProgress, message: downloadMessage });
            } else if (job.status === 'completed') {
              clearInterval(downloadInterval);
              downloadInterval = null;
              finishTask(taskId, 'success', t('storage.downloadComplete'), 100);
              toast.success(t('storage.downloadComplete'));
              loadIsos(selectedISOPool);
              resolve();
            } else if (job.status === 'error') {
              clearInterval(downloadInterval);
              downloadInterval = null;
              finishTask(taskId, 'error', job.error || t('storage.downloadFailed'), downloadProgress);
              toast.error(job.error || t('storage.downloadFailed'));
              resolve();
            }
          } catch {
            // keep polling
          }
        }, 1000);
      });
    } catch (e) {
      if (taskId) finishTask(taskId, 'error', e.message, 0);
      toast.error(e.message);
    } finally {
      downloading = false;
      showDownloadISO = false;
      downloadURL = '';
      downloadName = '';
    }
  }

  async function handleUpload() {
    const file = uploadFiles?.[0];
    if (!file) return;
    if (!isoTargetPool) {
      toast.error(t('storage.noIsoPool'));
      return;
    }
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
        isoTargetPool
      );
      uploadFiles = null;
      finishTask(taskId, 'success', t('storage.isoUploaded'), 100);
      toast.success(t('storage.isoUploaded'));
      loadIsos(selectedISOPool);
    } catch (e) {
      finishTask(taskId, 'error', e.message, uploadProgress || 0);
      toast.error(e.message);
    } finally {
      uploading = false;
    }
  }
</script>

<div class="p-4 sm:p-5 space-y-4">
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
    <div class="flex flex-wrap items-center gap-3">
      <select
        bind:value={selectedISOPool}
        class="input !py-1 !text-xs !w-auto"
      >
        <option value="__all__">{t('storage.allPoolsOption')}</option>
        {#each pools as p (p.name)}
          <option value={p.name}>{p.name} ({purposeLabel(p)})</option>
        {/each}
      </select>

      <div class="relative w-48 sm:w-60">
        <Icon
          name="search"
          size={13}
          class="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
        />
        <input
          type="text"
          placeholder={t('storage.searchIsoImages')}
          bind:value={isoSearch}
          class="w-full pl-7 pr-2.5 py-1 rounded-lg bg-background border border-border text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
        />
      </div>
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <select
        bind:value={isoTargetPool}
        class="input !py-1 !text-xs !w-auto"
        title={t('storage.isoTargetPool')}
      >
        {#if isoPools.length === 0}
          <option value="">{t('storage.noIsoPool')}</option>
        {/if}
        {#each isoPools as p (p.name)}
          <option value={p.name}>{t('storage.isoTargetPool')}: {p.name}</option>
        {/each}
      </select>

      <Button
        size="sm"
        variant="outline"
        class="!h-8 !text-xs"
        onclick={() => (showDownloadISO = !showDownloadISO)}
        disabled={!isoTargetPool}
      >
        <Icon name="download" size={13} class="mr-1" />
        {t('storage.downloadUrl')}
      </Button>

      <Input
        type="file"
        accept=".iso,.img"
        bind:files={uploadFiles}
        onchange={handleUpload}
        class="hidden"
        id="iso-upload-global"
      />
      <label
        for="iso-upload-global"
        class={cn(
          buttonVariants({ size: 'sm' }),
          'shadow-sm',
          isoTargetPool ? 'cursor-pointer' : 'opacity-50 cursor-not-allowed'
        )}
      >
        <Icon name="upload" size={13} class="mr-1" />
        {uploading ? t('storage.uploadProgress') : t('storage.uploadIso')}
      </label>
    </div>
  </div>

  {#if showDownloadISO}
    <div class="bg-muted/30 rounded-lg p-3.5 border border-border space-y-2">
      <div class="text-xs text-muted-foreground">{t('storage.downloadIsoFromUrl')}</div>
      <div class="flex flex-wrap gap-2 items-end">
        <Input
          bind:value={downloadURL}
          placeholder="https://releases.ubuntu.com/24.04/ubuntu-24.04-desktop-amd64.iso"
          class="flex-1 min-w-[280px]"
        />
        <Input
          bind:value={downloadName}
          placeholder={t('storage.filenameOptional')}
          class="w-48"
        />
        <Button
          onclick={handleDownloadISO}
          disabled={!downloadURL || downloading || !isoTargetPool}
          class="!h-8 !text-xs"
        >
          {downloading ? t('storage.downloadingButton') : t('common.download')}
        </Button>
      </div>
    </div>
  {/if}

  {#if uploading || downloading}
    <div class="bg-muted/30 rounded-lg p-3 border border-border space-y-2">
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

  {#if loadingIsos}
    <div class="flex justify-center py-12"><Spinner size="lg" /></div>
  {:else if filteredISOs.length === 0}
    <EmptyState compact icon="disc" title={t('storage.noIsos')} />
  {:else}
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
      {#each filteredISOs as iso (iso.name + (iso.pool || ''))}
        <div
          class="flex items-center justify-between p-3.5 rounded-xl border border-border bg-background shadow-xs"
        >
          <div class="flex items-center gap-3 min-w-0">
            <div
              class="w-8 h-8 rounded-lg bg-warning/10 text-warning flex items-center justify-center shrink-0"
            >
              <Icon name="disc" size={16} />
            </div>
            <div class="min-w-0">
              <span
                class="text-sm font-semibold text-foreground truncate block"
                title={iso.name}>{iso.name}</span
              >
              <div
                class="flex items-center gap-2 text-[11px] text-muted-foreground font-mono"
              >
                <span>{bytesToStr(iso.size)}</span>
                <span>•</span>
                <span>{t('storage.poolLabelShort')} {iso.pool || '—'}</span>
              </div>
            </div>
          </div>
          <div class="flex items-center gap-1 shrink-0">
            <button
              onclick={() => openRenameISO(iso)}
              class="p-1.5 rounded-md text-muted-foreground hover:text-accent hover:bg-muted"
              aria-label={`${t('storage.rename')} ${iso.name}`}
            >
              <Icon name="pencil" size={15} />
            </button>
            {#if canMoveTo('iso', iso.pool || isoTargetPool)}
              <button
                onclick={() => openMoveIso(iso.pool || isoTargetPool, iso.name)}
                class="p-1.5 rounded-md text-muted-foreground hover:text-accent hover:bg-muted"
                aria-label={`${t('storage.moveVolume')} ${iso.name}`}
                title={t('storage.moveVolume')}
              >
                <Icon name="arrowRightLeft" size={15} />
              </button>
            {/if}
            <button
              onclick={() => deleteISO(iso)}
              class="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10"
              aria-label={`${t('common.delete')} ${iso.name}`}
            >
              <Icon name="trash" size={15} />
            </button>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Rename ISO Modal -->
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
        {@html t('storage.mustEndWithIso', {
          code: htmlVar('<code>.iso</code>'),
          code2: htmlVar('<code>.img</code>'),
        })}
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

<MoveVolumeDialog
  bind:open={showMoveVol}
  name={moveVolName}
  srcPool={moveVolSrcPool}
  kind="iso"
  {pools}
  onMoved={() => loadIsos(selectedISOPool)}
  onError={showStorageErr}
/>
