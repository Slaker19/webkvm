<script>
  /**
   * HaReleasedList — KVM VMs whose HA was turned off with stop/forget. They
   * are no longer in libvirt; WebKVM kept their definition and storage
   * details so an admin can Restore them (or discard the entry). Rendered
   * only for admins with the Cluster/HA module on; `hideWhenEmpty` keeps the
   * VM list clean when there is nothing to show.
   */
  import { onMount } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Card } from '$lib/components/ui/card';
  import { Badge } from '$lib/components/ui/badge';
  import Alert from '$lib/components/Alert.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import HaRestoreDialog from '$lib/components/HaRestoreDialog.svelte';
  import { clusterModule } from '$lib/stores/clusterModule.svelte.js';
  import { haReleased, refreshHaReleased } from '$lib/stores/haReleased.svelte.js';
  import { haErrorText, canRestore } from '$lib/utils/haWorkloads.js';

  let { hideWhenEmpty = false, onChanged = () => {} } = $props();

  const enabled = $derived(clusterModule.enabled && auth.isAdmin());

  onMount(() => {
    if (clusterModule.enabled && auth.isAdmin()) refreshHaReleased();
  });

  const entries = $derived(haReleased.entries);
  const visible = $derived(enabled && (!hideWhenEmpty || entries.length > 0 || !!haReleased.error));

  function fmtTime(iso) {
    if (!iso) return '—';
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
  }

  let restoreFor = $state(null);
  async function restored() {
    restoreFor = null;
    await refreshHaReleased();
    onChanged();
  }

  let discardFor = $state(null);
  let discardTyped = $state('');
  let discardBusy = $state(false);
  let discardError = $state('');
  function openDiscard(e) {
    discardFor = e;
    discardTyped = '';
    discardError = '';
  }
  async function doDiscard() {
    if (!discardFor || discardBusy || discardTyped !== discardFor.name) return;
    discardBusy = true;
    discardError = '';
    try {
      await api.discardReleasedVM(discardFor.name, discardTyped);
      toast.success(t('haReleased.discarded', { name: discardFor.name }));
      discardFor = null;
      await refreshHaReleased();
      onChanged();
    } catch (e) {
      discardError = haErrorText(t, e);
    } finally {
      discardBusy = false;
    }
  }
</script>

{#if visible}
  <Card
    class={hideWhenEmpty ? 'p-4 space-y-3 mb-4' : 'p-4 space-y-3'}
    data-testid="ha-released-list"
  >
    <div>
      <h2 class="text-sm font-semibold text-foreground">{t('haReleased.title')}</h2>
      <p class="text-xs text-muted-foreground">{t('haReleased.description')}</p>
    </div>
    {#if haReleased.error}
      <Alert variant="error">{t('haReleased.loadError', { error: haReleased.error })}</Alert>
    {/if}
    {#if entries.length}
      <ul class="divide-y divide-border">
        {#each entries as e (e.name)}
          <li
            class="py-2 flex flex-wrap items-start justify-between gap-2"
            data-testid="ha-released-{e.name}"
          >
            <div class="min-w-0">
              <div class="font-medium text-sm text-foreground break-all">{e.name}</div>
              <div class="flex flex-wrap gap-1 mt-0.5">
                {#if e.status === 'restored'}
                  <Badge variant="success">{t('haReleased.badgeRestored')}</Badge>
                {:else}
                  <Badge variant="warning">{t('haReleased.badge')}</Badge>
                {/if}
                {#if !e.restorable}
                  <Badge variant="destructive">{t('haReleased.unrestorable')}</Badge>
                {/if}
              </div>
              <div class="text-xs text-muted-foreground mt-0.5 space-x-2">
                <span>{t('haReleased.releasedAt', { time: fmtTime(e.released_at) })}</span>
                {#if e.last_node}<span>{t('haReleased.lastNode', { node: e.last_node })}</span>{/if}
                {#if e.drbd_resource}<span class="font-mono"
                    >{t('haReleased.drbd', { res: e.drbd_resource })}</span
                  >{/if}
              </div>
            </div>
            <div class="flex flex-wrap gap-2">
              {#if canRestore(e)}
                <Button size="sm" class="min-h-11" onclick={() => (restoreFor = e)}>
                  {t('haReleased.restore')}
                </Button>
              {/if}
              <Button size="sm" variant="outline" class="min-h-11" onclick={() => openDiscard(e)}>
                {t('haReleased.discard')}
              </Button>
            </div>
          </li>
        {/each}
      </ul>
    {:else if haReleased.loaded && !haReleased.error}
      <p class="text-xs text-muted-foreground" data-testid="ha-released-empty">
        {t('haReleased.empty')}
      </p>
    {/if}
  </Card>
{/if}

<HaRestoreDialog
  entry={restoreFor}
  node={haReleased.localNode}
  onDone={restored}
  onCancel={() => (restoreFor = null)}
/>

<ConfirmDialog
  open={!!discardFor}
  title={t('haReleased.discardTitle', { name: discardFor?.name || '' })}
  description={t('haReleased.discardBody')}
  confirmLabel={t('haReleased.discardConfirm')}
  variant="destructive"
  loading={discardBusy}
  onConfirm={doDiscard}
  onCancel={() => (discardFor = null)}
>
  <div class="space-y-1">
    <label for="ha-discard-confirm" class="text-xs font-semibold">
      {t('haReleased.confirmLabel', { name: discardFor?.name || '' })}
    </label>
    <input
      id="ha-discard-confirm"
      class="input w-full min-h-11 font-mono text-sm"
      autocomplete="off"
      bind:value={discardTyped}
    />
  </div>
  {#if discardError}<Alert variant="error">{discardError}</Alert>{/if}
</ConfirmDialog>
