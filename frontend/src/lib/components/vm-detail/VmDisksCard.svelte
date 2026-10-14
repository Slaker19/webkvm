<script>
  import { t } from '$lib/i18n.svelte.js';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { Button } from '$lib/components/ui/button';

  let {
    vm = {},
    pools = [],
    isContainerVm = false,
    requireShutoff,
    removeDisk,
    onAddDisk,
    onChangeIso,
    onResizeDisk,
    onChangeBus,
  } = $props();

  function diskLabel(d) {
    if (d.device === 'cdrom') return d.name || (d.source ? d.source.split('/').pop() : '(empty)');
    if (d.zvol) return `zvol: ${d.zvol}`;
    return d.name || (d.source ? d.source.split('/').pop() : '(unknown)');
  }
</script>

<BlockCard bid="disks" title={t('vmDetail.disks')}>
  <div class="flex items-center justify-between mb-3">
    <Button size="xs" variant="outline" onclick={onAddDisk}>
      + Add Disk
    </Button>
  </div>
  {#if !vm?.disks || vm.disks.length === 0}
    <EmptyState compact icon="hardDrive" title={t('vmDetail.noDisks')} />
  {:else}
    <div class="space-y-1.5">
      {#each vm.disks as disk (disk.target || disk.source || disk.name)}
        <div
          class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
        >
          <div class="flex items-center gap-2 min-w-0">
            <span class="text-xs font-mono text-muted-foreground w-8">{disk.target}</span>
            <span
              class="text-xs px-1.5 py-0.5 rounded border {disk.device === 'cdrom'
                ? 'border-accent/30 bg-accent/10 text-accent'
                : disk.zvol
                  ? 'border-primary/30 bg-primary/10 text-primary'
                  : 'border-border bg-muted text-muted-foreground'}"
              >{disk.device === 'cdrom' ? 'CDROM' : disk.zvol ? 'ZVOL' : 'DISK'}</span
            >
            <span class="text-xs text-muted-foreground">{disk.bus}</span>
            <span class="text-sm truncate">{diskLabel(disk)}</span>
            {#if disk.serial}
              <span
                class="text-[11px] font-mono px-1.5 py-0.5 rounded bg-muted text-muted-foreground"
                title={t('vmDetail.diskSerialLabel')}>SN: {disk.serial}</span
              >
            {/if}
            {#if disk.wwn}
              <span
                class="text-[11px] font-mono px-1.5 py-0.5 rounded bg-muted text-muted-foreground"
                title={t('vmDetail.diskWwnLabel')}>WWN: {disk.wwn}</span
              >
            {/if}
            {#if disk.alias}
              <span
                class="text-[11px] font-mono px-1.5 py-0.5 rounded bg-muted text-muted-foreground"
                title={t('vmDetail.diskAliasLabel')}>{disk.alias}</span
              >
            {/if}
          </div>
          <div class="flex items-center gap-1 shrink-0">
            {#if disk.device === 'cdrom'}
              <button
                onclick={() => onChangeIso?.(disk)}
                class="text-xs text-accent hover:text-accent-hover px-2 py-1 rounded hover:bg-muted"
                >{t('vmDetail.changeIso')}</button
              >
            {:else if disk.pool}
              <span class="text-xs text-muted-foreground tnum"
                >{disk.size_gb ? disk.size_gb + ' GB' : ''}</span
              >
              <button
                onclick={() => onResizeDisk?.(disk)}
                class="text-xs text-accent hover:text-accent-hover px-2 py-1 rounded hover:bg-muted"
                >{t('vmDetail.resize')}</button
              >
            {/if}
            {#if !isContainerVm}
              <button
                onclick={() => onChangeBus?.(disk)}
                class="text-xs text-accent hover:text-accent-hover px-2 py-1 rounded hover:bg-muted"
                >{t('vmDetail.changeBus')}</button
              >
            {/if}
            {#if disk.target !== 'root'}
              <button
                onclick={() => {
                  if (!isContainerVm && vm.disks.length <= 1 && disk.device !== 'cdrom') {
                    if (!requireShutoff?.(t('vmDetail.removing'))) return;
                  }
                  removeDisk?.(disk.target);
                }}
                class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10"
                >{t('vmDetail.remove')}</button
              >
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</BlockCard>
