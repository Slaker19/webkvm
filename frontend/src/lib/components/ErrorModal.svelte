<script>
  import * as Dialog from './ui/dialog';
  import { Button } from './ui/button';
  import { AlertTriangle } from '@lucide/svelte';

  let {
    open = $bindable(false),
    title = 'Something went wrong',
    message = '',
    onClose = null,
  } = $props();

  function handleClose() {
    if (onClose) onClose();
    else open = false;
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{title}</Dialog.Title>
      <Dialog.Description>Please review the details below and try again.</Dialog.Description>
    </Dialog.Header>

    <div class="flex items-start gap-3 rounded-lg border border-red-500/20 bg-red-500/5 p-4">
      <AlertTriangle class="h-5 w-5 text-red-500 mt-0.5 shrink-0" />
      <p class="text-sm text-foreground whitespace-pre-wrap">{message}</p>
    </div>

    <Dialog.Footer class="gap-2">
      <Button onclick={handleClose}>Got it</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
