// imageJobs.svelte.js — background poller for Image Hub download jobs.
//
// The Hub starts async backend jobs (container image pull, base cloud disk
// download, ISO download). This module registers each one in the global
// task drawer and polls /storage/jobs/{id} until it reaches a terminal
// state, so progress keeps updating even if the user navigates away.
import { SvelteMap, SvelteSet } from 'svelte/reactivity';
import { api } from './auth.svelte.js';
import { upsertTask, updateTask, finishTask } from './tasks.svelte.js';
import { t } from '../i18n.svelte.js';
import { formatRate, formatBytes, formatETA } from '$lib/utils/format.js';

/** @type {SvelteMap<string, ReturnType<typeof setInterval>>} */
const timers = new SvelteMap();
/** @type {SvelteSet<() => void>} */
const doneListeners = new SvelteSet();

// Register a callback fired whenever any tracked job completes successfully
// (the Hub uses it to refresh its lists). Returns an unsubscribe function.
export function onImageJobDone(fn) {
  doneListeners.add(fn);
  return () => doneListeners.delete(fn);
}

function stopTimer(taskId) {
  const timer = timers.get(taskId);
  if (timer) clearInterval(timer);
  timers.delete(taskId);
}

// Track a backend image download job in the task drawer and poll its
// progress. `taskId` is stable so re-triggering the same download replaces
// the previous entry instead of stacking duplicates.
export function trackImageJob({ jobId, taskId, title, kind = 'download' }) {
  upsertTask({
    id: taskId,
    kind,
    title,
    pct: 0,
    message: t('imagehub.taskQueued'),
    status: 'running',
  });
  stopTimer(taskId);
  // The store is module-level, so a poller that never sees a terminal
  // status lives for the whole browser session. The catch below used to
  // be empty ("keep the timer alive"), so a job whose record was lost
  // (backend restarted, job swept, 404 forever) produced 1 req/s to the
  // API indefinitely — invisible to the user but never ending. Ten
  // consecutive failures is far beyond any transient blip.
  let consecutiveFailures = 0;
  const timer = setInterval(async () => {
    try {
      const job = await api.getDownloadJob(jobId);
      consecutiveFailures = 0;
      if (!job) return;
      const pct = Math.min(100, Math.max(0, Math.round(job.progress || 0)));
      if (job.status === 'completed') {
        stopTimer(taskId);
        finishTask(taskId, 'success', t('imagehub.taskComplete'), 100);
        for (const fn of doneListeners) fn();
      } else if (job.status === 'error') {
        stopTimer(taskId);
        finishTask(taskId, 'error', job.error || t('imagehub.taskFailed'), pct);
      } else if (job.status === 'queued') {
        updateTask(taskId, { message: t('imagehub.taskQueued') });
      } else {
        let msg = t('imagehub.taskRunning', { pct });
        if (job.speed_bps > 0 || job.bytes_done > 0) {
          const parts = [];
          if (job.speed_bps > 0) {
            parts.push(formatRate(job.speed_bps));
          }
          if (job.bytes_done > 0 && job.bytes_total > 0) {
            parts.push(`${formatBytes(job.bytes_done)} / ${formatBytes(job.bytes_total)}`);
          } else if (job.bytes_done > 0) {
            parts.push(formatBytes(job.bytes_done));
          }
          if (job.eta_seconds > 0) {
            parts.push(`ETA ${formatETA(job.eta_seconds)}`);
          }
          if (parts.length > 0) {
            msg = `${parts.join(' · ')} (${pct}%)`;
          }
        } else if (job.message) {
          // Steps with no byte counter of their own (decompressing,
          // converting, finalizing) only have their narration to
          // show, and without it the task appears stuck at one
          // percentage for minutes.
          msg = `${job.message} (${pct}%)`;
        }
        updateTask(taskId, {
          pct,
          message: msg,
          speed_bps: job.speed_bps,
          bytes_done: job.bytes_done,
          bytes_total: job.bytes_total,
          eta_seconds: job.eta_seconds,
        });
      }
    } catch {
      consecutiveFailures++;
      if (consecutiveFailures >= 10) {
        stopTimer(taskId);
        finishTask(taskId, 'error', t('imagehub.taskFailed'), 0);
        for (const fn of doneListeners) fn();
      }
    }
  }, 1000);
  timers.set(taskId, timer);
}
