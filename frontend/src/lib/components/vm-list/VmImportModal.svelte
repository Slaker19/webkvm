<script>
  import * as Dialog from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import Spinner from '$lib/components/Spinner.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let {
    open = $bindable(false),
    pools = [],
    importing = false,
    progress = 0,
    phase = '',
    error = '',
    onImport = () => {},
  } = $props();

  let file = $state(null);
  let name = $state('');
  let pool = $state('webkvm-disks');

  $effect(() => {
    if (open) {
      file = null;
      name = '';
      pool = pools[0]?.name || 'webkvm-disks';
    }
  });

  function handleSubmit() {
    if (!file || importing) return;
    onImport({ file, name, pool });
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vms.importTitle')}</Dialog.Title>
      <Dialog.Description>{t('vms.importDesc')}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      <div>
        <label for="import-file-comp" class="block text-sm font-medium mb-1.5"
          >{t('vms.backupOrOva')}</label
        >
        <Input
          id="import-file-comp"
          type="file"
          accept=".tar.gz,.tgz,.tar.zst,.zst,.ova"
          onchange={(e) => (file = e.target.files?.[0] || null)}
        />
        {#if file}
          <p class="text-xs text-muted-foreground mt-1.5">
            {file.name} ({(file.size / 1024 / 1024).toFixed(1)} MB)
          </p>
        {/if}
      </div>
      <div>
        <label for="import-name-comp" class="block text-sm font-medium mb-1.5"
          >{t('vms.newVmName')}</label
        >
        <Input id="import-name-comp" bind:value={name} placeholder={t('vms.leaveEmpty')} />
      </div>
      <div>
        <label for="import-pool-comp" class="block text-sm font-medium mb-1.5"
          >{t('vms.storagePoolLabel')}</label
        >
        <select id="import-pool-comp" bind:value={pool} class="input" disabled={pools.length === 0}>
          {#if pools.length === 0}<option value="webkvm-disks">webkvm-disks</option>{/if}
          {#each pools as p (p.name)}<option value={p.name}>{p.name}</option>{/each}
        </select>
      </div>
      {#if error}
        <p class="text-sm text-destructive">{error}</p>
      {/if}
      {#if importing}
        <div class="bg-muted/30 rounded-md p-3 border border-border">
          <ProgressBar value={progress} label={phase || t('vms.uploading')} showValue size="sm" />
        </div>
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (open = false)} disabled={importing}
        >{t('common.cancel')}</Button
      >
      <Button onclick={handleSubmit} disabled={importing || !file}>
        {#if importing}<Spinner size="sm" color="text-white" />{:else}{t('vms.importVm')}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
