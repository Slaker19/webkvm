<script>
  /**
   * Checkbox — accessible checkbox with label + description.
   *
   * Replaces the raw `<input type="checkbox">` calls in VmDetail,
   * Networks, Users. Bind `checked` to a $state boolean.
   *
   *   <Checkbox bind:checked={bootDisk} label="Boot from this disk" />
   */
  let {
    checked = $bindable(false),
    disabled = false,
    label = '',
    description = '',
    id = `cb-${Math.random().toString(36).slice(2, 9)}`,
  } = $props();
</script>

<label
  class="flex items-start gap-2 {disabled ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer'}"
>
  <input
    {id}
    type="checkbox"
    bind:checked
    {disabled}
    class="mt-0.5 h-4 w-4 rounded border-border bg-background text-accent focus:ring-2 focus:ring-accent/40 focus:ring-offset-0 disabled:cursor-not-allowed"
  />
  {#if label || description}
    <span class="flex-1 min-w-0">
      {#if label}
        <span class="text-sm font-medium block leading-tight">{label}</span>
      {/if}
      {#if description}
        <span class="text-xs text-muted-foreground block">{description}</span>
      {/if}
    </span>
  {/if}
</label>
