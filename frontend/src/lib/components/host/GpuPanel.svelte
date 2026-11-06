<script>
  import { t } from '$lib/i18n.svelte.js';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { formatBytes } from '$lib/utils/format.js';

  let { gpu } = $props();

  function getTempColor(temp) {
    if (temp > 85) return 'text-destructive';
    if (temp > 70) return 'text-warning';
    return 'text-success';
  }

  function getUsageColor(percent) {
    if (percent > 85) return 'bg-destructive';
    if (percent > 60) return 'bg-warning';
    return 'bg-success';
  }
</script>

<BlockCard bid="gpu" title={t('host.gpuNvidia')}>
  <!-- GPU Summary -->
  <div class="flex items-center gap-4 mb-4 px-3 py-2 bg-muted/30 rounded-lg text-xs flex-wrap">
    <span class="text-muted-foreground">
      {t('host.driver')}: <span class="font-mono">{gpu?.driver_version || '—'}</span>
    </span>
    {#if gpu?.cuda_version}
      <span class="text-muted-foreground">
        CUDA: <span class="font-mono">{gpu.cuda_version}</span>
      </span>
    {/if}
    <span class="text-muted-foreground">
      {gpu?.gpus?.length || 0} {t('host.gpus')}
    </span>
  </div>

  <!-- GPU Devices -->
  <div class="space-y-3">
    {#each gpu?.gpus || [] as device}
      <div class="border border-border rounded-lg p-3">
        <!-- Device Header -->
        <div class="flex items-center justify-between mb-3">
          <div class="flex items-center gap-2">
            <Icon name="cpu" size={14} class="text-success" />
            <span class="text-sm font-medium">[{device.index}] {device.name}</span>
          </div>
          <div class="flex items-center gap-3 text-xs">
            {#if device.temperature_c != null}
              <span class="font-mono {getTempColor(device.temperature_c)}">
                {device.temperature_c.toFixed(0)}°C
              </span>
            {/if}
            <span class="px-1.5 py-0.5 rounded bg-muted text-muted-foreground font-mono">
              {device.pstate}
            </span>
          </div>
        </div>

        <!-- GPU Utilization -->
        {#if device.utilization_gpu_percent != null}
          <div class="mb-2">
            <div class="flex items-baseline justify-between mb-1">
              <span class="text-xs text-muted-foreground">GPU</span>
              <span class="text-xs font-mono tnum">{device.utilization_gpu_percent.toFixed(1)}%</span>
            </div>
            <div class="h-2 bg-muted rounded-full overflow-hidden">
              <div
                class="h-full transition-all {getUsageColor(device.utilization_gpu_percent)}"
                style="width: {Math.min(100, device.utilization_gpu_percent)}%"
              ></div>
            </div>
          </div>
        {/if}

        <!-- Memory Utilization -->
        <div class="mb-2">
          <div class="flex items-baseline justify-between mb-1">
            <span class="text-xs text-muted-foreground">{t('host.memory')}</span>
            <span class="text-xs font-mono tnum">
              {formatBytes((device.memory_used_mb || 0) * 1024 * 1024)} / {formatBytes((device.memory_total_mb || 0) * 1024 * 1024)}
            </span>
          </div>
          <div class="h-2 bg-muted rounded-full overflow-hidden">
            <div
              class="h-full transition-all {getUsageColor(device.memory_total_mb > 0 ? (device.memory_used_mb / device.memory_total_mb) * 100 : 0)}"
              style="width: {Math.min(100, device.memory_total_mb > 0 ? (device.memory_used_mb / device.memory_total_mb) * 100 : 0)}%"
            ></div>
          </div>
        </div>

        <!-- Power & Fan -->
        <div class="flex items-center gap-4 text-xs text-muted-foreground pt-2 border-t border-border">
          {#if device.power_draw_w != null}
            <span>
              ⚡ {device.power_draw_w?.toFixed(0) || '0'}W / {device.power_limit_w?.toFixed(0) || '?'}W
            </span>
          {/if}
          {#if device.fan_speed_percent != null}
            <span>🌀 {device.fan_speed_percent.toFixed(0)}%</span>
          {/if}
          <span class="ml-auto font-mono text-[10px] truncate max-w-[200px]" title={device.uuid}>
            {device.uuid}
          </span>
        </div>
      </div>
    {/each}
  </div>

  <!-- GPU Processes -->
  {#if gpu?.processes?.length > 0}
    <div class="mt-4 pt-4 border-t border-border">
      <h3 class="text-xs font-medium text-muted-foreground uppercase tracking-wider mb-2">
        {t('host.gpuProcesses')}
      </h3>
      <div class="overflow-x-auto">
        <table class="w-full text-xs">
          <thead>
            <tr class="text-left text-muted-foreground border-b border-border">
              <th class="pb-1 pr-4 font-medium">PID</th>
              <th class="pb-1 pr-4 font-medium">{t('host.process')}</th>
              <th class="pb-1 pr-4 font-medium text-right">GPU</th>
              <th class="pb-1 font-medium text-right">{t('host.memory')}</th>
            </tr>
          </thead>
          <tbody class="font-mono">
            {#each gpu.processes as proc}
              <tr class="border-b border-border/50">
                <td class="py-1.5 pr-4">{proc.pid}</td>
                <td class="py-1.5 pr-4 truncate max-w-[150px]" title={proc.name}>{proc.name}</td>
                <td class="py-1.5 pr-4 text-right tnum">
                  [{proc.gpu_index}]
                </td>
                <td class="py-1.5 text-right tnum">{formatBytes((proc.used_memory_mb || 0) * 1024 * 1024)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {/if}
</BlockCard>
