<script>
  /**
   * StatCard — small label + value card used in the System page.
   *
   * Renders a status dot in the accent color of `status` (running /
   * paused / shutoff / crashed) or a generic info dot. Designed to
   * be used inside a CSS grid; the parent controls width.
   */
  /**
   * @typedef {'running' | 'paused' | 'shutoff' | 'crashed' | 'info'} StatusKind
   */

  /**
   * @typedef {Object} Props
   * @property {string} label
   * @property {string | import('svelte').Snippet} value
   * @property {string} [hint]
   * @property {StatusKind} [status]
   * @property {import('svelte').Snippet} [chart] - optional sparkline
   *   (or any small visual) rendered below the hint line.
   */

  import { stateDotClass } from '$lib/utils/vmState.js';

  /** @type {Props} */
  let { label, value, hint = '', status = 'info', chart } = $props();

  const dotClass = $derived(status === 'info' ? 'bg-muted-foreground' : stateDotClass(status));
</script>

<div
  class="border border-border rounded-lg bg-card p-4 transition-[border-color,box-shadow,transform] duration-200 ease-out hover:border-border-hover hover:shadow-md"
>
  <div class="flex items-center gap-2 mb-2">
    <span class="status-dot {dotClass}"></span>
    <span class="text-xs text-muted-foreground font-medium">{label}</span>
  </div>
  <div class="text-base font-semibold tnum">
    {#if typeof value === 'string'}
      {value}
    {:else}
      {@render value()}
    {/if}
  </div>
  {#if hint}
    <div class="text-xs text-muted-foreground mt-1">{hint}</div>
  {/if}
  {#if chart}
    <div class="mt-2">{@render chart()}</div>
  {/if}
</div>
