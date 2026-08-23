<script>
  import * as Dialog from './ui/dialog';
  import { Button } from './ui/button';
  import { Loader2 } from '@lucide/svelte';

  let {
    open = $bindable(false),
    title = 'Are you sure?',
    description = '',
    message = '',
    confirmLabel = 'Confirm',
    cancelLabel = 'Cancel',
    variant = 'default', // 'default' | 'destructive'
    loading = false,
    hideCancel = false,
    onConfirm = () => {},
    onCancel = null,
    children,
  } = $props();

  async function handleConfirm() {
    if (loading) return;
    await onConfirm();
    // Caller should set open=false when done; we don't auto-close to allow async flow
  }

  function handleCancel() {
    if (onCancel) onCancel();
    else open = false;
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{title}</Dialog.Title>
      {#if description}
        <Dialog.Description>{description}</Dialog.Description>
      {/if}
    </Dialog.Header>
    {#if message}
      <p class="text-sm text-muted-foreground break-words">{message}</p>
    {/if}
    {#if children}
      {@render children()}
    {/if}
    <Dialog.Footer class="gap-2">
      {#if !hideCancel}
        <Button variant="outline" onclick={handleCancel} disabled={loading}>
          {cancelLabel}
        </Button>
      {/if}
      <Button
        variant={variant === 'destructive' ? 'destructive' : 'default'}
        onclick={handleConfirm}
        disabled={loading}
      >
        {#if loading}
          <Loader2 class="h-3.5 w-3.5 animate-spin" />
        {/if}
        {confirmLabel}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
