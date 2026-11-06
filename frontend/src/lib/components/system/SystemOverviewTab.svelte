<script>
  import { t } from '$lib/i18n.svelte.js';
  import { auth } from '$lib/stores/auth.svelte.js';
  import Chart from '$lib/components/Chart.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ProcessPanel from '$lib/components/host/ProcessPanel.svelte';
  import DockerPanel from '$lib/components/host/DockerPanel.svelte';
  import UsersPanel from '$lib/components/host/UsersPanel.svelte';

  /**
   * @typedef {Object} Props
   * @property {any} metrics - Real-time host metrics from /api/host/monitor (auto-refreshed)
   * @property {any} hostMetrics - Dashboard metrics with history from api.hostMetrics()
   * @property {Array} alerts - Active alerts from api.listActiveAlerts()
   * @property {Array} incidents - Alertmanager incidents from api.listIncidents()
   * @property {Array} backupJobs - Recent backup jobs from api.listBackupJobs()
   * @property {Array} processes - Process list from /api/host/processes
   * @property {boolean} loading - Dashboard loading state
   * @property {Function} onLoadProcesses - Callback to load processes on demand
   */
  let {
    metrics,
    hostMetrics,
    alerts = [],
    incidents = [],
    backupJobs = [],
    processes = [],
    loading = false,
    onLoadProcesses,
  } = $props();

  // Collapsible sections
  let showProcesses = $state(false);
  let showDocker = $state(false);
  let showUsers = $state(false);

  const isAdmin = $derived(auth.isAdmin());
  const hasDocker = $derived(metrics?.docker?.available);

  const hostCpuPoints = $derived(
    (hostMetrics?.points || []).map((p) => ({ t: p.t, v: p.cpu_usage }))
  );
  const hostRamPct = $derived(
    (hostMetrics?.points || [])
      .filter((p) => p.total_ram > 0)
      .map((p) => ({ t: p.t, v: (p.used_ram / p.total_ram) * 100 }))
  );

  function jobStatusLabel(s) {
    if (s === 'success') return t('status.jobSuccess');
    if (s === 'error') return t('status.jobError');
    if (s === 'running') return t('status.jobRunning');
    return s;
  }

  function toggleProcesses() {
    showProcesses = !showProcesses;
    if (showProcesses && processes.length === 0) {
      onLoadProcesses?.();
    }
  }
</script>

<!-- Dashboard charts + alerts -->
<div class="grid grid-cols-1 lg:grid-cols-3 gap-4 mb-4">
  <div class="lg:col-span-2 rounded-xl border border-border bg-background p-4">
    <p class="text-sm font-semibold mb-1">{t('status.dashboardTitle')}</p>
    <p class="text-xs text-muted-foreground mb-3">{t('status.dashboardDesc')}</p>
    {#if loading}
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

  <!-- Alerts + incidents + backup jobs -->
  <div class="rounded-xl border border-border bg-background p-4">
    <div class="flex items-center justify-between mb-1">
      <p class="text-sm font-semibold">{t('status.alertsTitle')}</p>
    </div>
    <p class="text-xs text-muted-foreground mb-3">
      {t('status.alertsCount', {
        n: alerts.length + incidents.filter((i) => i.status === 'firing').length,
      })}
    </p>
    <div class="space-y-1.5 max-h-48 overflow-y-auto">
      <!-- Threshold alerts -->
      {#each alerts as alert (alert.vm_id + alert.rule?.id)}
        <div
          class="flex items-center gap-2 text-xs rounded-lg bg-destructive/10 border border-destructive/30 px-2.5 py-1.5"
        >
          <span class="w-2 h-2 rounded-full bg-destructive shrink-0 animate-pulse"></span>
          <span class="flex-1 min-w-0 truncate font-mono">
            {alert.vm_id ? alert.vm_id.slice(0, 8) : 'host'} · {alert.rule?.metric || ''}
            {alert.rule?.above ? '>' : '<'}
            {alert.rule?.threshold || 0}
          </span>
          <span
            class="text-[10px] px-1.5 py-0.5 rounded font-mono bg-destructive/20 text-destructive uppercase"
          >
            {t('status.incidentFiring')}
          </span>
        </div>
      {/each}

      <!-- Alertmanager incidents -->
      {#each incidents as inc (inc.id || inc.fingerprint)}
        <div
          class="flex items-start gap-2 text-xs rounded-lg px-2.5 py-1.5 border {inc.status ===
          'firing'
            ? inc.severity === 'critical'
              ? 'bg-destructive/10 border-destructive/30'
              : 'bg-amber-500/10 border-amber-500/30'
            : 'bg-muted/30 border-border text-muted-foreground'}"
        >
          <span
            class="w-2 h-2 rounded-full shrink-0 mt-1 {inc.status === 'firing'
              ? inc.severity === 'critical'
                ? 'bg-destructive animate-pulse'
                : 'bg-amber-500 animate-pulse'
              : 'bg-muted-foreground'}"
          ></span>
          <div class="flex-1 min-w-0 space-y-0.5">
            <div class="flex items-center justify-between gap-1">
              <span class="font-medium truncate">{inc.alertname || inc.summary}</span>
              <span
                class="text-[9px] px-1 py-0.2 rounded font-mono uppercase {inc.status === 'firing'
                  ? 'bg-destructive/20 text-destructive'
                  : 'bg-muted text-muted-foreground'}"
              >
                {inc.status === 'firing'
                  ? t('status.incidentFiring')
                  : t('status.incidentResolved')}
              </span>
            </div>
            {#if inc.summary && inc.summary !== inc.alertname}
              <p class="text-[11px] truncate opacity-90">{inc.summary}</p>
            {/if}
            <div class="flex items-center gap-2 text-[10px] text-muted-foreground font-mono">
              {#if inc.instance}<span>{inc.instance}</span>{/if}
              {#if inc.severity}<span class="uppercase">[{inc.severity}]</span>{/if}
              <span>{inc.source || 'alertmanager'}</span>
            </div>
          </div>
        </div>
      {/each}

      {#if alerts.length === 0 && incidents.length === 0}
        <p class="text-sm text-muted-foreground py-2 text-center">{t('status.noIncidents')}</p>
      {/if}
    </div>

    <!-- Backup jobs (admin-only) -->
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

<!-- Collapsible sections: Docker, Processes, Users -->
{#if metrics}
  <!-- Processes (collapsible) -->
  <div class="mt-4">
    <div class="rounded-xl border border-border bg-card p-5">
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
          {t('host.processes')}
        </h2>
        <button
          class="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
          onclick={toggleProcesses}
        >
          <Icon name={showProcesses ? 'chevron-down' : 'chevron-right'} size={12} />
          {showProcesses ? t('common.collapse') : t('common.expand')}
        </button>
      </div>
      {#if showProcesses}
        <ProcessPanel {processes} />
      {:else}
        <p class="text-sm text-muted-foreground">
          {#if processes.length > 0}
            {t('host.clickToShow', { count: processes.length })}
          {:else}
            {t('host.clickToLoadProcesses')}
          {/if}
        </p>
      {/if}
    </div>
  </div>

  <!-- Docker (conditional, collapsible) -->
  {#if hasDocker}
    <div class="mt-4">
      <div class="rounded-xl border border-border bg-card p-5">
        <div class="flex items-center justify-between mb-3">
          <h2 class="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            {t('host.docker')}
          </h2>
          <button
            class="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
            onclick={() => (showDocker = !showDocker)}
          >
            <Icon name={showDocker ? 'chevron-down' : 'chevron-right'} size={12} />
            {showDocker ? t('common.collapse') : t('common.expand')}
          </button>
        </div>
        {#if showDocker}
          <DockerPanel docker={metrics.docker} />
        {:else if metrics.docker}
          <p class="text-sm text-muted-foreground">
            {metrics.docker.containers?.length || 0} {t('host.containers')}
          </p>
        {:else}
          <p class="text-sm text-muted-foreground animate-pulse">
            {t('common.loading')}
          </p>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Users (collapsible) -->
  <div class="mt-4">
    <div class="rounded-xl border border-border bg-card p-5">
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
          {t('host.loggedInUsers')}
        </h2>
        <button
          class="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
          onclick={() => (showUsers = !showUsers)}
        >
          <Icon name={showUsers ? 'chevron-down' : 'chevron-right'} size={12} />
          {showUsers ? t('common.collapse') : t('common.expand')}
        </button>
      </div>
      {#if showUsers}
        <UsersPanel users={metrics.users || []} />
      {:else}
        <p class="text-sm text-muted-foreground">
          {metrics.users?.length || 0} {t('host.usersLoggedIn')}
        </p>
      {/if}
    </div>
  </div>
{/if}
