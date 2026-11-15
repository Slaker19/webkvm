<script>
  /**
   * ClusterAccessCard — managed SSH access to the remote KVM nodes.
   * Data comes from GET /api/cluster/ssh (passed in as `overview`); every
   * mutation goes through the api helpers and then asks the parent to
   * refresh. WebKVM never pushes the key: the operator runs the authorize
   * command on each node.
   */
  import { tick } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Card } from '$lib/components/ui/card';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import {
    accessState,
    accessStateLabel,
    currentStep,
    codeLabel,
    sshErrorText,
    shortFingerprint,
    withProbeAccess,
  } from '$lib/utils/clusterAccess.js';
  import { withoutSelfDuplicates } from '$lib/utils/nodes.js';

  // probeNodes: the GET /nodes list; its probe verdict (access) is overlaid
  // so this card never contradicts the node cards.
  let {
    overview = null,
    loading = false,
    error = '',
    onRefresh = () => {},
    probeNodes = [],
  } = $props();

  let mode = $state('easy');
  const advanced = $derived(mode === 'advanced');
  let open = $state({});
  // Per node UI state: { authorize, hostkey, verified, ack, busy, error, test }
  let ui = $state({});
  let keyBusy = $state(false);
  let keyError = $state('');
  let rotation = $state({ busy: '', error: '', command: '', line: '', force: false });
  // Result of POST /cluster/ssh/rotate/test for the CURRENT pending key.
  let rotTest = $state(null);
  let rollbackOpen = $state(false);
  let replaceFor = $state(null);
  let commitOpen = $state(false);
  let abortOpen = $state(false);

  // The self-duplicate entry (is_self: this same server registered again)
  // is never a node to set up SSH access to.
  const nodes = $derived(
    withoutSelfDuplicates(overview?.nodes || []).map((n) =>
      withProbeAccess(
        n,
        (probeNodes || []).find((p) => p.id === n.id)
      )
    )
  );
  const transportBroken = $derived(
    nodes.some((n) => n.last_test?.code === 'transport_unsupported') ||
      Object.values(ui).some((u) => u?.test?.code === 'transport_unsupported')
  );

  const st = (id) => ui[id] || {};
  function patch(id, v) {
    ui = { ...ui, [id]: { ...(ui[id] || {}), ...v } };
  }

  export async function openNode(id) {
    open = { ...open, [id]: true };
    loadAuthorize(id);
    await tick();
    document
      .getElementById(`access-node-${id}`)
      ?.scrollIntoView?.({ block: 'center', behavior: 'auto' });
  }

  async function loadAuthorize(id) {
    if (!overview?.configured || st(id).authorize) return;
    try {
      patch(id, { authorize: await api.getNodeSSHAuthorize(id) });
    } catch (e) {
      patch(id, { error: sshErrorText(t, e) });
    }
  }

  // The per-node state is dropped after a key switch; panels left open need
  // the authorize command of the key that is now current.
  function reloadOpenAuthorize() {
    for (const id of Object.keys(open)) if (open[id]) loadAuthorize(id);
  }

  function toggle(n) {
    const next = !open[n.id];
    open = { ...open, [n.id]: next };
    if (next) loadAuthorize(n.id);
  }

  async function copy(text) {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(t('clusterAccess.copied'));
    } catch {
      toast.error(t('clusterAccess.copyFailed'));
    }
  }

  async function generateKey() {
    keyBusy = true;
    keyError = '';
    try {
      await api.generateClusterSSHKey();
      toast.success(t('clusterAccess.generated'));
      await onRefresh();
    } catch (e) {
      keyError = sshErrorText(t, e);
    } finally {
      keyBusy = false;
    }
  }

  async function checkHostKey(n) {
    patch(n.id, { busy: 'hostkey', error: '', verified: false, hostkey: null });
    try {
      patch(n.id, { hostkey: await api.captureNodeHostKey(n.id) });
    } catch (e) {
      patch(n.id, { error: sshErrorText(t, e) });
    } finally {
      patch(n.id, { busy: '' });
    }
  }

  async function pin(n, replace = false) {
    const hk = st(n.id).hostkey;
    if (!hk?.fingerprint || !st(n.id).verified) return;
    patch(n.id, { busy: 'pin', error: '' });
    try {
      await api.pinNodeHostKey(n.id, hk.fingerprint, replace);
      toast.success(t('clusterAccess.pinned'));
      patch(n.id, { hostkey: null, verified: false });
      replaceFor = null;
      await onRefresh();
    } catch (e) {
      patch(n.id, { error: sshErrorText(t, e) });
      replaceFor = null;
    } finally {
      patch(n.id, { busy: '' });
    }
  }

  async function testNode(n) {
    patch(n.id, { busy: 'test', error: '' });
    try {
      const res = await api.testNodeSSH(n.id);
      patch(n.id, { test: res });
      await onRefresh();
      return res;
    } catch (e) {
      patch(n.id, { error: sshErrorText(t, e) });
    } finally {
      patch(n.id, { busy: '' });
    }
  }

  // Nodes a rotation affects: the enabled ones (the overview adds `enabled`
  // while a rotation is pending).
  const rotNodes = $derived(nodes.filter((n) => n.enabled !== false));
  const rotRow = (n) => rotTest?.nodes?.find((x) => x.id === n.id);
  const rotVerified = (n) => {
    const r = rotRow(n);
    return r ? r.ok === true : n.pending_verified === true;
  };
  const rotCode = (n) => rotRow(n)?.code || (rotVerified(n) ? '' : 'not_tested');
  const rotHint = (code) => {
    const key = `clusterAccess.hints.${code}`;
    const out = code ? t(key) : key;
    return out === key ? '' : out;
  };
  const rotFailing = $derived(rotNodes.filter((n) => !rotVerified(n)));
  const rotAllPassed = $derived(rotFailing.length === 0);
  // EASY: only a fully verified rotation can be switched. ADVANCED may force.
  const canCommit = $derived(rotAllPassed || (advanced && rotation.force));

  async function testRotation() {
    rotation = { ...rotation, busy: 'test', error: '' };
    try {
      rotTest = await api.testClusterSSHRotation();
      const ok = (rotTest.nodes || []).filter((x) => x.ok).length;
      toast.info(
        t('clusterAccess.rotationTestAllDone', { ok, total: (rotTest.nodes || []).length })
      );
      await onRefresh();
    } catch (e) {
      rotation = { ...rotation, error: sshErrorText(t, e) };
    } finally {
      rotation = { ...rotation, busy: '' };
    }
  }

  async function startRotation() {
    rotation = { ...rotation, busy: 'start', error: '' };
    try {
      const res = await api.startClusterSSHRotation();
      rotTest = null;
      rotation = {
        ...rotation,
        busy: '',
        command: res?.pending?.command || '',
        line: res?.pending?.authorized_keys_line || '',
        force: false,
      };
      toast.success(t('clusterAccess.rotationStarted'));
      await onRefresh();
    } catch (e) {
      rotation = { ...rotation, busy: '', error: sshErrorText(t, e) };
    }
  }

  async function finishRotation(kind) {
    rotation = { ...rotation, busy: kind, error: '' };
    try {
      if (kind === 'commit') {
        await api.commitClusterSSHRotation({ force: !rotAllPassed && rotation.force });
      } else await api.abortClusterSSHRotation();
      toast.success(
        t(kind === 'commit' ? 'clusterAccess.rotationCommitted' : 'clusterAccess.rotationAborted')
      );
      rotation = { busy: '', error: '', command: '', line: '', force: false };
      rotTest = null;
      ui = {};
      await onRefresh();
      reloadOpenAuthorize();
    } catch (e) {
      if (e?.code === 'rotation_unverified' && Array.isArray(e.data?.failing)) {
        // The server's verdict wins over what this page believed.
        const failing = new Map(e.data.failing.map((f) => [f.id, f.code]));
        rotTest = {
          ok: false,
          nodes: rotNodes.map((n) => ({
            id: n.id,
            name: n.name,
            ok: !failing.has(n.id),
            code: failing.get(n.id) || '',
          })),
        };
      }
      rotation = { ...rotation, busy: '', error: sshErrorText(t, e) };
    } finally {
      commitOpen = false;
      abortOpen = false;
    }
  }

  async function rollbackRotation() {
    rotation = { ...rotation, busy: 'rollback', error: '' };
    try {
      await api.rollbackClusterSSHRotation();
      toast.success(t('clusterAccess.rotationRolledBack'));
      rotTest = null;
      ui = {};
      await onRefresh();
      reloadOpenAuthorize();
    } catch (e) {
      rotation = { ...rotation, error: sshErrorText(t, e) };
    } finally {
      rotation = { ...rotation, busy: '' };
      rollbackOpen = false;
    }
  }

  const stateLabel = (s) => accessStateLabel(t, s);
  const stateClass = (s) =>
    s === 'ready'
      ? 'bg-success/10 text-success'
      : s === 'changed'
        ? 'bg-destructive/10 text-destructive'
        : s === 'unverified'
          ? 'bg-muted text-foreground'
          : 'bg-warning/10 text-warning';
  const nextText = (step) =>
    ({
      key: 'clusterAccess.nextGenerate',
      authorize: 'clusterAccess.nextAuthorize',
      hostkey: 'clusterAccess.nextHostKey',
      test: 'clusterAccess.nextTest',
      done: 'clusterAccess.nextDone',
    })[step];
  // Secondary info only: the time of the last explicit test, if any.
  const lastTestedText = (n) => {
    const at = n?.last_test?.at ? new Date(n.last_test.at) : null;
    return at && !Number.isNaN(at.getTime()) ? at.toLocaleString() : '';
  };
  const stepOf = (n) => {
    const base = currentStep(overview, n);
    return base === 'authorize' && st(n.id).ack ? 'hostkey' : base;
  };
  const rank = { key: 0, authorize: 1, hostkey: 2, test: 3, done: 4 };
  const stepStatus = (cur, idx) =>
    rank[cur] > idx ? 'stepDone' : rank[cur] === idx ? 'stepCurrent' : 'stepPending';
</script>

{#snippet copyable(label, text)}
  <div class="space-y-1">
    <div class="text-xs text-muted-foreground">{label}</div>
    <div class="flex items-start gap-2">
      <pre
        class="flex-1 min-w-0 overflow-x-auto rounded-md border border-border bg-muted p-2 text-xs font-mono whitespace-pre-wrap break-all">{text}</pre>
      <Button size="sm" variant="outline" class="min-h-11 shrink-0" onclick={() => copy(text)}>
        {t('clusterAccess.copy')}
      </Button>
    </div>
  </div>
{/snippet}

<Card class="p-4 space-y-3 border border-border bg-card/60" data-testid="cluster-access-card">
  <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
    <div class="space-y-1">
      <h2 class="text-sm font-semibold text-foreground">{t('clusterAccess.title')}</h2>
      <p class="text-xs text-muted-foreground">{t('clusterAccess.subtitle')}</p>
    </div>
    <div class="flex flex-wrap gap-2 shrink-0">
      <div class="flex gap-2" role="group" aria-label={t('clusterAccess.modeLabel')}>
        <Button
          size="sm"
          variant={mode === 'easy' ? 'default' : 'outline'}
          class="min-h-11"
          aria-pressed={mode === 'easy'}
          onclick={() => (mode = 'easy')}>{t('clusterAccess.modeEasy')}</Button
        >
        <Button
          size="sm"
          variant={mode === 'advanced' ? 'default' : 'outline'}
          class="min-h-11"
          aria-pressed={mode === 'advanced'}
          onclick={() => (mode = 'advanced')}>{t('clusterAccess.modeAdvanced')}</Button
        >
      </div>
      <Button
        size="sm"
        variant="outline"
        class="min-h-11"
        onclick={() => onRefresh()}
        disabled={loading}
      >
        {#if loading}<Spinner size="sm" />{/if}
        {t('clusterAccess.refresh')}
      </Button>
    </div>
  </div>

  {#if error}
    <Alert variant="error">
      <p>{t('clusterAccess.loadError', { error })}</p>
      <Button size="sm" variant="outline" class="mt-2 min-h-11" onclick={() => onRefresh()}>
        {t('clusterAccess.retry')}
      </Button>
    </Alert>
  {:else if !overview}
    <p class="text-xs text-muted-foreground" role="status">{t('clusterAccess.loading')}</p>
  {:else}
    {#if overview?.transport_fallback === 'libssh2_unsupported' && !transportBroken}
      <p class="text-xs text-muted-foreground" data-testid="transport-fallback">
        {t('clusterAccess.transportFallback')}
      </p>
    {/if}
    {#if transportBroken}
      <Alert variant="warning" title={t('clusterAccess.transportWarningTitle')}>
        {t('clusterAccess.transportWarningBody')}
      </Alert>
    {/if}

    {#if !overview.configured}
      <div class="space-y-2 rounded-md border border-border p-3">
        <h3 class="text-sm font-semibold">{t('clusterAccess.noKeyTitle')}</h3>
        <p class="text-xs text-muted-foreground">{t('clusterAccess.noKeyBody')}</p>
        {#if keyError}<Alert variant="error">{keyError}</Alert>{/if}
        <Button class="min-h-11" onclick={generateKey} disabled={keyBusy}>
          {#if keyBusy}<Spinner size="sm" color="text-white" />{/if}
          {keyBusy ? t('clusterAccess.generating') : t('clusterAccess.generate')}
        </Button>
      </div>
    {:else}
      <div class="text-xs text-muted-foreground space-y-1">
        <div>
          <span class="font-semibold text-foreground">{t('clusterAccess.keyTitle')}</span>
          · {t('clusterAccess.keyFingerprint')}:
          <code class="font-mono"
            >{advanced ? overview.fingerprint : shortFingerprint(overview.fingerprint)}</code
          >
        </div>
        {#if advanced}
          <div>
            {t('clusterAccess.keyAlgorithm')}: {overview.algorithm} · {t(
              'clusterAccess.keyCreated'
            )}:
            {overview.created_at}
          </div>
          <div>
            {t('clusterAccess.transportLabel')}:
            <code class="font-mono">{overview.transport}</code>
            ({overview.transport === 'ssh'
              ? t('clusterAccess.transportSsh')
              : overview.transport === 'libssh'
                ? t('clusterAccess.transportLibssh')
                : t('clusterAccess.transportLibssh2')})
          </div>
          {@render copyable(t('clusterAccess.publicKey'), overview.public_key)}
        {/if}
      </div>
    {/if}

    {#if nodes.length === 0}
      <p class="text-xs text-muted-foreground">{t('clusterAccess.noNodes')}</p>
    {/if}

    <ul class="space-y-3">
      {#each nodes as n (n.id)}
        {@const s = accessState(n)}
        {@const step = stepOf(n)}
        {@const u = st(n.id)}
        <li id="access-node-{n.id}" class="rounded-md border border-border p-3 space-y-3">
          <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <span class="font-medium text-sm">{n.name}</span>
                <span
                  class="text-[11px] px-2 py-0.5 rounded {stateClass(s)}"
                  data-testid="access-state-{n.id}">{stateLabel(s)}</span
                >
              </div>
              <p class="text-xs font-mono text-muted-foreground truncate">
                {n.user}@{n.host}{advanced ? `:${n.port}` : ''}
              </p>
              <p class="text-xs text-muted-foreground">{t(nextText(step))}</p>
              {#if s === 'ready' && n.access === 'ok'}
                <p class="text-xs text-muted-foreground" data-testid="access-secondary-{n.id}">
                  {t('clusterAccess.probeOk')}
                  {#if lastTestedText(n)}
                    {t('clusterAccess.lastTestedAt', { time: lastTestedText(n) })}
                  {/if}
                </p>
              {/if}
            </div>
            <div class="flex flex-wrap gap-2">
              <Button
                size="sm"
                class="min-h-11"
                variant={step === 'done' ? 'outline' : 'default'}
                aria-expanded={!!open[n.id]}
                aria-controls="access-panel-{n.id}"
                disabled={!overview.configured}
                onclick={() => toggle(n)}
              >
                {open[n.id] ? t('clusterAccess.hideSteps') : t('clusterAccess.setUp')}
              </Button>
            </div>
          </div>

          {#if open[n.id] && overview.configured}
            <div
              id="access-panel-{n.id}"
              class="space-y-4"
              role="group"
              aria-label={t('clusterAccess.stepsFor', { name: n.name })}
            >
              {#if u.error}<Alert variant="error">{u.error}</Alert>{/if}

              <!-- 2. authorize -->
              <section class="space-y-2" aria-labelledby="st-a-{n.id}">
                <h3 id="st-a-{n.id}" class="text-sm font-semibold">
                  {t('clusterAccess.stepAuthorize')}
                  <span class="text-xs font-normal text-muted-foreground"
                    >({t(`clusterAccess.${stepStatus(step, 1)}`)})</span
                  >
                </h3>
                <p class="text-xs text-muted-foreground">
                  {t('clusterAccess.stepAuthorizeDesc', { host: n.host, user: n.user })}
                </p>
                {#if u.authorize}
                  <p class="text-xs">
                    {t('clusterAccess.authorizeWhere', { host: n.host, user: n.user })}
                  </p>
                  {@render copyable(
                    t('clusterAccess.commandLabel', { host: n.host }),
                    u.authorize.command
                  )}
                  {#if advanced}
                    {@render copyable(
                      t('clusterAccess.authorizedKeysLine'),
                      u.authorize.authorized_keys_line
                    )}
                  {/if}
                {:else}
                  <p class="text-xs text-muted-foreground" role="status">
                    {t('clusterAccess.loadingCommand')}
                  </p>
                {/if}
                {#if step === 'authorize'}
                  <Button class="min-h-11" onclick={() => patch(n.id, { ack: true })}>
                    {t('clusterAccess.ranIt')}
                  </Button>
                {/if}
              </section>

              <!-- 3. host key -->
              <section class="space-y-2" aria-labelledby="st-h-{n.id}">
                <h3 id="st-h-{n.id}" class="text-sm font-semibold">
                  {t('clusterAccess.stepHostKey')}
                  <span class="text-xs font-normal text-muted-foreground"
                    >({t(`clusterAccess.${stepStatus(step, 2)}`)})</span
                  >
                </h3>
                <p class="text-xs text-muted-foreground">{t('clusterAccess.stepHostKeyDesc')}</p>
                {#if n.host_key?.state === 'changed'}
                  <Alert variant="error" title={t('clusterAccess.changedTitle', { name: n.name })}>
                    {t('clusterAccess.changedBody')}
                  </Alert>
                {/if}
                {#if n.host_key?.state === 'pinned' || n.host_key?.state === 'changed'}
                  <p class="text-xs">
                    {t('clusterAccess.pinnedFingerprint')}:
                    <code class="font-mono break-all">{n.host_key.fingerprint}</code>
                  </p>
                {/if}
                {#if u.hostkey}
                  <div class="space-y-2 rounded-md border border-border p-2">
                    <p class="text-xs">{t('clusterAccess.fingerprintLabel', { host: n.host })}</p>
                    <code class="block font-mono text-sm break-all" data-testid="fingerprint-{n.id}"
                      >{u.hostkey.fingerprint}</code
                    >
                    <p class="text-xs text-muted-foreground" data-testid="hostkey-type-{n.id}">
                      {t('clusterAccess.keyType')}: {u.hostkey.key_type}
                    </p>
                    <p class="text-xs text-muted-foreground">{t('clusterAccess.verifyHelp')}</p>
                    <label class="flex items-start gap-2 text-sm cursor-pointer min-h-11">
                      <input
                        type="checkbox"
                        class="mt-1 h-4 w-4"
                        checked={!!u.verified}
                        onchange={(e) => patch(n.id, { verified: e.currentTarget.checked })}
                      />
                      <span>{t('clusterAccess.verifyCheckbox')}</span>
                    </label>
                    {#if n.host_key?.state === 'changed' || u.hostkey.state === 'changed'}
                      <Button
                        variant="destructive"
                        class="min-h-11"
                        disabled={!u.verified || u.busy === 'pin'}
                        onclick={() => (replaceFor = n)}>{t('clusterAccess.replaceKey')}</Button
                      >
                    {:else}
                      <Button
                        class="min-h-11"
                        disabled={!u.verified || u.busy === 'pin'}
                        onclick={() => pin(n)}
                      >
                        {#if u.busy === 'pin'}<Spinner size="sm" color="text-white" />{/if}
                        {u.busy === 'pin' ? t('clusterAccess.pinning') : t('clusterAccess.pin')}
                      </Button>
                    {/if}
                  </div>
                {/if}
                {#if step === 'hostkey' || advanced || n.host_key?.state === 'changed'}
                  <Button
                    class="min-h-11"
                    variant={step === 'hostkey' && !u.hostkey ? 'default' : 'outline'}
                    disabled={u.busy === 'hostkey'}
                    onclick={() => checkHostKey(n)}
                  >
                    {#if u.busy === 'hostkey'}<Spinner size="sm" />{/if}
                    {u.busy === 'hostkey'
                      ? t('clusterAccess.checking')
                      : t('clusterAccess.checkHostKey')}
                  </Button>
                {/if}
              </section>

              <!-- 4. test -->
              <section class="space-y-2" aria-labelledby="st-t-{n.id}">
                <h3 id="st-t-{n.id}" class="text-sm font-semibold">
                  {t('clusterAccess.stepTest')}
                  <span class="text-xs font-normal text-muted-foreground"
                    >({t(`clusterAccess.${stepStatus(step, 3)}`)})</span
                  >
                </h3>
                <p class="text-xs text-muted-foreground">{t('clusterAccess.stepTestDesc')}</p>
                {#if step === 'test' || step === 'done' || advanced}
                  <Button
                    class="min-h-11"
                    variant={step === 'test' ? 'default' : 'outline'}
                    disabled={u.busy === 'test'}
                    onclick={() => testNode(n)}
                  >
                    {#if u.busy === 'test'}<Spinner size="sm" />{/if}
                    {u.busy === 'test'
                      ? t('clusterAccess.testing')
                      : step === 'done'
                        ? t('clusterAccess.testAgain')
                        : t('clusterAccess.testConnection')}
                  </Button>
                {/if}
                {#if u.test}
                  {@const r = u.test}
                  <div class="space-y-1" data-testid="test-result-{n.id}" role="status">
                    <Alert variant={r.ok ? 'success' : 'error'}>
                      {r.ok
                        ? t('clusterAccess.testOk', { name: n.name })
                        : `${t('clusterAccess.testFail', { name: n.name })} ${codeLabel(t, 'clusterAccess.codes', r.code, 'clusterAccess.unknownCode')}`}
                    </Alert>
                    <ul class="text-xs space-y-0.5">
                      <li>
                        {t('clusterAccess.sshStep')}:
                        {r.ssh?.ok ? t('clusterAccess.resultOk') : t('clusterAccess.resultFail')}
                        {#if r.ssh?.code}· {codeLabel(
                            t,
                            'clusterAccess.codes',
                            r.ssh.code,
                            'clusterAccess.unknownCode'
                          )}{/if}
                      </li>
                      <li>
                        {t('clusterAccess.libvirtStep')}:
                        {r.libvirt?.ok
                          ? t('clusterAccess.resultOk')
                          : r.ssh?.ok
                            ? t('clusterAccess.resultFail')
                            : t('clusterAccess.resultSkipped')}
                        {#if r.libvirt?.code}· {codeLabel(
                            t,
                            'clusterAccess.codes',
                            r.libvirt.code,
                            'clusterAccess.unknownCode'
                          )}{/if}
                        {#if r.libvirt?.ok}
                          · {t('clusterAccess.libvirtVersion', { version: r.libvirt.version })} ·
                          {r.libvirt.kvm
                            ? t('clusterAccess.libvirtKvm')
                            : t('clusterAccess.libvirtNoKvm')} ·
                          {t('clusterAccess.libvirtDomains', { count: r.libvirt.domains })}
                        {/if}
                      </li>
                      {#if advanced}
                        <li>
                          {t('clusterAccess.transportLabel')}:
                          <code class="font-mono">{r.transport}</code>
                        </li>
                      {/if}
                    </ul>
                    {#if !r.ok && r.code}
                      {@const hk = `clusterAccess.hints.${r.code}`}
                      <p class="text-xs text-muted-foreground">{t(hk) !== hk ? t(hk) : r.hint}</p>
                    {/if}
                  </div>
                {:else if n.last_test}
                  <p class="text-xs text-muted-foreground">
                    {t('clusterAccess.lastTest')}: {n.last_test.ok
                      ? t('clusterAccess.resultOk')
                      : codeLabel(
                          t,
                          'clusterAccess.codes',
                          n.last_test.code,
                          'clusterAccess.unknownCode'
                        )}
                  </p>
                {:else}
                  <p class="text-xs text-muted-foreground">{t('clusterAccess.neverTested')}</p>
                {/if}
              </section>
            </div>
          {/if}
        </li>
      {/each}
    </ul>

    {#if overview.configured}
      <details class="rounded-md border border-border p-3">
        <summary class="cursor-pointer text-sm font-semibold min-h-6"
          >{t('clusterAccess.rotationTitle')}</summary
        >
        <div class="mt-2 space-y-2">
          <p class="text-xs text-muted-foreground">{t('clusterAccess.rotationIntro')}</p>
          {#if rotation.error}<Alert variant="error">{rotation.error}</Alert>{/if}
          {#if !overview.pending}
            <Button
              variant="outline"
              class="min-h-11"
              disabled={rotation.busy === 'start'}
              onclick={startRotation}
            >
              {rotation.busy === 'start'
                ? t('clusterAccess.rotationStarting')
                : t('clusterAccess.rotationStart')}
            </Button>
          {:else}
            <p class="text-sm font-medium">
              {t('clusterAccess.rotationPending')}:
              <code class="font-mono text-xs">{overview.pending.fingerprint}</code>
            </p>
            <h3 class="text-xs font-semibold">{t('clusterAccess.rotationStep1')}</h3>
            <p class="text-xs text-muted-foreground">{t('clusterAccess.rotationNewCommand')}</p>
            {#each rotNodes as n (n.id)}
              {#if rotation.command}
                {@render copyable(
                  t('clusterAccess.rotationRunOn', { name: n.name, user: n.user, host: n.host }),
                  rotation.command
                )}
              {:else}
                {@render copyable(
                  t('clusterAccess.rotationRunOnKey', { name: n.name, user: n.user, host: n.host }),
                  overview.pending.public_key
                )}
              {/if}
            {/each}
            <h3 class="text-xs font-semibold">{t('clusterAccess.rotationStep2')}</h3>
            <Button
              variant="outline"
              class="min-h-11"
              disabled={rotation.busy === 'test'}
              onclick={testRotation}
            >
              {rotation.busy === 'test'
                ? t('clusterAccess.rotationTestingAll')
                : t('clusterAccess.rotationTestNew')}
            </Button>
            <ul
              class="space-y-1"
              aria-label={t('clusterAccess.rotationResultsAria')}
              data-testid="rotation-results"
            >
              {#each rotNodes as n (n.id)}
                <li class="flex flex-wrap items-center gap-2 text-xs">
                  <span class="font-medium">{n.name}</span>
                  {#if rotVerified(n)}
                    <span class="rounded px-2 py-0.5 bg-success/10 text-success"
                      >{t('clusterAccess.rotationNodeOk')}</span
                    >
                  {:else}
                    <span class="rounded px-2 py-0.5 bg-warning/10 text-warning">
                      {rotCode(n) === 'not_tested'
                        ? t('clusterAccess.rotationNodeNotTested')
                        : codeLabel(
                            t,
                            'clusterAccess.codes',
                            rotCode(n),
                            'clusterAccess.unknownCode'
                          )}
                    </span>
                    {#if rotHint(rotCode(n))}
                      <span class="text-muted-foreground">{rotHint(rotCode(n))}</span>
                    {/if}
                  {/if}
                </li>
              {/each}
            </ul>
            <h3 class="text-xs font-semibold">{t('clusterAccess.rotationStep3')}</h3>
            {#if !rotAllPassed}
              <p class="text-xs text-muted-foreground" data-testid="rotation-blocked">
                {t('clusterAccess.rotationBlocked', { count: rotFailing.length })}
              </p>
            {/if}
            {#if advanced && !rotAllPassed}
              <div class="rounded-md border border-destructive/50 bg-destructive/5 p-3 space-y-2">
                <p class="text-xs font-semibold text-destructive">
                  {t('clusterAccess.rotationForceWarning')}
                </p>
                <ul class="list-disc pl-5 text-xs">
                  {#each rotFailing as n (n.id)}<li>{n.name}</li>{/each}
                </ul>
                <label class="flex items-start gap-2 text-sm cursor-pointer min-h-11">
                  <input type="checkbox" class="mt-1 h-4 w-4" bind:checked={rotation.force} />
                  <span>{t('clusterAccess.rotationForce')}</span>
                </label>
              </div>
            {/if}
            <div class="flex flex-wrap gap-2">
              <Button class="min-h-11" disabled={!canCommit} onclick={() => (commitOpen = true)}
                >{t('clusterAccess.rotationCommit')}</Button
              >
              <Button variant="outline" class="min-h-11" onclick={() => (abortOpen = true)}>
                {t('clusterAccess.rotationAbort')}
              </Button>
            </div>
          {/if}
          {#if overview.previous}
            <div
              class="rounded-md border border-border p-3 space-y-2"
              data-testid="rotation-rollback"
            >
              <p class="text-xs font-semibold">{t('clusterAccess.rollbackTitle')}</p>
              <p class="text-xs text-muted-foreground">
                {t('clusterAccess.rollbackBody')}
                <code class="font-mono">{shortFingerprint(overview.previous.fingerprint)}</code>
              </p>
              <p class="text-xs text-muted-foreground">{t('clusterAccess.rollbackLimit')}</p>
              <Button variant="outline" class="min-h-11" onclick={() => (rollbackOpen = true)}>
                {t('clusterAccess.rollbackBtn')}
              </Button>
            </div>
          {/if}
        </div>
      </details>
    {/if}
  {/if}
</Card>

<ConfirmDialog
  open={!!replaceFor}
  title={t('clusterAccess.replaceTitle', { name: replaceFor?.name || '' })}
  description={t('clusterAccess.replaceBody')}
  confirmLabel={t('clusterAccess.replaceConfirm')}
  variant="destructive"
  loading={replaceFor ? st(replaceFor.id).busy === 'pin' : false}
  onConfirm={() => replaceFor && pin(replaceFor, true)}
  onCancel={() => (replaceFor = null)}
/>

<ConfirmDialog
  open={commitOpen}
  title={t('clusterAccess.rotationCommitTitle')}
  description={rotAllPassed
    ? t('clusterAccess.rotationCommitBody')
    : t('clusterAccess.rotationCommitForcedBody')}
  confirmLabel={t('clusterAccess.rotationCommit')}
  variant="destructive"
  loading={rotation.busy === 'commit'}
  onConfirm={() => canCommit && finishRotation('commit')}
  onCancel={() => (commitOpen = false)}
/>

<ConfirmDialog
  open={rollbackOpen}
  title={t('clusterAccess.rollbackTitle')}
  description={t('clusterAccess.rollbackConfirmBody')}
  confirmLabel={t('clusterAccess.rollbackBtn')}
  variant="destructive"
  loading={rotation.busy === 'rollback'}
  onConfirm={rollbackRotation}
  onCancel={() => (rollbackOpen = false)}
/>

<ConfirmDialog
  open={abortOpen}
  title={t('clusterAccess.rotationAbortTitle')}
  description={t('clusterAccess.rotationAbortBody')}
  confirmLabel={t('clusterAccess.rotationAbort')}
  loading={rotation.busy === 'abort'}
  onConfirm={() => finishRotation('abort')}
  onCancel={() => (abortOpen = false)}
/>
