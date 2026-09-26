<script>
  let { label, helper = '', error = '', stacked = false, align = 'right', children } = $props();
</script>

<div
  class="grid grid-cols-1 {stacked
    ? 'gap-2'
    : 'sm:grid-cols-[170px_minmax(0,1fr)] gap-3 sm:gap-5'} py-3 border-b border-border/30 last:border-b-0"
>
  <div class="flex flex-col justify-start">
    <div class="text-sm font-medium text-foreground">{label}</div>
    {#if helper}
      <p class="text-xs text-muted-foreground mt-0.5 leading-relaxed">{helper}</p>
    {/if}
    {#if error}
      <p class="text-xs text-destructive mt-1">{error}</p>
    {/if}
  </div>
  <div
    class="flex min-w-0 {align === 'right' && !stacked
      ? 'items-center justify-end sm:text-right'
      : 'flex-col items-start justify-start w-full'}"
  >
    {@render children?.()}
  </div>
</div>
