<script>
  /**
   * HaRestoreDialog — restore a released KVM VM on the node serving this
   * panel: DRBD up + promote, mount, define (optionally start). The backend
   * rolls every step back on failure; the VM name must be typed back.
   */
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import Alert from '$lib/components/Alert.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import CopyCommands from '$lib/components/CopyCommands.svelte';
  import { Button } from '$lib/components/ui/button';
  import { haErrorText, peersDownDetails } from '$lib/utils/haWorkloads.js';

  // entry: a released VM view ({ name, drbd_resource, mount_dir, ... }).
  let { entry = null, node = '', onDone = () => {}, onCancel = () => {} } = $props();

  let typed = $state('');
  let start = $state(false);
  let busy = $state(false);
  let error = $state('');
  // drbd_peers_down: { resource, peers: [{node, connection, command}], commands }
  let peersDown = $state(null);
  let lastName = '';

  $effect(() => {
    if (entry && entry.name !== lastName) {
      lastName = entry.name;
      typed = '';
      start = false;
      error = '';
      peersDown = null;
    }
    if (!entry) lastName = '';
  });

  const ready = $derived(!!entry && typed === entry.name && !busy);

  async function confirm() {
    if (!entry || !ready) return;
    busy = true;
    error = '';
    peersDown = null;
    try {
      const res = await api.restoreReleasedVM(entry.name, { confirm: typed, start, node });
      const where = res?.node || node;
      if (res?.changed === false) {
        toast.success(t('haReleased.restoredNoop', { name: entry.name }));
      } else if (res?.state === 'running') {
        toast.success(t('haReleased.restoredStarted', { name: entry.name, node: where }));
      } else {
        toast.success(t('haReleased.restored', { name: entry.name, node: where }));
      }
      if (res?.xml_differs) toast.info(t('haReleased.xmlDiffers'));
      toast.info(t('haReleased.enableHaHint'));
      onDone(res);
    } catch (e) {
      error = haErrorText(t, e);
      peersDown = peersDownDetails(e);
    } finally {
      busy = false;
    }
  }
</script>

<ConfirmDialog
  open={!!entry}
  title={t('haReleased.restoreTitle', { name: entry?.name || '' })}
  description={t('haReleased.restoreBody', { node: node || '—' })}
  confirmLabel={t('haReleased.restoreConfirm')}
  loading={busy}
  onConfirm={confirm}
  onCancel={() => onCancel()}
>
  <ol class="list-decimal pl-5 space-y-1 text-sm" data-testid="ha-restore-steps">
    {#if entry?.drbd_resource}
      <li data-testid="ha-restore-step-peers">
        {entry.drbd_peers?.length
          ? t('haReleased.stepPeers', {
              res: entry.drbd_resource,
              nodes: entry.drbd_peers.join(', '),
            })
          : t('haReleased.stepPeersUnknown', { res: entry.drbd_resource })}
      </li>
      <li>{t('haReleased.stepDrbd', { res: entry.drbd_resource })}</li>
      <li>{t('haReleased.stepMount', { dir: entry.mount_dir || '' })}</li>
    {:else}
      <li>{t('haReleased.stepNoStorage')}</li>
    {/if}
    <li>{t('haReleased.stepDefine')}</li>
    {#if entry?.drbd_resource}
      <li>{t('haReleased.stepLink', { name: entry.name })}</li>
    {/if}
  </ol>
  <label class="flex items-center gap-2 text-sm cursor-pointer min-h-11">
    <input type="checkbox" class="h-4 w-4" bind:checked={start} />
    <span>{t('haReleased.startAfter')}</span>
  </label>
  <div class="space-y-1">
    <label for="ha-restore-confirm" class="text-xs font-semibold">
      {t('haReleased.confirmLabel', { name: entry?.name || '' })}
    </label>
    <input
      id="ha-restore-confirm"
      class="input w-full min-h-11 font-mono text-sm"
      autocomplete="off"
      bind:value={typed}
    />
  </div>
  {#if error}<Alert variant="error">{error}</Alert>{/if}
  {#if peersDown}
    <div class="space-y-2" data-testid="ha-restore-peers-down">
      <p class="text-sm">{t('haReleased.peersDownIntro', { res: peersDown.resource })}</p>
      {#if peersDown.peers.length}
        <ul class="list-disc pl-5 text-sm" data-testid="ha-restore-peers">
          {#each peersDown.peers as p (p.node)}
            <li>
              <span class="font-mono">{p.node}</span>
              <span class="text-muted-foreground">({p.connection})</span>
            </li>
          {/each}
        </ul>
      {/if}
      <CopyCommands commands={peersDown.commands} title={t('haReleased.peersDownCommands')} />
      <p class="text-xs text-muted-foreground">{t('haReleased.peersDownRecheckHint')}</p>
      <Button
        size="sm"
        variant="outline"
        class="min-h-11"
        disabled={!ready}
        data-testid="ha-restore-recheck"
        onclick={confirm}
      >
        {t('haReleased.recheck')}
      </Button>
    </div>
  {/if}
</ConfirmDialog>
