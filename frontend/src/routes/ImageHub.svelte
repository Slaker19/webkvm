<script>
  import { onMount } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import * as Dialog from '$lib/components/ui/dialog';
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import CardGridSkeleton from '$lib/components/CardGridSkeleton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import SnippetsTab from '$lib/components/SnippetsTab.svelte';
  import { formatBytes } from '$lib/format.js';
  import { t } from '$lib/i18n.svelte.js';
  import { movablePools, baseImagePools } from '$lib/purpose.js';
  import { navigate } from '$lib/router.svelte.js';
  import { upsertTask, updateTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { trackImageJob, onImageJobDone } from '$lib/stores/imageJobs.svelte.js';

  let activeTab = $state('containers'); // 'containers', 'cloud-base', 'isos', 'cloud-init'
  let loading = $state(true);
  let confirmState = $state({
    open: false,
    title: '',
    description: '',
    confirmLabel: t('common.delete'),
    variant: 'destructive',
    onConfirm: () => {},
    loading: false,
  });

  function askConfirm(opts) {
    confirmState = { ...opts, open: true, loading: false };
  }

  // Storage pools (used to scope ISO writes to pools actually tagged "iso").
  let pools = $state([]);
  let isoTargetPool = $state('');

  // Tab 1: Containers
  let containerImages = $state([]);
  let containerSearch = $state('');
  let pullingContainerRef = $state('');

  // Tab 2: Base Cloud Disks
  let baseCloudImages = $state([]);
  let baseSearch = $state('');
  let pullingBaseId = $state('');
  // Destination pool for a base image download. A base image is a
  // golden image rather than a disk in daily use, so template pools
  // are offered alongside disk pools (see baseImagePools).
  let baseTargetPool = $state('');
  // Relocating an already-cached base image. The copy outlives the
  // request, so the server answers with a job and the dialog closes
  // on submit rather than on completion.
  let showMoveBase = $state(false);
  let moveBaseImage = $state(null);
  let moveBaseDest = $state('');
  let moveBaseSubmitting = $state(false);

  // Where Incus caches downloaded container images. Unset, it is a
  // directory on the system disk, so on a host whose storage was
  // deliberately put elsewhere the cache grows on the one disk the
  // operator was trying to keep free.
  let incusVolume = $state(null);
  let showIncusVolume = $state(false);
  let incusVolumeDest = $state('');
  let incusVolumeSaving = $state(false);

  // Tab 3: ISOs
  let isos = $state([]);
  let isoSearch = $state('');
  let showDownloadIsoModal = $state(false);
  let isoDownloadUrl = $state('');
  let isoDownloadName = $state('');
  let downloadingIso = $state(false);
  let uploadingIso = $state(false);
  let uploadIsoProgress = $state(0);

  // Quick Launch Modal
  let quickLaunchItem = $state(null);
  let launchName = $state('');
  let launchNetwork = $state('');
  let launchType = $state('container');
  let networks = $state([]);
  let launching = $state(false);

  async function loadData() {
    loading = true;
    try {
      const [contRes, cloudRes, isoRes, netRes, poolRes, incusVolRes] = await Promise.all([
        api.listContainerImages().catch(() => []),
        api.listBaseCloudImages().catch(() => []),
        api.listISOs().catch(() => []),
        api.listNetworks().catch(() => []),
        api.listPools().catch(() => []),
        // Absent on a KVM-only host (501). Not an error worth showing:
        // the card simply does not appear.
        api.getIncusImagesVolume().catch(() => null),
      ]);
      incusVolume = incusVolRes;
      containerImages = contRes || [];
      baseCloudImages = cloudRes || [];
      isos = isoRes || [];
      networks = (netRes || []).filter((n) => n.active !== false);
      pools = poolRes || [];
      if (networks.length > 0 && !launchNetwork) {
        launchNetwork = networks[0].name;
      }
      const names = isoPools.map((p) => p.name);
      if (!isoTargetPool || !names.includes(isoTargetPool)) {
        isoTargetPool = names[0] || '';
      }
      const diskNames = diskPools.map((p) => p.name);
      if (!baseTargetPool || !diskNames.includes(baseTargetPool)) {
        baseTargetPool = diskNames[0] || '';
      }
    } catch (err) {
      toast.error(err.message || t('imagehub.loadError'));
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    loadData();
    return onImageJobDone(loadData);
  });

  // ISO writes are only allowed into pools explicitly tagged "iso".
  // Both lists drop inactive pools, matching baseImagePools/requireISOPool
  // on the server: an inactive pool has no mounted directory behind it,
  // so a download there would fill the root filesystem under an unmounted
  // mount point. Offering one only to have the server refuse it is worse
  // than not offering it.
  const isoPools = $derived(movablePools(pools, 'iso'));
  // Disk AND template pools: a base image is a golden image, and the
  // template purpose exists to keep those apart from the disks VMs are
  // actually running on.
  const diskPools = $derived(baseImagePools(pools));
  // Destinations for an already-cached image, minus the one it is in.
  const moveBaseTargets = $derived(baseImagePools(pools, moveBaseImage?.pool || ''));
  // The Incus image cache can only live in an Incus container pool,
  // which is what the server enforces too.
  const containerPools = $derived(movablePools(pools, 'container'));

  function openIncusVolume() {
    incusVolumeDest = incusVolume?.pool || '';
    showIncusVolume = true;
  }

  async function saveIncusVolume() {
    if (incusVolumeSaving) return;
    incusVolumeSaving = true;
    try {
      // Incus relocates the existing cache itself, which on a warm
      // cache is hundreds of MB of copying inside one request. The
      // dialog stays open and busy until the daemon answers rather
      // than pretending it was instant.
      incusVolume = await api.setIncusImagesVolume(incusVolumeDest);
      toast.success(t('imagehub.incusVolumeSaved'));
      showIncusVolume = false;
    } catch (err) {
      toast.error(err.message);
    } finally {
      incusVolumeSaving = false;
    }
  }

  // Containers Computed
  const filteredContainerImages = $derived.by(() => {
    let list = containerImages;
    const q = containerSearch.trim().toLowerCase();
    if (q) {
      list = list.filter(
        (img) =>
          img.name?.toLowerCase().includes(q) ||
          img.os?.toLowerCase().includes(q) ||
          img.release?.toLowerCase().includes(q) ||
          img.ref?.toLowerCase().includes(q) ||
          img.fingerprint?.toLowerCase().includes(q)
      );
    }
    return list;
  });

  const localContainerImages = $derived(containerImages.filter((img) => img.is_local));

  // Base Cloud Computed
  const filteredBaseCloudImages = $derived.by(() => {
    let list = baseCloudImages;
    const q = baseSearch.trim().toLowerCase();
    if (q) {
      list = list.filter(
        (img) =>
          img.name?.toLowerCase().includes(q) ||
          img.description?.toLowerCase().includes(q) ||
          img.id?.toLowerCase().includes(q)
      );
    }
    return list;
  });

  // ISOs Computed
  const filteredISOs = $derived.by(() => {
    let list = isos;
    const q = isoSearch.trim().toLowerCase();
    if (q) {
      list = list.filter(
        (iso) => iso.name?.toLowerCase().includes(q) || iso.pool?.toLowerCase().includes(q)
      );
    }
    return list;
  });

  // Pull Container Image
  async function pullContainer(ref) {
    pullingContainerRef = ref;
    try {
      const res = await api.pullContainerImage(ref);
      if (res?.job_id) {
        trackImageJob({
          jobId: res.job_id,
          taskId: 'img-pull:' + ref,
          title: `${t('imagehub.taskContainerTitle')} ${ref}`,
        });
      }
      toast.success(t('imagehub.containerPullStarted', { ref }));
    } catch (err) {
      toast.error(err.message || t('imagehub.downloadStartError'));
    } finally {
      pullingContainerRef = '';
    }
  }

  // Delete Container Image
  async function deleteContainer(fingerprint) {
    askConfirm({
      title: t('imagehub.deleteTitle'),
      description: t('imagehub.deleteContainerDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        try {
          confirmState.loading = true;
          await api.deleteContainerImage(fingerprint);
          confirmState.open = false;
          toast.success(t('imagehub.imageDeleted'));
          await loadData();
        } catch (err) {
          confirmState.loading = false;
          toast.error(err.message || t('imagehub.imageDeleteError'));
        }
      },
    });
  }

  // Pull Base Cloud QCOW2 Image
  async function pullBaseCloud(id) {
    pullingBaseId = id;
    try {
      const res = await api.pullBaseCloudImage(id, baseTargetPool);
      if (res?.job_id) {
        trackImageJob({
          jobId: res.job_id,
          taskId: 'base-cloud:' + id,
          title: `${t('imagehub.taskCloudTitle')} ${id}`,
        });
      }
      toast.success(t('imagehub.baseCloudPullStarted', { id }));
    } catch (err) {
      toast.error(err.message || t('imagehub.baseDownloadError'));
    } finally {
      pullingBaseId = '';
    }
  }

  // Delete Base Cloud Image
  async function deleteBaseCloud(id) {
    askConfirm({
      title: t('imagehub.deleteTitle'),
      description: t('imagehub.deleteBaseCloudDesc', { id }),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        try {
          confirmState.loading = true;
          await api.deleteBaseCloudImage(id);
          confirmState.open = false;
          toast.success(t('imagehub.baseDeleted'));
          await loadData();
        } catch (err) {
          confirmState.loading = false;
          toast.error(err.message || t('imagehub.baseDeleteError'));
        }
      },
    });
  }

  // Relocate a cached base image to another pool.
  //
  // The file is an ordinary volume in the pool, so this rides on the
  // generic volume-move endpoint instead of a bespoke one: same job,
  // same progress, same server-side purpose checks.
  function openMoveBase(b) {
    moveBaseImage = b;
    moveBaseDest = '';
    showMoveBase = true;
  }

  async function submitMoveBase() {
    if (moveBaseSubmitting || !moveBaseDest || !moveBaseImage) return;
    // An image still in the legacy DataDir/base-images folder has no
    // pool behind it, so there is no source pool to move it from.
    if (!moveBaseImage.pool) {
      toast.error(t('imagehub.moveBaseNoPool'));
      return;
    }
    moveBaseSubmitting = true;
    const fileName = `base-${moveBaseImage.id}.qcow2`;
    try {
      const res = await api.moveVolume(moveBaseImage.pool, fileName, { pool: moveBaseDest });
      if (res?.id) {
        trackImageJob({
          jobId: res.id,
          taskId: 'base-move:' + moveBaseImage.id,
          title: `${t('imagehub.taskMoveTitle')} ${moveBaseImage.id}`,
        });
      }
      showMoveBase = false;
      toast.success(t('imagehub.moveBaseStarted', { pool: moveBaseDest }));
    } catch (err) {
      toast.error(err.message || t('imagehub.moveBaseError'));
    } finally {
      moveBaseSubmitting = false;
    }
  }

  // ISO Actions
  async function submitDownloadIso() {
    if (!isoDownloadUrl.trim() || !isoDownloadName.trim()) {
      toast.error(t('imagehub.urlAndNameRequired'));
      return;
    }
    if (!isoTargetPool) {
      toast.error(t('imagehub.noIsoPool'));
      return;
    }
    downloadingIso = true;
    const isoName = isoDownloadName.trim();
    try {
      const res = await api.downloadISO(isoDownloadUrl.trim(), isoName, isoTargetPool);
      if (res?.job_id) {
        trackImageJob({
          jobId: res.job_id,
          taskId: 'iso-dl:' + res.job_id,
          title: `${t('imagehub.taskIsoTitle')} ${isoName}`,
        });
      }
      toast.success(t('imagehub.isoDownloadStarted'));
      showDownloadIsoModal = false;
      isoDownloadUrl = '';
      isoDownloadName = '';
    } catch (err) {
      toast.error(err.message || t('imagehub.isoDownloadError'));
    } finally {
      downloadingIso = false;
    }
  }

  async function handleIsoUpload(e) {
    const file = e.target.files?.[0];
    if (!file) return;
    if (!isoTargetPool) {
      toast.error(t('imagehub.noIsoPool'));
      return;
    }
    uploadingIso = true;
    uploadIsoProgress = 0;
    const taskId = 'iso-upload:' + file.name;
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
        (p) => {
          uploadIsoProgress = p;
          updateTask(taskId, { pct: p });
        },
        isoTargetPool
      );
      finishTask(taskId, 'success', t('storage.isoUploaded'), 100);
      toast.success(`ISO ${file.name} subida con éxito`);
      await loadData();
    } catch (err) {
      finishTask(taskId, 'error', err.message, uploadIsoProgress || 0);
      toast.error(err.message || t('imagehub.isoUploadError'));
    } finally {
      uploadingIso = false;
      uploadIsoProgress = 0;
    }
  }

  async function deleteIsoFile(iso) {
    askConfirm({
      title: t('storage.deleteIsoTitle'),
      description: t('imagehub.deleteIsoDesc', { name: iso.name }),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        try {
          confirmState.loading = true;
          await api.deleteISO(iso.name, iso.pool || isoTargetPool);
          confirmState.open = false;
          toast.success(t('imagehub.isoDeleted'));
          await loadData();
        } catch (err) {
          confirmState.loading = false;
          toast.error(err.message || t('imagehub.isoDeleteError'));
        }
      },
    });
  }

  // Quick Launch
  function openQuickLaunch(item, type) {
    quickLaunchItem = item;
    launchType = type;
    launchName = (item.id || item.os || 'instance') + '-' + Math.floor(100 + Math.random() * 900);
  }

  async function executeQuickLaunch() {
    if (!launchName.trim()) return;
    launching = true;
    try {
      if (launchType === 'container') {
        const payload = {
          name: launchName.trim(),
          type: 'container',
          image: quickLaunchItem.ref,
          network: launchNetwork || 'default',
          vcpus: 2,
          ram_mb: 2048,
          disk_gb: 10,
        };
        const res = await api.createVM(payload);
        toast.success(`Contenedor ${res.name} creado con éxito!`);
        quickLaunchItem = null;
        navigate(`/vms/${res.id}`);
      } else {
        // VM create redirect with preset base
        navigate(`/vms/new`);
      }
    } catch (err) {
      toast.error(err.message || t('imagehub.launchError'));
    } finally {
      launching = false;
    }
  }
</script>

<div class="flex-1 flex flex-col min-h-0 bg-background overflow-y-auto">
  <!-- Top Banner Header -->
  <div class="p-4 sm:p-6 pb-2 border-b border-border bg-card/60 backdrop-blur-md shrink-0">
    <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
      <div>
        <div class="flex items-center gap-2.5 mb-1">
          <div class="w-8 h-8 rounded-xl bg-accent/15 flex items-center justify-center text-accent">
            <Icon name="disc" size={20} />
          </div>
          <h1 class="text-xl font-bold tracking-tight text-foreground">
            {t('imagehub.title')}
          </h1>
          <span
            class="text-xs px-2 py-0.5 rounded-full bg-accent/15 text-accent font-semibold border border-accent/30 font-mono"
          >
            {t('imagehub.unifiedPool')}
          </span>
        </div>
        <p class="text-xs text-muted-foreground max-w-2xl">
          {t('imagehub.subtitle')}
        </p>
      </div>

      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" onclick={loadData} title={t('imagehub.reloadHub')}>
          <Icon name="refresh" size={14} />
        </Button>
      </div>
    </div>

    <!-- Navigation Tabs -->
    <div class="mt-5 flex items-center gap-2 overflow-x-auto border-b border-border/50 text-xs">
      <button
        onclick={() => (activeTab = 'containers')}
        class="flex items-center gap-2 px-3.5 py-2.5 font-medium border-b-2 -mb-px transition-all shrink-0 whitespace-nowrap {activeTab ===
        'containers'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="box" size={15} class="text-success" />
        <span>{t('imagehub.tabLXC')}</span>
        <span
          class="text-[10px] px-1.5 py-0.2 rounded-full bg-success/10 text-success font-mono font-bold"
        >
          {t('imagehub.inCache', { count: localContainerImages.length })}
        </span>
      </button>

      <button
        onclick={() => (activeTab = 'cloud-base')}
        class="flex items-center gap-2 px-3.5 py-2.5 font-medium border-b-2 -mb-px transition-all shrink-0 whitespace-nowrap {activeTab ===
        'cloud-base'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="hardDrive" size={14} />
        <span>{t('imagehub.tabCloudBase')}</span>
        <span
          class="text-[10px] px-1.5 py-0.2 rounded-full bg-accent/10 text-accent font-mono font-bold"
        >
          {t('imagehub.localCount', { count: baseCloudImages.filter((b) => b.is_cached).length })}
        </span>
      </button>

      <button
        onclick={() => (activeTab = 'isos')}
        class="flex items-center gap-2 px-3.5 py-2.5 font-medium border-b-2 -mb-px transition-all shrink-0 whitespace-nowrap {activeTab ===
        'isos'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="disc" size={14} />
        <span>{t('imagehub.tabISOs')}</span>
        <span
          class="text-[10px] px-1.5 py-0.2 rounded-full bg-muted text-muted-foreground font-mono"
        >
          {isos.length}
        </span>
      </button>

      <button
        onclick={() => (activeTab = 'cloud-init')}
        class="flex items-center gap-2 px-3.5 py-2.5 font-medium border-b-2 -mb-px transition-all shrink-0 whitespace-nowrap {activeTab ===
        'cloud-init'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="code" size={14} />
        <span>{t('imagehub.tabCloudInit')}</span>
      </button>
    </div>
  </div>

  <!-- Tab Content Area -->
  <div class="p-4 sm:p-6 flex-1 min-h-0">
    {#if loading}
      <CardGridSkeleton count={8} cols="grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4" />
    {:else if activeTab === 'containers'}
      <!-- TAB 1: INCUS CONTAINER IMAGES -->
      <div class="space-y-6">
        <!-- Search & Info Bar -->
        <div
          class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 bg-muted/20 border border-border p-3.5 rounded-2xl"
        >
          <div class="flex items-center gap-3">
            <div
              class="w-9 h-9 rounded-xl bg-success/15 flex items-center justify-center text-success"
            >
              <Icon name="box" size={18} />
            </div>
            <div>
              <h3 class="text-sm font-bold text-foreground">Pool de Contenedores Incus / LXC</h3>
              <p class="text-xs text-muted-foreground">
                Las imágenes en caché permiten crear contenedores en 0.2 segundos de forma
                instantánea.
              </p>
            </div>
          </div>

          <div class="relative w-full sm:w-64">
            <Icon
              name="search"
              size={14}
              class="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
            />
            <input
              type="text"
              placeholder="Buscar (Ubuntu, Debian, Alpine...)"
              bind:value={containerSearch}
              class="w-full pl-8 pr-3 py-1.5 rounded-xl bg-background border border-border text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
            />
          </div>
        </div>

        <!-- Where the cache lives. Shown only when Incus is present
             (the endpoint answers 501 otherwise) and only to admins,
             since this rewrites a daemon-wide setting. -->
        {#if incusVolume && auth.isAdmin()}
          <div
            class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 border border-border p-3.5 rounded-2xl"
          >
            <div class="flex items-center gap-3 min-w-0">
              <div
                class="w-9 h-9 rounded-xl bg-accent/15 flex items-center justify-center text-accent shrink-0"
              >
                <Icon name="hardDrive" size={18} />
              </div>
              <div class="min-w-0">
                <h3 class="text-sm font-bold text-foreground">{t('imagehub.incusVolumeTitle')}</h3>
                <p class="text-xs text-muted-foreground truncate">
                  {#if incusVolume.default}
                    {t('imagehub.incusVolumeDefault')}
                    <code class="font-mono text-foreground">{incusVolume.default_path}</code>
                  {:else}
                    {t('imagehub.incusVolumeCustom')}
                    <code class="font-mono text-foreground">{incusVolume.volume}</code>
                  {/if}
                </p>
              </div>
            </div>
            <Button size="sm" variant="outline" onclick={openIncusVolume}>
              {t('imagehub.incusVolumeChange')}
            </Button>
          </div>
        {/if}

        <!-- Container Images Cards Grid -->
        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
          <!-- Keyed on the fingerprint when there is one. A local image's
               fingerprint is its identity; its ref is derived from
               (os, release) and two builds of the same release collide
               on it. Svelte 5 throws each_key_duplicate on a repeated
               key and ABORTS the whole render — one duplicate leaves the
               entire Images page blank (zero cached images, zero ISOs,
               zero base disks) despite every API call returning data.
               The backend now guarantees unique refs, but a keyed each
               must never be able to blank a page again. -->
          {#each filteredContainerImages as img (img.fingerprint || img.ref)}
            <div
              class="flex flex-col justify-between rounded-2xl border bg-card p-4 transition-all duration-200 {img.is_local
                ? 'border-success/30 bg-success/[0.02] shadow-sm'
                : 'border-border'}"
            >
              <div>
                <div class="flex items-start justify-between gap-2 mb-2">
                  <div class="flex items-center gap-2 min-w-0">
                    <div
                      class="w-9 h-9 rounded-xl flex items-center justify-center font-bold text-xs {img.is_local
                        ? 'bg-success/15 text-success'
                        : 'bg-muted text-muted-foreground'}"
                    >
                      {img.os ? img.os.slice(0, 3).toUpperCase() : 'LXC'}
                    </div>
                    <div class="min-w-0">
                      <h4 class="font-bold text-sm text-foreground truncate" title={img.name}>
                        {img.name || img.ref}
                      </h4>
                      <p class="text-[11px] text-muted-foreground font-mono truncate">{img.ref}</p>
                    </div>
                  </div>

                  {#if img.is_local}
                    <span
                      class="px-2 py-0.5 rounded-full bg-success/15 text-success font-mono text-[10px] font-medium border border-success/30 shrink-0 flex items-center gap-1"
                    >
                      <Icon name="zap" size={10} />
                      <span>Local</span>
                    </span>
                  {:else}
                    <span
                      class="px-2 py-0.5 rounded-full bg-muted text-muted-foreground font-mono text-[10px] font-bold shrink-0"
                    >
                      Remoto
                    </span>
                  {/if}
                </div>

                <div
                  class="my-3 flex items-center gap-2 text-[11px] font-mono text-muted-foreground"
                >
                  <span>{img.arch || 'x86_64'}</span>
                  <span>•</span>
                  <span>{img.size > 0 ? formatBytes(img.size) : '~130 MB'}</span>
                  {#if img.fingerprint}
                    <span>•</span>
                    <span class="text-[10px] truncate max-w-[70px]">fp:{img.fingerprint}</span>
                  {/if}
                </div>
              </div>

              <!-- Actions -->
              <div class="pt-3 border-t border-border flex items-center justify-between gap-2">
                {#if img.is_local}
                  {#if auth.role === 'admin'}
                    <button
                      onclick={() => deleteContainer(img.fingerprint)}
                      class="p-1.5 rounded-lg text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors"
                      aria-label="Eliminar de la caché local"
                      title="Eliminar de la caché local"
                    >
                      <Icon name="trash" size={13} />
                    </button>
                  {/if}
                  <Button
                    size="xs"
                    variant="primary"
                    onclick={() => openQuickLaunch(img, 'container')}
                    class="ml-auto font-semibold flex items-center gap-1 shadow-sm"
                  >
                    <span>Lanzar Contenedor</span>
                    <Icon name="arrowRight" size={12} />
                  </Button>
                {:else}
                  <Button
                    size="xs"
                    variant="outline"
                    onclick={() => pullContainer(img.ref)}
                    disabled={pullingContainerRef === img.ref}
                    class="w-full font-semibold flex items-center justify-center gap-1.5"
                  >
                    {#if pullingContainerRef === img.ref}
                      <Spinner size="xs" />
                      <span>Descargando...</span>
                    {:else}
                      <Icon name="download" size={12} />
                      <span>Pre-cargar al Pool Local</span>
                    {/if}
                  </Button>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      </div>
    {:else if activeTab === 'cloud-base'}
      <!-- TAB 2: VM QCOW2 CLOUD BASE POOL (CoW Instant Clones) -->
      <div class="space-y-6">
        <div
          class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 bg-muted/20 border border-border p-3.5 rounded-2xl"
        >
          <div class="flex items-center gap-3">
            <div
              class="w-9 h-9 rounded-xl bg-accent/15 flex items-center justify-center text-accent"
            >
              <Icon name="hardDrive" size={18} />
            </div>
            <div>
              <h3 class="text-sm font-bold text-foreground">
                Pool de Discos Base QCOW2 (Copy-on-Write)
              </h3>
              <p class="text-xs text-muted-foreground">
                Las imágenes descargadas en este pool permiten crear VMs instantáneas en 1 segundo
                usando enlaces CoW.
              </p>
            </div>
          </div>

          <div class="flex flex-col sm:flex-row gap-2 w-full sm:w-auto">
            <!-- Where a download lands. Hidden when there is only one
                 disk pool: a select with a single option is a decision
                 the operator does not actually get to make. -->
            {#if diskPools.length > 1}
              <div class="flex items-center gap-2">
                <label
                  for="base-pool-select"
                  class="text-xs font-semibold text-muted-foreground shrink-0"
                >
                  {t('imagehub.basePoolLabel')}
                </label>
                <select
                  id="base-pool-select"
                  bind:value={baseTargetPool}
                  class="input text-xs py-1.5"
                >
                  {#each diskPools as p (p.name)}
                    <option value={p.name}>{p.name}</option>
                  {/each}
                </select>
              </div>
            {/if}

            <div class="relative w-full sm:w-64">
              <Icon
                name="search"
                size={14}
                class="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
              />
              <input
                type="text"
                placeholder="Buscar disco base..."
                bind:value={baseSearch}
                class="w-full pl-8 pr-3 py-1.5 rounded-xl bg-background border border-border text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
              />
            </div>
          </div>
        </div>

        {#if diskPools.length === 0}
          <p class="text-xs text-destructive">{t('imagehub.noDiskPool')}</p>
        {/if}

        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {#each filteredBaseCloudImages as b (b.id)}
            <div
              class="flex flex-col justify-between rounded-2xl border bg-card p-4 transition-all duration-200 {b.is_cached
                ? 'border-accent/40 bg-accent/[0.02] shadow-sm'
                : 'border-border'}"
            >
              <div>
                <div class="flex items-start justify-between gap-2 mb-2">
                  <div class="min-w-0">
                    <h4 class="font-bold text-sm text-foreground truncate" title={b.name}>
                      {b.name}
                    </h4>
                    <p class="text-[11px] text-muted-foreground font-mono truncate">{b.id}.qcow2</p>
                  </div>

                  {#if b.is_cached}
                    <span
                      class="px-2 py-0.5 rounded-full bg-success/15 text-success font-mono text-[10px] font-bold border border-success/30 shrink-0 flex items-center gap-1"
                    >
                      <span>✓</span>
                      <span>En Pool Local</span>
                    </span>
                  {:else}
                    <span
                      class="px-2 py-0.5 rounded-full bg-muted text-muted-foreground font-mono text-[10px] font-bold shrink-0"
                    >
                      No descargado
                    </span>
                  {/if}
                </div>

                <p class="text-xs text-muted-foreground line-clamp-2 my-2 leading-relaxed">
                  {b.description || 'Disco base cloud oficial listo para Copy-on-Write.'}
                </p>

                <div
                  class="flex items-center gap-2 text-[11px] font-mono text-muted-foreground pt-2 border-t border-border/40"
                >
                  <span>{b.is_cached ? formatBytes(b.local_bytes) : formatBytes(b.size_bytes)}</span
                  >
                  <span>•</span>
                  <span>Formato: {b.format || 'qcow2'}</span>
                  {#if b.is_cached && b.pool}
                    <span>•</span>
                    <span class="truncate" title={b.local_path}>{b.pool}</span>
                  {/if}
                </div>
              </div>

              <!-- Actions -->
              <div class="pt-3 border-t border-border flex items-center justify-between gap-2 mt-3">
                {#if b.is_cached}
                  {#if auth.role === 'admin'}
                    <button
                      onclick={() => deleteBaseCloud(b.id)}
                      class="p-1.5 rounded-lg text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors"
                      aria-label="Eliminar disco base del pool"
                      title="Eliminar disco base del pool"
                    >
                      <Icon name="trash" size={13} />
                    </button>
                  {/if}
                  <!-- Only offered when the image sits in a real pool:
                       a legacy DataDir/base-images copy has no source
                       pool for the move endpoint to read from. -->
                  {#if b.pool && moveBaseTargets.length > 0}
                    <button
                      onclick={() => openMoveBase(b)}
                      class="p-1.5 rounded-lg text-muted-foreground hover:text-accent hover:bg-accent/10 transition-colors"
                      aria-label={t('imagehub.moveBaseAria')}
                      title={t('imagehub.moveBaseAria')}
                    >
                      <Icon name="arrowRightLeft" size={13} />
                    </button>
                  {/if}
                  <Button
                    size="xs"
                    variant="primary"
                    onclick={() => openQuickLaunch(b, 'vm')}
                    class="ml-auto font-semibold flex items-center gap-1 shadow-sm"
                  >
                    <span>Crear VM (CoW 1s)</span>
                    <Icon name="arrowRight" size={12} />
                  </Button>
                {:else}
                  <Button
                    size="xs"
                    variant="outline"
                    onclick={() => pullBaseCloud(b.id)}
                    disabled={pullingBaseId === b.id || diskPools.length === 0}
                    class="w-full font-semibold flex items-center justify-center gap-1.5"
                  >
                    {#if pullingBaseId === b.id}
                      <Spinner size="xs" />
                      <span>Descargando...</span>
                    {:else}
                      <Icon name="download" size={12} />
                      <span>Descargar al Pool Local</span>
                    {/if}
                  </Button>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      </div>
    {:else if activeTab === 'isos'}
      <!-- TAB 3: ISO IMAGES LIBRARY -->
      <div class="space-y-6">
        <div
          class="flex flex-col sm:flex-row sm:items-center justify-between gap-4 bg-muted/20 border border-border p-3.5 rounded-2xl"
        >
          <div class="flex items-center gap-3">
            <div
              class="w-9 h-9 rounded-xl bg-accent/15 flex items-center justify-center text-accent"
            >
              <Icon name="disc" size={18} />
            </div>
            <div>
              <h3 class="text-sm font-bold text-foreground">Biblioteca Centralizada de ISOs</h3>
              <p class="text-xs text-muted-foreground">
                Instaladores de sistemas operativos (Windows, Linux, FreeBSD, TrueNAS).
              </p>
            </div>
          </div>

          <div class="flex items-center gap-2">
            <label class="cursor-pointer">
              <input type="file" accept=".iso" onchange={handleIsoUpload} class="hidden" />
              <span
                class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-card border border-border hover:bg-muted text-xs font-semibold text-foreground shadow-sm transition-all"
              >
                <Icon name="upload" size={13} />
                <span>Subir ISO</span>
              </span>
            </label>

            <Button
              size="sm"
              onclick={() => (showDownloadIsoModal = true)}
              disabled={isoPools.length === 0}
              class="font-semibold shadow-sm gap-1.5"
            >
              <Icon name="download" size={13} />
              <span>Descargar por URL</span>
            </Button>
          </div>
        </div>

        {#if uploadingIso}
          <div class="p-4 rounded-2xl bg-accent/10 border border-accent/30 space-y-2">
            <div class="flex items-center justify-between text-xs font-bold text-accent">
              <span>Subiendo archivo ISO...</span>
              <span>{uploadIsoProgress}%</span>
            </div>
            <div class="w-full h-2 rounded-full bg-muted overflow-hidden">
              <div
                class="h-full bg-accent transition-all duration-200"
                style="width: {uploadIsoProgress}%"
              ></div>
            </div>
          </div>
        {/if}

        <!-- ISOs List -->
        {#if filteredISOs.length === 0}
          <div class="border border-dashed border-border rounded-2xl bg-card/40">
            <EmptyState
              icon="disc"
              title="No hay imágenes ISO en el pool"
              description="Sube un archivo .iso o introduce una URL de descarga."
            />
          </div>
        {:else}
          <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
            {#each filteredISOs as iso (iso.name + (iso.pool || ''))}
              <div
                class="flex flex-col justify-between rounded-2xl border border-border bg-card p-4 hover:border-accent/40 transition-all"
              >
                <div>
                  <div class="flex items-start justify-between gap-2 mb-2">
                    <div class="flex items-center gap-2 min-w-0">
                      <div
                        class="w-9 h-9 rounded-xl bg-accent/10 flex items-center justify-center text-accent shrink-0 font-bold text-xs"
                      >
                        ISO
                      </div>
                      <div class="min-w-0">
                        <h4 class="font-bold text-sm text-foreground truncate" title={iso.name}>
                          {iso.name}
                        </h4>
                        <p class="text-[11px] text-muted-foreground font-mono">
                          {iso.pool || '—'}
                        </p>
                      </div>
                    </div>
                  </div>

                  <div
                    class="my-2 flex items-center gap-2 text-[11px] font-mono text-muted-foreground"
                  >
                    <span>{formatBytes(iso.size_bytes || iso.size)}</span>
                    {#if iso.sha256}
                      <span>•</span>
                      <span class="text-[10px] truncate max-w-[100px]" title={iso.sha256}
                        >sha256:{iso.sha256.slice(0, 10)}...</span
                      >
                    {/if}
                  </div>
                </div>

                <div class="pt-3 border-t border-border flex items-center justify-between gap-2">
                  {#if auth.role === 'admin'}
                    <button
                      onclick={() => deleteIsoFile(iso)}
                      class="p-1.5 rounded-lg text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors"
                      aria-label="Eliminar archivo ISO"
                      title="Eliminar archivo ISO"
                    >
                      <Icon name="trash" size={13} />
                    </button>
                  {/if}
                  <Button
                    size="xs"
                    variant="outline"
                    onclick={() => navigate('/vms/new')}
                    class="ml-auto font-semibold flex items-center gap-1"
                  >
                    <span>Instalar en VM</span>
                    <Icon name="arrowRight" size={12} />
                  </Button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {:else if activeTab === 'cloud-init'}
      <!-- TAB 4: CLOUD-INIT STUDIO (EMBEDDED) -->
      <SnippetsTab />
    {/if}
  </div>
</div>

<!-- Download ISO by URL Modal -->
<Dialog.Root bind:open={showDownloadIsoModal}>
  <Dialog.Content class="sm:max-w-md [&>*]:min-w-0">
    <Dialog.Header>
      <Dialog.Title>Descargar ISO por URL</Dialog.Title>
    </Dialog.Header>

    <div class="space-y-3">
      <div>
        <label for="iso-url-input" class="text-xs font-semibold text-foreground block mb-1"
          >URL Oficial de Descarga *</label
        >
        <Input
          id="iso-url-input"
          bind:value={isoDownloadUrl}
          placeholder="https://releases.ubuntu.com/.../ubuntu.iso"
          class="w-full text-xs"
        />
      </div>
      <div>
        <label for="iso-name-input" class="text-xs font-semibold text-foreground block mb-1"
          >Nombre de Archivo *</label
        >
        <Input
          id="iso-name-input"
          bind:value={isoDownloadName}
          placeholder="ubuntu-24.04-server.iso"
          class="w-full text-xs"
        />
      </div>
      <div>
        <label for="iso-pool-select" class="text-xs font-semibold text-foreground block mb-1"
          >{t('imagehub.isoPoolLabel')}</label
        >
        <select id="iso-pool-select" bind:value={isoTargetPool} class="input w-full text-xs">
          {#each isoPools as p (p.name)}
            <option value={p.name}>{p.name}</option>
          {/each}
        </select>
        {#if isoPools.length === 0}
          <p class="text-[11px] text-destructive mt-1">{t('imagehub.noIsoPool')}</p>
        {/if}
      </div>
    </div>

    <Dialog.Footer>
      <Button
        variant="outline"
        size="sm"
        onclick={() => (showDownloadIsoModal = false)}
        disabled={downloadingIso}
      >
        Cancelar
      </Button>
      <Button
        variant="primary"
        size="sm"
        onclick={submitDownloadIso}
        disabled={downloadingIso || !isoTargetPool}
        class="font-semibold"
      >
        {#if downloadingIso}
          <Spinner size="sm" class="mr-1" />
          <span>Iniciando...</span>
        {:else}
          <Icon name="download" size={14} class="mr-1" />
          <span>Descargar al Pool</span>
        {/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Quick Launch Modal -->
<Dialog.Root
  open={!!quickLaunchItem}
  onOpenChange={(v) => {
    if (!v) quickLaunchItem = null;
  }}
>
  <Dialog.Content class="sm:max-w-md [&>*]:min-w-0">
    <Dialog.Header>
      <Dialog.Title class="flex items-center gap-2">
        <Icon name="zap" size={18} class="text-success" />
        Lanzamiento Instantáneo
      </Dialog.Title>
    </Dialog.Header>

    <div class="space-y-3">
      <div>
        <label for="launch-name-input" class="text-xs font-semibold text-foreground block mb-1"
          >Nombre de la Instancia *</label
        >
        <Input id="launch-name-input" bind:value={launchName} class="w-full text-xs" />
      </div>
      <div>
        <label for="launch-net-select" class="text-xs font-semibold text-foreground block mb-1"
          >Red / Bridge *</label
        >
        <select id="launch-net-select" bind:value={launchNetwork} class="input w-full text-xs">
          {#each networks as n (n.name)}
            <option value={n.name}>{n.name}</option>
          {/each}
        </select>
      </div>
    </div>

    <Dialog.Footer>
      <Button
        variant="outline"
        size="sm"
        onclick={() => (quickLaunchItem = null)}
        disabled={launching}
      >
        Cancelar
      </Button>
      <Button
        variant="primary"
        size="sm"
        onclick={executeQuickLaunch}
        disabled={launching}
        class="font-semibold"
      >
        {#if launching}
          <Spinner size="sm" class="mr-1" />
          <span>Lanzando...</span>
        {:else}
          <Icon name="zap" size={14} class="mr-1 text-warning" />
          <span>Lanzar en 1s</span>
        {/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Move a cached base image to another pool. Only disk and template
     pools are offered: those are the two the server accepts for a
     base image (baseImagePoolPurpose in api/image_hub.go). -->
<Dialog.Root bind:open={showMoveBase}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('imagehub.moveBaseTitle')}</Dialog.Title>
      <Dialog.Description>
        {t('imagehub.moveBaseDesc', {
          name: moveBaseImage?.name || moveBaseImage?.id || '',
          pool: moveBaseImage?.pool || '',
        })}
      </Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3 my-2">
      <div>
        <label for="move-base-pool" class="block text-xs font-semibold mb-1"
          >{t('imagehub.moveBaseTargetLabel')}</label
        >
        <select id="move-base-pool" bind:value={moveBaseDest} class="input w-full !text-xs">
          <option value="">{t('imagehub.moveBaseTargetPlaceholder')}</option>
          {#each moveBaseTargets as p (p.name)}
            <option value={p.name}>{p.name} ({formatBytes(p.available)} libres)</option>
          {/each}
        </select>
      </div>
      <p class="text-[11px] text-muted-foreground">{t('imagehub.moveBaseHint')}</p>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showMoveBase = false)}>{t('common.cancel')}</Button>
      <Button disabled={moveBaseSubmitting || !moveBaseDest} onclick={submitMoveBase}>
        {#if moveBaseSubmitting}<Spinner size="xs" class="mr-1" />{/if}
        {t('imagehub.moveBaseConfirm')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<Dialog.Root bind:open={showIncusVolume}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('imagehub.incusVolumeTitle')}</Dialog.Title>
      <Dialog.Description>{t('imagehub.incusVolumeDesc')}</Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3 my-2">
      <div>
        <label for="incus-volume-pool" class="block text-xs font-semibold mb-1"
          >{t('imagehub.incusVolumeTargetLabel')}</label
        >
        <select id="incus-volume-pool" bind:value={incusVolumeDest} class="input w-full !text-xs">
          <!-- The empty option is the daemon default, not "unset": it
               sends the cache back to /var/lib/incus/images. -->
          <option value="">{t('imagehub.incusVolumeDefaultOption')}</option>
          {#each containerPools as p (p.name)}
            <option value={p.name}>{p.name} ({formatBytes(p.available)} libres)</option>
          {/each}
        </select>
      </div>
      <!-- Said plainly: this is not a setting that takes effect later,
           it copies the whole cache now and the request will sit there
           while it does. -->
      <p class="text-[11px] text-warning">{t('imagehub.incusVolumeWarning')}</p>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showIncusVolume = false)}
        >{t('common.cancel')}</Button
      >
      <Button
        disabled={incusVolumeSaving || incusVolumeDest === (incusVolume?.pool || '')}
        onclick={saveIncusVolume}
      >
        {#if incusVolumeSaving}<Spinner size="xs" class="mr-1" />{/if}
        {t('imagehub.incusVolumeConfirm')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<ConfirmDialog
  bind:open={confirmState.open}
  title={confirmState.title}
  description={confirmState.description}
  confirmLabel={confirmState.confirmLabel}
  variant={confirmState.variant}
  loading={confirmState.loading}
  onConfirm={confirmState.onConfirm}
/>
