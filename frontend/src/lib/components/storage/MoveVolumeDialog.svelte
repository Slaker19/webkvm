<script>
  import * as Dialog from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import { Label } from '$lib/components/ui/label';
  import Switch from '$lib/components/Switch.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { toast } from '$lib/components/ui/toast';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { movablePools } from '$lib/purpose.js';

  let {
    open = $bindable(false),
    name = '',
    srcPool = '',
    kind = 'disk',
    pools = [],
    onMoved = () => {},
    onError = () => {},
  } = $props();

  let destPool = $state('');
  let copy = $state(false);
  let busy = $state(false);
  let progress = $state(null);

  const options = $derived(movablePools(pools, kind, srcPool));

  $effect(() => {
    if (open) {
      copy = false;
      progress = null;
      destPool = options[0]?.name || '';
    }
  });

  function moveStageLabel(stage) {
    const key = {
      move_preparing: 'storage.moveStagePreparing',
      move_copying: 'storage.moveStageCopying',
      move_verifying: 'storage.moveStageVerifying',
      move_updating: 'storage.moveStageUpdating',
      move_cleanup: 'storage.moveStageCleanup',
    }[stage];
    return key ? t(key) : '';
  }

  async function doMove() {
    if (busy || !destPool) return;
    busy = true;
    progress = { pct: 0, stage: 'move_preparing' };
    try {
      const res = await api.moveVolume(srcPool, name, {
        pool: destPool,
        kind,
        copy,
      });
      await api.waitJob(res.id, {
        title: `Mover volumen: ${name} (${srcPool} -> ${destPool})`,
        kind: 'storage',
        timeout: 4 * 60 * 60 * 1000,
        onPoll: (job) => {
          progress = { pct: job.progress || 0, stage: job.stage || '' };
        },
      });
      open = false;
      toast.success(
        t(copy ? 'storage.volumeCopied' : 'storage.volumeMoved', {
          name,
          pool: destPool,
        })
      );
      onMoved();
    } catch (e) {
      onError(t('storage.moveVolumeTitle'), e);
    } finally {
      busy = false;
      progress = null;
    }
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('storage.moveVolumeTitle')}</Dialog.Title>
      <Dialog.Description>
        {t('storage.moveVolumeDesc', { name, pool: srcPool })}
      </Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      {#if options.length === 0}
        <p class="text-sm text-muted-foreground">
          {t('storage.moveNoDestination')}
        </p>
      {:else}
        <div class="space-y-1.5">
          <Label for="move-vol-pool">{t('vmDetail.moveDestPool')}</Label>
          <select id="move-vol-pool" bind:value={destPool} class="input w-full" disabled={busy}>
            {#each options as p (p.name)}<option value={p.name}>{p.name}</option>{/each}
          </select>
        </div>

        <div class="rounded-lg border border-border p-3 space-y-2">
          <div class="flex items-center justify-between gap-2">
            <span class="text-sm font-medium">{t('storage.moveKeepSource')}</span>
            <Switch bind:checked={copy} disabled={busy} ariaLabel={t('storage.moveKeepSource')} />
          </div>
          <p class="text-xs text-muted-foreground">
            {copy ? t('storage.moveKeepSourceOn') : t('storage.moveKeepSourceOff')}
          </p>
        </div>

        {#if !copy}
          <p class="text-xs text-muted-foreground flex items-start gap-1.5">
            <Icon name="alert-triangle" size={13} class="mt-0.5 shrink-0 text-warning" />
            <span>
              {kind === 'iso' ? t('storage.moveIsoInUseHint') : t('storage.moveDiskInUseHint')}
            </span>
          </p>
        {/if}

        {#if progress}
          <div class="space-y-1.5">
            <div class="flex items-center justify-between text-xs">
              <span class="text-muted-foreground">{moveStageLabel(progress.stage)}</span>
              <span class="tnum">{Math.round(progress.pct)}%</span>
            </div>
            <div class="h-1.5 rounded-full bg-muted overflow-hidden">
              <div
                class="h-full bg-accent transition-[width] duration-300"
                style="width: {Math.max(2, Math.min(100, progress.pct))}%"
              ></div>
            </div>
          </div>
        {/if}
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (open = false)} disabled={busy}>
        {t('common.cancel')}
      </Button>
      <Button onclick={doMove} disabled={busy || !destPool}>
        {#if busy}<Spinner size="xs" class="mr-1" />{/if}
        {copy ? t('storage.moveCopyConfirm') : t('vmDetail.moveStorageConfirm')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
