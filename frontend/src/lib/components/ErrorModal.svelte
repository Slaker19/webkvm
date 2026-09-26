<script>
  import * as Dialog from './ui/dialog';
  import { Button } from './ui/button';
  import { AlertTriangle } from '@lucide/svelte';
  import { t } from '$lib/i18n.svelte.js';

  let { open = $bindable(false), title = '', message = '', onClose = null } = $props();

  function handleClose() {
    if (onClose) onClose();
    else open = false;
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md [&>*]:min-w-0">
    <Dialog.Header>
      <Dialog.Title>{title || t('errorModal.title')}</Dialog.Title>
      <Dialog.Description>{t('errorModal.description')}</Dialog.Description>
    </Dialog.Header>

    <div
      class="flex items-start gap-3 rounded-lg border border-destructive/20 bg-destructive-subtle p-4 min-w-0"
    >
      <AlertTriangle class="h-5 w-5 text-destructive mt-0.5 shrink-0" />
      <p class="text-sm text-foreground whitespace-pre-wrap break-words min-w-0">{message}</p>
    </div>

    <Dialog.Footer class="gap-2">
      <Button onclick={handleClose}>{t('errorModal.gotIt')}</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
