<script>
  /**
   * ProgressBar — linear progress with optional indeterminate mode.
   *
   * Replaces bespoke progress UI in VmList (import), Storage
   * (upload/download), VmDetail (export). Pass a 0-100 value; omit
   * `value` for indeterminate (animated striped bar).
   *
   *   <ProgressBar value={67} label="Uploading..." />
   *   <ProgressBar indeterminate label="Preparing..." />
   */
  let {
    value, // 0-100; if undefined, indeterminate
    label = '',
    showValue = false,
    size = 'md', // sm | md | lg
    variant = 'accent', // accent | success | warning | destructive
    class: className = '',
  } = $props();

  const heightClass = $derived({ sm: 'h-1', md: 'h-1.5', lg: 'h-2.5' }[size] || 'h-1.5');

  const colorClass = $derived(
    {
      accent: 'bg-accent',
      success: 'bg-success',
      warning: 'bg-warning',
      destructive: 'bg-destructive',
    }[variant] || 'bg-accent'
  );

  const pct = $derived(Math.max(0, Math.min(100, value ?? 0)));
</script>

<div class={className}>
  {#if label || showValue}
    <div class="flex items-center justify-between mb-1 text-xs text-muted-foreground">
      <span>{label}</span>
      {#if showValue && value != null}
        <span class="tnum">{Math.round(pct)}%</span>
      {/if}
    </div>
  {/if}
  <div
    role="progressbar"
    aria-label={label || 'Progress'}
    aria-valuenow={value != null ? Math.round(pct) : undefined}
    aria-valuemin="0"
    aria-valuemax="100"
    class="w-full {heightClass} rounded-full bg-muted overflow-hidden"
  >
    {#if value == null}
      <div class="h-full w-full {colorClass} opacity-50 animate-progress-indeterminate"></div>
    {:else}
      <div class="h-full {colorClass} transition-all" style="width: {pct}%"></div>
    {/if}
  </div>
</div>
