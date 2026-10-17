<script>
  import { t } from '$lib/i18n.svelte.js';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Chart from '$lib/components/Chart.svelte';
  import { formatRate } from '$lib/utils/format.js';

  let {
    vm = null,
    metrics = null,
    cpuPoints = [],
    ramPoints = [],
    diskRPoints = [],
    diskWPoints = [],
    netRxPoints = [],
    netTxPoints = [],
  } = $props();
</script>

<BlockCard bid="metrics" title={t('vmDetail.metrics')}>
  <div class="flex items-center justify-between mb-3">
    <span class="text-xs text-muted-foreground tnum"
      >{t('vmDetail.updatedAt', {
        time: metrics?.sampled_at ? new Date(metrics.sampled_at * 1000).toLocaleTimeString() : '—',
      })}</span
    >
  </div>
  {#if vm?.state !== 'running'}
    <p class="text-sm text-muted-foreground">
      {t('vmDetail.metricsOff')}
    </p>
  {:else if !metrics || (cpuPoints.length === 0 && ramPoints.length === 0)}
    <p class="text-sm text-muted-foreground">{t('vmDetail.collectingSamples')}</p>
  {:else}
    <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
      <div>
        <div class="flex items-baseline justify-between mb-1.5">
          <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
            >{t('vms.cpu')}</span
          >
          <span class="text-sm tnum"
            >{cpuPoints.length ? cpuPoints[cpuPoints.length - 1].v.toFixed(1) : '0.0'}%</span
          >
        </div>
        <Chart points={cpuPoints} yMax={100} height={70} />
      </div>
      <div>
        <div class="flex items-baseline justify-between mb-1.5">
          <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
            >{t('common.ram')}</span
          >
          <span class="text-sm tnum"
            >{ramPoints.length ? ramPoints[ramPoints.length - 1].v.toFixed(1) : '0.0'}%</span
          >
        </div>
        <Chart points={ramPoints} yMax={100} height={70} />
      </div>
      <div>
        <div class="flex items-baseline justify-between mb-1.5">
          <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
            >{t('vmDetail.diskRead')}</span
          >
          <span class="text-sm tnum"
            >{diskRPoints.length
              ? formatRate(diskRPoints[diskRPoints.length - 1].v)
              : '0 B/s'}</span
          >
        </div>
        <Chart points={diskRPoints} height={70} color="var(--success)" />
      </div>
      <div>
        <div class="flex items-baseline justify-between mb-1.5">
          <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
            >{t('vmDetail.diskWrite')}</span
          >
          <span class="text-sm tnum"
            >{diskWPoints.length
              ? formatRate(diskWPoints[diskWPoints.length - 1].v)
              : '0 B/s'}</span
          >
        </div>
        <Chart points={diskWPoints} height={70} color="var(--warning)" />
      </div>
      <div>
        <div class="flex items-baseline justify-between mb-1.5">
          <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
            >{t('vmDetail.netRx')}</span
          >
          <span class="text-sm tnum"
            >{netRxPoints.length
              ? formatRate(netRxPoints[netRxPoints.length - 1].v)
              : '0 B/s'}</span
          >
        </div>
        <Chart points={netRxPoints} height={70} color="var(--info, var(--accent))" />
      </div>
      <div>
        <div class="flex items-baseline justify-between mb-1.5">
          <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
            >{t('vmDetail.netTx')}</span
          >
          <span class="text-sm tnum"
            >{netTxPoints.length
              ? formatRate(netTxPoints[netTxPoints.length - 1].v)
              : '0 B/s'}</span
          >
        </div>
        <Chart points={netTxPoints} height={70} color="var(--info, var(--accent))" />
      </div>
    </div>
  {/if}
</BlockCard>
