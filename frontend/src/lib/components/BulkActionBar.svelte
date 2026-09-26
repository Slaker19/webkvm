<script>
  import { t } from '../i18n.svelte.js';
  import { Button } from './ui/button';

  /**
   * @typedef {Object} BulkAction
   * @property {string} key
   * @property {string} label
   * @property {'default'|'outline'|'destructive'} [variant]
   * @property {() => void} onClick
   */
  /**
   * @typedef {Object} Props
   * @property {number} count
   * @property {BulkAction[]} [actions]
   * @property {() => void} onClear
   * @property {boolean} [visible] - force-show the bar even at count 0
   *   (used while select mode is on, so "select all" is reachable
   *   before anything has been picked).
   * @property {number} [visibleCount] - how many rows are currently
   *   selectable, for the "N of M" hint.
   * @property {boolean} [allSelected]
   * @property {() => void} [onSelectAll]
   * @property {() => void} [onInvert]
   */
  /** @type {Props} */
  let {
    count,
    actions = [],
    onClear,
    visible = false,
    visibleCount = 0,
    allSelected = false,
    onSelectAll,
    onInvert,
  } = $props();
</script>

{#if count > 0 || visible}
  <div
    class="flex flex-wrap items-center justify-between gap-2 mb-3 px-3 py-2 border border-accent/30 bg-accent/10 rounded-md"
  >
    <div class="flex items-center gap-2 flex-wrap">
      <span class="text-sm font-medium text-accent">
        {count > 0 ? t('vms.selected', { n: count }) : t('vms.selectNone')}
        {#if visibleCount > 0}
          <span class="text-accent/60 font-normal">/ {visibleCount}</span>
        {/if}
      </span>
      {#if onSelectAll}
        <button
          type="button"
          onclick={onSelectAll}
          disabled={allSelected}
          class="text-xs px-2 py-1 rounded border border-accent/30 text-accent hover:bg-accent/15 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {t('vms.selectAll')}
        </button>
      {/if}
      {#if onInvert}
        <button
          type="button"
          onclick={onInvert}
          class="text-xs px-2 py-1 rounded border border-accent/30 text-accent hover:bg-accent/15 transition-colors"
        >
          {t('vms.invertSelection')}
        </button>
      {/if}
    </div>
    <div class="flex items-center gap-1.5 flex-wrap">
      {#each actions as a (a.key)}
        <Button
          size="sm"
          variant={a.variant || 'outline'}
          disabled={count === 0}
          onclick={a.onClick}>{a.label}</Button
        >
      {/each}
      <button
        onclick={onClear}
        class="text-xs text-muted-foreground hover:text-foreground px-2 py-1"
        >{t('vms.clear')}</button
      >
    </div>
  </div>
{/if}
