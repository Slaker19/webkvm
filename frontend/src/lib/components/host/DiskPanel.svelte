<script>
  import { t } from '$lib/i18n.svelte.js';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { formatBytes, formatRate } from '$lib/utils/format.js';

  let { disks, diskGroups } = $props();

  // Use diskGroups if available, otherwise fall back to disks
  const groups = $derived(diskGroups && diskGroups.length > 0 ? diskGroups : null);

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

<BlockCard bid="disks" title={t('host.diskUsage')}>
  <div class="space-y-4">
    {#if groups}
      {#each groups as group}
        <!-- Physical Disk Card -->
        <div class="border border-border rounded-lg overflow-hidden">
          <!-- Disk Header -->
          <div class="bg-muted/30 px-4 py-3 border-b border-border">
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-2">
                <Icon name="hard-drive" size={16} class="text-primary" />
                <span class="text-sm font-mono font-semibold">{group.device}</span>
                {#if group.partitions.length > 1}
                  <span class="text-xs px-1.5 py-0.5 rounded bg-accent/20 text-accent">
                    {group.partitions.length} {t('host.partitions')}
                  </span>
                {/if}
              </div>
              <div class="flex items-center gap-3">
                {#if group.usage_percent > 90}
                  <Icon name="alert-triangle" size={14} class="text-destructive" />
                {/if}
                <span class="text-sm font-mono tnum {getUsageTextColor(group.usage_percent)}">
                  {group.usage_percent.toFixed(1)}%
                </span>
              </div>
            </div>

            <!-- Disk Progress Bar -->
            <div class="h-2 bg-muted rounded-full overflow-hidden mt-2">
              <div
                class="h-full transition-all {getUsageColor(group.usage_percent)}"
                style="width: {Math.min(100, group.usage_percent)}%"
              ></div>
            </div>

            <!-- Disk Totals -->
            <div class="flex items-center justify-between text-xs mt-2">
              <span class="text-muted-foreground">
                {formatBytes(group.used_bytes)} / {formatBytes(group.total_bytes)}
              </span>
              <div class="flex items-center gap-4 font-mono">
                <span class="text-success">
                  R: {formatRate(group.read_bytes_per_sec)}
                </span>
                <span class="text-warning">
                  W: {formatRate(group.write_bytes_per_sec)}
                </span>
              </div>
            </div>
          </div>

          <!-- Partitions List -->
          <div class="divide-y divide-border">
            {#each group.partitions as partition}
              <div class="px-4 py-3">
                <!-- Partition Header -->
                <div class="flex items-center justify-between mb-2">
                  <div class="flex items-center gap-2">
                    <Icon name="layers" size={13} class="text-muted-foreground" />
                    <span class="text-xs font-mono font-medium text-foreground">{partition.device}</span>
                    <span class="text-xs text-muted-foreground">
                      {formatBytes(partition.used_bytes)} / {formatBytes(partition.total_bytes)}
                    </span>
                  </div>
                  <span class="text-xs font-mono tnum {getUsageTextColor(partition.usage_percent)}">
                    {partition.usage_percent.toFixed(1)}%
                  </span>
                </div>

                <!-- Partition Progress Bar -->
                <div class="h-1.5 bg-muted rounded-full overflow-hidden mb-2">
                  <div
                    class="h-full transition-all {getUsageColor(partition.usage_percent)}"
                    style="width: {Math.min(100, partition.usage_percent)}%"
                  ></div>
                </div>

                <!-- Mount Points -->
                <div class="space-y-1 ml-5">
                  {#each partition.mounts as mount, mountIdx}
                    {@const isPrimary = mountIdx === 0 && partition.mounts.reduce((minIdx, m, idx, arr) => m.path.length < arr[minIdx].path.length ? idx : minIdx, 0) === 0}
                    {@const primaryIdx = partition.mounts.reduce((minIdx, m, idx, arr) => m.path.length < arr[minIdx].path.length ? idx : minIdx, 0)}
                    {@const isPrimaryMount = mountIdx === primaryIdx}
                    <div class="flex items-center justify-between text-xs">
                      <div class="flex items-center gap-2">
                        <Icon name={isPrimaryMount ? "folder" : "link"} size={11} class={isPrimaryMount ? "text-muted-foreground" : "text-accent"} />
                        <span class="font-mono text-foreground">{mount.path}</span>
                        <span class="text-muted-foreground px-1 py-0.5 rounded bg-muted/50 text-[10px]">
                          {mount.fs_type}
                        </span>
                        {#if !isPrimaryMount}
                          <span class="text-[10px] text-accent">bind</span>
                        {/if}
                      </div>
                      {#if isPrimaryMount}
                        <span class="font-mono tnum text-muted-foreground">
                          {mount.usage_percent.toFixed(1)}%
                        </span>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            {/each}
          </div>
        </div>
      {/each}
    {:else if disks && disks.length > 0}
      <!-- Fallback: show individual disks if no grouping available -->
      {#each disks as disk}
        <div class="border border-border rounded-lg p-3">
          <div class="flex items-center justify-between mb-2">
            <div class="flex items-center gap-2">
              <Icon name="hard-drive" size={14} class="text-muted-foreground" />
              <span class="text-sm font-mono font-medium">{disk.device}</span>
              <span class="text-xs text-muted-foreground">{disk.mount}</span>
              <span class="text-xs px-1.5 py-0.5 rounded bg-muted text-muted-foreground">
                {disk.fs_type}
              </span>
            </div>
            <div class="flex items-center gap-3">
              {#if disk.usage_percent > 90}
                <Icon name="alert-triangle" size={14} class="text-destructive" />
              {/if}
              <span class="text-sm font-mono tnum {getUsageTextColor(disk.usage_percent)}">
                {disk.usage_percent.toFixed(1)}%
              </span>
            </div>
          </div>

          <div class="h-2 bg-muted rounded-full overflow-hidden mb-2">
            <div
              class="h-full transition-all {getUsageColor(disk.usage_percent)}"
              style="width: {Math.min(100, disk.usage_percent)}%"
            ></div>
          </div>

          <div class="flex items-center justify-between text-xs">
            <span class="text-muted-foreground">
              {formatBytes(disk.used_bytes)} / {formatBytes(disk.total_bytes)}
            </span>
            <div class="flex items-center gap-4 font-mono">
              <span class="text-success">
                R: {formatRate(disk.read_bytes_per_sec)}
              </span>
              <span class="text-warning">
                W: {formatRate(disk.write_bytes_per_sec)}
              </span>
            </div>
          </div>
        </div>
      {/each}
    {:else}
      <p class="text-sm text-muted-foreground text-center py-4">
        {t('host.noDisks')}
      </p>
    {/if}
  </div>
</BlockCard>
