<script>
  import { quotaPercent, limitBelowUsage } from '$lib/utils/permissions.js';
  import { t } from '../i18n.svelte.js';

  /**
   * A single quota dimension: what is used, what the limit is, and
   * whether the limit being edited is already below current usage.
   *
   * Setting a limit used to be blind — the enforcement path computed
   * live usage to refuse requests, but nothing ever showed it, so an
   * admin capping "8 vCPU" could not see the user was already at 6.
   */
  let { label, used = 0, limit = 0, unit = '', format = null } = $props();

  const pct = $derived(quotaPercent(used, limit));
  const over = $derived(limitBelowUsage(used, limit));
  const unlimited = $derived(!limit || limit <= 0);
  const fmt = (v) => (format ? format(v) : `${v}${unit ? ' ' + unit : ''}`);
</script>

<div class="flex flex-col gap-1">
  <div class="flex items-baseline justify-between gap-2">
    <span class="text-[10px] text-muted-foreground uppercase tracking-wide">{label}</span>
    <span
      class="text-[11px] tabular-nums {over
        ? 'text-destructive font-medium'
        : 'text-muted-foreground'}"
    >
      {#if unlimited}
        {fmt(used)} <span class="text-muted-foreground/70">/ {t('users.quotaUnlimited')}</span>
      {:else}
        {fmt(used)} <span class="text-muted-foreground/70">/ {fmt(limit)}</span>
      {/if}
    </span>
  </div>
  <div class="h-1.5 rounded-full bg-muted overflow-hidden">
    {#if !unlimited}
      <div
        class="h-full rounded-full transition-all {over
          ? 'bg-destructive'
          : pct >= 80
            ? 'bg-amber-500'
            : 'bg-accent'}"
        style="width: {pct}%"
      ></div>
    {/if}
  </div>
  {#if over}
    <p class="text-[10px] text-destructive">{t('users.quotaBelowUsage')}</p>
  {/if}
</div>
