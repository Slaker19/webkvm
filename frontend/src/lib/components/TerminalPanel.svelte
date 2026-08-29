<script>
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { Maximize2, Minimize2, RotateCw, Loader2 } from '@lucide/svelte';

  /**
   * Embedded terminal (VM serial console or host root shell).
   *
   * mode: 'vm'   -> connects to /api/vms/{vmId}/serial
   * mode: 'host' -> connects to /api/host/terminal (admin)
   *
   * Auth: fetches a short-lived single-use ticket with the user's
   * session; the ticket travels in the WS URL and is burned on the
   * first upgrade attempt — no long-lived tokens in URLs.
   */
  let { mode = 'vm', vmId = null } = $props();

  let container = $state(null);
  let status = $state('idle'); // idle | connecting | connected | closed | error
  let fullscreen = $state(false);
  let autoRetry = $state(true);

  let term = null;
  let fitAddon = null;
  let ws = null;
  let ro = null;
  let showedDisconnect = false;
  let openedThisAttempt = false;
  let failedAttempts = 0;

  // Guest serial TTYs are a fixed classic grid; matching it exactly is
  // what keeps TUIs (btop/mc) from garbling.
  const VM_COLS = 80,
    VM_ROWS = 24;

  function wsBase() {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${proto}//${window.location.host}`;
  }

  function appendLine(msg) {
    if (term) term.write(`\r\n\x1b[90m${msg}\x1b[0m\r\n`);
  }

  async function connect() {
    if (status === 'connecting') return;
    status = 'connecting';
    try {
      // Must run before building the URL below — the host-mode branch
      // reads term.cols/term.rows, and term is only created here. It
      // used to run after, so term was still null and every host
      // console connection failed with "can't access property 'cols',
      // term is null".
      initTerm();
      let url;
      if (mode === 'host') {
        const r = await api.getHostTerminalTicket();
        url = `${wsBase()}/api/host/terminal?ticket=${encodeURIComponent(r.ticket)}&cols=${term.cols}&rows=${term.rows}`;
      } else {
        const r = await api.getConsoleTicket(vmId);
        url = `${wsBase()}/api/vms/${encodeURIComponent(vmId)}/serial?ticket=${encodeURIComponent(r.ticket)}`;
      }
      openedThisAttempt = true;
      ws = new WebSocket(url);
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => {
        status = 'connected';
        showedDisconnect = false;
        failedAttempts = 0;
        term.focus();
        fit(sendResize);
      };
      ws.onmessage = (e) => {
        // Server sends text frames for serial; binary-safe anyway.
        term.write(typeof e.data === 'string' ? e.data : new TextDecoder().decode(e.data));
      };
      ws.onclose = (event) => {
        if (event.code === 4409) {
          // Another tab/window opened this same console and took over
          // (server's "newest viewer wins" policy) — stand down instead
          // of auto-reconnecting, or two open tabs would evict each
          // other forever. Only "Restart" reclaims it from here.
          status = 'closed';
          initTerm();
          appendLine('[console opened in another tab/window — click Restart to reclaim it here]');
          return;
        }
        if (!openedThisAttempt) {
          // Server rejected the upgrade before any data flowed — most
          // likely the VM is off. Stop retrying and say so.
          failedAttempts++;
          if (failedAttempts >= 2) {
            status = 'error';
            initTerm();
            appendLine(
              '[could not open console: is the VM running? Press Reconnect once it boots]'
            );
            return;
          }
        }
        if (status !== 'error') status = 'closed';
        // Single disconnect line; silent retries after that.
        if (!showedDisconnect) {
          appendLine('\r\n[disconnected — reconnecting...]');
          showedDisconnect = true;
        }
        // Backoff during guest reboots: quick first tries, then slower,
        // capped so a dead session never spams forever.
        if (autoRetry && status !== 'error') {
          const wait = Math.min(2000 * Math.pow(2, failedAttempts), 10000);
          setTimeout(() => connect(), wait);
        }
      };
      ws.onerror = () => {};
    } catch (e) {
      status = 'error';
      initTerm();
      appendLine('[error obteniendo ticket: ' + e.message + ']');
    }
  }

  function initTerm() {
    if (term) return;
    term = new window.Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: 'monospace',
      theme: { background: '#010101', foreground: '#e2e8f0' },
      ...(mode === 'vm' ? { cols: VM_COLS, rows: VM_ROWS } : {}),
    });
    if (mode === 'vm') {
      // Fixed guest grid: no dynamic fitting (see VM_COLS above).
      term.open(container);
      term.onData((data) => {
        if (ws && ws.readyState === WebSocket.OPEN) ws.send(data);
      });
      // Sync container size with the terminal's natural dimensions so
      // there's no overflow or empty space when the panel resizes.
      ro = new ResizeObserver(() => {
        if (!container) return;
        const w = container.clientWidth;
        const h = container.clientHeight;
        if (w <= 0 || h <= 0) return;
        // Force xterm.js to redraw its canvas into the available space.
        try {
          term.refresh(0, term.rows - 1);
        } catch (_e) {
          /* ignore transient refresh errors */
        }
      });
      ro.observe(container);
      return;
    }
    fitAddon = new window.FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.open(container);
    fit();
    term.onData((data) => {
      if (ws && ws.readyState === WebSocket.OPEN) ws.send(data);
    });
    ro = new ResizeObserver(() => {
      fit(sendResize);
    });
    ro.observe(container);
  }

  // onDone fires *after* fitAddon.fit() actually resizes the terminal.
  // Callers that need the post-fit term.cols/term.rows (sendResize)
  // must pass it in rather than calling it right after fit() returns:
  // fit()'s real work happens inside a requestAnimationFrame callback,
  // so term.cols/term.rows are still the pre-resize values at the
  // point fit() itself returns. Reading them synchronously right after
  // (the previous code) always sent the PTY a stale size — one resize
  // behind, or the library's 80x24 default if nothing had resized yet
  // — which is exactly the kind of size mismatch that makes fullscreen
  // TUIs like btop draw at the wrong dimensions and garble.
  function fit(onDone) {
    // Guard: never fit into a zero-sized container (breaks xterm renderer
    // and leaves giant glyphs after leaving fullscreen).
    if (mode === 'vm') return; // fixed guest grid
    if (!fitAddon || !container) return;
    const w = container.clientWidth;
    const h = container.clientHeight;
    if (w <= 0 || h <= 0) return;
    requestAnimationFrame(() => {
      try {
        fitAddon.fit();
      } catch (_e) {
        // ignore transient fit errors during layout transitions
      }
      onDone?.();
    });
  }

  // Host PTY supports live resize via JSON control messages.
  function sendResize() {
    if (mode === 'host' && ws && ws.readyState === WebSocket.OPEN && term) {
      ws.send(JSON.stringify({ cols: term.cols, rows: term.rows }));
    }
  }

  function toggleFullscreen() {
    fullscreen = !fullscreen;
    // Re-fit at several points while the layout transition settles,
    // otherwise the terminal keeps stale dimensions (huge glyphs) —
    // and tell the PTY about each corrected size once it's actually
    // computed, or the guest keeps drawing at the old geometry.
    setTimeout(() => fit(sendResize), 50);
    setTimeout(() => fit(sendResize), 150);
    setTimeout(() => fit(sendResize), 300);
  }

  // Hard restart of the console: drops the WebSocket, clears any
  // garbled alternate-screen state (e.g. after btop) and reconnects.
  function restartConsole() {
    autoRetry = true;
    showedDisconnect = false;
    failedAttempts = 0;
    if (ws)
      try {
        ws.close();
      } catch (_e) {
        // ignore close error
      }
    ws = null;
    if (term) term.reset();
    status = 'idle';
    setTimeout(connect, 250);
  }

  function disconnect() {
    autoRetry = false;
    if (ws)
      try {
        ws.close();
      } catch (_e) {
        // ignore close error
      }
    ws = null;
  }

  $effect(() => {
    if (status === 'connected') sendResize();
  });

  onMount(() => {
    connect();
  });

  onDestroy(() => {
    disconnect();
    if (ro) ro.disconnect();
    if (term)
      try {
        term.dispose();
      } catch (_e) {
        // ignore dispose error
      }
    term = null;
  });
</script>

<div
  class="{fullscreen
    ? 'fixed inset-0 z-50 bg-background p-2'
    : 'rounded-lg border border-border overflow-hidden'} flex flex-col"
>
  <div
    class="flex items-center justify-between px-2 py-1 bg-muted/60 border-b border-border shrink-0"
  >
    <div class="flex items-center gap-2 text-xs text-muted-foreground">
      <span
        class="inline-block w-2 h-2 rounded-full {status === 'connected'
          ? 'bg-green-500'
          : status === 'connecting'
            ? 'bg-yellow-500 animate-pulse'
            : 'bg-red-500'}"
      ></span>
      {#if mode === 'host'}
        Host system terminal — requires host credentials
      {:else}
        VM serial console · {VM_COLS}×{VM_ROWS}
      {/if}
      {#if status === 'connecting'}
        <Loader2 class="w-3 h-3 animate-spin" />
      {/if}
    </div>
    <div class="flex items-center gap-1">
      <button
        type="button"
        onclick={restartConsole}
        class="p-1.5 text-muted-foreground hover:text-foreground rounded-md hover:bg-muted"
        title="Restart console"
        aria-label="Restart console"
      >
        {#if status === 'closed' || status === 'error' || status === 'idle'}
          <RotateCw class="w-4 h-4" />
        {:else}
          <RotateCw class="w-3.5 h-3.5" />
        {/if}
      </button>
      <span
        class="text-[11px] text-muted-foreground pr-1 hidden sm:inline"
        title="Restarts the connection and clears the screen (useful if btop or another TUI garbles it)"
      >
        Restart
      </span>
      <button
        type="button"
        class="p-1.5 text-muted-foreground hover:text-foreground rounded-md hover:bg-muted"
        onclick={toggleFullscreen}
        title={fullscreen ? 'Exit fullscreen' : 'Fullscreen'}
      >
        {#if fullscreen}<Minimize2 class="w-4 h-4" />{:else}<Maximize2 class="w-4 h-4" />{/if}
      </button>
    </div>
  </div>
  <div
    bind:this={container}
    class="flex-1 min-h-0 p-1"
    style="height: {fullscreen ? 'auto' : '480px'}"
  ></div>
</div>
