<script>
  /**
   * Backup — multi-target / per-VM / schedule / manual cleanup.
   *
   * The Phase 1.7-bis-backup rewrite drops the global retention
   * policy. Backups accumulate on disk; the operator deletes them
   * by hand from the Files tab. Schedules are configured via a
   * visual cron picker instead of a raw 5-field string. Each
   * target also gets a VM selector so you can back up "all VMs",
   * "only these N VMs", or "every VM except these".
   */
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import * as Dialog from '$lib/components/ui/dialog';
  import { Loader2 } from '@lucide/svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import CronPicker from '$lib/components/CronPicker.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import { tasks, upsertTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { progressLabel } from '$lib/progress.js';
  import { t } from '../lib/i18n.svelte.js';

  let targets = $state([]);
  let schedules = $state([]);
  let jobs = $state([]);
  // filesByTarget keeps the per-target backup archive list so the
  // Files tab can render without re-hitting the API on every tab
  // switch. Keyed by target ID; an entry of `null` means "not yet
  // loaded" (we lazy-load on first visit).
  let filesByTarget = $state({});
  // configByTarget holds the stable "latest configuration" snapshot
  // info for each target ({filename, size, modified} | null).
  let configByTarget = $state({});
  let vms = $state([]);
  let loading = $state(true);
  let activeTab = $state('targets');

  // Add target form (also reused for Edit, driven by editingTarget)
  let showAddTarget = $state(false);
  let editingTarget = $state(null); // null = add mode, otherwise the target being edited
  let newTargetName = $state('');
  let newTargetType = $state('local');
  let newTargetPath = $state('');
  let newTargetHost = $state('');
  let newTargetPort = $state(22);
  let newTargetUsername = $state('');
  let newTargetPassword = $state('');
  let newTargetSSHKeyPath = $state('');
  let newTargetVMFilter = $state('all');
  let newTargetVMIDs = $state([]);
  let newTargetEnabled = $state(true);
  // Retention: 0 = keep everything. newTargetRetentionKeepLast / KeepDays
  // are the "Conservar últimas N / N días" values (0 = unlimited).
  let newTargetRetentionKeepLast = $state(0);
  let newTargetRetentionKeepDays = $state(0);
  let testing = $state(false);
  let testResult = $state(null); // {ok, message} | null
  // editSaving is true while a create/update request is in flight;
  // disables the confirm button to avoid double-submits on slow links.
  let editSaving = $state(false);

  // Add schedule form
  let showAddSchedule = $state(false);
  let newScheduleName = $state('');
  let newScheduleCron = $state('0 2 * * *');
  let newScheduleTarget = $state('');

  // Search box for the VM selector. Filtering the full list client-
  // side is fine — we don't expect more than a few dozen VMs on a
  // single host.
  let vmSearch = $state('');

  let confirmDeleteTarget = $state(null);
  let confirmDeleteSchedule = $state(null);
  let confirmDeleteFile = $state(null); // { targetId, filename }
  let askDeleteRun = $state(null); // { targetId, suffix }
  let askDeleteConfigState = $state(null); // { targetId }
  let runningBackups = $state({});
  let filesLoading = $state({}); // targetId -> bool
  let configVerifying = $state({}); // targetId -> bool

  // --- Live progress (async backup / restore jobs) ---------------
  // activeBackup tracks the in-flight "Backup now" job per target.
  // activeRestore tracks the in-flight restore-as-VM job. Both are
  // kept in sync by polling GET /api/backup/jobs while they run.
  let activeBackup = $state(null); // { targetId, jobId, pct, message }
  let activeRestore = $state(null); // { jobId, pct, message }
  let jobsPoller = $state(null); // setInterval id

  // A bulk restore (run/config/files) is in-flight for this target
  // when there's a running 'restore' task bound to that target.
  let restoreTasks = $derived(
    tasks.filter((x) => x.status === 'running' && x.kind === 'restore' && x.target_id)
  );

  function ensureJobsPoller() {
    if (jobsPoller) return;
    jobsPoller = setInterval(pollJobs, 1200);
  }

  function stopJobsPoller() {
    if (!jobsPoller) return;
    if (activeBackup || activeRestore) return;
    clearInterval(jobsPoller);
    jobsPoller = null;
  }

  async function pollJobs() {
    let all;
    try {
      const r = await api.listBackupJobs();
      all = r.jobs || [];
    } catch {
      return; // transient network hiccup; keep polling
    }
    if (activeBackup) {
      const j = all.find((x) => x.id === activeBackup.jobId);
      if (j) {
        activeBackup = {
          ...activeBackup,
          pct: j.progress ?? 0,
          stage: j.stage || '',
          stage_vars: j.stage_vars || {},
          message: j.message || '',
        };
        if (j.status === 'success' || j.status === 'error') {
          const tgtId = activeBackup.targetId;
          const ok = j.status === 'success';
          const errMsg = j.error;
          finishTask('job:' + activeBackup.jobId, j.status, j.message || errMsg, j.progress ?? 100);
          activeBackup = null;
          stopJobsPoller();
          if (ok) {
            toast.success(t('backup.backupDone'));
            await load();
            await loadFiles(tgtId);
          } else {
            toast.error(errMsg || t('backup.backupFailed'));
            await load();
          }
        }
      }
    }
    if (activeRestore) {
      const j = all.find((x) => x.id === activeRestore.jobId);
      if (j) {
        activeRestore = {
          ...activeRestore,
          pct: j.progress ?? 0,
          stage: j.stage || '',
          stage_vars: j.stage_vars || {},
          message: j.message || '',
        };
        if (j.status === 'success') {
          const vmId = j.vm_id || '';
          const vmName = j.vm_name || restoreForm?.name || '';
          finishTask('job:' + activeRestore.jobId, 'success', j.message || '', 100);
          activeRestore = null;
          restoreLoading = false;
          stopJobsPoller();
          restoreForm = null;
          toast.success(t('backup.restored', { name: vmName, id: vmId.slice(0, 8) }));
          if (vmId) navigate('/vms/' + vmId);
        } else if (j.status === 'error') {
          const errMsg = j.error;
          finishTask('job:' + activeRestore.jobId, 'error', errMsg, j.progress ?? 0);
          activeRestore = null;
          restoreLoading = false;
          stopJobsPoller();
          toast.error(errMsg || t('backup.restoreFailed'));
        }
      }
    }
  }

  onMount(async () => {
    await load();
  });

  async function load() {
    try {
      const [targetsRes, schedulesRes, jobsRes, vmsRes] = await Promise.all([
        api.listBackupTargets(),
        api.listBackupSchedules(),
        api.listBackupJobs(),
        api.listVMs(),
      ]);
      targets = targetsRes.targets || [];
      schedules = schedulesRes.schedules || [];
      jobs = jobsRes.jobs || [];
      vms = vmsRes || [];
      // Re-attach any in-flight backup so the per-target progress bar
      // survives navigation (activeBackup is component-local).
      for (const j of jobs) {
        if (j.status === 'running' && targets.some((t) => t.id === j.target_id)) {
          activeBackup = {
            targetId: j.target_id,
            jobId: j.id,
            pct: j.progress ?? 0,
            stage: j.stage || '',
            stage_vars: j.stage_vars || {},
            message: j.message || '',
          };
          ensureJobsPoller();
        }
      }
    } catch (err) {
      toast.error(t('backup.loadFailed', { error: err.message }));
    } finally {
      loading = false;
    }
  }

  function buildTargetBody() {
    const body = {
      name: newTargetName.trim(),
      type: newTargetType,
      path: newTargetPath.trim(),
      vm_filter: newTargetVMFilter,
      vm_ids: newTargetVMIDs,
      enabled: newTargetEnabled,
      retention: {
        keep_last: newTargetRetentionKeepLast || 0,
        keep_days: newTargetRetentionKeepDays || 0,
      },
    };
    if (newTargetType === 'sftp') {
      body.host = newTargetHost.trim();
      body.port = newTargetPort || 22;
      body.username = newTargetUsername.trim();
      if (newTargetPassword) body.password = newTargetPassword;
      if (newTargetSSHKeyPath.trim()) body.ssh_key_path = newTargetSSHKeyPath.trim();
    }
    return body;
  }

  async function testTarget() {
    const body = buildTargetBody();
    if (!body.path) {
      toast.error(t('backup.pathRequired'));
      return;
    }
    testing = true;
    testResult = null;
    try {
      const res = await api.testBackupTarget(body);
      testResult = res;
    } catch (err) {
      testResult = { ok: false, message: err.message };
    } finally {
      testing = false;
    }
  }

  // filteredVMs is the visible list inside the Add Target dialog
  // after applying the search box. Items are sorted by name so the
  // checkboxes are stable as the user types.
  let filteredVMs = $derived.by(() => {
    const q = vmSearch.trim().toLowerCase();
    const list = q ? vms.filter((vm) => (vm.name || '').toLowerCase().includes(q)) : vms;
    return [...list].sort((a, b) => (a.name || '').localeCompare(b.name || ''));
  });

  // addTarget handles both the Add and the Edit submit. The mode
  // is dictated by editingTarget: null = add, otherwise = update.
  async function addTarget() {
    if (!newTargetName.trim() || !newTargetPath.trim()) {
      toast.error(t('backup.namePathRequired'));
      return;
    }
    if (newTargetVMFilter === 'include' && newTargetVMIDs.length === 0) {
      toast.error(t('backup.pickAtLeastOneVm'));
      return;
    }
    if (editSaving) return;
    editSaving = true;
    try {
      const payload = buildTargetBody();
      if (editingTarget) {
        // The "default" target's path is pinned server-side; strip
        // it from the payload to avoid a 400 "cannot change the
        // path of the default target". The Input itself is also
        // disabled, but defence in depth.
        if (editingTarget.id === 'default') delete payload.path;
        await api.updateBackupTarget(editingTarget.id, payload);
        resetAddTarget();
        await load();
        toast.success(t('backup.targetUpdated'));
      } else {
        // New targets are always enabled server-side; drop the
        // flag so the create request stays minimal.
        delete payload.enabled;
        await api.createBackupTarget(payload);
        resetAddTarget();
        await load();
        toast.success(t('backup.targetAdded'));
      }
    } catch (err) {
      toast.error(err.message);
    } finally {
      editSaving = false;
    }
  }

  // editTarget pre-fills the form with the values of the chosen
  // target and reopens the same dialog. Called by the per-card
  // "Edit" button; not visible on the default target's card.
  function editTarget(target) {
    editingTarget = target;
    newTargetName = target.name || '';
    newTargetType = target.type || 'local';
    newTargetPath = target.path || '';
    newTargetHost = target.host || '';
    newTargetPort = target.port || 22;
    newTargetUsername = target.username || '';
    newTargetPassword = '';
    newTargetSSHKeyPath = '';
    newTargetVMFilter = target.vm_filter || 'all';
    newTargetVMIDs = Array.isArray(target.vm_ids) ? [...target.vm_ids] : [];
    newTargetEnabled = target.enabled !== false; // default to enabled
    newTargetRetentionKeepLast = target.retention?.keep_last || 0;
    newTargetRetentionKeepDays = target.retention?.keep_days || 0;
    vmSearch = '';
    testResult = null;
    showAddTarget = true;
  }

  function resetAddTarget() {
    showAddTarget = false;
    editingTarget = null;
    newTargetName = '';
    newTargetPath = '';
    newTargetType = 'local';
    newTargetHost = '';
    newTargetPort = 22;
    newTargetUsername = '';
    newTargetPassword = '';
    newTargetSSHKeyPath = '';
    newTargetVMFilter = 'all';
    newTargetVMIDs = [];
    newTargetEnabled = true;
    newTargetRetentionKeepLast = 0;
    newTargetRetentionKeepDays = 0;
    vmSearch = '';
    testResult = null;
  }

  // resetAddTarget covers the Confirm + Cancel paths, but the X
  // button and ESC key close the dialog through the underlying
  // bits-ui Dialog without firing onCancel. Watch the open flag
  // and clear the form on the open→closed transition so the next
  // "Add target" click doesn't inherit the previous values.
  let prevAddTargetOpen = false;
  $effect(() => {
    if (prevAddTargetOpen && !showAddTarget) {
      editingTarget = null;
      newTargetName = '';
      newTargetPath = '';
      newTargetType = 'local';
      newTargetHost = '';
      newTargetPort = 22;
      newTargetUsername = '';
      newTargetPassword = '';
      newTargetSSHKeyPath = '';
      newTargetVMFilter = 'all';
      newTargetVMIDs = [];
      newTargetEnabled = true;
      vmSearch = '';
      testResult = null;
    }
    prevAddTargetOpen = showAddTarget;
  });

  async function deleteTarget(target) {
    confirmDeleteTarget = target;
  }

  async function doDeleteTarget() {
    const target = confirmDeleteTarget;
    confirmDeleteTarget = null;
    try {
      await api.deleteBackupTarget(target.id);
      await load();
      toast.success(t('backup.targetRemoved'));
    } catch (err) {
      toast.error(err.message);
    }
  }

  async function addSchedule() {
    if (!newScheduleName.trim() || !newScheduleCron.trim() || !newScheduleTarget) {
      toast.error(t('backup.nameCronTargetRequired'));
      return;
    }
    try {
      await api.createBackupSchedule({
        name: newScheduleName.trim(),
        cron: newScheduleCron.trim(),
        target_id: newScheduleTarget,
      });
      showAddSchedule = false;
      newScheduleName = '';
      newScheduleCron = '0 2 * * *';
      newScheduleTarget = '';
      await load();
      toast.success(t('backup.scheduleAdded'));
    } catch (err) {
      toast.error(err.message);
    }
  }

  async function deleteSchedule(s) {
    confirmDeleteSchedule = s;
  }

  async function doDeleteSchedule() {
    const s = confirmDeleteSchedule;
    confirmDeleteSchedule = null;
    try {
      await api.deleteBackupSchedule(s.id);
      await load();
      toast.success(t('backup.scheduleRemoved'));
    } catch (err) {
      toast.error(err.message);
    }
  }

  async function toggleSchedule(s) {
    try {
      await api.updateBackupSchedule(s.id, { enabled: !s.enabled });
      await load();
    } catch (err) {
      toast.error(err.message);
    }
  }

  async function runBackup(target) {
    try {
      const res = await api.backupNow(target.id);
      const job = res.job;
      if (!job || !job.id) throw new Error(res.error || t('backup.backupFailed'));
      activeBackup = {
        targetId: target.id,
        jobId: job.id,
        pct: job.progress ?? 0,
        stage: job.stage || '',
        stage_vars: job.stage_vars || {},
        message: job.message || '',
      };
      upsertTask({
        id: 'job:' + job.id,
        kind: 'backup',
        title: target.name || target.id,
        pct: job.progress ?? 0,
        stage: job.stage || '',
        stage_vars: job.stage_vars || {},
        message: job.message || '',
        status: 'running',
      });
      ensureJobsPoller();
    } catch (err) {
      toast.error(err.message);
    }
  }

  // loadFiles fetches the per-target archive list. Called lazily
  // on first visit to the Files tab and again after a backup.
  async function loadFiles(targetId) {
    filesLoading = { ...filesLoading, [targetId]: true };
    try {
      const r = await api.listBackupsOnTarget(targetId);
      filesByTarget = { ...filesByTarget, [targetId]: r.backups || [] };
      configByTarget = { ...configByTarget, [targetId]: r.config || null };
    } catch (err) {
      toast.error(t('backup.listFilesFailed', { error: err.message }));
    } finally {
      filesLoading = { ...filesLoading, [targetId]: false };
    }
  }

  // verifyFile is a one-shot sha256 read; we don't keep the result
  // around, just toast it. If the user wants to inspect the hash
  // they can re-click or use the (future) dedicated detail view.
  let verifying = $state({}); // `${targetId}/${filename}` -> bool
  // verifyResult holds the outcome shown in a dialog so the
  // verification is visible and informative (not just a toast).
  let verifyResult = $state(null); // { name, filename, size, modified, sha256 }

  async function verifyFile(targetId, filename) {
    const key = `${targetId}/${filename}`;
    verifying = { ...verifying, [key]: true };
    try {
      const r = await api.verifyBackup(targetId, filename);
      verifyResult = {
        name: displayVmName(filename) || filename,
        filename,
        size: r.size,
        modified: r.modified,
        sha256: r.sha256 || '',
      };
    } catch (err) {
      toast.error(err.message);
    } finally {
      verifying = { ...verifying, [key]: false };
    }
  }

  function copySha() {
    if (!verifyResult?.sha256) return;
    navigator.clipboard.writeText(verifyResult.sha256).then(
      () => toast.success(t('backup.shaCopied')),
      () => toast.error(t('account.copyFailed'))
    );
  }

  function askDeleteFile(targetId, filename) {
    confirmDeleteFile = { targetId, filename };
  }

  async function doDeleteFile() {
    const { targetId, filename } = confirmDeleteFile;
    confirmDeleteFile = null;
    try {
      await api.deleteBackupFile(targetId, filename);
      await loadFiles(targetId);
      toast.success(t('backup.deleted', { name: filename }));
    } catch (err) {
      toast.error(err.message);
    }
  }

  // lastJobFor returns the most recent job for a target, if any.
  function lastJobFor(targetId) {
    const byTarget = jobs.filter((j) => j.target_id === targetId);
    if (byTarget.length === 0) return null;
    byTarget.sort((a, b) => (a.started_at < b.started_at ? 1 : -1));
    return byTarget[0];
  }

  // runSuffixOf extracts the "<ts26>-<randHex>" run identifier from
  // a backup filename (webkvm-<host>-<ts>-<rand>-<name>.tar.zst).
  // Returns null for legacy / unparseable names.
  function runSuffixOf(filename) {
    const m = filename.match(
      /^webkvm-[^-]+-(\d{8}T\d{6}\.\d{9}Z-[0-9a-f]{6,12})-[^/]+\.tar\.(gz|zst)$/
    );
    return m ? m[1] : null;
  }

  // groupFilesByRun groups a target's backup files into runs.
  // Files sharing the same run suffix belong to one backup run.
  // The per-run config tar is split off so it doesn't clutter the
  // VM list (the stable "latest config" snapshot is shown separately
  // via configByTarget; per-run configs stay on disk for compat).
  function groupFilesByRun(files) {
    const byRun = new Map(); // suffix -> files
    const ungrouped = [];
    for (const f of files) {
      if (f.filename && f.filename.includes('-config.tar.')) continue;
      const s = runSuffixOf(f.filename);
      if (s) {
        if (!byRun.has(s)) byRun.set(s, []);
        byRun.get(s).push(f);
      } else {
        ungrouped.push(f);
      }
    }
    const runs = [];
    for (const [suffix, runFiles] of byRun) {
      const first = runFiles[0];
      runs.push({
        suffix,
        date: first.modified,
        files: runFiles,
        size: runFiles.reduce((acc, f) => acc + (f.size || 0), 0),
      });
    }
    runs.sort((a, b) => (a.date < b.date ? 1 : -1));
    return { runs, ungrouped };
  }

  async function restoreRun(target, suffix) {
    try {
      const res = await api.restoreBackupRun(target.id, suffix);
      if (!res.job_id) throw new Error(res.error || t('backup.restoreFailed'));
      upsertTask({
        id: 'job:' + res.job_id,
        kind: 'restore',
        target_id: target.id,
        title: t('backup.restoreRun'),
        pct: 1,
        stage: 'restore_preparing',
        stage_vars: {},
        message: '',
        status: 'running',
      });
    } catch (err) {
      toast.error(err.message);
    }
  }

  async function deleteRun(target, suffix) {
    askDeleteRun = { targetId: target.id, suffix };
  }

  // --- Stable "latest configuration" snapshot actions ------------
  async function verifyConfig(target) {
    configVerifying = { ...configVerifying, [target.id]: true };
    try {
      const r = await api.verifyBackup(target.id, 'config/webkvm-config-latest.tar.zst');
      verifyResult = {
        name: 'config',
        filename: r.filename,
        size: r.size,
        modified: r.modified,
        sha256: r.sha256 || '',
      };
    } catch (err) {
      toast.error(err.message);
    } finally {
      configVerifying = { ...configVerifying, [target.id]: false };
    }
  }

  async function restoreConfig(target) {
    try {
      const res = await api.restoreBackupConfig(target.id);
      if (!res.job_id) throw new Error(res.error || t('backup.restoreFailed'));
      upsertTask({
        id: 'job:' + res.job_id,
        kind: 'restore',
        target_id: target.id,
        title: t('backup.restoreConfig'),
        pct: 1,
        stage: 'restore_preparing',
        stage_vars: {},
        message: '',
        status: 'running',
      });
    } catch (err) {
      toast.error(err.message);
    }
  }

  function askDeleteConfig(target) {
    askDeleteConfigState = { targetId: target.id };
  }

  async function doDeleteConfig() {
    const { targetId } = askDeleteConfigState;
    askDeleteConfigState = null;
    try {
      await api.deleteBackupConfig(targetId);
      await loadFiles(targetId);
      toast.success(t('backup.configDeleted'));
    } catch (err) {
      toast.error(err.message);
    }
  }

  async function doDeleteRun() {
    const { targetId, suffix } = askDeleteRun;
    askDeleteRun = null;
    try {
      const res = await api.deleteBackupRun(targetId, suffix);
      await loadFiles(targetId);
      toast.success(t('backup.runDeleted', { n: res.removed || 0 }));
    } catch (err) {
      toast.error(err.message);
    }
  }

  // restoreForm is the per-file Restore-as-VM dialog state.
  // - target/filename: which archive
  // - name: what to call the new VM (default = filename stem)
  // - pool: which storage pool to put the disk in (default =
  //   webkvm-disks or the first disk-purpose pool available)
  // - loading: disable the submit button while the API call
  //   is in flight (multi-GB imports can take a while)
  let restoreForm = $state(null);
  let restoreLoading = $state(false);
  let diskPools = $state([]);
  let restoreNetworks = $state([]);

  // Load disk pools + networks once so the dialog's pickers have
  // options without hitting the API every time it opens.
  onMount(async () => {
    try {
      const [all, nets] = await Promise.all([api.listPools(), api.listNetworks()]);
      diskPools = (all || []).filter((p) => p.purpose !== 'iso');
      restoreNetworks = nets || [];
      if (diskPools.length > 0 && !restoreForm?.pool) {
        const def = diskPools.find((p) => p.name === 'webkvm-disks') || diskPools[0];
        if (restoreForm) restoreForm.pool = def.name;
      }
    } catch {
      // If the pool list fails the dialog will show an empty
      // picker; the backend will reject with a 500 if the
      // operator picks a non-existent one.
    }
  });

  // vmNameOf extracts the VM identifier embedded in the archive
  // filename. The runner names per-VM archives with the VM's UUID,
  // so the extracted value is typically a UUID.
  function vmNameOf(filename) {
    const m = filename.match(
      /^webkvm-[^-]+-\d{8}T\d{6}\.\d{9}Z-[0-9a-f]{6,12}-(.+)\.tar\.(gz|zst)$/
    );
    return m ? m[1] : null;
  }

  // displayVmName resolves the archive's VM identifier to a human
  // name when the VM still exists (so a run shows "Ubuntu" instead
  // of a raw UUID). Returns the raw id otherwise.
  function displayVmName(filename) {
    const id = vmNameOf(filename);
    if (!id) return null;
    const known = vms.find((v) => v.id === id);
    return known ? known.name : id;
  }

  function openRestoreDialog(target, filename) {
    // Derive a sensible default name from the VM inside the archive:
    // <vmname>-restored (e.g. "Ubuntu-restored"). Falls back to a
    // hash-based name for legacy / unparseable archives.
    const vm = displayVmName(filename);
    let defaultName = vm ? `${vm}-restored` : 'restored';
    if (!vm) {
      const base = filename.replace(/\.tar\.(gz|zst)$/, '');
      const m = base.match(/-([0-9a-f-]{36})$/i);
      if (m) defaultName = `restored-${m[1].slice(0, 8)}`;
    }
    const defPool = diskPools.find((p) => p.name === 'webkvm-disks') || diskPools[0];
    restoreForm = {
      target,
      filename,
      vm,
      name: defaultName,
      pool: defPool?.name || 'webkvm-disks',
      network: '',
      vcpus: '',
      ramMB: '',
      autostart: false,
    };
  }

  function closeRestoreDialog() {
    if (restoreLoading) return;
    restoreForm = null;
  }

  async function submitRestore() {
    if (!restoreForm || restoreLoading) return;
    if (!restoreForm.name.trim()) {
      toast.error(t('backup.vmNameRequired'));
      return;
    }
    restoreLoading = true;
    try {
      const payload = {
        filename: restoreForm.filename,
        name: restoreForm.name.trim(),
        pool: restoreForm.pool,
      };
      if (restoreForm.network) payload.network = restoreForm.network;
      if (restoreForm.vcpus) payload.vcpus = Number(restoreForm.vcpus);
      if (restoreForm.ramMB) payload.ram_mb = Number(restoreForm.ramMB);
      if (restoreForm.autostart) payload.autostart = true;
      const r = await api.restoreAsVM(restoreForm.target.id, payload);
      if (!r.job_id) throw new Error(r.error || t('backup.restoreFailed'));
      // The restore runs in the background; keep the dialog open
      // and let pollJobs update the progress bar, then navigate
      // to the new VM when the job completes.
      activeRestore = {
        jobId: r.job_id,
        pct: 1,
        stage: 'restore_preparing',
        stage_vars: {},
        message: '',
      };
      upsertTask({
        id: 'job:' + r.job_id,
        kind: 'restore',
        title: restoreForm.vm || restoreForm.filename,
        pct: 1,
        stage: 'restore_preparing',
        stage_vars: {},
        message: '',
        status: 'running',
      });
      ensureJobsPoller();
    } catch (err) {
      restoreLoading = false;
      toast.error(err.message || t('backup.restoreFailed'));
    }
  }

  // --- Formatters -----------------------------------------------------
  function fmtBytes(n) {
    if (!n) return '0 B';
    const k = 1024;
    const units = ['B', 'KB', 'MB', 'GB'];
    const i = Math.min(Math.floor(Math.log(n) / Math.log(k)), units.length - 1);
    return `${(n / Math.pow(k, i)).toFixed(1)} ${units[i]}`;
  }

  function fmtDate(iso) {
    if (!iso) return '—';
    return new Date(iso).toLocaleString();
  }

  // vmFilterSummary returns the human description of a target's
  // VM filter. Used both in the card and in the dialog label.
  function vmFilterSummary(target) {
    const filter = target.vm_filter || 'all';
    if (filter === 'all' || !target.vm_ids || target.vm_ids.length === 0) {
      return t('backup.allVms');
    }
    const names = target.vm_ids.map((id) => vms.find((v) => v.id === id)?.name || id).join(', ');
    if (filter === 'include') {
      return t('backup.vmFilterInclude', {
        n: target.vm_ids.length,
        s: target.vm_ids.length === 1 ? '' : 's',
        names,
      });
    }
    return t('backup.vmFilterExclude', { n: target.vm_ids.length, names });
  }

  // --- Tab-change handler --------------------------------------------
  // setTab switches tabs and reloads the per-target archive list when
  // opening Files, so a backup finished while the user was elsewhere
  // shows up without a manual refresh. (Done in a click handler, not
  // a $effect, to avoid Svelte's effect_update_depth_exceeded loop.)
  function setTab(tab) {
    activeTab = tab;
    if (tab === 'files') {
      for (const target of targets) {
        loadFiles(target.id);
      }
    }
  }

  // targetName resolves a target id to its display name, so the
  // Jobs tab doesn't show raw ids (or ids of targets that have
  // since been deleted).
  function targetName(id) {
    return targets.find((x) => x.id === id)?.name || id;
  }

  // jobVms lists the VM names (or ids) backed up by a job, from the
  // job's non-config archives.
  function jobVms(job) {
    return (job.filenames || [])
      .filter((fn) => !fn.includes('-config'))
      .map((fn) => displayVmName(fn));
  }
</script>

<div class="p-6 max-w-5xl">
  <PageHeader title={t('backup.title')} subtitle={t('backup.subtitle')}>
    {#snippet actions()}
      {#if activeTab === 'targets'}
        <Button onclick={() => (showAddTarget = true)}>{t('backup.addTarget')}</Button>
      {:else if activeTab === 'schedules'}
        <Button onclick={() => (showAddSchedule = true)}>{t('backup.addSchedule')}</Button>
      {/if}
    {/snippet}
  </PageHeader>

  <div class="flex gap-1 mb-4 border-b border-border">
    <button
      class="px-3 py-2 text-sm font-medium border-b-2 transition-colors {activeTab === 'targets'
        ? 'border-accent text-foreground'
        : 'border-transparent text-muted-foreground hover:text-foreground'}"
      onclick={() => setTab('targets')}
    >
      {t('backup.targetsCount', { n: targets.length })}
    </button>
    <button
      class="px-3 py-2 text-sm font-medium border-b-2 transition-colors {activeTab === 'files'
        ? 'border-accent text-foreground'
        : 'border-transparent text-muted-foreground hover:text-foreground'}"
      onclick={() => setTab('files')}
    >
      {t('backup.filesCount')}
    </button>
    <button
      class="px-3 py-2 text-sm font-medium border-b-2 transition-colors {activeTab === 'schedules'
        ? 'border-accent text-foreground'
        : 'border-transparent text-muted-foreground hover:text-foreground'}"
      onclick={() => setTab('schedules')}
    >
      {t('backup.schedulesCount', { n: schedules.length })}
    </button>
    <button
      class="px-3 py-2 text-sm font-medium border-b-2 transition-colors {activeTab === 'jobs'
        ? 'border-accent text-foreground'
        : 'border-transparent text-muted-foreground hover:text-foreground'}"
      onclick={() => setTab('jobs')}
    >
      {t('backup.jobsCount', { n: jobs.length })}
    </button>
  </div>

  {#if loading}
    <p class="text-sm text-muted-foreground">{t('common.loading')}</p>
  {:else if activeTab === 'targets'}
    <div class="space-y-3">
      {#each targets as target (target.id)}
        {@const last = lastJobFor(target.id)}
        <div class="border border-border rounded-lg bg-card p-4">
          <div class="flex items-center justify-between mb-2">
            <div class="flex items-center gap-2">
              <span class="font-medium">{target.name}</span>
              <span
                class="text-[10px] px-1.5 py-0.5 rounded bg-muted text-muted-foreground uppercase"
                >{target.type}</span
              >
              {#if !target.enabled}
                <span class="text-[10px] px-1.5 py-0.5 rounded bg-destructive/10 text-destructive"
                  >{t('backup.disabledBadge')}</span
                >
              {:else if last && last.status === 'success'}
                <span class="text-[10px] px-1.5 py-0.5 rounded bg-success/10 text-success uppercase"
                  >{t('backup.lastOk')}</span
                >
              {:else if last && last.status === 'error'}
                <span
                  class="text-[10px] px-1.5 py-0.5 rounded bg-destructive/10 text-destructive uppercase"
                  >{t('backup.lastFailed')}</span
                >
              {:else if last && last.status === 'running'}
                <span class="text-[10px] px-1.5 py-0.5 rounded bg-warning/10 text-warning uppercase"
                  >{t('backup.running')}</span
                >
              {/if}
            </div>
            <div class="flex gap-1">
              <Button
                size="xs"
                onclick={() => runBackup(target)}
                disabled={runningBackups[target.id] || activeBackup?.targetId === target.id}
              >
                {activeBackup?.targetId === target.id ? t('backup.running') : t('backup.backupNow')}
              </Button>
              {#if target.id !== 'default'}
                <Button size="xs" variant="outline" onclick={() => editTarget(target)}
                  >{t('common.edit')}</Button
                >
                <Button size="xs" variant="destructive" onclick={() => deleteTarget(target)}>
                  {t('backup.remove')}
                </Button>
              {/if}
            </div>
          </div>
          {#if activeBackup?.targetId === target.id}
            <ProgressBar
              value={activeBackup.pct}
              label={progressLabel(
                activeBackup.stage,
                activeBackup.stage_vars,
                activeBackup.message || t('backup.running'),
                t
              )}
              showValue
              size="sm"
              class="mb-2"
            />
          {/if}
          <p class="text-xs text-muted-foreground font-mono mb-2">{target.path}</p>
          <div class="flex items-center justify-between gap-2">
            <p class="text-xs text-muted-foreground">
              <span class="font-medium text-foreground/70">{t('backup.backsUp')}</span>
              {vmFilterSummary(target)}
            </p>
            {#if last}
              <span class="text-xs text-muted-foreground tnum shrink-0">
                {t('backup.lastRun', { date: fmtDate(last.started_at) })}
              </span>
            {:else}
              <span class="text-xs text-muted-foreground shrink-0">{t('backup.neverRun')}</span>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {:else if activeTab === 'files'}
    {#if targets.length === 0}
      <p class="text-sm text-muted-foreground">{t('backup.noTargetsYet')}</p>
    {:else}
      <div class="space-y-4">
        {#each targets as target (target.id)}
          {@const files = filesByTarget[target.id]}
          <div class="border border-border rounded-lg bg-card p-4">
            <div class="flex items-center justify-between mb-3">
              <div>
                <div class="font-medium">{target.name}</div>
                <p class="text-xs text-muted-foreground font-mono">{target.path}</p>
              </div>
              <Button
                size="xs"
                variant="outline"
                onclick={() => loadFiles(target.id)}
                disabled={filesLoading[target.id]}
              >
                {filesLoading[target.id] ? t('backup.refreshing') : t('backup.refresh')}
              </Button>
            </div>

            <!-- Stable "latest configuration" snapshot -->
            <div class="border border-border rounded-md bg-background overflow-hidden mb-3">
              <div class="flex items-center justify-between gap-2 px-3 py-2 bg-muted/40">
                <div class="min-w-0">
                  <div class="text-sm font-medium flex items-center gap-2">
                    <svg
                      class="w-3.5 h-3.5 text-muted-foreground shrink-0"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="2"
                      viewBox="0 0 24 24"
                      ><path
                        d="M12 15v2m-6 4h12a2 2 0 0 0 2-2v-6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2zm10-10V7a4 4 0 0 0-8 0v4h8z"
                      /></svg
                    >
                    {t('backup.configSection')}
                  </div>
                  {#if configByTarget[target.id]}
                    <div class="text-xs text-muted-foreground">
                      {fmtDate(configByTarget[target.id].modified)} · {fmtBytes(
                        configByTarget[target.id].size
                      )}
                    </div>
                  {/if}
                </div>
                {#if configByTarget[target.id]}
                  <div class="flex gap-1 shrink-0">
                    <Button
                      size="xs"
                      variant="outline"
                      onclick={() => verifyConfig(target)}
                      disabled={configVerifying[target.id]}
                    >
                      {configVerifying[target.id] ? '…' : t('backup.verify')}
                    </Button>
                    <Button
                      size="xs"
                      variant="outline"
                      onclick={() => restoreConfig(target)}
                      disabled={restoreTasks.some((x) => x.target_id === target.id)}
                      >{restoreTasks.some((x) => x.target_id === target.id)
                        ? t('backup.restoring')
                        : t('backup.restoreConfig')}</Button
                    >
                    <Button size="xs" variant="destructive" onclick={() => askDeleteConfig(target)}
                      >{t('backup.delete')}</Button
                    >
                  </div>
                {:else if !filesLoading[target.id]}
                  <span class="text-xs text-muted-foreground">{t('backup.configEmpty')}</span>
                {/if}
              </div>
              {#if configByTarget[target.id]}
                <div class="px-3 py-1.5 text-xs text-muted-foreground border-t border-border">
                  {t('backup.configHint')}
                </div>
              {/if}
            </div>

            {#if filesLoading[target.id] && !files}
              <p class="text-sm text-muted-foreground">{t('common.loading')}</p>
            {:else if !files || files.length === 0}
              <p class="text-sm text-muted-foreground">{t('backup.noBackupFilesYet')}</p>
            {:else}
              {@const { runs, ungrouped } = groupFilesByRun(files)}
              <div class="space-y-3">
                {#each runs as run (run.suffix)}
                  <div class="border border-border rounded-md bg-background">
                    <div
                      class="flex items-center justify-between gap-2 px-3 py-2 bg-muted/40 rounded-t-md"
                    >
                      <div class="min-w-0">
                        <div class="text-sm font-medium">
                          {t('backup.runLabel', { date: fmtDate(run.date) })}
                        </div>
                        <div class="text-xs text-muted-foreground">
                          {t('backup.runFiles', {
                            n: run.files.length,
                            size: fmtBytes(run.size),
                          })}
                        </div>
                      </div>
                      <div class="flex gap-1 shrink-0">
                        <Button
                          size="xs"
                          variant="outline"
                          onclick={() => restoreRun(target, run.suffix)}
                          disabled={restoreTasks.some((x) => x.target_id === target.id)}
                          >{restoreTasks.some((x) => x.target_id === target.id)
                            ? t('backup.restoring')
                            : t('backup.restoreRun')}</Button
                        >
                        <Button
                          size="xs"
                          variant="destructive"
                          onclick={() => deleteRun(target, run.suffix)}
                          >{t('backup.deleteRun')}</Button
                        >
                      </div>
                    </div>
                    <div class="space-y-1.5 p-2">
                      {#each run.files as f (f.filename)}
                        <div
                          class="flex items-center justify-between gap-2 py-1.5 px-2 rounded bg-muted/30"
                        >
                          <div class="min-w-0 flex-1">
                            {#if displayVmName(f.filename)}
                              <div class="text-sm font-medium truncate">
                                {displayVmName(f.filename)}
                              </div>
                              <div class="font-mono text-xs text-muted-foreground truncate">
                                {f.filename}
                              </div>
                            {:else}
                              <div class="font-mono text-sm truncate">{f.filename}</div>
                            {/if}
                            <div class="text-xs text-muted-foreground">
                              {fmtBytes(f.size)} · {fmtDate(f.modified)}
                            </div>
                          </div>
                          <div class="flex gap-1 shrink-0">
                            <Button
                              size="xs"
                              variant="outline"
                              onclick={() => verifyFile(target.id, f.filename)}
                              disabled={verifying[`${target.id}/${f.filename}`]}
                            >
                              {verifying[`${target.id}/${f.filename}`] ? '…' : t('backup.verify')}
                            </Button>
                            <Button
                              size="xs"
                              variant="outline"
                              onclick={() => openRestoreDialog(target, f.filename)}
                            >
                              {t('backup.restore')}
                            </Button>
                            <Button
                              size="xs"
                              variant="outline"
                              onclick={() => askDeleteFile(target.id, f.filename)}
                            >
                              {t('backup.delete')}
                            </Button>
                          </div>
                        </div>
                      {/each}
                    </div>
                  </div>
                {/each}
                {#if ungrouped.length > 0}
                  <div class="space-y-1.5">
                    {#each ungrouped as f (f.filename)}
                      <div
                        class="flex items-center justify-between gap-2 py-1.5 px-2 rounded bg-muted/30"
                      >
                        <div class="min-w-0 flex-1">
                          {#if displayVmName(f.filename)}
                            <div class="text-sm font-medium truncate">
                              {displayVmName(f.filename)}
                            </div>
                            <div class="font-mono text-xs text-muted-foreground truncate">
                              {f.filename}
                            </div>
                          {:else}
                            <div class="font-mono text-sm truncate">{f.filename}</div>
                          {/if}
                          <div class="text-xs text-muted-foreground">
                            {fmtBytes(f.size)} · {fmtDate(f.modified)}
                          </div>
                        </div>
                        <div class="flex gap-1 shrink-0">
                          <Button
                            size="xs"
                            variant="outline"
                            onclick={() => verifyFile(target.id, f.filename)}
                            disabled={verifying[`${target.id}/${f.filename}`]}
                          >
                            {verifying[`${target.id}/${f.filename}`] ? '…' : t('backup.verify')}
                          </Button>
                          <Button
                            size="xs"
                            variant="outline"
                            onclick={() => openRestoreDialog(target, f.filename)}
                          >
                            {t('backup.restore')}
                          </Button>
                          <Button
                            size="xs"
                            variant="outline"
                            onclick={() => askDeleteFile(target.id, f.filename)}
                          >
                            {t('backup.delete')}
                          </Button>
                        </div>
                      </div>
                    {/each}
                  </div>
                {/if}
              </div>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  {:else if activeTab === 'schedules'}
    <div class="space-y-3">
      {#each schedules as s (s.id)}
        <div class="border border-border rounded-lg bg-card p-4 flex items-center justify-between">
          <div>
            <div class="flex items-center gap-2">
              <span class="font-medium">{s.name}</span>
              {#if !s.enabled}
                <span class="text-[10px] px-1.5 py-0.5 rounded bg-muted text-muted-foreground"
                  >{t('backup.disabledBadge')}</span
                >
              {/if}
              {#if s.last_status === 'success'}
                <span class="text-[10px] px-1.5 py-0.5 rounded bg-success/10 text-success"
                  >{t('backup.lastOk')}</span
                >
              {:else if s.last_status === 'error'}
                <span class="text-[10px] px-1.5 py-0.5 rounded bg-destructive/10 text-destructive"
                  >{t('backup.lastFailed')}</span
                >
              {/if}
            </div>
            <p class="text-xs text-muted-foreground mt-1">
              <span class="font-mono">{s.cron}</span> · {t('backup.target')}
              <span class="font-mono">{s.target_id}</span> · {t('backup.lastRun', {
                date: fmtDate(s.last_run_at),
              })}
            </p>
            {#if s.last_error}
              <p class="text-xs text-destructive mt-1">{s.last_error}</p>
            {/if}
          </div>
          <div class="flex gap-1">
            <Button size="xs" variant="outline" onclick={() => toggleSchedule(s)}>
              {s.enabled ? t('backup.disable') : t('backup.enable')}
            </Button>
            <Button size="xs" variant="destructive" onclick={() => deleteSchedule(s)}
              >{t('backup.remove')}</Button
            >
          </div>
        </div>
      {/each}
    </div>
  {:else if activeTab === 'jobs'}
    <div class="space-y-1">
      {#each jobs as j (j.id)}
        <div
          class="border border-border rounded-lg bg-card p-3 flex items-center justify-between text-sm"
        >
          <div class="flex items-center gap-3">
            <span
              class="w-2 h-2 rounded-full {j.status === 'success'
                ? 'bg-success'
                : j.status === 'error'
                  ? 'bg-destructive'
                  : 'bg-warning'}"
            ></span>
            <div>
              <div class="font-medium">
                {j.schedule_id
                  ? t('backup.schedulePrefix', { id: j.schedule_id })
                  : t('backup.manual')}
                · {t('backup.targetPrefix', { id: targetName(j.target_id) })}
              </div>
              <div class="text-xs text-muted-foreground">
                {fmtDate(j.started_at)}{j.ended_at ? ` → ${fmtDate(j.ended_at)}` : ''}
                · {fmtBytes(j.size)}
              </div>
              {#if jobVms(j).length > 0}
                <div class="flex flex-wrap gap-1 mt-1.5">
                  {#each jobVms(j) as vmName (vmName)}
                    <span
                      class="inline-flex items-center gap-1 text-[10px] px-1.5 py-0.5 rounded-full bg-accent/10 text-accent font-medium"
                    >
                      <svg
                        class="w-2.5 h-2.5"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="2"
                        viewBox="0 0 24 24"
                        ><rect x="2" y="3" width="20" height="14" rx="2" /><line
                          x1="8"
                          y1="21"
                          x2="16"
                          y2="21"
                        /><line x1="12" y1="17" x2="12" y2="21" /></svg
                      >
                      {vmName}
                    </span>
                  {/each}
                </div>
              {/if}
              {#if j.error}
                <div class="text-xs text-destructive mt-1">{j.error}</div>
              {/if}
            </div>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<ConfirmDialog
  open={showAddTarget}
  title={editingTarget ? t('backup.editTargetTitle') : t('backup.addTargetTitle')}
  message={editingTarget ? t('backup.editTargetMsg') : t('backup.addTargetMsg')}
  confirmLabel={editingTarget ? t('common.save') : t('backup.addTarget')}
  onConfirm={addTarget}
  onCancel={resetAddTarget}
>
  <div class="space-y-3">
    <div>
      <label class="text-sm font-medium block mb-1" for="add-tgt-name">{t('backup.name')}</label>
      <Input id="add-tgt-name" bind:value={newTargetName} placeholder="e.g. nightly-nfs" />
    </div>
    <div>
      <label class="text-sm font-medium block mb-1" for="add-tgt-type">{t('backup.type')}</label>
      <select
        id="add-tgt-type"
        bind:value={newTargetType}
        class="w-full h-8 rounded-lg border border-border bg-background px-2 text-sm"
      >
        <option value="local">{t('backup.localDir')}</option>
        <option value="nfs">{t('backup.nfsMounted')}</option>
        <option value="smb">{t('backup.smbMounted')}</option>
        <option value="sftp">{t('backup.sftpType')}</option>
      </select>
    </div>
    {#if newTargetType === 'sftp'}
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="text-sm font-medium block mb-1" for="add-tgt-host">{t('backup.host')}</label
          >
          <Input id="add-tgt-host" bind:value={newTargetHost} placeholder="backup.example.com" />
        </div>
        <div>
          <label class="text-sm font-medium block mb-1" for="add-tgt-port">{t('backup.port')}</label
          >
          <Input id="add-tgt-port" type="number" bind:value={newTargetPort} min="1" max="65535" />
        </div>
      </div>
      <div>
        <label class="text-sm font-medium block mb-1" for="add-tgt-user"
          >{t('backup.username')}</label
        >
        <Input id="add-tgt-user" bind:value={newTargetUsername} placeholder="backup" />
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label class="text-sm font-medium block mb-1" for="add-tgt-pass"
            >{t('backup.password')}</label
          >
          <Input
            id="add-tgt-pass"
            bind:value={newTargetPassword}
            type="password"
            placeholder={editingTarget ? '••••••••' : ''}
            autocomplete="new-password"
          />
        </div>
        <div>
          <label class="text-sm font-medium block mb-1" for="add-tgt-key"
            >{t('backup.sshKeyPath')}</label
          >
          <Input
            id="add-tgt-key"
            bind:value={newTargetSSHKeyPath}
            placeholder="/root/.ssh/id_ed25519"
          />
        </div>
      </div>
      <p class="text-xs text-muted-foreground">{t('backup.sftpHint')}</p>
    {/if}
    <div>
      <label class="text-sm font-medium block mb-1" for="add-tgt-path">
        {newTargetType === 'sftp' ? t('backup.remoteDir') : t('backup.path')}
        {#if editingTarget && editingTarget.id === 'default'}
          <span class="text-xs text-muted-foreground font-normal"
            >{t('backup.pinnedByBackend')}</span
          >
        {/if}
      </label>
      <Input
        id="add-tgt-path"
        bind:value={newTargetPath}
        placeholder={newTargetType === 'sftp' ? '/backups/webkvm' : '/mnt/backups'}
        class="font-mono"
        disabled={!!(editingTarget && editingTarget.id === 'default')}
      />
    </div>
    <div class="flex items-center gap-2">
      <Button size="sm" variant="outline" onclick={testTarget} disabled={testing}>
        {testing ? t('backup.testing') : t('backup.test')}
      </Button>
      {#if testResult}
        <span class="text-xs {testResult.ok ? 'text-success' : 'text-destructive'}"
          >{testResult.ok ? '✓ ' : '✗ '}{testResult.message}</span
        >
      {/if}
    </div>

    <!--
      VM selector: three radios, plus a conditional checkbox list
      with a search box. The list is bound to newTargetVMIDs as a
      Set-like array; toggling a checkbox adds or removes its VM ID.

      The checkboxes are native <input type=checkbox> rather than
      the bespoke Checkbox.svelte wrapper because that component
      only exposes bind:checked — it has no `onchange` prop, so
      imperative mutations on newTargetVMIDs wouldn't propagate.
    -->
    <div>
      <div class="text-sm font-medium mb-1">{t('backup.vmsLabel')}</div>
      <div class="flex flex-wrap gap-3 mb-2">
        <label class="flex items-center gap-1.5 text-sm cursor-pointer">
          <input
            type="radio"
            name="vm-filter"
            value="all"
            checked={newTargetVMFilter === 'all'}
            onchange={() => {
              newTargetVMFilter = 'all';
              newTargetVMIDs = [];
            }}
            class="accent-accent"
          />
          {t('backup.allVms')}
        </label>
        <label class="flex items-center gap-1.5 text-sm cursor-pointer">
          <input
            type="radio"
            name="vm-filter"
            value="include"
            checked={newTargetVMFilter === 'include'}
            onchange={() => (newTargetVMFilter = 'include')}
            class="accent-accent"
          />
          {t('backup.selectedOnly')}
        </label>
        <label class="flex items-center gap-1.5 text-sm cursor-pointer">
          <input
            type="radio"
            name="vm-filter"
            value="exclude"
            checked={newTargetVMFilter === 'exclude'}
            onchange={() => (newTargetVMFilter = 'exclude')}
            class="accent-accent"
          />
          {t('backup.allExcept')}
        </label>
      </div>
      {#if newTargetVMFilter !== 'all'}
        <Input bind:value={vmSearch} placeholder={t('backup.searchVms')} class="mb-2" />
        <div
          class="border border-border rounded-md bg-background max-h-48 overflow-y-auto p-1 space-y-0.5"
        >
          {#each filteredVMs as vm (vm.id)}
            <label
              class="flex items-start gap-2 px-2 py-1.5 rounded cursor-pointer hover:bg-muted/40"
            >
              <input
                type="checkbox"
                checked={newTargetVMIDs.includes(vm.id)}
                onchange={(e) => {
                  const c = e.currentTarget.checked;
                  if (c) {
                    if (!newTargetVMIDs.includes(vm.id)) {
                      newTargetVMIDs = [...newTargetVMIDs, vm.id];
                    }
                  } else {
                    newTargetVMIDs = newTargetVMIDs.filter((id) => id !== vm.id);
                  }
                }}
                class="mt-0.5 h-4 w-4 rounded border-border bg-background text-accent focus:ring-2 focus:ring-accent/40 accent-accent"
              />
              <span class="flex-1 min-w-0">
                <span class="text-sm font-medium block leading-tight">{vm.name}</span>
                <span class="text-xs text-muted-foreground block">{vm.state}</span>
              </span>
            </label>
          {:else}
            <p class="text-sm text-muted-foreground px-2 py-3 text-center">
              {vmSearch ? t('backup.noVmsMatch') : t('backup.noVmsFound')}
            </p>
          {/each}
        </div>
        <p class="text-xs text-muted-foreground mt-1">
          {t('backup.nSelected', { n: newTargetVMIDs.length })}
        </p>
      {/if}
    </div>

    <div class="pt-1 border-t border-border space-y-3">
      <div>
        <span class="text-sm font-medium">{t('backup.retentionTitle')}</span>
        <p class="text-xs text-muted-foreground">{t('backup.retentionDesc')}</p>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div class="space-y-1.5">
          <Label for="retention-keep-last">{t('backup.retentionKeepLast')}</Label>
          <Input
            id="retention-keep-last"
            type="number"
            min="0"
            bind:value={newTargetRetentionKeepLast}
            placeholder={t('backup.retentionUnlimited')}
          />
        </div>
        <div class="space-y-1.5">
          <Label for="retention-keep-days">{t('backup.retentionKeepDays')}</Label>
          <Input
            id="retention-keep-days"
            type="number"
            min="0"
            bind:value={newTargetRetentionKeepDays}
            placeholder={t('backup.retentionUnlimited')}
          />
        </div>
      </div>
      <p class="text-xs text-muted-foreground">{t('backup.retentionHint')}</p>
    </div>

    {#if editingTarget}
      <div class="pt-1 border-t border-border">
        <Switch
          bind:checked={newTargetEnabled}
          label={t('backup.enabled')}
          description={t('backup.enabledDesc')}
        />
      </div>
    {/if}
  </div>
</ConfirmDialog>

<ConfirmDialog
  open={showAddSchedule}
  title={t('backup.addScheduleTitle')}
  message={t('backup.addScheduleMsg')}
  confirmLabel={t('backup.addSchedule')}
  onConfirm={addSchedule}
  onCancel={() => (showAddSchedule = false)}
>
  <div class="space-y-3">
    <div>
      <label class="text-sm font-medium block mb-1" for="add-sched-name">{t('backup.name')}</label>
      <Input id="add-sched-name" bind:value={newScheduleName} placeholder="e.g. nightly" />
    </div>
    <div>
      <label class="text-sm font-medium block mb-1" for="add-sched-cron"
        >{t('backup.scheduleLabel')}</label
      >
      <CronPicker bind:expression={newScheduleCron} />
    </div>
    <div>
      <label class="text-sm font-medium block mb-1" for="add-sched-target"
        >{t('backup.target')}</label
      >
      <select
        id="add-sched-target"
        bind:value={newScheduleTarget}
        class="w-full h-8 rounded-lg border border-border bg-background px-2 text-sm"
      >
        <option value="">{t('backup.pickTarget')}</option>
        {#each targets as target}
          <option value={target.id}>{target.name} ({target.type})</option>
        {/each}
      </select>
    </div>
  </div>
</ConfirmDialog>

<ConfirmDialog
  open={!!confirmDeleteTarget}
  title={t('backup.removeTargetTitle')}
  message={confirmDeleteTarget
    ? t('backup.removeTargetMsg', { name: confirmDeleteTarget.name })
    : ''}
  confirmLabel={t('backup.remove')}
  onConfirm={doDeleteTarget}
  onCancel={() => (confirmDeleteTarget = null)}
/>

<ConfirmDialog
  open={!!confirmDeleteSchedule}
  title={t('backup.removeScheduleTitle')}
  message={confirmDeleteSchedule
    ? t('backup.removeScheduleMsg', { name: confirmDeleteSchedule.name })
    : ''}
  confirmLabel={t('backup.remove')}
  onConfirm={doDeleteSchedule}
  onCancel={() => (confirmDeleteSchedule = null)}
/>

<ConfirmDialog
  open={!!confirmDeleteFile}
  title={t('backup.deleteFileTitle')}
  message={confirmDeleteFile ? t('backup.deleteFileMsg', { name: confirmDeleteFile.filename }) : ''}
  confirmLabel={t('backup.delete')}
  onConfirm={doDeleteFile}
  onCancel={() => (confirmDeleteFile = null)}
/>

<ConfirmDialog
  open={!!askDeleteRun}
  title={t('backup.deleteRunTitle')}
  message={t('backup.deleteRunMsg')}
  confirmLabel={t('backup.deleteRun')}
  onConfirm={doDeleteRun}
  onCancel={() => (askDeleteRun = null)}
/>

<ConfirmDialog
  open={!!askDeleteConfigState}
  title={t('backup.deleteConfigTitle')}
  message={t('backup.deleteConfigMsg')}
  confirmLabel={t('backup.delete')}
  onConfirm={doDeleteConfig}
  onCancel={() => (askDeleteConfigState = null)}
/>

<!-- Verification result dialog -->
<Dialog.Root open={!!verifyResult} onOpenChange={(v) => !v && (verifyResult = null)}>
  <Dialog.Content class="sm:max-w-md [&>*]:min-w-0">
    <Dialog.Header>
      <Dialog.Title>{t('backup.verifyTitle')}</Dialog.Title>
      <Dialog.Description>{verifyResult?.name}</Dialog.Description>
    </Dialog.Header>
    {#if verifyResult}
      <div class="space-y-3 min-w-0">
        <div class="flex items-center gap-2 text-success text-sm font-medium">
          <svg
            class="w-4 h-4"
            fill="none"
            stroke="currentColor"
            stroke-width="2.5"
            viewBox="0 0 24 24"><polyline points="20 6 9 17 4 12" /></svg
          >
          {t('backup.verifyOkText')}
        </div>
        <div class="grid grid-cols-[100px_1fr] gap-y-1 text-sm">
          <span class="text-muted-foreground">{t('common.size')}</span>
          <span class="tnum">{fmtBytes(verifyResult.size)}</span>
          <span class="text-muted-foreground">{t('backup.verifyModified')}</span>
          <span>{fmtDate(verifyResult.modified)}</span>
        </div>
        <div>
          <div class="text-xs font-medium mb-1">SHA-256</div>
          <div class="flex items-center gap-2 min-w-0">
            <code class="flex-1 min-w-0 font-mono text-xs break-all bg-muted/40 rounded px-2 py-1.5"
              >{verifyResult.sha256}</code
            >
            <Button size="xs" variant="outline" onclick={copySha}>{t('common.copy')}</Button>
          </div>
        </div>
      </div>
    {/if}
    <Dialog.Footer>
      <Button class="w-full" onclick={() => (verifyResult = null)}>{t('common.ok')}</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!--
  Restore-as-VM dialog. Replaces the old extract-only Restore
  flow: the operator picks a name + pool, the backend imports
  the archive as a new libvirt domain, and we navigate to the
  new VM's detail page on success.
-->
<Dialog.Root open={!!restoreForm} onOpenChange={(v) => !v && closeRestoreDialog()}>
  <Dialog.Content class="sm:max-w-md [&>*]:min-w-0">
    <Dialog.Header>
      <Dialog.Title>{t('backup.restoreAsVmTitle')}</Dialog.Title>
      <Dialog.Description>{t('backup.restoreAsVmDesc')}</Dialog.Description>
    </Dialog.Header>
    {#if restoreForm}
      <div class="space-y-3 min-w-0">
        <div class="text-xs text-muted-foreground min-w-0">
          {t('backup.sourceLabel')}
          {#if restoreForm.vm}
            <span class="font-medium text-foreground">{restoreForm.vm}</span>
          {/if}
          <span class="font-mono block truncate">{restoreForm.filename}</span>
        </div>
        <div class="space-y-1.5 min-w-0">
          <Label for="restore-name">{t('backup.vmNameLabel')}</Label>
          <Input
            id="restore-name"
            bind:value={restoreForm.name}
            placeholder="ubuntu-1-restored"
            disabled={restoreLoading}
            onkeydown={(e) => e.key === 'Enter' && submitRestore()}
          />
          <p class="text-xs text-muted-foreground">{t('backup.nameHint')}</p>
        </div>
        <div class="space-y-1.5 min-w-0">
          <Label for="restore-pool">{t('backup.storagePoolLabel')}</Label>
          <select
            id="restore-pool"
            bind:value={restoreForm.pool}
            disabled={restoreLoading}
            class="input w-full min-w-0 max-w-full"
          >
            {#each diskPools as p (p.name)}
              <option value={p.name}>{p.name} ({p.purpose || 'disk'})</option>
            {/each}
            {#if diskPools.length === 0}
              <option value="webkvm-disks">webkvm-disks</option>
            {/if}
          </select>
        </div>
        <div class="space-y-1.5 min-w-0">
          <Label for="restore-network">{t('backup.networkLabel')}</Label>
          <select
            id="restore-network"
            bind:value={restoreForm.network}
            disabled={restoreLoading}
            class="input w-full min-w-0 max-w-full"
          >
            <option value="">{t('backup.keepBackupNetwork')}</option>
            {#each restoreNetworks as net (net.name)}
              <option value={net.name}>{net.name}</option>
            {/each}
          </select>
        </div>
        <div class="grid grid-cols-2 gap-3 min-w-0">
          <div class="space-y-1.5 min-w-0">
            <Label for="restore-vcpus">{t('backup.vcpusLabel')}</Label>
            <Input
              id="restore-vcpus"
              type="number"
              min="1"
              max="64"
              bind:value={restoreForm.vcpus}
              placeholder={t('backup.keepOriginal')}
              disabled={restoreLoading}
            />
          </div>
          <div class="space-y-1.5 min-w-0">
            <Label for="restore-ram">{t('backup.ramMbLabel')}</Label>
            <Input
              id="restore-ram"
              type="number"
              min="512"
              step="512"
              bind:value={restoreForm.ramMB}
              placeholder={t('backup.keepOriginal')}
              disabled={restoreLoading}
            />
          </div>
        </div>
        <label class="flex items-center gap-2 text-sm cursor-pointer select-none">
          <input
            type="checkbox"
            bind:checked={restoreForm.autostart}
            disabled={restoreLoading}
            class="w-4 h-4 rounded border-border bg-background text-accent focus:ring-accent"
          />
          {t('backup.autostartLabel')}
        </label>
        {#if activeRestore}
          <ProgressBar
            value={activeRestore.pct}
            label={progressLabel(
              activeRestore.stage,
              activeRestore.stage_vars,
              activeRestore.message || t('backup.restoring'),
              t
            )}
            showValue
          />
        {/if}
      </div>
    {/if}
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={closeRestoreDialog} disabled={restoreLoading}>
        {t('common.cancel')}
      </Button>
      <Button onclick={submitRestore} disabled={restoreLoading || !restoreForm}>
        {#if restoreLoading}
          <Loader2 class="h-3.5 w-3.5 mr-1.5 animate-spin" />
          {t('backup.restoring')}
        {:else}
          {t('backup.restoreAsVmBtn')}
        {/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
