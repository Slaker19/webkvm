<script>
  import Alert from '$lib/components/Alert.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { Skeleton } from '$lib/components/ui/skeleton';
  import { t } from '../lib/i18n.svelte.js';

  let status = $state(null);
  let logs = $state('');
  let loading = $state(true);
  let error = $state('');
  let logsLoading = $state(false);
  let logsAuto = $state(false);
  let actionMsg = $state('');
  let actionErr = $state('');

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

  async function loadStatus() {
    loading = true;
    error = '';
    try {
      status = await api.systemStatus();
    } catch (e) {
      error = e.message || String(e);
    } finally {
      loading = false;
    }
  }

  async function loadLogs() {
    logsLoading = true;
    try {
      logs = await api.systemLogs(200);
    } catch (e) {
      logs = t('status.loadLogsError', { error: e.message });
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
      setTimeout(() => {
        actionMsg = '';
        loadStatus();
      }, 8000);
    } catch (e) {
      actionErr = e.message;
      toast.error(e.message);
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
      actionErr = e.message;
      toast.error(e.message);
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
      setTimeout(() => {
        actionMsg = '';
      }, 8000);
    } catch (e) {
      actionErr = e.message;
      toast.error(e.message);
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
    loadStatus();
    loadLogs();
    loadBackups();
    return () => {
      if (logInterval) clearInterval(logInterval);
    };
  });
</script>

<div class="p-6 max-w-6xl">
  <PageHeader title={t('status.title')} subtitle={t('status.subtitle')}>
    {#snippet actions()}
      <Button variant="outline" size="sm" onclick={loadStatus}>
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

  {#if loading && !status}
    <!-- Skeleton loading state -->
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-3 mb-4">
      {#each Array(4) as _}
        <div class="border border-border rounded-lg bg-card p-4">
          <Skeleton class="h-3 w-16 mb-2" />
          <Skeleton class="h-5 w-20" />
          <Skeleton class="h-3 w-24 mt-2" />
        </div>
      {/each}
    </div>
    <Skeleton class="h-32 w-full mb-4" />
    <Skeleton class="h-24 w-full" />
  {:else if status}
    <!-- Stat cards -->
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-3 mb-4">
      <StatCard
        label={t('status.backend')}
        status={status.backend.version ? 'running' : 'crashed'}
        value={`v${status.backend.version}`}
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
        hint={t('status.currentVersion', { version: status.backend.version })}
      />
    </div>

    <!-- Storage pools -->
    <div class="border border-border rounded-lg bg-card p-5 mb-4">
      <h2 class="text-sm font-semibold mb-3">{t('status.storagePools')}</h2>
      <div class="space-y-3">
        {#each status.pools as p}
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
    </div>

    <!-- Actions + Host details -->
    <div class="grid grid-cols-1 md:grid-cols-2 gap-3 mb-4">
      <div class="border border-border rounded-lg bg-card p-4">
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
      </div>

      <div class="border border-border rounded-lg bg-card p-4">
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
      </div>
    </div>

    <!-- Logs -->
    <div class="border border-border rounded-lg bg-card p-5 mb-4">
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
    </div>

    <!-- Backups -->
    <div class="border border-border rounded-lg bg-card p-5 mb-4">
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
          {#each backupsList.backups.slice(0, 5) as b}
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
    </div>
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
{#if showUpdateResult}
  <div role="dialog" class="fixed inset-0 z-50 bg-black/50 flex items-center justify-center p-4">
    <div class="bg-card border border-border rounded-lg p-6 max-w-md w-full shadow-xl">
      <h3 class="text-base font-semibold mb-1">{t('status.updateStartedTitle')}</h3>
      <p class="text-sm text-muted-foreground mb-3">{t('status.updateStartedDesc')}</p>
      <pre
        class="bg-muted/30 border border-border rounded p-3 text-xs font-mono text-muted-foreground overflow-auto break-all mb-4">{updateResult}</pre>
      <Button
        class="w-full"
        onclick={() => {
          showUpdateResult = false;
          loadStatus();
        }}>{t('common.ok')}</Button
      >
    </div>
  </div>
{/if}
