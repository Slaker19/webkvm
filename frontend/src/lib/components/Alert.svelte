<script>
  /**
   * Alert — colored banner for inline status messages.
   *
   * Variants: error, warning, success, info. Replaces the half-dozen
   * bespoke `<div class="mb-4 p-3 border border-destructive/30 ...">`
   * red/green banners.
   *
   *   <Alert variant="error">Could not save</Alert>
   *   <Alert variant="warning" title="Heads up">…</Alert>
   *   <Alert variant="success" dismissible>Done.</Alert>
   */
  import { cn } from '$lib/utils.js';

  let {
    variant = 'info', // error | warning | success | info
    title = '',
    dismissible = false,
    class: className = '',
    children,
  } = $props();

  const variantClass = $derived(
    {
      error: 'border-destructive/30 bg-destructive/10 text-destructive',
      warning: 'border-warning/30 bg-warning/10 text-warning',
      success: 'border-success/30 bg-success/10 text-success',
      info: 'border-info/30 bg-info/10 text-info',
    }[variant] || 'border-info/30 bg-info/10 text-info'
  );

  const iconPath = $derived(
    {
      error: 'M10 18a8 8 0 1 1 16 0 8 8 0 0 1-16 0M11 6h2v8h-2zM11 16h2v2h-2z',
      warning:
        'M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0zM12 9v4M12 17h.01',
      success: 'M10 18a8 8 0 1 1 16 0 8 8 0 0 1-16 0M9 14l2 2 4-4',
      info: 'M10 18a8 8 0 1 1 16 0 8 8 0 0 1-16 0M11 11h2v6h-2zM11 7h2v2h-2z',
    }[variant] || ''
  );

  let dismissed = $state(false);

  function close() {
    dismissed = true;
  }
</script>

{#if !dismissed}
  <div
    role={variant === 'error' ? 'alert' : 'status'}
    aria-live={variant === 'error' ? 'assertive' : 'polite'}
    class={cn('flex items-start gap-2 p-3 border rounded-md text-sm', variantClass, className)}
  >
    <svg
      class="w-4 h-4 mt-0.5 shrink-0"
      fill="none"
      stroke="currentColor"
      stroke-width="2"
      viewBox="0 0 32 32"
    >
      <path d={iconPath} />
    </svg>
    <div class="flex-1 min-w-0">
      {#if title}
        <div class="font-semibold mb-0.5">{title}</div>
      {/if}
      {@render children?.()}
    </div>
    {#if dismissible}
      <button
        type="button"
        onclick={close}
        class="shrink-0 opacity-60 hover:opacity-100 -mt-0.5 -mr-1 p-1"
        aria-label="Dismiss"
      >
        <svg
          class="w-3.5 h-3.5"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          viewBox="0 0 24 24"
        >
          <line x1="18" y1="6" x2="6" y2="18" />
          <line x1="6" y1="6" x2="18" y2="18" />
        </svg>
      </button>
    {/if}
  </div>
{/if}
