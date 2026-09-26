<script>
  /**
   * VmReviewStep — final confirmation panel of the create wizard.
   *
   * Read-only on purpose: the last screen should be for checking, not
   * editing. Each earlier step gets a jump link so a correction is one
   * click away, which is what makes a stepped flow tolerable.
   */
  import Icon from '$lib/components/Icon.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let { rows = [], kindLabel = '', kindTheme = 'accent', steps = [], onedit = null } = $props();

  const badgeClass = $derived(
    kindTheme === 'warning'
      ? 'bg-warning/15 text-warning border-warning/30'
      : kindTheme === 'success'
        ? 'bg-success/15 text-success border-success/30'
        : 'bg-accent/15 text-accent border-accent/30'
  );

  /** Every step except this one (review is always last). */
  const editableSteps = $derived(steps.slice(0, -1));
</script>

<div class="space-y-5">
  <div class="rounded-xl border border-border bg-muted/20 overflow-hidden">
    <div class="flex items-center justify-between gap-2 px-4 py-2.5 border-b border-border/60">
      <span class="text-xs font-semibold text-foreground uppercase tracking-wider">
        {t('vmCreate.summaryTitle')}
      </span>
      <span class="text-[10px] px-1.5 py-0.5 rounded font-mono font-medium border {badgeClass}">
        {kindLabel}
      </span>
    </div>
    <dl class="divide-y divide-border/40">
      {#each rows as row (row.key)}
        <div class="flex items-center justify-between gap-3 px-4 py-2.5 text-sm">
          <dt class="text-muted-foreground shrink-0">{row.label}</dt>
          <dd class="font-medium text-foreground truncate text-right" title={row.value}>
            {row.value}
          </dd>
        </div>
      {/each}
    </dl>
  </div>

  {#if editableSteps.length > 0}
    <div class="space-y-2">
      <p class="text-xs font-semibold text-muted-foreground uppercase tracking-wider">
        {t('vmCreate.reviewEditTitle')}
      </p>
      <div class="flex flex-wrap gap-2">
        {#each editableSteps as s (s.id)}
          <button
            type="button"
            onclick={() => onedit?.(s.id)}
            class="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg border text-xs font-medium transition-colors cursor-pointer {s.hasError
              ? 'border-destructive/40 text-destructive bg-destructive/10 hover:bg-destructive/15'
              : 'border-border text-muted-foreground hover:text-foreground hover:bg-muted'}"
          >
            <Icon name="pencil" size={12} class="shrink-0" />
            <span>{s.label}</span>
            {#if s.hasError}
              <span class="w-1.5 h-1.5 rounded-full bg-destructive" aria-hidden="true"></span>
              <span class="sr-only">{t('vmCreate.stepHasError')}</span>
            {/if}
          </button>
        {/each}
      </div>
    </div>
  {/if}
</div>
