<script>
  /**
   * Gauge — a 0-100% load indicator in one of two shapes.
   *
   * The shape follows the user's global preference (see
   * lib/stores/gaugeStyle.svelte.js) unless a `variant` is passed
   * explicitly. Colour is the shared traffic-light scale, so the same
   * value reads the same whichever shape is active.
   *
   *   <Gauge value={cpuPct} label="CPU" />
   *   <Gauge value={ramPct} label="RAM" variant="linear" size={40} />
   */
  import { gaugeStyle, loadColor } from '../stores/gaugeStyle.svelte.js';

  /**
   * @typedef {Object} Props
   * @property {number} value - 0-100
   * @property {string} [label] - short caption ("CPU", "RAM")
   * @property {'radial'|'linear'} [variant] - override the global style
   * @property {number} [size] - radial diameter in px
   * @property {boolean} [showValue]
   * @property {string} [sublabel] - extra text under the value (e.g. "2.1 GiB")
   */
  /** @type {Props} */
  let {
    value = 0,
    label = '',
    variant = null,
    size = 52,
    showValue = true,
    sublabel = '',
  } = $props();

  const mode = $derived(variant || gaugeStyle.mode);
  // Clamp: metrics can briefly exceed 100 (multi-core CPU accounting)
  // or go slightly negative on a counter reset, and either would draw
  // a broken arc.
  const pct = $derived(Math.max(0, Math.min(100, Number(value) || 0)));
  const color = $derived(loadColor(pct));

  // Radial geometry. stroke-dasharray/offset draws the filled portion
  // of the ring; rotating -90deg puts 0% at 12 o'clock instead of 3.
  const radius = $derived((size - 6) / 2);
  const circumference = $derived(2 * Math.PI * radius);
  const dashOffset = $derived(circumference * (1 - pct / 100));
</script>

{#if mode === 'radial'}
  <div
    class="inline-flex flex-col items-center gap-0.5"
    role="meter"
    aria-valuenow={Math.round(pct)}
    aria-valuemin="0"
    aria-valuemax="100"
    aria-label={label || 'load'}
  >
    <div class="relative" style="width:{size}px;height:{size}px">
      <svg width={size} height={size} viewBox="0 0 {size} {size}" class="-rotate-90">
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          fill="none"
          stroke="var(--muted)"
          stroke-width="4"
        />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          fill="none"
          stroke={color}
          stroke-width="4"
          stroke-linecap="round"
          stroke-dasharray={circumference}
          stroke-dashoffset={dashOffset}
          style="transition: stroke-dashoffset 400ms ease, stroke 300ms ease"
        />
      </svg>
      {#if showValue}
        <div class="absolute inset-0 flex items-center justify-center">
          <span class="text-[11px] font-semibold tnum leading-none" style="color:{color}">
            {Math.round(pct)}<span class="text-[8px] opacity-70">%</span>
          </span>
        </div>
      {/if}
    </div>
    {#if label}
      <span class="text-[10px] font-medium text-muted-foreground uppercase tracking-wider"
        >{label}</span
      >
    {/if}
    {#if sublabel}
      <span class="text-[10px] text-muted-foreground tnum">{sublabel}</span>
    {/if}
  </div>
{:else}
  <div
    class="w-full min-w-0"
    role="meter"
    aria-valuenow={Math.round(pct)}
    aria-valuemin="0"
    aria-valuemax="100"
    aria-label={label || 'load'}
  >
    <div class="flex items-baseline justify-between gap-2 mb-1">
      {#if label}
        <span class="text-[10px] font-medium text-muted-foreground uppercase tracking-wider"
          >{label}</span
        >
      {/if}
      {#if showValue}
        <span class="text-[11px] font-semibold tnum leading-none" style="color:{color}">
          {Math.round(pct)}%
        </span>
      {/if}
    </div>
    <div class="h-1.5 w-full rounded-full bg-muted overflow-hidden">
      <div
        class="h-full rounded-full"
        style="width:{pct}%;background:{color};transition:width 400ms ease,background 300ms ease"
      ></div>
    </div>
    {#if sublabel}
      <span class="text-[10px] text-muted-foreground tnum mt-0.5 block">{sublabel}</span>
    {/if}
  </div>
{/if}
