<script>
  /**
   * StorageBreakdownBar — a segmented usage bar for a storage pool.
   *
   * Replaces the single opaque "62% used" progress bar with a split of
   * what is actually consuming the pool, so an operator can tell at a
   * glance whether they're out of space because of VM disks, a pile of
   * forgotten ISOs, or backups that were never pruned.
   *
   *   <StorageBreakdownBar breakdown={b} />
   *
   * Segments with zero bytes are skipped entirely (no zero-width slivers
   * or pointless legend entries).
   */
  import { t } from '../i18n.svelte.js';
  import { formatBytes } from '../utils/format.js';

  /**
   * @typedef {Object} Props
   * @property {{disk_bytes:number, iso_bytes:number, backup_bytes:number,
   *   other_bytes:number, free_bytes:number, capacity:number}} breakdown
   * @property {boolean} [showLegend]
   */
  /** @type {Props} */
  let { breakdown, showLegend = true } = $props();

  const segments = $derived(
    [
      { key: 'disk', bytes: breakdown?.disk_bytes || 0, color: 'var(--accent)' },
      { key: 'iso', bytes: breakdown?.iso_bytes || 0, color: 'var(--warning)' },
      { key: 'backup', bytes: breakdown?.backup_bytes || 0, color: 'var(--success)' },
      { key: 'other', bytes: breakdown?.other_bytes || 0, color: 'var(--muted-foreground)' },
    ].filter((s) => s.bytes > 0)
  );

  const used = $derived(segments.reduce((sum, s) => sum + s.bytes, 0));

  // Prefer the pool's real capacity. Fall back to used+free so the bar
  // still renders sensibly for a pool whose capacity libvirt couldn't
  // report (inactive/unreachable netfs).
  const total = $derived(
    breakdown?.capacity > 0 ? breakdown.capacity : used + (breakdown?.free_bytes || 0)
  );

  const pctOf = (bytes) => (total > 0 ? (bytes / total) * 100 : 0);
</script>

{#if total > 0}
  <div class="space-y-1.5">
    <div
      class="flex h-2 w-full rounded-full overflow-hidden bg-muted"
      title={t('storage.usedSpace')}
    >
      {#each segments as seg (seg.key)}
        <div
          class="h-full first:rounded-l-full transition-[width] duration-300"
          style="width:{pctOf(seg.bytes)}%;background:{seg.color}"
          title="{t(`storage.seg${seg.key[0].toUpperCase()}${seg.key.slice(1)}`)}: {formatBytes(
            seg.bytes
          )}"
        ></div>
      {/each}
    </div>

    {#if showLegend}
      <div class="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[10px] text-muted-foreground">
        {#each segments as seg (seg.key)}
          <span class="inline-flex items-center gap-1">
            <span class="w-2 h-2 rounded-sm shrink-0" style="background:{seg.color}"></span>
            {t(`storage.seg${seg.key[0].toUpperCase()}${seg.key.slice(1)}`)}
            <span class="font-mono">{formatBytes(seg.bytes)}</span>
          </span>
        {/each}
        <span class="inline-flex items-center gap-1 ml-auto">
          <span class="w-2 h-2 rounded-sm shrink-0 bg-muted"></span>
          {t('storage.segFree')}
          <span class="font-mono">{formatBytes(breakdown?.free_bytes || 0)}</span>
        </span>
      </div>
    {/if}
  </div>
{/if}
