<script>
  import { t } from '$lib/i18n.svelte.js';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Chart from '$lib/components/Chart.svelte';
  import Icon from '$lib/components/Icon.svelte';

  let { cpu, load, history, showPerCore = $bindable(false) } = $props();

  function getLoadColor(value, cores) {
    const perCore = value / (cores || 1);
    if (perCore < 0.7) return 'text-success';
    if (perCore < 1.0) return 'text-warning';
    return 'text-destructive';
  }

  function getCoreColor(usage) {
    if (usage < 30) return '#22c55e';
    if (usage < 60) return '#84cc16';
    if (usage < 85) return '#eab308';
    return '#ef4444';
  }
</script>

<BlockCard bid="cpu-load" title={t('host.cpuAndLoad')}>
  <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
    <!-- CPU Usage -->
    <div>
      <div class="flex items-baseline justify-between mb-1.5">
        <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
          {t('host.cpuUsage')}
        </span>
        <span class="text-sm tnum">{cpu?.usage_percent?.toFixed(1) || '0.0'}%</span>
      </div>
      <Chart points={history || []} yMax={100} height={70} />
      <div class="mt-2 text-xs text-muted-foreground">
        <span class="font-mono">{cpu?.cores || 0}C/{cpu?.threads || 0}T</span>
        {#if cpu?.model}
          <span class="ml-2 truncate" title={cpu.model}>{cpu.model}</span>
        {/if}
      </div>

      <!-- Per-Core Grid (collapsible) -->
      {#if cpu?.per_core?.length > 0}
        <button
          class="mt-2 flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
          onclick={() => showPerCore = !showPerCore}
        >
          <Icon name={showPerCore ? 'chevron-down' : 'chevron-right'} size={12} />
          {t('host.perCoreUsage')}
        </button>
        {#if showPerCore}
          <div class="mt-2 grid grid-cols-8 gap-1">
            {#each cpu.per_core as core}
              <div
                class="aspect-square rounded flex flex-col items-center justify-center text-[10px] text-white transition-transform hover:scale-110"
                style="background: {getCoreColor(core.usage)}"
                title="Core {core.id}: {core.usage.toFixed(1)}%"
              >
                <span class="font-bold">{core.id}</span>
                <span>{core.usage.toFixed(0)}%</span>
              </div>
            {/each}
          </div>
        {/if}
      {/if}
    </div>

    <!-- Load Average -->
    <div>
      <div class="flex items-baseline justify-between mb-1.5">
        <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
          {t('host.loadAverage')}
        </span>
      </div>
      <div class="bg-muted/30 rounded-lg p-4">
        <div class="grid grid-cols-3 gap-4 text-center">
          <div>
            <div class="text-xs text-muted-foreground mb-1">1 min</div>
            <div class="text-lg font-mono tnum {getLoadColor(load?.load1 || 0, cpu?.cores)}">
              {load?.load1?.toFixed(2) || '0.00'}
            </div>
          </div>
          <div>
            <div class="text-xs text-muted-foreground mb-1">5 min</div>
            <div class="text-lg font-mono tnum {getLoadColor(load?.load5 || 0, cpu?.cores)}">
              {load?.load5?.toFixed(2) || '0.00'}
            </div>
          </div>
          <div>
            <div class="text-xs text-muted-foreground mb-1">15 min</div>
            <div class="text-lg font-mono tnum {getLoadColor(load?.load15 || 0, cpu?.cores)}">
              {load?.load15?.toFixed(2) || '0.00'}
            </div>
          </div>
        </div>
      </div>
      <div class="mt-2 text-xs text-muted-foreground">
        {t('host.loadHint', { cores: cpu?.cores || 1 })}
      </div>
    </div>
  </div>
</BlockCard>
