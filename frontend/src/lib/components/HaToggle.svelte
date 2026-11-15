<script>
  /**
   * HaToggle — one switch to put a VM / Incus instance under Pacemaker HA.
   * Turning it on opens a dialog that first checks eligibility and says
   * exactly what to fix; turning it off goes through HaDisableDialog.
   * Renders nothing unless the Cluster/HA module is enabled and the user is
   * an admin (the HA API is admin only).
   */
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import Switch from '$lib/components/Switch.svelte';
  import CopyCommands from '$lib/components/CopyCommands.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { Button } from '$lib/components/ui/button';
  import HaDisableDialog from '$lib/components/HaDisableDialog.svelte';
  import { clusterModule } from '$lib/stores/clusterModule.svelte.js';
  import { haWorkloads, refreshHaWorkloads } from '$lib/stores/haWorkloads.svelte.js';
  import {
    workloadRef,
    isHaManaged,
    reasonText,
    reasonFix,
    blockerText,
    haErrorText,
    preconditionDetails,
  } from '$lib/utils/haWorkloads.js';

  let { vm, advanced = false, compact = false, onChanged = () => {} } = $props();

  const ref = $derived(workloadRef(vm));
  const label = $derived(vm?.alias || vm?.name || '');
  const managed = $derived(isHaManaged(vm, haWorkloads.workloads));
  // HA API facts are local to this Pacemaker/Incus control plane. A registry
  // node_id means the row belongs to a separate remote endpoint.
  const visible = $derived(clusterModule.enabled && auth.isAdmin() && !!ref && !vm?.node_id);

  let showAdv = $state(false);
  const adv = $derived(advanced || showAdv);
  let enableOpen = $state(false);
  let disableRef = $state(null);
  let checking = $state(false);
  let busy = $state(false);
  let eligibility = $state(null);
  let blockers = $state([]);
  let reasons = $state([]);
  let error = $state('');
  let commands = $state(null);
  let allowed = $state('');
  let preferred = $state('');
  let stickiness = $state('');
  let threshold = $state('');

  function onToggle(next) {
    if (next) openEnable();
    else disableRef = { ...ref, label };
  }

  async function openEnable() {
    enableOpen = true;
    checking = true;
    eligibility = null;
    blockers = [];
    reasons = [];
    error = '';
    commands = null;
    try {
      const [w] = await Promise.all([
        api.getHAWorkload(ref.kind, ref.name, ref.project),
        refreshHaWorkloads(),
      ]);
      eligibility = w?.eligibility || { eligible: true, reasons: [] };
      reasons = eligibility.reasons || [];
      const pre = haWorkloads.preconditions;
      blockers =
        pre && pre.ok === false ? (pre.blockers || []).filter((b) => b !== 'not_eligible') : [];
    } catch (e) {
      error = haErrorText(t, e);
    } finally {
      checking = false;
    }
  }

  const blocked = $derived(
    !!error || (eligibility && !eligibility.eligible) || blockers.length > 0
  );
  // Known blockers (not a transient error): Turn on HA stays disabled and
  // says why, instead of a click that silently does nothing.
  const hardBlocked = $derived(
    !checking && ((eligibility && !eligibility.eligible) || blockers.length > 0)
  );

  function buildBody(dryRun) {
    const body = {};
    if (dryRun) body.dry_run = true;
    const nodes = allowed
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    if (nodes.length) body.allowed_nodes = nodes;
    if (preferred.trim()) body.preferred_node = preferred.trim();
    if (String(stickiness).trim() !== '') body.stickiness = Number(stickiness);
    if (String(threshold).trim() !== '') body.migration_threshold = Number(threshold);
    return body;
  }

  const optionsValid = $derived(
    [stickiness, threshold].every(
      (v) => String(v).trim() === '' || /^-?\d+$/.test(String(v).trim())
    )
  );

  async function submit(dryRun = false) {
    if (busy || checking) return;
    if (!optionsValid) {
      error = t('haWorkloads.optionsInvalid');
      return;
    }
    if (!dryRun && blocked) return;
    busy = true;
    error = '';
    try {
      const res = await api.enableHAWorkload(ref.kind, ref.name, ref.project, buildBody(dryRun));
      if (dryRun) {
        commands = res?.commands || [];
        return;
      }
      toast.success(
        res?.changed === false
          ? t('haWorkloads.enableNoChange', { name: label })
          : t('haWorkloads.enabled', { name: label })
      );
      enableOpen = false;
      refreshHaWorkloads();
      onChanged();
    } catch (e) {
      error = haErrorText(t, e);
      if (e?.code === 'precondition_failed') {
        const d = preconditionDetails(e);
        blockers = d.blockers.filter((b) => b !== 'not_eligible');
        reasons = d.reasons;
        eligibility = { eligible: d.reasons.length === 0, reasons: d.reasons };
      }
    } finally {
      busy = false;
    }
  }
</script>

{#if visible}
  <div class="inline-flex items-center gap-2" data-testid="ha-toggle">
    <Switch
      size="sm"
      checked={managed}
      label={compact ? '' : t('haWorkloads.toggleLabel')}
      ariaLabel={t('haWorkloads.toggleAria', { name: label })}
      onchange={onToggle}
    />
  </div>

  <ConfirmDialog
    open={enableOpen}
    title={t('haWorkloads.enableTitle', { name: label })}
    description={t('haWorkloads.enableBody')}
    confirmLabel={t('haWorkloads.enableConfirm')}
    loading={busy || checking}
    cancelDisabled={busy}
    confirmDisabled={hardBlocked}
    confirmDisabledReason={hardBlocked ? t('haWorkloads.enableBlockedReason') : ''}
    onConfirm={() => submit(false)}
    onCancel={() => (enableOpen = false)}
  >
    {#if checking}
      <p class="text-xs text-muted-foreground" role="status">
        {t('haWorkloads.checkingEligibility')}
      </p>
    {:else}
      {#if error}<Alert variant="error">{error}</Alert>{/if}
      {#if blockers.length}
        <Alert variant="warning" title={t('haWorkloads.blockersTitle')}>
          <ul class="list-disc pl-5 space-y-0.5">
            {#each blockers as b (b)}<li>{blockerText(t, b)}</li>{/each}
          </ul>
        </Alert>
      {/if}
      {#if reasons.length}
        <Alert variant="warning" title={t('haWorkloads.reasonsTitle')}>
          <ul class="space-y-2">
            {#each reasons as r, i (`${r.code}:${i}`)}
              <li>
                <div>{reasonText(t, r.code)}</div>
                {#if reasonFix(t, r.code) || r.fix}
                  <div class="text-xs">
                    <span class="font-semibold">{t('haWorkloads.howToFix')}:</span>
                    {reasonFix(t, r.code) || r.fix}
                  </div>
                {/if}
                {#if r.detail && r.code === 'nic_network_not_on_all_members'}
                  <div class="text-xs" data-testid="reason-detail">{r.detail}</div>
                {/if}
                {#if r.commands?.length}
                  <CopyCommands commands={r.commands} title={t('haWorkloads.fixCommandsTitle')} />
                {/if}
              </li>
            {/each}
          </ul>
        </Alert>
      {:else if eligibility?.eligible && !error}
        <p class="text-xs text-success">{t('haWorkloads.eligibleOk')}</p>
      {/if}
      {#if !advanced}
        <Button
          variant="ghost"
          size="sm"
          class="min-h-11"
          aria-expanded={showAdv}
          onclick={() => (showAdv = !showAdv)}
        >
          {t('haWorkloads.advancedOptions')}
        </Button>
      {/if}
      {#if adv}
        <div class="space-y-2 border-t border-border pt-2">
          <h4 class="text-xs font-semibold">{t('haWorkloads.advancedOptions')}</h4>
          <div class="grid gap-2 sm:grid-cols-2">
            <label class="space-y-1 text-xs sm:col-span-2">
              <span>{t('haWorkloads.allowedNodes')}</span>
              <input class="input w-full min-h-11" bind:value={allowed} />
              <span class="text-muted-foreground">{t('haWorkloads.allowedNodesHint')}</span>
            </label>
            <label class="space-y-1 text-xs">
              <span>{t('haWorkloads.preferredNode')}</span>
              <input class="input w-full min-h-11" bind:value={preferred} />
            </label>
            <label class="space-y-1 text-xs">
              <span>{t('haWorkloads.stickiness')}</span>
              <input class="input w-full min-h-11" inputmode="numeric" bind:value={stickiness} />
            </label>
            <label class="space-y-1 text-xs">
              <span>{t('haWorkloads.migrationThreshold')}</span>
              <input class="input w-full min-h-11" inputmode="numeric" bind:value={threshold} />
            </label>
          </div>
          <Button variant="outline" class="min-h-11" disabled={busy} onclick={() => submit(true)}>
            {t('haWorkloads.dryRun')}
          </Button>
          {#if commands}
            <div class="space-y-1">
              <div class="text-xs font-semibold">{t('haWorkloads.dryRunTitle')}</div>
              {#if commands.length}
                <pre
                  class="rounded-md border border-border bg-muted p-2 text-xs font-mono whitespace-pre-wrap break-all">{commands.join(
                    '\n'
                  )}</pre>
              {:else}
                <p class="text-xs text-muted-foreground">{t('haWorkloads.dryRunNone')}</p>
              {/if}
            </div>
          {/if}
        </div>
      {/if}
    {/if}
  </ConfirmDialog>

  <HaDisableDialog
    ref={disableRef}
    onDone={() => {
      disableRef = null;
      onChanged();
    }}
    onCancel={() => (disableRef = null)}
  />
{/if}
