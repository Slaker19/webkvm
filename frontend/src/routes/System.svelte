<script>
  import { onMount, onDestroy } from 'svelte';
  import { t } from '$lib/i18n.svelte.js';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { capabilities, loadCapabilities } from '$lib/stores/capabilities.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import StatCard from '$lib/components/StatCard.svelte';
  import SystemInfoRow from '$lib/components/host/SystemInfoRow.svelte';
  import Alert from '$lib/components/Alert.svelte';

  // Tab components
  import SystemOverviewTab from '$lib/components/system/SystemOverviewTab.svelte';
  import SystemHardwareTab from '$lib/components/system/SystemHardwareTab.svelte';
  import SystemServicesTab from '$lib/components/system/SystemServicesTab.svelte';
  import SystemAdminTab from '$lib/components/system/SystemAdminTab.svelte';

  // Active tab: 'overview' | 'hardware' | 'services' | 'admin'
  let activeTab = $state('overview');

  // Auto-refresh state for real-time metrics
  let refreshInterval = $state(3000);
  let intervalId = null;
  let refreshing = $state(false);

  // Data state
  let metrics = $state(null); // Real-time from /api/host/monitor (auto-refreshed)
  let status = $state(null); // From /api/system/status (admin-only, lazy)
  let hostMetrics = $state(null); // Dashboard with history (lazy)
  let alerts = $state([]); // Active alerts (lazy)
  let incidents = $state([]); // Alertmanager incidents (lazy)
  let backupJobs = $state([]); // Recent backup jobs (lazy, admin-only)
  let processes = $state([]); // Process list (lazy, on-demand)
  let logs = $state(''); // System logs (admin-only, lazy)
  let backupsList = $state({ mounted: false, backups: [] }); // Backup files (admin-only, lazy)

  // Loading states
  let loading = $state(true);
  let dashboardLoading = $state(true);
  let logsLoading = $state(false);
  let logsAuto = $state(false);
  let backupsLoading = $state(false);

  let error = $state('');
  let actionMsg = $state('');
  let actionErr = $state('');

  const isAdmin = $derived(auth.isAdmin());
  let logInterval = null;
  let timers = [];

  function later(fn, ms) {
    const id = setTimeout(() => {
      timers = timers.filter((x) => x !== id);
      fn();
    }, ms);
    timers.push(id);
    return id;
  }

  function clearAllTimers() {
    for (const id of timers) clearTimeout(id);
    timers = [];
  }

  // Load real-time metrics (auto-refreshed)
  async function loadMetrics(isRefresh = false) {
    if (isRefresh) refreshing = true;
    try {
      const resp = await fetch('/api/host/monitor', { credentials: 'include' });
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      metrics = await resp.json();
      error = '';
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
      refreshing = false;
    }
  }

  // Load dashboard data (lazy, for Overview tab)
  async function loadDashboard() {
    const [hm, al, jb, inc] = await Promise.allSettled([
      api.hostMetrics(),
      api.listActiveAlerts(),
      isAdmin ? api.listBackupJobs() : Promise.resolve({ jobs: [] }),
      api.listIncidents(),
    ]);
    if (hm.status === 'fulfilled') hostMetrics = hm.value;
    if (al.status === 'fulfilled') alerts = al.value?.alerts || [];
    if (jb.status === 'fulfilled') backupJobs = (jb.value?.jobs || []).slice(0, 5);
    if (inc.status === 'fulfilled') incidents = inc.value?.incidents || [];
    dashboardLoading = false;
  }

  // Load system status (admin-only, lazy, for Services/Admin tabs)
  async function loadStatus() {
    if (!isAdmin) return;
    loading = true;
    error = '';
    try {
      status = await api.systemStatus();
    } catch (e) {
      error = errText(e);
    } finally {
      loading = false;
    }
  }

  // Load processes (on-demand)
  async function loadProcesses() {
    try {
      const resp = await fetch('/api/host/processes', { credentials: 'include' });
      if (resp.ok) {
        const data = await resp.json();
        processes = data.processes || [];
      }
    } catch {
      // Non-fatal
    }
  }

  // Load logs (admin-only, lazy)
  async function loadLogs() {
    logsLoading = true;
    try {
      logs = await api.systemLogs(200);
    } catch (e) {
      logs = t('status.loadLogsError', { error: errText(e) });
    } finally {
      logsLoading = false;
    }
  }

  // Load backups (admin-only, lazy)
  async function loadBackups() {
    backupsLoading = true;
    try {
      backupsList = await api.systemBackups();
    } catch {
      // Non-fatal
    } finally {
      backupsLoading = false;
    }
  }

  function toggleAutoRefresh() {
    logsAuto = !logsAuto;
    if (logsAuto) {
      loadLogs();
      logInterval = setInterval(loadLogs, 5000);
    } else if (logInterval) {
      clearInterval(logInterval);
      logInterval = null;
    }
  }

  // Actions (admin-only)
  async function doRestart() {
    try {
      await api.systemRestart();
      actionMsg = t('status.serviceRestarting');
      toast.success(t('status.serviceRestartingToast'));
      later(() => {
        actionMsg = '';
        loadStatus();
      }, 8000);
    } catch (e) {
      actionErr = errText(e);
      toast.error(errText(e));
    }
  }

  async function doUpdate() {
    try {
      const r = await api.systemUpdate();
      const logPath = r.log || '/var/log/webkvm/update.log';
      toast.success(t('status.updateStartedToast'));
      return { mode: r.mode, log: logPath };
    } catch (e) {
      actionErr = errText(e);
      toast.error(errText(e));
      throw e;
    }
  }

  async function doBackup() {
    try {
      const r = await api.systemBackup();
      actionMsg = t('status.backupReady', {
        filename: r.filename,
        size: fmtBytes(r.size),
        duration: (r.duration_ms / 1000).toFixed(1),
      });
      toast.success(t('status.backupCompleted'));
      await loadBackups();
      later(() => {
        actionMsg = '';
      }, 8000);
    } catch (e) {
      actionErr = errText(e);
      toast.error(errText(e));
      throw e;
    }
  }

  function errText(e) {
    if (e?.code === 'forbidden' || e?.status === 403) return t('status.forbidden');
    return e?.message || String(e);
  }

  function fmtBytes(n) {
    if (!n && n !== 0) return '—';
    const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    let i = 0;
    let v = n;
    while (v >= 1024 && i < u.length - 1) {
      v /= 1024;
      i++;
    }
    return `${v.toFixed(1)} ${u[i]}`;
  }

  // Tab switching with lazy loading
  function switchTab(tab) {
    activeTab = tab;
    if (tab === 'overview' && !hostMetrics) loadDashboard();
    if (tab === 'services' && !status && isAdmin) loadStatus();
    if (tab === 'admin' && isAdmin) {
      if (!status) loadStatus();
      if (!logs) loadLogs();
      if (!backupsList.mounted && backupsList.backups.length === 0) loadBackups();
    }
  }

  function refreshAll() {
    loadMetrics(true);
    if (activeTab === 'overview') loadDashboard();
    if (isAdmin && (activeTab === 'services' || activeTab === 'admin')) {
      loadStatus();
      if (activeTab === 'admin') {
        loadLogs();
        loadBackups();
      }
    }
    toast.success(t('host.refreshed'));
  }

  function startAutoRefresh() {
    stopAutoRefresh();
    intervalId = setInterval(() => {
      loadMetrics(true);
    }, refreshInterval);
  }

  function stopAutoRefresh() {
    if (intervalId) {
      clearInterval(intervalId);
      intervalId = null;
    }
  }

  onMount(() => {
    loadMetrics();
    startAutoRefresh();
    loadCapabilities();
    if (isAdmin) {
      loadStatus();
    } else {
      loading = false;
    }
    // Load dashboard data for Overview tab (default)
    loadDashboard();
  });

  onDestroy(() => {
    stopAutoRefresh();
    if (logInterval) clearInterval(logInterval);
    clearAllTimers();
  });

  // Derived stat card values
  const cpuPct = $derived(metrics?.cpu?.usage_percent || 0);
  const memPct = $derived(metrics?.memory?.usage_percent || 0);
  const load1 = $derived(metrics?.load?.load1 || 0);
  const load5 = $derived(metrics?.load?.load5 || 0);
  const load15 = $derived(metrics?.load?.load15 || 0);
  const cores = $derived(metrics?.cpu?.cores || 1);
  const threads = $derived(metrics?.cpu?.threads || cores);
  const diskPct = $derived(
    metrics?.disks?.length > 0
      ? metrics.disks.reduce((sum, d) => sum + (d.usage_percent || 0), 0) / metrics.disks.length
      : 0
  );
  const diskUsed = $derived(
    metrics?.disks?.reduce((sum, d) => sum + (d.used_bytes || 0), 0) || 0
  );
  const diskTotal = $derived(
    metrics?.disks?.reduce((sum, d) => sum + (d.total_bytes || 0), 0) || 0
  );

  function cpuStatus(pct) {
    if (pct >= 90) return 'crashed';
    if (pct >= 70) return 'paused';
    return 'running';
  }

  function memStatus(pct) {
    if (pct >= 90) return 'crashed';
    if (pct >= 75) return 'paused';
    return 'running';
  }

  function getLoadStatusColor(loadPerCore) {
    if (loadPerCore >= 1.0) return 'crashed';
    if (loadPerCore >= 0.7) return 'paused';
    return 'running';
  }

  function diskStatus(pct) {
    if (pct >= 90) return 'crashed';
    if (pct >= 75) return 'paused';
    return 'running';
  }
</script>

<div class="p-3 sm:p-5 w-full max-w-[1700px] mx-auto space-y-4">
  <!-- Header -->
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
    <div>
      <PageHeader title={t('system.title')} subtitle={t('system.subtitle')} class="!mb-0 !pb-0" />
    </div>

    <div class="flex items-center gap-2">
      <select
        class="text-xs border border-border rounded px-2 py-1.5 bg-background focus:outline-none focus:ring-2 focus:ring-accent"
        bind:value={refreshInterval}
        onchange={() => startAutoRefresh()}
      >
        <option value={1000}>1s</option>
        <option value={3000}>3s</option>
        <option value={5000}>5s</option>
        <option value={10000}>10s</option>
      </select>
      <Button variant="outline" size="sm" onclick={refreshAll} disabled={refreshing}>
        <Icon name="refresh" size={14} class={refreshing ? 'animate-spin' : ''} />
        {t('common.refresh')}
      </Button>
    </div>
  </div>

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if actionMsg}
    <Alert variant="info">{actionMsg}</Alert>
  {/if}
  {#if actionErr}
    <Alert variant="error">{actionErr}</Alert>
  {/if}

  {#if loading && !metrics}
    <!-- Skeleton loading -->
    <div class="space-y-4">
      <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
        {#each Array(4) as _}
          <div class="rounded-lg border border-border bg-card p-3 h-20 animate-pulse">
            <div class="h-3 bg-muted rounded w-16 mb-2"></div>
            <div class="h-5 bg-muted rounded w-24"></div>
          </div>
        {/each}
      </div>
    </div>
  {:else if metrics}
    <!-- System Info Row -->
    <SystemInfoRow system={metrics.system} />

    <!-- Stat Cards -->
    <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
      <StatCard
        label={t('system.cpuUsage')}
        value={`${cpuPct.toFixed(1)}%`}
        hint={`${cores}C / ${threads}T`}
        status={cpuStatus(cpuPct)}
      />
      <StatCard
        label={t('system.memoryUsage')}
        value={`${memPct.toFixed(1)}%`}
        hint={`${fmtBytes(metrics.memory?.used || 0)} / ${fmtBytes(metrics.memory?.total || 0)}`}
        status={memStatus(memPct)}
      />
      <StatCard
        label={t('system.loadAvg')}
        value={`${load1.toFixed(2)} · ${load5.toFixed(2)} · ${load15.toFixed(2)}`}
        hint={`per-core: ${(load1 / cores).toFixed(2)}`}
        status={getLoadStatusColor(load1 / cores)}
      />
      <StatCard
        label={t('system.diskUsage')}
        value={`${diskPct.toFixed(1)}%`}
        hint={`${fmtBytes(diskUsed)} / ${fmtBytes(diskTotal)}`}
        status={diskStatus(diskPct)}
      />
    </div>

    <!-- Tab Navigation Bar -->
    <div class="border border-border rounded-xl bg-card overflow-hidden shadow-sm">
      <div
        class="flex items-center gap-1 sm:gap-2 px-3 sm:px-4 border-b border-border bg-muted/15 overflow-x-auto text-xs sm:text-sm font-medium"
      >
        <button
          onclick={() => switchTab('overview')}
          class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
          'overview'
            ? 'border-accent text-accent font-bold'
            : 'border-transparent text-muted-foreground hover:text-foreground'}"
        >
          <Icon name="activity" size={15} />
          <span>{t('system.tabOverview')}</span>
        </button>

        <button
          onclick={() => switchTab('hardware')}
          class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
          'hardware'
            ? 'border-accent text-accent font-bold'
            : 'border-transparent text-muted-foreground hover:text-foreground'}"
        >
          <Icon name="cpu" size={15} />
          <span>{t('system.tabHardware')}</span>
        </button>

        <button
          onclick={() => switchTab('services')}
          class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
          'services'
            ? 'border-accent text-accent font-bold'
            : 'border-transparent text-muted-foreground hover:text-foreground'}"
        >
          <Icon name="settings" size={15} />
          <span>{t('system.tabServices')}</span>
        </button>

        {#if isAdmin}
          <button
            onclick={() => switchTab('admin')}
            class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
            'admin'
              ? 'border-accent text-accent font-bold'
              : 'border-transparent text-muted-foreground hover:text-foreground'}"
          >
            <Icon name="shield" size={15} />
            <span>{t('system.tabAdmin')}</span>
          </button>
        {/if}
      </div>

      <!-- Tab Content -->
      <div class="p-4">
        {#if activeTab === 'overview'}
          <SystemOverviewTab
            {metrics}
            {hostMetrics}
            {alerts}
            {incidents}
            {backupJobs}
            {processes}
            loading={dashboardLoading}
            onLoadProcesses={loadProcesses}
          />
        {:else if activeTab === 'hardware'}
          <SystemHardwareTab {metrics} />
        {:else if activeTab === 'services'}
          <SystemServicesTab {status} {capabilities} />
        {:else if activeTab === 'admin' && isAdmin}
          <SystemAdminTab
            {status}
            {logs}
            {backupsList}
            logsLoading={logsLoading}
            backupsLoading={backupsLoading}
            logsAuto={logsAuto}
            onLoadLogs={loadLogs}
            onToggleAutoRefresh={toggleAutoRefresh}
            onLoadBackups={loadBackups}
            onRestart={doRestart}
            onUpdate={doUpdate}
            onBackup={doBackup}
          />
        {/if}
      </div>
    </div>
  {/if}
</div>
