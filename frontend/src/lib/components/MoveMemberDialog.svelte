<script>
  /**
   * MoveMemberDialog — move an Incus instance to another cluster member.
   * Lists only online members other than the current one. `live` is offered
   * for virtual machines only; a running instance without live is explained
   * instead of being sent to fail with 409.
   */
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import {
    moveTargets,
    moveNeedsStop,
    remoteErrorText,
    vmSelector,
  } from '$lib/utils/remoteRoutes.js';

  let { open = $bindable(false), vm, members = [], onMoved = () => {} } = $props();

  let target = $state('');
  let live = $state(false);
  let busy = $state(false);
  let error = $state('');

  const targets = $derived(moveTargets(members, vm));
  const isVm = $derived(vm?.type === 'vm');
  const mustStop = $derived(moveNeedsStop(vm, isVm && live));
  const canSubmit = $derived(!!target && !mustStop && !busy);

  $effect(() => {
    if (open) {
      if (!targets.some((m) => m.name === target)) target = targets[0]?.name || '';
      error = '';
    }
  });

  async function submit() {
    if (!canSubmit) return;
    busy = true;
    error = '';
    try {
      const { node, hv } = vmSelector(vm);
      await api.moveVM(vm.id, target, isVm && live, node, hv || 'incus');
      toast.success(t('remote.move.success', { name: vm.alias || vm.name, member: target }));
      open = false;
      onMoved(target);
    } catch (e) {
      error = remoteErrorText(t, e);
    } finally {
      busy = false;
    }
  }
</script>

<ConfirmDialog
  bind:open
  title={t('remote.move.title')}
  description={t('remote.move.desc', { name: vm?.alias || vm?.name || '' })}
  confirmLabel={t('remote.move.submit')}
  loading={busy}
  onConfirm={submit}
>
  <div class="space-y-3 text-sm" data-testid="move-dialog">
    {#if targets.length === 0}
      <p class="text-muted-foreground" data-testid="move-no-targets">
        {t('remote.move.noTargets')}
      </p>
    {:else}
      <div class="space-y-1">
        <label for="move-member" class="text-xs font-medium text-foreground"
          >{t('remote.move.member')}</label
        >
        <select
          id="move-member"
          bind:value={target}
          class="h-9 w-full rounded-md border border-border bg-background px-2 text-sm"
        >
          {#each targets as m (m.name)}
            <option value={m.name}>{m.name}</option>
          {/each}
        </select>
      </div>
      {#if isVm}
        <label class="flex items-start gap-2">
          <input type="checkbox" bind:checked={live} class="mt-0.5" id="move-live" />
          <span>
            <span class="font-medium text-foreground">{t('remote.move.live')}</span>
            <span class="block text-xs text-muted-foreground">{t('remote.move.liveHint')}</span>
          </span>
        </label>
      {/if}
      {#if mustStop}
        <p class="text-warning" role="status" data-testid="move-must-stop">
          {isVm ? t('remote.move.mustStopLive') : t('remote.move.mustStop')}
        </p>
      {/if}
    {/if}
    {#if error}
      <p class="text-destructive" role="alert" data-testid="move-error">{error}</p>
    {/if}
  </div>
</ConfirmDialog>
