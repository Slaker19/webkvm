// tasks.svelte.js — global task/notification center.
//
// Every long-running operation (backup, restore, ISO upload/download,
// VM export/import) registers a task here. The TaskCenter component
// renders them in a collapsible panel: running tasks show a live
// progress bar, finished tasks stay as notifications until dismissed.
// Tasks can be minimised (sent to the background panel) and brought
// back to the foreground (opened as a focused popup with a big bar).

/** @type {Array<{id:string, kind:string, title:string, pct:number, status:string, message:string, minimized:boolean}>} */
let tasks = $state([]);
// unread is an object (not a primitive) because Svelte 5 forbids
// re-exporting a reassigned $state primitive from a module.
let unread = $state({ value: 0 });

function upsertTask(task) {
  const i = tasks.findIndex((t) => t.id === task.id);
  if (i >= 0) {
    tasks[i] = { ...tasks[i], ...task };
  } else {
    tasks.push({ minimized: false, status: 'running', pct: 0, message: '', ...task });
    unread.value += 1;
  }
}

function updateTask(id, patch) {
  const i = tasks.findIndex((t) => t.id === id);
  if (i >= 0) tasks[i] = { ...tasks[i], ...patch };
}

function removeTask(id) {
  const i = tasks.findIndex((t) => t.id === id);
  if (i >= 0) tasks.splice(i, 1);
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
  tasks[i] = {
    ...tasks[i],
    status,
    message: message || tasks[i].message,
    pct: pct ?? tasks[i].pct ?? (status === 'success' ? 100 : tasks[i].pct),
    minimized: false,
  };
  if (wasRunning) unread.value += 1;
}

function clearRead() {
  unread.value = 0;
}

// Svelte 5 forbids exporting $derived from a module; expose the
// current value via a plain function instead.
function getActiveCount() {
  return tasks.filter((t) => t.status === 'running').length;
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
};
