<script>
  /** ClusterOverviewStrip — counts at a glance for the Nodes page. */
  import { t } from '$lib/i18n.svelte.js';
  import { nodeHealthCounts } from '$lib/utils/nodes.js';

  let { nodes = [], vms = [] } = $props();
  // One classification (nodeHealth) for every count; this server is counted
  // once even when the registry also lists it as a remote entry.
  const c = $derived(nodeHealthCounts(nodes));
  const unmanaged = $derived(
    (Array.isArray(vms) ? vms : []).filter((v) => v.managed === false).length
  );
  const bad = $derived(c.degraded + c.offline);
  const items = $derived([
    { id: 'enabled', label: 'remote.overview.enabled', value: c.enabled, tone: 'text-success' },
    { id: 'online', label: 'nodeSelf.overviewOnline', value: c.online, tone: 'text-success' },
    {
      id: 'no_access',
      label: 'nodeSelf.overviewNoAccess',
      value: c.no_access,
      tone: c.no_access ? 'text-info' : 'text-foreground',
    },
    {
      id: 'degraded',
      label: 'remote.overview.degraded',
      value: bad,
      tone: bad ? 'text-warning' : 'text-foreground',
    },
    {
      id: 'disabled',
      label: 'remote.overview.disabled',
      value: c.disabled,
      tone: 'text-muted-foreground',
    },
    {
      id: 'unmanaged',
      label: 'remote.overview.unmanaged',
      value: unmanaged,
      tone: 'text-foreground',
    },
  ]);
</script>

<section
  aria-labelledby="cluster-overview-title"
  class="rounded-lg border border-border bg-card/60 p-4"
  data-testid="cluster-overview"
>
  <h2 id="cluster-overview-title" class="text-sm font-semibold text-foreground">
    {t('remote.overview.title')}
  </h2>
  <dl class="mt-2 grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
    {#each items as it (it.id)}
      <div>
        <dt class="text-xs text-muted-foreground">{t(it.label)}</dt>
        <dd class="font-mono text-xl font-bold {it.tone}" data-testid="overview-{it.id}">
          {it.value}
        </dd>
      </div>
    {/each}
  </dl>
  <p class="mt-2 text-xs text-muted-foreground">{t('remote.overview.hint')}</p>
</section>
