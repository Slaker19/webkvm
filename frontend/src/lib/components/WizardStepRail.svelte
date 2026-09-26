<script>
  /**
   * WizardStepRail — section navigator for long single-page forms.
   *
   * Renders the same step list twice from one source of truth: a
   * horizontal scroller below `lg` and a sticky vertical rail from `lg`
   * up. Steps are anchors, not gates — clicking scrolls to the section
   * but never blocks the rest of the form.
   *
   * These are in-page links, not tabs: every section stays mounted and
   * visible, so the active step is marked with `aria-current` and the
   * buttons keep their natural tab order.
   *
   * Each step: { id, label, num, hasError }.
   */
  import { t } from '$lib/i18n.svelte.js';

  let { steps = [], activeId = '', onselect = null } = $props();
</script>

<!-- Horizontal strip: phones and tablets -->
<nav class="lg:hidden overflow-x-auto pb-1 scrollbar-none" aria-label={t('vmCreate.stepsNavLabel')}>
  <ul class="flex items-center gap-1.5 list-none m-0 p-0">
    {#each steps as s (s.id)}
      {@const isActive = activeId === s.id}
      <li class="shrink-0">
        <button
          type="button"
          aria-current={isActive ? 'true' : undefined}
          onclick={() => onselect?.(s.id)}
          class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium border transition-all cursor-pointer {isActive
            ? 'bg-accent text-accent-foreground border-accent shadow-xs'
            : s.hasError
              ? 'bg-destructive/10 text-destructive border-destructive/40'
              : 'bg-muted/40 text-muted-foreground border-border hover:text-foreground hover:bg-muted'}"
        >
          <span
            class="w-4 h-4 rounded-full flex items-center justify-center text-[10px] font-bold {isActive
              ? 'bg-black/20 text-white'
              : 'bg-muted text-muted-foreground'}"
            aria-hidden="true"
          >
            {s.num}
          </span>
          <span>{s.label}</span>
          {#if s.hasError}
            <span class="w-1.5 h-1.5 rounded-full bg-destructive" aria-hidden="true"></span>
            <span class="sr-only">{t('vmCreate.stepHasError')}</span>
          {/if}
        </button>
      </li>
    {/each}
  </ul>
</nav>

<!-- Vertical rail: desktop. The outer wrapper is the grid cell and must
     stretch, otherwise `sticky` has no room to travel and the rail just
     scrolls away with the form. -->
<div class="hidden lg:block">
  <nav class="sticky top-4" aria-label={t('vmCreate.stepsNavLabel')}>
    <ul class="space-y-1 list-none m-0 p-0">
      {#each steps as s (s.id)}
        {@const isActive = activeId === s.id}
        <li>
          <button
            type="button"
            aria-current={isActive ? 'true' : undefined}
            onclick={() => onselect?.(s.id)}
            class="w-full text-left text-xs font-medium px-3 py-2 rounded-lg transition-all flex items-center justify-between gap-2 cursor-pointer {isActive
              ? 'bg-accent/15 text-accent border border-accent/30 font-semibold shadow-xs'
              : s.hasError
                ? 'bg-destructive/10 text-destructive border border-destructive/30 hover:bg-destructive/15'
                : 'text-muted-foreground hover:text-foreground hover:bg-muted border border-transparent'}"
          >
            <span class="flex items-center gap-2 truncate">
              <span
                class="w-4 h-4 rounded-full flex items-center justify-center text-[10px] font-bold shrink-0 {isActive
                  ? 'bg-accent text-accent-foreground'
                  : 'bg-muted text-muted-foreground'}"
                aria-hidden="true"
              >
                {s.num}
              </span>
              <span class="truncate">{s.label}</span>
            </span>
            {#if s.hasError}
              <span class="w-2 h-2 rounded-full bg-destructive shrink-0" aria-hidden="true"></span>
              <span class="sr-only">{t('vmCreate.stepHasError')}</span>
            {/if}
          </button>
        </li>
      {/each}
    </ul>
  </nav>
</div>
