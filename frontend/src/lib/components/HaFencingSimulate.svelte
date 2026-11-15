<script>
  /**
   * HaFencingSimulate — guided "Simulate a failure" flow used when the
   * cluster fences with the SBD watchdog only. Asking Pacemaker to fence a
   * healthy node does not reboot it in that mode, so the only reliable test
   * is to crash a node on purpose and wait for Pacemaker to record the fence
   * (GET /api/ha/fencing/evidence). Nothing here crashes anything: the
   * operator runs the shown command by hand on the chosen node.
   */
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { Button } from '$lib/components/ui/button';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import CopyCommands from '$lib/components/CopyCommands.svelte';

  let { readiness, candidates = [], controlPlaneNode = '', onChanged = () => {} } = $props();

  const POLL_MS = 5000;
  const MAX_WAIT_MS = 10 * 60 * 1000;
  const FALLBACK_COMMAND = `sudo sh -c 'echo 1 > /proc/sys/kernel/sysrq; echo c > /proc/sysrq-trigger'`;

  const resourceCount = (name) =>
    (readiness?.pacemaker?.resources || []).filter((r) => r.node === name && r.active !== false)
      .length;
  // Nodes without HA resources first: crashing them moves nothing.
  const options = $derived(
    candidates
      .filter((n) => n !== controlPlaneNode)
      .map((n) => ({ name: n, count: resourceCount(n) }))
      .sort((a, b) => a.count - b.count || a.name.localeCompare(b.name))
  );

  let target = $state('');
  let evidence = $state(null); // last GET /ha/fencing/evidence answer
  let phase = $state('idle'); // idle | waiting | proven | timeout
  let pollFailed = $state(false);
  let proof = $state(null); // the new fence event
  let timer = null;
  let baseline = new Set();
  let startedAt = 0;
  let alive = true;

  const effectiveTarget = $derived(options.some((o) => o.name === target) ? target : '');
  const command = $derived(evidence?.simulate?.command_template || FALLBACK_COMMAND);
  const fmt = (iso) => {
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? String(iso) : d.toLocaleString();
  };
  // Pacemaker prints "2026-11-12 10:00:00 +00:00"; make it Date-parseable.
  const completedISO = (s) =>
    String(s || '')
      .replace(' ', 'T')
      .replace(' ', '');
  const eventKey = (e) => `${e.target}|${e.action}|${e.completed}`;
  const isSuccess = (e) => e?.status === 'success' && (e.action === 'reboot' || e.action === 'off');

  async function load() {
    try {
      const res = await api.haFencingEvidence();
      if (alive) evidence = res;
      return res;
    } catch {
      return null;
    }
  }

  function stop() {
    if (timer) clearTimeout(timer);
    timer = null;
  }

  // Chained timeouts (never overlapping requests); ends on success, after
  // 10 minutes, on cancel and when the component is destroyed.
  function schedule() {
    stop();
    timer = setTimeout(async () => {
      timer = null;
      if (!alive || phase !== 'waiting') return;
      if (Date.now() - startedAt >= MAX_WAIT_MS) {
        phase = 'timeout';
        return;
      }
      const res = await load();
      if (!alive || phase !== 'waiting') return;
      pollFailed = !res;
      const fresh = (res?.events || []).find(
        (e) => isSuccess(e) && e.target === effectiveTarget && !baseline.has(eventKey(e))
      );
      if (fresh) {
        proof = fresh;
        phase = 'proven';
        onChanged();
        return;
      }
      schedule();
    }, POLL_MS);
  }

  async function startWaiting() {
    if (!effectiveTarget) return;
    const res = await load();
    baseline = new Set((res?.events || []).filter(isSuccess).map(eventKey));
    startedAt = Date.now();
    proof = null;
    pollFailed = false;
    phase = 'waiting';
    schedule();
  }

  function cancel() {
    stop();
    phase = 'idle';
  }

  $effect(() => {
    alive = true;
    load();
    return () => {
      alive = false;
      stop();
    };
  });
</script>

<div class="space-y-3" data-testid="fencing-simulate">
  <h4 class="text-xs font-semibold text-foreground">{t('nodes.haops.simTitle')}</h4>
  <p class="text-xs text-muted-foreground">{t('nodes.haops.simIntro')}</p>

  {#if evidence?.tested}
    <Alert variant="success">
      <span data-testid="sim-already"
        >{t('nodes.haops.simAlready', {
          when: evidence.tested_at ? fmt(evidence.tested_at) : '—',
        })}</span
      >
    </Alert>
  {/if}

  {#if options.length === 0}
    <Alert variant="warning">{t('nodes.haops.simNoCandidates')}</Alert>
  {:else}
    <div class="space-y-1">
      <label class="text-xs font-medium" for="sim-node">{t('nodes.haops.simPick')}</label>
      <select
        id="sim-node"
        class="h-9 w-full rounded-lg border border-input bg-background px-2 text-sm"
        value={target}
        disabled={phase === 'waiting'}
        onchange={(e) => (target = e.currentTarget.value)}
      >
        <option value="">{t('nodes.haops.fenceChoose')}</option>
        {#each options as o (o.name)}
          <option value={o.name}>
            {o.name} — {o.count === 0
              ? t('nodes.haops.simNoResources')
              : t('nodes.haops.simResources', { count: o.count })}
          </option>
        {/each}
      </select>
    </div>
  {/if}

  {#if effectiveTarget}
    <Alert variant="warning" title={t('nodes.haops.simWarnTitle', { node: effectiveTarget })}>
      <span data-testid="sim-warning"
        >{t('nodes.haops.simWarnBody', { node: effectiveTarget })}</span
      >
    </Alert>
    <p class="text-xs text-foreground">
      {t('nodes.haops.simCommandLabel', { node: effectiveTarget })}
    </p>
    <CopyCommands
      commands={[command]}
      copyAria={t('nodes.haops.simCopyAria', { node: effectiveTarget })}
    />

    {#if phase === 'idle' || phase === 'timeout'}
      {#if phase === 'timeout'}
        <Alert variant="error">
          <span data-testid="sim-timeout"
            >{t('nodes.haops.simTimeout', { node: effectiveTarget })}</span
          >
        </Alert>
      {/if}
      <Button class="min-h-11" onclick={startWaiting}>
        {phase === 'timeout' ? t('nodes.haops.simRetry') : t('nodes.haops.simStart')}
      </Button>
    {:else if phase === 'waiting'}
      <div class="space-y-2" data-testid="sim-waiting" aria-live="polite">
        <p class="flex items-center gap-2 text-sm text-foreground">
          <Spinner size="sm" />
          {t('nodes.haops.simWaiting', { node: effectiveTarget })}
        </p>
        {#if pollFailed}
          <p class="text-xs text-warning">{t('nodes.haops.simPollError')}</p>
        {/if}
        <Button variant="outline" class="min-h-11" onclick={cancel}
          >{t('nodes.haops.simCancel')}</Button
        >
      </div>
    {/if}
  {/if}

  {#if phase === 'proven' && proof}
    <Alert variant="success">
      <span data-testid="sim-proven"
        >{t('nodes.haops.simProven', {
          node: proof.target,
          action: proof.action,
          when: proof.completed ? fmt(completedISO(proof.completed)) : '',
        })}</span
      >
    </Alert>
  {/if}

  {#if evidence?.events?.length}
    <div class="space-y-1" data-testid="sim-events">
      <div class="text-xs font-semibold">{t('nodes.haops.simRecent')}</div>
      <ul class="space-y-0.5 text-xs text-muted-foreground">
        {#each evidence.events as e (eventKey(e) + e.status)}
          <li>{e.target} · {e.action} · {e.status} · {e.completed}</li>
        {/each}
      </ul>
    </div>
  {/if}
</div>
