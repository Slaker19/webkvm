<script>
  /**
   * TaskDrawer.svelte — Dockable Bottom Task & Background Job Drawer.
   *
   * Replaces floating popups with a dockable workspace panel (Ctrl+J / ⌘J).
   * Features:
   *  - Collapsed status bar with live progress summary and SSE health.
   *  - Resizable drawer height with localStorage persistence.
   *  - Live split view: Task list + Real-time streaming log console.
   *  - Filter by All / Running / Success / Error.
   *  - Full XSS protection (pure text/escaped log lines).
   */
  import { onDestroy, onMount } from 'svelte';
  import {
    Activity,
    CheckCircle2,
    ChevronDown,
    ChevronUp,
    Copy,
    ListFilter,
    Loader2,
    Terminal,
    Trash2,
    X,
    XCircle,
    Check,
  } from '@lucide/svelte';
  import {
    tasks,
    taskDrawer,
    getActiveCount,
    toggleTaskDrawer,
    closeTaskDrawer,
    setTaskDrawerHeight,
    removeTask,
    clearFinished,
  } from '$lib/stores/tasks.svelte.js';
  import { events } from '$lib/stores/events.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { progressLabel } from '$lib/progress.js';
  import EmptyState from '$lib/components/EmptyState.svelte';

  let activeCount = $derived(getActiveCount());
  let runningTask = $derived(tasks.find((t) => t.status === 'running') || null);

  let filteredTasks = $derived.by(() => {
    if (taskDrawer.filter === 'running') return tasks.filter((t) => t.status === 'running');
    if (taskDrawer.filter === 'success') return tasks.filter((t) => t.status === 'success');
    if (taskDrawer.filter === 'error') return tasks.filter((t) => t.status === 'error');
    return tasks;
  });

  let selectedTask = $derived(
    tasks.find((t) => t.id === taskDrawer.selectedId) || filteredTasks[0] || null
  );

  let isDragging = $state(false);
  let startY = 0;
  let startHeight = 0;
  let autoScroll = $state(true);
  let logContainer = $state(null);
  let copied = $state(false);

  function startResize(e) {
    isDragging = true;
    startY = e.clientY;
    startHeight = taskDrawer.height;
    window.addEventListener('mousemove', onResize);
    window.addEventListener('mouseup', stopResize);
  }

  function onResize(e) {
    if (!isDragging) return;
    const delta = startY - e.clientY;
    setTaskDrawerHeight(startHeight + delta);
  }

  function stopResize() {
    isDragging = false;
    window.removeEventListener('mousemove', onResize);
    window.removeEventListener('mouseup', stopResize);
  }

  // Both the drag listeners and the "copied" reset timer could outlive
  // the component: startResize attaches to `window` and only stopResize
  // detached them, so navigating away mid-drag left mousemove/mouseup
  // attached forever, and copyLogs' setTimeout wrote to a destroyed
  // component's state two seconds later.
  let copyResetTimer = null;
  onDestroy(() => {
    window.removeEventListener('mousemove', onResize);
    window.removeEventListener('mouseup', stopResize);
    isDragging = false;
    if (copyResetTimer) clearTimeout(copyResetTimer);
  });

  function selectTask(id) {
    taskDrawer.selectedId = id;
  }

  function copyLogs() {
    if (!selectedTask || !selectedTask.logs?.length) return;
    const text = selectedTask.logs.join('\n');
    navigator.clipboard?.writeText(text).then(() => {
      copied = true;
      if (copyResetTimer) clearTimeout(copyResetTimer);
      copyResetTimer = setTimeout(() => (copied = false), 2000);
    });
  }

  $effect(() => {
    if (autoScroll && logContainer && selectedTask?.logs) {
      logContainer.scrollTop = logContainer.scrollHeight;
    }
  });

  // Global shortcut Ctrl+J / Cmd+J
  onMount(() => {
    function handleKeydown(e) {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'j') {
        e.preventDefault();
        toggleTaskDrawer();
      }
    }
    window.addEventListener('keydown', handleKeydown);
    return () => window.removeEventListener('keydown', handleKeydown);
  });

  function formatDuration(startedAt, endedAt) {
    if (!startedAt) return '';
    const end = endedAt || Date.now();
    const sec = Math.max(0, Math.floor((end - startedAt) / 1000));
    if (sec < 60) return `${sec}s`;
    const min = Math.floor(sec / 60);
    const remSec = sec % 60;
    return `${min}m ${remSec}s`;
  }
</script>

<!-- Docked Bottom Bar -->
<div
  class="shrink-0 border-t border-border bg-card text-card-foreground select-none transition-all duration-150 z-30"
  style="height: {taskDrawer.open ? `${taskDrawer.height}px` : '32px'}"
>
  <!-- Collapsed / Header Bar (32px) -->
  <div
    class="h-8 px-3 flex items-center justify-between gap-3 text-xs bg-card border-b border-border/50"
  >
    <!-- Left: Status pill & active task summary -->
    <div class="flex items-center gap-2.5 min-w-0">
      <button
        type="button"
        onclick={toggleTaskDrawer}
        class="flex items-center gap-1.5 font-medium hover:text-accent transition-colors focus:outline-none"
        aria-label={t('taskDrawer.toggle')}
      >
        {#if activeCount > 0}
          <span class="flex h-2 w-2 relative">
            <span
              class="animate-ping absolute inline-flex h-full w-full rounded-full bg-accent opacity-75"
            ></span>
            <span class="relative inline-flex rounded-full h-2 w-2 bg-accent"></span>
          </span>
          <span class="text-foreground font-semibold"
            >{t('taskDrawer.activeCount', { count: activeCount })}</span
          >
        {:else}
          <Activity size={13} class="text-muted-foreground" />
          <span class="text-muted-foreground">{t('taskDrawer.noActive')}</span>
        {/if}
      </button>

      {#if runningTask}
        <div class="hidden sm:flex items-center gap-2 text-muted-foreground truncate max-w-md">
          <span class="text-border">|</span>
          <span class="truncate font-mono text-[11px] text-foreground">{runningTask.title}</span>
          <div class="w-16 h-1.5 bg-muted rounded-full overflow-hidden shrink-0">
            <div
              class="h-full bg-accent transition-all duration-300 rounded-full"
              style="width: {runningTask.pct}%"
            ></div>
          </div>
          <span class="text-[10px] font-mono shrink-0">{runningTask.pct}%</span>
        </div>
      {/if}
    </div>

    <!-- Center: SSE live indicator (if disconnected/reconnecting) -->
    <div class="hidden md:flex items-center gap-2">
      {#if events.reconnecting}
        <span class="flex items-center gap-1 text-[11px] text-warning">
          <span class="w-1.5 h-1.5 rounded-full bg-warning animate-pulse"></span>
          {t('layout.reconnecting')}
        </span>
      {:else if !events.connected}
        <span class="flex items-center gap-1 text-[11px] text-muted-foreground">
          <span class="w-1.5 h-1.5 rounded-full bg-muted-foreground"></span>
          {t('layout.offline')}
        </span>
      {/if}
    </div>

    <!-- Right: Controls & shortcut badge -->
    <div class="flex items-center gap-2 shrink-0">
      <button
        type="button"
        onclick={toggleTaskDrawer}
        class="flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground transition-colors px-1.5 py-0.5 rounded hover:bg-muted"
        title="{t('taskDrawer.shortcut')} (Ctrl+J / ⌘J)"
      >
        <span class="hidden sm:inline"
          >{taskDrawer.open ? t('taskDrawer.hide') : t('taskDrawer.show')}</span
        >
        <kbd
          class="text-[10px] font-mono px-1 py-0.2 rounded bg-muted text-muted-foreground border border-border"
        >
          ⌘J
        </kbd>
        {#if taskDrawer.open}
          <ChevronDown size={14} />
        {:else}
          <ChevronUp size={14} />
        {/if}
      </button>
    </div>
  </div>

  <!-- Expanded Drawer Content -->
  {#if taskDrawer.open}
    <!-- Top Drag Handle for resizing -->
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="h-1 -mt-1 w-full cursor-ns-resize hover:bg-accent/40 active:bg-accent transition-colors z-40"
      onmousedown={startResize}
      title={t('taskDrawer.resize')}
    ></div>

    <!-- Main Drawer Workspace -->
    <div class="flex flex-col h-[calc(100%-32px)] bg-background text-foreground overflow-hidden">
      <!-- Sub-header with Filter Tabs & Actions -->
      <div
        class="h-9 px-3 border-b border-border flex items-center justify-between gap-2 bg-card/60 shrink-0"
      >
        <div class="flex items-center gap-2 overflow-x-auto min-w-0 py-0.5">
          <span
            class="font-semibold text-xs text-foreground flex items-center gap-1.5 mr-1 shrink-0"
          >
            <Activity size={14} class="text-accent" />
            {t('taskDrawer.title')}
          </span>
          <div class="h-3 w-px bg-border shrink-0"></div>
          <ListFilter size={13} class="text-muted-foreground mr-0.5 shrink-0" />
          {#each [['all', t('taskDrawer.filterAll')], ['running', t('taskDrawer.filterRunning')], ['success', t('taskDrawer.filterSuccess')], ['error', t('taskDrawer.filterError')]] as [key, label], __i (__i)}
            <button
              type="button"
              onclick={() => (taskDrawer.filter = key)}
              class="px-2 py-0.5 rounded text-[11px] font-medium transition-colors shrink-0 {taskDrawer.filter ===
              key
                ? 'bg-accent text-accent-foreground shadow-sm'
                : 'text-muted-foreground hover:text-foreground hover:bg-muted'}"
            >
              {label}
            </button>
          {/each}
        </div>

        <div class="flex items-center gap-2">
          {#if tasks.some((t) => t.status !== 'running')}
            <button
              type="button"
              onclick={clearFinished}
              class="flex items-center gap-1 text-[11px] text-muted-foreground hover:text-destructive transition-colors px-1.5 py-0.5 rounded hover:bg-muted"
            >
              <Trash2 size={12} />
              <span class="hidden sm:inline">{t('taskDrawer.clearFinished')}</span>
            </button>
          {/if}
          <button
            type="button"
            onclick={closeTaskDrawer}
            class="p-1 text-muted-foreground hover:text-foreground rounded hover:bg-muted"
            aria-label={t('common.close')}
          >
            <X size={14} />
          </button>
        </div>
      </div>

      <!-- Split View: Tasks List (Left) + Log Console (Right) -->
      <div class="flex-1 flex overflow-hidden divide-x divide-border">
        <!-- Left: Tasks List -->
        <div
          class="w-full md:w-[45%] lg:w-[40%] overflow-y-auto divide-y divide-border/40 shrink-0"
        >
          {#if filteredTasks.length === 0}
            <EmptyState compact icon="activity" title={t('taskDrawer.noTasks')} />
          {:else}
            {#each filteredTasks as task (task.id)}
              <div
                role="button"
                tabindex="0"
                onclick={() => selectTask(task.id)}
                onkeydown={(e) => e.key === 'Enter' && selectTask(task.id)}
                class="p-2.5 flex items-start gap-2.5 cursor-pointer text-xs transition-colors hover:bg-muted/40 {selectedTask?.id ===
                task.id
                  ? 'bg-accent/10 border-l-2 border-accent'
                  : ''}"
              >
                <!-- Status Icon -->
                <div class="mt-0.5 shrink-0">
                  {#if task.status === 'running'}
                    <Loader2 size={15} class="text-accent animate-spin" />
                  {:else if task.status === 'success'}
                    <CheckCircle2 size={15} class="text-success" />
                  {:else}
                    <XCircle size={15} class="text-destructive" />
                  {/if}
                </div>

                <!-- Task Details -->
                <div class="flex-1 min-w-0">
                  <div class="flex items-center justify-between gap-1 mb-1">
                    <span class="font-medium text-foreground truncate font-mono text-[11px]"
                      >{task.title}</span
                    >
                    <span class="text-[10px] text-muted-foreground font-mono shrink-0">
                      {formatDuration(task.startedAt, task.endedAt)}
                    </span>
                  </div>

                  {#if task.status === 'running'}
                    <div class="w-full bg-muted h-1.5 rounded-full overflow-hidden mb-1">
                      <div
                        class="bg-accent h-full transition-all duration-300 rounded-full"
                        style="width: {task.pct}%"
                      ></div>
                    </div>
                  {/if}

                  <div
                    class="flex items-center justify-between gap-2 text-[10px] text-muted-foreground"
                  >
                    <span class="truncate"
                      >{progressLabel(
                        task.stage,
                        task.stage_vars,
                        task.message || task.title,
                        t
                      )}</span
                    >
                    <span
                      class="shrink-0 font-mono font-medium {task.status === 'success'
                        ? 'text-success'
                        : task.status === 'error'
                          ? 'text-destructive'
                          : 'text-accent'}"
                    >
                      {task.status === 'running' ? `${task.pct}%` : task.status.toUpperCase()}
                    </span>
                  </div>
                </div>

                <!-- Delete / Dismiss Action -->
                {#if task.status !== 'running'}
                  <button
                    type="button"
                    onclick={(e) => {
                      e.stopPropagation();
                      removeTask(task.id);
                    }}
                    class="p-1 text-muted-foreground hover:text-foreground rounded hover:bg-muted shrink-0"
                    title={t('common.dismiss')}
                  >
                    <X size={12} />
                  </button>
                {/if}
              </div>
            {/each}
          {/if}
        </div>

        <!-- Right: Log Console & Telemetry Viewer -->
        <div class="hidden md:flex flex-col flex-1 bg-card/40 overflow-hidden">
          {#if selectedTask}
            <div
              class="h-8 px-3 border-b border-border flex items-center justify-between gap-2 bg-card/80 text-xs shrink-0"
            >
              <div
                class="flex items-center gap-1.5 font-mono text-[11px] truncate min-w-0 text-foreground"
              >
                <Terminal size={13} class="text-muted-foreground shrink-0" />
                <span class="truncate font-semibold">{selectedTask.title}</span>
                <span class="text-muted-foreground"
                  >({selectedTask.logs?.length || 0} {t('taskDrawer.lines')})</span
                >
              </div>
              <div class="flex items-center gap-1.5 shrink-0">
                <button
                  type="button"
                  onclick={() => (autoScroll = !autoScroll)}
                  class="px-2 py-0.5 text-[10px] font-mono rounded border border-border transition-colors {autoScroll
                    ? 'bg-accent/15 text-accent border-accent/40 font-semibold'
                    : 'text-muted-foreground hover:text-foreground'}"
                >
                  {t('taskDrawer.autoScroll')}
                  {autoScroll ? 'ON' : 'OFF'}
                </button>
                <button
                  type="button"
                  onclick={copyLogs}
                  class="flex items-center gap-1 px-2 py-0.5 text-[10px] text-muted-foreground hover:text-foreground rounded border border-border hover:bg-muted transition-colors"
                >
                  {#if copied}
                    <Check size={11} class="text-success" />
                    <span class="text-success">{t('taskDrawer.copied')}</span>
                  {:else}
                    <Copy size={11} />
                    <span>{t('taskDrawer.copyLogs')}</span>
                  {/if}
                </button>
              </div>
            </div>

            <!-- Terminal Output Window -->
            <div
              bind:this={logContainer}
              class="flex-1 p-3 overflow-y-auto font-mono text-[11px] leading-relaxed select-text bg-background/80 text-foreground"
            >
              {#if !selectedTask.logs || selectedTask.logs.length === 0}
                <div class="text-muted-foreground italic">{t('taskDrawer.noLogsYet')}</div>
              {:else}
                {#each selectedTask.logs as line, i (i)}
                  <div class="whitespace-pre-wrap break-all hover:bg-muted/20 py-0.5 px-1 rounded">
                    {line}
                  </div>
                {/each}
              {/if}
            </div>
          {:else}
            <div
              class="flex-1 flex flex-col items-center justify-center text-muted-foreground text-xs p-6 text-center"
            >
              <Terminal size={32} class="mb-2 opacity-30" />
              <span>{t('taskDrawer.selectTaskToView')}</span>
            </div>
          {/if}
        </div>
      </div>
    </div>
  {/if}
</div>
