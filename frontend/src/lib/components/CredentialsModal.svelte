<script>
  import * as Dialog from './ui/dialog';
  import { Button } from './ui/button';
  import { Copy, Check, AlertTriangle, Database } from '@lucide/svelte';
  import { useCopyFeedback } from '$lib/utils/copyFeedback.svelte.js';
  import { t } from '$lib/i18n.svelte.js';

  let { open = $bindable(false), info = null, onClose = null } = $props();

  const copyState = useCopyFeedback();

  const hasDB = $derived(Boolean(info && (info.engine || info.db_name || info.db_user)));

  function copyAll() {
    const lines = [];
    if (info?.app) lines.push(`App: ${info.app}`);
    if (info?.path) lines.push(t('credentialsModal.urlLine', { path: info.path }));
    if (hasDB) {
      lines.push(
        `Database: ${info.engine || ''} db=${info.db_name || '-'} user=${info.db_user || '-'} pass=${info.db_pass || '-'}`
      );
    }
    lines.push(t('credentialsModal.changeAllPasswordsReminder'));
    copyState.copy(lines.join('\n'));
  }

  function handleClose() {
    if (onClose) onClose();
    else open = false;
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{info?.app || 'App'} {t('credentialsModal.titleSuffix')}</Dialog.Title>
      <Dialog.Description>
        {t('credentialsModal.description')}
      </Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3">
      <div class="rounded-lg border border-border bg-muted/50 p-4 space-y-2">
        {#if info?.path}
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-muted-foreground shrink-0"
              >{t('credentialsModal.pathLabel')}</span
            >
            <code class="text-sm font-mono truncate max-w-[65%]">http://&lt;IP&gt;{info.path}</code>
          </div>
        {/if}
        {#if hasDB}
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-muted-foreground flex items-center gap-1">
              <Database class="h-3.5 w-3.5" /> {t('credentialsModal.engineLabel')}</span
            >
            <span class="text-sm font-mono">{info.engine}</span>
          </div>
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-muted-foreground">{t('credentialsModal.dbNameLabel')}</span>
            <span class="text-sm font-mono">{info.db_name || '-'}</span>
          </div>
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-muted-foreground">{t('credentialsModal.dbUserLabel')}</span>
            <span class="text-sm font-mono">{info.db_user || '-'}</span>
          </div>
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-muted-foreground">{t('credentialsModal.dbPassLabel')}</span>
            <span class="text-sm font-mono break-all">{info.db_pass || '-'}</span>
          </div>
        {:else}
          <p class="text-sm text-muted-foreground">{t('credentialsModal.noDatabase')}</p>
        {/if}
      </div>

      <div class="flex items-start gap-2 rounded-lg border border-warning/20 bg-warning-subtle p-3">
        <AlertTriangle class="h-4 w-4 text-warning mt-0.5 shrink-0" />
        <p class="text-xs text-warning">
          {t('credentialsModal.securityWarning')}
        </p>
      </div>

      <p class="text-[11px] text-muted-foreground">
        {t('credentialsModal.alsoInVmHint')}
      </p>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={copyAll}>
        {#if copyState.copied}
          <Check class="h-3.5 w-3.5 mr-1.5" /> {t('credentialsModal.copied')}
        {:else}
          <Copy class="h-3.5 w-3.5 mr-1.5" /> {t('credentialsModal.copyAll')}
        {/if}
      </Button>
      <Button onclick={handleClose}>{t('credentialsModal.gotIt')}</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
