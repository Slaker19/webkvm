<script>
  import { SvelteSet } from 'svelte/reactivity';
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { isContainer } from '$lib/utils/computeType.js';
  import Alert from '$lib/components/Alert.svelte';
  import { Button } from '$lib/components/ui/button';
  import * as Dialog from '$lib/components/ui/dialog';
  import Icon from '$lib/components/Icon.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import { toast } from '$lib/components/ui/toast';
  import { browser } from '$lib/utils/browser.js';

  let vms = $state([]);
  let loading = $state(true);
  let error = $state('');
  let selectedVmIds = $state([]);
  let tickets = $state({}); // vmId -> ticket string
  let loadingTickets = $state({});
  let isFullscreen = $state(false);
  let showVmSelectorModal = $state(false);
  let focusedVmId = $state(null);
  let viewOnlyMode = $state(false);
  let pollInterval = null;

  // Layout & Interactive Mouse Resizing Controls
  let columns = $state(2); // 1, 2, 3, 4, 6 (default = 2)
  let tileHeight = $state(480); // interactive tile height in px (default = 480)
  let density = $state('compact'); // 'none' (0px), 'compact' (4px), 'normal' (10px)
  let headerCompact = $state(false); // Collapsed 36px toolbar vs full header
  let isResizing = $state(false);
  let resizeStartY = 0;
  let resizeStartHeight = 480;

  // Height bounds, shared by the drag handler and the keyboard one so
  // the two cannot drift to different limits.
  const MIN_TILE_H = 260;
  const MAX_TILE_H = 1000;

  function persistTileHeight() {
    if (!browser) return;
    try {
      localStorage.setItem('webkvm.multiview.height.v3', tileHeight.toString());
    } catch {
      /* ignore */
    }
  }

  // The handle was focusable (tabindex=0) but only reacted to the mouse,
  // so a keyboard user could tab onto it and find it inert. Arrow keys
  // resize it, Home/End jump to the bounds.
  function onResizeKey(e) {
    const step = e.shiftKey ? 50 : 10;
    let next = tileHeight;
    switch (e.key) {
      case 'ArrowUp':
        next = tileHeight - step;
        break;
      case 'ArrowDown':
        next = tileHeight + step;
        break;
      case 'Home':
        next = MIN_TILE_H;
        break;
      case 'End':
        next = MAX_TILE_H;
        break;
      default:
        return;
    }
    e.preventDefault();
    tileHeight = Math.max(MIN_TILE_H, Math.min(MAX_TILE_H, next));
    persistTileHeight();
  }

  function onResizeStart(e) {
    e.preventDefault();
    e.stopPropagation();
    isResizing = true;
    resizeStartY = e.clientY || e.touches?.[0]?.clientY || 0;
    resizeStartHeight = tileHeight;

    const onMove = (ev) => {
      const clientY = ev.clientY || ev.touches?.[0]?.clientY || 0;
      const delta = clientY - resizeStartY;
      tileHeight = Math.max(
        MIN_TILE_H,
        Math.min(MAX_TILE_H, Math.round(resizeStartHeight + delta))
      );
    };

    const onEnd = () => {
      isResizing = false;
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onEnd);
      window.removeEventListener('touchmove', onMove);
      window.removeEventListener('touchend', onEnd);
      if (browser) {
        try {
          localStorage.setItem('webkvm.multiview.height.v3', tileHeight.toString());
        } catch {
          /* ignore */
        }
      }
    };

    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onEnd);
    window.addEventListener('touchmove', onMove, { passive: false });
    window.addEventListener('touchend', onEnd);
  }

  function setColumns(c) {
    columns = c;
    if (browser) {
      try {
        localStorage.setItem('webkvm.multiview.cols.v3', c.toString());
      } catch {
        /* ignore */
      }
    }
  }

  function saveTileHeight() {
    if (browser) {
      try {
        localStorage.setItem('webkvm.multiview.height.v3', tileHeight.toString());
      } catch {
        /* ignore */
      }
    }
  }

  // Supervision Exam Timer
  let timerActive = $state(false);
  let timerSeconds = $state(0);
  let timerInterval = null;
  let showBroadcastMenu = $state(false);

  // Selector Search & Filter State
  let selectorSearch = $state('');
  let selectorGroupFilter = $state('all');
  let selectorStateFilter = $state('all');

  function formatRAM(vm) {
    const mb = vm.ram_mb || vm.memory || vm.memory_mb || 0;
    if (!mb) return '1 GB';
    if (mb >= 1024) {
      const gb = mb / 1024;
      return `${gb % 1 === 0 ? gb.toFixed(0) : gb.toFixed(1)} GB`;
    }
    return `${mb} MB`;
  }

  function formatTimer(totalSeconds) {
    const hrs = Math.floor(totalSeconds / 3600);
    const mins = Math.floor((totalSeconds % 3600) / 60);
    const secs = totalSeconds % 60;
    const pad = (n) => String(n).padStart(2, '0');
    if (hrs > 0) return `${pad(hrs)}:${pad(mins)}:${pad(secs)}`;
    return `${pad(mins)}:${pad(secs)}`;
  }

  function toggleTimer() {
    timerActive = !timerActive;
    if (timerActive) {
      timerInterval = setInterval(() => {
        timerSeconds++;
      }, 1000);
    } else {
      if (timerInterval) clearInterval(timerInterval);
    }
  }

  function resetTimer() {
    timerActive = false;
    if (timerInterval) clearInterval(timerInterval);
    timerSeconds = 0;
  }

  function setDensity(d) {
    density = d;
    if (browser) {
      try {
        localStorage.setItem('webkvm.multiview.density.v1', d);
      } catch {
        /* ignore */
      }
    }
  }

  function toggleHeaderCompact() {
    headerCompact = !headerCompact;
    if (browser) {
      try {
        localStorage.setItem('webkvm.multiview.compact.v1', headerCompact.toString());
      } catch {
        /* ignore */
      }
    }
  }

  async function loadVMs() {
    try {
      const data = await api.listVMs();
      const rawList = data || [];
      vms = rawList.filter((v) => !isContainer(v));

      if (selectedVmIds.length === 0 && vms.length > 0) {
        const running = vms.filter((v) => v.state === 'running').map((v) => v.id);
        selectedVmIds = running.length > 0 ? running.slice(0, 8) : vms.slice(0, 4).map((v) => v.id);
      }
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
    refreshTickets();
  }

  function refreshTickets() {
    for (const id of selectedVmIds) {
      const vm = vms.find((v) => v.id === id);
      if (vm && vm.state === 'running' && !tickets[id] && !loadingTickets[id]) {
        fetchTicketFor(id);
      }
    }
  }

  async function fetchTicketFor(vmId) {
    if (loadingTickets[vmId]) return;
    loadingTickets[vmId] = true;
    try {
      const res = await (api.getVNCTicket ? api.getVNCTicket(vmId) : api.createVNCTicket(vmId));
      if (res && res.vnc_ticket) {
        tickets = { ...tickets, [vmId]: res.vnc_ticket };
      }
    } catch (e) {
      console.warn(`[MultiView] Failed ticket for ${vmId}:`, e.message);
    } finally {
      loadingTickets[vmId] = false;
    }
  }

  async function toggleVmPower(vm) {
    try {
      if (vm.state === 'running') {
        toast.info(t('vms.stopping', { name: vm.name }));
        await api.shutdownVM(vm.id);
        const next = { ...tickets };
        delete next[vm.id];
        tickets = next;
      } else {
        toast.info(t('vms.starting', { name: vm.name }));
        await api.startVM(vm.id);
        later(() => fetchTicketFor(vm.id), 2500);
      }
      await loadVMs();
    } catch (e) {
      toast.error(e.message);
    }
  }

  function toggleVmSelection(id) {
    if (selectedVmIds.includes(id)) {
      selectedVmIds = selectedVmIds.filter((v) => v !== id);
      const next = { ...tickets };
      delete next[id];
      tickets = next;
      if (focusedVmId === id) focusedVmId = null;
    } else {
      selectedVmIds = [...selectedVmIds, id];
      const vm = vms.find((v) => v.id === id);
      if (vm && vm.state === 'running') {
        fetchTicketFor(id);
      }
    }
  }

  function selectAllRunning() {
    const running = vms.filter((v) => v.state === 'running').map((v) => v.id);
    selectedVmIds = running;
    refreshTickets();
    toast.info(t('multiview.selectedRunning', { count: running.length }));
  }

  function selectAllFiltered() {
    selectedVmIds = Array.from(
      new Set([...selectedVmIds, ...filteredSelectorVms.map((v) => v.id)])
    );
    refreshTickets();
  }

  function invertSelection() {
    const currentSet = new Set(selectedVmIds);
    selectedVmIds = vms.filter((v) => !currentSet.has(v.id)).map((v) => v.id);
    refreshTickets();
  }

  function clearSelection() {
    selectedVmIds = [];
    tickets = {};
    focusedVmId = null;
  }

  // The focused-station view is a full-screen "modal" (fixed inset-0) but
  // isn't built on the Dialog primitive — it owns the whole viewport for
  // the embedded console iframe, which Dialog's centered/portal layout
  // isn't a good fit for. Escape still needs to back out of it, though;
  // this only fires while keyboard focus is on the parent document (not
  // inside the console iframe, which is its own document and captures
  // its own Escape for the guest OS).
  function handleGlobalKeydown(e) {
    if (e.key === 'Escape' && focusedVmId) {
      e.preventDefault();
      focusedVmId = null;
    }
  }

  function popoutConsole(vm) {
    const ticket = tickets[vm.id] || '';
    const url = `/console/${vm.id}?vt=${encodeURIComponent(ticket)}`;
    window.open(url, `console_${vm.id}`, 'width=1024,height=768,menubar=no,toolbar=no,location=no');
  }

  function toggleFullscreen() {
    const el = document.getElementById('multiview-container');
    if (!document.fullscreenElement) {
      el?.requestFullscreen().catch(() => {});
      isFullscreen = true;
    } else {
      document.exitFullscreen().catch(() => {});
      isFullscreen = false;
    }
  }

  function reloadTile(vmId) {
    const el = document.querySelector(`iframe[data-vm-id="${vmId}"]`);
    if (el) {
      el.src = el.src;
    } else {
      fetchTicketFor(vmId);
    }
    toast.info(t('multiview.reconnectingStation'));
  }

  function reloadAllTiles() {
    document.querySelectorAll('iframe[data-vm-id]').forEach((el) => {
      el.src = el.src;
    });
    toast.success(t('multiview.allConsolesRestarted'));
  }

  function sendCtrlAltDel(vm) {
    toast.info(t('multiview.sendingCadTo', { name: vm.name }));
    const iframes = document.querySelectorAll(`iframe[data-vm-id="${vm.id}"]`);
    if (iframes.length > 0) {
      try {
        iframes[0].contentWindow?.postMessage(
          { action: 'send_key_macro', macro: 'cad' },
          window.location.origin
        );
      } catch {
        /* ignore */
      }
    }
  }

  function broadcastCtrlAltDel() {
    showBroadcastMenu = false;
    const runningCount = activeVms.filter((v) => v.state === 'running').length;
    toast.info(t('multiview.sendingCadBroadcast', { count: runningCount }));
    document.querySelectorAll('iframe').forEach((frame) => {
      try {
        frame.contentWindow?.postMessage(
          { action: 'send_key_macro', macro: 'cad' },
          window.location.origin
        );
      } catch {
        /* ignore */
      }
    });
  }

  async function broadcastStartAll() {
    showBroadcastMenu = false;
    const stopped = activeVms.filter((v) => v.state !== 'running');
    if (stopped.length === 0) {
      toast.info(t('multiview.allAlreadyOn'));
      return;
    }
    toast.info(t('multiview.startingBatch', { count: stopped.length }));
    for (const vm of stopped) {
      api.startVM(vm.id).catch(() => {});
    }
    later(loadVMs, 3000);
  }

  // Groups extracted from KVM VMs
  let availableGroups = $derived.by(() => {
    const set = new SvelteSet();
    vms.forEach((v) => {
      if (Array.isArray(v.groups)) v.groups.forEach((g) => set.add(g));
    });
    return Array.from(set);
  });

  // Selector Filtered VMs
  let filteredSelectorVms = $derived.by(() => {
    let list = vms;
    const q = selectorSearch.toLowerCase().trim();
    if (selectorGroupFilter !== 'all') {
      list = list.filter((v) => Array.isArray(v.groups) && v.groups.includes(selectorGroupFilter));
    }
    if (selectorStateFilter !== 'all') {
      list = list.filter((v) => v.state === selectorStateFilter);
    }
    if (q) {
      list = list.filter(
        (v) =>
          (v.name || '').toLowerCase().includes(q) ||
          (v.alias && v.alias.toLowerCase().includes(q)) ||
          (v.ip && v.ip.includes(q)) ||
          (v.ips && v.ips.some((ip) => ip.includes(q)))
      );
    }
    return list;
  });

  onMount(() => {
    loadVMs();
    pollInterval = setInterval(loadVMs, 15000);
    const fsHandler = () => {
      isFullscreen = !!document.fullscreenElement;
    };
    document.addEventListener('fullscreenchange', fsHandler);

    if (browser) {
      try {
        density = localStorage.getItem('webkvm.multiview.density.v1') || 'compact';
        headerCompact = localStorage.getItem('webkvm.multiview.compact.v1') === 'true';
        const savedCols = parseInt(localStorage.getItem('webkvm.multiview.cols.v3') || '2', 10);
        columns = savedCols >= 1 && savedCols <= 6 ? savedCols : 2;
        const savedH = parseInt(localStorage.getItem('webkvm.multiview.height.v3') || '480', 10);
        tileHeight = savedH >= 260 && savedH <= 1000 ? savedH : 480;
      } catch {
        /* ignore */
      }
    }

    return () => {
      document.removeEventListener('fullscreenchange', fsHandler);
      if (pollInterval) clearInterval(pollInterval);
      if (timerInterval) clearInterval(timerInterval);
    };
  });

  onDestroy(() => {
    if (pollInterval) clearInterval(pollInterval);
    if (timerInterval) clearInterval(timerInterval);
    // The delayed post-start refreshes below were bare setTimeouts, not
    // tracked by the component: navigating away within 2.5 s of starting
    // a VM still fired fetchTicketFor (issuing a real API request and
    // writing to destroyed state) and loadVMs.
    for (const t of pendingTimers) clearTimeout(t);
    pendingTimers.clear();
  });

  /** setTimeout that is cancelled automatically on unmount. */
  const pendingTimers = new SvelteSet();
  function later(fn, ms) {
    const t = setTimeout(() => {
      pendingTimers.delete(t);
      fn();
    }, ms);
    pendingTimers.add(t);
    return t;
  }

  let activeVms = $derived(
    focusedVmId
      ? vms.filter((v) => v.id === focusedVmId)
      : vms.filter((v) => selectedVmIds.includes(v.id))
  );

  // Dynamic Responsive Grid Layout Engine
  let gridStyle = $derived.by(() => {
    if (focusedVmId) {
      return 'display: flex; flex-direction: column; width: 100%; height: 100%; flex: 1 1 0%; min-height: 0; min-width: 0;';
    }
    const count = activeVms.length;
    if (count === 0) return 'width: 100%; height: 100%;';

    const gapMap = { none: '0px', compact: '6px', normal: '12px' };
    const gap = gapMap[density] || '6px';

    const cols = columns > 0 ? columns : count === 1 ? 1 : 2;
    return `display: grid; grid-template-columns: repeat(${cols}, minmax(0, 1fr)); gap: ${gap}; width: 100%; auto-rows: ${tileHeight}px;`;
  });
</script>

<svelte:window onkeydown={handleGlobalKeydown} />

<!-- MultiView Main Outer Container -->
{#if focusedVmId}
  <!-- Fullscreen Modal Mode for Focused Station -->
  {@const focusedVm = vms.find((v) => v.id === focusedVmId)}
  <div
    class="fixed inset-0 z-50 flex flex-col bg-background animate-fadeIn select-none"
    style="margin: 0; padding: 0;"
    role="dialog"
    aria-modal="true"
    aria-label={t('multiview.focusedStation')}
  >
    <!-- Top Focused HUD Bar -->
    <div
      class="h-10 px-4 bg-card border-b border-border flex items-center justify-between shrink-0 z-10 shadow-sm"
    >
      <div class="flex items-center gap-2.5">
        <span
          class="w-2.5 h-2.5 rounded-full {focusedVm?.state === 'running'
            ? 'bg-success shadow-[0_0_8px_rgba(34,197,94,0.8)]'
            : 'bg-muted-foreground'}"
        ></span>
        <span class="font-bold text-sm text-foreground">
          {focusedVm?.alias || focusedVm?.name || 'VM'}
        </span>
        {#if focusedVm?.ip}
          <span class="text-xs font-mono text-muted-foreground px-2 py-0.5 rounded bg-muted/60"
            >{focusedVm.ip}</span
          >
        {/if}
        <span class="text-[11px] text-muted-foreground hidden sm:inline"
          >({t('multiview.focusedStation')})</span
        >
      </div>

      <div class="flex items-center gap-1.5">
        {#if focusedVm}
          <Button
            size="xs"
            variant="outline"
            onclick={() => popoutConsole(focusedVm)}
            title={t('multiview.popoutTitle')}
          >
            <Icon name="externalLink" size={13} class="mr-1" />
            <span class="hidden sm:inline">{t('multiview.popoutTitle')}</span>
          </Button>
          <Button
            size="xs"
            variant="outline"
            onclick={() => reloadTile(focusedVm.id)}
            title={t('multiview.reconnectTitle')}
          >
            <Icon name="refresh" size={13} class="mr-1" />
            <span class="hidden sm:inline">{t('multiview.reconnectTitle')}</span>
          </Button>
          <Button
            size="xs"
            variant="outline"
            onclick={() => sendCtrlAltDel(focusedVm)}
            title="Ctrl+Alt+Del"
          >
            <Icon name="keyboard" size={13} class="mr-1 text-warning" />
            <span class="hidden sm:inline">Ctrl+Alt+Del</span>
          </Button>
        {/if}
        <Button
          size="xs"
          variant="primary"
          onclick={() => (focusedVmId = null)}
          class="font-semibold"
        >
          <Icon name="grid" size={13} class="mr-1" />
          <span>{t('multiview.restoreGrid')}</span>
        </Button>
      </div>
    </div>

    <!-- Full-Size Screen Frame Canvas (100% Window) -->
    <div
      class="flex-1 relative w-full h-full min-h-0 min-w-0 bg-black flex items-center justify-center overflow-hidden"
    >
      {#if focusedVm && focusedVm.state === 'running'}
        {#if tickets[focusedVm.id]}
          <iframe
            src="/console/{focusedVm.id}?vt={encodeURIComponent(tickets[focusedVm.id])}&embedded=1"
            data-vm-id={focusedVm.id}
            title={t('multiview.consoleTitle', { name: focusedVm.name })}
            class="w-full h-full border-0 bg-black {viewOnlyMode ? 'pointer-events-none' : ''}"
            allow="fullscreen; clipboard-read; clipboard-write"
          ></iframe>
        {:else}
          <div class="flex flex-col items-center gap-2 text-muted-foreground text-sm font-mono">
            <Spinner size="md" />
            <span>{t('multiview.connectingVnc')}</span>
          </div>
        {/if}
      {:else}
        <div class="flex flex-col items-center justify-center p-8 text-center gap-3">
          <div
            class="w-12 h-12 rounded-2xl bg-muted border border-border flex items-center justify-center text-muted-foreground shadow-inner"
          >
            <Icon name="power" size={24} />
          </div>
          <p class="text-sm font-bold text-foreground/80">
            {focusedVm?.name || 'VM'}
            {t('multiview.stoppedStationTitle')}
          </p>
          {#if focusedVm}
            <Button size="sm" onclick={() => toggleVmPower(focusedVm)}>
              <Icon name="play" size={14} class="mr-1 text-success" />
              <span>{t('multiview.powerOn')}</span>
            </Button>
          {/if}
        </div>
      {/if}
    </div>
  </div>
{/if}

<div
  class="w-full flex-1 flex flex-col min-h-0 min-w-0 {isFullscreen
    ? 'p-0'
    : density === 'none' && headerCompact
      ? 'p-0'
      : 'p-2 sm:p-3'} overflow-y-auto"
  id="multiview-container"
  class:bg-background={isFullscreen}
  class:select-none={isResizing}
  class:cursor-ns-resize={isResizing}
>
  {#if error}
    <Alert variant="error" class="mb-2 shrink-0">{t('multiview.loadError', { error })}</Alert>
  {/if}

  <!-- Header Toolbar: Compact HUD (36px) vs Full Spacious Banner -->
  {#if !focusedVmId}
    {#if headerCompact}
      <!-- Minimal Ultra-Compact 36px Toolbar -->
      <div
        class="h-9 px-2.5 mb-2 rounded-xl bg-card border border-border flex items-center justify-between gap-2 shrink-0 text-xs shadow-sm"
      >
        <!-- Left: Title & Quick Station Selector -->
        <div class="flex items-center gap-2 min-w-0">
          <button
            onclick={() => (showVmSelectorModal = true)}
            class="flex items-center gap-1.5 px-2.5 py-1 rounded-lg bg-accent text-accent-foreground font-semibold hover:bg-accent-hover transition-colors shrink-0 text-xs"
          >
            <Icon name="monitor" size={14} />
            <span>{t('multiview.title')} ({selectedVmIds.length})</span>
          </button>

          <!-- Broadcast Menu Trigger -->
          <div class="relative">
            <button
              class="flex items-center gap-1 px-2 py-1 rounded-lg bg-muted text-foreground hover:bg-muted/80 border border-border transition-colors text-xs"
              onclick={() => (showBroadcastMenu = !showBroadcastMenu)}
            >
              <Icon name="zap" size={13} class="text-warning" />
              <span class="hidden md:inline">{t('multiview.broadcast')}</span>
            </button>
            {#if showBroadcastMenu}
              <div
                class="absolute left-0 top-full mt-1.5 w-60 rounded-xl border border-border bg-popover text-popover-foreground p-1.5 shadow-2xl z-50 animate-fadeIn space-y-0.5"
              >
                <button
                  class="flex items-center gap-2.5 w-full px-2.5 py-1.5 text-xs font-medium text-foreground rounded-lg hover:bg-accent/15 hover:text-accent transition-colors text-left"
                  onclick={broadcastStartAll}
                >
                  <Icon name="play" size={14} class="text-success" />
                  <span>{t('multiview.broadcastStartOff')}</span>
                </button>
                <button
                  class="flex items-center gap-2.5 w-full px-2.5 py-1.5 text-xs font-medium text-foreground rounded-lg hover:bg-accent/15 hover:text-accent transition-colors text-left"
                  onclick={reloadAllTiles}
                >
                  <Icon name="refresh" size={14} class="text-accent" />
                  <span>{t('multiview.broadcastReconnect')}</span>
                </button>
                <button
                  class="flex items-center gap-2.5 w-full px-2.5 py-1.5 text-xs font-medium text-foreground rounded-lg hover:bg-accent/15 hover:text-accent transition-colors text-left"
                  onclick={broadcastCtrlAltDel}
                >
                  <Icon name="keyboard" size={14} class="text-warning" />
                  <span>{t('multiview.broadcastCtrlAltDel')}</span>
                </button>
              </div>
            {/if}
          </div>
        </div>

        <!-- Center: Timer & Interactive Mode -->
        <div class="hidden lg:flex items-center gap-2">
          <div
            class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-lg bg-background border border-border font-mono text-xs"
          >
            <Icon
              name="clock"
              size={13}
              class={timerActive ? 'text-warning animate-pulse' : 'text-muted-foreground'}
            />
            <span class="font-bold {timerActive ? 'text-warning' : 'text-foreground'}"
              >{formatTimer(timerSeconds)}</span
            >
            <button
              onclick={toggleTimer}
              class="hover:text-accent ml-0.5"
              title={timerActive ? t('multiview.timerPauseTitle') : t('multiview.timerStartTitle')}
            >
              <Icon name={timerActive ? 'pause' : 'play'} size={12} />
            </button>
          </div>

          <button
            onclick={() => (viewOnlyMode = !viewOnlyMode)}
            class="inline-flex items-center gap-1 px-2 py-0.5 rounded-lg border text-xs font-medium transition-colors {viewOnlyMode
              ? 'bg-warning/15 border-warning/30 text-warning'
              : 'bg-background border-border text-foreground'}"
            title={viewOnlyMode ? t('multiview.viewOnlyOnTitle') : t('multiview.viewOnlyOffTitle')}
          >
            <Icon name={viewOnlyMode ? 'eye' : 'mousePointer'} size={13} />
            <span>{viewOnlyMode ? t('multiview.viewOnly') : t('multiview.interactive')}</span>
          </button>
        </div>

        <!-- Right: Auto-Adjust & Density Controls -->
        <div class="flex items-center gap-1.5 shrink-0">
          <!-- Grid Columns Selector (1, 2, 3, 4) -->
          <div
            class="hidden sm:inline-flex rounded-lg bg-background border border-border p-0.5 text-[11px]"
          >
            <button
              onclick={() => setColumns(1)}
              class="px-1.5 py-0.5 rounded {columns === 1
                ? 'bg-accent text-accent-foreground font-bold'
                : 'text-muted-foreground hover:text-foreground'}"
              title="1 Col">1</button
            >
            <button
              onclick={() => setColumns(2)}
              class="px-1.5 py-0.5 rounded {columns === 2
                ? 'bg-accent text-accent-foreground font-bold'
                : 'text-muted-foreground hover:text-foreground'}"
              title="2 Cols (Defecto - 2 pantallas)">2</button
            >
            <button
              onclick={() => setColumns(3)}
              class="px-1.5 py-0.5 rounded {columns === 3
                ? 'bg-accent text-accent-foreground font-bold'
                : 'text-muted-foreground hover:text-foreground'}"
              title="3 Cols">3</button
            >
            <button
              onclick={() => setColumns(4)}
              class="px-1.5 py-0.5 rounded {columns === 4
                ? 'bg-accent text-accent-foreground font-bold'
                : 'text-muted-foreground hover:text-foreground'}"
              title="4 Cols">4</button
            >
          </div>

          <!-- Interactive Height Slider Control -->
          <div
            class="hidden md:inline-flex items-center gap-1.5 px-2 py-0.5 rounded-lg bg-background border border-border text-[11px]"
          >
            <span class="text-muted-foreground text-[10px]">{t('multiview.heightLabel')}</span>
            <input
              type="range"
              min="260"
              max="850"
              step="10"
              bind:value={tileHeight}
              oninput={saveTileHeight}
              class="w-20 sm:w-24 h-1.5 bg-muted rounded-lg accent-accent cursor-pointer"
              title={t('multiview.heightTooltip')}
            />
            <span class="font-mono text-[10px] text-foreground font-bold">{tileHeight}px</span>
          </div>

          <!-- Density Selector -->
          <div
            class="hidden sm:inline-flex rounded-lg bg-background border border-border p-0.5 text-[11px]"
          >
            <button
              onclick={() => setDensity('none')}
              class="px-1.5 py-0.5 rounded {density === 'none'
                ? 'bg-accent text-accent-foreground font-bold'
                : 'text-muted-foreground hover:text-foreground'}"
              title={t('multiview.densityNone')}>0px</button
            >
            <button
              onclick={() => setDensity('compact')}
              class="px-1.5 py-0.5 rounded {density === 'compact'
                ? 'bg-accent text-accent-foreground font-bold'
                : 'text-muted-foreground hover:text-foreground'}"
              title={t('multiview.densityCompact')}>4px</button
            >
            <button
              onclick={() => setDensity('normal')}
              class="px-1.5 py-0.5 rounded {density === 'normal'
                ? 'bg-accent text-accent-foreground font-bold'
                : 'text-muted-foreground hover:text-foreground'}"
              title={t('multiview.densityNormal')}>10px</button
            >
          </div>

          <!-- Fullscreen -->
          <button
            onclick={toggleFullscreen}
            class="p-1 rounded-lg bg-muted text-foreground hover:bg-muted/80 border border-border"
            title={t('multiview.fullscreenTitle')}
          >
            <Icon name={isFullscreen ? 'minimize' : 'maximize'} size={14} />
          </button>

          <!-- Expand to full banner -->
          <button
            onclick={toggleHeaderCompact}
            class="p-1 rounded-lg hover:bg-muted text-muted-foreground hover:text-foreground"
            title={t('multiview.expandToolbar')}
          >
            <Icon name="chevronDown" size={14} />
          </button>
        </div>
      </div>
    {:else}
      <!-- Full Spacious Header Card -->
      <div
        class="rounded-2xl bg-card border border-border p-3.5 sm:p-4 shadow-md space-y-3 shrink-0 mb-2"
      >
        <!-- Top Row -->
        <div
          class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 pb-3 border-b border-border"
        >
          <div class="flex items-center gap-3">
            <div
              class="w-9 h-9 rounded-xl bg-accent/15 border border-accent/30 flex items-center justify-center text-accent shadow-sm shrink-0"
            >
              <Icon name="monitor" size={18} />
            </div>
            <div>
              <div class="flex items-center gap-2 flex-wrap">
                <h1 class="text-sm sm:text-base font-bold text-foreground tracking-tight">
                  {t('multiview.title')}
                </h1>
                <span
                  class="px-2 py-0.2 text-[10px] font-semibold uppercase rounded-full bg-accent/15 text-accent border border-accent/30"
                >
                  KVM RFB
                </span>
              </div>
              <p class="text-[11px] text-muted-foreground">{t('multiview.subtitle')}</p>
            </div>
          </div>

          <!-- Actions -->
          <div class="flex items-center flex-wrap gap-2">
            <button
              class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-accent hover:bg-accent-hover text-accent-foreground text-xs font-semibold shadow-sm transition-all active:scale-95"
              onclick={() => (showVmSelectorModal = true)}
            >
              <Icon name="server" size={14} />
              <span>{t('multiview.selectStations', { count: selectedVmIds.length })}</span>
            </button>

            <!-- Broadcast -->
            <div class="relative">
              <button
                class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-muted hover:bg-muted/80 text-foreground text-xs font-medium border border-border transition-all"
                onclick={() => (showBroadcastMenu = !showBroadcastMenu)}
              >
                <Icon name="zap" size={14} class="text-warning" />
                <span>{t('multiview.broadcast')}</span>
              </button>
              {#if showBroadcastMenu}
                <div
                  class="absolute right-0 top-full mt-2 w-64 rounded-2xl border border-border bg-popover text-popover-foreground p-2 shadow-2xl z-50 animate-fadeIn space-y-1"
                >
                  <button
                    class="flex items-center gap-3 w-full px-3 py-2 text-xs font-medium text-foreground rounded-xl hover:bg-accent/15 hover:text-accent transition-colors"
                    onclick={broadcastStartAll}
                  >
                    <Icon name="play" size={14} class="text-success" />
                    <span>{t('multiview.broadcastStartOff')}</span>
                  </button>
                  <button
                    class="flex items-center gap-3 w-full px-3 py-2 text-xs font-medium text-foreground rounded-xl hover:bg-accent/15 hover:text-accent transition-colors"
                    onclick={reloadAllTiles}
                  >
                    <Icon name="refresh" size={14} class="text-accent" />
                    <span>{t('multiview.broadcastReconnect')}</span>
                  </button>
                  <button
                    class="flex items-center gap-3 w-full px-3 py-2 text-xs font-medium text-foreground rounded-xl hover:bg-accent/15 hover:text-accent transition-colors"
                    onclick={broadcastCtrlAltDel}
                  >
                    <Icon name="keyboard" size={14} class="text-warning" />
                    <span>{t('multiview.broadcastCtrlAltDel')}</span>
                  </button>
                </div>
              {/if}
            </div>

            <!-- Fullscreen & Collapse Toolbar -->
            <button
              class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-muted hover:bg-muted/80 text-foreground text-xs font-medium border border-border transition-all"
              onclick={toggleFullscreen}
              title={t('multiview.fullscreenTitle')}
            >
              <Icon name={isFullscreen ? 'minimize' : 'maximize'} size={14} />
              <span>{isFullscreen ? t('multiview.exitFullscreen') : t('multiview.videoWall')}</span>
            </button>

            <button
              class="p-1.5 rounded-xl text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
              onclick={toggleHeaderCompact}
              title={t('multiview.compactToolbar')}
            >
              <Icon name="chevronUp" size={16} />
            </button>
          </div>
        </div>

        <!-- Bottom Controls -->
        <div class="flex flex-wrap items-center justify-between gap-3 text-xs">
          <!-- Timer & Mode -->
          <div class="flex items-center flex-wrap gap-2.5">
            <div
              class="inline-flex items-center gap-2 px-3 py-1.5 rounded-xl bg-background border border-border font-mono"
            >
              <Icon
                name="clock"
                size={14}
                class={timerActive ? 'text-warning animate-pulse' : 'text-muted-foreground'}
              />
              <span class="font-bold {timerActive ? 'text-warning' : 'text-foreground'}"
                >{formatTimer(timerSeconds)}</span
              >
              <button class="p-0.5 hover:text-accent" onclick={toggleTimer}>
                <Icon name={timerActive ? 'pause' : 'play'} size={12} />
              </button>
              {#if timerSeconds > 0}
                <button class="p-0.5 hover:text-destructive" onclick={resetTimer}>
                  <Icon name="rotateCcw" size={12} />
                </button>
              {/if}
            </div>

            <button
              class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-xl border font-medium transition-all {viewOnlyMode
                ? 'bg-warning/15 border-warning/40 text-warning'
                : 'bg-background border-border text-foreground'}"
              onclick={() => (viewOnlyMode = !viewOnlyMode)}
            >
              <Icon name={viewOnlyMode ? 'eye' : 'mousePointer'} size={14} />
              <span>{viewOnlyMode ? t('multiview.viewOnly') : t('multiview.interactive')}</span>
            </button>
          </div>

          <!-- Layout Controls & Interactive Height -->
          <div class="flex items-center flex-wrap gap-3">
            <!-- Columns -->
            <div class="inline-flex items-center gap-1">
              <span class="text-muted-foreground hidden sm:inline">Columnas:</span>
              <div class="inline-flex rounded-xl bg-background border border-border p-1 text-xs">
                <button
                  onclick={() => setColumns(1)}
                  class="px-2.5 py-0.5 rounded-lg {columns === 1
                    ? 'bg-accent text-accent-foreground font-bold'
                    : 'text-muted-foreground hover:text-foreground'}">1</button
                >
                <button
                  onclick={() => setColumns(2)}
                  class="px-2.5 py-0.5 rounded-lg {columns === 2
                    ? 'bg-accent text-accent-foreground font-bold'
                    : 'text-muted-foreground hover:text-foreground'}">2 (Defecto)</button
                >
                <button
                  onclick={() => setColumns(3)}
                  class="px-2.5 py-0.5 rounded-lg {columns === 3
                    ? 'bg-accent text-accent-foreground font-bold'
                    : 'text-muted-foreground hover:text-foreground'}">3</button
                >
                <button
                  onclick={() => setColumns(4)}
                  class="px-2.5 py-0.5 rounded-lg {columns === 4
                    ? 'bg-accent text-accent-foreground font-bold'
                    : 'text-muted-foreground hover:text-foreground'}">4</button
                >
              </div>
            </div>

            <!-- Altura Interactiva -->
            <div
              class="inline-flex items-center gap-2 px-3 py-1 rounded-xl bg-background border border-border text-xs"
            >
              <span class="text-muted-foreground">{t('multiview.heightLabel')}</span>
              <input
                type="range"
                min="260"
                max="900"
                step="10"
                bind:value={tileHeight}
                oninput={saveTileHeight}
                class="w-28 sm:w-36 h-1.5 bg-muted rounded-lg accent-accent cursor-pointer"
                title={t('multiview.heightTooltip')}
              />
              <span class="font-mono text-foreground font-bold min-w-[40px] text-right"
                >{tileHeight}px</span
              >
              <div class="hidden lg:inline-flex items-center gap-1 border-l border-border pl-2">
                <button
                  onclick={() => {
                    tileHeight = 360;
                    saveTileHeight();
                  }}
                  class="px-1.5 py-0.5 rounded text-[10px] {tileHeight === 360
                    ? 'bg-accent text-accent-foreground font-bold'
                    : 'text-muted-foreground hover:text-foreground'}">S</button
                >
                <button
                  onclick={() => {
                    tileHeight = 480;
                    saveTileHeight();
                  }}
                  class="px-1.5 py-0.5 rounded text-[10px] {tileHeight === 480
                    ? 'bg-accent text-accent-foreground font-bold'
                    : 'text-muted-foreground hover:text-foreground'}">M (480px)</button
                >
                <button
                  onclick={() => {
                    tileHeight = 620;
                    saveTileHeight();
                  }}
                  class="px-1.5 py-0.5 rounded text-[10px] {tileHeight === 620
                    ? 'bg-accent text-accent-foreground font-bold'
                    : 'text-muted-foreground hover:text-foreground'}">L (620px)</button
                >
              </div>
            </div>

            <!-- Density -->
            <div class="inline-flex items-center gap-1">
              <span class="text-muted-foreground hidden sm:inline">{t('multiview.density')}:</span>
              <div class="inline-flex rounded-xl bg-background border border-border p-1">
                <button
                  onclick={() => setDensity('none')}
                  class="px-2 py-0.5 rounded-lg {density === 'none'
                    ? 'bg-accent text-accent-foreground font-bold'
                    : 'text-muted-foreground hover:text-foreground'}"
                  >{t('multiview.densityNone')}</button
                >
                <button
                  onclick={() => setDensity('compact')}
                  class="px-2 py-0.5 rounded-lg {density === 'compact'
                    ? 'bg-accent text-accent-foreground font-bold'
                    : 'text-muted-foreground hover:text-foreground'}"
                  >{t('multiview.densityCompact')}</button
                >
                <button
                  onclick={() => setDensity('normal')}
                  class="px-2 py-0.5 rounded-lg {density === 'normal'
                    ? 'bg-accent text-accent-foreground font-bold'
                    : 'text-muted-foreground hover:text-foreground'}"
                  >{t('multiview.densityNormal')}</button
                >
              </div>
            </div>
          </div>
        </div>
      </div>
    {/if}
  {/if}

  <!-- Single Enlarge Focus Banner -->
  {#if focusedVmId}
    <div
      class="flex items-center justify-between px-3.5 py-2 mb-2 rounded-xl bg-accent/10 border border-accent/40 text-xs text-foreground shadow-sm shrink-0 animate-fadeIn"
    >
      <div class="flex items-center gap-2">
        <Icon name="eye" size={16} class="text-accent animate-pulse" />
        <span
          >{t('multiview.focusedStation')}
          <strong class="text-foreground font-bold"
            >{vms.find((v) => v.id === focusedVmId)?.name}</strong
          ></span
        >
      </div>
      <Button size="xs" onclick={() => (focusedVmId = null)}>
        <Icon name="grid" size={13} class="mr-1" />
        <span>{t('multiview.restoreGrid')}</span>
      </Button>
    </div>
  {/if}

  <!-- Video Wall Grid Area (Fills 100% of Viewport) -->
  {#if loading && vms.length === 0}
    <div class="flex-1 flex flex-col items-center justify-center py-20 gap-3">
      <Spinner size="lg" />
      <p class="text-xs text-muted-foreground">{t('multiview.connectingFleet')}</p>
    </div>
  {:else if activeVms.length === 0}
    <div
      class="flex-1 flex flex-col items-center justify-center rounded-2xl border border-dashed border-border bg-card/40"
    >
      <EmptyState
        icon="monitor"
        title={t('multiview.emptyTitle')}
        description={t('multiview.emptyDesc')}
      >
        {#snippet action()}
          <div class="flex items-center justify-center gap-2">
            <Button size="sm" onclick={() => (showVmSelectorModal = true)}>
              <Icon name="plus" size={13} class="mr-1" />
              <span>{t('multiview.chooseStations')}</span>
            </Button>
            <Button variant="outline" size="sm" onclick={selectAllRunning}>
              <Icon name="play" size={13} class="mr-1 text-success" />
              <span>{t('multiview.superviseAll')}</span>
            </Button>
          </div>
        {/snippet}
      </EmptyState>
    </div>
  {:else}
    <!-- Zero-Scroll 100% Viewport Fill Grid -->
    <div class="flex-1 min-h-0 min-w-0" style={gridStyle}>
      {#each activeVms as vm, index (vm.id)}
        <div
          class="group flex flex-col rounded-xl overflow-hidden border border-border bg-card shadow-sm transition-all w-full min-w-0 {focusedVmId ===
          vm.id
            ? 'border-accent ring-2 ring-accent/40 flex-1 h-full min-h-0'
            : 'hover:border-border-hover'}"
          style={focusedVmId ? 'height: 100%; min-height: 0;' : `height: ${tileHeight}px;`}
          ondblclick={() => (focusedVmId = focusedVmId === vm.id ? null : vm.id)}
          role="presentation"
        >
          <!-- Compact Tile Header (30px) -->
          <div
            class="h-7 px-2 flex items-center justify-between bg-card border-b border-border select-none shrink-0 gap-1.5"
          >
            <!-- Left: Status indicator & Name -->
            <div class="flex items-center gap-1.5 min-w-0 pr-1">
              <span
                class="w-2 h-2 rounded-full shrink-0 {vm.state === 'running'
                  ? 'bg-success shadow-[0_0_6px_rgba(34,197,94,0.8)]'
                  : 'bg-muted-foreground'}"
                title={vm.state === 'running'
                  ? t('multiview.runningVncTitle')
                  : t('multiview.stoppedStationTitle')}
              ></span>
              <span
                class="text-[10px] font-mono font-bold px-1.5 py-0.2 rounded bg-accent/15 text-accent shrink-0"
              >
                #{index + 1}
              </span>
              <span class="font-bold text-xs text-foreground truncate min-w-0" title={vm.name}>
                {vm.alias || vm.name}
              </span>
              {#if vm.ip}
                <span class="hidden xl:inline text-[10px] font-mono text-muted-foreground truncate"
                  >{vm.ip}</span
                >
              {/if}
            </div>

            <!-- Right: Action Icons -->
            <div
              class="flex items-center gap-0.5 shrink-0 opacity-70 group-hover:opacity-100 transition-opacity"
            >
              <button
                class="p-1 rounded text-muted-foreground hover:text-accent hover:bg-muted transition-colors"
                title={focusedVmId === vm.id
                  ? t('multiview.restoreTile')
                  : t('multiview.maximizeStation')}
                onclick={() => (focusedVmId = focusedVmId === vm.id ? null : vm.id)}
              >
                <Icon name={focusedVmId === vm.id ? 'minimize' : 'maximize'} size={13} />
              </button>
              <button
                class="p-1 rounded text-muted-foreground hover:text-accent hover:bg-muted transition-colors"
                title={t('multiview.popoutTitle')}
                onclick={() => popoutConsole(vm)}
              >
                <Icon name="externalLink" size={13} />
              </button>
              <button
                class="p-1 rounded text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
                title={t('multiview.reconnectTitle')}
                onclick={() => reloadTile(vm.id)}
              >
                <Icon name="refresh" size={13} />
              </button>
              <button
                class="p-1 rounded text-muted-foreground hover:text-warning hover:bg-muted transition-colors"
                title={t('multiview.ctrlAltDelTitle')}
                onclick={() => sendCtrlAltDel(vm)}
              >
                <Icon name="keyboard" size={13} />
              </button>
              <button
                class="p-1 rounded text-muted-foreground hover:text-success hover:bg-muted transition-colors"
                title={vm.state === 'running'
                  ? t('multiview.powerOffTitle')
                  : t('multiview.powerOnStationTitle')}
                onclick={() => toggleVmPower(vm)}
              >
                <Icon name="power" size={13} />
              </button>
              <button
                class="p-1 rounded text-muted-foreground hover:text-destructive hover:bg-muted transition-colors"
                title={t('multiview.removeTitle')}
                onclick={() => toggleVmSelection(vm.id)}
              >
                <Icon name="x" size={13} />
              </button>
            </div>
          </div>

          <!-- Tile Screen Canvas Frame (Fills 100% of tile quadrant) -->
          <div
            class="flex-1 relative w-full h-full min-h-0 min-w-0 bg-background flex items-center justify-center overflow-hidden"
          >
            {#if vm.state === 'running'}
              {#if tickets[vm.id]}
                <iframe
                  src="/console/{vm.id}?vt={encodeURIComponent(tickets[vm.id])}&embedded=1"
                  data-vm-id={vm.id}
                  title={t('multiview.consoleTitle', { name: vm.name })}
                  class="w-full h-full border-0 bg-background {viewOnlyMode
                    ? 'pointer-events-none'
                    : ''}"
                  allow="fullscreen; clipboard-read; clipboard-write"
                  loading="lazy"
                ></iframe>

                {#if viewOnlyMode}
                  <div
                    class="absolute bottom-2 left-2 px-2 py-0.5 rounded bg-background/90 border border-border text-[10px] text-warning backdrop-blur-sm pointer-events-none flex items-center gap-1 shadow-sm"
                  >
                    <Icon name="eye" size={11} />
                    <span>{t('multiview.viewOnlyBadge')}</span>
                  </div>
                {/if}
              {:else}
                <div
                  class="flex flex-col items-center gap-2 text-muted-foreground text-xs font-mono"
                >
                  <Spinner size="sm" />
                  <span>{t('multiview.connectingVnc')}</span>
                </div>
              {/if}
            {:else}
              <!-- VM Stopped State Display -->
              <div class="flex flex-col items-center justify-center p-4 text-center gap-2">
                <div
                  class="w-9 h-9 rounded-xl bg-muted border border-border flex items-center justify-center text-muted-foreground shadow-inner"
                >
                  <Icon name="power" size={18} />
                </div>
                <p class="text-xs font-bold text-foreground/80">
                  {t('multiview.stationOff', { n: index + 1 })}
                </p>
                <Button size="xs" onclick={() => toggleVmPower(vm)}>
                  <Icon name="play" size={12} class="mr-1 text-success" />
                  <span>{t('multiview.powerOn')}</span>
                </Button>
              </div>
            {/if}
          </div>

          <!-- Bottom Resize Handle (drag with mouse to resize) -->
          {#if !focusedVmId}
            <!--
              role="slider", not "separator": the handle sets a value
              within a range, and a separator is not a focusable widget —
              which is exactly what the a11y warning was pointing at.
              With the role comes the value contract below, so assistive
              tech can announce the current height instead of an
              anonymous, silent control.
            -->
            <div
              role="slider"
              tabindex="0"
              aria-label={t('multiview.resizeLabel')}
              aria-orientation="vertical"
              aria-valuemin={MIN_TILE_H}
              aria-valuemax={MAX_TILE_H}
              aria-valuenow={tileHeight}
              aria-valuetext={`${tileHeight}px`}
              class="h-2.5 w-full bg-card hover:bg-accent/40 cursor-ns-resize transition-colors flex items-center justify-center select-none border-t border-border/40 group-hover:border-accent/40 shrink-0"
              title={t('multiview.resizeHint')}
              onmousedown={onResizeStart}
              ontouchstart={onResizeStart}
              onkeydown={onResizeKey}
            >
              <div
                class="w-10 h-1 rounded-full bg-muted-foreground/30 group-hover:bg-accent/80 transition-colors"
              ></div>
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Dedicated Modal for Station Selection -->
<Dialog.Root bind:open={showVmSelectorModal}>
  <Dialog.Content class="sm:max-w-4xl max-h-[90vh] flex flex-col">
    <Dialog.Header>
      <div class="flex items-center gap-3">
        <div
          class="w-9 h-9 rounded-xl bg-accent/15 border border-accent/30 flex items-center justify-center text-accent shadow-sm"
        >
          <Icon name="checkSquare" size={18} />
        </div>
        <div>
          <Dialog.Title>{t('multiview.modalTitle')}</Dialog.Title>
          <Dialog.Description>
            {t('multiview.modalSubtitle', { selected: selectedVmIds.length, total: vms.length })}
          </Dialog.Description>
        </div>
      </div>
    </Dialog.Header>

    <!-- Quick Action Buttons & Search Filters -->
    <div class="space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-2.5">
        <!-- Filter Buttons -->
        <div class="flex items-center flex-wrap gap-1.5">
          <Button variant="outline" size="xs" onclick={selectAllRunning}
            >{t('multiview.onlyRunning')}</Button
          >
          <Button variant="outline" size="xs" onclick={selectAllFiltered}
            >{t('multiview.selectFiltered')}</Button
          >
          <Button variant="outline" size="xs" onclick={invertSelection}
            >{t('multiview.invert')}</Button
          >
          <Button variant="ghost" size="xs" onclick={clearSelection}
            >{t('multiview.clearSelection')}</Button
          >
        </div>

        <!-- Group Filters -->
        {#if availableGroups.length > 0}
          <div class="flex items-center gap-1 overflow-x-auto max-w-full">
            <button
              class="px-2.5 py-0.5 text-xs rounded-lg font-medium transition-colors {selectorGroupFilter ===
              'all'
                ? 'bg-accent text-accent-foreground'
                : 'bg-muted text-muted-foreground hover:text-foreground'}"
              onclick={() => (selectorGroupFilter = 'all')}
            >
              {t('multiview.all')}
            </button>
            {#each availableGroups as group (group)}
              <button
                class="px-2.5 py-0.5 text-xs rounded-lg font-medium transition-colors {selectorGroupFilter ===
                group
                  ? 'bg-accent text-accent-foreground'
                  : 'bg-muted text-muted-foreground hover:text-foreground'}"
                onclick={() => (selectorGroupFilter = group)}
              >
                {group}
              </button>
            {/each}
          </div>
        {/if}
      </div>

      <!-- Search Input -->
      <div class="relative w-full">
        <Icon
          name="search"
          size={15}
          class="absolute left-3.5 top-1/2 -translate-y-1/2 text-muted-foreground"
        />
        <input
          type="text"
          placeholder={t('multiview.searchPlaceholder')}
          bind:value={selectorSearch}
          class="w-full pl-9 pr-3.5 py-2 text-xs sm:text-sm rounded-xl bg-background border border-border text-foreground placeholder:text-muted-foreground focus:outline-none focus:border-accent transition-colors shadow-inner"
        />
      </div>
    </div>

    <!-- Station Selection Grid -->
    <div
      class="flex-1 min-w-0 overflow-y-auto pr-1 grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 gap-2.5 min-h-[200px]"
    >
      <!--
          Key by vm.id, not by the object itself. loadVMs() runs every
          15 s and does `vms = rawList.filter(...)`, which produces a
          brand-new object for every VM on each poll. With `(vm)` as the
          key Svelte saw 100% identity churn and tore down and rebuilt
          the whole grid every 15 seconds — so a checkbox being ticked,
          or the text the user had just typed into the filter box, was
          destroyed mid-interaction.
        -->
      {#each filteredSelectorVms as vm, idx (vm.id)}
        <label
          class="group relative flex items-center gap-3 p-3 rounded-xl border cursor-pointer transition-all {selectedVmIds.includes(
            vm.id
          )
            ? 'bg-accent/10 border-accent/60 shadow-sm ring-1 ring-accent/30'
            : 'bg-background border-border hover:bg-card hover:border-border-hover'}"
        >
          <input
            type="checkbox"
            class="rounded border-border text-accent focus:ring-accent w-4 h-4"
            checked={selectedVmIds.includes(vm.id)}
            onchange={() => toggleVmSelection(vm.id)}
          />
          <div class="flex-1 min-w-0">
            <div class="flex items-center justify-between gap-1.5">
              <div class="flex items-center gap-1.5 truncate">
                <span
                  class="w-2 h-2 rounded-full shrink-0 {vm.state === 'running'
                    ? 'bg-success shadow-[0_0_6px_rgba(34,197,94,0.8)]'
                    : 'bg-muted-foreground'}"
                ></span>
                <span class="text-xs font-bold text-foreground truncate">{vm.alias || vm.name}</span
                >
              </div>
              <span
                class="text-[10px] font-mono px-1.5 py-0.2 rounded bg-muted text-muted-foreground font-semibold"
                >#{idx + 1}</span
              >
            </div>
            <div class="flex items-center gap-1.5 mt-1 text-[11px] text-muted-foreground">
              <span class="font-mono text-foreground/90">{vm.ip || t('multiview.noIp')}</span>
              <span>·</span>
              <span>{vm.vcpus}vCPU</span>
              <span>·</span>
              <span>{formatRAM(vm)}</span>
            </div>
          </div>
        </label>
      {/each}
    </div>

    <Dialog.Footer class="items-center justify-between flex-row">
      <span class="text-xs text-muted-foreground">
        {t('multiview.showingStations', { shown: filteredSelectorVms.length, total: vms.length })}
      </span>
      <Button size="sm" onclick={() => (showVmSelectorModal = false)}>
        <span>{t('multiview.applySelection', { count: selectedVmIds.length })}</span>
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<style>
  @keyframes fadeIn {
    from {
      opacity: 0;
      transform: translateY(-4px);
    }
    to {
      opacity: 1;
      transform: translateY(0);
    }
  }
  .animate-fadeIn {
    animation: fadeIn 0.15s cubic-bezier(0.16, 1, 0.3, 1) forwards;
  }
</style>
