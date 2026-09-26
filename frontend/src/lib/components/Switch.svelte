<script>
  /**
   * Switch — iOS-style toggle.
   *
   * Replaces the bespoke indigo pill toggles in VmCreate + VmDetail
   * Identity dialog. Bind `checked` to a $state boolean.
   *
   *   <Switch bind:checked={secureBoot} label="Enable Secure Boot" />
   *
   * For controlled toggles (where the parent's value is the source
   * of truth and a server round-trip is needed on every change),
   * pass `checked` WITHOUT bind plus an `onchange` callback. In that
   * mode the Switch never writes `checked` itself: it shows the
   * requested value optimistically while `onchange(nextValue)` runs
   * (awaiting it if it returns a promise) and then renders whatever
   * `checked` the parent ended up with. So a parent that rolls its
   * state back on failure — even to the very same value it already
   * had — sees the toggle snap back, instead of staying flipped.
   * Returning `false` or throwing/rejecting also counts as "rejected".
   *
   *   <Switch
   *     checked={vm.autostart}
   *     onchange={async (v) => { await api.setAutostart(vm.id, v); vm.autostart = v; }}
   *     label="Start on host boot"
   *   />
   */
  let {
    checked = $bindable(false),
    disabled = false,
    label = '',
    ariaLabel = '',
    description = '',
    size = 'md', // sm | md
    onchange,
    id = `switch-${Math.random().toString(36).slice(2, 9)}`,
  } = $props();

  // Optimistic value shown while an async `onchange` is in flight
  // (null = show the `checked` prop as-is).
  let pending = $state(null);
  const on = $derived(pending ?? checked);

  const dim = $derived(size === 'sm' ? 'w-7 h-4' : 'w-9 h-5');
  const dot = $derived(size === 'sm' ? 'w-3 h-3' : 'w-4 h-4');
  const offset = $derived(
    on ? (size === 'sm' ? 'translate-x-3' : 'translate-x-4') : 'translate-x-0'
  );

  async function handleClick() {
    if (disabled || pending !== null) return;
    if (!onchange) {
      // Uncontrolled / bind:checked — flip and let the binding carry it.
      checked = !checked;
      return;
    }
    // Controlled: ask the parent; never mutate `checked` locally, which
    // would shadow the prop and ignore a same-value rollback.
    const next = !checked;
    pending = next;
    try {
      await onchange(next);
    } catch {
      // Rejected — the parent is responsible for reporting the error.
    } finally {
      pending = null;
    }
  }
</script>

{#if label || description}
  <label
    class="inline-flex items-start gap-2.5 {disabled
      ? 'opacity-50 cursor-not-allowed'
      : 'cursor-pointer'}"
  >
    <button
      {id}
      type="button"
      role="switch"
      aria-checked={on}
      aria-label={ariaLabel || label || undefined}
      {disabled}
      onclick={handleClick}
      class="relative inline-flex shrink-0 {dim} rounded-full transition-colors {on
        ? 'bg-accent'
        : 'bg-muted'}"
    >
      <span
        class="absolute top-0.5 left-0.5 {dot} bg-white rounded-full shadow transition-transform {offset}"
      ></span>
    </button>
    <span class="flex-1 min-w-0">
      {#if label}
        <span class="text-sm font-medium block">{label}</span>
      {/if}
      {#if description}
        <span class="text-xs text-muted-foreground block">{description}</span>
      {/if}
    </span>
  </label>
{:else}
  <button
    {id}
    type="button"
    role="switch"
    aria-checked={on}
    aria-label={ariaLabel || undefined}
    {disabled}
    onclick={handleClick}
    class="relative inline-flex shrink-0 {dim} rounded-full transition-colors {on
      ? 'bg-accent'
      : 'bg-muted'} {disabled ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer'}"
  >
    <span
      class="absolute top-0.5 left-0.5 {dot} bg-white rounded-full shadow transition-transform {offset}"
    ></span>
  </button>
{/if}
