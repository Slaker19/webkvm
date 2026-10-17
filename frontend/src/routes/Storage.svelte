<script>
  import { SvelteSet } from 'svelte/reactivity';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import { formatBytes } from '$lib/utils/format.js';
  import { onImageJobDone } from '$lib/stores/imageJobs.svelte.js';
  import { onMount, onDestroy } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { Button } from '$lib/components/ui/button';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import ErrorModal from '$lib/components/ErrorModal.svelte';
  import SmartModal from '$lib/components/SmartModal.svelte';
  import { t } from '../lib/i18n.svelte.js';
  import { hasPurpose, isIsoOnly } from '$lib/purpose.js';

  import StoragePoolsTab from '$lib/components/storage/StoragePoolsTab.svelte';
  import StorageHostDisksTab from '$lib/components/storage/StorageHostDisksTab.svelte';
  import StorageZfsTab from '$lib/components/storage/StorageZfsTab.svelte';
  import StorageVmDisksTab from '$lib/components/storage/StorageVmDisksTab.svelte';
  import StorageLxcDisksTab from '$lib/components/storage/StorageLxcDisksTab.svelte';
  import StorageIsosTab from '$lib/components/storage/StorageIsosTab.svelte';
  import StorageLxcTemplatesTab from '$lib/components/storage/StorageLxcTemplatesTab.svelte';
  import StorageCloudInitTab from '$lib/components/storage/StorageCloudInitTab.svelte';

  // Active Tab: 'pools' | 'host-disks' | 'zfs' | 'vm-disks' | 'lxc-disks' | 'isos' | 'lxc-templates' | 'cloud-init'
  let activeTab = $state('pools');

  let pools = $state([]);
  let breakdownByPool = $state({});
  let loading = $state(true);
  let error = $state('');

  let hostPoolsDir = $state('/opt/webkvm/pools');
  let showCreatePool = $state(false);

  // Per-tab state
  let selectedDiskPool = $state('');
  let selectedLxcPool = $state('');
  let selectedISOPool = $state('__all__');

  let hostDisks = $state([]);
  let hostDisksLoading = $state(false);
  let orphanMounts = $state([]);
  let raidArrays = $state([]);
  let raidLoading = $state(false);

  let zpools = $state([]);
  let zvols = $state([]);
  let zfsLoading = $state(false);

  let lxcImages = $state([]);
  let cloudInitSnippets = $state([]);

  // Smart Modal state
  let selectedSmartDisk = $state(null);
  let showSmartModal = $state(false);

  // Storage Error Modal
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

  function askConfirm(opts) {
    confirmState = {
      ...opts,
      open: true,
      loading: false,
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await opts.onConfirm();
          confirmState.open = false;
        } finally {
          confirmState.loading = false;
        }
      },
    };
  }

  function sumDedupedByDevice(list, field) {
    const seenDevices = new SvelteSet();
    let sum = 0;
    for (const p of list) {
      const key = p.device_id || `pool:${p.name}`;
      if (seenDevices.has(key)) continue;
      seenDevices.add(key);
      sum += p[field] || 0;
    }
    return sum;
  }
  const totalCapacity = $derived(sumDedupedByDevice(pools, 'capacity'));
  const totalAllocated = $derived(sumDedupedByDevice(pools, 'allocated'));

  async function loadBreakdown() {
    try {
      const list = (await api.getStorageBreakdown()) || [];
      const map = {};
      for (const b of list) map[b.pool] = b;
      breakdownByPool = map;
    } catch {
      breakdownByPool = {};
    }
  }

  async function loadHostDisks() {
    hostDisksLoading = true;
    try {
      hostDisks = (await api.listHostDisks()) || [];
    } catch {
      hostDisks = [];
    } finally {
      hostDisksLoading = false;
    }
    try {
      orphanMounts = (await api.listOrphanMounts()) || [];
    } catch {
      orphanMounts = [];
    }
    if (auth.isAdmin()) {
      loadZfsData();
      loadRaidData();
    }
  }

  async function loadRaidData() {
    raidLoading = true;
    try {
      raidArrays = (await api.listHostRaid().catch(() => [])) || [];
    } catch {
      raidArrays = [];
    } finally {
      raidLoading = false;
    }
  }

  async function loadZfsData() {
    zfsLoading = true;
    try {
      const [poolsRes, volsRes] = await Promise.all([
        api.listHostZpools().catch(() => []),
        api.listHostZVols().catch(() => []),
      ]);
      zpools = poolsRes || [];
      zvols = volsRes || [];
    } catch {
      zpools = [];
      zvols = [];
    } finally {
      zfsLoading = false;
    }
  }

  let unsubscribeDone;
  onDestroy(() => {
    if (unsubscribeDone) unsubscribeDone();
  });

  onMount(() => {
    load();
    unsubscribeDone = onImageJobDone(() => {
      loadQuiet();
    });
  });

  async function loadQuiet() {
    try {
      const [pList, lxcImgs, snips] = await Promise.all([
        api.listPools().catch(() => []),
        api.listContainerImages().catch(() => []),
        api.listCloudInitSnippets().catch(() => []),
      ]);
      pools = pList || [];
      lxcImages = lxcImgs || [];
      cloudInitSnippets = snips || [];
      loadBreakdown();
    } catch {
      // quiet refresh
    }
  }

  async function load() {
    loading = true;
    error = '';
    try {
      const [pList, lxcImgs, snips, hostInfo] = await Promise.all([
        api.listPools().catch(() => []),
        api.listContainerImages().catch(() => []),
        api.listCloudInitSnippets().catch(() => []),
        api.getHostInfo().catch(() => null),
      ]);
      pools = pList || [];
      if (hostInfo?.pools_dir) hostPoolsDir = hostInfo.pools_dir;
      lxcImages = lxcImgs || [];
      cloudInitSnippets = snips || [];
      loadBreakdown();

      const diskPools = pools.filter((p) => hasPurpose(p, 'disk'));
      if (!selectedDiskPool || !diskPools.find((p) => p.name === selectedDiskPool)) {
        selectedDiskPool = diskPools[0]?.name || pools[0]?.name || '';
      }

      const lxcList = pools.filter(
        (p) => hasPurpose(p, 'container') || (p.type && p.type.includes('incus'))
      );
      if (!selectedLxcPool || !lxcList.find((p) => p.name === selectedLxcPool)) {
        selectedLxcPool = lxcList[0]?.name || '';
      }
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  function switchTab(tab) {
    activeTab = tab;
    if (tab === 'host-disks') {
      loadHostDisks();
    } else if (tab === 'zfs') {
      loadZfsData();
      loadHostDisks();
    }
  }

  function openPoolInTab(p) {
    if (isIsoOnly(p)) {
      selectedISOPool = p.name;
      switchTab('isos');
    } else if (hasPurpose(p, 'container') || (p.type && p.type.includes('incus'))) {
      selectedLxcPool = p.name;
      switchTab('lxc-disks');
    } else {
      selectedDiskPool = p.name;
      switchTab('vm-disks');
    }
  }
</script>

<div class="p-3 sm:p-5 w-full max-w-[1700px] mx-auto space-y-4">
  <!-- Top Header & Global Metrics -->
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
    <div>
      <PageHeader title={t('storage.title')} subtitle={t('storage.subtitle')} class="!mb-0 !pb-0" />
    </div>

    <div class="flex items-center gap-2">
      <Button
        variant="ghost"
        size="sm"
        disabled={loading}
        onclick={load}
        class="!h-8 !text-xs"
        aria-label={t('common.refresh')}
      >
        <Icon name="refresh" size={13} class={loading ? 'animate-spin mr-1' : 'mr-1'} />
        {t('common.refresh')}
      </Button>
      {#if auth.isAdmin()}
        <Button size="sm" onclick={() => (showCreatePool = true)} class="!h-8 !text-xs">
          <Icon name="plus" size={13} class="mr-1" />
          {t('storage.createPool')}
        </Button>
      {/if}
    </div>
  </div>

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  <!-- 4 Clean Metric Cards Strip -->
  <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
    <StatCard label={t('storage.storagePools')} value={String(pools.length)} hint="KVM + Incus" />
    <StatCard label={t('storage.usedSpace')} value={formatBytes(totalAllocated)} />
    <StatCard
      label={t('storage.totalCapacity')}
      value={formatBytes(totalCapacity)}
      hint={totalCapacity > 0
        ? t('storage.occupiedPct', { pct: Math.round((totalAllocated / totalCapacity) * 100) })
        : '-'}
    />
    <StatCard
      label={t('storage.tabLxcTemplates')}
      value={String(lxcImages.length)}
      hint={t('storage.lxcCachedCount', {
        n: lxcImages.filter((i) => i.is_local).length,
      })}
    />
  </div>

  <!-- FLAGSHIP HORIZONTAL TAB NAVIGATION BAR -->
  <div class="border border-border rounded-xl bg-card overflow-hidden shadow-sm">
    <div
      class="flex items-center gap-1 sm:gap-2 px-3 sm:px-4 border-b border-border bg-muted/15 overflow-x-auto text-xs sm:text-sm font-medium"
    >
      <button
        onclick={() => switchTab('pools')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'pools'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="hardDrive" size={15} />
        <span>{t('storage.storagePools')}</span>
        <span class="px-1.5 py-0.2 rounded-full text-[10px] bg-muted font-mono">{pools.length}</span>
      </button>

      <button
        onclick={() => switchTab('host-disks')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'host-disks'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="server" size={15} />
        <span>{t('storage.tabHostDisks')}</span>
        {#if hostDisks.length > 0}
          <span class="px-1.5 py-0.2 rounded-full text-[10px] bg-accent/15 text-accent font-mono">
            {hostDisks.length}
          </span>
        {/if}
      </button>

      <button
        onclick={() => switchTab('zfs')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'zfs'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="layers" size={15} />
        <span>{t('storage.tabZfs')}</span>
        {#if zpools.length > 0}
          <span class="px-1.5 py-0.2 rounded-full text-[10px] bg-accent/15 text-accent font-mono">
            {zpools.length}
          </span>
        {/if}
      </button>

      <button
        onclick={() => switchTab('vm-disks')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'vm-disks'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="computer" size={15} />
        <span>{t('storage.tabVmDisks')}</span>
      </button>

      <button
        onclick={() => switchTab('lxc-disks')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'lxc-disks'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="cpu" size={15} />
        <span>{t('storage.tabLxcDisks')}</span>
      </button>

      <button
        onclick={() => switchTab('isos')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'isos'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="disc" size={15} />
        <span>{t('storage.tabIsos')}</span>
        <span class="px-1.5 py-0.2 rounded-full text-[10px] bg-warning/15 text-warning font-mono"
          >{pools.length > 0 ? '' : '0'}</span
        >
      </button>

      <button
        onclick={() => switchTab('lxc-templates')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'lxc-templates'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="boxes" size={15} />
        <span>{t('storage.tabLxcTemplates')}</span>
        <span class="px-1.5 py-0.2 rounded-full text-[10px] bg-success/15 text-success font-mono">
          {lxcImages.filter((i) => i.is_local).length}
        </span>
      </button>

      <button
        onclick={() => switchTab('cloud-init')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'cloud-init'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="fileText" size={15} />
        <span>{t('storage.tabCloudInit')}</span>
        <span class="px-1.5 py-0.2 rounded-full text-[10px] bg-muted font-mono"
          >{cloudInitSnippets.length}</span
        >
      </button>
    </div>

    <!-- TAB 1: STORAGE POOLS -->
    {#if activeTab === 'pools'}
      <StoragePoolsTab
        {pools}
        {breakdownByPool}
        {hostPoolsDir}
        bind:showCreatePool
        onOpenPool={openPoolInTab}
        onRefresh={load}
        {askConfirm}
        {showStorageErr}
      />
    {/if}

    <!-- TAB 2: HOST PHYSICAL DISKS -->
    {#if activeTab === 'host-disks'}
      <StorageHostDisksTab
        {hostDisks}
        {hostDisksLoading}
        {orphanMounts}
        {raidArrays}
        {raidLoading}
        onRefresh={loadHostDisks}
        onOpenSmart={(disk) => {
          selectedSmartDisk = disk;
          showSmartModal = true;
        }}
        {askConfirm}
        {showStorageErr}
      />
    {/if}

    <!-- TAB 3: ZFS STORAGE -->
    {#if activeTab === 'zfs'}
      <StorageZfsTab
        {zpools}
        {zvols}
        {zfsLoading}
        {hostDisks}
        onRefresh={loadZfsData}
        {showStorageErr}
      />
    {/if}

    <!-- TAB 4: VM DISKS -->
    {#if activeTab === 'vm-disks'}
      <StorageVmDisksTab
        {pools}
        bind:selectedDiskPool
        {askConfirm}
        {showStorageErr}
      />
    {/if}

    <!-- TAB 5: LXC DISKS -->
    {#if activeTab === 'lxc-disks'}
      <StorageLxcDisksTab
        {pools}
        bind:selectedLxcPool
        {askConfirm}
        {showStorageErr}
      />
    {/if}

    <!-- TAB 6: ISO LIBRARY -->
    {#if activeTab === 'isos'}
      <StorageIsosTab
        {pools}
        bind:selectedISOPool
        {askConfirm}
        {showStorageErr}
      />
    {/if}

    <!-- TAB 7: LXC TEMPLATES -->
    {#if activeTab === 'lxc-templates'}
      <StorageLxcTemplatesTab
        {lxcImages}
        onRefresh={load}
        {askConfirm}
      />
    {/if}

    <!-- TAB 8: CLOUD-INIT STUDIO -->
    {#if activeTab === 'cloud-init'}
      <StorageCloudInitTab
        {cloudInitSnippets}
        onRefresh={load}
        {askConfirm}
        {showStorageErr}
      />
    {/if}
  </div>
</div>

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

<SmartModal
  disk={selectedSmartDisk}
  open={showSmartModal}
  onClose={() => {
    showSmartModal = false;
    selectedSmartDisk = null;
  }}
/>
