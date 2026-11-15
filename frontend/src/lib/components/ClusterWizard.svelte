<script>
  /**
   * ClusterWizard — "create the cluster" (HA Phase 2).
   *
   * Inline panel with four steps: nodes & network, fencing/SBD, review,
   * run. EASY mode asks for three things (name, nodes, recommended
   * settings) and goes straight to the review; ADVANCED shows everything.
   * The hacluster password is never requested here: it is only shown as a
   * manual step the operator performs on each node.
   */
  import { onMount, onDestroy, tick } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Card } from '$lib/components/ui/card';
  import { Input } from '$lib/components/ui/input';
  import { Badge } from '$lib/components/ui/badge';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import { Skeleton } from '$lib/components/ui/skeleton/index.js';
  import { codeText } from '$lib/utils/nodes.js';
  import {
    validateClusterName,
    validateNodes,
    validateTimeout,
    buildPlanBody,
    buildSetupBody,
    prefillRows,
    existingCluster,
    manualStepCommands,
  } from '$lib/utils/clusterWizard.js';

  const MAX_POLL_ERRORS = 5;
  const MODE_KEY = 'webkvm.clusterWizard.mode';
  const EXTRA_FENCING = ['pve', 'redfish', 'ipmi'];

  let { onclose = () => {}, onCheckReadiness = null, pollMs = 2000 } = $props();

  function loadMode() {
    try {
      return localStorage.getItem(MODE_KEY) === 'advanced' ? 'advanced' : 'easy';
    } catch {
      return 'easy';
    }
  }
  function saveMode(m) {
    try {
      localStorage.setItem(MODE_KEY, m);
    } catch {
      /* private mode: remembering the mode is only a convenience */
    }
  }

  let mode = $state(loadMode());
  let step = $state(1); // 1 nodes, 2 fencing, 3 review, 4 run
  let clusterName = $state('');
  let rows = $state([{ name: '', address: '' }]);
  let recommended = $state(true);
  let allowTwo = $state(false);
  let watchdogDevice = $state('');
  let timeoutSeconds = $state('');
  let fencingExtra = $state([]);
  let sbdEnable = $state(true);
  let enableOnBoot = $state(true);
  let showErrors = $state(false);

  let plan = $state(null);
  let planning = $state(false);
  let planError = $state('');
  let planBody = $state(null);

  let confirmChecked = $state(false);
  let typedName = $state('');
  let starting = $state(false);
  let setupError = $state('');

  let status = $state(null);
  let pollErrors = $state(0);
  let pollStopped = $state(false);
  let pollTimer = null;
  let polling = false;
  let destroyed = false;
  let manualDone = $state({});
  let headingEl = $state(null);
  // A Pacemaker cluster that already runs: { name, nodes } or null.
  let existing = $state(null);
  // True until the first prefill (registry + /ha/readiness) answered: the
  // create form must not flash before we know a cluster is already running.
  let initialLoading = $state(true);

  const easy = $derived(mode === 'easy');
  const effAllowTwo = $derived(!easy && allowTwo);
  const validation = $derived(validateNodes(rows, { allowTwo: effAllowTwo }));
  const nameError = $derived(validateClusterName(clusterName));
  const timeoutError = $derived(easy ? null : validateTimeout(timeoutSeconds));
  const step1Valid = $derived(!nameError && validation.valid && !timeoutError);
  const blockers = $derived(plan?.blockers || []);
  const clusterExists = $derived(blockers.includes('cluster_exists'));
  const canRun = $derived(!!plan && blockers.length === 0 && !clusterExists);
  const running = $derived(status?.state === 'running');
  const nameConfirmed = $derived(typedName.trim() === planBody?.cluster_name);
  const stepLabels = $derived([
    t('nodes.cw.step1'),
    t('nodes.cw.step2'),
    t('nodes.cw.step3'),
    t('nodes.cw.step4'),
  ]);
  const visibleSteps = $derived(easy ? [1, 3, 4] : [1, 2, 3, 4]);
  const manual = $derived(manualStepCommands(planBody?.nodes || []));

  function currentBody() {
    return buildPlanBody({
      clusterName,
      nodes: rows,
      watchdogDevice: easy ? '' : watchdogDevice,
      timeoutSeconds: easy ? '' : timeoutSeconds,
      fencing: easy ? [] : fencingExtra,
      allowTwo: effAllowTwo,
    });
  }

  async function focusHeading() {
    await tick();
    headingEl?.focus();
  }

  function setMode(m) {
    mode = m;
    saveMode(m);
    if (m === 'easy') recommended = true;
    if (m === 'easy' && step === 2) step = 1;
  }

  function onRecommendedChange() {
    // Unchecking "recommended" means the operator wants to choose: that is
    // what ADVANCED is for.
    if (!recommended) setMode('advanced');
  }

  function addRow() {
    rows = [...rows, { name: '', address: '' }];
  }
  function removeRow(i) {
    rows = rows.filter((_, idx) => idx !== i);
  }

  // Rows from the registry (this server once, by name; never the browser's
  // host, which may be the VIP) plus every member of a detected Pacemaker
  // membership. Also records an already-running cluster.
  async function prefill() {
    const [nodesRes, readiness] = await Promise.all([
      api.listNodes().catch(() => null),
      api.getHAReadiness ? api.getHAReadiness().catch(() => null) : Promise.resolve(null),
    ]);
    if (destroyed) return;
    const found = existingCluster(readiness);
    existing = found;
    const next = prefillRows({ nodes: nodesRes?.nodes, pacemakerNames: found?.nodes || [] });
    if (next.length) rows = next;
    if (initialLoading) {
      initialLoading = false;
      focusHeading();
    }
  }

  async function runPlan(nextStep) {
    showErrors = true;
    if (!step1Valid) return;
    planning = true;
    planError = '';
    const body = currentBody();
    try {
      const res = await api.haClusterPlan(body);
      plan = res;
      planBody = body;
      step = nextStep;
      confirmChecked = false;
      typedName = '';
      focusHeading();
    } catch (e) {
      planError = e?.message || String(e);
    } finally {
      planning = false;
    }
  }

  function goBack() {
    step = easy ? 1 : step - 1;
    focusHeading();
  }

  function toggleFencing(kind) {
    fencingExtra = fencingExtra.includes(kind)
      ? fencingExtra.filter((k) => k !== kind)
      : [...fencingExtra, kind];
  }

  async function copy(text) {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(t('nodes.cw.copied'));
    } catch {
      toast.error(t('nodes.cw.copyFailed'));
    }
  }

  // --- status polling: chained setTimeout, never two requests in flight ---
  function clearPoll() {
    if (pollTimer) clearTimeout(pollTimer);
    pollTimer = null;
  }
  function schedulePoll() {
    clearPoll();
    if (destroyed) return;
    pollTimer = setTimeout(poll, pollMs);
  }
  async function poll() {
    if (destroyed || polling) return;
    polling = true;
    try {
      const s = await api.haClusterStatus();
      if (destroyed) return;
      pollErrors = 0;
      status = s;
      if (s?.state === 'running') schedulePoll();
      else focusHeading();
    } catch {
      if (destroyed) return;
      pollErrors += 1;
      if (pollErrors >= MAX_POLL_ERRORS) pollStopped = true;
      else schedulePoll();
    } finally {
      polling = false;
    }
  }
  function retryPoll() {
    pollErrors = 0;
    pollStopped = false;
    poll();
  }

  async function startSetup() {
    if (!canRun || !confirmChecked || !nameConfirmed || starting) return;
    starting = true;
    setupError = '';
    try {
      const res = await api.haClusterSetup(
        buildSetupBody(planBody, {
          sbdEnable: easy || sbdEnable,
          enableOnBoot: easy || enableOnBoot,
        })
      );
      status = { state: 'running', steps: res?.steps || [] };
      pollErrors = 0;
      pollStopped = false;
      schedulePoll();
    } catch (e) {
      const code = e?.code;
      const known = [
        'cluster_exists',
        'module_not_installed',
        'setup_busy',
        'confirmation_required',
        'plan_blocked',
      ];
      setupError = known.includes(code)
        ? t(`nodes.cw.setupErrors.${code}`)
        : e?.message || String(e);
      if (code === 'plan_blocked' && Array.isArray(e?.data?.blockers)) {
        plan = { ...plan, blockers: e.data.blockers };
        step = 3;
        focusHeading();
      }
      if (code === 'setup_busy') {
        status = { state: 'running', steps: [] };
        schedulePoll();
      }
    } finally {
      starting = false;
    }
  }

  // After a failed or interrupted run: forget the status and go back to the
  // review, re-calling /plan so the operator sees the current state.
  function replan() {
    clearPoll();
    status = null;
    pollErrors = 0;
    pollStopped = false;
    setupError = '';
    runPlan(3);
  }

  function checkReadiness() {
    onCheckReadiness?.();
    onclose();
  }

  onMount(async () => {
    focusHeading();
    // Resume an in-flight setup after a page reload.
    try {
      const s = await api.haClusterStatus();
      if (s?.state === 'running' && !destroyed) {
        status = s;
        initialLoading = false;
        step = 4;
        schedulePoll();
        return;
      }
    } catch {
      /* no status yet: start from scratch */
    }
    if (!destroyed) prefill();
  });
  onDestroy(() => {
    destroyed = true;
    clearPoll();
  });

  const stepStateKey = (s) => codeText(t, 'nodes.cw.stepState', s, 'nodes.cw.stepStateUnknown');
  const stepIdText = (id) => {
    const key = `nodes.cw.stepIds.${id}`;
    const out = t(key);
    return out === key ? id : out;
  };
  const blockerText = (c) => codeText(t, 'nodes.cw.blockers', c, 'nodes.cw.unknownBlocker');
  const warningText = (c) => codeText(t, 'nodes.cw.warnings', c, 'nodes.cw.unknownWarning');
  const checkText = (id) => {
    const key = `nodes.cw.checks.${id}`;
    const out = t(key);
    return out === key ? id : out;
  };
  const rowErr = (code) => (code ? t(`nodes.cw.rowErrors.${code}`) : '');
  const wdType = $derived(plan?.watchdog?.type || 'none');
  const wdVariant = $derived(
    { hardware: 'success', i6300esb: 'info', softdog: 'warning', none: 'destructive' }[wdType] ||
      'secondary'
  );
</script>

{#snippet copyBtn(text, label)}
  <Button
    size="sm"
    variant="outline"
    class="min-h-11"
    aria-label={label}
    onclick={() => copy(text)}
  >
    {t('nodes.cw.copy')}
  </Button>
{/snippet}

<Card
  class="space-y-4 border border-border bg-card p-4"
  role="region"
  aria-labelledby="cw-title"
  data-testid="cluster-wizard"
>
  <div class="flex flex-wrap items-start justify-between gap-2">
    <div class="space-y-1">
      <h2 id="cw-title" class="text-sm font-semibold text-foreground">
        {initialLoading && step !== 4
          ? t('nodes.cw.loadingTitle')
          : existing && step !== 4
            ? t('nodes.cw.existing.heading')
            : t('nodes.cw.title')}
      </h2>
      {#if !initialLoading && !(existing && step !== 4)}
        <p class="text-xs text-muted-foreground">{t('nodes.cw.intro')}</p>
      {/if}
    </div>
    <div class="flex flex-wrap items-center gap-2">
      {#if !initialLoading || step === 4}
        <div class="flex gap-2" role="group" aria-label={t('nodes.cw.mode')}>
          <Button
            size="sm"
            variant={easy ? 'default' : 'outline'}
            class="min-h-11"
            aria-pressed={easy}
            disabled={running}
            onclick={() => setMode('easy')}>{t('nodes.cw.easy')}</Button
          >
          <Button
            size="sm"
            variant={!easy ? 'default' : 'outline'}
            class="min-h-11"
            aria-pressed={!easy}
            disabled={running}
            onclick={() => setMode('advanced')}>{t('nodes.cw.advanced')}</Button
          >
        </div>
      {/if}
      <Button size="sm" variant="ghost" class="min-h-11" disabled={running} onclick={onclose}>
        {t('nodes.cw.close')}
      </Button>
    </div>
  </div>

  {#if initialLoading && step !== 4}
    <div
      class="space-y-3"
      data-testid="cw-loading"
      role="status"
      aria-live="polite"
      aria-label={t('nodes.cw.loadingState')}
    >
      <p class="sr-only">{t('nodes.cw.loadingState')}</p>
      <Skeleton class="h-5 w-48" />
      <Skeleton class="h-9 w-full max-w-xs" />
      <Skeleton class="h-9 w-full" />
      <Skeleton class="h-9 w-full" />
    </div>
  {:else if existing && step !== 4}
    <section
      class="space-y-2 rounded border border-border bg-background/70 p-3"
      data-testid="cw-existing-cluster"
      aria-labelledby="cw-existing-title"
    >
      <h3 id="cw-existing-title" class="text-sm font-medium">
        {existing.name
          ? t('nodes.cw.existing.titleNamed', { name: existing.name, n: existing.nodes.length })
          : t('nodes.cw.existing.title', { n: existing.nodes.length })}
      </h3>
      <ul class="flex flex-wrap gap-1 text-xs">
        {#each existing.nodes as m (m)}
          <li><Badge variant="outline">{m}</Badge></li>
        {/each}
      </ul>
      <p class="text-xs text-muted-foreground">{t('nodes.cw.existing.noCreate')}</p>
      <p class="text-xs font-medium">{t('nodes.cw.existing.addTitle')}</p>
      <p class="text-xs text-muted-foreground">{t('nodes.cw.existing.addHint')}</p>
      <p class="text-xs font-medium">{t('nodes.cw.existing.manageTitle')}</p>
      <p class="text-xs text-muted-foreground">
        {t('nodes.cw.existing.manageHint')}
        <a href="#/nodes" class="font-medium underline underline-offset-2"
          >{t('nodes.cw.existing.manageLink')}</a
        >
      </p>
    </section>
  {:else}
    <ol class="flex flex-wrap gap-2 text-xs" aria-label={t('nodes.cw.steps')}>
      {#each visibleSteps as s, i (s)}
        <li
          class="rounded border px-2 py-1 {step === s
            ? 'border-primary bg-primary/10 font-semibold text-foreground'
            : 'border-border text-muted-foreground'}"
          aria-current={step === s ? 'step' : undefined}
        >
          {i + 1}. {stepLabels[s - 1]}
        </li>
      {/each}
    </ol>

    <!-- ============ Step 1: nodes & network ============ -->
    {#if step === 1}
      <section class="space-y-3" aria-labelledby="cw-h1">
        <h3 id="cw-h1" bind:this={headingEl} tabindex="-1" class="text-sm font-medium outline-none">
          {t('nodes.cw.step1')}
        </h3>
        <p class="text-xs text-muted-foreground">{t('nodes.cw.step1Hint')}</p>

        <div class="space-y-1">
          <label class="text-xs font-medium text-foreground" for="cw-cluster-name"
            >{t('nodes.cw.clusterName')}</label
          >
          <Input
            id="cw-cluster-name"
            bind:value={clusterName}
            autocomplete="off"
            aria-invalid={showErrors && !!nameError}
            aria-describedby="cw-name-hint"
            class="max-w-xs"
          />
          <p
            id="cw-name-hint"
            class="text-[11px] {showErrors && nameError
              ? 'text-destructive'
              : 'text-muted-foreground'}"
          >
            {showErrors && nameError
              ? t('nodes.cw.rowErrors.name_invalid')
              : t('nodes.cw.nameHint')}
          </p>
        </div>

        <fieldset class="space-y-2">
          <legend class="text-xs font-medium text-foreground">{t('nodes.cw.nodes')}</legend>
          {#each rows as row, i (i)}
            <div class="grid gap-2 sm:grid-cols-[1fr_1fr_auto] sm:items-start">
              <div>
                <Input
                  bind:value={row.name}
                  autocomplete="off"
                  placeholder={t('nodes.cw.nodeName')}
                  aria-label={t('nodes.cw.nodeNameRow', { n: i + 1 })}
                  aria-invalid={showErrors && !!validation.rows[i]?.name}
                />
                {#if showErrors && validation.rows[i]?.name}
                  <p class="text-[11px] text-destructive">{rowErr(validation.rows[i].name)}</p>
                {/if}
              </div>
              <div>
                <Input
                  bind:value={row.address}
                  autocomplete="off"
                  placeholder={t('nodes.cw.nodeAddress')}
                  aria-label={t('nodes.cw.nodeAddressRow', { n: i + 1 })}
                  aria-invalid={showErrors && !!validation.rows[i]?.address}
                />
                {#if showErrors && validation.rows[i]?.address}
                  <p class="text-[11px] text-destructive">{rowErr(validation.rows[i].address)}</p>
                {/if}
              </div>
              <Button
                size="sm"
                variant="outline"
                class="min-h-11"
                aria-label={t('nodes.cw.removeNodeRow', { n: i + 1 })}
                disabled={rows.length <= 1}
                onclick={() => removeRow(i)}>{t('nodes.cw.remove')}</Button
              >
            </div>
          {/each}
          <div class="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" class="min-h-11" onclick={addRow}
              >{t('nodes.cw.addNode')}</Button
            >
            <Button size="sm" variant="outline" class="min-h-11" onclick={prefill}
              >{t('nodes.cw.prefill')}</Button
            >
          </div>
        </fieldset>

        {#if validation.count !== 'ok'}
          <Alert variant="warning" title={t('nodes.cw.quorumTitle')}>
            <p>
              {validation.count === 'too_few'
                ? t('nodes.cw.quorumTooFew')
                : t('nodes.cw.quorumTwoNodes')}
            </p>
            {#if easy && validation.count === 'two_nodes'}
              <p class="mt-1 text-xs">{t('nodes.cw.quorumEasyHint')}</p>
            {/if}
          </Alert>
          {#if !easy && validation.count === 'two_nodes'}
            <label class="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                bind:checked={allowTwo}
                class="mt-0.5 h-4 w-4 rounded border-border"
              />
              <span>{t('nodes.cw.allowTwo')}</span>
            </label>
          {/if}
        {/if}

        {#if easy}
          <label class="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              bind:checked={recommended}
              onchange={onRecommendedChange}
              class="mt-0.5 h-4 w-4 rounded border-border"
            />
            <span>
              <span class="block font-medium">{t('nodes.cw.recommended')}</span>
              <span class="block text-xs text-muted-foreground"
                >{t('nodes.cw.recommendedHint')}</span
              >
            </span>
          </label>
        {/if}

        {#if planError}<Alert variant="error">{planError}</Alert>{/if}
        <div class="flex flex-wrap gap-2">
          {#if easy}
            <Button class="min-h-11" disabled={planning} onclick={() => runPlan(3)}>
              {#if planning}<Spinner size="sm" />{/if}
              {t('nodes.cw.reviewPlan')}
            </Button>
          {:else}
            <Button class="min-h-11" disabled={planning} onclick={() => runPlan(2)}>
              {#if planning}<Spinner size="sm" />{/if}
              {t('nodes.cw.next')}
            </Button>
          {/if}
        </div>
        {#if showErrors && !step1Valid}
          <p class="text-xs text-destructive" role="alert">{t('nodes.cw.fixErrors')}</p>
        {/if}
      </section>
    {/if}

    <!-- ============ Step 2: fencing / SBD ============ -->
    {#if step === 2 && plan}
      <section class="space-y-3" aria-labelledby="cw-h2">
        <h3 id="cw-h2" bind:this={headingEl} tabindex="-1" class="text-sm font-medium outline-none">
          {t('nodes.cw.step2')}
        </h3>
        <p class="text-xs text-muted-foreground">{t('nodes.cw.step2Hint')}</p>

        <div class="space-y-2 rounded border border-border p-3">
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-xs font-medium">{t('nodes.cw.watchdog')}:</span>
            <Badge variant={wdVariant} data-testid="wd-type"
              >{codeText(t, 'nodes.cw.wdType', wdType, 'nodes.cw.wdTypeUnknown')}</Badge
            >
            {#if plan.watchdog?.devices?.length}
              <code class="text-[11px]">{plan.watchdog.devices.join(', ')}</code>
            {/if}
          </div>
          <p class="text-xs text-muted-foreground">
            {codeText(t, 'nodes.cw.wdDesc', wdType, 'nodes.cw.wdTypeUnknown')}
          </p>
          {#if wdType === 'softdog'}
            <Alert variant="warning" title={t('nodes.cw.softdogTitle')}>
              {t('nodes.cw.softdogWarning')}
            </Alert>
          {/if}
          {#if plan.virt}
            <p class="text-xs text-muted-foreground" data-testid="virt-hint">
              {plan.virt.is_vm
                ? t('nodes.cw.virtVm', { kind: plan.virt.kind || '?' })
                : t('nodes.cw.virtMetal')}
            </p>
          {/if}
          {#if plan.fencing_hint}
            <p class="text-xs text-muted-foreground">
              {codeText(
                t,
                'nodes.cw.fencingHints',
                plan.fencing_hint,
                'nodes.cw.fencingHintUnknown'
              )}
            </p>
          {/if}
        </div>

        <div class="grid gap-3 sm:grid-cols-2">
          <div class="space-y-1">
            <label class="text-xs font-medium" for="cw-wd-dev">{t('nodes.cw.watchdogDevice')}</label
            >
            <Input
              id="cw-wd-dev"
              bind:value={watchdogDevice}
              placeholder={plan.watchdog?.recommended || '/dev/watchdog'}
              autocomplete="off"
            />
            <p class="text-[11px] text-muted-foreground">{t('nodes.cw.watchdogDeviceHint')}</p>
          </div>
          <div class="space-y-1">
            <label class="text-xs font-medium" for="cw-sbd-timeout"
              >{t('nodes.cw.sbdTimeout')}</label
            >
            <Input
              id="cw-sbd-timeout"
              type="number"
              min="5"
              max="120"
              bind:value={timeoutSeconds}
              placeholder="30"
              aria-invalid={!!timeoutError}
            />
            <p class="text-[11px] {timeoutError ? 'text-destructive' : 'text-muted-foreground'}">
              {timeoutError
                ? t('nodes.cw.rowErrors.timeout_invalid')
                : t('nodes.cw.sbdTimeoutHint')}
            </p>
          </div>
        </div>

        <fieldset class="space-y-1">
          <legend class="text-xs font-medium">{t('nodes.cw.extraFencing')}</legend>
          <p class="text-[11px] text-muted-foreground">{t('nodes.cw.extraFencingHint')}</p>
          {#each EXTRA_FENCING as kind (kind)}
            <label class="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={fencingExtra.includes(kind)}
                onchange={() => toggleFencing(kind)}
                class="h-4 w-4 rounded border-border"
              />
              <span>{t(`nodes.cw.fencingKinds.${kind}`)}</span>
            </label>
          {/each}
        </fieldset>

        {#if planError}<Alert variant="error">{planError}</Alert>{/if}
        <div class="flex flex-wrap gap-2">
          <Button variant="outline" class="min-h-11" onclick={goBack}>{t('nodes.cw.back')}</Button>
          <Button class="min-h-11" disabled={planning || !!timeoutError} onclick={() => runPlan(3)}>
            {#if planning}<Spinner size="sm" />{/if}
            {t('nodes.cw.reviewPlan')}
          </Button>
        </div>
      </section>
    {/if}

    <!-- ============ Step 3: review ============ -->
    {#if step === 3 && plan}
      <section class="space-y-3" aria-labelledby="cw-h3">
        <h3 id="cw-h3" bind:this={headingEl} tabindex="-1" class="text-sm font-medium outline-none">
          {t('nodes.cw.step3')}
        </h3>

        {#if clusterExists}
          <Alert variant="error" title={t('nodes.cw.clusterExistsTitle')}>
            {t('nodes.cw.clusterExistsBody')}
          </Alert>
        {/if}

        {#if blockers.filter((b) => b !== 'cluster_exists').length}
          <Alert variant="error" title={t('nodes.cw.blockersTitle')}>
            <ul class="list-disc space-y-0.5 pl-5">
              {#each blockers.filter((b) => b !== 'cluster_exists') as b (b)}
                <li>{blockerText(b)}</li>
              {/each}
            </ul>
          </Alert>
        {/if}
        {#if plan.warnings?.length}
          <Alert variant="warning" title={t('nodes.cw.warningsTitle')}>
            <ul class="list-disc space-y-0.5 pl-5">
              {#each plan.warnings as w (w)}
                <li>{warningText(w)}</li>
              {/each}
            </ul>
          </Alert>
        {/if}
        {#if canRun}
          <Alert variant="success">{t('nodes.cw.planOk', { count: planBody.nodes.length })}</Alert>
        {/if}

        {#if !easy && plan.checks?.length}
          <details class="rounded border border-border p-2">
            <summary class="cursor-pointer text-xs font-medium">{t('nodes.cw.checks')}</summary>
            <ul class="mt-2 space-y-1 text-xs">
              {#each plan.checks as c (c.id)}
                <li>
                  <span class="font-medium">{c.ok ? '✓' : '✗'} {checkText(c.id)}</span>
                  <span class="sr-only"
                    >{c.ok ? t('nodes.cw.checkOk') : t('nodes.cw.checkFail')}</span
                  >
                  {#if c.detail}<span class="text-muted-foreground"> — {c.detail}</span>{/if}
                </li>
              {/each}
            </ul>
          </details>
        {/if}

        <div class="space-y-2">
          <h4 class="text-xs font-semibold text-foreground">{t('nodes.cw.manualTitle')}</h4>
          <p class="text-xs text-muted-foreground">{t('nodes.cw.manualIntro')}</p>
          <ul class="space-y-2">
            {#each planBody.nodes as n (n.name)}
              <li class="space-y-1 rounded border border-border p-2 text-xs">
                <label class="flex items-start gap-2 font-medium">
                  <input
                    type="checkbox"
                    class="mt-0.5 h-4 w-4 rounded border-border"
                    checked={!!manualDone[n.name]}
                    onchange={(e) =>
                      (manualDone = { ...manualDone, [n.name]: e.currentTarget.checked })}
                  />
                  <span>{t('nodes.cw.manualNode', { name: n.name, address: n.address })}</span>
                </label>
                <div class="flex flex-wrap items-center gap-2 pl-6">
                  <code class="rounded bg-muted px-1.5 py-0.5">{manual.passwd}</code>
                  {@render copyBtn(manual.passwd, t('nodes.cw.copyFor', { name: n.name }))}
                  <span class="text-muted-foreground">{t('nodes.cw.passwdNote')}</span>
                </div>
              </li>
            {/each}
          </ul>
          <div class="space-y-1 text-xs">
            <p>{t('nodes.cw.hostAuthNote')}</p>
            <div class="flex flex-wrap items-center gap-2">
              <code class="rounded bg-muted px-1.5 py-0.5">{manual.hostAuth}</code>
              {@render copyBtn(manual.hostAuth, t('nodes.cw.copyHostAuth'))}
            </div>
          </div>
          {#if plan.manual_steps?.length}
            <ul class="space-y-1 text-xs" data-testid="manual-steps">
              {#each plan.manual_steps as ms, i (i)}
                {@const text =
                  typeof ms === 'string' ? ms : ms.text || ms.description || ms.command || ''}
                {@const cmd = typeof ms === 'string' ? ms : ms.command || ''}
                <li class="flex flex-wrap items-start gap-2 rounded border border-border p-2">
                  <label class="flex flex-1 items-start gap-2">
                    <input type="checkbox" class="mt-0.5 h-4 w-4 rounded border-border" />
                    <span class="break-words">{text}</span>
                  </label>
                  {#if cmd}
                    {@render copyBtn(cmd, t('nodes.cw.copyStep', { n: i + 1 }))}
                  {/if}
                </li>
              {/each}
            </ul>
          {/if}
        </div>

        {#if plan.commands?.length}
          <div class="space-y-1">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <h4 class="text-xs font-semibold text-foreground">{t('nodes.cw.commandsTitle')}</h4>
              {@render copyBtn(plan.commands.join('\n'), t('nodes.cw.copyCommands'))}
            </div>
            <pre
              class="overflow-x-auto rounded bg-muted p-2 text-[11px] leading-relaxed"
              data-testid="plan-commands">{plan.commands.join('\n')}</pre>
          </div>
        {/if}

        {#if planError}<Alert variant="error">{planError}</Alert>{/if}
        <div class="flex flex-wrap gap-2">
          <Button variant="outline" class="min-h-11" onclick={goBack}>{t('nodes.cw.back')}</Button>
          <Button variant="outline" class="min-h-11" disabled={planning} onclick={() => runPlan(3)}>
            {#if planning}<Spinner size="sm" />{/if}
            {t('nodes.cw.recheck')}
          </Button>
          <Button
            class="min-h-11"
            disabled={!canRun}
            onclick={() => {
              step = 4;
              focusHeading();
            }}>{t('nodes.cw.continue')}</Button
          >
        </div>
      </section>
    {/if}

    <!-- ============ Step 4: run ============ -->
    {#if step === 4}
      <section class="space-y-3" aria-labelledby="cw-h4">
        <h3 id="cw-h4" bind:this={headingEl} tabindex="-1" class="text-sm font-medium outline-none">
          {t('nodes.cw.step4')}
        </h3>

        {#if !status}
          <Alert variant="warning">{t('nodes.cw.runWarning')}</Alert>
          {#if !easy}
            <div class="space-y-1">
              <label class="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  bind:checked={sbdEnable}
                  class="h-4 w-4 rounded border-border"
                />
                <span>{t('nodes.cw.sbdEnable')}</span>
              </label>
              <label class="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  bind:checked={enableOnBoot}
                  class="h-4 w-4 rounded border-border"
                />
                <span>{t('nodes.cw.enableOnBoot')}</span>
              </label>
            </div>
          {/if}
          <label class="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              bind:checked={confirmChecked}
              class="mt-0.5 h-4 w-4 rounded border-border"
            />
            <span>{t('nodes.cw.confirmCheck')}</span>
          </label>
          <div class="space-y-1">
            <label class="text-xs font-medium" for="cw-type-name"
              >{t('nodes.cw.typeName', { name: planBody?.cluster_name || '' })}</label
            >
            <Input id="cw-type-name" bind:value={typedName} autocomplete="off" class="max-w-xs" />
          </div>
          {#if setupError}<Alert variant="error">{setupError}</Alert>{/if}
          <div class="flex flex-wrap gap-2">
            <Button variant="outline" class="min-h-11" onclick={goBack}>{t('nodes.cw.back')}</Button
            >
            <Button
              class="min-h-11"
              disabled={!canRun || !confirmChecked || !nameConfirmed || starting}
              onclick={startSetup}
            >
              {#if starting}<Spinner size="sm" />{/if}
              {t('nodes.cw.createCluster')}
            </Button>
          </div>
        {:else}
          <div aria-live="polite" class="space-y-2">
            {#if status.state === 'running'}
              <p class="flex items-center gap-2 text-xs text-muted-foreground" role="status">
                <Spinner size="sm" />
                {t('nodes.cw.running')}
              </p>
            {/if}
            {#if status.steps?.length}
              <ol class="space-y-1 text-sm" data-testid="setup-steps">
                {#each status.steps as s (s.id)}
                  <li class="flex flex-wrap items-baseline gap-2">
                    <span aria-hidden="true">
                      {s.state === 'done'
                        ? '✓'
                        : s.state === 'failed'
                          ? '✗'
                          : s.state === 'running'
                            ? '…'
                            : s.state === 'skipped'
                              ? '–'
                              : '○'}
                    </span>
                    <span class="font-medium">{stepIdText(s.id)}</span>
                    <span class="sr-only">{stepStateKey(s.state)}</span>
                    <Badge
                      variant={s.state === 'done'
                        ? 'success'
                        : s.state === 'failed'
                          ? 'destructive'
                          : s.state === 'running'
                            ? 'info'
                            : 'secondary'}>{stepStateKey(s.state)}</Badge
                    >
                    {#if s.detail}<span class="text-xs text-muted-foreground">{s.detail}</span>{/if}
                  </li>
                {/each}
              </ol>
            {/if}
          </div>

          {#if pollStopped}
            <Alert variant="error">
              <p>{t('nodes.cw.pollStopped')}</p>
              <Button size="sm" variant="outline" class="mt-2 min-h-11" onclick={retryPoll}
                >{t('nodes.cw.retry')}</Button
              >
            </Alert>
          {/if}

          {#if status.state === 'failed'}
            <Alert variant="error" title={t('nodes.cw.failedTitle')}>
              {#if status.failed_step}
                <p>{t('nodes.cw.failedStep', { step: stepIdText(status.failed_step) })}</p>
              {/if}
              {#if status.error}<p class="mt-1 break-words">{status.error}</p>{/if}
            </Alert>
            {#if status.error === 'interrupted'}
              <p class="text-xs text-muted-foreground">{t('nodes.cw.interrupted')}</p>
            {/if}
            <Button variant="outline" class="min-h-11" onclick={replan}
              >{t('nodes.cw.replan')}</Button
            >
            {#if status.commands_remaining?.length}
              <div class="space-y-1">
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <h4 class="text-xs font-semibold">{t('nodes.cw.remainingTitle')}</h4>
                  {@render copyBtn(
                    status.commands_remaining.join('\n'),
                    t('nodes.cw.copyRemaining')
                  )}
                </div>
                <p class="text-xs text-muted-foreground">{t('nodes.cw.remainingHint')}</p>
                <pre
                  class="overflow-x-auto rounded bg-muted p-2 text-[11px] leading-relaxed"
                  data-testid="commands-remaining">{status.commands_remaining.join('\n')}</pre>
              </div>
            {/if}
          {/if}

          {#if status.log?.length}
            <details class="rounded border border-border p-2">
              <summary class="cursor-pointer text-xs font-medium">{t('nodes.cw.log')}</summary>
              <pre
                class="mt-2 max-h-56 overflow-auto text-[11px] whitespace-pre-wrap">{status.log.join(
                  '\n'
                )}</pre>
            </details>
          {/if}

          {#if status.state === 'done'}
            <Alert variant="success">{t('nodes.cw.doneBody')}</Alert>
            <Button class="min-h-11" onclick={checkReadiness}>{t('nodes.cw.checkReadiness')}</Button
            >
          {/if}
        {/if}
      </section>
    {/if}
  {/if}
</Card>
