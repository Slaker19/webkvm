<script>
  import * as Dialog from './ui/dialog';
  import { Button } from './ui/button';
  import { Loader2 } from '@lucide/svelte';
  import { t } from '../i18n.svelte.js';

  let {
    open = $bindable(false),
    title = t('common.areYouSure'),
    description = '',
    message = '',
    confirmLabel = t('common.confirm'),
    cancelLabel = t('common.cancel'),
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

<!-- Closing via Esc / overlay must go through the same path as the
     Cancel button: most callers pass `open={!!pending}` without bind and
     clear `pending` in onCancel. Without this the parent never learned
     the dialog closed, so reopening it for the same item did nothing. -->
<Dialog.Root
  bind:open
  onOpenChange={(o) => {
    if (!o && onCancel) onCancel();
  }}
>
  <Dialog.Content class="sm:max-w-md [&>*]:min-w-0">
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
