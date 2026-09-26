// tasks.svelte.js — global task/notification center & dockable drawer state.
//
// Every long-running operation (backup, restore, ISO upload/download,
// VM export/import, disk resize) registers a task here.
// Supports both dockable bottom drawer and notifications.
import { SvelteDate } from 'svelte/reactivity';
import { browser } from '$lib/utils/browser.js';

/** @type {Array<{id:string, kind:string, title:string, pct:number, status:string, message:string, minimized:boolean, logs:string[], startedAt:number, endedAt:number|null, target_id?:string, vm_id?:string}>} */
let tasks = $state([]);
// unread is an object (not a primitive) because Svelte 5 forbids
// re-exporting a reassigned $state primitive from a module.
let unread = $state({ value: 0 });

const DRAWER_HEIGHT_KEY = 'webkvm.taskdrawer.height.v1';

function initialDrawerHeight() {
  if (!browser) return 280;
  try {
    const v = parseInt(localStorage.getItem(DRAWER_HEIGHT_KEY) || '280', 10);
    if (!isNaN(v) && v >= 160 && v <= 600) return v;
  } catch {
    /* ignore */
  }
  return 280;
}

export const taskDrawer = $state({
  open: false,
  height: initialDrawerHeight(),
  selectedId: null,
  filter: 'all', // 'all' | 'running' | 'success' | 'error'
});

function upsertTask(task) {
  const now = Date.now();
  const i = tasks.findIndex((t) => t.id === task.id);
  if (i >= 0) {
    const existing = tasks[i];
    const newLogs =
      task.message && task.message !== existing.message
        ? [
            ...(existing.logs || []),
            `[${new SvelteDate().toLocaleTimeString()}] ${task.message}`,
          ].slice(-500)
        : existing.logs || [];

    tasks[i] = {
      ...existing,
      ...task,
      logs: newLogs,
    };
  } else {
    const initialLog = task.message
      ? [`[${new SvelteDate().toLocaleTimeString()}] ${task.message}`]
      : [];
    tasks.unshift({
      minimized: false,
      status: 'running',
      pct: 0,
      message: '',
      startedAt: now,
      endedAt: null,
      logs: initialLog,
      ...task,
    });
    unread.value += 1;
    if (!taskDrawer.selectedId) {
      taskDrawer.selectedId = task.id;
    }
  }
}

function updateTask(id, patch) {
  const i = tasks.findIndex((t) => t.id === id);
  if (i >= 0) tasks[i] = { ...tasks[i], ...patch };
}

function removeTask(id) {
  const i = tasks.findIndex((t) => t.id === id);
  if (i >= 0) {
    tasks.splice(i, 1);
    if (taskDrawer.selectedId === id) {
      taskDrawer.selectedId = tasks[0]?.id || null;
    }
  }
}

function minimizeTask(id) {
  updateTask(id, { minimized: true });
}

function focusTask(id) {
  updateTask(id, { minimized: false });
}

function finishTask(id, status, message, pct) {
  const i = tasks.findIndex((t) => t.id === id);
  if (i < 0) return;
  const wasRunning = tasks[i].status === 'running';
  const existing = tasks[i];
  const finalMsg = message || existing.message;
  const updatedLogs =
    finalMsg &&
    (!existing.logs?.length || !existing.logs[existing.logs.length - 1].endsWith(finalMsg))
      ? [...(existing.logs || []), `[${new SvelteDate().toLocaleTimeString()}] ${finalMsg}`].slice(
          -500
        )
      : existing.logs || [];

  tasks[i] = {
    ...existing,
    status,
    message: finalMsg,
    pct: pct ?? existing.pct ?? (status === 'success' ? 100 : existing.pct),
    minimized: false,
    endedAt: Date.now(),
    logs: updatedLogs,
  };
  if (wasRunning) unread.value += 1;
}

function clearRead() {
  unread.value = 0;
}

function clearFinished() {
  const running = tasks.filter((t) => t.status === 'running');
  tasks.splice(0, tasks.length, ...running);
  if (taskDrawer.selectedId && !running.some((t) => t.id === taskDrawer.selectedId)) {
    taskDrawer.selectedId = running[0]?.id || null;
  }
}

function addLog(id, line) {
  const i = tasks.findIndex((t) => t.id === id);
  if (i < 0) return;
  const formatted = `[${new SvelteDate().toLocaleTimeString()}] ${line}`;
  tasks[i].logs = [...(tasks[i].logs || []), formatted].slice(-500);
}

function getActiveCount() {
  return tasks.filter((t) => t.status === 'running').length;
}

function toggleTaskDrawer() {
  taskDrawer.open = !taskDrawer.open;
  if (taskDrawer.open) {
    clearRead();
    if (!taskDrawer.selectedId && tasks.length > 0) {
      taskDrawer.selectedId = tasks[0].id;
    }
  }
}

function openTaskDrawer(taskId = null) {
  taskDrawer.open = true;
  clearRead();
  if (taskId) {
    taskDrawer.selectedId = taskId;
  } else if (!taskDrawer.selectedId && tasks.length > 0) {
    taskDrawer.selectedId = tasks[0].id;
  }
}

function closeTaskDrawer() {
  taskDrawer.open = false;
}

function setTaskDrawerHeight(h) {
  const clamped = Math.max(160, Math.min(600, h));
  taskDrawer.height = clamped;
  if (browser) {
    try {
      localStorage.setItem(DRAWER_HEIGHT_KEY, clamped.toString());
    } catch {
      /* ignore */
    }
  }
}

export {
  tasks,
  unread,
  getActiveCount,
  upsertTask,
  updateTask,
  removeTask,
  minimizeTask,
  focusTask,
  finishTask,
  clearRead,
  clearFinished,
  addLog,
  toggleTaskDrawer,
  openTaskDrawer,
  closeTaskDrawer,
  setTaskDrawerHeight,
};
