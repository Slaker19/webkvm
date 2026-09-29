<script>
  import Alert from '$lib/components/Alert.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { capabilities, loadCapabilities } from '$lib/stores/capabilities.svelte.js';
  import Chart from '$lib/components/Chart.svelte';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import * as Dialog from '$lib/components/ui/dialog';
  import { Skeleton } from '$lib/components/ui/skeleton';
  import { Card } from '$lib/components/ui/card';
  import { t } from '../lib/i18n.svelte.js';

  let status = $state(null);
  let logs = $state('');
  let loading = $state(true);
  let error = $state('');
  let logsLoading = $state(false);
  let logsAuto = $state(false);
  let actionMsg = $state('');
  let actionErr = $state('');

  // V13-D-04 dashboard: host metrics (native SVG charts), active alerts,
  // and recent backup jobs — all lightweight, no heavy chart libs.
  let hostMetrics = $state(null);
  let activeAlerts = $state([]);
  let backupJobs = $state([]);
  let dashboardLoading = $state(true);

  const hostCpuPoints = $derived(
    (hostMetrics?.points || []).map((p) => ({ t: p.t, v: p.cpu_usage }))
  );
  const hostRamPct = $derived(
    (hostMetrics?.points || [])
      .filter((p) => p.total_ram > 0)
      .map((p) => ({ t: p.t, v: (p.used_ram / p.total_ram) * 100 }))
  );

  async function loadDashboard() {
    // Settled, not all: these three are independent panels and a single
    // rejection used to blank the other two. /backup/jobs is admin-only
    // (it leaks destination paths and remote hosts), so for a viewer it
    // now answers 403 — which previously would have taken the host
    // metrics and the alert list down with it.
    const [hm, al, jb] = await Promise.allSettled([
      api.hostMetrics(),
      api.listActiveAlerts(),
      auth.isAdmin() ? api.listBackupJobs() : Promise.resolve({ jobs: [] }),
    ]);
    if (hm.status === 'fulfilled') hostMetrics = hm.value;
    if (al.status === 'fulfilled') activeAlerts = al.value?.alerts || [];
    if (jb.status === 'fulfilled') backupJobs = (jb.value?.jobs || []).slice(0, 5);
    dashboardLoading = false;
  }

  let showRestartConfirm = $state(false);
  let restartLoading = $state(false);
  let showUpdateConfirm = $state(false);
  let updateLoading = $state(false);
  let showUpdateResult = $state(false);
  let updateResult = $state('');

  let showBackupConfirm = $state(false);
  let backupLoading = $state(false);
  let backupsList = $state({ mounted: false, backups: [] });
  let backupsLoading = $state(false);

  let logInterval = null;
  // One-shot timers (restart/backup feedback) are tracked and
  // cancelled on unmount so they never touch an unmounted component.
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

  // Everything under /api/system/* (status, logs, backups, restart,
  // update) is admin-only. Operators can open this page for the dashboard
  // and device capabilities, so those panels are skipped for them rather
  // than firing requests that can only answer 403.
  const isAdmin = $derived(auth.isAdmin());

  // A residual 403 (e.g. role changed mid-session) carries the raw
  // backend string "insufficient role for this action"; show it localised.
  function errText(e) {
    if (e?.code === 'forbidden' || e?.status === 403) return t('status.forbidden');
    return e?.message || String(e);
  }

  function jobStatusLabel(s) {
    if (s === 'success') return t('status.jobSuccess');
    if (s === 'error') return t('status.jobError');
    if (s === 'running') return t('status.jobRunning');
    return s;
  }

  function refreshAll() {
    loadDashboard();
    if (auth.isAdmin()) loadStatus();
  }

  async function loadStatus() {
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

  async function doRestart() {
    restartLoading = true;
    try {
      await api.systemRestart();
      showRestartConfirm = false;
      actionMsg = t('status.serviceRestarting');
      toast.success(t('status.serviceRestartingToast'));
      later(() => {
        actionMsg = '';
        loadStatus();
      }, 8000);
    } catch (e) {
      actionErr = errText(e);
      toast.error(errText(e));
    } finally {
      restartLoading = false;
    }
  }

  async function doUpdate() {
    updateLoading = true;
    try {
      const r = await api.systemUpdate();
      updateResult = r.log || '/var/log/webkvm/update.log';
      showUpdateConfirm = false;
      showUpdateResult = true;
      toast.success(t('status.updateStartedToast'));
    } catch (e) {
      actionErr = errText(e);
      toast.error(errText(e));
    } finally {
      updateLoading = false;
    }
  }

  async function loadBackups() {
    backupsLoading = true;
    try {
      backupsList = await api.systemBackups();
    } catch (_e) {
      // Non-fatal: list stays at its last value
    } finally {
      backupsLoading = false;
    }
  }

  async function doBackup() {
    backupLoading = true;
    try {
      const r = await api.systemBackup();
      showBackupConfirm = false;
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
    } finally {
      backupLoading = false;
    }
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

  function fmtUptime(sec) {
    if (!sec) return '—';
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    if (d > 0) return `${d}d ${h}h ${m}m`;
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
  }

  function fmtDate(s) {
    if (!s) return '—';
    try {
      return new Date(s).toLocaleString();
    } catch {
      return s;
    }
  }

  $effect(() => {
    if (auth.isAdmin()) {
      loadStatus();
      loadLogs();
      loadBackups();
    } else {
      loading = false;
    }
    loadDashboard();
    loadCapabilities();
    return () => {
      if (logInterval) clearInterval(logInterval);
      clearAllTimers();
    };
  });
</script>

{#snippet deviceCaps()}
  {#if capabilities.loaded}
    <Card class="p-5 mb-4">
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-sm font-semibold">{t('status.deviceCaps')}</h2>
        {#if !capabilities.parsed}
          <span class="text-xs text-warning">{t('status.deviceCapsUnprobed')}</span>
        {/if}
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-2 text-sm">
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.deviceCapsVideo')}</dt>
          <dd class="font-mono text-right text-xs">
            {capabilities.videoModels.join(', ') || '—'}
          </dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.deviceCapsNetwork')}</dt>
          <dd class="font-mono text-right text-xs">
            {capabilities.networkModels.join(', ') || '—'}
          </dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.deviceCapsSound')}</dt>
          <dd class="font-mono text-right text-xs">
            {capabilities.soundModels.join(', ') || '—'}
          </dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.deviceCapsDiskBus')}</dt>
          <dd class="font-mono text-right text-xs">
            {capabilities.diskBuses.join(', ') || '—'}
          </dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">SPICE</dt>
          <dd
            class="text-right text-xs {capabilities.spiceSupported
              ? 'text-success'
              : 'text-muted-foreground'}"
          >
            {capabilities.spiceSupported
              ? t('status.deviceCapsSpiceOn')
              : t('status.deviceCapsSpiceOff')}
          </dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">QXL</dt>
          <dd
            class="text-right text-xs {capabilities.videoModels.includes('qxl')
              ? 'text-success'
              : 'text-muted-foreground'}"
          >
            {capabilities.videoModels.includes('qxl')
              ? t('status.deviceCapsQxlOn')
              : t('status.deviceCapsQxlOff')}
          </dd>
        </div>
      </div>
      {#if !capabilities.spiceSupported || !capabilities.videoModels.includes('qxl')}
        <p class="text-xs text-muted-foreground mt-3 pt-3 border-t border-border">
          {t('status.deviceCapsHint')}
        </p>
      {/if}
    </Card>
  {/if}
{/snippet}

<div class="p-3 sm:p-5 w-full max-w-[1700px] mx-auto">
  <PageHeader title={t('status.title')} subtitle={t('status.subtitle')}>
    {#snippet actions()}
      <Button variant="outline" size="sm" onclick={refreshAll}>
        <Icon name="refresh" size={14} />
        {t('status.refresh')}
      </Button>
    {/snippet}
  </PageHeader>

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if actionMsg}
    <Alert variant="info">{actionMsg}</Alert>
  {/if}
  {#if actionErr}
    <Alert variant="error">{actionErr}</Alert>
  {/if}

  <!-- V13-D-04: consolidated dashboard (fast, native SVG charts) -->
  <div class="grid grid-cols-1 lg:grid-cols-3 gap-4 mb-4">
    <div class="lg:col-span-2 rounded-xl border border-border bg-background p-4">
      <p class="text-sm font-semibold mb-1">{t('status.dashboardTitle')}</p>
      <p class="text-xs text-muted-foreground mb-3">{t('status.dashboardDesc')}</p>
      {#if dashboardLoading}
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4 py-6">
          <div class="h-20 bg-muted/40 rounded animate-pulse"></div>
          <div class="h-20 bg-muted/40 rounded animate-pulse"></div>
        </div>
      {:else}
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div>
            <div class="flex items-baseline justify-between mb-1.5">
              <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
                >{t('status.hostCpu')}</span
              >
              <span class="text-sm tnum"
                >{hostCpuPoints.length
                  ? hostCpuPoints[hostCpuPoints.length - 1].v.toFixed(1)
                  : '0.0'}%</span
              >
            </div>
            <Chart points={hostCpuPoints} yMax={100} height={70} />
          </div>
          <div>
            <div class="flex items-baseline justify-between mb-1.5">
              <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
                >{t('status.hostRam')}</span
              >
              <span class="text-sm tnum"
                >{hostRamPct.length ? hostRamPct[hostRamPct.length - 1].v.toFixed(1) : '0.0'}%</span
              >
            </div>
            <Chart points={hostRamPct} yMax={100} height={70} color="var(--info, var(--accent))" />
          </div>
        </div>
      {/if}
    </div>
    <div class="rounded-xl border border-border bg-background p-4">
      <p class="text-sm font-semibold mb-1">{t('status.alertsTitle')}</p>
      <p class="text-xs text-muted-foreground mb-3">
        {t('status.alertsCount', { n: activeAlerts.length })}
      </p>
      <div class="space-y-1.5 max-h-40 overflow-y-auto">
        {#each activeAlerts as alert (alert.vm_id + alert.rule?.id)}
          <div
            class="flex items-center gap-2 text-xs rounded-lg bg-destructive/10 border border-destructive/30 px-2 py-1.5"
          >
            <span class="w-2 h-2 rounded-full bg-destructive shrink-0"></span>
            <span class="flex-1 min-w-0 truncate">
              {alert.vm_id.slice(0, 8)} · {alert.rule?.metric || ''}
              {alert.rule?.above ? '>' : '<'}
              {alert.rule?.threshold || 0}
            </span>
          </div>
        {:else}
          <p class="text-sm text-muted-foreground">{t('status.noAlerts')}</p>
        {/each}
      </div>
      <!-- Backups are admin-only data; showing "no backups" to a viewer
           would claim the fleet is unprotected when it simply is not
           their information to see. -->
      <div class="border-t border-border mt-3 pt-3" class:hidden={!isAdmin}>
        <p class="text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2">
          {t('status.lastBackups')}
        </p>
        <div class="space-y-1">
          {#each backupJobs as job (job.id)}
            <div class="flex items-center gap-2 text-xs">
              <span
                class="w-1.5 h-1.5 rounded-full shrink-0 {job.status === 'success'
                  ? 'bg-success'
                  : job.status === 'error'
                    ? 'bg-destructive'
                    : 'bg-warning'}"
              ></span>
              <span class="flex-1 min-w-0 truncate">{job.filename || t('backup.title')}</span>
              <span class="text-muted-foreground tnum shrink-0">{jobStatusLabel(job.status)}</span>
            </div>
          {:else}
            <p class="text-sm text-muted-foreground">{t('status.noBackups')}</p>
          {/each}
        </div>
      </div>
    </div>
  </div>

  {#if !isAdmin}
    <!-- Operators/viewers: only the non-admin panels (dashboard above and
         device capabilities); host status, services, logs and backups
         come from admin-only /api/system endpoints. -->
    {@render deviceCaps()}
  {:else if loading && !status}
    <!-- Skeleton loading state -->
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3 mb-4">
      {#each Array(4) as _, i (i)}
        <Card class="p-4">
          <Skeleton class="h-3 w-16 mb-2" />
          <Skeleton class="h-5 w-20" />
          <Skeleton class="h-3 w-24 mt-2" />
        </Card>
      {/each}
    </div>
    <Skeleton class="h-32 w-full mb-4" />
    <Skeleton class="h-24 w-full" />
  {:else if status}
    <!-- Stat cards -->
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3 mb-4">
      <StatCard
        label={t('status.backend')}
        status={status.backend.version ? 'running' : 'crashed'}
        value={status.backend.version
          ? status.backend.version.startsWith('v')
            ? status.backend.version
            : `v${status.backend.version}`
          : '—'}
        hint={t('status.uptimeHint', { uptime: fmtUptime(status.uptime_sec) })}
      />
      <StatCard
        label={t('status.libvirt')}
        status={status.libvirt.connected ? 'running' : 'crashed'}
        value={status.libvirt.uri}
        hint={status.libvirt.connected ? t('status.connected') : t('status.disconnected')}
      />
      <StatCard
        label={t('status.host')}
        status="running"
        value={status.host.hostname}
        hint={status.host.os}
      />
      <StatCard
        label={t('status.update')}
        status={status.update_available ? 'paused' : 'running'}
        value={status.update_available
          ? t('status.updateAvailable', { version: status.latest_version })
          : t('status.upToDate')}
        hint={t('status.currentVersion', {
          version: status.backend.version ? status.backend.version.replace(/^v/, '') : '',
        })}
      />
    </div>

    <!-- Aggregate disk + load/uptime -->
    <div class="grid grid-cols-1 md:grid-cols-2 gap-3 mb-4">
      <StatCard
        label={t('status.diskTitle')}
        status={status.disk.used_pct > 90
          ? 'crashed'
          : status.disk.used_pct > 75
            ? 'paused'
            : 'running'}
        value={`${fmtBytes(status.disk.used_bytes)} / ${fmtBytes(status.disk.total_bytes)}`}
        hint={`${status.disk.used_pct.toFixed(1)}%`}
      />
      <StatCard
        label={t('status.loadTitle')}
        status="running"
        value={`${status.load.load1.toFixed(2)} ${status.load.load5.toFixed(2)} ${status.load.load15.toFixed(2)}`}
        hint={t('status.hostUptime', { uptime: fmtUptime(status.host_uptime_sec) })}
      />
    </div>

    <!-- System services -->
    <Card class="p-5 mb-4">
      <h2 class="text-sm font-semibold mb-3">{t('status.servicesTitle')}</h2>
      <div class="space-y-2">
        {#each status.services as svc (svc)}
          <div
            class="flex items-center justify-between text-sm border-b border-border/50 pb-2 last:border-0 last:pb-0"
          >
            <div class="min-w-0">
              <div class="font-medium truncate">
                {svc.key === 'libvirt'
                  ? t('status.serviceLibvirt')
                  : svc.key === 'incus'
                    ? t('status.serviceIncus')
                    : svc.key.startsWith('dnsmasq:')
                      ? t('status.servicesDnsmasqBridge', { bridge: svc.key.slice(8) })
                      : svc.unit}
              </div>
              <div class="text-xs text-muted-foreground truncate">
                {svc.description || svc.unit}
              </div>
            </div>
            <span
              class="shrink-0 inline-flex items-center gap-1 text-[10px] px-1.5 py-0.5 rounded-full font-medium {!svc.found
                ? 'bg-muted text-muted-foreground'
                : svc.active
                  ? 'bg-success/10 text-success'
                  : 'bg-destructive/10 text-destructive'}"
            >
              <span
                class="w-1.5 h-1.5 rounded-full {!svc.found
                  ? 'bg-muted-foreground'
                  : svc.active
                    ? 'bg-success'
                    : 'bg-destructive'}"
              ></span>
              {!svc.found
                ? t('status.serviceNotFound')
                : svc.active
                  ? t('status.serviceRunning')
                  : t('status.serviceStopped')}
            </span>
          </div>
        {:else}
          <p class="text-sm text-muted-foreground">{t('status.noServices')}</p>
        {/each}
      </div>
    </Card>

    <!-- Hypervisor platform -->
    <Card class="p-5 mb-4">
      <h2 class="text-sm font-semibold mb-3">{t('status.platformTitle')}</h2>
      <dl class="text-sm space-y-1.5">
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.platformKernel')}</dt>
          <dd class="font-mono truncate text-right">{status.platform.kernel || '—'}</dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.platformQemu')}</dt>
          <dd class="font-mono truncate text-right">{status.platform.qemu_version || '—'}</dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.platformLibvirt')}</dt>
          <dd class="font-mono truncate text-right">{status.platform.libvirt_version || '—'}</dd>
        </div>
        {#if status.platform.incus_enabled}
          <div class="flex justify-between gap-2">
            <dt class="text-muted-foreground shrink-0">{t('status.platformIncus')}</dt>
            <dd class="font-mono truncate text-right">{status.platform.incus_version || '—'}</dd>
          </div>
        {/if}
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.platformNested')}</dt>
          <dd class="text-right">
            {status.platform.nested_virt.supported
              ? t('status.platformEnabled')
              : t('status.platformDisabled')}
            {#if status.platform.nested_virt.vendor && status.platform.nested_virt.vendor !== 'unknown'}
              <span class="text-muted-foreground">({status.platform.nested_virt.vendor})</span>
            {/if}
          </dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.platformIommu')}</dt>
          <dd class="text-right">
            {#if status.platform.iommu.enabled}
              {t('status.platformGroups', { n: status.platform.iommu.groups })}
              {#if status.platform.iommu.assignable !== undefined}
                <span class="text-muted-foreground text-xs ml-1">
                  ({status.platform.iommu.assignable}
                  {t('vmDetail.pciAvailable').toLowerCase()})
                </span>
              {/if}
            {:else}
              {t('status.platformDisabled')}
            {/if}
          </dd>
        </div>
      </dl>
    </Card>

    <!-- Device capabilities: which emulated models this host's QEMU
         actually provides. Operators land here when a model they expect
         (notably qxl) is greyed out in the VM form. -->
    {@render deviceCaps()}

    <!-- Storage pools -->
    <Card class="p-5 mb-4">
      <h2 class="text-sm font-semibold mb-3">{t('status.storagePools')}</h2>
      <div class="space-y-3">
        {#each status.pools as p (p)}
          <div>
            <div class="flex items-center justify-between text-sm mb-1">
              <span class="font-medium">{p.name}</span>
              <span class="text-muted-foreground text-xs tnum"
                >{fmtBytes(p.used_bytes)} / {fmtBytes(p.total_bytes)} ({p.used_pct.toFixed(
                  1
                )}%)</span
              >
            </div>
            <div class="h-2 bg-muted rounded-full overflow-hidden">
              <div
                class="h-full transition-all {p.used_pct > 90
                  ? 'bg-destructive'
                  : p.used_pct > 75
                    ? 'bg-warning'
                    : 'bg-success'}"
                style="width: {Math.min(100, p.used_pct)}%"
              ></div>
            </div>
            <div class="text-xs text-muted-foreground mt-1 font-mono">{p.path}</div>
          </div>
        {/each}
      </div>
    </Card>

    <!-- Actions + Host details -->
    <div class="grid grid-cols-1 md:grid-cols-2 gap-3 mb-4">
      <Card class="p-4">
        <h2 class="text-sm font-semibold mb-3">{t('common.actions')}</h2>
        <div class="space-y-1.5">
          <button
            onclick={() => (showRestartConfirm = true)}
            class="w-full flex items-start gap-3 px-3 py-2.5 text-sm rounded-md border border-border bg-background hover:bg-muted hover:border-border-hover transition text-left"
          >
            <Icon name="restart" size={16} class="text-muted-foreground mt-0.5 shrink-0" />
            <div class="min-w-0 flex-1">
              <div class="font-medium">{t('status.restartService')}</div>
              <div class="text-xs text-muted-foreground mt-0.5">
                {t('status.restartServiceDesc')}
              </div>
            </div>
          </button>
          <button
            onclick={() => (showUpdateConfirm = true)}
            disabled={!status.update_available}
            class="w-full flex items-start gap-3 px-3 py-2.5 text-sm rounded-md border border-border bg-background hover:bg-muted hover:border-border-hover transition text-left disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:bg-background disabled:hover:border-border"
          >
            <Icon name="download" size={16} class="text-muted-foreground mt-0.5 shrink-0" />
            <div class="min-w-0 flex-1">
              <div class="font-medium">
                {t('status.updateTo', { version: status.latest_version || status.backend.version })}
              </div>
              <div class="text-xs text-muted-foreground mt-0.5">{t('status.updateDesc')}</div>
            </div>
          </button>
          <button
            onclick={() => (showBackupConfirm = true)}
            data-testid="backup-now-btn"
            class="w-full flex items-start gap-3 px-3 py-2.5 text-sm rounded-md border border-border bg-background hover:bg-muted hover:border-border-hover transition text-left"
          >
            <Icon name="archive" size={16} class="text-muted-foreground mt-0.5 shrink-0" />
            <div class="min-w-0 flex-1">
              <div class="font-medium">{t('status.createBackup')}</div>
              <div class="text-xs text-muted-foreground mt-0.5">
                {#if backupsList.mounted}
                  {t('status.backupNowDesc')}
                {:else}
                  {t('status.backupShareUnmounted')}
                {/if}
              </div>
            </div>
          </button>
        </div>
      </Card>

      <Card class="p-4">
        <h2 class="text-sm font-semibold mb-3">{t('status.hostDetails')}</h2>
        <dl class="text-sm space-y-1.5">
          <div class="flex justify-between gap-2">
            <dt class="text-muted-foreground shrink-0">{t('status.hostname')}</dt>
            <dd class="font-mono truncate text-right">{status.host.hostname}</dd>
          </div>
          <div class="flex justify-between gap-2">
            <dt class="text-muted-foreground shrink-0">{t('status.kernel')}</dt>
            <dd class="font-mono truncate text-right">{status.host.kernel}</dd>
          </div>
          <div class="flex justify-between gap-2">
            <dt class="text-muted-foreground shrink-0">{t('status.os')}</dt>
            <dd class="truncate text-right">{status.host.os}</dd>
          </div>
          <div class="flex justify-between gap-2">
            <dt class="text-muted-foreground shrink-0">{t('status.arch')}</dt>
            <dd class="font-mono text-right">{status.host.arch}</dd>
          </div>
          <div class="flex justify-between gap-2">
            <dt class="text-muted-foreground shrink-0">{t('status.started')}</dt>
            <dd class="truncate text-right">{fmtDate(status.start_time)}</dd>
          </div>
          <div class="flex justify-between gap-2">
            <dt class="text-muted-foreground shrink-0">{t('status.goroutines')}</dt>
            <dd class="font-mono tnum text-right">{status.backend.goroutines}</dd>
          </div>
        </dl>
      </Card>
    </div>

    <!-- Logs -->
    <Card class="p-5 mb-4">
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-sm font-semibold">{t('status.logs')}</h2>
        <div class="flex items-center gap-2">
          <Button variant="outline" size="sm" onclick={loadLogs} disabled={logsLoading}>
            <Icon name="refresh" size={14} />
            {logsLoading ? t('common.loading') : t('status.refresh')}
          </Button>
          <Button variant={logsAuto ? 'default' : 'outline'} size="sm" onclick={toggleAutoRefresh}>
            {logsAuto ? t('status.autoRefreshOn') : t('status.autoRefreshOff')}
          </Button>
        </div>
      </div>
      <pre
        class="bg-muted/30 border border-border rounded-md p-3 text-xs font-mono text-muted-foreground overflow-auto max-h-96 whitespace-pre-wrap break-all">{logs ||
          t('status.noLogs')}</pre>
    </Card>

    <!-- Backups -->
    <Card class="p-5 mb-4">
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-sm font-semibold">{t('status.backups')}</h2>
        <Button variant="outline" size="sm" onclick={loadBackups} disabled={backupsLoading}>
          <Icon name="refresh" size={14} />
          {backupsLoading ? t('common.loading') : t('status.refresh')}
        </Button>
      </div>
      {#if !backupsList.mounted}
        <Alert variant="info">{t('status.smbNotMounted')}</Alert>
      {:else if !backupsList.backups || backupsList.backups.length === 0}
        <p class="text-sm text-muted-foreground">
          {t('status.noBackupsYet', { host: backupsList.host })}
        </p>
      {:else}
        <div class="space-y-1.5 text-sm">
          {#each backupsList.backups.slice(0, 5) as b (b)}
            <div class="flex justify-between gap-2 items-baseline">
              <span class="font-mono text-xs truncate" title={b.filename}>{b.filename}</span>
              <span class="text-muted-foreground text-xs tnum shrink-0">
                {fmtBytes(b.size)} · {fmtDate(b.modified)}
              </span>
            </div>
          {/each}
          {#if backupsList.backups.length > 5}
            <div class="text-xs text-muted-foreground pt-1">
              {t('status.more', { n: backupsList.backups.length - 5 })}
            </div>
          {/if}
        </div>
      {/if}
    </Card>
  {/if}
</div>

<!-- Restart confirm -->
<ConfirmDialog
  bind:open={showRestartConfirm}
  title={t('status.restartServiceConfirmTitle')}
  description={t('status.restartServiceConfirmDesc')}
  confirmLabel={t('status.restart')}
  variant="default"
  loading={restartLoading}
  onConfirm={doRestart}
/>

<!-- Update confirm -->
<ConfirmDialog
  bind:open={showUpdateConfirm}
  title={t('status.updateToTitle', { version: status?.latest_version || '' })}
  description={t('status.updateConfirmDesc')}
  confirmLabel={t('status.update')}
  variant="default"
  loading={updateLoading}
  onConfirm={doUpdate}
/>

<!-- Backup confirm -->
<ConfirmDialog
  bind:open={showBackupConfirm}
  title={t('status.backupNowTitle')}
  description={t('status.backupNowDesc')}
  confirmLabel={t('status.backup')}
  variant="default"
  loading={backupLoading}
  onConfirm={doBackup}
/>

<!-- Update result -->
<Dialog.Root bind:open={showUpdateResult}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('status.updateStartedTitle')}</Dialog.Title>
      <Dialog.Description>{t('status.updateStartedDesc')}</Dialog.Description>
    </Dialog.Header>
    <pre
      class="bg-muted/30 border border-border rounded p-3 text-xs font-mono text-muted-foreground overflow-auto break-all">{updateResult}</pre>
    <Button
      class="w-full"
      onclick={() => {
        showUpdateResult = false;
        loadStatus();
      }}>{t('common.ok')}</Button
    >
  </Dialog.Content>
</Dialog.Root>
