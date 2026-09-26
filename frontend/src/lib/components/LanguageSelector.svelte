<script>
  /**
   * LanguageSelector — scalable language picker supporting two layouts:
   *   - 'dropdown': compact trigger button with a floating popover list.
   *     Scales cleanly to 15+ languages with scroll. Supports `compact={true}`
   *     for icon-only triggers (e.g. collapsed sidebars or mobile bars).
   *   - 'cards': responsive card grid designed for the Account -> Appearance tab.
   */
  import { t, getLocale, setLocale, LOCALES } from '$lib/i18n.svelte.js';
  import * as DropdownMenu from '$lib/components/ui/dropdown-menu';
  import { Globe, Check, ChevronUp, ChevronDown } from '@lucide/svelte';
  import { cn } from '$lib/utils.js';

  let {
    variant = 'dropdown',
    compact = false,
    side = 'top',
    align = 'start',
    class: className = '',
  } = $props();

  const currentLocale = $derived(getLocale());
  const activeLocaleObj = $derived(LOCALES.find((l) => l.code === currentLocale) || LOCALES[0]);
</script>

{#if variant === 'cards'}
  <div class={cn('grid grid-cols-1 sm:grid-cols-3 gap-3', className)}>
    {#each LOCALES as l (l.code)}
      <button
        type="button"
        onclick={() => setLocale(l.code)}
        aria-pressed={currentLocale === l.code}
        class="relative flex items-center justify-between p-3.5 rounded-xl border text-left transition-all cursor-pointer group {currentLocale ===
        l.code
          ? 'border-accent bg-accent/5 ring-2 ring-accent/30 shadow-xs'
          : 'border-border bg-card/60 hover:bg-muted/40 hover:border-border-hover'}"
      >
        <div class="flex items-center gap-3 min-w-0">
          <span
            class="w-8 h-8 rounded-lg flex items-center justify-center text-xs font-bold font-mono uppercase shrink-0 transition-colors {currentLocale ===
            l.code
              ? 'bg-accent text-accent-foreground shadow-xs'
              : 'bg-muted text-muted-foreground group-hover:text-foreground'}"
          >
            {l.code}
          </span>
          <div class="min-w-0">
            <span class="block font-semibold text-sm text-foreground truncate">
              {l.label}
            </span>
            <span class="block text-[11px] text-muted-foreground font-mono">
              {l.code.toUpperCase()}
            </span>
          </div>
        </div>

        {#if currentLocale === l.code}
          <div
            class="w-5 h-5 rounded-full bg-accent text-accent-foreground flex items-center justify-center shrink-0 ml-2 shadow-xs"
          >
            <Check class="w-3 h-3" />
          </div>
        {/if}
      </button>
    {/each}
  </div>
{:else}
  <DropdownMenu.Root>
    <DropdownMenu.Trigger
      class={cn(
        'inline-flex items-center rounded-md text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring select-none cursor-pointer',
        compact
          ? 'w-9 h-9 justify-center text-muted-foreground hover:text-foreground hover:bg-muted'
          : 'w-full justify-between gap-2 px-2.5 py-1.5 text-muted-foreground hover:text-foreground hover:bg-muted border border-transparent hover:border-border/60',
        className
      )}
      title={t('common.language')}
      aria-label={t('common.language')}
    >
      <div class="flex items-center gap-2 min-w-0">
        <Globe class="w-4 h-4 shrink-0 text-muted-foreground/80" />
        {#if !compact}
          <span class="truncate font-medium">{activeLocaleObj.label}</span>
        {/if}
      </div>
      {#if !compact}
        {#if side === 'top'}
          <ChevronUp class="w-3.5 h-3.5 shrink-0 text-muted-foreground/60" />
        {:else}
          <ChevronDown class="w-3.5 h-3.5 shrink-0 text-muted-foreground/60" />
        {/if}
      {/if}
    </DropdownMenu.Trigger>

    <DropdownMenu.Content
      {side}
      {align}
      class="min-w-[12rem] max-h-64 overflow-y-auto p-1 shadow-lg border border-border bg-popover text-popover-foreground rounded-xl z-50"
    >
      <DropdownMenu.Group>
        <DropdownMenu.GroupHeading
          class="px-2 py-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground"
        >
          {t('common.language')}
        </DropdownMenu.GroupHeading>
        {#each LOCALES as l (l.code)}
          <DropdownMenu.Item
            onSelect={() => setLocale(l.code)}
            class="flex items-center justify-between px-2.5 py-1.5 text-xs rounded-lg cursor-pointer transition-colors {currentLocale ===
            l.code
              ? 'bg-accent/15 text-accent font-medium'
              : 'hover:bg-muted/80 text-foreground'}"
          >
            <div class="flex items-center gap-2 min-w-0">
              <span class="w-5 text-[10px] font-mono uppercase font-bold text-muted-foreground">
                {l.code}
              </span>
              <span class="truncate">{l.label}</span>
            </div>
            {#if currentLocale === l.code}
              <Check class="w-3.5 h-3.5 text-accent shrink-0 ml-1.5" />
            {/if}
          </DropdownMenu.Item>
        {/each}
      </DropdownMenu.Group>
    </DropdownMenu.Content>
  </DropdownMenu.Root>
{/if}
