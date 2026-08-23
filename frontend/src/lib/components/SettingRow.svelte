<script>
  let { label, helper = '', error = '', children } = $props();
</script>

<div class="grid grid-cols-1 sm:grid-cols-[200px_1fr] gap-3 sm:gap-6 py-3">
  <div class="flex flex-col">
    <div class="text-sm font-medium text-foreground">{label}</div>
    {#if helper}
      <p class="text-xs text-muted-foreground mt-0.5">{helper}</p>
    {/if}
    {#if error}
      <p class="text-xs text-destructive mt-1">{error}</p>
    {/if}
  </div>
  <div class="flex items-center justify-end min-w-0">
    {@render children?.()}
  </div>
</div>
