<script>
  /**
   * RemoteVncConsole — in-page noVNC for a VM that lives on another node.
   * The legacy /console/{id} page cannot carry ?node=, so remote VMs connect
   * the RFB WebSocket straight to /api/vms/{id}/vnc?vt=…&node=…&hv=….
   */
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { Button } from '$lib/components/ui/button';

  // onReticket: ask the parent for a FRESH ticket (tickets are single-use and
  // bound to this node+VM: reconnecting with the old one is a dead end).
  let {
    vmId,
    ticket,
    node,
    hv = '',
    name = '',
    rfbOut = $bindable(null),
    onReticket = null,
  } = $props();

  let el = $state(null);
  let status = $state('connecting'); // connecting | connected | closed | failed
  let rfb = null;
  let gone = false;

  async function connect() {
    if (!el || !ticket) return;
    status = 'connecting';
    try {
      const mod = await import('@novnc/novnc');
      if (gone) return;
      const RFB = mod.default;
      rfb?.disconnect();
      rfb = new RFB(el, api.getVNCWsUrl(vmId, ticket, node, hv));
      rfb.scaleViewport = true;
      rfb.resizeSession = false;
      rfbOut = rfb;
      rfb.addEventListener('connect', () => (status = 'connected'));
      const self = rfb;
      rfb.addEventListener('disconnect', (e) => {
        status = e?.detail?.clean ? 'closed' : 'failed';
        // A disconnected RFB object is final: calling disconnect() on it
        // again (unmount after a forced power-off, reconnect) makes noVNC
        // report "Tried changing state of a disconnected RFB object".
        if (rfb === self) {
          rfb = null;
          if (!gone) rfbOut = null;
        }
      });
    } catch {
      status = 'failed';
    }
  }

  onMount(connect);
  onDestroy(() => {
    gone = true;
    try {
      rfb?.disconnect();
    } catch {
      /* already closed */
    }
    rfbOut = null;
  });
</script>

<div class="relative h-full w-full bg-black">
  <div
    bind:this={el}
    class="h-full w-full"
    role="img"
    aria-label={t('remote.console.screenLabel', { name })}
    data-testid="remote-vnc-screen"
  ></div>
  {#if status !== 'connected'}
    <div
      class="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/70 p-4 text-center text-sm text-white"
      role="status"
    >
      {#if status === 'connecting'}
        <span>{t('remote.console.connecting', { node })}</span>
      {:else}
        <span>{status === 'closed' ? t('remote.console.closed') : t('remote.console.failed')}</span>
        {#if status === 'failed'}
          <span class="text-xs opacity-90" data-testid="remote-vnc-failed-hint">
            {t('remote.console.failedHint')}
          </span>
        {/if}
        <Button
          size="sm"
          variant="secondary"
          data-testid="remote-vnc-reconnect"
          onclick={() => (onReticket ? onReticket() : connect())}
          >{t('remote.console.reconnect')}</Button
        >
      {/if}
    </div>
  {/if}
</div>
