<script>
  import { t } from '$lib/i18n.svelte.js';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Chart from '$lib/components/Chart.svelte';
  import { formatBytes } from '$lib/utils/format.js';

  let { memory, swap, history } = $props();

  function getUsageColor(percent) {
    if (percent > 90) return 'bg-destructive';
    if (percent > 75) return 'bg-warning';
    return 'bg-success';
  }

  function getUsageTextColor(percent) {
    if (percent > 90) return 'text-destructive';
    if (percent > 75) return 'text-warning';
    return 'text-muted-foreground';
  }
</script>

<BlockCard bid="memory" title={t('host.memoryAndSwap')}>
  <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
    <!-- Memory Usage -->
    <div>
      <div class="flex items-baseline justify-between mb-1.5">
        <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
          {t('host.ramUsage')}
        </span>
        <span class="text-sm tnum {getUsageTextColor(memory?.usage_percent || 0)}">
          {memory?.usage_percent?.toFixed(1) || '0.0'}%
        </span>
      </div>
      <Chart points={history || []} yMax={100} height={70} />
      <div class="mt-2 text-xs text-muted-foreground">
        {formatBytes(memory?.used_bytes || 0)} / {formatBytes(memory?.total_bytes || 0)}
      </div>

      <!-- Memory Details -->
      <div class="mt-3 bg-muted/30 rounded-lg p-3 space-y-1.5">
        <div class="flex justify-between text-xs">
          <span class="text-muted-foreground">{t('host.used')}</span>
          <span class="font-mono tnum">{formatBytes(memory?.used_bytes || 0)}</span>
        </div>
        <div class="flex justify-between text-xs">
          <span class="text-muted-foreground">{t('host.available')}</span>
          <span class="font-mono tnum">{formatBytes(memory?.available_bytes || 0)}</span>
        </div>
        <div class="flex justify-between text-xs">
          <span class="text-muted-foreground">{t('host.cached')}</span>
          <span class="font-mono tnum">{formatBytes(memory?.cached_bytes || 0)}</span>
        </div>
        <div class="flex justify-between text-xs">
          <span class="text-muted-foreground">{t('host.buffers')}</span>
          <span class="font-mono tnum">{formatBytes(memory?.buffers_bytes || 0)}</span>
        </div>
      </div>
    </div>

    <!-- Swap Usage -->
    <div>
      <div class="flex items-baseline justify-between mb-1.5">
        <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
          {t('host.swapUsage')}
        </span>
        <span class="text-sm tnum {getUsageTextColor(swap?.usage_percent || 0)}">
          {swap?.usage_percent?.toFixed(1) || '0.0'}%
        </span>
      </div>

      <!-- Swap Progress Bar -->
      <div class="mb-4">
        <div class="h-4 bg-muted rounded-full overflow-hidden">
          <div
            class="h-full transition-all {getUsageColor(swap?.usage_percent || 0)}"
            style="width: {Math.min(100, swap?.usage_percent || 0)}%"
          ></div>
        </div>
        <div class="mt-1 text-xs text-muted-foreground text-center">
          {formatBytes(swap?.used_bytes || 0)} / {formatBytes(swap?.total_bytes || 0)}
        </div>
      </div>

      <!-- Memory Usage Bar -->
      <div class="mb-4">
        <div class="flex items-baseline justify-between mb-1">
          <span class="text-xs text-muted-foreground">{t('host.memory')}</span>
          <span class="text-xs font-mono tnum">{memory?.usage_percent?.toFixed(1) || '0'}%</span>
        </div>
        <div class="h-3 bg-muted rounded-full overflow-hidden">
          <div
            class="h-full transition-all {getUsageColor(memory?.usage_percent || 0)}"
            style="width: {Math.min(100, memory?.usage_percent || 0)}%"
          ></div>
        </div>
      </div>

      <!-- Swap Usage Bar -->
      <div>
        <div class="flex items-baseline justify-between mb-1">
          <span class="text-xs text-muted-foreground">{t('host.swap')}</span>
          <span class="text-xs font-mono tnum">{swap?.usage_percent?.toFixed(1) || '0'}%</span>
        </div>
        <div class="h-3 bg-muted rounded-full overflow-hidden">
          <div
            class="h-full transition-all {getUsageColor(swap?.usage_percent || 0)}"
            style="width: {Math.min(100, swap?.usage_percent || 0)}%"
          ></div>
        </div>
      </div>
    </div>
  </div>
</BlockCard>
