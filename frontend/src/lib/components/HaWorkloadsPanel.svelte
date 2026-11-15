<script>
  /**
   * HaWorkloadsPanel — the VMs/containers Pacemaker manages: health, where
   * they run, and the safe operations (move, disable, clear failures,
   * maintenance, bulk). Mounted only while the Cluster/HA module is enabled.
   */
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Card } from '$lib/components/ui/card';
  import { Badge } from '$lib/components/ui/badge';
  import CopyCommands from '$lib/components/CopyCommands.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import HaDisableDialog from '$lib/components/HaDisableDialog.svelte';
  import HaReleasedList from '$lib/components/HaReleasedList.svelte';
  import {
    haWorkloads,
    refreshHaWorkloads,
    watchHaWorkloads,
  } from '$lib/stores/haWorkloads.svelte.js';
  import {
    haErrorText,
    blockerText,
    reasonText,
    reasonFix,
    workloadHealth,
    moveTargets,
    isMaintenance,
    maintenanceLeft,
  } from '$lib/utils/haWorkloads.js';

  let { nodeNames = [], advanced = false } = $props();

  const MAX_BULK = 50;

  onMount(() => watchHaWorkloads());

  const list = $derived(haWorkloads.workloads);
  const preBlockers = $derived(
    haWorkloads.preconditions && haWorkloads.preconditions.ok === false
      ? haWorkloads.preconditions.blockers || []
      : []
  );

  const refOf = (w) => ({
    kind: w.kind,
    name: w.name,
    project: w.project || '',
    key: w.key,
    label: w.name,
  });
  const healthVariant = { ok: 'success', warn: 'warning', bad: 'destructive', muted: 'secondary' };
  const healthKey = { ok: 'healthOk', warn: 'healthWarn', bad: 'healthBad', muted: 'healthMuted' };

  function stateText(w) {
    const s = isMaintenance(w) ? 'maintenance' : w.state;
    const key = `haWorkloads.states.${s}`;
    const out = t(key);
    return out === key ? t('haWorkloads.unknownState', { code: s }) : out;
  }
  function fmtTime(iso) {
    if (!iso) return '—';
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
  }

  // --- selection / bulk ---
  let selected = $state({});
  const selectedKeys = $derived(list.filter((w) => selected[w.key]).map((w) => w.key));
  const tooMany = $derived(selectedKeys.length > MAX_BULK);
  let bulkAction = $state(null); // 'enable' | 'disable'
  let bulkMode = $state('stop');
  let bulkBusy = $state(false);
  let bulkError = $state('');
  let bulkResults = $state(null);

  const allSelected = $derived(list.length > 0 && selectedKeys.length === list.length);
  function toggleAll(on) {
    selected = on ? Object.fromEntries(list.map((w) => [w.key, true])) : {};
  }

  async function runBulk() {
    if (bulkBusy || tooMany) return;
    bulkBusy = true;
    bulkError = '';
    try {
      const items = list
        .filter((w) => selected[w.key])
        .map((w) => ({ kind: w.kind, name: w.name, project: w.project || '' }));
      const body = { action: bulkAction, items };
      if (bulkAction === 'disable') body.mode = bulkMode;
      const res = await api.bulkHAWorkloads(body);
      bulkResults = res?.results || [];
      refreshHaWorkloads();
    } catch (e) {
      bulkError = haErrorText(t, e);
    } finally {
      bulkBusy = false;
    }
  }
  function closeBulk() {
    if (bulkResults) selected = {};
    bulkAction = null;
    bulkResults = null;
    bulkError = '';
  }
  function bulkItemText(r) {
    if (r.ok) return r.changed ? t('haWorkloads.bulkOk') : t('haWorkloads.bulkNoChange');
    return haErrorText(t, { code: r.code, message: r.error });
  }

  // --- move ---
  let moveFor = $state(null);
  let moveNode = $state('');
  let moveBusy = $state(false);
  let moveError = $state('');
  const targets = $derived(moveFor ? moveTargets(moveFor, nodeNames) : []);
  function openMove(w) {
    moveFor = w;
    moveError = '';
    moveNode = moveTargets(w, nodeNames)[0] || '';
  }
  async function doMove() {
    if (!moveFor || !moveNode || moveBusy) return;
    moveBusy = true;
    moveError = '';
    try {
      await api.moveHAWorkload(moveFor.kind, moveFor.name, moveFor.project || '', moveNode);
      toast.success(t('haWorkloads.moved', { name: moveFor.name, node: moveNode }));
      moveFor = null;
      refreshHaWorkloads();
    } catch (e) {
      moveError = haErrorText(t, e);
    } finally {
      moveBusy = false;
    }
  }

  // --- disable ---
  let disableRef = $state(null);

  // --- failcount ---
  let clearing = $state({});
  async function clearFail(w) {
    clearing = { ...clearing, [w.key]: true };
    try {
      await api.clearHAWorkloadFailcount(w.kind, w.name, w.project || '');
      toast.success(t('haWorkloads.failcountCleared', { name: w.name }));
      refreshHaWorkloads();
    } catch (e) {
      toast.error(haErrorText(t, e));
    } finally {
      clearing = { ...clearing, [w.key]: false };
    }
  }

  // --- maintenance ---
  let maintFor = $state(null);
  let maintTtl = $state(3600);
  let maintBusy = $state(false);
  let maintError = $state('');
  const maintEnding = $derived(!!maintFor && isMaintenance(maintFor));
  function openMaint(w) {
    maintFor = w;
    maintError = '';
    maintTtl = 3600;
  }
  async function doMaint() {
    if (!maintFor || maintBusy) return;
    maintBusy = true;
    maintError = '';
    const ending = maintEnding;
    try {
      await api.maintenanceHAWorkload(
        maintFor.kind,
        maintFor.name,
        maintFor.project || '',
        ending ? 'end' : 'begin',
        ending ? undefined : Number(maintTtl)
      );
      toast.success(
        t(ending ? 'haWorkloads.maintEnded' : 'haWorkloads.maintStarted', { name: maintFor.name })
      );
      maintFor = null;
      refreshHaWorkloads();
    } catch (e) {
      maintError = haErrorText(t, e);
    } finally {
      maintBusy = false;
    }
  }
</script>

<Card class="p-4 space-y-3 border border-border bg-card/60" data-testid="ha-workloads-panel">
  <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
    <div class="space-y-1">
      <h2 class="text-sm font-semibold text-foreground">{t('haWorkloads.title')}</h2>
      <p class="text-xs text-muted-foreground">{t('haWorkloads.description')}</p>
      {#if haWorkloads.summary}
        <p class="text-xs text-muted-foreground">
          {t('haWorkloads.summary', {
            managed: haWorkloads.summary.managed,
            failed: haWorkloads.summary.failed,
          })}
        </p>
      {/if}
    </div>
    <Button
      size="sm"
      variant="outline"
      class="min-h-11 shrink-0"
      disabled={haWorkloads.loading}
      onclick={() => refreshHaWorkloads()}
    >
      {#if haWorkloads.loading}<Spinner size="sm" />{/if}
      {t('haWorkloads.refresh')}
    </Button>
  </div>

  {#if haWorkloads.error}
    <Alert variant="error">{t('haWorkloads.loadError', { error: haWorkloads.error })}</Alert>
  {/if}
  {#if preBlockers.length}
    <Alert variant="warning" title={t('haWorkloads.notReady')}>
      <ul class="list-disc pl-5 space-y-0.5">
        {#each preBlockers as b (b)}<li>{blockerText(t, b)}</li>{/each}
      </ul>
    </Alert>
  {/if}

  {#if !haWorkloads.loaded && !haWorkloads.error}
    <p class="text-xs text-muted-foreground" role="status">{t('haWorkloads.loading')}</p>
  {:else if list.length === 0 && haWorkloads.loaded}
    <p class="text-xs text-muted-foreground">{t('haWorkloads.empty')}</p>
  {:else if list.length}
    {#if selectedKeys.length}
      <div
        class="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted p-2"
        role="toolbar"
        aria-label={t('haWorkloads.selectedCount', { count: selectedKeys.length })}
        data-testid="ha-bulk-bar"
      >
        <span class="text-sm font-medium"
          >{t('haWorkloads.selectedCount', { count: selectedKeys.length })}</span
        >
        <Button
          size="sm"
          class="min-h-11"
          disabled={tooMany}
          onclick={() => (bulkAction = 'enable')}
        >
          {t('haWorkloads.bulkEnable')}
        </Button>
        <Button
          size="sm"
          variant="outline"
          class="min-h-11"
          disabled={tooMany}
          onclick={() => (bulkAction = 'disable')}
        >
          {t('haWorkloads.bulkDisable')}
        </Button>
        <Button size="sm" variant="ghost" class="min-h-11" onclick={() => toggleAll(false)}>
          {t('haWorkloads.clearSelection')}
        </Button>
        {#if tooMany}<span class="text-xs text-destructive">{t('haWorkloads.bulkMax')}</span>{/if}
      </div>
    {/if}
    <div class="overflow-x-auto">
      <table class="w-full text-xs">
        <thead>
          <tr class="text-left text-muted-foreground">
            <th class="p-2 w-10">
              <input
                type="checkbox"
                class="h-4 w-4"
                aria-label={t('haWorkloads.selectAll')}
                checked={allSelected}
                onchange={(e) => toggleAll(e.currentTarget.checked)}
              />
            </th>
            <th class="p-2">{t('haWorkloads.colHealth')}</th>
            <th class="p-2">{t('haWorkloads.colWorkload')}</th>
            <th class="p-2">{t('haWorkloads.colState')}</th>
            <th class="p-2">{t('haWorkloads.colNode')}</th>
            <th class="p-2">{t('haWorkloads.colSince')}</th>
            <th class="p-2">{t('haWorkloads.colFailcount')}</th>
            <th class="p-2">{t('haWorkloads.colEligibility')}</th>
            <!-- Pinned right: on a phone the table scrolls inside the card and
                 the actions must stay reachable. -->
            <th
              class="p-2 sticky right-0 z-10 bg-card shadow-[-6px_0_6px_-6px_rgba(0,0,0,0.3)]"
              data-testid="ha-actions-head">{t('haWorkloads.colActions')}</th
            >
          </tr>
        </thead>
        <tbody>
          {#each list as w (w.key)}
            {@const h = workloadHealth(w)}
            <tr class="border-t border-border align-top" data-testid="ha-row-{w.key}">
              <td class="p-2">
                <input
                  type="checkbox"
                  class="h-4 w-4"
                  aria-label={t('haWorkloads.selectRow', { name: w.name })}
                  checked={!!selected[w.key]}
                  onchange={(e) => (selected = { ...selected, [w.key]: e.currentTarget.checked })}
                />
              </td>
              <td class="p-2"
                ><Badge variant={healthVariant[h]}>{t(`haWorkloads.${healthKey[h]}`)}</Badge></td
              >
              <td class="p-2">
                <div class="font-medium text-foreground">{w.name}</div>
                <div class="flex flex-wrap gap-1 mt-0.5">
                  <Badge variant="outline"
                    >{w.kind === 'incus'
                      ? t('haWorkloads.kindIncus')
                      : t('haWorkloads.kindKvm')}</Badge
                  >
                  <Badge variant="info" title={t('haWorkloads.managedByTitle')}
                    >{t('haWorkloads.managedBy')}</Badge
                  >
                </div>
                {#if w.project && w.project !== 'default'}
                  <div class="text-muted-foreground font-mono">{w.project}</div>
                {/if}
                {#if advanced}
                  <div class="text-muted-foreground font-mono break-all">{w.key}</div>
                {/if}
              </td>
              <td class="p-2">
                {stateText(w)}
                {#if isMaintenance(w)}
                  {@const left = maintenanceLeft(w)}
                  <div
                    class={left !== null && left < 0 ? 'text-destructive' : 'text-muted-foreground'}
                  >
                    {left !== null && left < 0
                      ? t('haWorkloads.maintExpired')
                      : t('haWorkloads.maintUntil', { time: fmtTime(w.maint_until) })}
                  </div>
                {/if}
              </td>
              <td class="p-2 font-mono">{w.node || '—'}</td>
              <td class="p-2">{fmtTime(w.since)}</td>
              <td class="p-2 font-mono">{w.failcount}</td>
              <td class="p-2">
                {#if w.eligibility?.eligible}
                  <span class="text-success">{t('haWorkloads.eligible')}</span>
                {:else}
                  <details>
                    <summary class="cursor-pointer text-warning"
                      >{t('haWorkloads.notEligible')} · {t('haWorkloads.showReasons')}</summary
                    >
                    <ul class="mt-1 space-y-1">
                      {#each w.eligibility?.reasons || [] as r, i (`${r.code}:${i}`)}
                        <li>
                          {reasonText(t, r.code)}
                          {#if reasonFix(t, r.code)}<div class="text-muted-foreground">
                              {reasonFix(t, r.code)}
                            </div>{/if}
                          {#if r.detail && r.code === 'nic_network_not_on_all_members'}
                            <div class="text-muted-foreground">{r.detail}</div>
                          {/if}
                          {#if r.commands?.length}
                            <CopyCommands
                              commands={r.commands}
                              title={t('haWorkloads.fixCommandsTitle')}
                            />
                          {/if}
                        </li>
                      {/each}
                    </ul>
                  </details>
                {/if}
              </td>
              <td
                class="p-2 sticky right-0 z-10 bg-card shadow-[-6px_0_6px_-6px_rgba(0,0,0,0.3)]"
                data-testid="ha-actions-cell"
              >
                <div class="flex flex-wrap gap-1">
                  <Button
                    size="sm"
                    variant="outline"
                    class="min-h-11"
                    disabled={isMaintenance(w)}
                    onclick={() => openMove(w)}
                  >
                    {t('haWorkloads.move')}
                  </Button>
                  <Button size="sm" variant="outline" class="min-h-11" onclick={() => openMaint(w)}>
                    {isMaintenance(w) ? t('haWorkloads.maintEnd') : t('haWorkloads.maintBegin')}
                  </Button>
                  {#if w.failcount > 0 || w.state === 'failed'}
                    <Button
                      size="sm"
                      variant="outline"
                      class="min-h-11"
                      title={t('haWorkloads.clearFailcountDesc')}
                      disabled={clearing[w.key]}
                      onclick={() => clearFail(w)}>{t('haWorkloads.clearFailcount')}</Button
                    >
                  {/if}
                  <Button
                    size="sm"
                    variant="destructive"
                    class="min-h-11"
                    onclick={() => (disableRef = refOf(w))}
                  >
                    {t('haWorkloads.disableHa')}
                  </Button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Card>

<HaReleasedList onChanged={() => refreshHaWorkloads()} />

<!-- Move -->
<ConfirmDialog
  open={!!moveFor}
  title={t('haWorkloads.moveTitle', { name: moveFor?.name || '' })}
  description={t('haWorkloads.moveBody')}
  confirmLabel={t('haWorkloads.moveConfirm')}
  loading={moveBusy}
  onConfirm={doMove}
  onCancel={() => (moveFor = null)}
>
  {#if targets.length}
    <div class="space-y-1">
      <label for="ha-move-node" class="text-xs font-semibold">{t('haWorkloads.moveTarget')}</label>
      <select id="ha-move-node" class="input w-full min-h-11" bind:value={moveNode}>
        {#each targets as n (n)}<option value={n}>{n}</option>{/each}
      </select>
    </div>
  {:else}
    <Alert variant="warning">{t('haWorkloads.moveNoTargets')}</Alert>
  {/if}
  {#if moveError}<Alert variant="error">{moveError}</Alert>{/if}
</ConfirmDialog>

<HaDisableDialog
  ref={disableRef}
  onDone={() => (disableRef = null)}
  onCancel={() => (disableRef = null)}
/>

<!-- Maintenance -->
<ConfirmDialog
  open={!!maintFor}
  title={t('haWorkloads.maintTitle', { name: maintFor?.name || '' })}
  description={t('haWorkloads.maintExplain')}
  confirmLabel={maintEnding ? t('haWorkloads.maintEnd') : t('haWorkloads.maintBegin')}
  loading={maintBusy}
  onConfirm={doMaint}
  onCancel={() => (maintFor = null)}
>
  {#if maintEnding}
    <p class="text-xs text-muted-foreground">{t('haWorkloads.maintEndBody')}</p>
  {:else}
    <div class="space-y-1">
      <label for="ha-maint-ttl" class="text-xs font-semibold">{t('haWorkloads.maintTtl')}</label>
      <select id="ha-maint-ttl" class="input w-full min-h-11" bind:value={maintTtl}>
        <option value={900}>{t('haWorkloads.maintTtl15')}</option>
        <option value={3600}>{t('haWorkloads.maintTtl60')}</option>
        <option value={14400}>{t('haWorkloads.maintTtl240')}</option>
      </select>
    </div>
  {/if}
  {#if maintError}<Alert variant="error">{maintError}</Alert>{/if}
</ConfirmDialog>

<!-- Bulk -->
<ConfirmDialog
  open={!!bulkAction}
  title={t(
    bulkAction === 'enable' ? 'haWorkloads.bulkTitleEnable' : 'haWorkloads.bulkTitleDisable',
    {
      count: selectedKeys.length,
    }
  )}
  description={bulkResults
    ? ''
    : t(bulkAction === 'enable' ? 'haWorkloads.bulkBodyEnable' : 'haWorkloads.bulkBodyDisable')}
  confirmLabel={bulkResults ? t('haWorkloads.bulkClose') : t('haWorkloads.bulkRun')}
  hideCancel={!!bulkResults}
  variant={bulkAction === 'disable' && !bulkResults ? 'destructive' : 'default'}
  loading={bulkBusy}
  onConfirm={() => (bulkResults ? closeBulk() : runBulk())}
  onCancel={closeBulk}
>
  {#if bulkAction === 'disable' && !bulkResults}
    <div class="space-y-1">
      <label for="ha-bulk-mode" class="text-xs font-semibold">{t('haWorkloads.disableModes')}</label
      >
      <select id="ha-bulk-mode" class="input w-full min-h-11" bind:value={bulkMode}>
        <option value="stop">{t('haWorkloads.modeStop')}</option>
        <option value="pause">{t('haWorkloads.modePause')}</option>
      </select>
    </div>
  {/if}
  {#if bulkError}<Alert variant="error">{bulkError}</Alert>{/if}
  {#if bulkResults}
    <table class="w-full text-xs" aria-label={t('haWorkloads.bulkResults')}>
      <thead>
        <tr class="text-left text-muted-foreground">
          <th class="p-1">{t('haWorkloads.bulkColItem')}</th>
          <th class="p-1">{t('haWorkloads.bulkColResult')}</th>
        </tr>
      </thead>
      <tbody>
        {#each bulkResults as r (r.key)}
          <tr class="border-t border-border">
            <td class="p-1 font-mono break-all">{r.key}</td>
            <td class="p-1 {r.ok ? 'text-success' : 'text-destructive'}">{bulkItemText(r)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</ConfirmDialog>
