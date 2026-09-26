<script>
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import {
    Maximize2,
    Minimize2,
    RotateCw,
    Loader2,
    ZoomIn,
    ZoomOut,
    Palette,
    Copy,
    Clipboard,
    SlidersHorizontal,
  } from '@lucide/svelte';
  import { Terminal } from '@xterm/xterm';
  import { FitAddon } from '@xterm/addon-fit';
  import { WebglAddon } from '@xterm/addon-webgl';
  import { Unicode11Addon } from '@xterm/addon-unicode11';
  import { WebLinksAddon } from '@xterm/addon-web-links';
  import { ClipboardAddon } from '@xterm/addon-clipboard';
  import '@xterm/xterm/css/xterm.css';
  import { TERMINAL_THEMES, TUI_KEYS } from '$lib/utils/terminalConfig.js';
  import {
    terminalSettings,
    setTerminalTheme,
    setTerminalFontSize,
    setTerminalTuiBar,
    setTerminalFnKeys,
  } from '$lib/stores/terminalSettings.svelte.js';
  import { t } from '$lib/i18n.svelte.js';

  /**
   * High-performance, full-featured embedded terminal.
   * Supports both Host Root Terminal and VM/Container Serial Consoles.
   *
   * mode: 'vm'   -> connects to /api/vms/{vmId}/serial
   * mode: 'host' -> connects to /api/host/terminal (admin)
   */
  let { mode = 'vm', vmId = null, height = '480px', fullPage = false } = $props();

  let container = $state(null);
  let status = $state('idle'); // idle | connecting | connected | closed | error
  let fullscreen = $state(false);
  let autoRetry = $state(true);
  let showThemeMenu = $state(false);

  // Synced reactive preferences from central store
  const currentThemeId = $derived(terminalSettings.theme);
  const fontSize = $derived(terminalSettings.fontSize);
  const showTuiBar = $derived(terminalSettings.showTuiBar);
  const showFnKeys = $derived(terminalSettings.showFnKeys);
  let termCols = $state(120);
  let termRows = $state(30);

  let term = null;
  let fitAddon = null;
  let webglAddon = null;
  let ws = null;
  let ro = null;
  let showedDisconnect = false;
  let openedThisAttempt = false;
  let failedAttempts = 0;
  let timers = [];

  function later(fn, ms) {
    const id = setTimeout(() => {
      timers = timers.filter((t) => t !== id);
      fn();
    }, ms);
    timers.push(id);
    return id;
  }

  function clearAllTimers() {
    for (const id of timers) clearTimeout(id);
    timers = [];
  }

  const VM_COLS = 80;
  const VM_ROWS = 24;

  function wsBase() {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${proto}//${window.location.host}`;
  }

  function appendLine(msg) {
    if (term) term.write(`\r\n\x1b[90m${msg}\x1b[0m\r\n`);
  }

  function sendData(data) {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(data);
    }
  }

  function sendKey(keyName) {
    const seq = TUI_KEYS[keyName];
    if (seq) {
      sendData(seq);
      if (term) term.focus();
    }
  }

  async function copySelection() {
    if (!term) return;
    const sel = term.getSelection();
    if (sel && navigator.clipboard) {
      try {
        await navigator.clipboard.writeText(sel);
      } catch (_e) {
        // clipboard write error ignored
      }
    }
  }

  async function pasteClipboard() {
    if (navigator.clipboard && navigator.clipboard.readText) {
      try {
        const text = await navigator.clipboard.readText();
        if (text) {
          sendData(text);
          if (term) term.focus();
        }
      } catch (_e) {
        // clipboard read error ignored
      }
    }
  }

  function changeFontSize(delta) {
    setTerminalFontSize(terminalSettings.fontSize + delta);
    if (term) {
      term.options.fontSize = terminalSettings.fontSize;
      fit(sendResize);
    }
  }

  function setTheme(themeId) {
    if (TERMINAL_THEMES[themeId]) {
      setTerminalTheme(themeId);
      if (term) {
        term.options.theme = TERMINAL_THEMES[themeId];
      }
    }
    showThemeMenu = false;
  }

  $effect(() => {
    if (term && currentThemeId) {
      term.options.theme = TERMINAL_THEMES[currentThemeId] || TERMINAL_THEMES.webkvm;
    }
  });

  $effect(() => {
    if (term && fontSize && term.options.fontSize !== fontSize) {
      term.options.fontSize = fontSize;
      fit(sendResize);
    }
  });

  async function connect() {
    if (status === 'connecting') return;
    status = 'connecting';
    try {
      initTerm();
      let url;
      if (mode === 'host') {
        const r = await api.getHostTerminalTicket();
        const initialCols = term ? term.cols : 120;
        const initialRows = term ? term.rows : 30;
        url = `${wsBase()}/api/host/terminal?ticket=${encodeURIComponent(r.ticket)}&cols=${initialCols}&rows=${initialRows}`;
      } else {
        const r = await api.getConsoleTicket(vmId);
        url = `${wsBase()}/api/vms/${encodeURIComponent(vmId)}/serial?ticket=${encodeURIComponent(r.ticket)}`;
      }
      // Reset BEFORE the attempt, not after. The old code assigned
      // openedThisAttempt = true here — i.e. immediately before
      // constructing the WebSocket — and only ever cleared it when a
      // socket eventually opened, so `if (!openedThisAttempt)` in
      // onclose was dead code after the first try. failedAttempts
      // therefore never incremented, the "[Could not open console]"
      // give-up was unreachable, and the backoff stayed frozen at
      // 2^0: a stopped VM or an unreachable host produced an endless
      // 2-second reconnect loop hitting the ticket endpoint forever,
      // with no message explaining why.
      openedThisAttempt = false;
      ws = new WebSocket(url);
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => {
        openedThisAttempt = true;
        status = 'connected';
        showedDisconnect = false;
        failedAttempts = 0;
        if (term) term.focus();
        fit(sendResize);
      };
      ws.onmessage = (e) => {
        if (term) {
          if (typeof e.data === 'string') {
            term.write(e.data);
          } else if (e.data instanceof ArrayBuffer) {
            term.write(new Uint8Array(e.data));
          } else if (e.data) {
            term.write(e.data);
          }
        }
      };
      ws.onclose = (event) => {
        if (event.code === 4409) {
          status = 'closed';
          initTerm();
          appendLine('[Console opened in another tab/window — click Restart to reclaim]');
          return;
        }
        if (!openedThisAttempt) {
          failedAttempts++;
          if (failedAttempts >= 2) {
            status = 'error';
            initTerm();
            appendLine(
              '[Could not open console: is the host or VM available? Click Restart to retry]'
            );
            return;
          }
        }
        if (status !== 'error') status = 'closed';
        if (!showedDisconnect) {
          appendLine('\r\n[Disconnected — reconnecting...]');
          showedDisconnect = true;
        }
        if (autoRetry && status !== 'error') {
          const wait = Math.min(2000 * Math.pow(2, failedAttempts), 10000);
          later(() => connect(), wait);
        }
      };
      ws.onerror = () => {};
    } catch (e) {
      status = 'error';
      initTerm();
      appendLine('[Error obtaining ticket: ' + e.message + ']');
    }
  }

  function initTerm() {
    if (term || !container) return;

    const activeTheme = TERMINAL_THEMES[currentThemeId] || TERMINAL_THEMES.webkvm;

    term = new Terminal({
      cursorBlink: true,
      cursorStyle: 'block',
      fontSize: fontSize,
      fontFamily:
        'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace',
      lineHeight: 1.15,
      letterSpacing: 0,
      scrollback: 10000,
      smoothScrollDuration: 100,
      allowProposedApi: true,
      theme: activeTheme,
      ...(mode === 'vm' ? { cols: VM_COLS, rows: VM_ROWS } : {}),
    });

    // 1. Unicode 11 addon for precise CJK, emoji, and modern TUI box-drawing
    try {
      const unicode11Addon = new Unicode11Addon();
      term.loadAddon(unicode11Addon);
      term.unicode.activeVersion = '11';
    } catch (_e) {
      // unicode addon fallback
    }

    // 2. Web Links addon for clickable URLs
    try {
      term.loadAddon(new WebLinksAddon());
    } catch (_e) {
      // weblinks fallback
    }

    // 3. Clipboard addon
    try {
      term.loadAddon(new ClipboardAddon());
    } catch (_e) {
      // clipboard fallback
    }

    // 4. Fit addon for dynamic sizing
    fitAddon = new FitAddon();
    term.loadAddon(fitAddon);

    // Open terminal inside container
    term.open(container);

    // 5. Hardware acceleration: WebGL with DOM fallback
    try {
      webglAddon = new WebglAddon();
      webglAddon.onContextLoss(() => {
        webglAddon?.dispose();
        webglAddon = null;
      });
      term.loadAddon(webglAddon);
    } catch (_e) {
      // fallback to standard DOM renderer
    }

    // Custom Key Event Handler: Protect TUI function keys from browser interception
    term.attachCustomKeyEventHandler((e) => {
      // Copy: Ctrl+Shift+C or Cmd+C when text is selected
      if (
        (e.ctrlKey && e.shiftKey && e.code === 'KeyC') ||
        (e.metaKey && e.code === 'KeyC' && term.hasSelection())
      ) {
        if (e.type === 'keydown') copySelection();
        return false;
      }
      // Paste: Ctrl+Shift+V
      if (e.ctrlKey && e.shiftKey && e.code === 'KeyV') {
        if (e.type === 'keydown') pasteClipboard();
        return false;
      }
      // F1-F12: Let xterm handle them and prevent browser help/reload/inspector
      if (
        ['F1', 'F2', 'F3', 'F4', 'F5', 'F6', 'F7', 'F8', 'F9', 'F10', 'F11', 'F12'].includes(e.code)
      ) {
        if (e.code === 'F11') {
          // Allow F11 fullscreen toggle if desired
          if (e.type === 'keydown') toggleFullscreen();
          return false;
        }
        return true;
      }
      return true;
    });

    term.onData((data) => {
      sendData(data);
    });

    term.onBinary((data) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(data);
      }
    });

    if (mode === 'vm') {
      ro = new ResizeObserver(() => {
        if (!container || !term) return;
        try {
          term.refresh(0, term.rows - 1);
        } catch {
          /* ignore refresh failure on detached container */
        }
      });
      ro.observe(container);
    } else {
      fit(sendResize);
      ro = new ResizeObserver(() => {
        fit(sendResize);
      });
      ro.observe(container);
    }
  }

  function fit(onDone) {
    if (mode === 'vm') return;
    if (!fitAddon || !container || !term) return;
    const w = container.clientWidth;
    const h = container.clientHeight;
    if (w <= 0 || h <= 0) return;
    requestAnimationFrame(() => {
      try {
        fitAddon.fit();
      } catch {
        /* ignore fit failure during layout transition */
      }
      onDone?.();
    });
  }

  function sendResize() {
    if (mode === 'host' && ws && ws.readyState === WebSocket.OPEN && term) {
      termCols = term.cols;
      termRows = term.rows;
      ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
    }
  }

  function toggleFullscreen() {
    fullscreen = !fullscreen;
    later(() => fit(sendResize), 50);
    later(() => fit(sendResize), 150);
    later(() => fit(sendResize), 300);
  }

  function closeWs(socket) {
    if (!socket) return;
    socket.onopen = null;
    socket.onmessage = null;
    socket.onclose = null;
    socket.onerror = null;
    try {
      socket.close();
    } catch {
      /* ignore close errors on already dead socket */
    }
  }

  function restartConsole() {
    autoRetry = true;
    showedDisconnect = false;
    failedAttempts = 0;
    closeWs(ws);
    ws = null;
    if (term) term.reset();
    status = 'idle';
    later(connect, 200);
  }

  function disconnect() {
    autoRetry = false;
    closeWs(ws);
    ws = null;
  }

  $effect(() => {
    if (status === 'connected') sendResize();
  });

  onMount(() => {
    connect();
  });

  onDestroy(() => {
    clearAllTimers();
    disconnect();
    if (ro) ro.disconnect();
    if (webglAddon) {
      try {
        webglAddon.dispose();
      } catch {
        /* ignore dispose error */
      }
      webglAddon = null;
    }
    if (term) {
      try {
        term.dispose();
      } catch {
        /* ignore dispose error */
      }
      term = null;
    }
  });
</script>

<div
  class={fullscreen
    ? 'fixed inset-0 z-50 bg-background flex flex-col p-2'
    : 'rounded-xl border border-border overflow-hidden bg-card shadow-sm flex flex-col'}
>
  <!-- Top Control Bar -->
  <div
    class="flex flex-wrap items-center justify-between px-3 py-1.5 bg-muted/70 border-b border-border text-xs gap-2 shrink-0 select-none"
  >
    <div class="flex items-center gap-2 text-muted-foreground font-medium">
      <span
        class="inline-block w-2.5 h-2.5 rounded-full transition-colors {status === 'connected'
          ? 'bg-success shadow-sm shadow-success/50'
          : status === 'connecting'
            ? 'bg-warning animate-pulse'
            : 'bg-destructive'}"
      ></span>
      <span class="text-foreground font-semibold">
        {#if mode === 'host'}
          {t('terminal.hostTitle')}
        {:else}
          {t('terminal.serialTitle', { cols: VM_COLS, rows: VM_ROWS })}
        {/if}
      </span>
      {#if status === 'connecting'}
        <Loader2 class="w-3.5 h-3.5 animate-spin text-primary" />
      {/if}
      {#if mode === 'host' && status === 'connected'}
        <span class="hidden sm:inline text-[11px] text-muted-foreground/80 font-mono">
          [{termCols}×{termRows}]
        </span>
      {/if}
    </div>

    <!-- Actions & Toolbar Controls -->
    <div class="flex items-center gap-1">
      <!-- Font Size Controls -->
      <div
        class="flex items-center bg-background/80 rounded-md border border-border/80 px-1 py-0.5"
      >
        <button
          type="button"
          onclick={() => changeFontSize(-1)}
          class="p-1 text-muted-foreground hover:text-foreground rounded hover:bg-muted/80 transition-colors"
          title={t('terminal.zoomOut')}
          aria-label={t('terminal.zoomOut')}
        >
          <ZoomOut class="w-3.5 h-3.5" />
        </button>
        <span class="px-1.5 text-[11px] font-mono text-muted-foreground">{fontSize}px</span>
        <button
          type="button"
          onclick={() => changeFontSize(1)}
          class="p-1 text-muted-foreground hover:text-foreground rounded hover:bg-muted/80 transition-colors"
          title={t('terminal.zoomIn')}
          aria-label={t('terminal.zoomIn')}
        >
          <ZoomIn class="w-3.5 h-3.5" />
        </button>
      </div>

      <!-- Theme Selector Dropdown -->
      <div class="relative">
        <button
          type="button"
          onclick={() => (showThemeMenu = !showThemeMenu)}
          class="p-1.5 text-muted-foreground hover:text-foreground rounded-md hover:bg-muted/80 border border-transparent hover:border-border/60 transition-colors"
          title={t('terminal.colorTheme')}
          aria-label={t('terminal.colorTheme')}
        >
          <Palette class="w-3.5 h-3.5" />
        </button>
        {#if showThemeMenu}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div class="fixed inset-0 z-40" onclick={() => (showThemeMenu = false)}></div>
          <div
            class="absolute right-0 top-full mt-1.5 z-50 w-44 rounded-lg border border-border bg-popover p-1 shadow-lg text-popover-foreground text-xs"
          >
            {#each Object.values(TERMINAL_THEMES) as th (th.id)}
              <button
                type="button"
                onclick={() => setTheme(th.id)}
                class="w-full flex items-center justify-between px-2 py-1.5 rounded-md hover:bg-muted transition-colors text-left {currentThemeId ===
                th.id
                  ? 'bg-muted/80 font-semibold text-primary'
                  : ''}"
              >
                <span>{th.name}</span>
                <span
                  class="w-3 h-3 rounded-full border border-border/80 shrink-0"
                  style="background-color: {th.background}; border-color: {th.cursor};"
                ></span>
              </button>
            {/each}
          </div>
        {/if}
      </div>

      <!-- Toggle TUI Shortcut Keys Bar -->
      <button
        type="button"
        onclick={() => setTerminalTuiBar(!showTuiBar)}
        class="p-1.5 text-muted-foreground hover:text-foreground rounded-md hover:bg-muted/80 border border-transparent hover:border-border/60 transition-colors {showTuiBar
          ? 'text-primary bg-muted/60'
          : ''}"
        title={t('terminal.toggleTuiBar')}
        aria-label={t('terminal.toggleTuiBar')}
      >
        <SlidersHorizontal class="w-3.5 h-3.5" />
      </button>

      <!-- Copy Selection -->
      <button
        type="button"
        onclick={copySelection}
        class="p-1.5 text-muted-foreground hover:text-foreground rounded-md hover:bg-muted/80 border border-transparent hover:border-border/60 transition-colors"
        title={t('terminal.copySelected')}
        aria-label={t('terminal.copySelected')}
      >
        <Copy class="w-3.5 h-3.5" />
      </button>

      <!-- Paste Clipboard -->
      <button
        type="button"
        onclick={pasteClipboard}
        class="p-1.5 text-muted-foreground hover:text-foreground rounded-md hover:bg-muted/80 border border-transparent hover:border-border/60 transition-colors"
        title={t('terminal.pasteClipboard')}
        aria-label={t('terminal.pasteClipboard')}
      >
        <Clipboard class="w-3.5 h-3.5" />
      </button>

      <!-- Restart / Reconnect -->
      <button
        type="button"
        onclick={restartConsole}
        class="p-1.5 text-muted-foreground hover:text-foreground rounded-md hover:bg-muted/80 border border-transparent hover:border-border/60 transition-colors"
        title={t('terminal.restartConsole')}
        aria-label={t('terminal.restartConsole')}
      >
        <RotateCw class="w-3.5 h-3.5" />
      </button>

      <!-- Fullscreen Toggle -->
      <button
        type="button"
        onclick={toggleFullscreen}
        class="p-1.5 text-muted-foreground hover:text-foreground rounded-md hover:bg-muted/80 border border-transparent hover:border-border/60 transition-colors"
        title={t('terminal.fullscreen')}
        aria-label={t('terminal.fullscreen')}
      >
        {#if fullscreen}
          <Minimize2 class="w-3.5 h-3.5" />
        {:else}
          <Maximize2 class="w-3.5 h-3.5" />
        {/if}
      </button>
    </div>
  </div>

  <!-- TUI Helper Quick Keys Toolbar -->
  {#if showTuiBar}
    <div
      class="flex flex-wrap items-center justify-between px-3 py-1 bg-muted/40 border-b border-border/80 gap-1 text-[11px] font-mono select-none"
    >
      <div class="flex flex-wrap items-center gap-1">
        <button
          type="button"
          onclick={() => sendKey('ESC')}
          class="px-2 py-0.5 rounded bg-background border border-border hover:bg-muted hover:border-foreground/30 text-foreground transition-colors font-semibold shadow-2xs"
          title="Escape"
        >
          ESC
        </button>
        <button
          type="button"
          onclick={() => sendKey('TAB')}
          class="px-2 py-0.5 rounded bg-background border border-border hover:bg-muted hover:border-foreground/30 text-foreground transition-colors font-semibold shadow-2xs"
          title="Tabulador"
        >
          TAB
        </button>
        <button
          type="button"
          onclick={() => sendKey('CTRL_C')}
          class="px-2 py-0.5 rounded bg-background border border-border hover:bg-destructive/10 hover:text-destructive hover:border-destructive/40 text-foreground transition-colors font-semibold shadow-2xs"
          title="Enviar Ctrl+C (Interrumpir proceso)"
        >
          ^C
        </button>
        <button
          type="button"
          onclick={() => sendKey('CTRL_Z')}
          class="px-2 py-0.5 rounded bg-background border border-border hover:bg-muted hover:border-foreground/30 text-foreground transition-colors font-semibold shadow-2xs"
          title="Enviar Ctrl+Z (Suspender proceso)"
        >
          ^Z
        </button>
        <button
          type="button"
          onclick={() => sendKey('CTRL_D')}
          class="px-2 py-0.5 rounded bg-background border border-border hover:bg-muted hover:border-foreground/30 text-foreground transition-colors font-semibold shadow-2xs"
          title="Enviar Ctrl+D (EOF / Salir)"
        >
          ^D
        </button>
        <button
          type="button"
          onclick={() => sendKey('CTRL_L')}
          class="px-2 py-0.5 rounded bg-background border border-border hover:bg-muted hover:border-foreground/30 text-foreground transition-colors font-semibold shadow-2xs"
          title="Enviar Ctrl+L (Limpiar pantalla)"
        >
          ^L (Clear)
        </button>

        <span class="text-border mx-0.5">|</span>

        <button
          type="button"
          onclick={() => sendKey('ARROW_UP')}
          class="px-1.5 py-0.5 rounded bg-background border border-border hover:bg-muted text-foreground transition-colors shadow-2xs"
          title={t('terminal.arrowUp')}
        >
          ↑
        </button>
        <button
          type="button"
          onclick={() => sendKey('ARROW_DOWN')}
          class="px-1.5 py-0.5 rounded bg-background border border-border hover:bg-muted text-foreground transition-colors shadow-2xs"
          title={t('terminal.arrowDown')}
        >
          ↓
        </button>
        <button
          type="button"
          onclick={() => sendKey('ARROW_LEFT')}
          class="px-1.5 py-0.5 rounded bg-background border border-border hover:bg-muted text-foreground transition-colors shadow-2xs"
          title={t('terminal.arrowLeft')}
        >
          ←
        </button>
        <button
          type="button"
          onclick={() => sendKey('ARROW_RIGHT')}
          class="px-1.5 py-0.5 rounded bg-background border border-border hover:bg-muted text-foreground transition-colors shadow-2xs"
          title={t('terminal.arrowRight')}
        >
          →
        </button>
      </div>

      <div class="flex items-center gap-1">
        <button
          type="button"
          onclick={() => setTerminalFnKeys(!showFnKeys)}
          class="px-2 py-0.5 rounded bg-background/80 border border-border hover:bg-muted text-muted-foreground hover:text-foreground transition-colors text-[11px] {showFnKeys
            ? 'text-primary border-primary/50 font-semibold'
            : ''}"
        >
          {showFnKeys ? t('terminal.hideFnKeys') : t('terminal.showFnKeys')}
        </button>
      </div>
    </div>
  {/if}

  <!-- Extended Function Keys Toolbar (F1 to F12) -->
  {#if showTuiBar && showFnKeys}
    <div
      class="flex flex-wrap items-center gap-1 px-3 py-1 bg-muted/20 border-b border-border/80 text-[10px] font-mono select-none"
    >
      {#each [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12] as n (n)}
        <button
          type="button"
          onclick={() => sendKey(`F${n}`)}
          class="px-2 py-0.5 rounded bg-background border border-border hover:bg-primary/10 hover:border-primary/40 hover:text-primary text-foreground transition-colors font-semibold shadow-2xs"
          title={t('terminal.sendFnKey', { key: `F${n}` })}
        >
          F{n}
        </button>
      {/each}
    </div>
  {/if}

  <!-- Terminal Screen Container -->
  <div
    bind:this={container}
    class="flex-1 min-h-0 w-full p-0 bg-black overflow-hidden"
    style="height: {fullscreen
      ? 'auto'
      : fullPage
        ? 'calc(100vh - 180px)'
        : height}; min-height: 380px;"
  ></div>
</div>

<style>
  :global(.xterm) {
    height: 100%;
    padding: 0;
  }
  :global(.xterm .xterm-viewport) {
    background-color: transparent !important;
  }
  :global(.xterm .xterm-screen) {
    padding: 0 !important;
  }
</style>
