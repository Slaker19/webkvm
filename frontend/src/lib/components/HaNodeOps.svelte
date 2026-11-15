<script>
  /**
   * HaNodeOps — Pacemaker node maintenance (Standby / Resume) and the
   * "Test fencing" card. Both are admin-only mutations that the backend
   * re-validates; the UI only prevents the obvious mistakes.
   */
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import HaFencingSimulate from '$lib/components/HaFencingSimulate.svelte';
  import CopyCommands from '$lib/components/CopyCommands.svelte';
  import { codeText } from '$lib/utils/nodes.js';

  let { readiness, advanced = false, onChanged = () => {} } = $props();

  const nodeStates = $derived(readiness?.pacemaker?.node_states || []);
  const nodeNames = $derived(
    nodeStates.length ? nodeStates.map((n) => n.name) : readiness?.pacemaker?.node_names || []
  );
  const stateOf = (name) => nodeStates.find((n) => n.name === name);
  // Only online nodes can be fenced; without node_states keep every name
  // and let the backend answer node_offline.
  const fenceCandidates = $derived(
    nodeStates.length ? nodeStates.filter((n) => n.online).map((n) => n.name) : nodeNames
  );
  // Best effort: the node that hosts the control-plane resource. The
  // backend enforces target_is_control_plane regardless of this guess.
  const controlPlaneNode = $derived(
    readiness?.control_plane?.node ||
      readiness?.pacemaker?.resources?.find((r) => /webkvm|control/i.test(r.id) && r.node)?.node ||
      ''
  );
  const blockers = $derived(readiness?.blockers || []);
  const testedAt = $derived(readiness?.fencing?.tested_at || null);
  // watchdog_only | device | none. An older backend sends no mode: keep the
  // direct fence test it always had.
  const mode = $derived(readiness?.fencing?.mode || 'device');
  const modeName = $derived(
    mode === 'watchdog_only'
      ? t('nodes.haops.modeWatchdogName')
      : mode === 'none'
        ? t('nodes.haops.modeNoneName')
        : t('nodes.haops.modeDeviceName')
  );

  // --- standby / resume ---
  let pending = $state(null); // { node, action: 'standby' | 'unstandby' }
  let busy = $state(false);
  let opError = $state('');
  let states = $state({});

  const opErrorText = (e) => {
    const code = e?.code;
    const key = `nodes.haops.opErrors.${code}`;
    const out = code ? t(key) : key;
    return out === key ? e?.message || String(e) : out;
  };

  async function confirmOp() {
    if (!pending || busy) return;
    busy = true;
    opError = '';
    const { node, action } = pending;
    try {
      const res =
        action === 'standby' ? await api.haNodeStandby(node) : await api.haNodeUnstandby(node);
      states = { ...states, [node]: res?.state || (action === 'standby' ? 'standby' : 'online') };
      toast.success(
        action === 'standby'
          ? t('nodes.haops.standbyDone', { node })
          : t('nodes.haops.resumeDone', { node })
      );
      pending = null;
      onChanged();
    } catch (e) {
      opError = opErrorText(e);
      pending = null;
    } finally {
      busy = false;
    }
  }

  // --- fencing test ---
  let target = $state('');
  let allowCp = $state(false);
  let typed = $state('');
  let testing = $state(false);
  let fenceResult = $state(null);
  let fenceErrors = $state([]);
  let fenceUnreliable = $state(null); // 409 watchdog_only_unreliable body

  const selectable = $derived(
    fenceCandidates.filter((n) => allowCp || !controlPlaneNode || n !== controlPlaneNode)
  );
  const effectiveTarget = $derived(selectable.includes(target) ? target : '');
  const expected = $derived(effectiveTarget ? `FENCE ${effectiveTarget}` : '');
  const confirmed = $derived(!!expected && typed === expected);
  const disabledReason = $derived(
    blockers.length
      ? t('nodes.haops.fenceBlocked')
      : nodeNames.length === 0
        ? t('nodes.haops.noNodes')
        : ''
  );
  const fenceErrText = (c) =>
    codeText(t, 'nodes.haops.fenceErrors', c, 'nodes.haops.fenceErrorUnknown');

  async function runFenceTest() {
    if (!confirmed || testing || disabledReason) return;
    testing = true;
    fenceErrors = [];
    fenceUnreliable = null;
    fenceResult = null;
    try {
      const body = { node: effectiveTarget, confirm: expected };
      if (advanced && allowCp) body.allow_control_plane = true;
      const res = await api.haFencingTest(body);
      typed = '';
      if (res?.ok === false) fenceErrors = ['__run_failed'];
      else fenceResult = res;
      onChanged();
    } catch (e) {
      const list =
        Array.isArray(e?.data?.blockers) && e.data.blockers.length
          ? e.data.blockers
          : e?.code && e.code !== 'request_failed'
            ? [e.code]
            : [];
      fenceErrors = list.length ? list : [`__raw:${e?.message || e}`];
      if (list.includes('watchdog_only_unreliable')) fenceUnreliable = e?.data || {};
    } finally {
      testing = false;
    }
  }

  const fmt = (iso) => {
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? String(iso) : d.toLocaleString();
  };
</script>

{#if nodeNames.length}
  <div class="space-y-2 border-t border-border pt-3" data-testid="ha-node-ops">
    <h3 class="text-xs font-semibold text-foreground">{t('nodes.haops.nodesTitle')}</h3>
    <p class="text-xs text-muted-foreground">{t('nodes.haops.nodesHint')}</p>
    {#if opError}<Alert variant="error">{opError}</Alert>{/if}
    <ul class="space-y-1 text-xs">
      {#each nodeNames as name (name)}
        <li class="flex flex-wrap items-center justify-between gap-2">
          <span class="font-medium text-foreground">
            {name}
            {#if states[name] && !stateOf(name)}
              <span class="font-normal text-muted-foreground">· {states[name]}</span>
            {/if}
          </span>
          <span class="flex items-center gap-2">
            {#if stateOf(name) && !stateOf(name).online}
              <span class="text-muted-foreground">{t('nodes.haops.offline')}</span>
            {/if}
            {#if !stateOf(name) || stateOf(name).standby || states[name] === 'standby'}
              <Button
                size="sm"
                variant="outline"
                class="min-h-11"
                aria-label={t('nodes.haops.resumeAria', { node: name })}
                onclick={() => (pending = { node: name, action: 'unstandby' })}
                >{t('nodes.haops.resume')}</Button
              >
            {/if}
            {#if !stateOf(name) || (!stateOf(name).standby && states[name] !== 'standby')}
              <Button
                size="sm"
                variant="outline"
                class="min-h-11"
                aria-label={t('nodes.haops.standbyAria', { node: name })}
                onclick={() => (pending = { node: name, action: 'standby' })}
                >{t('nodes.haops.standby')}</Button
              >
            {/if}
          </span>
        </li>
      {/each}
    </ul>
  </div>

  <div class="space-y-2 border-t border-border pt-3" data-testid="fencing-test">
    <h3 class="text-xs font-semibold text-foreground">{t('nodes.haops.fenceTitle')}</h3>
    <div class="space-y-1 text-xs text-muted-foreground" data-testid="fence-modes">
      <p>{t('nodes.haops.modesIntro')}</p>
      <ul class="list-disc space-y-0.5 pl-5">
        <li class={mode === 'device' ? 'font-medium text-foreground' : ''}>
          {t('nodes.haops.modeDeviceText')}
        </li>
        <li class={mode === 'watchdog_only' ? 'font-medium text-foreground' : ''}>
          {t('nodes.haops.modeWatchdogText')}
        </li>
      </ul>
      <p class="text-foreground" data-testid="fence-mode-current">
        {t('nodes.haops.modeCurrent', { mode: modeName })}
      </p>
    </div>
    <p class="text-xs text-muted-foreground" data-testid="fence-last">
      {testedAt ? t('nodes.haops.fenceLast', { when: fmt(testedAt) }) : t('nodes.haops.fenceNever')}
    </p>
    {#if disabledReason}
      <Alert variant="warning">{disabledReason}</Alert>
    {/if}
    {#if mode === 'watchdog_only'}
      <HaFencingSimulate {readiness} candidates={fenceCandidates} {controlPlaneNode} {onChanged} />
    {:else if mode === 'none'}
      <Alert variant="warning">{t('nodes.haops.modeNoneText')}</Alert>
    {:else}
      <p class="text-xs text-muted-foreground">{t('nodes.haops.fenceExplain')}</p>
      <div class="grid gap-2 sm:grid-cols-2">
        <div class="space-y-1">
          <label class="text-xs font-medium" for="fence-node">{t('nodes.haops.fenceNode')}</label>
          <select
            id="fence-node"
            class="h-9 w-full rounded-lg border border-input bg-background px-2 text-sm"
            value={target}
            onchange={(e) => (target = e.currentTarget.value)}
            disabled={!!disabledReason}
          >
            <option value="">{t('nodes.haops.fenceChoose')}</option>
            {#each selectable as n (n)}<option value={n}>{n}</option>{/each}
          </select>
        </div>
        <div class="space-y-1">
          <label class="text-xs font-medium" for="fence-confirm"
            >{t('nodes.haops.fenceType', { text: expected || 'FENCE <node>' })}</label
          >
          <Input
            id="fence-confirm"
            bind:value={typed}
            autocomplete="off"
            disabled={!!disabledReason || !effectiveTarget}
          />
        </div>
      </div>
      {#if advanced}
        <label class="flex items-start gap-2 text-sm">
          <input
            type="checkbox"
            bind:checked={allowCp}
            class="mt-0.5 h-4 w-4 rounded border-border"
          />
          <span>{t('nodes.haops.allowControlPlane')}</span>
        </label>
      {/if}
      {#if fenceErrors.length}
        <Alert variant="error" title={t('nodes.haops.fenceFailed')}>
          <ul class="list-disc pl-5">
            {#each fenceErrors as c (c)}
              <li>
                {c === '__run_failed'
                  ? t('nodes.haops.fenceFailedRun')
                  : c.startsWith('__raw:')
                    ? c.slice(6)
                    : fenceErrText(c)}
              </li>
            {/each}
          </ul>
          {#if fenceUnreliable}
            <p class="mt-1" data-testid="fence-unreliable">
              {codeText(
                t,
                'nodes.haops.fenceExplanations',
                fenceUnreliable.explanation,
                'nodes.haops.fenceErrorUnknown'
              )}
            </p>
            {#if fenceUnreliable.simulate?.command}
              <CopyCommands
                commands={[fenceUnreliable.simulate.command]}
                title={t('nodes.haops.simCommandLabel', { node: fenceUnreliable.node || '' })}
              />
            {/if}
          {/if}
        </Alert>
      {/if}
      {#if fenceResult}
        <Alert variant="success">
          {t('nodes.haops.fenceOk', {
            node: fenceResult.node,
            when: fenceResult.fenced_at ? fmt(fenceResult.fenced_at) : '',
          })}
        </Alert>
      {/if}
      <Button
        variant="destructive"
        class="min-h-11"
        disabled={!confirmed || testing || !!disabledReason}
        onclick={runFenceTest}
      >
        {#if testing}<Spinner size="sm" />{/if}
        {t('nodes.haops.fenceRun')}
      </Button>
    {/if}
  </div>
{/if}

<ConfirmDialog
  open={!!pending}
  title={pending?.action === 'standby'
    ? t('nodes.haops.standbyTitle', { node: pending?.node || '' })
    : t('nodes.haops.resumeTitle', { node: pending?.node || '' })}
  description={pending?.action === 'standby'
    ? t('nodes.haops.standbyBody')
    : t('nodes.haops.resumeBody')}
  confirmLabel={pending?.action === 'standby' ? t('nodes.haops.standby') : t('nodes.haops.resume')}
  loading={busy}
  onConfirm={confirmOp}
  onCancel={() => (pending = null)}
/>
