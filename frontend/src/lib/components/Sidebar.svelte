<script>
  import { getRoute, navigate } from '../router.svelte.js';
  import { auth } from '../stores/auth.svelte.js';
  import { APP_VERSION, SITE_NAME } from '../brand.js';
  import { t, getLocale, setLocale, LOCALES } from '../i18n.svelte.js';
  import Icon from './Icon.svelte';

  let { onNavigate = () => {} } = $props();

  const route = $derived(getRoute());

  // Grouped so each area shows only what belongs to it. Labels are
  // resolved through t() inside a derived so they follow the language.
  const groups = [
    {
      labelKey: 'nav.main',
      items: [
        {
          id: 'vms',
          path: '/vms',
          labelKey: 'nav.vms',
          icon: 'computer',
          roles: ['admin', 'operator', 'viewer'],
        },
        {
          id: 'storage',
          path: '/storage',
          labelKey: 'nav.storage',
          icon: 'hardDrive',
          roles: ['admin', 'operator', 'viewer'],
        },
        {
          id: 'networks',
          path: '/networks',
          labelKey: 'nav.networks',
          icon: 'network',
          roles: ['admin', 'operator', 'viewer'],
        },
      ],
    },
    {
      labelKey: 'nav.system',
      items: [
        {
          id: 'status',
          path: '/status',
          labelKey: 'nav.systemStatus',
          icon: 'activity',
          roles: ['admin', 'operator', 'viewer'],
        },
        { id: 'users', path: '/users', labelKey: 'nav.users', icon: 'users', roles: ['admin'] },
        { id: 'nodes', path: '/nodes', labelKey: 'nav.nodes', icon: 'server', roles: ['admin'] },
        {
          id: 'backup',
          path: '/backup',
          labelKey: 'nav.backup',
          icon: 'archive',
          roles: ['admin'],
        },
        {
          id: 'settings',
          path: '/settings',
          labelKey: 'nav.settings',
          icon: 'settings',
          roles: ['admin'],
        },
        {
          id: 'host-console',
          path: '/host-console',
          labelKey: 'nav.hostTerminal',
          icon: 'terminal',
          roles: ['admin'],
        },
      ],
    },
  ];

  const visibleGroups = $derived(
    groups
      .map((g) => ({
        labelKey: g.labelKey,
        label: t(g.labelKey),
        items: g.items
          .filter((it) => it.roles.includes(auth.role || ''))
          .map((it) => ({ ...it, label: t(it.labelKey) })),
      }))
      .filter((g) => g.items.length > 0)
  );

  const locale = $derived(getLocale());

  function isActive(id) {
    if (
      id === 'vms' &&
      (route.name === 'vms' || route.name === 'vm-detail' || route.name === 'vms-new')
    )
      return true;
    return route.name === id;
  }

  function go(path, external) {
    if (external) {
      window.open(
        path + '?token=' + encodeURIComponent(auth.token),
        '_blank',
        'noopener,noreferrer'
      );
    } else {
      navigate(path);
      onNavigate();
    }
  }
</script>

<aside class="w-56 border-r border-border flex flex-col shrink-0 h-screen bg-card">
  <div class="p-4 border-b border-border">
    <div class="flex items-center gap-3">
      <div
        class="w-8 h-8 rounded-lg bg-gradient-to-br from-accent to-accent-hover flex items-center justify-center shrink-0 shadow-sm"
      >
        <Icon name="computer" size={16} class="text-accent-foreground" />
      </div>
      <div class="min-w-0">
        <span class="font-semibold text-sm truncate block leading-tight">{SITE_NAME}</span>
        <p class="text-xs text-muted-foreground leading-tight">{t('nav.manager')}</p>
      </div>
    </div>
  </div>

  <nav class="flex-1 p-2 overflow-y-auto">
    {#each visibleGroups as group (group.labelKey)}
      <div
        class="px-2.5 pt-3 pb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70"
      >
        {group.label}
      </div>
      <div class="space-y-0.5">
        {#each group.items as item (item.id)}
          <button
            onclick={() => go(item.path, item.external)}
            aria-current={isActive(item.id) ? 'page' : undefined}
            class="relative w-full flex items-center gap-2.5 px-2.5 py-2 rounded-md text-sm font-medium transition-colors duration-150 {isActive(
              item.id
            )
              ? 'bg-accent/10 text-accent'
              : 'text-muted-foreground hover:text-foreground hover:bg-muted'}"
          >
            {#if isActive(item.id)}
              <span
                class="absolute left-0 top-1/2 -translate-y-1/2 w-0.5 h-4 rounded-full bg-accent"
              ></span>
            {/if}
            <Icon name={item.icon} size={16} class="shrink-0" />
            <span class="truncate min-w-0">{item.label}</span>
          </button>
        {/each}
      </div>
    {/each}
  </nav>

  <div class="p-2 border-t border-border space-y-0.5">
    <div class="flex items-center gap-1 px-1 py-1">
      {#each LOCALES as l (l.code)}
        <button
          type="button"
          onclick={() => setLocale(l.code)}
          aria-pressed={locale === l.code}
          class="flex-1 px-1 py-1 rounded text-[11px] font-medium transition-colors {locale ===
          l.code
            ? 'bg-accent/15 text-accent'
            : 'text-muted-foreground hover:text-foreground hover:bg-muted'}"
        >
          {l.label}
        </button>
      {/each}
    </div>
    <button
      onclick={() => go('/account')}
      class="w-full flex items-center gap-2.5 px-2.5 py-2 rounded-md text-sm transition-colors duration-150 {route.name ===
      'account'
        ? 'bg-accent/10 text-accent'
        : 'text-muted-foreground hover:text-foreground hover:bg-muted'}"
      aria-current={route.name === 'account' ? 'page' : undefined}
    >
      <Icon name="users" size={16} class="shrink-0" />
      <div class="flex-1 text-left min-w-0">
        <div class="font-medium truncate">{auth.user || t('nav.account')}</div>
        <div class="text-xs text-muted-foreground truncate">{auth.role || '—'}</div>
      </div>
    </button>
    <div class="px-2.5 py-1 text-[11px] text-muted-foreground font-mono">
      {t('nav.version', { version: APP_VERSION })}
    </div>
  </div>
</aside>
