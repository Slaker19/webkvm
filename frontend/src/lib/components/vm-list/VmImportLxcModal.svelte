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
    networks = [],
    importing = false,
    progress = 0,
    phase = '',
    error = '',
    onImport = () => {},
  } = $props();

  let file = $state(null);
  let name = $state('');
  let pool = $state('');
  let network = $state('');

  $effect(() => {
    if (open) {
      file = null;
      name = '';
      pool = pools[0]?.name || '';
      network = networks[0]?.name || '';
    }
  });

  function handleSubmit() {
    if (!file || importing) return;
    onImport({ file, name, pool, network });
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vms.importLxcTitle')}</Dialog.Title>
      <Dialog.Description>{t('vms.importLxcDesc')}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      <div>
        <label for="import-lxc-file-comp" class="block text-sm font-medium mb-1.5"
          >{t('vms.backupOrCtar')}</label
        >
        <Input
          id="import-lxc-file-comp"
          type="file"
          accept=".tar.zst,.tar.gz,.tgz,.tar"
          onchange={(e) => (file = e.target.files?.[0] || null)}
        />
        {#if file}
          <p class="text-xs text-muted-foreground mt-1.5">
            {file.name} ({(file.size / 1024 / 1024).toFixed(1)} MB)
          </p>
        {/if}
      </div>
      <div>
        <label for="import-lxc-name-comp" class="block text-sm font-medium mb-1.5"
          >{t('vms.containerName')}</label
        >
        <Input
          id="import-lxc-name-comp"
          bind:value={name}
          placeholder={t('vms.containerNamePlaceholder')}
        />
      </div>
      {#if pools.length > 1}
        <div>
          <label for="import-lxc-pool-comp" class="block text-sm font-medium mb-1.5"
            >{t('vms.storagePoolLabel')}</label
          >
          <select id="import-lxc-pool-comp" bind:value={pool} class="input">
            {#each pools as p (p.name)}<option value={p.name}>{p.name}</option>{/each}
          </select>
        </div>
      {/if}
      {#if networks.length > 1}
        <div>
          <label for="import-lxc-net-comp" class="block text-sm font-medium mb-1.5"
            >{t('vms.networkLabel')}</label
          >
          <select id="import-lxc-net-comp" bind:value={network} class="input">
            <option value="">{t('vms.poolDefault')}</option>
            {#each networks as n (n.name)}<option value={n.name}>{n.name}</option>{/each}
          </select>
        </div>
      {/if}
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
        {#if importing}<Spinner size="sm" color="text-white" />{:else}{t('vms.importLxc')}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
