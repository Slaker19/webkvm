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
   */
  /** @type {Props} */
  let { count, actions = [], onClear } = $props();
</script>

{#if count > 0}
  <div
    class="flex items-center justify-between gap-2 mb-3 px-3 py-2 border border-accent/30 bg-accent/10 rounded-md"
  >
    <span class="text-sm font-medium text-accent">
      {t('vms.selected', { n: count })}
    </span>
    <div class="flex items-center gap-1.5">
      {#each actions as a (a.key)}
        <Button size="sm" variant={a.variant || 'outline'} onclick={a.onClick}>{a.label}</Button>
      {/each}
      <button
        onclick={onClear}
        class="text-xs text-muted-foreground hover:text-foreground px-2 py-1"
        >{t('vms.clear')}</button
      >
    </div>
  </div>
{/if}
