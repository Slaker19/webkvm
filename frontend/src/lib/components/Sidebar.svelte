<script>
  import { getRoute, navigate } from '../router.svelte.js';
  import { auth, api } from '../stores/auth.svelte.js';
  import { APP_VERSION, SITE_NAME } from '../brand.js';
  import { t } from '../i18n.svelte.js';
  import LanguageSelector from './LanguageSelector.svelte';
  import { fleetSnapshots, checkFleetSnapshots } from '../stores/fleetSnapshots.svelte.js';
  import {
    sidebarOrder,
    setSidebarMode,
    toggleSidebarHidden,
    isSidebarHidden,
    resetSidebarOrder,
    moveSidebarItem,
    applySidebarOrder,
  } from '../stores/sidebarOrder.svelte.js';
  import { onMount } from 'svelte';
  import Icon from './Icon.svelte';
  import Avatar from './Avatar.svelte';
  import { branding } from '../stores/branding.svelte.js';
  import * as Dialog from './ui/dialog';
  import { Button } from './ui/button';

  /**
   * @typedef {Object} Props
   * @property {() => void} [onNavigate]
   * @property {'full'|'rail'|'hover'} [mode] - desktop display mode (see
   *   lib/stores/sidebarMode.svelte.js). Mobile always renders this
   *   component at full width regardless of `mode` — Layout.svelte's
   *   off-canvas drawer doesn't pass a rail/hover mode.
   */
  /** @type {Props} */
  let { onNavigate = () => {}, mode = 'full' } = $props();

  // In 'hover' mode the rail expands to full width while the pointer or
  // keyboard focus is inside it, and retracts on leave. 'full' is always
  // expanded; 'rail' never is (icon-only, with a title tooltip per item).
  let hovering = $state(false);
  const showLabels = $derived(mode === 'full' || (mode === 'hover' && hovering));
  // Rail mode floats the expanded panel over the page (absolute,
  // shadowed) instead of pushing content when hovered — the wrapping
  // flex item in Layout.svelte only ever reserves rail width for
  // 'rail'/'hover' modes, so an in-flow expansion would reflow the
  // whole page on every hover.
  const floating = $derived(mode !== 'full' && showLabels);

  const route = $derived(getRoute());

  // The "Snapshots" link is hidden until the fleet has at least one
  // snapshot — nothing to see there on a fresh install. Checked once
  // here (not on every navigation); Snapshots.svelte's own load()
  // refreshes the same store, so visiting it directly self-heals this.
  onMount(() => {
    if (!fleetSnapshots.checked) checkFleetSnapshots(api.listAllSnapshots);
  });

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
          id: 'apps',
          path: '/apps',
          labelKey: 'nav.apps',
          icon: 'package',
          roles: ['admin', 'operator', 'viewer'],
        },
        {
          id: 'multiview',
          path: '/multiview',
          labelKey: 'nav.multiview',
          icon: 'grid',
          roles: ['admin', 'operator'],
        },
        {
          id: 'storage',
          path: '/storage',
          labelKey: 'nav.storage',
          icon: 'hardDrive',
          roles: ['admin', 'operator', 'viewer'],
        },
        {
          id: 'media',
          path: '/media',
          labelKey: 'nav.media',
          icon: 'image',
          roles: ['admin', 'operator', 'viewer'],
        },
        {
          id: 'images',
          path: '/images',
          labelKey: 'nav.images',
          icon: 'disc',
          roles: ['admin', 'operator', 'viewer'],
        },
        {
          id: 'networks',
          path: '/networks',
          labelKey: 'nav.networks',
          icon: 'network',
          roles: ['admin', 'operator', 'viewer'],
        },
        {
          id: 'snapshots',
          path: '/snapshots',
          labelKey: 'nav.snapshots',
          icon: 'camera',
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
          id: 'firewall',
          path: '/firewall',
          labelKey: 'nav.firewall',
          icon: 'shield',
          roles: ['admin'],
        },
        {
          id: 'audit',
          path: '/audit',
          labelKey: 'nav.audit',
          icon: 'fileText',
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
        items: applySidebarOrder(
          g.items
            .filter((it) => it.roles.includes(auth.role || ''))
            .filter((it) => it.id !== 'snapshots' || fleetSnapshots.hasAny)
            .map((it) => ({ ...it, label: t(it.labelKey) }))
        ),
      }))
      .filter((g) => g.items.length > 0)
  );

  // ---- Sidebar customization panel ----
  // Items are grouped (Main / System) in the panel, matching the real
  // sidebar sections. Drag & drop reordering is restricted to within
  // a single group — dragging "VMs" past "Users" would be confusing
  // since they live in different areas of the nav. Each group's items
  // are ordered/filtered independently via applySidebarOrder.
  let showCustomize = $state(false);
  let draggedItemId = $state(null);
  let draggedGroupKey = $state(null);
  let dragOverItemId = $state(null);
  let dragOverAfter = $state(false);

  const customizeGroups = $derived(
    groups.map((g) => ({
      labelKey: g.labelKey,
      label: t(g.labelKey),
      items: applySidebarOrder(
        g.items
          .filter((it) => it.roles.includes(auth.role || ''))
          .map((it) => ({ ...it, label: t(it.labelKey), group: t(g.labelKey) }))
      ),
    }))
  );

  function onItemDragStart(e, id, groupKey) {
    if (sidebarOrder.mode !== 'custom') setSidebarMode('custom');
    draggedItemId = id;
    draggedGroupKey = groupKey;
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', id);
  }

  function onItemDragOver(e, id, groupKey) {
    if (!draggedItemId || draggedItemId === id) return;
    if (draggedGroupKey !== groupKey) return; // no cross-group reordering
    e.preventDefault();
    const rect = e.currentTarget.getBoundingClientRect();
    dragOverAfter = e.clientY - rect.top > rect.height / 2;
    dragOverItemId = id;
  }

  function onItemDrop(e, id, groupKey, groupIds) {
    if (!draggedItemId || draggedGroupKey !== groupKey) return;
    e.preventDefault();
    if (draggedItemId !== id) {
      moveSidebarItem(groupIds, draggedItemId, id, dragOverAfter);
    }
    draggedItemId = null;
    draggedGroupKey = null;
    dragOverItemId = null;
  }

  function onItemDragEnd() {
    draggedItemId = null;
    draggedGroupKey = null;
    dragOverItemId = null;
  }

  // Reordering used to be drag-only, which left the list unusable for
  // anyone on a keyboard: the row advertised role="button" and took
  // focus, then did nothing. Alt+Arrow moves the focused entry, matching
  // the convention used by most reorderable lists.
  function onItemKeydown(e, id, groupIds) {
    if (sidebarOrder.mode !== 'custom') return;
    if (!e.altKey || (e.key !== 'ArrowUp' && e.key !== 'ArrowDown')) return;
    const idx = groupIds.indexOf(id);
    if (idx < 0) return;
    const target = e.key === 'ArrowUp' ? idx - 1 : idx + 1;
    if (target < 0 || target >= groupIds.length) return;
    e.preventDefault();
    // "after" is true when moving down, so the item lands past its
    // neighbour rather than swapping back into the same slot.
    moveSidebarItem(groupIds, id, groupIds[target], e.key === 'ArrowDown');
    // Focus follows the moved row; without this the list re-renders and
    // focus falls back to the dialog, forcing a re-tab on every step.
    const el = e.currentTarget;
    queueMicrotask(() => el?.focus?.());
  }

  function isActive(id) {
    if (
      id === 'vms' &&
      (route.name === 'vms' || route.name === 'vm-detail' || route.name === 'vms-new')
    )
      return true;
    return route.name === id;
  }

  function go(path) {
    navigate(path);
    onNavigate();
  }
</script>

<aside
  class="{floating
    ? 'absolute inset-y-0 left-0 z-30 shadow-lg'
    : 'relative'} border-r border-border flex flex-col shrink-0 h-screen bg-card transition-[width,box-shadow] duration-150 ease-out overflow-hidden"
  style="width: {showLabels ? '224px' : '56px'}"
  onmouseenter={() => mode === 'hover' && (hovering = true)}
  onmouseleave={() => mode === 'hover' && (hovering = false)}
  onfocusin={() => mode === 'hover' && (hovering = true)}
  onfocusout={() => mode === 'hover' && (hovering = false)}
>
  <div class="p-4 border-b border-border">
    <div class="flex items-center gap-3">
      <div class="w-8 h-8 rounded-lg shrink-0 shadow-sm overflow-hidden">
        <img
          src={branding.logo || '/favicon.png'}
          alt={SITE_NAME}
          class="w-full h-full object-cover"
          onerror={(e) => (e.currentTarget.src = '/favicon.png')}
        />
      </div>
      {#if showLabels}
        <div class="min-w-0">
          <span class="font-semibold text-sm truncate block leading-tight">{SITE_NAME}</span>
          <p class="text-xs text-muted-foreground leading-tight">{t('nav.manager')}</p>
        </div>
      {/if}
    </div>
  </div>

  <nav class="flex-1 p-2 overflow-y-auto">
    {#each visibleGroups as group, gi (group.labelKey)}
      {#if showLabels}
        <div
          class="px-2.5 pt-3 pb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70"
        >
          {group.label}
        </div>
      {:else if gi > 0}
        <hr class="fade-rule mx-1.5 my-2" />
      {/if}
      <div class="space-y-0.5">
        {#each group.items as item (item.id)}
          <button
            onclick={() => go(item.path)}
            aria-current={isActive(item.id) ? 'page' : undefined}
            title={showLabels ? undefined : item.label}
            class="relative w-full flex items-center gap-2.5 px-2.5 py-2 rounded-md text-sm font-medium transition-colors duration-150 {showLabels
              ? ''
              : 'justify-center'} {isActive(item.id)
              ? 'bg-accent/10 text-accent'
              : 'text-muted-foreground hover:text-foreground hover:bg-muted'}"
          >
            {#if isActive(item.id)}
              <span
                class="absolute left-0 top-1/2 -translate-y-1/2 w-0.5 h-4 rounded-full bg-accent"
              ></span>
            {/if}
            <Icon name={item.icon} size={16} class="shrink-0" />
            {#if showLabels}
              <span class="truncate min-w-0">{item.label}</span>
            {/if}
          </button>
        {/each}
      </div>
    {/each}
    {#if showLabels}
      <button
        type="button"
        onclick={() => (showCustomize = true)}
        class="w-full flex items-center gap-2.5 px-2.5 py-2 mt-2 rounded-md text-xs font-medium text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
      >
        <Icon name="gripVertical" size={14} class="shrink-0" />
        <span class="truncate min-w-0">{t('nav.customizeSidebar')}</span>
      </button>
    {/if}
  </nav>

  <div class="p-2 border-t border-border space-y-1">
    <div class={showLabels ? 'px-0.5' : 'flex justify-center'}>
      <LanguageSelector compact={!showLabels} side="top" align={showLabels ? 'start' : 'center'} />
    </div>
    <button
      onclick={() => go('/account')}
      title={showLabels ? undefined : auth.user || t('nav.account')}
      class="w-full flex items-center gap-2.5 px-2.5 py-2 rounded-md text-sm transition-colors duration-150 {showLabels
        ? ''
        : 'justify-center'} {route.name === 'account'
        ? 'bg-accent/10 text-accent'
        : 'text-muted-foreground hover:text-foreground hover:bg-muted'}"
      aria-current={route.name === 'account' ? 'page' : undefined}
    >
      <Avatar src={auth.avatar} name={auth.user} size={20} />
      {#if showLabels}
        <div class="flex-1 text-left min-w-0">
          <div class="font-medium truncate">{auth.user || t('nav.account')}</div>
          <div class="text-xs text-muted-foreground truncate">{auth.role || '—'}</div>
        </div>
      {/if}
    </button>
    {#if showLabels}
      <div class="px-2.5 py-1 text-[11px] text-muted-foreground font-mono">
        {t('nav.version', { version: APP_VERSION })}
      </div>
    {/if}
  </div>
</aside>

<!-- Sidebar customization: reorder (drag & drop / alphabetical) and
     hide/show individual nav items. Persisted to localStorage. -->
<Dialog.Root bind:open={showCustomize}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('nav.customizeSidebar')}</Dialog.Title>
      <Dialog.Description>
        {t('nav.customizeSidebarDesc')}
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex items-center gap-1.5 mb-2">
      <button
        type="button"
        onclick={() => setSidebarMode('default')}
        class="text-[11px] px-2.5 py-1 rounded-full border transition-colors {sidebarOrder.mode ===
        'default'
          ? 'border-accent bg-accent/10 text-accent font-semibold'
          : 'border-border text-muted-foreground hover:text-foreground'}"
      >
        {t('nav.orderDefault')}
      </button>
      <button
        type="button"
        onclick={() => setSidebarMode('alpha')}
        class="text-[11px] px-2.5 py-1 rounded-full border transition-colors {sidebarOrder.mode ===
        'alpha'
          ? 'border-accent bg-accent/10 text-accent font-semibold'
          : 'border-border text-muted-foreground hover:text-foreground'}"
      >
        {t('nav.orderAlpha')}
      </button>
      <button
        type="button"
        onclick={() => setSidebarMode('custom')}
        class="text-[11px] px-2.5 py-1 rounded-full border transition-colors {sidebarOrder.mode ===
        'custom'
          ? 'border-accent bg-accent/10 text-accent font-semibold'
          : 'border-border text-muted-foreground hover:text-foreground'}"
      >
        {t('nav.orderCustom')}
      </button>
    </div>

    <div class="max-h-[360px] overflow-y-auto -mx-1 px-1 space-y-1">
      {#each customizeGroups as group (group.labelKey)}
        {@const groupIds = group.items.map((it) => it.id)}
        <div
          class="px-1.5 pt-2.5 pb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70"
        >
          {group.label}
        </div>
        {#each group.items as item (item.id)}
          {@const hidden = isSidebarHidden(item.id)}
          <!--
            The row itself carries the drop target (a mouse concern) but
            is no longer focusable: it used to claim role="button" while
            doing nothing on Enter, so a keyboard user landed on a dead
            stop. Focus and keyboard reordering now live on the real
            <button> grip below, which is what they describe.
          -->
          <div
            role="presentation"
            draggable={sidebarOrder.mode === 'custom'}
            ondragstart={(e) => onItemDragStart(e, item.id, group.labelKey)}
            ondragover={(e) => onItemDragOver(e, item.id, group.labelKey)}
            ondrop={(e) => onItemDrop(e, item.id, group.labelKey, groupIds)}
            ondragend={onItemDragEnd}
            class="flex items-center gap-2 p-2 rounded-lg border transition-colors {hidden
              ? 'border-border/50 bg-muted/20 opacity-50'
              : 'border-border bg-card'} {draggedItemId === item.id
              ? 'opacity-30'
              : ''} {dragOverItemId === item.id
              ? dragOverAfter
                ? 'border-b-2 border-b-accent'
                : 'border-t-2 border-t-accent'
              : ''}"
          >
            {#if sidebarOrder.mode === 'custom'}
              <button
                type="button"
                class="shrink-0 cursor-grab rounded text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-accent"
                aria-label={t('nav.reorderItem', { name: item.label })}
                onkeydown={(e) => onItemKeydown(e, item.id, groupIds)}
              >
                <Icon name="gripVertical" size={14} />
              </button>
            {/if}
            <Icon name={item.icon} size={14} class="text-muted-foreground shrink-0" />
            <span class="text-xs font-medium truncate block flex-1 min-w-0">{item.label}</span>
            <button
              type="button"
              onclick={() => toggleSidebarHidden(item.id)}
              title={hidden ? t('nav.showItem') : t('nav.hideItem')}
              class="shrink-0 p-1 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            >
              <Icon name={hidden ? 'eyeOff' : 'eye'} size={14} />
            </button>
          </div>
        {/each}
      {/each}
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={resetSidebarOrder}>
        {t('nav.resetSidebar')}
      </Button>
      <Button onclick={() => (showCustomize = false)}>{t('common.close')}</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
