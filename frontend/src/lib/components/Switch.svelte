<script>
  /**
   * Switch — iOS-style toggle.
   *
   * Replaces the bespoke indigo pill toggles in VmCreate + VmDetail
   * Identity dialog. Bind `checked` to a $state boolean.
   *
   *   <Switch bind:checked={secureBoot} label="Enable Secure Boot" />
   *
   * For read-only toggles (where the parent's value is the source
   * of truth and a server round-trip is needed on every change),
   * use the `onchange` callback — the Switch flips the visual
   * state locally and then asks the parent to confirm via
   * `onchange(nextValue)`. The parent re-renders with the new
   * `checked` prop (or restores the old one on failure).
   *
   *   <Switch
   *     checked={vm.autostart}
   *     onchange={async (v) => { await api.setAutostart(vm.id, v); }}
   *     label="Start on host boot"
   *   />
   */
  let {
    checked = $bindable(false),
    disabled = false,
    label = '',
    description = '',
    size = 'md', // sm | md
    onchange,
    id = `switch-${Math.random().toString(36).slice(2, 9)}`,
  } = $props();

  const dim = $derived(size === 'sm' ? 'w-7 h-4' : 'w-9 h-5');
  const dot = $derived(size === 'sm' ? 'w-3 h-3' : 'w-4 h-4');
  const offset = $derived(
    checked ? (size === 'sm' ? 'translate-x-3' : 'translate-x-4') : 'translate-x-0'
  );

  function handleClick() {
    if (disabled) return;
    // Always flip the visual state. The parent can re-set
    // `checked` (via prop re-render) to override on failure.
    checked = !checked;
    if (onchange) onchange(checked);
  }
</script>

<label
  class="flex items-start gap-2.5 {disabled ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer'}"
>
  <button
    {id}
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={label || undefined}
    {disabled}
    onclick={handleClick}
    class="relative inline-flex shrink-0 {dim} rounded-full transition-colors {checked
      ? 'bg-accent'
      : 'bg-muted'}"
  >
    <span
      class="absolute top-0.5 left-0.5 {dot} bg-white rounded-full shadow transition-transform {offset}"
    ></span>
  </button>
  {#if label || description}
    <span class="flex-1 min-w-0">
      {#if label}
        <span class="text-sm font-medium block">{label}</span>
      {/if}
      {#if description}
        <span class="text-xs text-muted-foreground block">{description}</span>
      {/if}
    </span>
  {/if}
</label>
