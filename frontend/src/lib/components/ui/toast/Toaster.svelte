<script>
  import { getToasts, dismiss } from '$lib/stores/toast.svelte.js';
  import { fly } from 'svelte/transition';

  // getToasts() returns the store's current array by value: a plain
  // `const toasts = getToasts()` captured that array ONCE at mount and
  // never saw a toast again, because toast.svelte.js's add()/dismiss()
  // REASSIGN the module-level $state variable to a brand new array
  // (`_toasts = [...]`) rather than mutating it in place — so this
  // component's snapshot never advanced past `[]`. $derived re-reads it
  // reactively (through the read of the $state var inside getToasts())
  // every time the store reassigns, so every toast.* call actually
  // renders.
  const toasts = $derived(getToasts());

  // Opaque backgrounds: the tint is mixed into the card colour instead of
  // being a 10%-alpha layer, which let the page (and the top bar) show
  // straight through an error toast.
  const typeStyles = {
    success:
      'border-success/40 bg-[color-mix(in_oklab,var(--success)_12%,var(--card))] text-foreground',
    error:
      'border-destructive/40 bg-[color-mix(in_oklab,var(--destructive)_12%,var(--card))] text-foreground',
    info: 'border-border bg-card text-foreground',
    warning:
      'border-warning/40 bg-[color-mix(in_oklab,var(--warning)_12%,var(--card))] text-foreground',
  };

  const typeIcons = {
    success: 'M5 13l4 4L19 7',
    error: 'M6 18L18 6M6 6l12 12',
    info: 'M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z',
    warning:
      'M12 9v2m0 4h.01M5.07 19h13.86c1.54 0 2.5-1.67 1.73-3L13.73 4a2 2 0 00-3.46 0L3.34 16c-.77 1.33.19 3 1.73 3z',
  };

  const typeColors = {
    success: 'text-success',
    error: 'text-destructive',
    info: 'text-foreground',
    warning: 'text-warning',
  };
</script>

<div
  role="status"
  aria-live="polite"
  aria-atomic="false"
  class="fixed top-[calc(3.5rem+env(safe-area-inset-top,0px))] right-4 left-4 sm:left-auto sm:right-4 z-[100] flex flex-col items-end gap-2 pointer-events-none"
>
  <!-- Anchored just below the 3rem top bar (plus the notch inset) so a
       toast never covers the page title, menu button or theme toggle. -->
  {#each toasts as t (t.id)}
    <div
      transition:fly={{ y: -20, duration: 150 }}
      role={t.type === 'error' ? 'alert' : 'status'}
      class="pointer-events-auto flex items-start gap-3 w-full sm:w-auto min-w-0 sm:min-w-[280px] max-w-sm px-3.5 py-3 rounded-lg border shadow-lg {typeStyles[
        t.type
      ]}"
    >
      <svg
        class="w-4 h-4 mt-0.5 shrink-0 {typeColors[t.type]}"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        viewBox="0 0 24 24"
      >
        <path stroke-linecap="round" stroke-linejoin="round" d={typeIcons[t.type]} />
      </svg>
      <div class="flex-1 min-w-0">
        {#if t.title}
          <p class="text-sm font-medium">{t.title}</p>
        {/if}
        <p class="text-sm text-muted-foreground break-words">{t.message}</p>
      </div>
      <button
        onclick={() => dismiss(t.id)}
        class="text-muted-foreground hover:text-foreground transition-colors shrink-0 -mr-1 -mt-1 p-1"
        aria-label="Dismiss"
      >
        <svg
          class="w-3.5 h-3.5"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          viewBox="0 0 24 24"
        >
          <path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" />
        </svg>
      </button>
    </div>
  {/each}
</div>
