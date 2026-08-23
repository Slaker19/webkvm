<script>
  /**
   * TaskCenter — global notification center.
   *
   * Every long-running operation (backup, restore, ISO upload/download,
   * VM export/import) surfaces here. A task can run in the *foreground*
   * (focused popup with a big progress bar) or be *minimised* to the
   * background panel while it keeps running and informs you. Finished
   * tasks stay as notifications until dismissed.
   */
  import { onMount } from 'svelte';
  import { Bell, Loader2, CheckCircle2, XCircle, Minimize2, X } from '@lucide/svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import { Button } from '$lib/components/ui/button';
  import { t } from '$lib/i18n.svelte.js';
  import { progressLabel } from '$lib/progress.js';
  import {
    tasks,
    unread,
    getActiveCount,
    upsertTask,
    finishTask,
    minimizeTask,
    removeTask,
    clearRead,
  } from '$lib/stores/tasks.svelte.js';

  let panelOpen = $state(false);
  let focusedId = $state(null);
  let focusedTask = $derived(tasks.find((x) => x.id === focusedId) || null);
  let activeCount = $derived(getActiveCount());

  // Global poller: keeps backup / restore tasks in sync even when the
  // user has navigated away from the Backup page. The Backup page also
  // registers its tasks with a nicer title; upsertTask merges, so the
  // poll never clobbers an existing title.
  onMount(() => {
    const iv = setInterval(async () => {
      try {
        const r = await api.listBackupJobs();
        for (const j of r.jobs || []) {
          const id = 'job:' + j.id;
          if (j.status === 'running') {
            upsertTask({
              id,
              kind: j.vm_id ? 'restore' : 'backup',
              title: j.vm_name || j.target_id || t('taskCenter.backupDefault'),
              pct: j.progress ?? 0,
              stage: j.stage || '',
              stage_vars: j.stage_vars || {},
              message: j.message || '',
              status: 'running',
              target_id: j.target_id || '',
            });
          } else if (j.status === 'success' || j.status === 'error') {
            finishTask(
              id,
              j.status,
              j.destination
                ? t('taskCenter.extractedTo', { dest: j.destination })
                : j.message || j.error || '',
              j.progress ?? (j.status === 'success' ? 100 : 0)
            );
          }
        }
      } catch {
        /* transient; keep polling */
      }
    }, 2000);
    return () => clearInterval(iv);
  });

  function togglePanel() {
    panelOpen = !panelOpen;
    if (panelOpen) clearRead();
  }

  function openTask(id) {
    focusedId = id;
    panelOpen = false;
  }

  function minimizeFocused() {
    minimizeTask(focusedId);
    focusedId = null;
  }

  function closeFocused() {
    focusedId = null;
  }

  function labelOf(task) {
    return progressLabel(task.stage, task.stage_vars, task.message || task.title, t);
  }
</script>

<!-- Bell button + badge -->
<div class="relative">
  <button
    type="button"
    onclick={togglePanel}
    aria-label={t('taskCenter.title')}
    class="relative p-2 text-muted-foreground hover:text-foreground rounded-md hover:bg-muted transition-colors"
  >
    <Bell size={16} />
    {#if activeCount > 0}
      <span
        class="absolute -top-0.5 -right-0.5 flex items-center justify-center min-w-4 h-4 px-1 text-[10px] font-bold rounded-full bg-accent text-background"
        >{activeCount}</span
      >
    {:else if unread.value > 0}
      <span class="absolute top-1 right-1 w-2 h-2 rounded-full bg-accent"></span>
    {/if}
  </button>

  <!-- Background panel -->
  {#if panelOpen}
    <div class="fixed inset-0 z-40" onclick={() => (panelOpen = false)} aria-hidden="true"></div>
    <div
      class="absolute right-0 top-full mt-2 z-50 w-80 max-w-[calc(100vw-2rem)] rounded-xl border border-border bg-popover text-popover-foreground shadow-lg overflow-hidden"
    >
      <div class="flex items-center justify-between px-3 py-2 border-b border-border">
        <span class="text-sm font-medium">{t('taskCenter.title')}</span>
        <span class="text-xs text-muted-foreground">
          {activeCount}
          {t('taskCenter.active')}
        </span>
      </div>
      <div class="max-h-96 overflow-y-auto p-2 space-y-2">
        {#if tasks.length === 0}
          <p class="text-sm text-muted-foreground p-3">{t('taskCenter.empty')}</p>
        {:else}
          {#each tasks as task (task.id)}
            <div
              class="rounded-lg border border-border bg-background p-2.5 space-y-1.5"
              class:opacity-75={task.status === 'success' || task.status === 'error'}
            >
              <div class="flex items-start gap-2">
                {#if task.status === 'running'}
                  <Loader2 class="w-4 h-4 mt-0.5 text-accent animate-spin shrink-0" />
                {:else if task.status === 'success'}
                  <CheckCircle2 class="w-4 h-4 mt-0.5 text-success shrink-0" />
                {:else if task.status === 'error'}
                  <XCircle class="w-4 h-4 mt-0.5 text-destructive shrink-0" />
                {:else}
                  <span class="w-4 h-4 mt-0.5 shrink-0"></span>
                {/if}
                <div class="flex-1 min-w-0">
                  <div class="text-sm font-medium truncate">{task.title}</div>
                  {#if task.status === 'running'}
                    <div class="text-xs text-muted-foreground truncate">{labelOf(task)}</div>
                  {/if}
                  {#if task.status === 'running'}
                    <ProgressBar value={task.pct} showValue size="sm" class="mt-1.5" />
                  {:else}
                    <div
                      class="mt-1 text-xs font-medium {task.status === 'success'
                        ? 'text-success'
                        : 'text-destructive'}"
                    >
                      {task.status === 'success' ? t('taskCenter.done') : t('taskCenter.failed')}
                    </div>
                  {/if}
                </div>
                <div class="flex items-center gap-0.5 shrink-0">
                  <button
                    type="button"
                    class="p-1 rounded text-muted-foreground hover:text-foreground hover:bg-muted"
                    title={t('taskCenter.minimize')}
                    onclick={() => minimizeTask(task.id)}><Minimize2 class="w-3.5 h-3.5" /></button
                  >
                  <button
                    type="button"
                    class="p-1 rounded text-muted-foreground hover:text-foreground hover:bg-muted"
                    title={t('taskCenter.close')}
                    onclick={() => removeTask(task.id)}><X class="w-3.5 h-3.5" /></button
                  >
                </div>
              </div>
              <button
                type="button"
                class="w-full text-xs text-accent hover:underline text-left"
                onclick={() => openTask(task.id)}
              >
                {task.status === 'running' ? t('taskCenter.foreground') : t('taskCenter.details')}
              </button>
            </div>
          {/each}
        {/if}
      </div>
    </div>
  {/if}
</div>

<!-- Focused foreground popup -->
{#if focusedTask}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <div class="absolute inset-0 bg-black/50" onclick={closeFocused} aria-hidden="true"></div>
    <div
      class="relative w-full max-w-md rounded-xl border border-border bg-popover text-popover-foreground p-5 shadow-xl"
    >
      <div class="flex items-center justify-between mb-3">
        <div class="flex items-center gap-2 min-w-0">
          {#if focusedTask.status === 'running'}
            <Loader2 class="w-5 h-5 text-accent animate-spin shrink-0" />
          {:else if focusedTask.status === 'success'}
            <CheckCircle2 class="w-5 h-5 text-success shrink-0" />
          {:else}
            <XCircle class="w-5 h-5 text-destructive shrink-0" />
          {/if}
          <span class="text-base font-semibold truncate">{focusedTask.title}</span>
        </div>
        <button
          type="button"
          class="p-1 rounded text-muted-foreground hover:text-foreground hover:bg-muted"
          onclick={closeFocused}
          aria-label={t('taskCenter.close')}
        >
          <X class="w-4 h-4" />
        </button>
      </div>

      <ProgressBar
        value={focusedTask.status === 'running' ? focusedTask.pct : 100}
        label={focusedTask.status === 'running' ? labelOf(focusedTask) : focusedTask.title}
        showValue
        size="lg"
        variant={focusedTask.status === 'error' ? 'destructive' : 'accent'}
      />

      <div class="mt-3 text-xs text-muted-foreground">
        {focusedTask.status === 'running'
          ? t('taskCenter.runningNote')
          : focusedTask.status === 'success'
            ? t('taskCenter.done')
            : t('taskCenter.failed')}
      </div>

      <div class="mt-4 flex justify-end gap-2">
        {#if focusedTask.status === 'running'}
          <Button variant="outline" onclick={minimizeFocused}>
            <Minimize2 class="w-3.5 h-3.5 mr-1.5" />
            {t('taskCenter.minimize')}
          </Button>
        {/if}
        {#if focusedTask.status !== 'running'}
          <Button variant="outline" onclick={closeFocused}>{t('common.close')}</Button>
        {/if}
      </div>
    </div>
  </div>
{/if}
