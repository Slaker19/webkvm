<script>
  /**
   * Layout — sidebar + top bar + main area.
   *
   * The top bar keeps a single line with:
   *   - the current page title (from viewNames below),
   *   - a small connection-status pill (live SSE state),
   *   - the global command-palette trigger on the right.
   *
   * The <title> is updated on every route change so the browser tab
   * matches what the user is looking at. Without that, the tab
   * would always read the static index.html title.
   */
  import { auth } from '../stores/auth.svelte.js';
  import { events } from '../stores/events.svelte.js';
  import { getRoute } from '../router.svelte.js';
  import { t } from '../i18n.svelte.js';
  import Sidebar from './Sidebar.svelte';
  import Icon from './Icon.svelte';
  import TaskCenter from './TaskCenter.svelte';
  import { SITE_NAME } from '../brand.js';
  import { sidebarMode, cycleSidebarMode } from '../stores/sidebarMode.svelte.js';

  let { children } = $props();

  const route = $derived(getRoute());

  let mobileNavOpen = $state(false);

  const sidebarModeLabelKey = {
    full: 'layout.sidebarFull',
    rail: 'layout.sidebarRail',
    hover: 'layout.sidebarHover',
  };
  // Short labels for the header toggle button itself (the long
  // descriptions above are for the tooltip/aria-label only).
  const sidebarModeShortKey = {
    full: 'layout.sidebarFullShort',
    rail: 'layout.sidebarRailShort',
    hover: 'layout.sidebarHoverShort',
  };

  const viewNames = {
    vms: () => t('vms.title'),
    'vms-new': () => t('vms.create'),
    'vm-detail': () => t('vmDetail.overview'),
    storage: () => t('storage.title'),
    networks: () => t('networks.title'),
    users: () => t('users.title'),
    nodes: () => t('nodes.title'),
    snapshots: () => t('snapshots.title'),
    backup: () => t('backup.title'),
    settings: () => t('settings.title'),
    status: () => t('status.title'),
    account: () => t('account.title'),
    'not-found': () => t('common.notFound'),
    'access-denied': () => t('common.accessDenied'),
  };

  const title = $derived((viewNames[route.name] || (() => SITE_NAME))());

  // Sync the document title with the current route so browser tabs
  // and history entries are accurate. " · " separates the page from
  // the brand (matches GitHub / Linear / Proxmox style).
  $effect(() => {
    if (typeof document !== 'undefined') {
      document.title = route.name === 'vms' ? SITE_NAME : `${title} · ${SITE_NAME}`;
    }
  });

  // Close mobile nav on route change.
  $effect(() => {
    void route.name;
    mobileNavOpen = false;
  });

  // Reset scroll on route change. #main (not window) is what scrolls,
  // so a lingering deep scroll position from the previous page (e.g.
  // a long DataTable) could make a freshly-mounted page look wrong or,
  // combined with a slow re-render, make navigation look like it did
  // nothing at all.
  $effect(() => {
    void route.name;
    document.getElementById('main')?.scrollTo(0, 0);
  });
</script>

<a
  href="#main"
  class="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:px-3 focus:py-1.5 focus:rounded focus:bg-accent focus:text-accent-foreground focus:shadow"
>
  {t('layout.skip')}
</a>

<div class="flex h-screen overflow-hidden bg-background text-foreground">
  <!-- Desktop sidebar -->
  <div
    class="hidden md:flex shrink-0 relative h-full"
    style="width: {sidebarMode.value === 'full' ? '224px' : '56px'}"
  >
    <Sidebar mode={sidebarMode.value} />
  </div>

  <!-- Mobile drawer -->
  {#if mobileNavOpen}
    <button
      type="button"
      aria-label={t('layout.closeMenu')}
      onclick={() => (mobileNavOpen = false)}
      class="md:hidden fixed inset-0 z-40 bg-black/50"
    ></button>
    <div class="md:hidden fixed inset-y-0 left-0 z-50 shadow-2xl">
      <Sidebar onNavigate={() => (mobileNavOpen = false)} />
    </div>
  {/if}

  <div class="flex-1 flex flex-col overflow-hidden">
    <header
      class="h-12 border-b border-border flex items-center justify-between px-4 sm:px-6 shrink-0 gap-3"
    >
      <div class="flex items-center gap-3 min-w-0">
        <button
          type="button"
          onclick={() => (mobileNavOpen = true)}
          class="md:hidden -ml-1 p-1.5 text-muted-foreground hover:text-foreground rounded"
          aria-label={t('layout.openMenu')}
        >
          <Icon name="menu" size={18} />
        </button>
        <span class="text-sm font-medium truncate min-w-0">{title}</span>

        {#if events.reconnecting}
          <span
            class="hidden sm:inline-flex items-center gap-1.5 text-xs text-warning border border-warning/30 bg-warning/10 rounded-full px-2 py-0.5"
            role="status"
            aria-live="polite"
          >
            <span class="w-1.5 h-1.5 rounded-full bg-warning animate-pulse"></span>
            {t('layout.reconnecting')}
          </span>
        {:else if !events.connected}
          <span
            class="hidden sm:inline-flex items-center gap-1.5 text-xs text-muted-foreground border border-border rounded-full px-2 py-0.5"
            role="status"
            aria-live="polite"
          >
            <span class="w-1.5 h-1.5 rounded-full bg-muted-foreground"></span>
            {t('layout.offline')}
          </span>
        {/if}
      </div>

      <div class="flex items-center gap-1.5 sm:gap-2 shrink-0">
        <button
          onclick={cycleSidebarMode}
          class="hidden md:flex items-center gap-1.5 px-2 py-1.5 text-muted-foreground hover:text-foreground hover:bg-muted rounded-md transition-colors"
          title={t('layout.toggleSidebar', { mode: t(sidebarModeLabelKey[sidebarMode.value]) })}
          aria-label={t('layout.toggleSidebar', {
            mode: t(sidebarModeLabelKey[sidebarMode.value]),
          })}
        >
          <Icon name="panelLeft" size={16} />
          <span class="text-xs font-medium">{t(sidebarModeShortKey[sidebarMode.value])}</span>
        </button>

        <button
          onclick={() => window.dispatchEvent(new CustomEvent('open-command-palette'))}
          class="sm:flex items-center gap-2 text-xs text-muted-foreground hover:text-foreground transition-colors px-2 py-1 rounded border border-border hover:border-border-hover"
          aria-label={t('layout.openPalette')}
        >
          <Icon name="search" size={14} />
          <span class="hidden sm:inline">{t('nav.search')}</span>
          <kbd
            class="hidden sm:inline text-[10px] font-mono px-1 py-0.5 rounded bg-muted text-muted-foreground"
            >⌘K</kbd
          >
        </button>

        <span class="hidden md:flex items-center gap-2 text-sm text-muted-foreground px-1.5">
          <Icon name="users" size={14} />
          <span class="truncate max-w-[120px]">{auth.user}</span>
        </span>

        <TaskCenter />

        <button
          onclick={() => auth.logout()}
          class="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-destructive transition-colors px-2 py-1 rounded-md hover:bg-destructive/10"
          aria-label={t('layout.logOut')}
        >
          <Icon name="logOut" size={14} />
          <span class="hidden sm:inline">{t('layout.logOut')}</span>
        </button>
      </div>
    </header>

    <main id="main" tabindex="-1" class="flex-1 overflow-y-auto focus:outline-none">
      {#if children}
        {@render children()}
      {/if}
    </main>
  </div>
</div>
