<script>
  import SearchInput from '$lib/components/SearchInput.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import { upsertTask, updateTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { events } from '$lib/stores/events.svelte.js';
  import { getRoute, navigate } from '$lib/router.svelte.js';
  import { auth } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { t } from '../lib/i18n.svelte.js';
  import { stateDotClass } from '$lib/utils/vmState.js';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import BulkActionBar from '$lib/components/BulkActionBar.svelte';
  import ErrorModal from '$lib/components/ErrorModal.svelte';
  import CredentialsModal from '$lib/components/CredentialsModal.svelte';
  import * as Dialog from '$lib/components/ui/dialog';
  import Chart from '$lib/components/Chart.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import { formatRate } from '$lib/utils/format.js';
  import {
    Plus,
    Download,
    CopyPlus,
    FileUp,
    Shield,
    Home,
    HardDrive,
    Cloud,
    Sparkles,
    AppWindow,
    Pencil,
    Trash2,
    FileCode2,
    Eye,
    EyeOff,
  } from '@lucide/svelte';

  let vms = $state([]);
  let loading = $state(true);
  let error = $state('');

  // Read initial filter state from URL query.
  const route = $derived(getRoute());
  function readQuery(key) {
    return route.query?.[key] ?? '';
  }
  let search = $state(readQuery('q'));
  let groupFilter = $state(readQuery('group') || 'all');
  let stateFilter = $state(readQuery('state') || 'all');

  // Sync filters → URL.
  $effect(() => {
    const q = new URLSearchParams();
    if (search) q.set('q', search);
    if (groupFilter && groupFilter !== 'all') q.set('group', groupFilter);
    if (stateFilter && stateFilter !== 'all') q.set('state', stateFilter);
    if (selectMode) q.set('select', '1');
    const target = '/vms' + (q.toString() ? '?' + q.toString() : '');
    if (typeof location !== 'undefined' && location.hash !== '#' + target) {
      history.replaceState(null, '', '#' + target);
    }
  });

  // Bulk selection (Phase D, reworked in Phase H: grid-only with
  // explicit select-mode toggle). When selectMode is false, clicking
  // a card navigates to the VM; when true, clicking toggles its
  // membership in selectedKeys. The toggle lives in the PageHeader
  // and is the only way to enter select mode (no mouse-only affordance).
  let selectMode = $state(readQuery('select') === '1');
  let selectedKeys = $state(new Set());

  // Auto-exit select mode when the selection is cleared, so the UI
  // doesn't stay in "bulk" mode after the user is done.
  $effect(() => {
    if (!selectMode && selectedKeys.size === 0) return;
    if (selectedKeys.size === 0) selectMode = false;
  });

  let groups = $state([]);
  let showManageGroups = $state(false);
  let newGroupName = $state('');
  let newGroupColor = $state('#7c3aed');
  let mgSaving = $state(false);
  let mgError = $state('');

  const palette = [
    '#7c3aed',
    '#3b82f6',
    '#10b981',
    '#f59e0b',
    '#ef4444',
    '#ec4899',
    '#06b6d4',
    '#84cc16',
  ];

  // Confirm dialog state
  let confirmDeleteOpen = $state(false);
  let confirmDeleteVm = $state(null);
  let confirmDeleteLoading = $state(false);

  // Bulk confirm dialog
  let confirmBulkOpen = $state(false);
  let confirmBulkAction = $state(''); // 'start' | 'shutdown' | 'forceoff' | 'delete'
  let confirmBulkLoading = $state(false);

  // Bulk tag dialog
  let showBulkTag = $state(false);
  // A VM can belong to more than one group at once, so tagging supports
  // selecting several groups in one go (toggleable chips), not just one.
  let bulkTagNames = $state(new Set());

  // Import modal state
  let showImport = $state(false);
  let importName = $state('');
  let importPool = $state('webkvm-disks');
  // Template instantiation dialog.
  let showInstantiate = $state(false);
  let instTemplates = $state([]);
  let instTemplateId = $state('');
  let showInstPass = $state(false);
  let instName = $state('');
  let instCI = $state(false);
  let instCIUser = $state('');
  let instCIPassword = $state('');
  let instCIKey = $state('');
  let instCIHostname = $state('');
  let instSaving = $state(false);
  let instNet = $state('default');
  let instNetOptions = $state([]);
  // Community appliance deploy dialog.
  let showAppliances = $state(false);
  let appliances = $state([]);
  let appNames = $state({}); // applianceId -> custom VM name
  let appCIUsers = $state({}); // applianceId -> cloud-init username
  let appCIPasswords = $state({}); // applianceId -> cloud-init password
  let showAppPass = $state({}); // applianceId -> password visibility
  let appNets = $state({}); // applianceId -> network name
  let netOptions = $state([]); // [{name, type}]
  let showDeployConfirm = $state(false);
  let deployApp = $state(null); // appliance elegido para instalar
  let showAppError = $state(false);
  let appErrorTitle = $state('');
  let appErrorMessage = $state('');
  let showAppCreds = $state(false);
  let appCreds = $state(null);
  let pendingNavId = $state(null);
  let appDeploying = $state(null); // { jobId, name, status, pct, error }
  let appPoller = $state(null);
  // Admin CRUD for the appliance catalog.
  let showAppEditor = $state(false);
  let appEditorMode = $state('create'); // 'create' | 'edit'
  let appEditId = $state('');
  let appForm = $state({
    id: '',
    name: '',
    description: '',
    category: 'cloud',
    url: '',
    format: 'qcow2',
    compression: 'none',
    vcpus: 2,
    ram_mb: 2048,
    disk_gb: 10,
    cloud_init_supported: true,
    notes: '',
    base_image_id: '',
  });
  let appSaving = $state(false);
  let appEditorError = $state('');
  // Provision script editor state.
  let appScript = $state('');
  let appScriptOrig = $state('');
  let appIsBuiltin = $state(false);
  let appScriptLoading = $state(false);
  // Read-only script viewer (operators).
  let showScriptView = $state(false);
  let viewScriptText = $state('');
  let viewScriptApp = $state(null);

  const APP_SCRIPT_TEMPLATE = `#!/bin/bash
set -e

# Runs as ROOT on the VM's first boot (cloud-init runcmd).
# {{WEBKVM_DB_PASS}} is replaced by WebKVM with a generated DB password
# (also shown in the UI pop-up after deploy).
# Output lands in /var/log/webkvm-provision.log inside the guest.

apt-get update -y
# apt-get install -y your-packages-here
`;

  // Delete confirmation (double confirmation for builtin appliances).
  let showAppDelete = $state(false);
  let appDeleteTarget = $state(null);
  let appDeleteStep = $state(1);
  let appDeleteText = $state('');
  let appDeleteError = $state('');
  let appDeleting = $state(false);
  // Quick-action menu on VM cards.
  let menuFor = $state(null); // vm id with the card menu open
  let quickBusy = $state(''); // action key while an action is running

  function toggleMenu(vmId) {
    menuFor = menuFor === vmId ? null : vmId;
  }

  // Direct per-VM entry point for group assignment — reuses the same
  // bulk-tag dialog/logic as the multi-select flow, just pre-seeded
  // with a single VM. Before this, the only way to tag a VM with a
  // group was: enter select mode, check a box, then find "Tag with
  // group" in the bulk action bar — nothing in "Manage groups" (where
  // groups are created) let you assign one directly to a VM.
  function openTagForVm(vm) {
    selectedKeys = new Set([vm.id]);
    bulkTagNames = new Set(vm.groups || []);
    showBulkTag = true;
    menuFor = null;
  }

  async function quickAction(vm, action) {
    const key = `${vm.id}:${action}`;
    quickBusy = key;
    menuFor = null;
    try {
      switch (action) {
        case 'start':
          await api.startVM(vm.id);
          toast.success(t('vms.startedName', { name: vm.alias || vm.name }));
          break;
        case 'shutdown':
          await api.shutdownVM(vm.id);
          toast.success(t('vms.shutdownSent', { name: vm.alias || vm.name }));
          break;
        case 'forceoff':
          await api.forceOffVM(vm.id);
          toast.success(t('vms.forceoffDone', { name: vm.alias || vm.name }));
          break;
        case 'clone': {
          const res = await api.cloneVM(vm.id, { name: `${vm.name}-clone` });
          toast.success(t('vms.cloned', { name: res.name || `${vm.name}-clone` }));
          if (res.id) navigate('/vms/' + res.id);
          else await loadVMs();
          return;
        }
        case 'template':
          await api.makeVMTemplate(vm.id);
          toast.success(t('vms.madeTemplate', { name: vm.alias || vm.name }));
          break;
        case 'console': {
          // The VNC console is a separate server-rendered page (not an
          // SPA route) authenticated with a short-lived, VM-scoped
          // ticket — never the session JWT. Mirrors VmDetail.svelte's
          // openConsole().
          const { vnc_ticket } = await api.getVNCTicket(vm.id);
          window.open(
            `/console/${vm.id}?vt=${encodeURIComponent(vnc_ticket)}`,
            '_blank',
            'noopener,noreferrer'
          );
          return;
        }
        case 'serial':
          // Embedded serial console lives in the VM detail page.
          navigate('/vms/' + vm.id, { query: { serial: '1' } });
          return;
      }
      await loadVMs();
    } catch (e) {
      toast.error(e.message);
    } finally {
      quickBusy = '';
    }
  }
  let importFile = $state(null);
  let importing = $state(false);
  let importProgress = $state(0);
  let importPhase = $state('');
  let importError = $state('');
  let pools = $state([]);

  // Derived filtered list (search AND group filter AND state filter).
  const filteredVms = $derived.by(() => {
    const q = search.toLowerCase().trim();
    let out = vms;
    if (groupFilter !== 'all') {
      out = out.filter((v) => Array.isArray(v.groups) && v.groups.includes(groupFilter));
    }
    if (stateFilter !== 'all') {
      out = out.filter((v) => v.state === stateFilter);
    }
    if (q) {
      out = out.filter(
        (v) =>
          v.name.toLowerCase().includes(q) ||
          (v.alias && v.alias.toLowerCase().includes(q)) ||
          (v.ip && v.ip.includes(q))
      );
    }
    return out;
  });

  // Selection should clear when the filtered list changes.
  $effect(() => {
    // Re-derive when filtered set changes.
    void filteredVms;
    const valid = new Set(filteredVms.map((v) => v.id));
    let changed = false;
    const next = new Set();
    for (const k of selectedKeys) {
      if (valid.has(k)) next.add(k);
      else changed = true;
    }
    if (changed) selectedKeys = next;
  });

  onMount(() => {
    loadVMs();
    loadGroups();
    // Subscribe to VM state events for realtime updates
    const off = events.onVmState((e) => {
      const idx = vms.findIndex((v) => v.id === e.vm_id);
      if (idx >= 0) {
        const prev = vms[idx];
        if (prev.state !== e.state) {
          vms = vms.map((v) =>
            v.id === e.vm_id ? { ...v, state: e.state, name: e.name || v.name } : v
          );
          if (e.state === 'running') loadSparklines();
        }
      }
    });
    // Subscribe to metrics for live sparkline updates.
    const offMetrics = events.onVmMetrics((e) => {
      metricsByVm = { ...metricsByVm, [e.vm_id]: e.data };
    });
    return () => {
      off();
      offMetrics();
    };
  });

  async function loadVMs() {
    loading = true;
    error = '';
    try {
      vms = await api.listVMs();
      // Fire-and-forget sparkline load; don't block the table render.
      loadSparklines();
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  async function loadGroups() {
    try {
      const res = await api.listGroups();
      groups = res.groups || [];
    } catch {
      groups = [];
    }
  }

  // Per-VM metric series for sparklines (Phase 21). Keyed by VM id.
  let metricsByVm = $state({});

  async function loadSparklines() {
    // Only request for VMs that are running; others stay empty (no chart).
    const running = vms.filter((v) => v.state === 'running');
    const updates = {};
    await Promise.all(
      running.map(async (v) => {
        try {
          const m = await api.getVMMetrics(v.id);
          updates[v.id] = m;
        } catch {
          // Don't fail the whole load on one VM.
        }
      })
    );
    metricsByVm = { ...metricsByVm, ...updates };
  }

  const last30 = (arr) => (Array.isArray(arr) ? arr.slice(-30) : []);

  // Fleet-wide sparkline aggregation: index-wise combine each running
  // VM's metric series, aligned from the most recent sample (arrays can
  // have different lengths if a VM started polling more recently).
  function sumSeries(seriesList) {
    const trimmed = seriesList.map((s) => last30(s)).filter((s) => s.length > 0);
    if (trimmed.length === 0) return [];
    const len = Math.min(...trimmed.map((s) => s.length));
    const out = [];
    for (let i = 0; i < len; i++) {
      const offset = i - len;
      let sum = 0;
      for (const s of trimmed) sum += s[s.length + offset]?.v || 0;
      out.push({ v: sum });
    }
    return out;
  }

  function avgSeries(seriesList) {
    const summed = sumSeries(seriesList);
    const count = seriesList.filter((s) => Array.isArray(s) && s.length > 0).length || 1;
    return summed.map((p) => ({ v: p.v / count }));
  }

  const runningVms = $derived(vms.filter((v) => v.state === 'running'));
  const fleetCpu = $derived(avgSeries(runningVms.map((v) => metricsByVm[v.id]?.cpu?.points)));
  const fleetRam = $derived(avgSeries(runningVms.map((v) => metricsByVm[v.id]?.ram?.points)));
  const fleetDisk = $derived(
    sumSeries([
      ...runningVms.map((v) => metricsByVm[v.id]?.disk_read?.points),
      ...runningVms.map((v) => metricsByVm[v.id]?.disk_write?.points),
    ])
  );
  const fleetNet = $derived(
    sumSeries([
      ...runningVms.map((v) => metricsByVm[v.id]?.net_rx?.points),
      ...runningVms.map((v) => metricsByVm[v.id]?.net_tx?.points),
    ])
  );
  const lastVal = (series) => (series.length ? series[series.length - 1].v : 0);

  async function openManageGroups() {
    mgError = '';
    newGroupName = '';
    newGroupColor = palette[0];
    await loadGroups();
    showManageGroups = true;
  }

  async function createGroup() {
    if (!newGroupName.trim()) {
      mgError = t('vms.nameRequired');
      return;
    }
    mgSaving = true;
    mgError = '';
    try {
      await api.createGroup({ name: newGroupName.trim(), color: newGroupColor });
      newGroupName = '';
      await loadGroups();
    } catch (e) {
      mgError = e.message;
    } finally {
      mgSaving = false;
    }
  }

  async function updateGroupColor(g) {
    try {
      await api.updateGroup(g.name, { name: g.name, color: g.color });
      await loadGroups();
    } catch (e) {
      toast.error(e.message);
    }
  }

  async function deleteGroup(g) {
    try {
      await api.deleteGroup(g.name);
      if (groupFilter === g.name) groupFilter = 'all';
      await loadGroups();
      await loadVMs();
      toast.success(`Group "${g.name}" removed`);
    } catch (e) {
      toast.error(e.message);
    }
  }

  async function doDelete() {
    if (!confirmDeleteVm) return;
    confirmDeleteLoading = true;
    try {
      await api.deleteVM(confirmDeleteVm.id);
      toast.success(t('vms.deleted'));
      confirmDeleteOpen = false;
      confirmDeleteVm = null;
      await loadVMs();
    } catch (e) {
      toast.error(e.message);
    } finally {
      confirmDeleteLoading = false;
    }
  }

  // ---- bulk actions ----
  function askBulk(action) {
    confirmBulkAction = action;
    confirmBulkOpen = true;
  }

  async function doBulk() {
    const ids = Array.from(selectedKeys);
    if (ids.length === 0) return;
    confirmBulkLoading = true;
    let succeeded = 0,
      failed = 0;
    try {
      for (const id of ids) {
        try {
          if (confirmBulkAction === 'start') await api.startVM(id);
          else if (confirmBulkAction === 'shutdown') await api.shutdownVM(id);
          else if (confirmBulkAction === 'forceoff') await api.forceOffVM(id);
          else if (confirmBulkAction === 'delete') await api.deleteVM(id);
          succeeded++;
        } catch (_) {
          failed++;
        }
      }
      const label = {
        start: 'started',
        shutdown: 'shut down',
        forceoff: 'force-offed',
        delete: 'deleted',
      }[confirmBulkAction];
      if (succeeded) toast.success(`${succeeded} VM${succeeded !== 1 ? 's' : ''} ${label}`);
      if (failed) toast.error(`${failed} failed`);
      confirmBulkOpen = false;
      selectedKeys = new Set();
      await loadVMs();
    } finally {
      confirmBulkLoading = false;
    }
  }

  async function doBulkTag() {
    if (bulkTagNames.size === 0) return;
    if (selectedKeys.size === 0) {
      // Can happen if the selection got cleared (e.g. a filter change)
      // while this dialog was open — fail loudly instead of silently
      // doing nothing, which is exactly what looked like a bug before.
      toast.error(t('vms.noVmsSelected'));
      showBulkTag = false;
      return;
    }
    const ids = Array.from(selectedKeys);
    const namesToAdd = Array.from(bulkTagNames);
    let ok = 0,
      fail = 0;
    for (const id of ids) {
      try {
        const m = await api.getVMMeta(id);
        const groups = new Set(Array.isArray(m.groups) ? m.groups : []);
        for (const name of namesToAdd) groups.add(name);
        await api.updateVMMeta(id, { groups: Array.from(groups) });
        ok++;
      } catch (_) {
        fail++;
      }
    }
    const namesLabel = namesToAdd.map((n) => `"${n}"`).join(', ');
    if (ok) toast.success(`Tagged ${ok} VM${ok !== 1 ? 's' : ''} with ${namesLabel}`);
    if (fail) toast.error(`${fail} failed`);
    showBulkTag = false;
    bulkTagNames = new Set();
    selectedKeys = new Set();
    await loadVMs();
  }

  function formatRAM(mb) {
    if (!mb) return '—';
    if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`;
    return `${mb} MB`;
  }

  function vmDiskGB(vm) {
    return (vm.disks || []).reduce((acc, d) => acc + (d.size_gb || 0), 0) || 0;
  }

  // Import modal
  async function openInstantiate() {
    showInstantiate = true;
    instName = '';
    instCI = false;
    instCIUser = '';
    instCIPassword = '';
    instCIKey = '';
    instCIHostname = '';
    instNet = 'default';
    try {
      const [r, nets] = await Promise.all([api.listTemplates(), api.listNetworks()]);
      instTemplates = r.templates || [];
      instTemplateId = instTemplates[0]?.id || '';
      instNetOptions = (nets.networks || nets || []).map((n) => ({
        name: n.name,
        type: n.mode || n.type || '',
      }));
      if (!instNetOptions.some((n) => n.name === instNet)) {
        instNet = instNetOptions[0]?.name || 'default';
      }
    } catch (e) {
      const msg = e.message || '';
      toast.error(msg);
      if (/collides with a system group/i.test(msg)) {
        appErrorTitle = 'User name not available';
        appErrorMessage = msg;
        showAppError = true;
      }
      showInstantiate = false;
    }
  }

  async function doInstantiate() {
    if (!instTemplateId || !instName.trim()) {
      toast.error(t('vms.instantiateRequired'));
      return;
    }
    if (instCI) {
      if (!instCIUser.trim()) {
        return ciFail('Username is required for cloud-init provisioning');
      }
      if (
        /^(root|daemon|bin|sys|sync|games|man|lp|mail|news|uucp|proxy|www-data|backup|list|irc|_apt|nobody|systemd-network|systemd-timesync|dhcpcd|messagebus|syslog|systemd-resolve|uuidd|tss|sshd|pollinate|tcpdump|landscape|fwupd-refresh|polkitd|sudo|adm|admin)$/i.test(
          instCIUser
        )
      ) {
        return ciFail(
          `"${instCIUser}" is a system group and would fail to provision; choose a different user name`
        );
      }
      if (!instCIPassword) {
        return ciFail('Password is required for cloud-init provisioning');
      }
      if (instCIPassword.length < 6 || instCIPassword.length > 12) {
        return ciFail('Password must be 6-12 characters');
      }
    }
    instSaving = true;
    try {
      const data = { name: instName.trim(), network: instNet };
      if (instCI) {
        data.cloud_init = {
          user: instCIUser || undefined,
          password: instCIPassword || undefined,
          ssh_key: instCIKey || undefined,
          hostname: instCIHostname || undefined,
        };
      }
      const r = await api.instantiateTemplate(instTemplateId, data);
      if (r.warning) {
        showInstantiate = false;
        appErrorTitle = 'Provisioning not possible';
        appErrorMessage = r.warning;
        showAppError = true;
        await loadVMs();
        if (r.id) navigate('/vms/' + r.id);
        return;
      }
      toast.success(t('vms.instantiated', { name: r.name || instName }));
      showInstantiate = false;
      await loadVMs();
      if (r.id) navigate('/vms/' + r.id);
    } catch (e) {
      toast.error(e.message);
    } finally {
      instSaving = false;
    }
  }

  async function openAppliances() {
    showAppliances = true;
    appDeploying = null;
    try {
      await loadAppliances();
      // Redes disponibles para el selector de despliegue.
      try {
        const nets = await api.listNetworks();
        netOptions = (nets.networks || nets || []).map((n) => ({
          name: n.name,
          type: n.mode || n.type || '',
        }));
        const def = {};
        for (const app of appliances) def[app.id] = appNets[app.id] || 'default';
        appNets = def;
      } catch (e) {
        netOptions = [];
        toast.warning('Could not load network list: ' + (e.message || 'unknown error'));
      }
    } catch (e) {
      toast.error(e.message);
      showAppliances = false;
    }
  }

  async function loadAppliances() {
    const r = await api.listAppliances();
    appliances = r.appliances || [];
    const names = {};
    for (const app of appliances) {
      names[app.id] = suggestName(app);
    }
    appNames = names;
  }

  function openAppCreate() {
    appEditorMode = 'create';
    appEditId = '';
    appForm = {
      id: '',
      name: '',
      description: '',
      category: 'cloud',
      url: '',
      format: 'qcow2',
      compression: 'none',
      vcpus: 2,
      ram_mb: 2048,
      disk_gb: 10,
      cloud_init_supported: true,
      notes: '',
      base_image_id: '',
    };
    appScript = APP_SCRIPT_TEMPLATE;
    appScriptOrig = '';
    appIsBuiltin = false;
    appEditorError = '';
    showAppEditor = true;
  }

  function openAppEdit(app) {
    appEditorMode = 'edit';
    appEditId = app.id;
    appForm = {
      id: app.id,
      name: app.name || '',
      description: app.description || '',
      category: app.category || 'cloud',
      url: app.url || '',
      format: app.format || 'qcow2',
      compression: app.compression || 'none',
      vcpus: app.vcpus || 2,
      ram_mb: app.ram_mb || 2048,
      disk_gb: app.disk_gb || 10,
      cloud_init_supported: !!app.cloud_init_supported,
      notes: app.notes || '',
      base_image_id: app.base_image_id || '',
    };
    appEditorError = '';
    showAppEditor = true;
    loadAppScript(app);
  }

  async function loadAppScript(app) {
    appScriptLoading = true;
    try {
      const r = await api.getApplianceProvision(app.id);
      appScript = r.script || '';
      appIsBuiltin = !!r.is_builtin;
      appScriptOrig = appScript;
    } catch (e) {
      appScript = '';
      appScriptOrig = '';
      appEditorError = 'Could not load provisioning script: ' + e.message;
    } finally {
      appScriptLoading = false;
    }
  }

  function restoreOriginalScript() {
    // Empty string on a builtin = fall back to the embedded default.
    appScript = '';
  }

  // Writing a script implies cloud-init provisioning: enable it
  // automatically so the field is never silently ignored.
  function scriptInputHandler(e) {
    if (e.target.value.trim() && !appForm.cloud_init_supported) {
      appForm.cloud_init_supported = true;
      toast.info('Cloud-init activado automáticamente (los scripts lo requieren)');
    }
  }

  async function openViewScript(app) {
    try {
      const r = await api.getApplianceProvision(app.id);
      viewScriptApp = app;
      viewScriptText = r.script || '(sin script de instalación)';
      showScriptView = true;
    } catch (e) {
      toast.error(e.message);
    }
  }

  async function saveAppliance() {
    appEditorError = '';
    if (!appForm.id.trim() || !appForm.name.trim() || !appForm.url.trim()) {
      appEditorError = 'ID, name and URL are required';
      return;
    }
    appSaving = true;
    try {
      const payload = {
        id: appForm.id.trim(),
        name: appForm.name.trim(),
        description: appForm.description.trim(),
        category: appForm.category,
        url: appForm.url.trim(),
        format: appForm.format,
        compression: appForm.compression,
        vcpus: Number(appForm.vcpus) || 2,
        ram_mb: Number(appForm.ram_mb) || 2048,
        disk_gb: Number(appForm.disk_gb) || 10,
        cloud_init_supported: appForm.cloud_init_supported,
        notes: appForm.notes.trim(),
        base_image_id: appForm.base_image_id.trim(),
      };
      // Pointer semantics on the backend: omit = keep current; "" =
      // clear/restore embedded default for builtins.
      if (appEditorMode === 'create') {
        if (appScript.trim()) payload.provision_script = appScript;
      } else if (appScript !== appScriptOrig) {
        payload.provision_script = appScript;
      }
      if (appEditorMode === 'create') {
        await api.createAppliance(payload);
        toast.success('Appliance added');
      } else {
        await api.updateAppliance(appEditId, payload);
        toast.success('Appliance updated');
      }
      showAppEditor = false;
      await loadAppliances();
    } catch (e) {
      appEditorError = e.message;
    } finally {
      appSaving = false;
    }
  }

  function askDeleteApp(app) {
    appDeleteTarget = app;
    appDeleteStep = 1;
    appDeleteText = '';
    showAppDelete = true;
  }

  // Delete requires a double confirmation (two clicks) plus typing the
  // appliance name to confirm. Builtin appliances show the two-step
  // progression explicitly.
  async function confirmDeleteApp() {
    if (!appDeleteTarget) return;
    if (appDeleteStep === 1) {
      // First confirmation: advance to the second step. The button stays
      // disabled until the name is typed, so this is a genuine first click.
      appDeleteStep = 2;
      return;
    }
    // Second confirmation: require the exact appliance name.
    if (appDeleteText.trim() !== appDeleteTarget.name) {
      appDeleteError = 'Type the appliance name to confirm';
      return;
    }
    appDeleting = true;
    appDeleteError = '';
    try {
      await api.deleteAppliance(appDeleteTarget.id);
      toast.success('Appliance deleted');
      showAppDelete = false;
      appDeleteTarget = null;
      await loadAppliances();
    } catch (e) {
      appDeleteError = e.message;
    } finally {
      appDeleting = false;
    }
  }

  // suggestName turns an appliance id into a friendly default VM name,
  // e.g. "ubuntu-24.04" -> "ubuntu", "openwrt-23.05" -> "openwrt".
  function suggestName(app) {
    return app.id.split(/[.\-_]/)[0] || app.id;
  }

  function fmtBytes(n) {
    if (!n) return '';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024;
      i++;
    }
    return `${n.toFixed(n >= 10 || i === 0 ? 0 : 1)} ${u[i]}`;
  }

  // applianceGroups groups the catalog by category for a friendlier UI.
  function applianceGroups(list) {
    const order = ['app', 'cloud', 'nas', 'router', 'home'];
    const groups = new Map();
    for (const a of list) {
      if (!groups.has(a.category)) groups.set(a.category, []);
      groups.get(a.category).push(a);
    }
    const out = [];
    for (const cat of order) {
      if (groups.has(cat)) out.push({ category: cat, items: groups.get(cat) });
    }
    for (const [cat, items] of groups) {
      if (!order.includes(cat)) out.push({ category: cat, items });
    }
    return out;
  }

  function categoryIcon(cat) {
    return (
      {
        router: Shield,
        home: Home,
        nas: HardDrive,
        cloud: Cloud,
        app: AppWindow,
      }[cat] || Sparkles
    );
  }

  // Validación cloud-init: toast + ErrorModal (el toast puede quedar
  // tapado por el overlay del diálogo; el modal siempre se ve).
  function ciFail(msg) {
    toast.error(msg);
    appErrorTitle = 'Provisioning not possible';
    appErrorMessage = msg;
    showAppError = true;
  }

  function openDeployConfirm(app) {
    deployApp = app;
    if (!appNames[app.id]) appNames[app.id] = suggestName(app);
    if (!appNets[app.id]) appNets[app.id] = 'default';
    showDeployConfirm = true;
  }

  async function deployAppliance(app, vmName) {
    try {
      const body = { name: vmName };
      // If the appliance supports cloud-init and the user provided a user
      // + password, provision the guest (serial console access + guest agent).
      if (app.cloud_init_supported) {
        const user = (appCIUsers[app.id] || '').trim();
        const pass = appCIPasswords[app.id] || '';
        if (!user) return ciFail('Username is required for cloud-init provisioning');
        if (
          /^(root|daemon|bin|sys|sync|games|man|lp|mail|news|uucp|proxy|www-data|backup|list|irc|_apt|nobody|systemd-network|systemd-timesync|dhcpcd|messagebus|syslog|systemd-resolve|uuidd|tss|sshd|pollinate|tcpdump|landscape|fwupd-refresh|polkitd|sudo|adm|admin)$/i.test(
            user
          )
        ) {
          return ciFail(
            `"${user}" is a system group and would fail to provision; choose a different user name`
          );
        }
        if (!pass) return ciFail('Password is required for cloud-init provisioning');
        if (pass.length < 6) return ciFail('Password must be at least 6 characters');
        if (pass.length > 12) return ciFail('Password must be at most 12 characters');
        body.cloud_init = {
          user,
          password: pass,
          hostname: vmName || app.id,
        };
      }
      body.network = (appNets[app.id] || 'default').trim();
      const r = await api.deployAppliance(app.id, body);
      showDeployConfirm = false;
      deployApp = null;
      appDeploying = {
        jobId: r.job_id,
        name: vmName || app.id,
        status: 'queued',
        pct: 0,
        error: '',
      };
      if (appPoller) clearInterval(appPoller);
      appPoller = setInterval(pollApplianceJob, 1000);
    } catch (e) {
      const msg = e.message || '';
      toast.error(msg);
      if (/system group|password is required|at most 12/i.test(msg)) {
        appErrorTitle = 'Provisioning not possible';
        appErrorMessage = msg;
        showAppError = true;
      }
    }
  }

  async function pollApplianceJob() {
    if (!appDeploying?.jobId) {
      if (appPoller) clearInterval(appPoller);
      appPoller = null;
      return;
    }
    try {
      const job = await api.getDownloadJob(appDeploying.jobId);
      appDeploying = {
        jobId: job.id,
        name: appDeploying.name,
        status: job.status,
        pct: Math.round(job.progress || 0),
        error: job.error || '',
      };
      if (job.status === 'completed' || job.status === 'error') {
        if (appPoller) clearInterval(appPoller);
        appPoller = null;
        if (job.status === 'completed') {
          const name = appDeploying.name;
          appDeploying = null;
          showAppliances = false;
          toast.success(t('vms.applianceDeployed', { name }));
          await loadVMs();
          const found = vms.find((v) => v.name === name);
          if (found) {
            // Show the app credentials pop-up (if any) before navigating.
            try {
              const meta = await api.getVMMeta(found.id);
              if (meta && meta.app_info) {
                appCreds = JSON.parse(meta.app_info);
                pendingNavId = found.id;
                showAppCreds = true;
                return;
              }
            } catch {
              /* no metadata — fall through to navigation */
            }
            navigate('/vms/' + found.id);
          }
        } else {
          toast.error(job.error || t('vms.applianceFailed'), { duration: 8000 });
          appDeploying = { ...appDeploying, status: 'error', error: job.error || '' };
        }
      }
    } catch {
      // transient; keep polling
    }
  }

  async function openImport() {
    showImport = true;
    importError = '';
    try {
      pools = ((await api.listPools()) || []).filter((p) => p.purpose !== 'iso');
      if (pools.length > 0 && !pools.find((p) => p.name === importPool)) {
        importPool = pools[0].name;
      }
    } catch (e) {
      importError = t('vms.couldNotLoadPools', { error: e.message });
    }
  }

  async function doImport() {
    if (!importFile) {
      importError = t('vms.pickFile');
      return;
    }
    importing = true;
    importError = '';
    importProgress = 0;
    importPhase = t('vms.uploading');
    const taskId = 'import:' + importFile.name;
    upsertTask({
      id: taskId,
      kind: 'import',
      title: importFile.name,
      pct: 0,
      message: t('vms.uploading'),
      status: 'running',
    });
    try {
      const res = await api.importVM(importFile, importName, importPool, (pct) => {
        importProgress = pct;
        if (pct >= 100) importPhase = t('vms.processingOnServer');
        updateTask(taskId, {
          pct,
          message: pct >= 100 ? t('vms.processingOnServer') : t('vms.uploading'),
        });
      });
      finishTask(
        taskId,
        'success',
        res?.name ? t('vms.importedAs', { name: res.name }) : t('vms.imported'),
        100
      );
      if (res && res.name) {
        if (res.requested_name && res.requested_name !== res.name) {
          toast.warning(t('vms.alreadyExisted', { requested: res.requested_name, name: res.name }));
        } else if (importName && importName !== res.name) {
          toast.warning(t('vms.nameConflict', { name: res.name }));
        } else {
          toast.success(t('vms.importedAs', { name: res.name }));
        }
        // Surface non-fatal server warnings (typically a
        // CDROM ISO that was not bundled with the archive,
        // so the VM is defined but cannot start until the
        // user uploads the ISO). These are sticky so the
        // operator actually notices them.
        if (Array.isArray(res.warnings) && res.warnings.length > 0) {
          for (const w of res.warnings) {
            toast.warning(w, { duration: 0 });
          }
        }
      } else {
        toast.success(t('vms.imported'));
      }
      showImport = false;
      importFile = null;
      importName = '';
      importProgress = 0;
      importPhase = '';
      await loadVMs();
    } catch (e) {
      finishTask(taskId, 'error', e.message, importProgress || 0);
      importError = e.message;
    } finally {
      importing = false;
    }
  }
</script>

<div class="p-4 sm:p-6 max-w-6xl">
  <PageHeader
    title={t('vms.title')}
    subtitle={`${vms.length} ${vms.length === 1 ? t('vms.machine') : t('vms.machines')}`}
  />

  {#if !loading && vms.length > 0}
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-4">
      <StatCard label={t('vms.fleetCpu')} value={`${lastVal(fleetCpu).toFixed(0)}%`}>
        {#snippet chart()}
          <Chart points={fleetCpu} yMax={100} height={28} strokeWidth={1} fillOpacity={0.15} />
        {/snippet}
      </StatCard>
      <StatCard label={t('vms.fleetRam')} value={`${lastVal(fleetRam).toFixed(0)}%`}>
        {#snippet chart()}
          <Chart
            points={fleetRam}
            yMax={100}
            height={28}
            strokeWidth={1}
            fillOpacity={0.15}
            color="var(--success)"
          />
        {/snippet}
      </StatCard>
      <StatCard label={t('vms.fleetDisk')} value={formatRate(lastVal(fleetDisk))}>
        {#snippet chart()}
          <Chart
            points={fleetDisk}
            height={28}
            strokeWidth={1}
            fillOpacity={0.15}
            color="var(--warning)"
          />
        {/snippet}
      </StatCard>
      <StatCard label={t('vms.fleetNet')} value={formatRate(lastVal(fleetNet))}>
        {#snippet chart()}
          <Chart
            points={fleetNet}
            height={28}
            strokeWidth={1}
            fillOpacity={0.15}
            color="var(--info, var(--accent))"
          />
        {/snippet}
      </StatCard>
    </div>
  {/if}

  <!-- Toolbar: kept on its own row so it never overlaps the title/subtitle -->
  <div class="flex flex-wrap items-center gap-2 -mt-3 mb-4">
    <button
      onclick={() => (selectMode = !selectMode)}
      class="px-3 h-8 inline-flex items-center gap-1.5 border rounded-md text-xs font-medium transition-colors {selectMode
        ? 'border-accent bg-accent/15 text-accent'
        : 'border-border text-muted-foreground hover:text-foreground hover:bg-muted'}"
      aria-pressed={selectMode}
      title={t('vms.selectModeTitle')}
    >
      <svg
        class="w-3.5 h-3.5"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        viewBox="0 0 24 24"
      >
        <rect x="3" y="3" width="18" height="18" rx="2" />
        {#if selectMode}
          <polyline points="9 12 11 14 15 10" stroke-linecap="round" stroke-linejoin="round" />
        {/if}
      </svg>
      {t('vms.selectMode')}
    </button>
    <SearchInput bind:value={search} placeholder={t('vms.searchPlaceholder')} class="w-64" />
    <Button variant="outline" onclick={openManageGroups}>{t('vms.manageGroups')}</Button>
    <Button variant="outline" onclick={openImport}>
      <FileUp class="w-3.5 h-3.5 mr-1.5" />
      {t('vms.importVm')}
    </Button>
    <Button variant="outline" onclick={openInstantiate}>
      <CopyPlus class="w-3.5 h-3.5 mr-1.5" />
      {t('vms.fromTemplate')}
    </Button>
    <Button variant="outline" onclick={openAppliances}>
      <Download class="w-3.5 h-3.5 mr-1.5" />
      {t('vms.communityApps')}
    </Button>
    <Button onclick={() => navigate('/vms/new')}>
      <Plus class="w-3.5 h-3.5 mr-1.5" />
      {t('vms.create')}
    </Button>
  </div>

  <BulkActionBar
    count={selectedKeys.size}
    actions={[
      ...(auth.canMutate()
        ? [
            { key: 'start', label: t('vms.start'), onClick: () => askBulk('start') },
            { key: 'shutdown', label: t('vms.shutdown'), onClick: () => askBulk('shutdown') },
            { key: 'forceoff', label: t('vms.forceOff'), onClick: () => askBulk('forceoff') },
          ]
        : []),
      {
        key: 'tag',
        label: t('vms.tagWithGroup'),
        onClick: () => (
          (showBulkTag = true),
          (bulkTagNames = new Set(groupFilter !== 'all' ? [groupFilter] : []))
        ),
      },
      ...(auth.isAdmin()
        ? [
            {
              key: 'delete',
              label: t('common.delete'),
              variant: 'destructive',
              onClick: () => askBulk('delete'),
            },
          ]
        : []),
    ]}
    onClear={() => (selectedKeys = new Set())}
  />

  {#if groups.length > 0 || stateFilter !== 'all'}
    <div class="flex items-center gap-1.5 flex-wrap mb-4">
      <button
        onclick={() => ((groupFilter = 'all'), (stateFilter = 'all'))}
        class="text-xs px-2.5 py-1 rounded-full border transition-colors {groupFilter === 'all' &&
        stateFilter === 'all'
          ? 'border-accent bg-accent/15 text-accent'
          : 'border-border text-muted-foreground hover:text-foreground hover:border-border-hover'}"
      >
        All <span class="text-[10px] opacity-60">({vms.length})</span>
      </button>
      {#each [{ v: 'running', c: 'bg-status-running', l: 'running' }, { v: 'shutoff', c: 'bg-status-shutoff', l: 'shutoff' }, { v: 'paused', c: 'bg-status-paused', l: 'paused' }, { v: 'crashed', c: 'bg-status-crashed', l: 'crashed' }] as s}
        <button
          onclick={() => (stateFilter = stateFilter === s.v ? 'all' : s.v)}
          class="text-xs px-2.5 py-1 rounded-full border transition-colors {stateFilter === s.v
            ? 'border-foreground text-foreground bg-muted'
            : 'border-border text-muted-foreground hover:text-foreground'}"
        >
          <span class="inline-block w-1.5 h-1.5 rounded-full mr-1.5 {s.c}"></span>
          {s.l}
          <span class="text-[10px] opacity-60">({vms.filter((v) => v.state === s.v).length})</span>
        </button>
      {/each}
      {#if groups.length > 0}
        <span class="text-xs text-muted-foreground mx-1">|</span>
        {#each groups as g}
          <button
            onclick={() => (groupFilter = groupFilter === g.name ? 'all' : g.name)}
            class="text-xs px-2.5 py-1 rounded-full border transition-colors {groupFilter === g.name
              ? 'border-foreground text-foreground'
              : 'border-border text-muted-foreground hover:text-foreground'}"
            style={groupFilter === g.name
              ? `background-color: ${g.color}25; border-color: ${g.color};`
              : ''}
          >
            <span
              class="inline-block w-1.5 h-1.5 rounded-full mr-1.5"
              style="background-color: {g.color}"
            ></span>
            {g.name} <span class="text-[10px] opacity-60">({g.member_count})</span>
          </button>
        {/each}
      {/if}
    </div>
  {/if}

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if loading}
    <div class="flex items-center justify-center py-24"><Spinner size="lg" /></div>
  {:else if filteredVms.length === 0}
    <div class="border border-border rounded-lg bg-card p-12 text-center">
      <svg
        class="w-12 h-12 mx-auto mb-3 text-muted-foreground/40"
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
        viewBox="0 0 24 24"
      >
        <rect x="3" y="4" width="18" height="12" rx="2" /><path d="M8 20h8M12 16v4" />
      </svg>
      <p class="text-muted-foreground text-sm mb-4">
        {t('vms.emptyDesc')}
      </p>
      <div class="flex items-center justify-center gap-2 flex-wrap">
        <Button onclick={() => navigate('/vms/new')}>
          <Plus class="w-3.5 h-3.5 mr-1.5" />
          {t('vms.create')}
        </Button>
        <Button variant="outline" onclick={openAppliances}>
          <Download class="w-3.5 h-3.5 mr-1.5" />
          {t('vms.communityApps')}
        </Button>
        <Button variant="outline" onclick={openImport}>
          <FileUp class="w-3.5 h-3.5 mr-1.5" />
          {t('vms.importVm')}
        </Button>
      </div>
    </div>
  {:else}
    <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-4">
      {#each filteredVms as vm (vm.id)}
        {@const isSelected = selectedKeys.has(vm.id)}
        {@const metrics = metricsByVm[vm.id]}
        {@const cpuPts = last30(metrics?.cpu?.points)}
        {@const ramPts = last30(metrics?.ram?.points)}
        <div
          role="button"
          tabindex="0"
          onclick={() => {
            if (selectMode) {
              const next = new Set(selectedKeys);
              if (next.has(vm.id)) next.delete(vm.id);
              else next.add(vm.id);
              selectedKeys = next;
            } else {
              navigate(`/vms/${vm.id}`);
            }
          }}
          onkeydown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault();
              if (!selectMode) navigate(`/vms/${vm.id}`);
            }
          }}
          aria-pressed={selectMode ? isSelected : undefined}
          class="group relative text-left border rounded-lg bg-card transition-[border-color,box-shadow,transform] duration-200 ease-out {(
            selectMode ? isSelected : false
          )
            ? 'border-accent ring-1 ring-accent/40'
            : 'border-border hover:border-border-hover hover:-translate-y-0.5 hover:shadow-md'}"
        >
          <div
            class="aspect-video w-full bg-gradient-to-br from-muted to-background relative overflow-hidden"
          >
            {#if vm.cover}
              <img src={vm.cover} alt="" class="w-full h-full object-cover" />
            {:else}
              <div
                class="absolute inset-0 flex items-center justify-center text-5xl font-bold text-muted-foreground/30 select-none"
              >
                {(vm.alias || vm.name).charAt(0).toUpperCase()}
              </div>
            {/if}
            <div
              class="absolute top-2 left-2 inline-flex items-center gap-1.5 px-1.5 py-0.5 rounded bg-black/50 text-white text-[10px] uppercase tracking-wider backdrop-blur"
            >
              <span class="w-1.5 h-1.5 rounded-full {stateDotClass(vm.state)}"></span>
              {vm.state}
            </div>
            {#if selectMode}
              <div
                class="absolute top-2 right-2 w-5 h-5 rounded border-2 flex items-center justify-center transition-colors {isSelected
                  ? 'bg-accent border-accent'
                  : 'bg-black/40 border-white/70'}"
              >
                {#if isSelected}
                  <svg
                    class="w-3 h-3 text-white"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="3"
                    viewBox="0 0 24 24"
                    ><polyline
                      points="5 12 10 17 19 7"
                      stroke-linecap="round"
                      stroke-linejoin="round"
                    /></svg
                  >
                {/if}
              </div>
            {/if}
          </div>
          <div class="p-3 space-y-2">
            <div class="flex items-center justify-between gap-2">
              <div class="font-medium text-sm truncate min-w-0">{vm.alias || vm.name}</div>
              <div class="flex items-center gap-1 shrink-0">
                {#if vm.state === 'running' && vm.ip}
                  <span class="font-mono text-[10px] text-accent">{vm.ip}</span>
                {/if}
                <div class="relative">
                  <button
                    type="button"
                    aria-label={t('vms.quickActions')}
                    class="p-1 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
                    onclick={(e) => {
                      e.stopPropagation();
                      toggleMenu(vm.id);
                    }}
                  >
                    <svg class="w-4 h-4" fill="currentColor" viewBox="0 0 24 24">
                      <circle cx="5" cy="12" r="1.6" />
                      <circle cx="12" cy="12" r="1.6" />
                      <circle cx="19" cy="12" r="1.6" />
                    </svg>
                  </button>
                  {#if menuFor === vm.id}
                    <div
                      class="absolute right-0 top-full z-30 mt-1 w-44 rounded-lg border border-border bg-popover text-popover-foreground shadow-lg p-1"
                      onclick={(e) => e.stopPropagation()}
                    >
                      {#if vm.state === 'shutoff'}
                        <button
                          type="button"
                          class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                          onclick={() => quickAction(vm, 'start')}
                        >
                          <span class="w-1.5 h-1.5 rounded-full bg-success"></span>
                          {quickBusy === `${vm.id}:start` ? t('vms.starting') : t('vms.start')}
                        </button>
                      {:else}
                        <button
                          type="button"
                          class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                          onclick={() => quickAction(vm, 'shutdown')}
                        >
                          <span class="w-1.5 h-1.5 rounded-full bg-warning"></span>
                          {t('vms.shutdown')}
                        </button>
                        <button
                          type="button"
                          class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted text-destructive flex items-center gap-2"
                          onclick={() => quickAction(vm, 'forceoff')}
                        >
                          <span class="w-1.5 h-1.5 rounded-full bg-destructive"></span>
                          {t('vms.forceOff')}
                        </button>
                      {/if}
                      <button
                        type="button"
                        class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                        onclick={() => quickAction(vm, 'console')}
                      >
                        {t('vms.openConsole')}
                      </button>
                      <button
                        type="button"
                        class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                        onclick={() => quickAction(vm, 'serial')}
                      >
                        Serial Console
                      </button>
                      <button
                        type="button"
                        class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                        onclick={() => quickAction(vm, 'clone')}
                      >
                        {t('vms.clone')}
                      </button>
                      <button
                        type="button"
                        class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                        onclick={() => openTagForVm(vm)}
                      >
                        {t('vms.tagWithGroup')}
                      </button>
                      {#if vm.state === 'shutoff'}
                        <button
                          type="button"
                          class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                          onclick={() => quickAction(vm, 'template')}
                        >
                          {t('vms.makeTemplateShort')}
                        </button>
                      {/if}
                      <button
                        type="button"
                        class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                        onclick={() => navigate(`/vms/${vm.id}`)}
                      >
                        {t('vms.details')}
                      </button>
                    </div>
                  {/if}
                </div>
              </div>
            </div>
            <div class="flex items-center justify-between gap-2 text-xs text-muted-foreground tnum">
              <span class="flex items-center gap-1.5 min-w-0">
                <span class="truncate min-w-0">{vm.vcpus} vCPU · {formatRAM(vm.ram_mb)}</span>
                {#if vmDiskGB(vm)}
                  <span class="inline-flex items-center gap-1 shrink-0">
                    <HardDrive class="w-3 h-3" />
                    {vmDiskGB(vm)} GB
                  </span>
                {/if}
              </span>
            </div>
            {#if vm.state === 'running' && (cpuPts.length > 0 || ramPts.length > 0)}
              <div class="grid grid-cols-1 sm:grid-cols-2 gap-2 pt-1.5 border-t border-border/50">
                <div>
                  <div
                    class="flex items-center justify-between text-[10px] text-muted-foreground tnum mb-0.5"
                  >
                    <span>{t('vms.cpu')}</span>
                    <span>{cpuPts.length ? cpuPts[cpuPts.length - 1].v.toFixed(0) : 0}%</span>
                  </div>
                  <Chart
                    points={cpuPts}
                    yMax={100}
                    width={80}
                    height={22}
                    strokeWidth={1}
                    fillOpacity={0.2}
                  />
                </div>
                <div>
                  <div
                    class="flex items-center justify-between text-[10px] text-muted-foreground tnum mb-0.5"
                  >
                    <span>{t('common.ram')}</span>
                    <span>{ramPts.length ? ramPts[ramPts.length - 1].v.toFixed(0) : 0}%</span>
                  </div>
                  <Chart
                    points={ramPts}
                    yMax={100}
                    width={80}
                    height={22}
                    strokeWidth={1}
                    fillOpacity={0.2}
                    color="var(--success)"
                  />
                </div>
              </div>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Delete confirmation -->
<ConfirmDialog
  bind:open={confirmDeleteOpen}
  title={t('vms.deleteConfirmTitle', { name: confirmDeleteVm?.name || '' })}
  description={t('vms.deleteConfirmDesc')}
  confirmLabel={t('common.delete')}
  variant="destructive"
  loading={confirmDeleteLoading}
  onConfirm={doDelete}
/>

<!-- Bulk action confirmation -->
<ConfirmDialog
  bind:open={confirmBulkOpen}
  title={confirmBulkAction === 'delete'
    ? t('vms.bulkDeleteTitle')
    : t('vms.bulkConfirmTitle', { n: selectedKeys.size, action: confirmBulkAction || '' })}
  description={confirmBulkAction === 'delete'
    ? t('vms.bulkDeleteDesc')
    : {
        start: t('vms.bulkStartDesc'),
        shutdown: t('vms.bulkShutdownDesc'),
        forceoff: t('vms.bulkForceoffDesc'),
      }[confirmBulkAction] || ''}
  confirmLabel={confirmBulkAction === 'delete' ? t('vms.deleteAll') : t('vms.apply')}
  variant={confirmBulkAction === 'delete' ? 'destructive' : 'default'}
  loading={confirmBulkLoading}
  onConfirm={doBulk}
/>

<!-- Bulk tag dialog -->
<Dialog.Root open={showBulkTag} onOpenChange={(v) => (showBulkTag = v)}>
  <Dialog.Content>
    <Dialog.Header>
      <Dialog.Title>{t('vms.tagBulkTitle', { n: selectedKeys.size })}</Dialog.Title>
      <Dialog.Description>{t('vms.addToGroupTitle')}</Dialog.Description>
    </Dialog.Header>
    <div class="py-2 space-y-2">
      <span class="text-sm font-medium block">{t('vms.groupNameLabel')}</span>
      {#if groups.length === 0}
        <p class="text-sm text-muted-foreground">{t('vms.noGroupsCreateFirst')}</p>
        <Button
          size="sm"
          variant="outline"
          onclick={() => {
            showBulkTag = false;
            openManageGroups();
          }}
        >
          {t('vms.manageGroups')}
        </Button>
      {:else}
        <!-- Tagging can only pick existing groups (not free text) — a
             typed name that doesn't exactly match a registered group
             silently became an orphaned tag nothing else recognized.
             A VM can belong to more than one group, so each chip toggles
             independently — this adds every checked group, it doesn't
             replace whatever groups a VM already has. -->
        <div class="flex flex-wrap gap-1.5">
          {#each groups as g (g.name)}
            {@const active = bulkTagNames.has(g.name)}
            <button
              onclick={() => {
                const next = new Set(bulkTagNames);
                if (active) next.delete(g.name);
                else next.add(g.name);
                bulkTagNames = next;
              }}
              type="button"
              class="text-xs px-2.5 py-1 rounded-full border transition-colors {active
                ? 'border-transparent text-white'
                : ''}"
              style={active
                ? `background-color: ${g.color}`
                : `border-color: ${g.color}40; color: ${g.color}`}
            >
              {g.name}
            </button>
          {/each}
        </div>
      {/if}
    </div>
    <Dialog.Footer>
      <Button variant="outline" onclick={() => (showBulkTag = false)}>Cancel</Button>
      <Button disabled={bulkTagNames.size === 0} onclick={doBulkTag}>Tag</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Import dialog -->
<Dialog.Root bind:open={showImport}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vms.importTitle')}</Dialog.Title>
      <Dialog.Description>{t('vms.importDesc')}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      <div>
        <label for="import-file" class="block text-sm font-medium mb-1.5"
          >{t('vms.backupOrOva')}</label
        >
        <Input
          id="import-file"
          type="file"
          accept=".tar.gz,.tgz,.tar.zst,.zst,.ova"
          onchange={(e) => (importFile = e.target.files?.[0] || null)}
        />
        {#if importFile}
          <p class="text-xs text-muted-foreground mt-1.5">
            {importFile.name} ({(importFile.size / 1024 / 1024).toFixed(1)} MB)
          </p>
        {/if}
      </div>
      <div>
        <label for="import-name" class="block text-sm font-medium mb-1.5"
          >{t('vms.newVmName')}</label
        >
        <Input id="import-name" bind:value={importName} placeholder={t('vms.leaveEmpty')} />
      </div>
      <div>
        <label for="import-pool" class="block text-sm font-medium mb-1.5"
          >{t('vms.storagePoolLabel')}</label
        >
        <select
          id="import-pool"
          bind:value={importPool}
          class="input"
          disabled={pools.length === 0}
        >
          {#if pools.length === 0}<option value="webkvm-disks">webkvm-disks</option>{/if}
          {#each pools as p}<option value={p.name}>{p.name}</option>{/each}
        </select>
      </div>
      {#if importError}
        <p class="text-sm text-destructive">{importError}</p>
      {/if}
      {#if importing}
        <div class="bg-muted/30 rounded-md p-3 border border-border">
          <ProgressBar
            value={importProgress}
            label={importPhase || t('vms.uploading')}
            showValue
            size="sm"
          />
        </div>
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showImport = false)} disabled={importing}
        >{t('common.cancel')}</Button
      >
      <Button onclick={doImport} disabled={importing || !importFile}>
        {#if importing}<Spinner size="sm" color="text-white" />{:else}{t('vms.importVm')}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Instantiate template dialog -->
<Dialog.Root bind:open={showInstantiate}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vms.instantiateTitle')}</Dialog.Title>
      <Dialog.Description>{t('vms.instantiateDesc')}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      {#if instTemplates.length === 0}
        <div class="text-center py-6">
          <p class="text-sm text-muted-foreground mb-3">{t('vms.noTemplates')}</p>
          <ol
            class="text-sm text-muted-foreground text-left space-y-1.5 list-decimal list-inside mx-auto w-fit"
          >
            <li>{t('vms.templateStep1')}</li>
            <li>{t('vms.templateStep2')}</li>
            <li>{t('vms.templateStep3')}</li>
          </ol>
        </div>
      {:else}
        <div class="space-y-1.5">
          <Label for="inst-template">{t('vms.templateLabel')}</Label>
          <select id="inst-template" bind:value={instTemplateId} class="input w-full">
            {#each instTemplates as tmpl (tmpl.id)}
              <option value={tmpl.id}
                >{tmpl.alias || tmpl.name} ({tmpl.vcpus} vCPU · {tmpl.ram_mb} MB)</option
              >
            {/each}
          </select>
        </div>
        <div class="space-y-1.5">
          <Label for="inst-name">{t('vms.newVmName')}</Label>
          <Input id="inst-name" bind:value={instName} placeholder="my-vm" />
        </div>
        <div class="space-y-1.5">
          <Label for="inst-net">{t('vmDetail.networkLabel')}</Label>
          <select id="inst-net" bind:value={instNet} class="input w-full">
            {#each instNetOptions as n (n.name)}
              <option value={n.name}>{n.name}{n.type ? ' (' + n.type + ')' : ''}</option>
            {/each}
          </select>
        </div>

        <label class="flex items-center gap-2 text-sm cursor-pointer select-none">
          <input type="checkbox" bind:checked={instCI} class="w-4 h-4 rounded border-border" />
          {t('vmCreate.cloudInitEnable')}
        </label>
        {#if instCI}
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label class="text-xs text-muted-foreground">{t('vmCreate.cloudInitUser')}</label>
              <Input bind:value={instCIUser} placeholder="webkvm" class="w-full" />
            </div>
            <div>
              <label class="text-xs text-muted-foreground">Password *</label>
              <div class="relative">
                <Input
                  bind:value={instCIPassword}
                  type={showInstPass ? 'text' : 'password'}
                  placeholder="6-12 characters"
                  minlength="6"
                  maxlength="12"
                  class="w-full pr-10"
                />
                <button
                  type="button"
                  onclick={() => (showInstPass = !showInstPass)}
                  class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground p-1 rounded"
                  aria-label={showInstPass ? t('login.hidePassword') : t('login.showPassword')}
                  aria-pressed={showInstPass}
                >
                  {#if showInstPass}<EyeOff class="w-4 h-4" />{:else}<Eye class="w-4 h-4" />{/if}
                </button>
              </div>
            </div>
            <div>
              <label class="text-xs text-muted-foreground">{t('vmCreate.cloudInitHostname')}</label>
              <Input bind:value={instCIHostname} placeholder="my-vm" class="w-full" />
            </div>
            <div class="col-span-2">
              <label class="text-xs text-muted-foreground">{t('vmCreate.cloudInitSSHKey')}</label>
              <textarea
                bind:value={instCIKey}
                class="input w-full font-mono text-xs"
                rows="3"
                placeholder="ssh-ed25519 AAAA..."
              ></textarea>
            </div>
          </div>
        {/if}
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showInstantiate = false)}
        >{t('common.cancel')}</Button
      >
      <Button onclick={doInstantiate} disabled={instSaving || !instTemplateId || !instName.trim()}>
        {#if instSaving}<Spinner size="sm" color="text-white" />{:else}{t('vms.instantiate')}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Community appliances dialog -->
<Dialog.Root bind:open={showAppliances}>
  <Dialog.Content class="sm:max-w-2xl">
    <Dialog.Header>
      <Dialog.Title>{t('vms.appliancesTitle')}</Dialog.Title>
      <Dialog.Description>{t('vms.appliancesDesc')}</Dialog.Description>
    </Dialog.Header>

    {#if auth.isAdmin() && !appDeploying}
      <div class="mb-3 flex justify-end">
        <Button size="sm" variant="outline" onclick={openAppCreate}>
          <Plus class="w-3.5 h-3.5 mr-1.5" />
          Add appliance
        </Button>
      </div>
    {/if}

    {#if appDeploying}
      <div class="space-y-3 border border-border rounded-lg p-4 bg-background">
        <div class="flex items-center gap-2 text-sm font-medium">
          <Spinner size="xs" />
          {t('vms.deployingAppliance', { name: appDeploying.name })}
        </div>
        {#if appDeploying.status !== 'error'}
          <ProgressBar
            value={appDeploying.status === 'downloading' || appDeploying.status === 'processing'
              ? appDeploying.pct
              : undefined}
            label={appDeploying.status === 'queued'
              ? t('vms.deployQueued')
              : appDeploying.status === 'downloading'
                ? t('vms.deployDownloading')
                : appDeploying.status === 'processing'
                  ? t('vms.deployProcessing')
                  : ''}
            showValue
          />
        {:else}
          <p class="text-sm text-destructive">{appDeploying.error}</p>
        {/if}
      </div>
    {:else}
      <div class="max-h-[60vh] overflow-y-auto pr-1 space-y-4">
        {#if appliances.length === 0}
          <p class="text-sm text-muted-foreground">{t('vms.noAppliances')}</p>
        {:else}
          {#each applianceGroups(appliances) as group (group.category)}
            {@const CatIcon = categoryIcon(group.category)}
            <div>
              <div
                class="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2"
              >
                <CatIcon class="w-3.5 h-3.5" />
                {t('vms.cat_' + group.category)}
              </div>
              <div class="space-y-2">
                {#each group.items as app (app.id)}
                  <div class="border border-border rounded-lg p-3 bg-background">
                    <div class="flex items-start justify-between gap-3">
                      <div class="min-w-0 flex-1">
                        <div class="text-sm font-medium">{app.name}</div>
                        <div class="text-xs text-muted-foreground mt-0.5">{app.description}</div>
                        <div class="flex flex-wrap gap-1.5 mt-2">
                          {#if app.category === 'app'}
                            <span
                              class="text-[10px] px-1.5 py-0.5 rounded bg-accent/10 text-accent border border-accent/30"
                              >App</span
                            >
                          {/if}
                          <span
                            class="text-[10px] px-1.5 py-0.5 rounded bg-muted text-muted-foreground"
                            >{app.vcpus} vCPU · {app.ram_mb} MB · {app.disk_gb} GB</span
                          >
                          {#if app.size_bytes}
                            <span
                              class="text-[10px] px-1.5 py-0.5 rounded bg-muted text-muted-foreground tnum"
                              >{fmtBytes(app.size_bytes)}</span
                            >
                          {/if}
                          {#if app.cloud_init_supported}
                            <span
                              class="text-[10px] px-1.5 py-0.5 rounded bg-success/10 text-success"
                              >cloud-init</span
                            >
                          {/if}
                        </div>
                        {#if app.notes}
                          <p class="text-[11px] text-muted-foreground mt-1.5">{app.notes}</p>
                        {/if}
                        {#if app.cloud_init_supported}
                          <div class="grid grid-cols-1 sm:grid-cols-2 gap-2 mt-2">
                            <div>
                              <label class="text-[11px] text-muted-foreground">Username</label>
                              <Input
                                id={'appuser-' + app.id}
                                type="text"
                                class="w-full"
                                placeholder="webkvm"
                                bind:value={appCIUsers[app.id]}
                              />
                            </div>
                            <div>
                              <label class="text-[11px] text-muted-foreground">Password</label>
                              <div class="relative">
                                <Input
                                  id={'apppass-' + app.id}
                                  type={showAppPass[app.id] ? 'text' : 'password'}
                                  class="w-full pr-10"
                                  placeholder="6-12 chars"
                                  bind:value={appCIPasswords[app.id]}
                                />
                                <button
                                  type="button"
                                  onclick={() => (showAppPass[app.id] = !showAppPass[app.id])}
                                  class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground p-1 rounded"
                                  aria-label={showAppPass[app.id]
                                    ? t('login.hidePassword')
                                    : t('login.showPassword')}
                                  aria-pressed={showAppPass[app.id] || false}
                                >
                                  {#if showAppPass[app.id]}
                                    <EyeOff class="w-4 h-4" />
                                  {:else}
                                    <Eye class="w-4 h-4" />
                                  {/if}
                                </button>
                              </div>
                            </div>
                          </div>
                          <p class="text-[10px] text-muted-foreground mt-1">
                            Used to log in on the serial console. QEMU guest agent is installed
                            automatically for password resets from WebKVM.
                          </p>
                        {/if}
                      </div>
                      <div class="flex flex-col items-end gap-1.5 shrink-0">
                        <div class="flex items-center gap-1.5">
                          <label
                            for={'appnet-' + app.id}
                            class="text-[11px] text-muted-foreground whitespace-nowrap"
                            >{t('vmDetail.networkLabel')}</label
                          >
                          <select
                            id={'appnet-' + app.id}
                            bind:value={appNets[app.id]}
                            class="input w-36"
                          >
                            {#if !netOptions.some((n) => n.name === 'default')}
                              <option value="default">default</option>
                            {/if}
                            {#each netOptions as n (n.name)}
                              <option value={n.name}
                                >{n.name}{n.type ? ' (' + n.type + ')' : ''}</option
                              >
                            {/each}
                          </select>
                        </div>
                        <div class="flex items-center gap-1.5">
                          <Label
                            for={'appname-' + app.id}
                            class="text-[11px] text-muted-foreground whitespace-nowrap"
                            >{t('vms.vmNameShort')}</Label
                          >
                          <Input
                            id={'appname-' + app.id}
                            type="text"
                            class="w-40"
                            bind:value={appNames[app.id]}
                          />
                        </div>
                        <Button size="sm" onclick={() => openDeployConfirm(app)}>
                          <Download class="w-3.5 h-3.5 mr-1.5" />
                          {t('vms.installAppliance')}
                        </Button>
                        {#if auth.canMutate()}
                          <button
                            type="button"
                            onclick={() => openViewScript(app)}
                            class="p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted rounded-md transition-colors"
                            title="Ver / editar script de instalación"
                          >
                            <FileCode2 class="w-3.5 h-3.5" />
                          </button>
                        {/if}
                        {#if auth.isAdmin()}
                          <div class="flex items-center gap-1">
                            <button
                              type="button"
                              onclick={() => openAppEdit(app)}
                              class="p-1.5 text-muted-foreground hover:text-foreground hover:bg-muted rounded-md transition-colors"
                              title="Edit appliance"
                            >
                              <Pencil class="w-3.5 h-3.5" />
                            </button>
                            <button
                              type="button"
                              onclick={() => askDeleteApp(app)}
                              class="p-1.5 text-muted-foreground hover:text-destructive hover:bg-destructive/10 rounded-md transition-colors"
                              title="Delete appliance"
                            >
                              <Trash2 class="w-3.5 h-3.5" />
                            </button>
                          </div>
                        {/if}
                      </div>
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          {/each}
        {/if}
      </div>
    {/if}
    <Dialog.Footer class="gap-2">
      <Button
        variant="outline"
        onclick={() => (showAppliances = false)}
        disabled={appDeploying?.status === 'downloading' ||
          appDeploying?.status === 'processing' ||
          appDeploying?.status === 'queued'}
      >
        {t('common.close')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Manage Groups dialog -->
<Dialog.Root bind:open={showManageGroups}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vms.manageGroupsTitle')}</Dialog.Title>
      <Dialog.Description>{t('vms.manageGroupsDesc')}</Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3">
      <div class="border border-border rounded-md bg-background p-3 space-y-2">
        <div class="flex gap-2">
          <Input bind:value={newGroupName} placeholder={t('vms.groupPlaceholder')} class="flex-1" />
          <Button onclick={createGroup} disabled={mgSaving || !newGroupName.trim()}>
            {#if mgSaving}<Spinner size="xs" color="text-white" />{:else}{t('common.add')}{/if}
          </Button>
        </div>
        <div class="flex items-center gap-1.5">
          <span class="text-xs text-muted-foreground mr-1">{t('vms.groupColor')}</span>
          {#each palette as c}
            <button
              type="button"
              onclick={() => (newGroupColor = c)}
              class="w-5 h-5 rounded-full border-2 transition-all {newGroupColor === c
                ? 'border-foreground scale-110'
                : 'border-transparent'}"
              style="background-color: {c}"
              aria-label={c}
            ></button>
          {/each}
        </div>
        {#if mgError}<p class="text-xs text-destructive">{mgError}</p>{/if}
      </div>

      <div>
        <h3 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-2">
          {t('vms.existingGroups')}
        </h3>
        {#if groups.length === 0}
          <p class="text-sm text-muted-foreground">{t('vms.noGroups')}</p>
        {:else}
          <div class="space-y-1.5">
            {#each groups as g (g.name)}
              <div
                class="flex items-center justify-between border border-border rounded-md bg-background px-2.5 py-1.5"
              >
                <div class="flex items-center gap-2">
                  <span
                    class="inline-block w-2.5 h-2.5 rounded-full"
                    style="background-color: {g.color}"
                  ></span>
                  <span class="text-sm font-medium">{g.name}</span>
                  <span class="text-xs text-muted-foreground tnum"
                    >{g.member_count} VM{g.member_count !== 1 ? 's' : ''}</span
                  >
                </div>
                <div class="flex items-center gap-1.5">
                  {#each palette as c}
                    <button
                      type="button"
                      onclick={() => {
                        g.color = c;
                        updateGroupColor(g);
                      }}
                      class="w-3.5 h-3.5 rounded-full border {g.color === c
                        ? 'border-foreground'
                        : 'border-transparent'}"
                      style="background-color: {c}"
                      aria-label="color {c}"
                    ></button>
                  {/each}
                  <button
                    type="button"
                    onclick={() => deleteGroup(g)}
                    class="p-1 text-muted-foreground hover:text-destructive hover:bg-destructive/10 rounded transition-colors"
                    aria-label="Delete group"
                    title="Delete group"
                  >
                    <svg
                      class="w-3.5 h-3.5"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="2"
                      viewBox="0 0 24 24"
                      ><polyline points="3 6 5 6 21 6" /><path
                        d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"
                      /></svg
                    >
                  </button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </div>

    <Dialog.Footer>
      <Button variant="outline" onclick={() => (showManageGroups = false)}>Close</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Appliance editor (create / edit) -->
<Dialog.Root bind:open={showAppEditor}>
  <Dialog.Content class="sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>
        {appEditorMode === 'create' ? 'Add appliance' : `Edit appliance: ${appEditId}`}
      </Dialog.Title>
      <Dialog.Description>
        {appEditorMode === 'create'
          ? 'Add a new community template. The URL must come from an official source.'
          : 'Update the appliance. The URL must come from an official source.'}
      </Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      {#if appEditorMode === 'create'}
        <div>
          <label class="text-xs font-medium text-muted-foreground">ID</label>
          <Input bind:value={appForm.id} placeholder="ubuntu-24.04" class="w-full" />
        </div>
      {/if}
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div>
          <label class="text-xs font-medium text-muted-foreground">Name</label>
          <Input bind:value={appForm.name} placeholder="Ubuntu Server" class="w-full" />
        </div>
        <div>
          <label class="text-xs font-medium text-muted-foreground">Category</label>
          <select bind:value={appForm.category} class="input w-full">
            <option value="cloud">Cloud OS</option>
            <option value="nas">Network storage</option>
            <option value="router">Router / firewall</option>
            <option value="home">Home automation</option>
            <option value="app">Application</option>
          </select>
        </div>
      </div>
      <div>
        <label class="text-xs font-medium text-muted-foreground">URL (official source)</label>
        <Input
          bind:value={appForm.url}
          placeholder="https://cloud-images.ubuntu.com/..."
          class="w-full font-mono text-xs"
        />
      </div>
      <div>
        <label class="text-xs font-medium text-muted-foreground">Description</label>
        <Input bind:value={appForm.description} class="w-full" />
      </div>
      <div class="rounded-lg border border-border/80 bg-muted/20 p-3 space-y-2">
        <div class="flex items-center justify-between mb-1">
          <label class="text-xs font-semibold flex items-center gap-1.5">
            <FileCode2 class="w-4 h-4" />
            Provision script (bash · runs as root on first boot)
          </label>
          {#if appEditorMode === 'edit' && appIsBuiltin && appScript !== ''}
            <button
              type="button"
              class="text-xs underline text-muted-foreground hover:text-foreground"
              onclick={restoreOriginalScript}
              title="Deja el campo vacío para volver al script original embebido"
            >
              Restaurar original
            </button>
          {/if}
        </div>
        {#if appScriptLoading}
          <div class="h-32 flex items-center justify-center"><Spinner size="sm" /></div>
        {:else}
          <textarea
            bind:value={appScript}
            oninput={scriptInputHandler}
            rows="12"
            spellcheck="false"
            placeholder="#!/bin/bash
set -e
# tus comandos de instalación..."
            class="input w-full font-mono text-[11px] leading-snug"
          ></textarea>
        {/if}
        <p class="text-[11px] text-muted-foreground leading-relaxed">
          Se ejecuta como root en el primer arranque. Placeholder disponible:{' '}
          <code class="font-mono">{'{{WEBKVM_DB_PASS}}'}</code> — WebKVM lo sustituye por una
          contraseña generada y te la muestra en el pop-up tras el deploy. La salida queda en{' '}
          <code class="font-mono">/var/log/webkvm-provision.log</code>. En un builtin, vacío =
          script original.
        </p>
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <div>
          <label class="text-xs font-medium text-muted-foreground">Format</label>
          <select bind:value={appForm.format} class="input w-full">
            <option value="qcow2">qcow2</option>
            <option value="raw">raw</option>
          </select>
        </div>
        <div>
          <label class="text-xs font-medium text-muted-foreground">Compression</label>
          <select bind:value={appForm.compression} class="input w-full">
            <option value="none">none</option>
            <option value="gz">gz</option>
            <option value="xz">xz</option>
            <option value="bz2">bz2</option>
          </select>
        </div>
        <div>
          <label class="text-xs font-medium text-muted-foreground">Cloud-init</label>
          <label class="flex items-center gap-2 text-sm mt-1.5">
            <input
              type="checkbox"
              bind:checked={appForm.cloud_init_supported}
              class="w-4 h-4 rounded border-border"
            />
            Supported
          </label>
        </div>
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <div>
          <label class="text-xs font-medium text-muted-foreground">vCPU</label>
          <Input type="number" bind:value={appForm.vcpus} class="w-full" />
        </div>
        <div>
          <label class="text-xs font-medium text-muted-foreground">RAM (MB)</label>
          <Input type="number" bind:value={appForm.ram_mb} class="w-full" />
        </div>
        <div>
          <label class="text-xs font-medium text-muted-foreground">Disk (GB)</label>
          <Input type="number" bind:value={appForm.disk_gb} class="w-full" />
        </div>
      </div>
      <div>
        <label class="text-xs font-medium text-muted-foreground">Notes</label>
        <Input bind:value={appForm.notes} class="w-full" />
      </div>
      {#if appEditorError}
        <p class="text-sm text-destructive">{appEditorError}</p>
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showAppEditor = false)} disabled={appSaving}>
        Cancel
      </Button>
      <Button onclick={saveAppliance} disabled={appSaving}>
        {#if appSaving}<Spinner size="sm" color="text-white" />{:else}Save{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Appliance delete (double confirmation) -->
<Dialog.Root bind:open={showAppDelete}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>Delete appliance</Dialog.Title>
      <Dialog.Description>
        This removes "{appDeleteTarget?.name}" from the catalog. It does not affect any VMs already
        created from it.
      </Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      {#if appDeleteTarget?.builtin}
        <p class="text-sm text-destructive">
          This is a built-in appliance. Deleting it is permanent. Confirm twice to proceed.
        </p>
      {:else}
        <p class="text-sm text-muted-foreground">
          Deleting this appliance is permanent. Confirm twice to proceed.
        </p>
      {/if}
      <p class="text-xs text-muted-foreground">
        Step {appDeleteStep} of 2 — click Delete, then type the appliance name and confirm again.
      </p>
      <Input bind:value={appDeleteText} placeholder={appDeleteTarget?.name} class="w-full" />
      {#if appDeleteError}
        <p class="text-sm text-destructive">{appDeleteError}</p>
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showAppDelete = false)} disabled={appDeleting}>
        Cancel
      </Button>
      <Button
        variant="destructive"
        onclick={confirmDeleteApp}
        disabled={appDeleting || appDeleteText.trim() !== appDeleteTarget?.name}
      >
        {#if appDeleting}<Spinner size="sm" color="text-white" />{:else}Delete{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Confirm appliance deployment: name + network + CI summary -->
<Dialog.Root bind:open={showDeployConfirm}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vms.installAppliance')}: {deployApp?.name || ''}</Dialog.Title>
      <Dialog.Description>{t('vms.appliancesDesc')}</Dialog.Description>
    </Dialog.Header>
    {#if deployApp}
      <div class="space-y-3">
        <div class="space-y-1.5">
          <Label for="deploy-name">{t('vms.vmNameShort')}</Label>
          <Input id="deploy-name" bind:value={appNames[deployApp.id]} placeholder={deployApp.id} />
        </div>
        <div class="space-y-1.5">
          <Label for="deploy-net">{t('vmDetail.networkLabel')}</Label>
          <select id="deploy-net" bind:value={appNets[deployApp.id]} class="input w-full">
            {#if !netOptions.some((n) => n.name === 'default')}
              <option value="default">default</option>
            {/if}
            {#each netOptions as n (n.name)}
              <option value={n.name}>{n.name}{n.type ? ' (' + n.type + ')' : ''}</option>
            {/each}
          </select>
        </div>
        {#if deployApp.cloud_init_supported}
          <div class="rounded-lg border border-border p-3 text-xs space-y-1">
            <div class="font-medium text-muted-foreground">cloud-init</div>
            <div class="flex justify-between gap-2">
              <span class="text-muted-foreground">{t('vmCreate.cloudInitUser')}</span>
              <span class="font-mono">{appCIUsers[deployApp.id] || '—'}</span>
            </div>
            <div class="flex justify-between gap-2">
              <span class="text-muted-foreground">Password</span>
              <span class="font-mono">{appCIPasswords[deployApp.id] ? '••••••' : '—'}</span>
            </div>
          </div>
        {/if}
      </div>
    {/if}
    <Dialog.Footer class="gap-2">
      <Button
        variant="outline"
        onclick={() => {
          showDeployConfirm = false;
          deployApp = null;
        }}
      >
        {t('common.cancel')}
      </Button>
      <Button onclick={() => deployAppliance(deployApp, appNames[deployApp.id]?.trim() || '')}>
        {#if appDeploying}<Spinner size="sm" color="text-white" />{:else}{t(
            'vms.installAppliance'
          )}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<ErrorModal bind:open={showAppError} title={appErrorTitle} message={appErrorMessage} />

<CredentialsModal
  bind:open={showAppCreds}
  info={appCreds}
  onClose={() => {
    showAppCreds = false;
    if (pendingNavId) {
      navigate('/vms/' + pendingNavId);
      pendingNavId = null;
    }
  }}
/>

<Dialog.Root bind:open={showScriptView}>
  <Dialog.Content class="sm:max-w-3xl">
    <Dialog.Header>
      <Dialog.Title>
        Script de instalación{viewScriptApp ? ` — ${viewScriptApp.name || viewScriptApp.id}` : ''}
      </Dialog.Title>
      <Dialog.Description>
        Bash ejecutado como root en el primer arranque de la VM.
      </Dialog.Description>
    </Dialog.Header>
    <pre
      class="max-h-[55vh] overflow-auto rounded-lg border border-border bg-muted/50 p-4 text-[11px] font-mono leading-snug whitespace-pre-wrap">{viewScriptText}</pre>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showScriptView = false)}>Cerrar</Button>
      {#if auth.isAdmin() && viewScriptApp}
        <Button
          onclick={() => {
            showScriptView = false;
            openAppEdit(viewScriptApp);
          }}
        >
          <Pencil class="w-3.5 h-3.5 mr-1.5" /> Editar
        </Button>
      {/if}
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
