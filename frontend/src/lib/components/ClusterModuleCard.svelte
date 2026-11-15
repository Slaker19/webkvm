<script>
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import {
    clusterModule,
    refreshClusterModule,
    startClusterModuleInstall,
    verifyClusterModule,
    enableClusterModule,
    disableClusterModule,
    uninstallClusterModule,
  } from '$lib/stores/clusterModule.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { ChevronDown, ChevronUp } from '@lucide/svelte';
  import { Card } from '$lib/components/ui/card';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import ClusterWizard from '$lib/components/ClusterWizard.svelte';
  import HaPlanGuide from '$lib/components/HaPlanGuide.svelte';

  const POLL_MS = 1500;
  const MAX_POLL_ERRORS = 5;

  let { onCheckReadiness = null } = $props();
  let showWizard = $state(false);

  let plan = $state(null);
  let preflightLoading = $state(false);
  let installing = $state(false);
  let installLinstor = $state(false);
  // The flag the current plan was generated with. The install always sends
  // this one, never the live checkbox, so the preflight and the install
  // cannot disagree.
  let planLinstor = $state(false);
  let acceptExperimental = $state(false);
  let advanced = $state(false);
  let uninstallOpen = $state(false);
  let uninstallBlockers = $state([]);
  let purgeOnUninstall = $state(false);
  let uninstalling = $state(false);
  let verifying = $state(false);
  let toggling = $state(false);
  let disableOpen = $state(false);
  let pollFailed = $state(false);
  let pollTimer = null;
  let pollActive = false;
  let pollErrors = 0;
  // 'install' | 'uninstall' | '' — what the running operation is, so the
  // final state can be reported correctly.
  let activeOp = '';

  const loadFailed = $derived(!!clusterModule.error && !clusterModule.loaded);
  const status = $derived(clusterModule.status);
  const state = $derived(status?.state || 'not_installed');
  const installed = $derived(state === 'installed');
  const partial = $derived(state === 'partial');
  const partialGuided = $derived(state === 'partial_guided');
  const running = $derived(
    state === 'installing' || state === 'uninstalling' || clusterModule.busy
  );
  const planTier = $derived(plan?.preflight?.tier || plan?.tier || '');
  const needsExperimentalAck = $derived(
    typeof plan?.accept_required === 'boolean' ? plan.accept_required : planTier === 'experimental'
  );
  // Old backends sent `guided` meaning "no install button". New ones send
  // can_install explicitly and `guided` only means "manual steps exist".
  const planGuided = $derived(
    planTier === 'guided' || (!!plan?.guided && typeof plan?.can_install !== 'boolean')
  );
  const canInstall = $derived(
    typeof plan?.can_install === 'boolean' ? plan.can_install : !planGuided
  );
  const hasGuide = $derived(Array.isArray(plan?.guided_steps));
  const guidedMissing = $derived(
    Array.isArray(status?.guided_missing) ? status.guided_missing : []
  );
  // Package-manager family of the node, to word the guided text. Falls back
  // to the neutral text when the backend does not say (or says something new).
  const planFamily = $derived.by(() => {
    const raw = String(plan?.preflight?.family || plan?.family || '').toLowerCase();
    const distro = String(plan?.preflight?.distro || '').toLowerCase();
    if (['dnf', 'rhel', 'fedora'].includes(raw) || ['fedora', 'rhel'].includes(distro))
      return 'Dnf';
    if (['pacman', 'arch'].includes(raw) || ['arch', 'manjaro', 'endeavouros'].includes(distro))
      return 'Pacman';
    return '';
  });
  const tierSuffix = $derived(
    planGuided
      ? 'Guided'
      : planTier === 'experimental'
        ? 'Experimental'
        : planTier === 'partial'
          ? 'Partial'
          : 'Unsupported'
  );
  const showTierAlert = $derived(planGuided || (!!planTier && planTier !== 'supported'));
  const unitsActive = $derived(Array.isArray(status?.units_active) ? status.units_active : []);
  const unknownUnits = $derived(Array.isArray(status?.unknown_units) ? status.unknown_units : []);
  const planMissingCount = $derived((plan?.packages || []).filter((p) => !p.installed).length);
  const planInstalledCount = $derived((plan?.packages || []).filter((p) => p.installed).length);
  const attentionChecks = $derived((plan?.preflight?.checks || []).filter((c) => !c.ok));

  const causeKeys = {
    network: 'causeNetwork',
    lock: 'causeLock',
    disk_full: 'causeDiskFull',
    package_unavailable: 'causePackageUnavailable',
    broken_dependencies: 'causeBrokenDependencies',
    not_root: 'causeNotRoot',
    sandbox_readonly: 'causeSandboxReadonly',
    unsupported_distro: 'causeUnsupportedDistro',
    preflight: 'causePreflight',
    interrupted: 'causeInterrupted',
    services_active: 'causeServicesActive',
    guided_distro: 'causeGuidedDistro',
    experimental_not_accepted: 'causeExperimentalNotAccepted',
    repo_missing: 'causeRepoMissing',
    sync_db_missing: 'causeSyncDbMissing',
    guided_pending: 'causeGuidedPending',
  };

  const checkKeys = {
    distro: 'checkDistro',
    privileges: 'checkPrivileges',
    pkg_lock: 'checkLock',
    apt_lock: 'checkLock',
    repo_scope: 'checkRepos',
    disk: 'checkDisk',
    time_sync: 'checkTime',
    hostname: 'checkHostname',
    drbd_module: 'checkDrbd',
    ha_repo: 'checkHaRepo',
    sync_db: 'checkSyncDb',
    watchdog: 'checkWatchdog',
    virt: 'checkVirt',
    experimental: 'checkExperimental',
  };

  // Install step ids -> translated label. Unknown ids fall back to a
  // generic "Step {id}" so no raw internal name is shown as copy.
  const stepKeys = {
    preflight: 'stepPreflight',
    snapshot_units: 'stepSnapshotUnits',
    pkg_refresh: 'stepPkgRefresh',
    install_packages: 'stepPackages',
    packages: 'stepPackages',
    restore_units: 'stepRestoreUnits',
    services: 'stepServices',
    verify: 'stepVerify',
  };
  // Legacy step the new backend no longer emits; never displayed.
  const HIDDEN_STEPS = ['mask_services'];
  const visibleSteps = $derived(
    (status?.steps || []).filter((step) => !HIDDEN_STEPS.includes(step.id))
  );

  function stepLabel(id) {
    return t('nodes.clusterModule.' + (stepKeys[id] || 'stepOther'), { id });
  }

  // Backend `uninstall_blocked` reasons. Prefixed ones carry a path after
  // the colon, which is appended raw.
  const BLOCKER_KEYS = {
    pacemaker_cluster_configured: 'blockerPacemakerConfigured',
    pacemaker_state_unknown: 'blockerPacemakerUnknown',
    pacemaker_probe_failed: 'blockerPacemakerProbeFailed',
  };
  const BLOCKER_PREFIX_KEYS = {
    config_present: 'blockerConfigPresent',
    config_check_failed: 'blockerConfigCheckFailed',
  };

  function blockerText(blocker) {
    const raw = String(blocker || '');
    if (BLOCKER_KEYS[raw]) return t('nodes.clusterModule.' + BLOCKER_KEYS[raw]);
    const idx = raw.indexOf(':');
    if (idx > 0) {
      const prefix = raw.slice(0, idx);
      const path = raw.slice(idx + 1);
      if (BLOCKER_PREFIX_KEYS[prefix]) {
        return t('nodes.clusterModule.' + BLOCKER_PREFIX_KEYS[prefix], { path });
      }
    }
    return t('nodes.clusterModule.blockerUnknown', { reason: raw });
  }

  const STATE_KEYS = {
    not_installed: 'stateNotInstalled',
    installing: 'stateInstalling',
    uninstalling: 'stateUninstalling',
    installed: 'stateInstalled',
    partial: 'statePartial',
    partial_guided: 'statePartialGuided',
  };

  function causeText(cause) {
    return t('nodes.clusterModule.' + (causeKeys[cause] || 'causeUnknown'));
  }

  function checkLabel(id) {
    return t('nodes.clusterModule.' + (checkKeys[id] || 'checkOther'), { id });
  }

  function fmtDate(iso) {
    if (!iso) return '—';
    try {
      return new Date(iso).toLocaleString();
    } catch {
      return iso;
    }
  }

  // Commands that stop the units the install left running. Taken from
  // status.commands (the backend's exact text); when it did not send one
  // for a unit, the documented equivalent is built here.
  const unitCommands = $derived.by(() => {
    const cmds = Array.isArray(status?.commands) ? status.commands : [];
    return unitsActive.map((unit) => {
      const found = cmds.find(
        (c) => typeof c === 'string' && c.includes(unit) && /systemctl\s+disable/.test(c)
      );
      return found || `sudo systemctl disable --now ${unit}`;
    });
  });

  async function copyText(text) {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(t('nodes.clusterModule.copied'));
    } catch {
      toast.error(t('nodes.clusterModule.copyFailed'));
    }
  }

  function copyDiagnostics() {
    const s = status || {};
    const text = [
      `state: ${s.state || ''}`,
      `failed_step: ${s.failed_step || ''}`,
      `cause: ${s.cause || ''}`,
      `error: ${s.error || ''}`,
      `missing: ${(s.missing || []).join(', ')}`,
      `commands:\n${(s.commands || []).join('\n')}`,
    ].join('\n');
    copyText(text);
  }

  async function openPlan() {
    preflightLoading = true;
    plan = null;
    acceptExperimental = false;
    const linstor = installLinstor;
    try {
      const result = await api.preflightHAModule({ linstor });
      planLinstor = linstor;
      plan = result;
    } catch (e) {
      toast.error(e.message);
    } finally {
      preflightLoading = false;
    }
  }

  // --- polling: one request at a time (chained timeouts) ---
  function stopPolling() {
    pollActive = false;
    if (pollTimer) {
      clearTimeout(pollTimer);
      pollTimer = null;
    }
  }

  function scheduleTick() {
    if (!pollActive) return;
    pollTimer = setTimeout(tick, POLL_MS);
  }

  function finishOperation(finalState) {
    const op = activeOp;
    activeOp = '';
    installing = false;
    if (op === 'uninstall') {
      if (finalState === 'not_installed') toast.success(t('nodes.clusterModule.uninstalledOk'));
      else toast.error(t('nodes.clusterModule.uninstallFailedToast'));
    } else if (op === 'install') {
      if (finalState === 'installed') toast.success(t('nodes.clusterModule.installedOk'));
      else if (finalState === 'partial_guided')
        toast.info(t('nodes.clusterModule.installedGuidedToast'));
      else toast.error(t('nodes.clusterModule.installFailedToast'));
    }
  }

  async function tick() {
    pollTimer = null;
    if (!pollActive) return;
    try {
      const data = await api.getHAModule();
      if (!pollActive) return;
      pollErrors = 0;
      clusterModule.status = data.status;
      clusterModule.busy = !!data.busy;
      clusterModule.enabled = !!data.enabled;
      clusterModule.loaded = true;
      clusterModule.error = '';
      const st = data.status?.state;
      if (st === 'installing') activeOp = 'install';
      if (st === 'uninstalling') activeOp = 'uninstall';
      const settling = st === 'installing' || st === 'uninstalling';
      if (!data.busy && !settling) {
        stopPolling();
        finishOperation(st);
        return;
      }
    } catch {
      if (!pollActive) return;
      pollErrors++;
      if (pollErrors >= MAX_POLL_ERRORS) {
        stopPolling();
        pollFailed = true;
        installing = false;
        return;
      }
    }
    scheduleTick();
  }

  function startPolling() {
    stopPolling();
    pollErrors = 0;
    pollFailed = false;
    pollActive = true;
    scheduleTick();
  }

  function retryPolling() {
    startPolling();
    tick();
  }

  async function retryLoad() {
    await refreshClusterModule(true);
    const s = clusterModule.status?.state;
    if (s === 'installing' || s === 'uninstalling' || clusterModule.busy) startPolling();
  }

  // Any change to the opt-in after preflight drops the plan: it was
  // computed for a different package set.
  function onLinstorChange() {
    if (plan) plan = null;
  }

  async function confirmInstall() {
    if (!plan) return;
    if (!canInstall || (needsExperimentalAck && !acceptExperimental)) return;
    installing = true;
    activeOp = 'install';
    try {
      const opts = { linstor: planLinstor };
      if (needsExperimentalAck && acceptExperimental) opts.accept_experimental = true;
      await startClusterModuleInstall(opts);
      plan = null;
      toast.success(t('nodes.clusterModule.installStarted'));
      startPolling();
      await tick();
    } catch (e) {
      installing = false;
      activeOp = '';
      toast.error(e.message);
    }
  }

  async function runVerify() {
    verifying = true;
    try {
      const result = await verifyClusterModule();
      if (result.state === 'installed') toast.success(t('nodes.clusterModule.installedOk'));
    } catch (e) {
      toast.error(e.message);
    } finally {
      verifying = false;
    }
  }

  async function enable() {
    toggling = true;
    try {
      await enableClusterModule();
      toast.success(t('nodes.clusterModule.enabledOk'));
    } catch (e) {
      toast.error(e.message);
    } finally {
      toggling = false;
    }
  }

  async function disable() {
    toggling = true;
    try {
      await disableClusterModule();
      disableOpen = false;
      toast.success(t('nodes.clusterModule.disabledOk'));
    } catch (e) {
      toast.error(e.message);
    } finally {
      toggling = false;
    }
  }

  async function doUninstall() {
    uninstalling = true;
    uninstallBlockers = [];
    try {
      await uninstallClusterModule(purgeOnUninstall);
      uninstallOpen = false;
      activeOp = 'uninstall';
      toast.success(t('nodes.clusterModule.uninstallStarted'));
      startPolling();
      await tick();
    } catch (e) {
      // 409 uninstall_blocked carries the fail-closed reasons in the body
      // (ApiError.data). Keep the dialog open and list them.
      const blockers = Array.isArray(e?.data?.blockers) ? e.data.blockers : [];
      if (blockers.length) {
        uninstallBlockers = blockers;
        toast.error(t('nodes.clusterModule.uninstallBlockedTitle'));
      } else {
        toast.error(e.message);
      }
    } finally {
      uninstalling = false;
    }
  }

  onMount(() => {
    // Wait for the refresh: the store is stale right after a page reload,
    // so reading it synchronously never saw an in-flight install/uninstall.
    refreshClusterModule(true).then(() => {
      const s = clusterModule.status?.state;
      if (s === 'installing' || s === 'uninstalling' || clusterModule.busy) startPolling();
      // A cluster setup survives page reloads: reopen the wizard on it.
      if (clusterModule.enabled && s === 'installed') {
        Promise.resolve()
          .then(() => api.haClusterStatus())
          .then((cs) => {
            if (cs?.state === 'running') showWizard = true;
          })
          .catch(() => {});
      }
    });
  });

  onDestroy(stopPolling);
</script>

<Card class="space-y-3 border border-border bg-card/60 p-4">
  <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
    <div class="space-y-1">
      <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
        <h2 class="text-sm font-semibold text-foreground">{t('nodes.clusterModule.title')}</h2>
        {#if clusterModule.loaded}
          <span class="inline-flex items-center gap-2" data-testid="module-badges">
            <span
              class="inline-flex w-fit items-center rounded border px-2 py-0.5 text-[11px] font-medium {installed
                ? 'border-success/40 bg-success/15 text-success'
                : partial || partialGuided
                  ? 'border-warning/40 bg-warning/15 text-warning'
                  : running
                    ? 'border-primary/40 bg-primary/15 text-foreground'
                    : 'border-border bg-muted text-muted-foreground'}"
            >
              {t('nodes.clusterModule.' + (STATE_KEYS[state] || 'stateNotInstalled'))}
            </span>
            {#if clusterModule.enabled}
              <span class="text-[11px] text-success">{t('nodes.clusterModule.badgeOn')}</span>
            {:else}
              <span class="text-[11px] text-muted-foreground"
                >{t('nodes.clusterModule.badgeOff')}</span
              >
            {/if}
          </span>
        {/if}
      </div>
      <p class="text-xs text-muted-foreground">{t('nodes.clusterModule.description')}</p>
      <p class="text-[11px] text-muted-foreground">{t('nodes.clusterModule.localOnly')}</p>
    </div>
    {#if clusterModule.loaded}
      <div class="flex flex-wrap gap-2">
        {#if clusterModule.enabled}
          <Button
            size="sm"
            variant="outline"
            class="min-h-11"
            onclick={() => (disableOpen = true)}
            disabled={toggling || running}
          >
            {#if toggling}<Spinner size="sm" />{/if}
            {t('nodes.clusterModule.disable')}
          </Button>
        {:else if installed}
          <Button size="sm" class="min-h-11" onclick={enable} disabled={toggling || running}>
            {#if toggling}<Spinner size="sm" />{/if}
            {t('nodes.clusterModule.enable')}
          </Button>
        {:else}
          <Button
            size="sm"
            class="min-h-11"
            onclick={openPlan}
            disabled={preflightLoading || running}
            aria-busy={preflightLoading}
          >
            {#if preflightLoading}<Spinner size="sm" />{/if}
            {preflightLoading
              ? t('nodes.clusterModule.preflightRunning')
              : partialGuided
                ? t('nodes.clusterModule.showManualSteps')
                : partial || status?.failed_step
                  ? t('nodes.clusterModule.retry')
                  : t('nodes.clusterModule.install')}
          </Button>
        {/if}
        <Button
          size="sm"
          variant="outline"
          class="min-h-11"
          onclick={runVerify}
          disabled={verifying || running}
        >
          {#if verifying}<Spinner size="sm" />{/if}
          {partialGuided ? t('nodes.clusterModule.verifyDone') : t('nodes.clusterModule.verify')}
        </Button>
        {#if partial && clusterModule.enabled}
          <Button
            size="sm"
            variant="outline"
            class="min-h-11"
            onclick={openPlan}
            disabled={preflightLoading || running}
          >
            {#if preflightLoading}<Spinner size="sm" />{/if}
            {t('nodes.clusterModule.retry')}
          </Button>
        {/if}
        {#if installed || partial || partialGuided}
          <Button
            size="sm"
            variant="outline"
            class="min-h-11"
            onclick={() => {
              purgeOnUninstall = false;
              uninstallOpen = true;
            }}
            disabled={running}
          >
            {t('nodes.clusterModule.uninstall')}
          </Button>
        {/if}
      </div>
    {/if}
  </div>

  {#if loadFailed}
    <Alert variant="error">
      <p class="font-medium">{t('nodes.clusterModule.loadErrorTitle')}</p>
      <p>{t('nodes.clusterModule.loadErrorHint', { error: clusterModule.error })}</p>
      <Button
        size="sm"
        variant="outline"
        class="mt-2 min-h-11"
        onclick={retryLoad}
        disabled={clusterModule.loading}
      >
        {#if clusterModule.loading}<Spinner size="sm" />{/if}
        {t('nodes.clusterModule.retryLoad')}
      </Button>
    </Alert>
  {:else if !clusterModule.loaded}
    <p class="text-xs text-muted-foreground" role="status">
      {t('nodes.clusterModule.loadingStatus')}
    </p>
  {:else}
    {#if pollFailed}
      <Alert variant="warning">
        <p class="font-medium">{t('nodes.clusterModule.pollFailedTitle')}</p>
        <p>{t('nodes.clusterModule.pollFailedBody')}</p>
        <Button size="sm" variant="outline" class="mt-2 min-h-11" onclick={retryPolling}>
          {t('nodes.clusterModule.retryLoad')}
        </Button>
      </Alert>
    {/if}

    {#if !installed && !running}
      <p class="text-xs text-muted-foreground">{t('nodes.clusterModule.easySummary')}</p>
    {/if}

    {#if !installed}
      <div>
        <Button
          size="sm"
          variant="outline"
          class="min-h-9 gap-1.5 px-3 text-xs"
          aria-expanded={advanced}
          onclick={() => (advanced = !advanced)}
        >
          {advanced
            ? t('nodes.clusterModule.hideAdvancedOptions')
            : t('nodes.clusterModule.advancedOptions')}
          {#if advanced}<ChevronUp aria-hidden="true" />{:else}<ChevronDown
              aria-hidden="true"
            />{/if}
        </Button>
      </div>
    {/if}

    {#if advanced && !installed}
      <div class="space-y-3 rounded border border-border bg-background/70 p-3">
        <p class="text-[11px] text-muted-foreground">{t('nodes.clusterModule.advancedIntro')}</p>
        <div class="space-y-1">
          <p class="text-xs font-medium">{t('nodes.clusterModule.layerBase')}</p>
          <p class="text-[11px] text-muted-foreground">{t('nodes.clusterModule.layerBaseDesc')}</p>
          <ul class="list-disc space-y-0.5 pl-5 text-[11px] text-muted-foreground">
            <li>{t('nodes.clusterModule.compPacemaker')}</li>
            <li>{t('nodes.clusterModule.compCorosync')}</li>
            <li>{t('nodes.clusterModule.compPcs')}</li>
            <li>{t('nodes.clusterModule.compSbd')}</li>
            <li>{t('nodes.clusterModule.compFenceAgents')}</li>
            <li>{t('nodes.clusterModule.compResourceAgents')}</li>
            <li>{t('nodes.clusterModule.compDrbdUtils')}</li>
          </ul>
        </div>
        <div class="space-y-1">
          <p class="text-xs font-medium">{t('nodes.clusterModule.layerStorage')}</p>
          <label class="flex items-start gap-2 text-[11px] text-muted-foreground">
            <input
              type="checkbox"
              bind:checked={installLinstor}
              onchange={onLinstorChange}
              disabled={!!plan || preflightLoading || running}
              class="mt-0.5"
            />
            <span>{t('nodes.clusterModule.linstorOptIn')}</span>
          </label>
          {#if plan}
            <p class="text-[11px] text-muted-foreground">
              {t('nodes.clusterModule.optionsLocked')}
            </p>
          {/if}
        </div>
        <p class="text-[11px] text-muted-foreground">{t('nodes.clusterModule.fencingRule')}</p>
      </div>
    {/if}
  {/if}

  {#if clusterModule.loaded && !clusterModule.enabled}
    <p class="text-[11px] text-muted-foreground" data-testid="module-disabled-notice">
      {t('nodes.clusterModule.disabledNotice')}
    </p>
  {/if}

  {#if unitsActive.length}
    <Alert variant="warning">
      <p class="font-medium">
        {t('nodes.clusterModule.unitsActiveTitle', { units: unitsActive.join(', ') })}
      </p>
      <p>{t('nodes.clusterModule.unitsActiveBody')}</p>
      <p class="mt-1 text-[11px] font-medium">{t('nodes.clusterModule.unitsActiveCommands')}</p>
      <pre
        class="rounded bg-background p-2 text-[11px] leading-relaxed break-all whitespace-pre-wrap text-foreground">{unitCommands.join(
          '\n'
        )}</pre>
      <Button
        size="sm"
        variant="outline"
        class="mt-1 min-h-11"
        onclick={() => copyText(unitCommands.join('\n'))}
      >
        {t('nodes.clusterModule.copyUnitCommands')}
      </Button>
    </Alert>
  {/if}

  {#if unknownUnits.length}
    <Alert variant="info">
      <p class="font-medium">
        {t('nodes.clusterModule.unknownUnitsTitle', { units: unknownUnits.join(', ') })}
      </p>
      <p>{t('nodes.clusterModule.unknownUnitsBody')}</p>
      <pre
        class="mt-1 rounded bg-background p-2 text-[11px] leading-relaxed break-all whitespace-pre-wrap text-foreground">{unknownUnits
          .map((u) => `systemctl status ${u}`)
          .join('\n')}</pre>
    </Alert>
  {/if}

  {#if partialGuided && !plan}
    <Alert variant="warning">
      <p class="font-medium" data-testid="guided-pending">
        {t('nodes.clusterModule.guidedPendingTitle', {
          packages: (guidedMissing.length ? guidedMissing : status?.missing || []).join(', '),
        })}
      </p>
      <p>{t('nodes.clusterModule.guidedPendingBody')}</p>
    </Alert>
  {/if}

  {#if uninstallBlockers.length && !uninstallOpen}
    <Alert variant="warning">
      <p class="font-medium">{t('nodes.clusterModule.uninstallBlockedTitle')}</p>
      <ul class="mt-1 list-disc space-y-0.5 pl-5">
        {#each uninstallBlockers as blocker (blocker)}
          <li class="break-all">{blockerText(blocker)}</li>
        {/each}
      </ul>
    </Alert>
  {/if}

  {#if plan}
    <div class="space-y-3 rounded border border-border bg-background/70 p-3">
      <div class="flex items-center justify-between gap-2">
        <h3 class="text-sm font-medium">{t('nodes.clusterModule.planTitle')}</h3>
        <span class="text-[11px] text-muted-foreground">
          {t('nodes.clusterModule.planDistro')}: {plan.preflight.distro}
          {plan.preflight.version}
        </span>
      </div>

      {#if hasGuide}
        <p class="text-xs text-muted-foreground" data-testid="plan-split">
          {t('nodes.clusterModule.planSummarySplit', {
            install: plan.packages.filter((p) => !p.installed && !p.guided).length,
            manual: plan.packages.filter((p) => !p.installed && p.guided).length,
            installed: planInstalledCount,
          })}
        </p>
      {:else if !planGuided}
        <p class="text-xs text-muted-foreground">
          {t('nodes.clusterModule.planSummary', {
            missing: planMissingCount,
            installed: planInstalledCount,
          })}
        </p>
      {/if}

      {#if showTierAlert}
        <!-- One alert per tier. Guided is a "do it yourself" notice, not a failure. -->
        <Alert variant={tierSuffix === 'Unsupported' ? 'error' : 'warning'}>
          <p class="font-medium">
            {t('nodes.clusterModule.tierLabel')}:
            {t('nodes.clusterModule.tier' + tierSuffix)}
          </p>
          <p>
            {t('nodes.clusterModule.tier' + tierSuffix + 'Desc' + (planGuided ? planFamily : ''))}
          </p>
        </Alert>
      {/if}

      {#if needsExperimentalAck}
        <label class="flex items-start gap-2 text-[11px]">
          <input type="checkbox" bind:checked={acceptExperimental} class="mt-0.5" />
          <span>{t('nodes.clusterModule.acceptExperimental')}</span>
        </label>
      {/if}

      {#if hasGuide}
        <HaPlanGuide {plan} {advanced} onCopy={copyText} />
      {/if}

      <!-- Failed or warning checks are security/health information: always
           shown, in easy mode too. The full list is an advanced detail. -->
      {#if advanced || attentionChecks.length}
        <div class="space-y-1">
          <p class="text-xs font-medium">
            {advanced
              ? t('nodes.clusterModule.planChecks')
              : t('nodes.clusterModule.checksAttentionTitle')}
          </p>
          <ul class="space-y-1 text-[11px]">
            {#each advanced ? plan.preflight.checks : attentionChecks as check (`${check.id}`)}
              <li class="flex flex-wrap items-baseline gap-2">
                <span
                  class={check.ok
                    ? 'text-success'
                    : check.severity === 'warning'
                      ? 'text-warning'
                      : 'text-destructive'}
                >
                  <span aria-hidden="true"
                    >{check.ok ? '✓' : check.severity === 'warning' ? '!' : '✕'}</span
                  >
                  <span class="sr-only">
                    {check.ok
                      ? t('nodes.clusterModule.srOk')
                      : check.severity === 'warning'
                        ? t('nodes.clusterModule.srWarning')
                        : t('nodes.clusterModule.srFailed')}
                  </span>
                </span>
                <span class="font-medium">{checkLabel(check.id)}</span>
                {#if check.detail}<span class="text-muted-foreground">{check.detail}</span>{/if}
                {#if !check.ok && check.hint}<span class="text-muted-foreground">{check.hint}</span
                  >{/if}
              </li>
            {/each}
          </ul>
        </div>
      {/if}

      {#if advanced}
        <div class="overflow-x-auto">
          <p class="text-xs font-medium">{t('nodes.clusterModule.advancedPackagesTitle')}</p>
          <table class="w-full text-left text-[11px]">
            <thead class="text-muted-foreground">
              <tr>
                <th class="py-1 pr-3 font-medium">{t('nodes.clusterModule.planPackages')}</th>
                <th class="py-1 pr-3 font-medium">{t('nodes.clusterModule.colLayer')}</th>
                <th class="py-1 pr-3 font-medium">{t('nodes.clusterModule.planOrigin')}</th>
                <th class="py-1 pr-3 font-medium">{t('nodes.clusterModule.colWho')}</th>
                <th class="py-1 font-medium">{t('nodes.clusterModule.colState')}</th>
              </tr>
            </thead>
            <tbody>
              {#each plan.packages as pkg (`${pkg.name}`)}
                <tr class="border-t border-border/60">
                  <td class="py-1 pr-3 font-mono">
                    {pkg.name}{#if pkg.logical}<span class="text-muted-foreground">
                        ({pkg.logical})</span
                      >{/if}
                  </td>
                  <td class="py-1 pr-3 text-muted-foreground">{pkg.layer || '—'}</td>
                  <td class="py-1 pr-3 text-muted-foreground">{pkg.origin}</td>
                  <td class="py-1 pr-3 text-muted-foreground">
                    {pkg.guided
                      ? t('nodes.clusterModule.whoYou')
                      : t('nodes.clusterModule.whoWebkvm')}
                  </td>
                  <td class="py-1 text-muted-foreground">
                    {pkg.installed
                      ? t('nodes.clusterModule.planInstalled')
                      : t('nodes.clusterModule.planMissing')}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}

      <p class="text-[11px] text-muted-foreground">{t('nodes.clusterModule.planServices')}</p>

      {#if planLinstor}
        <Alert variant="info">{t('nodes.clusterModule.linstorOrigin')}</Alert>
      {/if}

      {#if !plan.preflight.ok}
        <Alert variant="error">{t('nodes.clusterModule.preflightBlocked')}</Alert>
      {/if}
      {#if !hasGuide && (advanced || planGuided || !plan.preflight.ok) && plan.manual_commands?.length}
        <div class="space-y-1">
          <p class="text-[11px] font-medium">{t('nodes.clusterModule.manualCommands')}</p>
          <pre
            class="rounded bg-background p-2 text-[11px] leading-relaxed break-all whitespace-pre-wrap">{plan.manual_commands.join(
              '\n'
            )}</pre>
          <Button
            size="sm"
            variant="outline"
            class="min-h-11"
            onclick={() => copyText(plan.manual_commands.join('\n'))}
          >
            {t('nodes.clusterModule.copyCommands')}
          </Button>
        </div>
      {/if}

      <div class="flex flex-wrap gap-2">
        {#if canInstall}
          <Button
            size="sm"
            class="min-h-11"
            onclick={confirmInstall}
            disabled={!plan.preflight.ok ||
              installing ||
              (needsExperimentalAck && !acceptExperimental)}
          >
            {#if installing}<Spinner size="sm" />{/if}
            {t('nodes.clusterModule.confirmInstall')}
          </Button>
        {/if}
        <Button
          size="sm"
          variant="outline"
          class="min-h-11"
          onclick={() => (plan = null)}
          disabled={installing}
        >
          {t('nodes.clusterModule.cancel')}
        </Button>
      </div>
    </div>
  {/if}

  {#if status?.failed_step}
    <Alert variant="error">
      <p class="font-medium">
        {t('nodes.clusterModule.failedStep', { step: stepLabel(status.failed_step) })}
      </p>
      {#if status.cause}<p>{causeText(status.cause)}</p>{/if}
      {#if status.missing?.length}
        <p>{t('nodes.clusterModule.missingPackages', { packages: status.missing.join(', ') })}</p>
      {/if}
      <p>{t('nodes.clusterModule.nothingChanged')}</p>
      <div class="mt-2 flex flex-wrap gap-2">
        <Button
          size="sm"
          class="min-h-11"
          onclick={openPlan}
          disabled={preflightLoading || running}
        >
          {#if preflightLoading}<Spinner size="sm" />{/if}
          {t('nodes.clusterModule.retry')}
        </Button>
        <Button size="sm" variant="outline" class="min-h-11" onclick={copyDiagnostics}>
          {t('nodes.clusterModule.copyDiagnostics')}
        </Button>
      </div>
    </Alert>
    {#if status.error}
      <div class="space-y-1">
        <p class="text-[11px] font-medium">{t('nodes.clusterModule.errorExcerpt')}</p>
        <pre
          class="max-h-40 overflow-auto rounded bg-background p-2 text-[11px] leading-relaxed whitespace-pre-wrap">{status.error}</pre>
        <Button size="sm" variant="outline" class="min-h-11" onclick={() => copyText(status.error)}>
          {t('nodes.clusterModule.copyError')}
        </Button>
      </div>
    {/if}
    {#if status.commands?.length}
      <div class="space-y-1">
        <p class="text-[11px] font-medium">{t('nodes.clusterModule.manualCommands')}</p>
        <pre
          class="rounded bg-background p-2 text-[11px] leading-relaxed break-all whitespace-pre-wrap">{status.commands.join(
            '\n'
          )}</pre>
        <Button
          size="sm"
          variant="outline"
          class="min-h-11"
          onclick={() => copyText(status.commands.join('\n'))}
        >
          {t('nodes.clusterModule.copyCommands')}
        </Button>
      </div>
    {/if}
  {/if}

  {#if visibleSteps.length && (running || partial || partialGuided || !!status?.failed_step)}
    <div class="space-y-1">
      <p class="text-xs font-medium">{t('nodes.clusterModule.steps')}</p>
      <ul class="space-y-0.5 text-[11px]">
        {#each visibleSteps as step (`${step.id}`)}
          <li class="flex flex-wrap items-baseline gap-2">
            <span
              class={step.state === 'done'
                ? 'text-success'
                : step.state === 'failed'
                  ? 'text-destructive'
                  : step.state === 'skipped'
                    ? 'text-muted-foreground'
                    : 'text-foreground'}
            >
              <span aria-hidden="true">
                {step.state === 'done'
                  ? '✓'
                  : step.state === 'failed'
                    ? '✕'
                    : step.state === 'skipped'
                      ? '–'
                      : '…'}
              </span>
              <span class="sr-only">
                {step.state === 'done'
                  ? t('nodes.clusterModule.srStepDone')
                  : step.state === 'failed'
                    ? t('nodes.clusterModule.srStepFailed')
                    : step.state === 'skipped'
                      ? t('nodes.clusterModule.srStepSkipped')
                      : t('nodes.clusterModule.srStepRunning')}
              </span>
            </span>
            <span class="font-medium">{stepLabel(step.id)}</span>
            {#if step.detail}<span class="text-muted-foreground">{step.detail}</span>{/if}
          </li>
        {/each}
      </ul>
    </div>
  {/if}

  {#if running || status?.log?.length}
    <details class="rounded border border-border bg-background/70 p-2" open={running}>
      <summary class="cursor-pointer text-xs font-medium"
        >{t('nodes.clusterModule.installLog')}</summary
      >
      <pre class="mt-2 max-h-56 overflow-auto text-[11px] leading-relaxed whitespace-pre-wrap">{(
          status?.log || []
        ).join('\n') || t('nodes.clusterModule.noLog')}</pre>
    </details>
  {/if}

  {#if installed && clusterModule.enabled}
    <div class="rounded border border-border bg-background/70 p-3">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div>
          <p class="text-sm font-medium">{t('nodes.clusterModule.clusterWizard')}</p>
          <p class="text-[11px] text-muted-foreground">
            {t('nodes.clusterModule.clusterWizardHint')}
          </p>
        </div>
        <!-- The wizard has its own Close button; a second one here was
             confusing (8.29 defect 5). -->
        {#if !showWizard}
          <Button size="sm" variant="outline" class="min-h-11" onclick={() => (showWizard = true)}>
            {t('nodes.cw.open')}
          </Button>
        {/if}
      </div>
    </div>
    {#if showWizard}
      <ClusterWizard onclose={() => (showWizard = false)} {onCheckReadiness} />
    {/if}
  {/if}

  {#if status?.updated_at}
    <p class="text-[11px] text-muted-foreground">
      {t('nodes.clusterModule.lastChecked')}: {fmtDate(status.updated_at)}
    </p>
  {/if}
</Card>

<ConfirmDialog
  bind:open={disableOpen}
  title={t('nodes.clusterModule.disableConfirmTitle')}
  description={t('nodes.clusterModule.disableConfirmBody')}
  confirmLabel={t('nodes.clusterModule.disable')}
  variant="destructive"
  loading={toggling}
  onConfirm={disable}
/>

<ConfirmDialog
  bind:open={uninstallOpen}
  title={t('nodes.clusterModule.uninstallConfirmTitle')}
  description={t('nodes.clusterModule.uninstallConfirmBody')}
  confirmLabel={t('nodes.clusterModule.uninstall')}
  variant="destructive"
  loading={uninstalling}
  onConfirm={doUninstall}
>
  <label class="flex items-start gap-2 text-[11px]">
    <input type="checkbox" bind:checked={purgeOnUninstall} class="mt-0.5" />
    <span>{t('nodes.clusterModule.purgeOption')}</span>
  </label>
  {#if uninstallBlockers.length}
    <Alert variant="error">
      <p class="font-medium">{t('nodes.clusterModule.uninstallBlockedTitle')}</p>
      <p class="text-[11px]">{t('nodes.clusterModule.uninstallBlockedHint')}</p>
      <ul class="mt-1 list-disc space-y-0.5 pl-5 text-[11px]">
        {#each uninstallBlockers as blocker (blocker)}
          <li class="break-all">{blockerText(blocker)}</li>
        {/each}
      </ul>
    </Alert>
  {/if}
</ConfirmDialog>
