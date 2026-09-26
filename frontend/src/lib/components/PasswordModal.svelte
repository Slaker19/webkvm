<script>
  import * as Dialog from './ui/dialog';
  import { Button } from './ui/button';
  import { Copy, Check, AlertTriangle } from '@lucide/svelte';
  import { useCopyFeedback } from '$lib/utils/copyFeedback.svelte.js';
  import { t } from '$lib/i18n.svelte.js';

  let {
    open = $bindable(false),
    username = '',
    password = '',
    title = '',
    onClose = null,
  } = $props();

  const copyState = useCopyFeedback();

  function copyToClipboard() {
    copyState.copy(t('passwordModal.clipboardTemplate', { username, password }));
  }

  function handleClose() {
    if (onClose) onClose();
    else open = false;
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md [&>*]:min-w-0">
    <Dialog.Header>
      <Dialog.Title>{title || t('passwordModal.title')}</Dialog.Title>
      <Dialog.Description>{t('passwordModal.description')}</Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3 min-w-0">
      <div class="rounded-lg border border-border bg-muted/50 p-4 space-y-2 min-w-0">
        <div class="flex items-center justify-between gap-3 min-w-0">
          <span class="text-xs text-muted-foreground shrink-0">{t('passwordModal.username')}</span>
          <span class="text-sm font-mono font-medium truncate select-all">{username}</span>
        </div>
        <div class="flex items-center justify-between gap-3 min-w-0">
          <span class="text-xs text-muted-foreground shrink-0">{t('passwordModal.password')}</span>
          <span class="text-sm font-mono font-medium break-all select-all text-right"
            >{password}</span
          >
        </div>
      </div>

      <div class="flex items-start gap-2 rounded-lg border border-warning/20 bg-warning-subtle p-3">
        <AlertTriangle class="h-4 w-4 text-warning mt-0.5 shrink-0" />
        <p class="text-xs text-warning">
          {t('passwordModal.warning')}
        </p>
      </div>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={copyToClipboard}>
        {#if copyState.copied}
          <Check class="h-3.5 w-3.5 mr-1.5" />
          {t('passwordModal.copied')}
        {:else}
          <Copy class="h-3.5 w-3.5 mr-1.5" />
          {t('passwordModal.copyCredentials')}
        {/if}
      </Button>
      <Button onclick={handleClose}>{t('passwordModal.savedIt')}</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
