<script>
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';

  let { vmId, vm = null, isContainerVm = false, onStart = null } = $props();

  let vncTicket = $state('');
  let loading = $state(false);
  let error = $state('');
  let consoleMode = $state('vnc'); // 'vnc' | 'spice'
  let containerEl = $state(null);
  let iframeEl = $state(null);
  let isFullscreen = $state(false);

  const isRunning = $derived(vm?.state === 'running');
  const graphicsType = $derived(vm?.graphics_type || 'both');

  async function loadTicket() {
    if (!vmId || isContainerVm) return;
    loading = true;
    error = '';
    try {
      const res = await api.getVNCTicket(vmId);
      vncTicket = res.vnc_ticket || '';
    } catch (e) {
      error = e.message || 'Error getting console ticket';
    } finally {
      loading = false;
    }
  }

  function sendMacro(macro) {
    if (!iframeEl?.contentWindow) return;
    try {
      iframeEl.contentWindow.postMessage(
        { action: 'send_key_macro', macro },
        window.location.origin
      );
      toast.info(`Sent ${macro.toUpperCase()}`);
    } catch {
      /* ignore postMessage errors */
    }
  }

  function toggleFullscreen() {
    if (!containerEl) return;
    if (!document.fullscreenElement) {
      containerEl
        .requestFullscreen()
        .then(() => {
          isFullscreen = true;
        })
        .catch(() => {
          /* ignore fullscreen denial */
        });
    } else {
      document
        .exitFullscreen()
        .then(() => {
          isFullscreen = false;
        })
        .catch(() => {
          /* ignore exit error */
        });
    }
  }

  function onFullscreenChange() {
    isFullscreen = !!document.fullscreenElement;
  }

  function openPopout() {
    if (!vmId) return;
    const modeParam = consoleMode === 'spice' ? '&mode=spice' : '';
    const url = `/console/${vmId}?vt=${encodeURIComponent(vncTicket)}${modeParam}`;
    window.open(url, '_blank', 'noopener,noreferrer');
  }

  $effect(() => {
    if (isRunning && !vncTicket && !loading) {
      loadTicket();
    }
  });

  onMount(() => {
    document.addEventListener('fullscreenchange', onFullscreenChange);
    if (isRunning) {
      loadTicket();
    }
  });

  onDestroy(() => {
    if (typeof document !== 'undefined') {
      document.removeEventListener('fullscreenchange', onFullscreenChange);
    }
  });
</script>

<div
  bind:this={containerEl}
  class="flex flex-col rounded-xl border border-border/80 bg-card overflow-hidden shadow-xs transition-all {isFullscreen
    ? 'fixed inset-0 z-50 rounded-none border-none bg-black'
    : 'min-h-[560px] h-[calc(100vh-280px)]'}"
>
  <!-- Console Control Header Toolbar -->
  <div
    class="flex items-center justify-between px-3 py-2 border-b border-border bg-muted/30 shrink-0 gap-2 flex-wrap"
  >
    <div class="flex items-center gap-2">
      {#if isRunning}
        <span
          class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded text-xs font-medium bg-success/15 text-success border border-success/30"
        >
          <span class="w-1.5 h-1.5 rounded-full bg-success animate-pulse"></span>
          {t('vmDetail.inlineConsole')}
        </span>
      {:else}
        <span
          class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded text-xs font-medium bg-muted text-muted-foreground border border-border"
        >
          <span class="w-1.5 h-1.5 rounded-full bg-muted-foreground"></span>
          {t('vmDetail.vmPoweredOffConsole')}
        </span>
      {/if}

      {#if graphicsType === 'both'}
        <div class="inline-flex items-center rounded-md border border-border p-0.5 bg-background">
          <button
            type="button"
            onclick={() => (consoleMode = 'vnc')}
            class="px-2 py-0.5 text-xs font-medium rounded transition-colors {consoleMode === 'vnc'
              ? 'bg-accent/15 text-accent font-semibold'
              : 'text-muted-foreground hover:text-foreground'}"
          >
            noVNC
          </button>
          <button
            type="button"
            onclick={() => (consoleMode = 'spice')}
            class="px-2 py-0.5 text-xs font-medium rounded transition-colors {consoleMode ===
            'spice'
              ? 'bg-accent/15 text-accent font-semibold'
              : 'text-muted-foreground hover:text-foreground'}"
          >
            SPICE
          </button>
        </div>
      {/if}
    </div>

    <!-- Right Controls: Key Macros & Tools -->
    <div class="flex items-center gap-1.5 flex-wrap">
      {#if isRunning && vncTicket}
        <Button
          variant="outline"
          size="sm"
          onclick={() => sendMacro('cad')}
          title="Send Ctrl+Alt+Del"
          class="h-7 px-2 text-xs"
        >
          <Icon name="keyboard" size={13} class="mr-1" />
          {t('vmDetail.sendCad')}
        </Button>
        <Button
          variant="outline"
          size="sm"
          onclick={() => sendMacro('taskmgr')}
          title="Send Task Manager (Ctrl+Shift+Esc)"
          class="h-7 px-2 text-xs hidden sm:inline-flex"
        >
          <Icon name="activity" size={13} class="mr-1" />
          {t('vmDetail.sendTaskManager')}
        </Button>
        <Button
          variant="outline"
          size="sm"
          onclick={loadTicket}
          title={t('vmDetail.reconnect')}
          class="h-7 px-2 text-xs"
        >
          <Icon name="refresh" size={13} class="mr-1" />
          <span class="hidden md:inline">{t('vmDetail.reconnect')}</span>
        </Button>
        <Button
          variant="outline"
          size="sm"
          onclick={toggleFullscreen}
          title={t('vmDetail.fullscreen')}
          class="h-7 px-2 text-xs"
        >
          <Icon name={isFullscreen ? 'minimize' : 'maximize'} size={13} class="mr-1" />
          <span class="hidden md:inline">{t('vmDetail.fullscreen')}</span>
        </Button>
        <Button
          variant="outline"
          size="sm"
          onclick={openPopout}
          title={t('vmDetail.popout')}
          class="h-7 px-2 text-xs"
        >
          <Icon name="external-link" size={13} class="mr-1" />
          <span class="hidden md:inline">{t('vmDetail.popout')}</span>
        </Button>
      {/if}
    </div>
  </div>

  <!-- Screen Area -->
  <div
    class="flex-1 relative w-full h-full min-h-0 min-w-0 bg-black flex items-center justify-center overflow-hidden"
  >
    {#if isRunning}
      {#if loading}
        <div class="flex flex-col items-center gap-2 text-muted-foreground text-sm font-mono">
          <Spinner size="lg" />
          <span>{t('multiview.connectingVnc')}</span>
        </div>
      {:else if error}
        <div
          class="flex flex-col items-center justify-center p-6 text-center gap-2 text-destructive max-w-sm"
        >
          <Icon name="error" size={32} />
          <span class="text-sm font-medium">{error}</span>
          <Button variant="outline" size="sm" onclick={loadTicket} class="mt-2">
            <Icon name="refresh" size={13} class="mr-1.5" />
            {t('vmDetail.reconnect')}
          </Button>
        </div>
      {:else if vncTicket}
        <iframe
          bind:this={iframeEl}
          src="/console/{vmId}?vt={encodeURIComponent(vncTicket)}{consoleMode === 'spice'
            ? '&mode=spice'
            : ''}&embedded=1"
          data-vm-id={vmId}
          title="{vm?.name || 'VM'} Console"
          class="w-full h-full border-0 bg-black"
          allow="fullscreen; clipboard-read; clipboard-write"
        ></iframe>
      {/if}
    {:else}
      <!-- Powered off State -->
      <div class="flex flex-col items-center justify-center p-8 text-center gap-3 max-w-md">
        <div
          class="w-14 h-14 rounded-2xl bg-muted/60 border border-border flex items-center justify-center text-muted-foreground shadow-inner"
        >
          <Icon name="monitor" size={28} />
        </div>
        <div class="space-y-1">
          <h3 class="text-base font-semibold text-foreground">
            {t('vmDetail.vmPoweredOffConsole')}
          </h3>
          <p class="text-xs text-muted-foreground">
            {t('vmDetail.vmPoweredOffConsoleDesc')}
          </p>
        </div>
        {#if onStart}
          <Button onclick={onStart} class="mt-2">
            <Icon name="play" size={14} class="mr-1.5 text-success" />
            {t('vms.start')}
          </Button>
        {/if}
      </div>
    {/if}
  </div>
</div>
