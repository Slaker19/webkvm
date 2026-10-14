<script>
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';

  let {
    vm = {},
    vmId = '',
    isContainerVm = false,
    load,
  } = $props();

  let actionLoading = $state('');
  let newSharedFolderPath = $state('');
  let newSharedFolderTag = $state('');
  let newSharedFolderReadOnly = $state(false);

  function sharedFolder9pMountCommand(tag) {
    return `mount -t 9p -o trans=virtio,version=9p2000.L ${tag} /mnt/${tag}`;
  }

  async function attachSharedFolder() {
    actionLoading = 'sharedfolder';
    try {
      await api.attachSharedFolder(
        vmId,
        newSharedFolderPath,
        newSharedFolderTag,
        newSharedFolderReadOnly
      );
      toast.success(t('vmDetail.sharedFolderAttached'));
      newSharedFolderPath = '';
      newSharedFolderTag = '';
      newSharedFolderReadOnly = false;
      await load?.();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  async function detachSharedFolder(tag) {
    actionLoading = 'sharedfolder';
    try {
      await api.detachSharedFolder(vmId, tag);
      toast.success(t('vmDetail.sharedFolderDetached'));
      await load?.();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }
</script>

<BlockCard bid="sharedfolders" title={t('vmDetail.sharedFolders')}>
  <div class="space-y-3">
    <p class="text-xs text-muted-foreground">{t('vmDetail.sharedFoldersHint')}</p>
    {#if !vm?.shared_folders || vm.shared_folders.length === 0}
      <EmptyState
        compact
        icon="folderOpen"
        title={t('vmDetail.sharedFoldersNoneAttached')}
      />
    {:else}
      <div class="space-y-2">
        {#each vm.shared_folders as f (f.tag)}
          <div class="rounded-md border border-border bg-background p-2 space-y-1">
            <div class="flex items-center justify-between">
              <div class="text-xs min-w-0">
                <div class="font-mono truncate">{f.host_path}</div>
                <div class="text-muted-foreground">
                  {t('vmDetail.sharedFolderTag')}: <span class="font-mono">{f.tag}</span>
                  {#if f.read_only}· {t('vmDetail.sharedFolderReadOnly')}{/if}
                </div>
              </div>
              <button
                onclick={() => detachSharedFolder(f.tag)}
                disabled={actionLoading === 'sharedfolder' ||
                  (!isContainerVm && vm.state !== 'shutoff')}
                title={!isContainerVm && vm.state !== 'shutoff'
                  ? t('vmDetail.sharedFolderRequiresShutoff')
                  : ''}
                class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10 disabled:opacity-50 disabled:cursor-not-allowed shrink-0"
                >{t('vmDetail.sharedFolderDetach')}</button
              >
            </div>
            <div class="text-[10px] font-mono text-muted-foreground truncate">
              {isContainerVm
                ? `Montado en: ${f.tag.startsWith('/') ? f.tag : '/mnt/' + f.tag}`
                : sharedFolder9pMountCommand(f.tag)}
            </div>
          </div>
        {/each}
      </div>
    {/if}
    <div class="border-t border-border pt-3 space-y-2">
      <div class="text-xs font-medium text-muted-foreground">
        {t('vmDetail.sharedFolderAdd')}
      </div>
      <Input
        bind:value={newSharedFolderPath}
        placeholder={t('vmDetail.sharedFolderHostPath')}
        class="!text-xs"
      />
      <Input
        bind:value={newSharedFolderTag}
        placeholder={t('vmDetail.sharedFolderTag')}
        class="!text-xs"
      />
      <label class="flex items-center gap-1.5 text-xs">
        <input type="checkbox" bind:checked={newSharedFolderReadOnly} />
        {t('vmDetail.sharedFolderReadOnly')}
      </label>
      <Button
        size="xs"
        variant="outline"
        onclick={attachSharedFolder}
        disabled={actionLoading === 'sharedfolder' ||
          (!isContainerVm && vm?.state !== 'shutoff') ||
          !newSharedFolderPath ||
          !newSharedFolderTag}
        title={!isContainerVm && vm?.state !== 'shutoff'
          ? t('vmDetail.sharedFolderRequiresShutoff')
          : ''}>{t('vmDetail.sharedFolderAttach')}</Button
      >
    </div>
  </div>
</BlockCard>
