<script>
  import * as Dialog from './ui/dialog';
  import { Button } from './ui/button';
  import { Copy, Check, AlertTriangle } from '@lucide/svelte';

  let {
    open = $bindable(false),
    username = '',
    password = '',
    title = 'VM Credentials',
    onClose = null,
  } = $props();

  let copied = $state(false);

  function copyToClipboard() {
    navigator.clipboard.writeText(`Username: ${username}\nPassword: ${password}`);
    copied = true;
    setTimeout(() => (copied = false), 2000);
  }

  function handleClose() {
    if (onClose) onClose();
    else open = false;
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{title}</Dialog.Title>
      <Dialog.Description>Save these credentials now. They won't be shown again.</Dialog.Description
      >
    </Dialog.Header>

    <div class="space-y-3">
      <div class="rounded-lg border border-border bg-muted/50 p-4 space-y-2">
        <div class="flex items-center justify-between">
          <span class="text-xs text-muted-foreground">Username</span>
          <span class="text-sm font-mono font-medium">{username}</span>
        </div>
        <div class="flex items-center justify-between">
          <span class="text-xs text-muted-foreground">Password</span>
          <span class="text-sm font-mono font-medium">{password}</span>
        </div>
      </div>

      <div class="flex items-start gap-2 rounded-lg border border-amber-500/20 bg-amber-500/5 p-3">
        <AlertTriangle class="h-4 w-4 text-amber-500 mt-0.5 shrink-0" />
        <p class="text-xs text-amber-600 dark:text-amber-400">
          This password will only be shown once. Copy it and store it securely. You'll need it to
          log into the VM via serial console or SSH.
        </p>
      </div>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={copyToClipboard}>
        {#if copied}
          <Check class="h-3.5 w-3.5 mr-1.5" />
          Copied!
        {:else}
          <Copy class="h-3.5 w-3.5 mr-1.5" />
          Copy Credentials
        {/if}
      </Button>
      <Button onclick={handleClose}>I've Saved It</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
