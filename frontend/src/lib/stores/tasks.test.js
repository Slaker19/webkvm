import { describe, it, expect, beforeEach } from 'vitest';
import {
  tasks,
  taskDrawer,
  getActiveCount,
  upsertTask,
  finishTask,
  clearFinished,
  addLog,
  toggleTaskDrawer,
  setTaskDrawerHeight,
} from './tasks.svelte.js';

describe('tasks store (node env)', () => {
  beforeEach(() => {
    tasks.splice(0, tasks.length);
    taskDrawer.open = false;
    taskDrawer.selectedId = null;
    taskDrawer.filter = 'all';
  });

  it('upserts and tracks active tasks', () => {
    expect(getActiveCount()).toBe(0);
    upsertTask({
      id: 'job:1',
      title: 'Backup VM 1',
      pct: 10,
      message: 'Starting backup...',
    });
    expect(tasks.length).toBe(1);
    expect(getActiveCount()).toBe(1);
    expect(tasks[0].status).toBe('running');
    expect(tasks[0].pct).toBe(10);
    expect(tasks[0].logs.length).toBe(1);
  });

  it('finishes task and updates status', () => {
    upsertTask({
      id: 'job:1',
      title: 'Backup VM 1',
      pct: 50,
      message: 'In progress',
    });
    finishTask('job:1', 'success', 'Backup completed successfully', 100);
    expect(tasks[0].status).toBe('success');
    expect(tasks[0].pct).toBe(100);
    expect(tasks[0].message).toBe('Backup completed successfully');
    expect(getActiveCount()).toBe(0);
  });

  it('buffers logs with timestamp', () => {
    upsertTask({ id: 'job:2', title: 'Task 2' });
    addLog('job:2', 'Step 1 complete');
    addLog('job:2', 'Step 2 complete');
    expect(tasks[0].logs.length).toBe(2);
    expect(tasks[0].logs[0]).toContain('Step 1 complete');
    expect(tasks[0].logs[1]).toContain('Step 2 complete');
  });

  it('clears only finished tasks', () => {
    upsertTask({ id: 'job:1', title: 'Task 1' });
    upsertTask({ id: 'job:2', title: 'Task 2' });
    finishTask('job:1', 'success', 'Done', 100);
    expect(tasks.length).toBe(2);
    clearFinished();
    expect(tasks.length).toBe(1);
    expect(tasks[0].id).toBe('job:2');
  });

  it('manages task drawer state and clamps height', () => {
    expect(taskDrawer.open).toBe(false);
    toggleTaskDrawer();
    expect(taskDrawer.open).toBe(true);
    setTaskDrawerHeight(50); // below min 160
    expect(taskDrawer.height).toBe(160);
    setTaskDrawerHeight(1000); // above max 600
    expect(taskDrawer.height).toBe(600);
    setTaskDrawerHeight(320);
    expect(taskDrawer.height).toBe(320);
  });
});
