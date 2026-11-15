<script>
  /**
   * NodeHealthBanner — dismissible list of nodes that failed or returned
   * stale data in the VM list, each with its translated cause and, for
   * SSH/host-key problems, a one-click link to the Cluster access card.
   */
  import { t } from '$lib/i18n.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import { remoteErrorText } from '$lib/utils/remoteRoutes.js';

  // issues: [{ id, name, kind: 'stale'|'offline'|'degraded'|'no_access', code, access }]
  // hiddenWorkloads: node id -> names of HA workloads this node's problem
  // hides from the list (folded here instead of a second notice).
  let { issues = [], hiddenWorkloads = {} } = $props();
  let dismissedKey = $state('');
  const key = $derived(issues.map((i) => `${i.id}:${i.kind}:${i.code}`).join('|'));
  // A new/different problem re-opens the banner after a dismissal.
  const hidden = $derived(dismissedKey !== '' && dismissedKey === key);
  const anyAccess = $derived(issues.some((i) => i.access));
</script>

{#if issues.length && !hidden}
  <div
    role="alert"
    class="rounded-md border border-warning/30 bg-warning/10 p-3 text-sm text-warning"
    data-testid="node-health-banner"
  >
    <div class="flex items-start justify-between gap-3">
      <div class="font-semibold">{t('remote.health.title')}</div>
      <button
        type="button"
        class="shrink-0 rounded px-1.5 py-0.5 text-xs underline underline-offset-2 hover:opacity-80 focus-visible:outline-2"
        aria-label={t('remote.health.dismissAria')}
        onclick={() => (dismissedKey = key)}
      >
        {t('remote.health.dismiss')}
      </button>
    </div>
    <ul class="mt-1 space-y-1">
      {#each issues as issue (issue.id + issue.kind)}
        <li data-testid="node-issue-{issue.id}">
          {issue.kind === 'no_access'
            ? t('nodeSelf.healthNoAccess', { node: issue.name })
            : t(`remote.health.${issue.kind}`, { node: issue.name })}
          {#if issue.code}
            <span class="block text-xs opacity-90">
              {t('remote.health.reason', { reason: remoteErrorText(t, { code: issue.code }) })}
            </span>
          {/if}
          {#if hiddenWorkloads?.[issue.id]?.length}
            <span class="block text-xs opacity-90" data-testid="node-issue-ha-{issue.id}">
              {t('nodeSelf.haMissing.hiddenOnNode', {
                names: hiddenWorkloads[issue.id].join(', '),
              })}
            </span>
          {/if}
        </li>
      {/each}
    </ul>
    {#if anyAccess}
      <button
        type="button"
        class="mt-2 rounded border border-warning/40 px-2 py-1 text-xs font-medium hover:bg-warning/10"
        onclick={() => navigate('/nodes', { query: { focus: 'access' } })}
      >
        {t('remote.health.fix')}
      </button>
    {/if}
  </div>
{/if}
