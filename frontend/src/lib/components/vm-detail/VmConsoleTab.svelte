<script>
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import { navigate } from '$lib/router.svelte.js';
  import RemoteVncConsole from './RemoteVncConsole.svelte';
  import { needsClusterAccess } from '$lib/utils/remoteRoutes.js';
  import { fetchConsoleFile, consoleFileErrorText, saveBlob } from '$lib/utils/consoleFile.js';

  let { vmId, vm = null, isContainerVm = false, onStart = null, node = '', hv = '' } = $props();
  // A VM on another node must not use the legacy /console/{id} page (it does
  // not go through node dispatch): connect noVNC in-page with ?node=&hv=.
  const remote = $derived(!!node);
  let errorCode = $state('');
  let remoteRfb = $state(null);

  let vncTicket = $state('');
  let loading = $state(false);
  let error = $state('');
  let consoleMode = $state('vnc'); // 'vnc' | 'spice'
  let containerEl = $state(null);
  let iframeEl = $state(null);
  let isFullscreen = $state(false);

  const isRunning = $derived(vm?.state === 'running');
  const graphicsType = $derived(remote ? 'vnc' : vm?.graphics_type || 'both');

  async function loadTicket() {
    // One ticket request at a time (the effect below and a click could
    // otherwise ask twice on open).
    if (!vmId || isContainerVm || loading) return;
    loading = true;
    error = '';
    errorCode = '';
    try {
      const res = await api.getVNCTicket(vmId, node, hv);
      vncTicket = res.vnc_ticket || '';
    } catch (e) {
      error = e.message || 'Error getting console ticket';
      errorCode = e.code || '';
    } finally {
      loading = false;
    }
  }

  let vvBusy = $state(false);
  // The .vv launcher is fetched here (not a plain <a download>) so a failure
  // (host key not pinned, console busy, ...) shows a translated message
  // instead of silently saving the JSON error as a file.
  async function downloadVv() {
    vvBusy = true;
    try {
      const blob = await fetchConsoleFile(api.getSPICEUrl(vmId, node, hv));
      saveBlob(blob, `${vm?.name || vmId}.vv`);
    } catch (e) {
      toast.error(consoleFileErrorText(t, e));
    } finally {
      vvBusy = false;
    }
  }

  function sendMacro(macro) {
    if (remote) {
      if (macro === 'cad') remoteRfb?.sendCtrlAltDel?.();
      else if (macro === 'taskmgr') {
        remoteRfb?.sendKey?.(0xffe3, 'ControlLeft', true);
        remoteRfb?.sendKey?.(0xffe1, 'ShiftLeft', true);
        remoteRfb?.sendKey?.(0xff1b, 'Escape');
        remoteRfb?.sendKey?.(0xffe1, 'ShiftLeft', false);
        remoteRfb?.sendKey?.(0xffe3, 'ControlLeft', false);
      }
      return;
    }
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
    if (!vmId || remote) return;
    const modeParam = consoleMode === 'spice' ? '&mode=spice' : '';
    const url = `/console/${vmId}?vt=${encodeURIComponent(vncTicket)}${modeParam}`;
    window.open(url, '_blank', 'noopener,noreferrer');
  }

  $effect(() => {
    // `!error`: a failed ticket (e.g. node unreachable) must not retry in a
    // tight loop; the Reconnect button retries on demand.
    if (isRunning && !vncTicket && !loading && !error) {
      loadTicket();
    }
  });

  onMount(() => {
    // The first ticket is requested by the $effect above; asking here too
    // sent two vnc-ticket POSTs on every detail open.
    document.addEventListener('fullscreenchange', onFullscreenChange);
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
        {#if remote}
          <Button
            variant="outline"
            size="sm"
            onclick={downloadVv}
            disabled={vvBusy}
            title={t('remote.console.remoteSpice')}
            class="h-7 px-2 text-xs"
            data-testid="remote-vv-link"
          >
            {t('remote.console.downloadVv')}
          </Button>
        {:else}
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
          {#if needsClusterAccess(errorCode)}
            <Button
              variant="outline"
              size="sm"
              onclick={() => navigate('/nodes', { query: { focus: 'access' } })}
            >
              {t('remote.console.openAccess')}
            </Button>
          {/if}
          <Button variant="outline" size="sm" onclick={loadTicket} class="mt-2">
            <Icon name="refresh" size={13} class="mr-1.5" />
            {t('vmDetail.reconnect')}
          </Button>
        </div>
      {:else if vncTicket && remote}
        {#key vncTicket}
          <RemoteVncConsole
            {vmId}
            ticket={vncTicket}
            {node}
            {hv}
            name={vm?.name || ''}
            bind:rfbOut={remoteRfb}
            onReticket={loadTicket}
          />
        {/key}
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
