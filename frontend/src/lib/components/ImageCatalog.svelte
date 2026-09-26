<script>
  import Icon from '$lib/components/Icon.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let {
    items = [],
    selectedId = $bindable(''),
    categories = [],
    selectedCategory = $bindable('all'),
    searchQuery = $bindable(''),
    searchPlaceholder = '',
    emptyMessage = '',
    theme = 'accent', // 'accent' | 'success'
    onselect = null,
    limitInitial = 12,
  } = $props();

  let showAll = $state(false);

  const isSuccess = $derived(theme === 'success');
  const accentBorder = $derived(isSuccess ? 'border-success' : 'border-accent');
  const accentBg = $derived(isSuccess ? 'bg-success/10' : 'bg-accent/10');
  const accentRing = $derived(isSuccess ? 'ring-2 ring-success/40' : 'ring-2 ring-accent/40');
  const accentText = $derived(isSuccess ? 'text-success' : 'text-accent');
  const accentBadgeBg = $derived(
    isSuccess
      ? 'bg-success/10 text-success border-success/20'
      : 'bg-accent/10 text-accent border-accent/20'
  );

  function getItemId(item) {
    return item.ref || item.id || '';
  }

  function getItemName(item) {
    return item.label || item.name || getItemId(item);
  }

  function getItemDesc(item) {
    return item.description || item.desc || '';
  }

  function formatRam(mb) {
    if (!mb) return '2 GB';
    return mb >= 1024 ? `${(mb / 1024).toFixed(0)} GB` : `${mb} MB`;
  }

  function resetFilters() {
    searchQuery = '';
    selectedCategory = 'all';
  }

  const displayedItems = $derived.by(() => {
    if (showAll || items.length <= limitInitial) {
      return items;
    }
    return items.slice(0, limitInitial);
  });
</script>

<div class="space-y-3">
  <!-- Barra de búsqueda y Categorías -->
  <div class="space-y-2">
    <!-- Buscador -->
    <div class="relative w-full">
      <input
        type="text"
        bind:value={searchQuery}
        placeholder={searchPlaceholder || t('common.search')}
        class="input w-full pl-9 pr-9 text-xs h-9 bg-background/80"
      />
      <div
        class="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
      >
        <Icon name="search" size={14} />
      </div>
      {#if searchQuery}
        <button
          type="button"
          onclick={() => (searchQuery = '')}
          class="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground p-0.5 rounded cursor-pointer"
          aria-label={t('common.clearSearch')}
        >
          <Icon name="x" size={14} />
        </button>
      {/if}
    </div>

    <!-- Píldoras de Categoría -->
    {#if categories.length > 0}
      <div
        role="tablist"
        aria-label="Categorías"
        class="flex items-center gap-1.5 overflow-x-auto pb-1 scrollbar-none"
      >
        {#each categories as cat (cat.id)}
          {@const isActive = selectedCategory === cat.id}
          <button
            type="button"
            role="tab"
            aria-selected={isActive}
            onclick={() => (selectedCategory = cat.id)}
            class="px-2.5 py-1.5 rounded-lg text-xs font-medium whitespace-nowrap transition-all flex items-center gap-1.5 cursor-pointer {isActive
              ? isSuccess
                ? 'bg-success text-success-foreground shadow-sm'
                : 'bg-accent text-accent-foreground shadow-sm'
              : 'bg-muted/60 hover:bg-muted text-muted-foreground hover:text-foreground border border-border/60'}"
          >
            <span>{cat.label}</span>
            {#if cat.badgeCount > 0}
              <span
                class="px-1.5 py-0.2 rounded-full text-[10px] font-bold bg-warning text-warning-foreground"
              >
                {cat.badgeCount}
              </span>
            {/if}
          </button>
        {/each}
      </div>
    {/if}
  </div>

  <!-- Rejilla de Imágenes / Plantillas -->
  {#if items.length === 0}
    <div
      class="py-10 text-center text-xs text-muted-foreground space-y-2 border border-border/60 rounded-xl bg-card/20"
    >
      <p class="text-sm">
        {emptyMessage || t('vmCreate.catalogEmpty')}
      </p>
      <button
        type="button"
        onclick={resetFilters}
        class="font-medium underline cursor-pointer {accentText}"
      >
        {t('vmCreate.catalogResetFilters')}
      </button>
    </div>
  {:else}
    <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3 p-1">
      {#each displayedItems as item (getItemId(item))}
        {@const id = getItemId(item)}
        {@const isSelected = selectedId === id}
        {@const vcpus = item.rec_vcpus || item.recommended_vcpu || item.vcpus || 2}
        {@const ramMB = item.rec_ram_mb || item.recommended_ram_mb || item.ram_mb || 2048}
        {@const diskGB = item.rec_disk_gb || item.recommended_disk_gb || item.disk_gb || 20}
        <button
          type="button"
          aria-pressed={isSelected}
          onclick={() => {
            selectedId = id;
            if (onselect) onselect(item);
          }}
          class="flex flex-col text-left p-3.5 rounded-xl border transition-all cursor-pointer relative {isSelected
            ? `${accentBorder} ${accentBg} ${accentRing} shadow-md`
            : 'border-border/70 bg-card/60 hover:bg-card hover:border-border hover:shadow-sm'}"
        >
          <!-- Encabezado de la tarjeta -->
          <div class="flex items-center justify-between gap-2 w-full">
            <span
              class="font-semibold text-xs text-foreground tracking-tight flex items-center gap-1.5 truncate"
            >
              {#if isSelected}
                <span class="{accentText} text-xs shrink-0">✓</span>
              {/if}
              <span class="truncate">{getItemName(item)}</span>
            </span>

            <div class="flex items-center gap-1 shrink-0">
              {#if item.is_local}
                <span
                  class="px-2 py-0.5 rounded text-[10px] font-medium bg-success/15 text-success border border-success/30 flex items-center gap-1"
                >
                  <Icon name="zap" size={10} />
                  {t('vmCreate.badgeLocal')}
                </span>
              {:else if item.badge}
                <span
                  class="px-1.5 py-0.5 rounded text-[10px] font-mono font-medium border bg-muted text-muted-foreground border-border/80"
                >
                  {item.badge}
                </span>
              {:else if item.category && item.category !== 'all'}
                <span
                  class="px-1.5 py-0.5 rounded text-[10px] font-mono font-medium border bg-muted text-muted-foreground border-border/80 uppercase"
                >
                  {item.category}
                </span>
              {/if}
            </div>
          </div>

          <!-- Descripción -->
          <p class="text-[11px] text-muted-foreground mt-1.5 line-clamp-2 leading-relaxed">
            {getItemDesc(item) || t('vmCreate.catalogDefaultDesc')}
          </p>

          <!-- Pie de tarjeta con specs sugeridas -->
          <div
            class="mt-3 pt-2 border-t border-border/50 flex items-center justify-between gap-2 text-[10px] w-full"
          >
            <span class="font-mono text-muted-foreground truncate max-w-[130px]" title={id}>
              {id}
            </span>
            <span class="font-medium shrink-0 px-1.5 py-0.5 rounded border {accentBadgeBg}">
              {vcpus}C · {formatRam(ramMB)} · {diskGB} GB
            </span>
          </div>
        </button>
      {/each}
    </div>

    <!-- Paginación suave / Ver más si supera limitInitial -->
    {#if items.length > limitInitial}
      <div class="flex justify-center pt-1">
        <button
          type="button"
          onclick={() => (showAll = !showAll)}
          class="text-xs font-medium px-3 py-1.5 rounded-lg border border-border bg-card/80 hover:bg-muted text-muted-foreground hover:text-foreground transition-colors cursor-pointer"
        >
          {showAll
            ? t('vmCreate.catalogShowLess')
            : t('vmCreate.catalogShowMore', { count: items.length - limitInitial })}
        </button>
      </div>
    {/if}
  {/if}
</div>
