<script>
  import { onMount, onDestroy } from 'svelte';
  import { fade, scale as scaleTransition } from 'svelte/transition';
  import { navigate } from '$lib/router.svelte.js';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '../i18n.svelte.js';
  import { sidebarMode, cycleSidebarMode } from '../stores/sidebarMode.svelte.js';
  import Icon from './Icon.svelte';

  let open = $state(false);
  let query = $state('');
  let vms = $state([]);
  let selectedIndex = $state(0);
  let inputEl = $state(null);

  const sidebarModeLabelKey = {
    full: 'layout.sidebarFull',
    rail: 'layout.sidebarRail',
    hover: 'layout.sidebarHover',
  };

  const navigationCommands = [
    {
      id: 'nav-vms',
      label: () => t('vms.title'),
      path: '/vms',
      icon: 'computer',
      keywords: 'home list',
    },
    {
      id: 'nav-vms-new',
      label: () => t('vms.create'),
      path: '/vms/new',
      icon: 'plus',
      keywords: 'new add',
    },
    {
      id: 'nav-storage',
      label: () => t('storage.title'),
      path: '/storage',
      icon: 'hardDrive',
      keywords: 'pool volume disk',
    },
    {
      id: 'nav-networks',
      label: () => t('networks.title'),
      path: '/networks',
      icon: 'network',
      keywords: 'net',
    },
    {
      id: 'nav-users',
      label: () => t('users.title'),
      path: '/users',
      icon: 'users',
      keywords: 'account',
    },
    {
      id: 'nav-status',
      label: () => t('status.title'),
      path: '/status',
      icon: 'activity',
      keywords: 'status log update',
    },
  ];

  const actionCommands = $derived([
    {
      id: 'action-toggle-sidebar',
      label: () => t('layout.toggleSidebar', { mode: t(sidebarModeLabelKey[sidebarMode.value]) }),
      action: cycleSidebarMode,
      icon: 'panelLeft',
      keywords: 'sidebar collapse rail hover expand',
    },
  ]);

  const filteredCommands = $derived.by(() => {
    const q = query.toLowerCase().trim();
    const navFiltered = navigationCommands.filter(
      (c) => !q || c.label().toLowerCase().includes(q) || c.keywords.includes(q)
    );
    const actionFiltered = actionCommands.filter(
      (c) => !q || c.label().toLowerCase().includes(q) || c.keywords.includes(q)
    );
    const vmFiltered = vms
      .filter((v) => !q || v.name.toLowerCase().includes(q) || (v.ip && v.ip.includes(q)))
      .map((v) => ({
        id: `vm-${v.id}`,
        label: () => v.name,
        subtitle:
          v.state === 'running' && v.ip
            ? `${t('common.running')} · ${v.ip}`
            : t(`common.${v.state}`, v.state),
        path: `/vms/${v.id}`,
        icon: 'computer',
        keywords: `vm ${v.state}`,
      }));
    return [...navFiltered, ...vmFiltered, ...actionFiltered];
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
      // Preload VMs
      api
        .listVMs()
        .then((d) => (vms = d || []))
        .catch(() => {});
      setTimeout(() => inputEl?.focus(), 10);
    }
  });

  function handleKeydown(e) {
    if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
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
    if (typeof window !== 'undefined') {
      window.removeEventListener('keydown', handleKeydown);
      window.removeEventListener('open-command-palette', handleOpenPalette);
    }
  });
</script>

{#if open}
  <div
    class="fixed inset-0 z-50 bg-black/50 backdrop-blur-sm flex items-start justify-center pt-[15vh] cursor-default"
    role="presentation"
    onclick={() => (open = false)}
    transition:fade={{ duration: 120 }}
  >
    <div
      role="dialog"
      aria-label={t('layout.openPalette')}
      tabindex="-1"
      class="bg-popover border border-border rounded-lg shadow-2xl w-full max-w-lg mx-4 overflow-hidden"
      transition:scaleTransition={{ start: 0.97, duration: 150 }}
      onclick={(e) => e.stopPropagation()}
      onkeydown={(e) => e.stopPropagation()}
    >
      <div class="flex items-center border-b border-border px-3">
        <svg
          class="w-4 h-4 text-muted-foreground shrink-0"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          viewBox="0 0 24 24"
        >
          <circle cx="11" cy="11" r="8" />
          <path d="m21 21-4.35-4.35" />
        </svg>
        <input
          bind:this={inputEl}
          bind:value={query}
          type="text"
          placeholder={t('common.search')}
          class="flex-1 bg-transparent border-0 outline-none px-3 py-3 text-sm placeholder:text-muted-foreground"
        />
        <kbd class="text-[10px] font-mono px-1.5 py-0.5 rounded bg-muted text-muted-foreground"
          >ESC</kbd
        >
      </div>

      <div class="max-h-80 overflow-y-auto py-1">
        {#each filteredCommands as cmd, i (cmd.id)}
          <button
            type="button"
            onclick={() => runCommand(cmd)}
            onmouseenter={() => (selectedIndex = i)}
            class="w-full flex items-center gap-3 px-3 py-2 text-sm text-left {i === selectedIndex
              ? 'bg-accent/10 text-foreground'
              : 'text-muted-foreground hover:text-foreground'}"
          >
            <Icon name={cmd.icon} size={16} class="shrink-0" />
            <span class="flex-1 truncate">{cmd.label()}</span>
            {#if cmd.subtitle}
              <span class="text-xs text-muted-foreground">{cmd.subtitle}</span>
            {/if}
          </button>
        {:else}
          <div class="px-3 py-8 text-center text-sm text-muted-foreground">
            {t('common.noResults')}
          </div>
        {/each}
      </div>
    </div>
  </div>
{/if}
