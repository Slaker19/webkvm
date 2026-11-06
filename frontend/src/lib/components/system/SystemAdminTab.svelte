<script>
  import { t } from '$lib/i18n.svelte.js';
  import { Card } from '$lib/components/ui/card';
  import { Button } from '$lib/components/ui/button';
  import Alert from '$lib/components/Alert.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import * as Dialog from '$lib/components/ui/dialog';

  /**
   * @typedef {Object} Props
   * @property {any} status - System status from /api/system/status
   * @property {string} logs - System logs text
   * @property {any} backupsList - Backup files list {mounted, backups, host}
   * @property {boolean} logsLoading - Loading state for logs
   * @property {boolean} backupsLoading - Loading state for backups
   * @property {boolean} logsAuto - Auto-refresh state for logs
   * @property {Function} onLoadLogs - Callback to reload logs
   * @property {Function} onToggleAutoRefresh - Callback to toggle logs auto-refresh
   * @property {Function} onLoadBackups - Callback to reload backups
   * @property {Function} onRestart - Callback for restart action
   * @property {Function} onUpdate - Callback for update action
   * @property {Function} onBackup - Callback for backup action
   */
  let {
    status,
    logs,
    backupsList,
    logsLoading = false,
    backupsLoading = false,
    logsAuto = false,
    onLoadLogs,
    onToggleAutoRefresh,
    onLoadBackups,
    onRestart,
    onUpdate,
    onBackup,
  } = $props();

  let showRestartConfirm = $state(false);
  let showUpdateConfirm = $state(false);
  let showBackupConfirm = $state(false);
  let showUpdateResult = $state(false);
  let updateResult = $state('');

  let restartLoading = $state(false);
  let updateLoading = $state(false);
  let backupLoading = $state(false);

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

  function fmtDate(s) {
    if (!s) return '—';
    try {
      return new Date(s).toLocaleString();
    } catch {
      return s;
    }
  }

  async function handleRestart() {
    restartLoading = true;
    try {
      await onRestart?.();
      showRestartConfirm = false;
    } finally {
      restartLoading = false;
    }
  }

  async function handleUpdate() {
    updateLoading = true;
    try {
      const result = await onUpdate?.();
      if (result) {
        const logPath = result.log || '/var/log/webkvm/update.log';
        updateResult = result.mode ? `${result.mode} · ${logPath}` : logPath;
        showUpdateConfirm = false;
        showUpdateResult = true;
      }
    } finally {
      updateLoading = false;
    }
  }

  async function handleBackup() {
    backupLoading = true;
    try {
      await onBackup?.();
      showBackupConfirm = false;
    } finally {
      backupLoading = false;
    }
  }
</script>

{#if status?.backend && status?.host && status?.disk && status?.load}
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
              {#if status.update_mode === 'source'}
                {t('status.updateFromSource')}
              {:else}
                {t('status.updateTo', {
                  version: status.latest_version || status.backend.version,
                })}
              {/if}
            </div>
            <div class="text-xs text-muted-foreground mt-0.5">
              {status.update_mode === 'source'
                ? t('status.updateDescSource')
                : t('status.updateDescRelease')}
            </div>
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
              {#if backupsList?.mounted}
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
        <Button variant="outline" size="sm" onclick={onLoadLogs} disabled={logsLoading}>
          <Icon name="refresh" size={14} />
          {logsLoading ? t('common.loading') : t('status.refresh')}
        </Button>
        <Button variant={logsAuto ? 'default' : 'outline'} size="sm" onclick={onToggleAutoRefresh}>
          {logsAuto ? t('status.autoRefreshOn') : t('status.autoRefreshOff')}
        </Button>
      </div>
    </div>
    <pre
      class="bg-muted/30 border border-border rounded-md p-3 text-xs font-mono text-muted-foreground overflow-auto max-h-96 whitespace-pre-wrap break-all"
    >{logs || t('status.noLogs')}</pre>
  </Card>

  <!-- Backups -->
  <Card class="p-5 mb-4">
    <div class="flex items-center justify-between mb-3">
      <h2 class="text-sm font-semibold">{t('status.backups')}</h2>
      <Button variant="outline" size="sm" onclick={onLoadBackups} disabled={backupsLoading}>
        <Icon name="refresh" size={14} />
        {backupsLoading ? t('common.loading') : t('status.refresh')}
      </Button>
    </div>
    {#if !backupsList?.mounted}
      <Alert variant="info">{t('status.smbNotMounted')}</Alert>
    {:else if !backupsList?.backups || backupsList.backups.length === 0}
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

<!-- Restart confirm -->
<ConfirmDialog
  bind:open={showRestartConfirm}
  title={t('status.restartServiceConfirmTitle')}
  description={t('status.restartServiceConfirmDesc')}
  confirmLabel={t('status.restart')}
  variant="default"
  loading={restartLoading}
  onConfirm={handleRestart}
/>

<!-- Update confirm -->
<ConfirmDialog
  bind:open={showUpdateConfirm}
  title={status?.update_mode === 'source'
    ? t('status.updateFromSourceTitle')
    : t('status.updateToTitle', { version: status?.latest_version || '' })}
  description={status?.update_mode === 'source'
    ? t('status.updateConfirmSourceDesc')
    : t('status.updateConfirmDesc')}
  confirmLabel={status?.update_mode === 'source'
    ? t('status.updateFromSource')
    : t('status.update')}
  variant="default"
  loading={updateLoading}
  onConfirm={handleUpdate}
/>

<!-- Backup confirm -->
<ConfirmDialog
  bind:open={showBackupConfirm}
  title={t('status.backupNowTitle')}
  description={t('status.backupNowDesc')}
  confirmLabel={t('status.backup')}
  variant="default"
  loading={backupLoading}
  onConfirm={handleBackup}
/>

<!-- Update result -->
<Dialog.Root bind:open={showUpdateResult}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('status.updateStartedTitle')}</Dialog.Title>
      <Dialog.Description>{t('status.updateStartedDesc')}</Dialog.Description>
    </Dialog.Header>
    <pre
      class="bg-muted/30 border border-border rounded p-3 text-xs font-mono text-muted-foreground overflow-auto break-all"
    >{updateResult}</pre>
    <Button
      class="w-full"
      onclick={() => {
        showUpdateResult = false;
      }}>{t('common.ok')}</Button>
  </Dialog.Content>
</Dialog.Root>
