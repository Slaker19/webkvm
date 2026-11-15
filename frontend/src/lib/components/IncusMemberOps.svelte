<script>
  /**
   * IncusMemberOps — Evacuate / Restore buttons for one Incus cluster member.
   * Disabled with a visible reason when the member is offline, already in the
   * target state, or hosts Pacemaker-managed workloads. ADVANCED exposes the
   * evacuation mode and, after a 409 healing_managed_by_pacemaker, a force.
   */
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { memberOpBlock, remoteErrorText } from '$lib/utils/remoteRoutes.js';

  let { member, hostsHa = false, onDone = () => {} } = $props();

  let op = $state(''); // '' | 'evacuate' | 'restore'
  let open = $state(false);
  let busy = $state(false);
  let advanced = $state(false);
  let mode = $state('');
  let error = $state('');
  let haWorkloads = $state([]);
  let blockedByHa = $state(false);

  const BLOCK_KEY = {
    offline: 'blockedOffline',
    alreadyEvacuated: 'blockedAlready',
    notEvacuated: 'blockedNotEvacuated',
    ha: 'blockedHa',
  };
  const evacBlock = $derived(memberOpBlock(member, 'evacuate', hostsHa));
  const restoreBlock = $derived(memberOpBlock(member, 'restore', hostsHa));
  const blockText = (b) => (b ? t(`remote.member.${BLOCK_KEY[b] || 'blockedOffline'}`) : '');

  function ask(which) {
    op = which;
    error = '';
    haWorkloads = [];
    blockedByHa = false;
    advanced = false;
    mode = '';
    open = true;
  }

  async function run(force = false) {
    busy = true;
    error = '';
    try {
      if (op === 'evacuate') {
        await api.evacuateIncusMember(member.name, advanced ? mode : '', force);
        toast.success(t('remote.member.evacuated', { name: member.name }));
      } else {
        await api.restoreIncusMember(member.name, force);
        toast.success(t('remote.member.restored', { name: member.name }));
      }
      open = false;
      onDone();
    } catch (e) {
      error = remoteErrorText(t, e);
      if (e?.code === 'healing_managed_by_pacemaker') {
        blockedByHa = true;
        haWorkloads = e?.data?.workloads || [];
      }
    } finally {
      busy = false;
    }
  }
</script>

{#if auth.isAdmin()}
  <div class="flex flex-col items-start gap-1 sm:items-end" data-testid="member-ops-{member.name}">
    <div
      class="flex gap-1.5"
      role="group"
      aria-label={t('remote.member.actionsAria', { name: member.name })}
    >
      <Button
        size="sm"
        variant="outline"
        disabled={!!evacBlock}
        aria-describedby={evacBlock ? `member-block-${member.name}` : undefined}
        onclick={() => ask('evacuate')}
      >
        {t('remote.member.evacuate')}
      </Button>
      <Button size="sm" variant="outline" disabled={!!restoreBlock} onclick={() => ask('restore')}>
        {t('remote.member.restore')}
      </Button>
    </div>
    {#if member.status === 'Offline' || hostsHa}
      <span id="member-block-{member.name}" class="text-[11px] text-muted-foreground">
        {blockText(member.status === 'Offline' ? 'offline' : 'ha')}
      </span>
    {/if}
  </div>

  <ConfirmDialog
    bind:open
    variant={op === 'evacuate' ? 'destructive' : 'default'}
    title={op === 'evacuate'
      ? t('remote.member.evacuateTitle', { name: member.name })
      : t('remote.member.restoreTitle', { name: member.name })}
    description={op === 'evacuate'
      ? t('remote.member.evacuateDesc', { name: member.name })
      : t('remote.member.restoreDesc', { name: member.name })}
    confirmLabel={op === 'evacuate' ? t('remote.member.evacuate') : t('remote.member.restore')}
    loading={busy}
    onConfirm={() => run(false)}
  >
    <div class="space-y-3 text-sm">
      {#if op === 'evacuate'}
        <label class="flex items-center gap-2 text-xs">
          <input type="checkbox" bind:checked={advanced} data-testid="member-advanced" />
          {t('remote.member.advanced')}
        </label>
        {#if advanced}
          <div class="space-y-1">
            <label for="evac-mode-{member.name}" class="text-xs font-medium"
              >{t('remote.member.mode')}</label
            >
            <select
              id="evac-mode-{member.name}"
              value={mode}
              onchange={(e) => (mode = e.currentTarget.value)}
              class="h-9 w-full rounded-md border border-border bg-background px-2 text-sm"
            >
              <option value="">{t('remote.member.modeDefault')}</option>
              <option value="migrate">{t('remote.member.modeMigrate')}</option>
              <option value="live-migrate">{t('remote.member.modeLiveMigrate')}</option>
              <option value="stop">{t('remote.member.modeStop')}</option>
            </select>
          </div>
        {/if}
      {/if}
      {#if error}
        <p class="text-destructive" role="alert" data-testid="member-error">{error}</p>
      {/if}
      {#if blockedByHa}
        {#if haWorkloads.length}
          <p class="text-xs text-muted-foreground">
            {t('remote.member.haConflict', { workloads: haWorkloads.join(', ') })}
          </p>
        {/if}
        {#if advanced || op === 'restore'}
          <p class="text-xs text-muted-foreground">{t('remote.member.forceHint')}</p>
          <Button size="sm" variant="destructive" disabled={busy} onclick={() => run(true)}>
            {t('remote.member.force')}
          </Button>
        {/if}
      {/if}
    </div>
  </ConfirmDialog>
{/if}
