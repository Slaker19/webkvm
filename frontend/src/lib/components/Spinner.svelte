<script>
  /**
   * Spinner — animated loading indicator.
   *
   * Replaces the ~30 bespoke `<div class="spinner !w-X !h-Y !border-…">`
   * instances scattered across the app. Size and color are props so
   * the call site can stay terse.
   *
   *   <Spinner />
   *   <Spinner size="sm" />
   *   <Spinner size="md" color="text-white" />
   */
  let {
    size = 'md', // xs | sm | md | lg | xl
    color = 'text-accent', // any text-* color (sets the border base)
    class: className = '',
    label = 'Loading', // aria-label
  } = $props();

  const sizeClass = $derived(
    {
      xs: 'w-3 h-3 border',
      sm: 'w-4 h-4 border-2',
      md: 'w-5 h-5 border-2',
      lg: 'w-8 h-8 border-[3px]',
      xl: 'w-12 h-12 border-4',
    }[size] || 'w-5 h-5 border-2'
  );
</script>

<span
  role="status"
  aria-label={label}
  class="inline-block rounded-full border-solid border-t-transparent animate-spin {color} {sizeClass} {className}"
></span>
