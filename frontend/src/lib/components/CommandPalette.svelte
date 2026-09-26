<script>
  import { onMount, onDestroy } from 'svelte';
  import { fade, scale as scaleTransition } from 'svelte/transition';
  import { navigate } from '$lib/router.svelte.js';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { t } from '../i18n.svelte.js';
  import { sidebarMode, cycleSidebarMode } from '../stores/sidebarMode.svelte.js';
  import { toggleTaskDrawer } from '../stores/tasks.svelte.js';
  import { setTheme, setAccent, THEMES, ACCENTS } from '../stores/theme.svelte.js';
  import Icon from './Icon.svelte';

  let open = $state(false);
  let query = $state('');
  let vms = $state([]);
  let pools = $state([]);
  let networks = $state([]);
  let selectedIndex = $state(0);
  let inputEl = $state(null);
  let focusTimer = null;
  // Monotonic token identifying the newest preload burst (see $effect).
  let loadSeq = 0;

  const sidebarModeLabelKey = {
    full: 'layout.sidebarFull',
    rail: 'layout.sidebarRail',
    hover: 'layout.sidebarHover',
  };

  // `roles` mirrors the route guards in router.svelte.js / Sidebar: a
  // command the current role cannot open is not offered at all (it
  // would only land on AccessDenied). No `roles` = every role.
  const navigationCommands = [
    {
      id: 'nav-vms',
      category: 'nav',
      label: () => t('vms.title'),
      path: '/vms',
      icon: 'computer',
      keywords: 'home list instances dashboard',
    },
    {
      id: 'nav-vms-new',
      category: 'nav',
      roles: ['admin', 'operator'],
      label: () => t('vms.create'),
      path: '/vms/new',
      icon: 'plus',
      keywords: 'new add create wizard',
    },
    {
      id: 'nav-apps',
      category: 'nav',
      label: () => t('nav.apps'),
      path: '/apps',
      icon: 'package',
      keywords: 'app store community helper scripts lxc containers templates pihole docker uptime',
    },
    {
      id: 'nav-multiview',
      category: 'nav',
      label: () => t('nav.multiview'),
      path: '/multiview',
      icon: 'grid',
      keywords: 'grid console novnc tiles multi',
    },
    {
      id: 'nav-storage',
      category: 'nav',
      label: () => t('storage.title'),
      path: '/storage',
      icon: 'hardDrive',
      keywords: 'pool volume disk iso backup storage',
    },
    {
      id: 'nav-images',
      category: 'nav',
      label: () => t('nav.images'),
      path: '/images',
      icon: 'disc',
      keywords: 'image hub pool container lxc cloud base qcow2 iso templates snippets',
    },
    {
      id: 'nav-networks',
      category: 'nav',
      label: () => t('networks.title'),
      path: '/networks',
      icon: 'network',
      keywords: 'net bridge nat isolated dhcp leases',
    },
    {
      id: 'nav-firewall',
      category: 'nav',
      roles: ['admin'],
      label: () => t('firewall.title'),
      path: '/firewall',
      icon: 'shield',
      keywords: 'security rules forward nftables port safe apply',
    },
    {
      id: 'nav-backup',
      category: 'nav',
      roles: ['admin'],
      label: () => t('backup.title'),
      path: '/backup',
      icon: 'archive',
      keywords: 'snapshots jobs s3 schedule restore',
    },
    {
      id: 'nav-audit',
      category: 'nav',
      roles: ['admin'],
      label: () => t('audit.title'),
      path: '/audit',
      icon: 'fileText',
      keywords: 'log history security export csv audit',
    },
    {
      id: 'nav-users',
      category: 'nav',
      roles: ['admin'],
      label: () => t('users.title'),
      path: '/users',
      icon: 'users',
      keywords: 'account operators permissions rbac 2fa',
    },
    {
      id: 'nav-nodes',
      category: 'nav',
      roles: ['admin'],
      label: () => t('nodes.title'),
      path: '/nodes',
      icon: 'server',
      keywords: 'cluster remote host',
    },
    {
      id: 'nav-settings',
      category: 'nav',
      roles: ['admin'],
      label: () => t('settings.title'),
      path: '/settings',
      icon: 'settings',
      keywords: 'config options live apply tls',
    },
    {
      id: 'nav-status',
      category: 'nav',
      label: () => t('status.title'),
      path: '/status',
      icon: 'activity',
      keywords: 'status log update health metrics',
    },
  ];

  const actionCommands = $derived([
    {
      id: 'action-toggle-task-drawer',
      category: 'actions',
      label: () => t('taskDrawer.toggle'),
      action: toggleTaskDrawer,
      icon: 'activity',
      keywords: 'tasks drawer jobs background progress terminal logs ctrl+j cmd+j',
    },
    {
      id: 'action-toggle-sidebar',
      category: 'actions',
      label: () => t('layout.toggleSidebar', { mode: t(sidebarModeLabelKey[sidebarMode.value]) }),
      action: cycleSidebarMode,
      icon: 'panelLeft',
      keywords: 'sidebar collapse rail hover expand',
    },
  ]);

  const themeCommands = $derived([
    ...THEMES.map((th) => ({
      id: `theme-mode-${th}`,
      category: 'themes',
      label: () => `${t('theme.mode')}: ${t(`theme.${th}`)}`,
      action: () => setTheme(th),
      icon: th === 'light' ? 'sun' : th === 'oled' ? 'sparkles' : 'moon',
      keywords: `theme appearance mode dark light oled ${th}`,
    })),
    ...ACCENTS.map((acc) => ({
      id: `theme-accent-${acc}`,
      category: 'themes',
      label: () => `${t('theme.accent')}: ${t(`theme.${acc}`)}`,
      action: () => setAccent(acc),
      icon: 'zap',
      keywords: `accent color ${acc}`,
    })),
  ]);

  const filteredCommands = $derived.by(() => {
    const q = query.toLowerCase().trim();

    const navFiltered = navigationCommands.filter(
      (c) =>
        (!c.roles || c.roles.includes(auth.role || '')) &&
        (!q || c.label().toLowerCase().includes(q) || c.keywords.includes(q))
    );

    const actionFiltered = actionCommands.filter(
      (c) => !q || c.label().toLowerCase().includes(q) || c.keywords.includes(q)
    );

    const themeFiltered = themeCommands.filter(
      (c) => !q || c.label().toLowerCase().includes(q) || c.keywords.includes(q)
    );

    // Global VM / Container search
    const vmFiltered = vms
      .filter(
        (v) =>
          !q ||
          v.name.toLowerCase().includes(q) ||
          (v.ip && v.ip.includes(q)) ||
          (v.groups || []).some((g) => g.toLowerCase().includes(q)) ||
          v.state.includes(q)
      )
      .map((v) => ({
        id: `vm-${v.id}`,
        category: 'vms',
        label: () => v.name,
        subtitle:
          v.state === 'running' && v.ip
            ? `${t('common.running')} · ${v.ip}${v.groups?.length ? ` · ${v.groups.join(', ')}` : ''}`
            : `${t(`common.${v.state}`)}${v.groups?.length ? ` · ${v.groups.join(', ')}` : ''}`,
        path: `/vms/${v.id}`,
        icon: v.type === 'incus' ? 'box' : 'computer',
        keywords: `vm container ${v.type} ${v.state} ${(v.groups || []).join(' ')}`,
      }));

    // Quick direct VM actions (if operator/admin)
    let vmQuickActions = [];
    if (auth.role !== 'viewer' && q.length >= 2) {
      vms.forEach((v) => {
        if (v.name.toLowerCase().includes(q)) {
          if (v.state === 'running') {
            vmQuickActions.push({
              id: `action-stop-${v.id}`,
              category: 'actions',
              label: () => `${t('common.stop')}: ${v.name}`,
              action: () => api.shutdownVM(v.id).catch(() => {}),
              icon: 'stop',
              keywords: `stop shutdown ${v.name}`,
            });
          } else if (v.state === 'shutoff') {
            vmQuickActions.push({
              id: `action-start-${v.id}`,
              category: 'actions',
              label: () => `${t('common.start')}: ${v.name}`,
              action: () => api.startVM(v.id).catch(() => {}),
              icon: 'play',
              keywords: `start boot ${v.name}`,
            });
          }
        }
      });
    }

    // Storage pools search
    const poolFiltered = pools
      .filter((p) => !q || p.name.toLowerCase().includes(q) || p.type.includes(q))
      .map((p) => ({
        id: `pool-${p.name}`,
        category: 'storage',
        label: () => `${t('storage.pool')}: ${p.name}`,
        subtitle: `${p.type.toUpperCase()} · ${p.path || ''}`,
        path: '/storage',
        icon: 'hardDrive',
        keywords: `storage pool ${p.type} ${p.name}`,
      }));

    // Networks search
    const netFiltered = networks
      .filter((n) => !q || n.name.toLowerCase().includes(q) || (n.bridge && n.bridge.includes(q)))
      .map((n) => ({
        id: `net-${n.id || n.name}`,
        category: 'networks',
        label: () => `${t('networks.title')}: ${n.name}`,
        subtitle: `${n.bridge || n.name} · ${n.cidr || n.kind || ''}`,
        path: '/networks',
        icon: 'network',
        keywords: `network bridge ${n.name} ${n.bridge || ''}`,
      }));

    return [
      ...vmQuickActions,
      ...navFiltered,
      ...vmFiltered,
      ...poolFiltered,
      ...netFiltered,
      ...actionFiltered,
      ...themeFiltered,
    ];
  });

  function runCommand(cmd) {
    if (cmd.action) {
      cmd.action();
    } else if (cmd.path) {
      navigate(cmd.path);
    }
    open = false;
  }

  $effect(() => {
    if (open) {
      selectedIndex = 0;
      query = '';
      // Sequence token: the palette refetches the whole world on every
      // open. Closing and reopening quickly fires two overlapping
      // bursts, and a slow first response then overwrote the fresh
      // second one — the palette showed a stale VM list with no
      // indication anything was wrong. Each burst increments the token
      // and only the newest one is allowed to write.
      const seq = ++loadSeq;
      const fresh = () => seq === loadSeq;
      api
        .listVMs()
        .then((d) => {
          if (fresh()) vms = d || [];
        })
        .catch(() => {});
      api
        .listPools()
        .then((d) => {
          if (fresh()) pools = d || [];
        })
        .catch(() => {});
      api
        .listNetworks()
        .then((d) => {
          if (fresh()) networks = d || [];
        })
        .catch(() => {});
      focusTimer = setTimeout(() => inputEl?.focus(), 15);
    } else {
      // Invalidate any in-flight burst so it cannot write after close.
      loadSeq++;
    }
  });

  function handleKeydown(e) {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      open = !open;
      return;
    }
    if (!open) return;
    if (e.key === 'Escape') {
      open = false;
      return;
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      selectedIndex = Math.min(filteredCommands.length - 1, selectedIndex + 1);
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault();
      selectedIndex = Math.max(0, selectedIndex - 1);
    }
    if (e.key === 'Enter') {
      e.preventDefault();
      const cmd = filteredCommands[selectedIndex];
      if (cmd) runCommand(cmd);
    }
  }

  function handleOpenPalette() {
    open = true;
  }

  onMount(() => {
    window.addEventListener('keydown', handleKeydown);
    window.addEventListener('open-command-palette', handleOpenPalette);
  });

  onDestroy(() => {
    if (focusTimer) clearTimeout(focusTimer);
    if (typeof window !== 'undefined') {
      window.removeEventListener('keydown', handleKeydown);
      window.removeEventListener('open-command-palette', handleOpenPalette);
    }
  });
</script>

{#if open}
  <div
    class="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm flex items-start justify-center pt-[12vh] cursor-default"
    role="presentation"
    onclick={() => (open = false)}
    transition:fade={{ duration: 120 }}
  >
    <div
      role="dialog"
      aria-label={t('layout.openPalette')}
      tabindex="-1"
      class="bg-popover text-popover-foreground border border-border rounded-xl shadow-2xl w-full max-w-xl mx-4 overflow-hidden"
      transition:scaleTransition={{ start: 0.97, duration: 150 }}
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => {
        // Handle the palette keys here: stopping propagation (so page-level
        // shortcuts don't fire while typing) also keeps the event from ever
        // reaching the window listener, which used to leave Esc, the
        // arrows and Enter dead whenever focus was inside the dialog.
        handleKeydown(e);
        e.stopPropagation();
      }}
    >
      <!-- Search Input Bar -->
      <div class="flex items-center border-b border-border px-3.5 bg-card/50">
        <Icon name="search" size={16} class="text-muted-foreground shrink-0" />
        <input
          bind:this={inputEl}
          bind:value={query}
          oninput={() => (selectedIndex = 0)}
          type="text"
          placeholder={t('commandPalette.placeholder')}
          class="flex-1 bg-transparent border-0 outline-none px-3 py-3.5 text-sm text-foreground placeholder:text-muted-foreground font-sans"
        />
        <div class="flex items-center gap-1 shrink-0">
          <kbd
            class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-muted text-muted-foreground border border-border"
          >
            ESC
          </kbd>
        </div>
      </div>

      <!-- Command List with Categorized Badges -->
      <div class="max-h-96 overflow-y-auto p-1.5 divide-y divide-border/20">
        {#each filteredCommands as cmd, i (cmd.id)}
          <button
            type="button"
            onclick={() => runCommand(cmd)}
            onmouseenter={() => (selectedIndex = i)}
            class="w-full flex items-center gap-3 px-3 py-2 rounded-lg text-sm text-left transition-colors {i ===
            selectedIndex
              ? 'bg-accent text-accent-foreground shadow-sm'
              : 'text-foreground hover:bg-muted/60'}"
          >
            <div
              class="p-1.5 rounded-md {i === selectedIndex
                ? 'bg-accent-foreground/15 text-accent-foreground'
                : 'bg-muted text-muted-foreground'} shrink-0"
            >
              <Icon name={cmd.icon} size={15} />
            </div>
            <div class="flex-1 min-w-0">
              <div class="flex items-center justify-between gap-2">
                <span class="truncate font-medium">{cmd.label()}</span>
                {#if cmd.category}
                  <span
                    class="text-[10px] font-mono uppercase px-1.5 py-0.2 rounded {i ===
                    selectedIndex
                      ? 'bg-accent-foreground/20 text-accent-foreground'
                      : 'bg-muted text-muted-foreground'} shrink-0"
                  >
                    {cmd.category}
                  </span>
                {/if}
              </div>
              {#if cmd.subtitle}
                <div
                  class="text-xs {i === selectedIndex
                    ? 'text-accent-foreground/80'
                    : 'text-muted-foreground'} truncate mt-0.5 font-mono text-[11px]"
                >
                  {cmd.subtitle}
                </div>
              {/if}
            </div>
          </button>
        {:else}
          <div
            class="px-3 py-10 text-center text-sm text-muted-foreground flex flex-col items-center justify-center"
          >
            <Icon name="search" size={24} class="mb-2 opacity-30" />
            <span>{t('common.noResults')}</span>
          </div>
        {/each}
      </div>

      <!-- Footer Quick Keys Help -->
      <div
        class="px-3 py-2 border-t border-border bg-card/60 flex items-center justify-between text-[11px] text-muted-foreground font-mono"
      >
        <div class="flex items-center gap-3">
          <span
            ><kbd class="px-1 py-0.2 bg-muted rounded border border-border">↑</kbd>
            <kbd class="px-1 py-0.2 bg-muted rounded border border-border">↓</kbd>
            {t('commandPalette.navigate')}</span
          >
          <span
            ><kbd class="px-1 py-0.2 bg-muted rounded border border-border">↵</kbd>
            {t('commandPalette.select')}</span
          >
        </div>
        <div>
          <span
            ><kbd class="px-1 py-0.2 bg-muted rounded border border-border">⌘J</kbd>
            {t('taskDrawer.shortcut')}</span
          >
        </div>
      </div>
    </div>
  </div>
{/if}
