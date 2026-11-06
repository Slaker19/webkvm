<script>
  import { t } from '$lib/i18n.svelte.js';
  import CpuLoadPanel from '$lib/components/host/CpuLoadPanel.svelte';
  import MemoryPanel from '$lib/components/host/MemoryPanel.svelte';
  import NetworkPanel from '$lib/components/host/NetworkPanel.svelte';
  import DiskPanel from '$lib/components/host/DiskPanel.svelte';
  import GpuPanel from '$lib/components/host/GpuPanel.svelte';

  /**
   * @typedef {Object} Props
   * @property {any} metrics - Host metrics from /api/host/monitor
   */
  let { metrics } = $props();

  let showPerCore = $state(false);

  const hasGPU = $derived(metrics?.gpu?.available && metrics?.gpu?.gpus?.length > 0);
  const hasNPU = $derived(metrics?.npu?.available && metrics?.npu?.npus?.length > 0);
</script>

{#if metrics}
  <!-- CPU & Load -->
  <div class="mt-4">
    <CpuLoadPanel
      cpu={metrics.cpu}
      load={metrics.load}
      history={metrics.history?.cpu || []}
      bind:showPerCore
    />
  </div>

  <!-- Memory & Swap -->
  <div class="mt-4">
    <MemoryPanel
      memory={metrics.memory}
      swap={metrics.swap}
      history={metrics.history?.memory || []}
    />
  </div>

  <!-- Network Traffic -->
  <div class="mt-4">
    <NetworkPanel
      networks={metrics.networks}
      summary={metrics.network_summary}
      history={metrics.history?.networks || {}}
    />
  </div>

  <!-- Disk Usage & I/O -->
  <div class="mt-4">
    <DiskPanel disks={metrics.disks} diskGroups={metrics.disk_groups} />
  </div>

  <!-- GPU (conditional) -->
  {#if hasGPU}
    <div class="mt-4">
      <GpuPanel gpu={metrics.gpu} />
    </div>
  {/if}

  <!-- NPU (conditional, placeholder) -->
  {#if hasNPU}
    <div class="mt-4">
      <!-- NpuPanel would go here -->
    </div>
  {/if}

  <!-- Last Updated -->
  <div class="mt-4 text-xs text-muted-foreground text-right">
    {t('host.lastUpdated')}: {new Date(metrics.sampled_at).toLocaleTimeString()}
  </div>
{/if}
