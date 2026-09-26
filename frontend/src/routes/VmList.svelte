<script>
  import { SvelteSet, SvelteURLSearchParams, SvelteMap } from 'svelte/reactivity';
  import SearchInput from '$lib/components/SearchInput.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import { upsertTask, updateTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { onMount, onDestroy } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { events } from '$lib/stores/events.svelte.js';
  import { getRoute, navigate } from '$lib/router.svelte.js';
  import { auth } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { t } from '../lib/i18n.svelte.js';
  import { purposeLabel, vmDiskPools, containerPools, deployablePools } from '$lib/purpose.js';
  import { stateDotClass } from '$lib/utils/vmState.js';
  import {
    computeTypeBadgeClass,
    computeTypeLabel,
    isContainer,
    provisionChip,
  } from '$lib/utils/computeType.js';
  import { networkLabel } from '$lib/utils/networkLabel.js';
  import { vmIps } from '$lib/utils/vmIps.js';
  import {
    vmOrder,
    setSortMode,
    togglePin,
    isPinned,
    moveInCustomOrder,
    applySort,
  } from '$lib/stores/vmOrder.svelte.js';

  // Friendly label for a network option {name, bridge} (v1.4 Fase 4.1):
  // "Red Interna (vmbr0)" instead of a raw bridge/name.
  function netDisplay(n) {
    if (!n) return '';
    return networkLabel({ name: n.name, bridge: n.bridge });
  }
  import { Button } from '$lib/components/ui/button';
  import { Card } from '$lib/components/ui/card';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import BulkActionBar from '$lib/components/BulkActionBar.svelte';
  import Gauge from '$lib/components/Gauge.svelte';
  import { gaugeStyle, toggleGaugeStyle } from '$lib/stores/gaugeStyle.svelte.js';
  import Icon from '$lib/components/Icon.svelte';
  import ErrorModal from '$lib/components/ErrorModal.svelte';
  import CredentialsModal from '$lib/components/CredentialsModal.svelte';
  import ManageGroupsDialog from '$lib/components/ManageGroupsDialog.svelte';
  import * as Dialog from '$lib/components/ui/dialog';
  import Chart from '$lib/components/Chart.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import { formatRate } from '$lib/utils/format.js';
  import { CATEGORICAL_COLORS } from '$lib/utils/chartColors.js';
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
    Cpu,
    Container,
    CloudCog,
    Box,
    SquareTerminal,
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
  // v1.4 Fase 4: type filter (All · VMs · Containers), PLAN-LXD 5.1.
  let typeFilter = $state(readQuery('type') || 'all');

  // Sync filters → URL.
  $effect(() => {
    const q = new SvelteURLSearchParams();
    if (search) q.set('q', search);
    if (groupFilter && groupFilter !== 'all') q.set('group', groupFilter);
    if (stateFilter && stateFilter !== 'all') q.set('state', stateFilter);
    if (typeFilter && typeFilter !== 'all') q.set('type', typeFilter);
    if (selectMode) q.set('select', '1');
    const target = '/vms' + (q.toString() ? '?' + q.toString() : '');
    // Only touch the URL while this list is the active route. Without
    // this guard the effect could fire during the route teardown and
    // history.replaceState would rewrite the hash (e.g. #/storage →
    // #/vms) with NO hashchange event, silently desyncing the URL from
    // the router state — the next in-page navigation then renders the
    // wrong page.
    if (
      typeof location !== 'undefined' &&
      getRoute().name === 'vms' &&
      location.hash !== '#' + target
    ) {
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
  const visibleGroups = $derived(
    groups
      .map((g) => ({
        ...g,
        member_count: vms.filter((v) => Array.isArray(v.groups) && v.groups.includes(g.name))
          .length,
      }))
      .filter((g) => g.member_count > 0)
  );
  // Group CRUD dialog itself lives in ManageGroupsDialog.svelte; VmList
  // only keeps `groups` (also used by the filter chips below) and the
  // trigger flag.
  let showManageGroups = $state(false);

  // Shared with Backup's group-colour fallback (see chartColors.js) so a
  // group's swatch and its colour in the backup chart no longer come from
  // two independently-maintained lists.
  const palette = CATEGORICAL_COLORS;

  // Confirm dialog state
  let confirmDeleteOpen = $state(false);
  let confirmDeleteVm = $state(null);
  let confirmDeleteLoading = $state(false);

  // Bulk confirm dialog
  let confirmBulkOpen = $state(false);
  let confirmBulkAction = $state(''); // 'start' | 'shutdown' | 'forceoff' | 'delete'
  let confirmBulkLoading = $state(false);

  // Group assignment dialog
  let showAssignGroup = $state(false);
  // A VM can belong to more than one group at once, so assignment supports
  // selecting several groups in one go (toggleable chips), not just one.
  let assignGroupNames = $state(new Set());

  // Import modal state
  let showImport = $state(false);
  let importName = $state('');
  let importPool = $state('webkvm-disks');
  // Import LXC modal state
  let showImportLxc = $state(false);
  let importLxcFile = $state(null);
  let importLxcName = $state('');
  let importLxcPool = $state('');
  let importLxcNetwork = $state('');
  let importingLxc = $state(false);
  let importErrorLxc = $state('');
  let importProgressLxc = $state(0);
  let importPhaseLxc = $state('');
  let networks = $state([]);
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
  // Full copy by default: a linked clone is faster and thinner but
  // stays bound to the template's disk, which is a trap if the
  // operator later deletes the template. Opt-in, never the default.
  let instLinked = $state(false);
  let instNet = $state('default');
  let instNetOptions = $state([]);
  let instPool = $state('');
  let instPoolOptions = $state([]);
  // Community appliance deploy dialog.
  let showAppliances = $state(false);
  let appliances = $state([]);
  let appNames = $state({}); // applianceId -> custom VM name
  let appCIUsers = $state({}); // applianceId -> cloud-init username
  let appCIPasswords = $state({}); // applianceId -> cloud-init password
  let showAppPass = $state({}); // applianceId -> password visibility
  let appNets = $state({}); // applianceId -> network name
  // V13-DATA-02: per-appliance eligible-pool selection + the loaded list.
  let appPools = $state({}); // applianceId -> pool name
  let deployPools = $state([]);
  let netOptions = $state([]); // [{name, type}]
  let showDeployConfirm = $state(false);
  let deployApp = $state(null); // appliance elegido para instalar
  // Guards the deploy submit against double-clicks.
  let deployBusy = $state(false);
  let showAppError = $state(false);
  let appErrorTitle = $state('');
  let appErrorMessage = $state('');
  let showAppCreds = $state(false);
  let appCreds = $state(null);
  let pendingNavId = $state(null);
  let appDeploying = $state(null); // { jobId, name, status, pct, error }
  let appPoller = $state(null);
  // Strict abort: after this many CONSECUTIVE 404s the poller
  // stops — the job vanished from the backend (e.g. purged by the 24h job
  // sweeper). Any non-404 error resets the counter, so a generic network
  // blip can never trip a false positive.
  const MAX_CONSECUTIVE_404 = 10;
  let appPoller404s = 0;

  function stopAppPoller() {
    if (appPoller) clearInterval(appPoller);
    appPoller = null;
    appPoller404s = 0;
  }
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
  // group-assign dialog/logic as the multi-select flow, just pre-seeded
  // with a single VM.
  function openAssignGroupForVm(vm) {
    selectedKeys = new Set([vm.id]);
    assignGroupNames = new Set(vm.groups || []);
    showAssignGroup = true;
    menuFor = null;
  }

  async function quickAction(vm, action) {
    const key = `${vm.id}:${action}`;
    // Never fire the same mutation twice (double-click / key
    // repeat). A busy flag is set synchronously before any await.
    if (quickBusy === key) return;
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
          const cloned = await api.waitJob(res.job);
          const cloneName = cloned?.name || `${vm.name}-clone`;
          toast.success(t('vms.cloned', { name: cloneName }));
          if (cloned?.id) navigate('/vms/' + cloned.id);
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
  // One list per import dialog. A single shared `pools` was a mix-up
  // waiting to happen: opening the LXC import filled it with Incus
  // pools, and if the KVM dialog's own fetch then failed it kept
  // offering those container pools for a VM archive.
  let importPools = $state([]);
  let importLxcPools = $state([]);

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
    if (typeFilter !== 'all') {
      out = out.filter((v) => (typeFilter === 'container' ? isContainer(v) : v.type === 'vm'));
    }
    if (q) {
      out = out.filter(
        (v) =>
          (v.name || '').toLowerCase().includes(q) ||
          (v.alias && v.alias.toLowerCase().includes(q)) ||
          (v.ip && v.ip.includes(q)) ||
          vmIps(v).some((ip) => ip.includes(q))
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
    const next = new SvelteSet();
    for (const k of selectedKeys) {
      if (valid.has(k)) next.add(k);
      else changed = true;
    }
    if (changed) selectedKeys = next;
  });

  // Bulk selection helpers. They operate on the CURRENTLY VISIBLE set
  // (filteredVms), not every VM on the host — "select all" while a
  // group/state filter is active should only pick what the user can
  // actually see, otherwise a bulk delete could hit VMs scrolled out
  // of view by a filter.
  const allVisibleSelected = $derived(
    filteredVms.length > 0 && filteredVms.every((v) => selectedKeys.has(v.id))
  );

  function selectAllVisible() {
    const next = new SvelteSet(selectedKeys);
    for (const v of filteredVms) next.add(v.id);
    selectedKeys = next;
  }

  function invertVisibleSelection() {
    const next = new SvelteSet(selectedKeys);
    for (const v of filteredVms) {
      if (next.has(v.id)) next.delete(v.id);
      else next.add(v.id);
    }
    selectedKeys = next;
  }

  // ---- VM ordering: custom drag & drop, smart sorts, pinning ----
  const lastMetricVal = (series) => (series && series.length ? series[series.length - 1].v : 0);
  const sortedVms = $derived.by(() => {
    const metricsMap = {};
    for (const v of filteredVms) {
      metricsMap[v.id] = {
        cpu: lastMetricVal(metricsByVm[v.id]?.cpu?.points),
        ram: lastMetricVal(metricsByVm[v.id]?.ram?.points),
      };
    }
    return applySort(filteredVms, metricsMap);
  });

  const SORT_OPTIONS = [
    { id: 'custom', label: 'sortCustom', icon: 'mousePointer' },
    { id: 'alpha', label: 'sortAlpha', icon: 'chevronDown' },
    { id: 'alpha-desc', label: 'sortAlphaDesc', icon: 'chevronUp' },
    { id: 'state', label: 'sortState', icon: 'activity' },
    { id: 'cpu', label: 'sortCpu', icon: 'cpu' },
    { id: 'ram', label: 'sortRam', icon: 'server' },
  ];
  let showSortMenu = $state(false);

  // Drag & drop state (custom order only)
  let draggedVmId = $state(null);
  let dragOverVmId = $state(null);
  let dragOverAfter = $state(false);

  function onCardDragStart(e, vm) {
    if (vmOrder.sortMode !== 'custom') return;
    draggedVmId = vm.id;
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', vm.id);
  }

  function onCardDragOver(e, vm) {
    if (vmOrder.sortMode !== 'custom' || !draggedVmId || draggedVmId === vm.id) return;
    e.preventDefault();
    const rect = e.currentTarget.getBoundingClientRect();
    dragOverAfter = e.clientX - rect.left > rect.width / 2;
    dragOverVmId = vm.id;
  }

  function onCardDrop(e, vm) {
    if (vmOrder.sortMode !== 'custom' || !draggedVmId) return;
    e.preventDefault();
    if (draggedVmId !== vm.id) {
      moveInCustomOrder(
        filteredVms.map((v) => v.id),
        draggedVmId,
        vm.id,
        dragOverAfter
      );
    }
    draggedVmId = null;
    dragOverVmId = null;
  }

  function onCardDragEnd() {
    draggedVmId = null;
    dragOverVmId = null;
  }

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

  // Never leave the appliance-job poller running after this
  // page unmounts — a leaked interval would keep hitting the API (and
  // re-rendering toasts) while the user is elsewhere.
  onDestroy(() => {
    stopAppPoller();
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
    await loadGroups();
    showManageGroups = true;
  }

  // Passed to ManageGroupsDialog as onChanged: called after create/update
  // (no args) and after delete (deletedName), so VmList can refresh its
  // own group list/VM list and clear the filter if it pointed at the
  // now-deleted group.
  async function onGroupsChanged(deletedName) {
    if (deletedName && groupFilter === deletedName) groupFilter = 'all';
    await loadGroups();
    if (deletedName) await loadVMs();
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
          else if (confirmBulkAction === 'autostart-on') await api.setVMAutostart(id, true);
          else if (confirmBulkAction === 'autostart-off') await api.setVMAutostart(id, false);
          else if (confirmBulkAction === 'snapshot') {
            const snapName = 'bulk-' + new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
            await api.createSnapshot(id, { name: snapName, description: 'Bulk snapshot' });
          }
          succeeded++;
        } catch (_) {
          failed++;
        }
      }
      const label =
        {
          start: 'iniciadas',
          shutdown: 'apagadas',
          forceoff: 'apagadas forzosamente',
          delete: 'eliminadas',
          'autostart-on': 'autostart activado',
          'autostart-off': 'autostart desactivado',
          snapshot: 'instantáneas creadas',
        }[confirmBulkAction] || 'procesadas';
      if (succeeded) toast.success(`${succeeded} VM${succeeded !== 1 ? 's' : ''} ${label}`);
      if (failed) toast.error(`${failed} failed`);
      confirmBulkOpen = false;
      selectedKeys = new Set();
      await loadVMs();
    } finally {
      confirmBulkLoading = false;
    }
  }

  async function doAssignGroup() {
    if (assignGroupNames.size === 0) return;
    if (selectedKeys.size === 0) {
      // Can happen if the selection got cleared (e.g. a filter change)
      // while this dialog was open — fail loudly instead of silently
      // doing nothing, which is exactly what looked like a bug before.
      toast.error(t('vms.noVmsSelected'));
      showAssignGroup = false;
      return;
    }
    const ids = Array.from(selectedKeys);
    const namesToAdd = Array.from(assignGroupNames);
    let ok = 0,
      fail = 0;
    for (const id of ids) {
      try {
        const m = await api.getVMMeta(id);
        const groups = new SvelteSet(Array.isArray(m.groups) ? m.groups : []);
        for (const name of namesToAdd) groups.add(name);
        await api.updateVMMeta(id, { groups: Array.from(groups) });
        ok++;
      } catch (_) {
        fail++;
      }
    }
    const namesLabel = namesToAdd.map((n) => `"${n}"`).join(', ');
    if (ok) toast.success(t('vms.assignedCount', { ok, names: namesLabel }));
    if (fail) toast.error(`${fail} failed`);
    showAssignGroup = false;
    assignGroupNames = new Set();
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
    instLinked = false;
    instNet = 'default';
    instPool = '';
    try {
      // The pool list is optional context: a failure there should still
      // let the operator instantiate onto the server's default pool.
      const [r, nets, pools] = await Promise.all([
        api.listTemplates(),
        api.listNetworks(),
        api.listPools().catch(() => []),
      ]);
      instTemplates = r.templates || [];
      instTemplateId = instTemplates[0]?.id || '';
      // A template instantiates into a VM, so only libvirt disk pools
      // are eligible. The backend has always accepted this field
      // (templates.go) — the dialog simply never sent it, so every
      // instantiation silently landed on the default pool.
      instPoolOptions = deployablePools(pools, 'vm');
      instNetOptions = (nets.networks || nets || []).map((n) => ({
        name: n.name,
        type: n.mode || n.type || '',
        bridge: n.bridge || '',
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
      const data = { name: instName.trim(), network: instNet, linked: instLinked };
      // Empty means "server default", which the backend resolves.
      if (instPool) data.pool = instPool;
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
          bridge: n.bridge || '',
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
    if (appDeleteText.trim() !== (appDeleteTarget?.name || '')) {
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
    const groups = new SvelteMap();
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
    // V13-DATA-02: load the caller's eligible storage pools (the backend
    // already scopes them by AllowedPools) and preselect the first one
    // so the deploy is one click away.
    if (!appPools[app.id]) {
      // The eligible pools depend on what this appliance deploys as: a
      // container lands in an Incus pool, a VM in a libvirt disk pool.
      // This used to offer libvirt disk pools unconditionally, so a
      // container appliance was preselected onto a pool it could not
      // use.
      const targetType = app.default_type || 'container';
      api
        .listPools()
        .then((all) => {
          const eligible = deployablePools(all, targetType);
          if (eligible.length > 0 && !eligible.some((p) => p.name === appPools[app.id])) {
            appPools[app.id] = eligible[0].name;
          }
          deployPools = eligible;
        })
        .catch(() => {
          /* listPools failure: fall back to the server default pool */
        });
    }
    showDeployConfirm = true;
  }

  async function deployAppliance(app, vmName) {
    // The confirm dialog stays open during the (async) API
    // call, so a double-click could otherwise fire two deploys. Set the
    // flag synchronously and clear it in finally.
    if (deployBusy) return;
    deployBusy = true;
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
      // V13-DATA-02: send the chosen pool (empty = server default).
      body.pool = (appPools[app.id] || '').trim();
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
    } finally {
      deployBusy = false;
    }
  }

  async function pollApplianceJob() {
    if (!appDeploying?.jobId) {
      stopAppPoller();
      return;
    }
    try {
      const job = await api.getDownloadJob(appDeploying.jobId);
      // Any successful response (even a stale job) clears the 404 streak.
      appPoller404s = 0;
      appDeploying = {
        jobId: job.id,
        name: appDeploying.name,
        status: job.status,
        pct: Math.round(job.progress || 0),
        error: job.error || '',
      };
      if (job.status === 'completed' || job.status === 'error') {
        stopAppPoller();
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
    } catch (err) {
      // Only a strict run of CONSECUTIVE 404s aborts the
      // poller (the job no longer exists server-side — e.g. the 24h job
      // sweeper purged it). Generic network failures (offline, 5xx, …)
      // are transient and reset the counter instead of aborting.
      if (err && err.status === 404) {
        appPoller404s += 1;
        if (appPoller404s >= MAX_CONSECUTIVE_404) {
          stopAppPoller();
          appDeploying = {
            ...appDeploying,
            status: 'error',
            error: t('vms.applianceJobGone'),
          };
          toast.error(t('vms.applianceJobGone'), { duration: 8000 });
        }
      } else {
        appPoller404s = 0;
      }
    }
  }

  async function openImport() {
    showImport = true;
    importError = '';
    try {
      // Importing a VM archive writes disk images, so only pools that
      // can hold one. usableForDisks let container pools through, and
      // the dialog's own {#each pools} rendered the unfiltered list on
      // top of that, so even the ISO pool was offered.
      importPools = vmDiskPools((await api.listPools()) || []);
      if (importPools.length > 0 && !importPools.find((p) => p.name === importPool)) {
        importPool = importPools[0].name;
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

  // --- Import LXC ---
  async function openImportLxc() {
    showImportLxc = true;
    importErrorLxc = '';
    importLxcNetwork = ''; // reset network selection
    try {
      const [poolsRes, netsRes] = await Promise.all([api.listPools(), api.listNetworks()]);
      // For LXC: only Incus pools. The name-based escape hatch for
      // webkvm-incus is gone — it carries the container purpose like
      // any other, and matching by name also skipped the state check.
      importLxcPools = containerPools(poolsRes || []);
      // Prefer webkvm-incus, then default Incus pool, then first container pool
      const preferred = ['webkvm-incus', 'default'];
      for (const pref of preferred) {
        if (importLxcPools.some((p) => p.name === pref)) {
          importLxcPool = pref;
          break;
        }
      }
      if (!importLxcPool && importLxcPools.length > 0) {
        importLxcPool = importLxcPools[0].name;
      }
      networks = netsRes.networks || netsRes || [];
      // If there's a 'default' network, select it
      if (networks.some((n) => n.name === 'default')) {
        importLxcNetwork = 'default';
      } else if (networks.length > 0) {
        importLxcNetwork = networks[0].name;
      }
    } catch (e) {
      importErrorLxc = t('vms.couldNotLoadPools', { error: e.message });
    }
  }

  async function doImportLxc() {
    if (!importLxcFile) {
      importErrorLxc = t('vms.pickFileLxc');
      return;
    }
    if (!importLxcName.trim()) {
      importErrorLxc = t('vms.containerNameRequired');
      return;
    }
    importingLxc = true;
    importErrorLxc = '';
    importProgressLxc = 0;
    importPhaseLxc = t('vms.uploading');
    const taskId = 'import-lxc:' + importLxcFile.name;
    upsertTask({
      id: taskId,
      kind: 'import',
      title: importLxcFile.name,
      pct: 0,
      message: t('vms.uploading'),
      status: 'running',
    });
    try {
      const res = await api.importVM(
        importLxcFile,
        importLxcName,
        importLxcPool,
        (pct) => {
          importProgressLxc = pct;
          if (pct >= 100) importPhaseLxc = t('vms.processingOnServer');
          updateTask(taskId, {
            pct,
            message: pct >= 100 ? t('vms.processingOnServer') : t('vms.uploading'),
          });
        },
        importLxcNetwork
      );
      finishTask(
        taskId,
        'success',
        res?.name ? t('vms.importedAs', { name: res.name }) : t('vms.imported'),
        100
      );
      if (res && res.name) {
        if (res.requested_name && res.requested_name !== res.name) {
          toast.warning(t('vms.alreadyExisted', { requested: res.requested_name, name: res.name }));
        } else if (importLxcName && importLxcName !== res.name) {
          toast.warning(t('vms.nameConflict', { name: res.name }));
        } else {
          toast.success(t('vms.importedAs', { name: res.name }));
        }
        if (Array.isArray(res.warnings) && res.warnings.length > 0) {
          for (const w of res.warnings) {
            toast.warning(w, { duration: 0 });
          }
        }
      } else {
        toast.success(t('vms.imported'));
      }
      showImportLxc = false;
      importLxcFile = null;
      importLxcName = '';
      importProgressLxc = 0;
      importPhaseLxc = '';
      await loadVMs();
    } catch (e) {
      finishTask(taskId, 'error', e.message, importProgressLxc || 0);
      importErrorLxc = e.message;
    } finally {
      importingLxc = false;
    }
  }
</script>

<svelte:window
  onclick={() => {
    if (showSortMenu) showSortMenu = false;
  }}
/>

<div class="p-3 sm:p-5 w-full max-w-[1800px] mx-auto">
  <PageHeader
    title={t('vms.title')}
    subtitle={`${vms.length} ${vms.length === 1 ? t('vms.machine') : t('vms.machines')}`}
  >
    {#snippet actions()}
      <!-- Gauge shape preference (rings vs bars). Per-browser, so the
           same admin can pick a different density per device. -->
      <button
        type="button"
        onclick={toggleGaugeStyle}
        title={t('vms.gaugeToggle')}
        aria-label={t('vms.gaugeToggle')}
        class="inline-flex items-center gap-1.5 px-2.5 py-1.5 text-xs font-medium rounded-lg border border-border bg-card hover:bg-muted transition-colors"
      >
        <Icon name={gaugeStyle.mode === 'radial' ? 'gauge' : 'activity'} size={13} />
        <span class="hidden sm:inline">
          {gaugeStyle.mode === 'radial' ? t('vms.gaugeRadial') : t('vms.gaugeLinear')}
        </span>
      </button>

      <!-- VM ordering: custom drag & drop, or a smart sort criterion -->
      <div class="relative">
        <button
          type="button"
          onclick={(e) => {
            e.stopPropagation();
            showSortMenu = !showSortMenu;
          }}
          class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg border border-border bg-card hover:bg-muted transition-colors"
        >
          <Icon
            name={SORT_OPTIONS.find((o) => o.id === vmOrder.sortMode)?.icon || 'mousePointer'}
            size={13}
          />
          {t(`vms.${SORT_OPTIONS.find((o) => o.id === vmOrder.sortMode)?.label}`) ||
            vmOrder.sortMode}
          <Icon name="chevronDown" size={12} class="text-muted-foreground" />
        </button>
        {#if showSortMenu}
          <div
            class="absolute right-0 top-full z-30 mt-1 w-52 rounded-lg border border-border bg-popover text-popover-foreground shadow-lg p-1"
          >
            {#each SORT_OPTIONS as opt (opt.id)}
              <button
                type="button"
                onclick={() => {
                  setSortMode(opt.id);
                  showSortMenu = false;
                }}
                class="w-full text-left text-xs px-2.5 py-1.5 rounded flex items-center gap-2 hover:bg-muted {vmOrder.sortMode ===
                opt.id
                  ? 'text-accent font-semibold'
                  : ''}"
              >
                <Icon name={opt.icon} size={13} />
                {t(`vms.${opt.label}`)}
              </button>
            {/each}
          </div>
        {/if}
      </div>
    {/snippet}
  </PageHeader>

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
  <div class="flex flex-wrap items-center gap-2 mb-4">
    {#if auth.canMutate()}
      <button
        type="button"
        onclick={() => (selectMode = !selectMode)}
        class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-md border transition-colors {selectMode
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
    {/if}
    <SearchInput
      bind:value={search}
      placeholder={t('vms.searchPlaceholder')}
      class="w-full sm:w-64"
    />

    {#if auth.isAdmin()}
      <Button variant="outline" onclick={openManageGroups}>{t('vms.manageGroups')}</Button>
    {/if}
    {#if auth.canMutate()}
      <Button variant="outline" onclick={openImport}>
        <FileUp class="w-3.5 h-3.5 mr-1.5" />
        {t('vms.importVm')}
      </Button>
      <Button variant="outline" onclick={openImportLxc}>
        <Container class="w-3.5 h-3.5 mr-1.5" />
        {t('vms.importLxc')}
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
    {/if}
  </div>

  <BulkActionBar
    count={selectedKeys.size}
    visible={selectMode}
    visibleCount={filteredVms.length}
    allSelected={allVisibleSelected}
    onSelectAll={selectAllVisible}
    onInvert={invertVisibleSelection}
    actions={[
      ...(auth.canMutate()
        ? [
            { key: 'start', label: t('vms.start'), onClick: () => askBulk('start') },
            { key: 'shutdown', label: t('vms.shutdown'), onClick: () => askBulk('shutdown') },
            { key: 'forceoff', label: t('vms.forceOff'), onClick: () => askBulk('forceoff') },
            {
              key: 'snapshot',
              label: t('vms.bulkSnapshot'),
              onClick: () => askBulk('snapshot'),
            },
            {
              key: 'autostart-on',
              label: 'Autostart ON',
              onClick: () => askBulk('autostart-on'),
            },
            {
              key: 'autostart-off',
              label: 'Autostart OFF',
              onClick: () => askBulk('autostart-off'),
            },
          ]
        : []),
      {
        key: 'assign-group',
        label: t('vms.assignToGroup'),
        onClick: () => (
          (showAssignGroup = true),
          (assignGroupNames = new Set(groupFilter !== 'all' ? [groupFilter] : []))
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

  <!-- v1.4 Fase 4: instance-type filter (PLAN-LXD 5.1), always visible -->
  <div class="flex items-center gap-1.5 flex-wrap mb-4">
    {#each [{ v: 'all', l: t('vms.allTypes'), c: '' }, { v: 'vm', l: t('vms.typeVms'), c: '' }, { v: 'container', l: t('vms.typeContainers'), c: 'text-warning' }] as f (f.v)}
      <button
        onclick={() => (typeFilter = typeFilter === f.v ? 'all' : f.v)}
        class="text-xs px-2.5 py-1 rounded-full border transition-colors {typeFilter === f.v
          ? 'border-foreground text-foreground bg-muted'
          : 'border-border text-muted-foreground hover:text-foreground'}"
      >
        <span class="inline-flex items-center gap-1">
          {#if f.v === 'vm'}
            <Cpu class="w-3 h-3" />
          {:else if f.v === 'container'}
            <Container class="w-3 h-3 {f.c}" />
          {/if}
          {f.l}
          <span class="text-[10px] opacity-60"
            >({f.v === 'all'
              ? vms.length
              : f.v === 'container'
                ? vms.filter((v) => isContainer(v)).length
                : vms.filter((v) => v.type === 'vm').length})</span
          >
        </span>
      </button>
    {/each}
  </div>

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
      {#each [{ v: 'running', c: 'bg-status-running', l: 'running' }, { v: 'shutoff', c: 'bg-status-shutoff', l: 'shutoff' }, { v: 'paused', c: 'bg-status-paused', l: 'paused' }, { v: 'crashed', c: 'bg-status-crashed', l: 'crashed' }] as s (s.v)}
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
      {#if visibleGroups.length > 0}
        <span class="text-xs text-muted-foreground mx-1">|</span>
        {#each visibleGroups as g (g)}
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
    <Card class="p-0">
      <EmptyState icon="server" title={t('vms.emptyDesc')}>
        {#snippet action()}
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
            <Button variant="outline" onclick={openImportLxc}>
              <Container class="w-3.5 h-3.5 mr-1.5" />
              {t('vms.importLxc')}
            </Button>
          </div>
        {/snippet}
      </EmptyState>
    </Card>
  {:else}
    {#if menuFor}
      <!-- Backdrop transparente para cerrar el menú de 3 puntos sin navegar por error -->
      <button
        type="button"
        class="fixed inset-0 z-40 bg-transparent cursor-default border-none outline-none"
        onclick={(e) => {
          e.stopPropagation();
          menuFor = null;
        }}
        aria-label={t('common.close')}
      ></button>
    {/if}
    <div
      class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5 3xl:grid-cols-6 gap-3.5"
    >
      {#each sortedVms as vm (vm.id)}
        {@const isSelected = selectedKeys.has(vm.id)}
        {@const metrics = metricsByVm[vm.id]}
        {@const cpuPts = last30(metrics?.cpu?.points)}
        {@const ramPts = last30(metrics?.ram?.points)}
        {@const chip = provisionChip(vm.provision_method)}
        {@const pinned = isPinned(vm.id)}
        <div
          role="button"
          tabindex="0"
          draggable={vmOrder.sortMode === 'custom' && !selectMode}
          ondragstart={(e) => onCardDragStart(e, vm)}
          ondragover={(e) => onCardDragOver(e, vm)}
          ondrop={(e) => onCardDrop(e, vm)}
          ondragend={onCardDragEnd}
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
          class="group relative text-left border rounded-lg bg-card transition-[border-color,box-shadow,transform] duration-200 ease-out {menuFor ===
          vm.id
            ? 'z-50 ring-2 ring-accent/30 shadow-xl'
            : 'z-0'} {(selectMode ? isSelected : false)
            ? 'border-accent ring-1 ring-accent/40'
            : 'border-border hover:border-border-hover hover:-translate-y-0.5 hover:shadow-md'} {draggedVmId ===
          vm.id
            ? 'opacity-40'
            : ''} {dragOverVmId === vm.id
            ? dragOverAfter
              ? 'border-r-2 border-r-accent'
              : 'border-l-2 border-l-accent'
            : ''}"
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
            {#if !selectMode}
              <button
                type="button"
                title={pinned ? t('vms.unpin') : t('vms.pin')}
                onclick={(e) => {
                  e.stopPropagation();
                  togglePin(vm.id);
                }}
                class="absolute bottom-2 left-2 w-6 h-6 rounded-full flex items-center justify-center backdrop-blur transition-all {pinned
                  ? 'bg-accent text-white opacity-100'
                  : 'bg-black/40 text-white/80 opacity-0 group-hover:opacity-100 hover:bg-black/60'}"
              >
                <Icon name="pin" size={12} />
              </button>
            {/if}
            <!-- v1.4 Fase 4: identity badge (PLAN-LXD 5.2) — top-right,
                 shifts left of the select checkbox when in select mode -->
            <div
              class="absolute top-2 {selectMode
                ? 'right-9'
                : 'right-2'} inline-flex items-center gap-1 px-1.5 py-0.5 rounded border text-[10px] uppercase tracking-wider font-medium {computeTypeBadgeClass(
                vm.type
              )}"
              title={isContainer(vm) ? 'Incus container' : 'KVM virtual machine'}
            >
              {#if isContainer(vm)}
                <Container class="w-3 h-3" />
              {:else}
                <Cpu class="w-3 h-3" />
              {/if}
              {computeTypeLabel(vm.type)}
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
              <div class="flex items-center gap-1.5 min-w-0 flex-1">
                <div class="font-medium text-sm truncate min-w-0" title={vm.alias || vm.name}>
                  {vm.alias || vm.name}
                </div>
                {#if vm.template}
                  <span
                    title={t('vms.templateBadgeTitle')}
                    class="shrink-0 inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[9px] font-semibold uppercase tracking-wider border border-warning/40 bg-warning-subtle text-warning"
                  >
                    <Icon name="layers" size={9} />
                    {t('vms.templateBadge')}
                  </span>
                {/if}
              </div>
              <div class="flex items-center gap-1.5 shrink-0">
                {#if vm.state === 'running' && vmIps(vm).length}
                  <span
                    class="font-mono text-[10px] text-accent truncate max-w-[100px]"
                    title={vmIps(vm).join(', ')}>{vmIps(vm)[0]}</span
                  >
                {/if}
                <div class="relative shrink-0">
                  <button
                    type="button"
                    aria-label={t('vms.quickActions')}
                    class="p-1 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted transition-colors {menuFor ===
                    vm.id
                      ? 'bg-muted text-foreground'
                      : ''}"
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
                      role="menu"
                      tabindex="-1"
                      class="absolute right-0 top-full z-50 mt-1 w-48 rounded-xl border border-border bg-popover text-popover-foreground shadow-2xl p-1 backdrop-blur-md"
                      onclick={(e) => e.stopPropagation()}
                      onkeydown={(e) => e.stopPropagation()}
                    >
                      {#if auth.canMutate()}
                        {#if vm.state === 'shutoff' && !vm.template}
                          <button
                            type="button"
                            class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                            disabled={quickBusy === `${vm.id}:start`}
                            onclick={() => quickAction(vm, 'start')}
                          >
                            {#if quickBusy === `${vm.id}:start`}<Spinner class="w-3.5 h-3.5" />{/if}
                            <span class="w-1.5 h-1.5 rounded-full bg-success"></span>
                            {quickBusy === `${vm.id}:start` ? t('vms.starting') : t('vms.start')}
                          </button>
                        {:else if vm.state !== 'shutoff'}
                          <button
                            type="button"
                            class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                            disabled={quickBusy === `${vm.id}:shutdown`}
                            onclick={() => quickAction(vm, 'shutdown')}
                          >
                            {#if quickBusy === `${vm.id}:shutdown`}<Spinner
                                class="w-3.5 h-3.5"
                              />{/if}
                            <span class="w-1.5 h-1.5 rounded-full bg-warning"></span>
                            {t('vms.shutdown')}
                          </button>
                          <button
                            type="button"
                            class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted text-destructive flex items-center gap-2"
                            disabled={quickBusy === `${vm.id}:forceoff`}
                            onclick={() => quickAction(vm, 'forceoff')}
                          >
                            {#if quickBusy === `${vm.id}:forceoff`}<Spinner
                                class="w-3.5 h-3.5"
                              />{/if}
                            <span class="w-1.5 h-1.5 rounded-full bg-destructive"></span>
                            {t('vms.forceOff')}
                          </button>
                        {/if}
                      {/if}
                      {#if !isContainer(vm)}
                        <button
                          type="button"
                          class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                          onclick={() => quickAction(vm, 'console')}
                        >
                          <Icon name="monitor" size={14} />
                          {t('vms.openConsole')}
                        </button>
                      {/if}
                      <button
                        type="button"
                        class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                        onclick={() => quickAction(vm, 'serial')}
                      >
                        <Icon name="terminal" size={14} />
                        Serial Console
                      </button>
                      <button
                        type="button"
                        class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                        onclick={() => navigate(`/vms/${vm.id}?tab=hardware`)}
                      >
                        <Icon name="settings" size={14} />
                        {t('vmDetail.hardware')}
                      </button>
                      <button
                        type="button"
                        class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                        onclick={() => navigate(`/vms/${vm.id}?tab=snapshots`)}
                      >
                        <Icon name="camera" size={14} />
                        {t('vmDetail.snapshots')}
                      </button>
                      {#if auth.canMutate()}
                        <button
                          type="button"
                          class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                          disabled={quickBusy === `${vm.id}:clone`}
                          onclick={() => quickAction(vm, 'clone')}
                        >
                          {#if quickBusy === `${vm.id}:clone`}<Spinner class="w-3.5 h-3.5" />{/if}
                          <Icon name="copy" size={14} />
                          {t('vms.clone')}
                        </button>
                        <button
                          type="button"
                          class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                          onclick={() => openAssignGroupForVm(vm)}
                        >
                          <Icon name="tag" size={14} />
                          {t('vms.assignToGroup')}
                        </button>
                        {#if vm.state === 'shutoff'}
                          <button
                            type="button"
                            class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                            disabled={quickBusy === `${vm.id}:template`}
                            onclick={() => quickAction(vm, 'template')}
                          >
                            {#if quickBusy === `${vm.id}:template`}<Spinner
                                class="w-3.5 h-3.5"
                              />{/if}
                            <Icon name="box" size={14} />
                            {t('vms.makeTemplateShort')}
                          </button>
                        {/if}
                      {/if}
                      <button
                        type="button"
                        class="w-full text-left text-sm px-2 py-1.5 rounded hover:bg-muted flex items-center gap-2"
                        onclick={() => navigate(`/vms/${vm.id}`)}
                      >
                        <Icon name="external-link" size={14} />
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
                <!-- v1.4 Fase 4: tertiary provisioning chip (PLAN-LXD 5.2) -->
                {#if chip}
                  <span
                    class="inline-flex items-center gap-1 shrink-0 text-muted-foreground/70"
                    title={`Provisioning: ${chip.label}`}
                  >
                    {#if chip.icon === 'cloud-cog'}
                      <CloudCog class="w-3 h-3" />
                    {:else if chip.icon === 'square-terminal'}
                      <SquareTerminal class="w-3 h-3" />
                    {:else}
                      <Box class="w-3 h-3" />
                    {/if}
                  </span>
                {/if}
              </span>
            </div>
            {#if vm.state === 'running' && (cpuPts.length > 0 || ramPts.length > 0)}
              {@const cpuNow = cpuPts.length ? cpuPts[cpuPts.length - 1].v : 0}
              {@const ramNow = ramPts.length ? ramPts[ramPts.length - 1].v : 0}
              {#if gaugeStyle.mode === 'radial'}
                <!-- Rings read faster across a grid of cards; the
                     sparkline history stays available on the detail
                     page, so it's dropped here to keep the card compact. -->
                <div
                  class="flex items-center justify-around gap-2 pt-1.5 border-t border-border/50"
                >
                  <Gauge value={cpuNow} label={t('vms.cpu')} size={46} />
                  <Gauge value={ramNow} label={t('common.ram')} size={46} />
                </div>
              {:else}
                <div class="grid grid-cols-1 sm:grid-cols-2 gap-2 pt-1.5 border-t border-border/50">
                  <div>
                    <Gauge value={cpuNow} label={t('vms.cpu')} variant="linear" />
                    <Chart
                      points={cpuPts}
                      yMax={100}
                      width={80}
                      height={18}
                      strokeWidth={1}
                      fillOpacity={0.2}
                    />
                  </div>
                  <div>
                    <Gauge value={ramNow} label={t('common.ram')} variant="linear" />
                    <Chart
                      points={ramPts}
                      yMax={100}
                      width={80}
                      height={18}
                      strokeWidth={1}
                      fillOpacity={0.2}
                      color="var(--success)"
                    />
                  </div>
                </div>
              {/if}
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

<!-- Group assignment dialog -->
<Dialog.Root open={showAssignGroup} onOpenChange={(v) => (showAssignGroup = v)}>
  <Dialog.Content>
    <Dialog.Header>
      <Dialog.Title>{t('vms.assignGroupTitle', { n: selectedKeys.size })}</Dialog.Title>
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
            showAssignGroup = false;
            openManageGroups();
          }}
        >
          {t('vms.manageGroups')}
        </Button>
      {:else}
        <!-- Can only pick existing groups (not free text). A VM can
             belong to more than one group, so each chip toggles
             independently — this adds every checked group, it doesn't
             replace whatever groups a VM already has. -->
        <div class="flex flex-wrap gap-1.5">
          {#each groups as g (g.name)}
            {@const active = assignGroupNames.has(g.name)}
            <button
              onclick={() => {
                const next = new Set(assignGroupNames);
                if (active) next.delete(g.name);
                else next.add(g.name);
                assignGroupNames = next;
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
      <Button variant="outline" onclick={() => (showAssignGroup = false)}
        >{t('common.cancel')}</Button
      >
      <Button disabled={assignGroupNames.size === 0} onclick={doAssignGroup}
        >{t('vms.assignBtn')}</Button
      >
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
          disabled={importPools.length === 0}
        >
          {#if importPools.length === 0}<option value="webkvm-disks">webkvm-disks</option>{/if}
          {#each importPools as p (p.name)}<option value={p.name}>{p.name}</option>{/each}
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

<!-- Import LXC dialog -->
<Dialog.Root bind:open={showImportLxc}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vms.importLxcTitle')}</Dialog.Title>
      <Dialog.Description>{t('vms.importLxcDesc')}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      <div>
        <label for="import-lxc-file" class="block text-sm font-medium mb-1.5"
          >{t('vms.backupOrCtar')}</label
        >
        <Input
          id="import-lxc-file"
          type="file"
          accept=".tar.gz,.tgz,.tar.zst,.zst"
          onchange={(e) => (importLxcFile = e.target.files?.[0] || null)}
        />
        {#if importLxcFile}
          <p class="text-xs text-muted-foreground mt-1.5">
            {importLxcFile.name} ({(importLxcFile.size / 1024 / 1024).toFixed(1)} MB)
          </p>
        {/if}
      </div>
      <div>
        <label for="import-lxc-name" class="block text-sm font-medium mb-1.5"
          >{t('vms.containerName')} <span class="text-destructive">*</span></label
        >
        <Input
          id="import-lxc-name"
          bind:value={importLxcName}
          placeholder={t('vms.containerNamePlaceholder')}
          required
        />
      </div>
      <div>
        <label for="import-lxc-pool" class="block text-sm font-medium mb-1.5"
          >{t('vms.storagePoolLabel')} <span class="text-destructive">*</span></label
        >
        <select
          id="import-lxc-pool"
          bind:value={importLxcPool}
          class="input"
          disabled={importLxcPools.length === 0}
          required
        >
          {#if importLxcPools.length === 0}
            <option value="webkvm-incus">webkvm-incus</option>
          {:else}
            {#each importLxcPools as p (p.name)}<option value={p.name}
                >{p.name} ({purposeLabel(p)})</option
              >{/each}
          {/if}
        </select>
      </div>
      <div>
        <label for="import-lxc-network" class="block text-sm font-medium mb-1.5"
          >{t('vms.networkLabel')} <span class="text-destructive">*</span></label
        >
        <select
          id="import-lxc-network"
          bind:value={importLxcNetwork}
          class="input"
          disabled={networks.length === 0}
          required
        >
          {#if networks.length === 0}
            <option value="">No networks available</option>
          {:else}
            {#each networks as n (n.name)}<option value={n.name}
                >{n.name} ({n.bridge || n.type})</option
              >{/each}
          {/if}
        </select>
      </div>
      {#if importErrorLxc}
        <p class="text-sm text-destructive">{importErrorLxc}</p>
      {/if}
      {#if importingLxc}
        <div class="bg-muted/30 rounded-md p-3 border border-border">
          <ProgressBar
            value={importProgressLxc}
            label={importPhaseLxc || t('vms.uploading')}
            showValue
            size="sm"
          />
        </div>
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showImportLxc = false)} disabled={importingLxc}
        >{t('common.cancel')}</Button
      >
      <Button
        onclick={doImportLxc}
        disabled={importingLxc ||
          !importLxcFile ||
          !importLxcName.trim() ||
          !importLxcPool ||
          !importLxcNetwork}
      >
        {#if importingLxc}<Spinner size="sm" color="text-white" />{:else}{t('vms.importLxc')}{/if}
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
              <option value={n.name}>{netDisplay(n)}</option>
            {/each}
          </select>
        </div>

        {#if instPoolOptions.length > 1}
          <!-- Only worth a control when there is an actual choice: with
               a single eligible pool the selector is a decision the
               operator cannot get wrong or right. -->
          <div class="space-y-1.5">
            <Label for="inst-pool">{t('vms.poolLabel')}</Label>
            <select id="inst-pool" bind:value={instPool} class="input w-full">
              <option value="">{t('vms.poolDefault')}</option>
              {#each instPoolOptions as p (p.name)}
                <option value={p.name}>{p.name}</option>
              {/each}
            </select>
            {#if instLinked && instPool}
              <!-- A linked clone keeps its backing file in the
                   template's pool. Landing the overlay in a different
                   pool makes the new VM depend on two pools at once:
                   losing or unmounting the template's pool kills it. -->
              <p class="text-[11px] text-warning flex items-start gap-1">
                <Icon name="alertTriangle" size={12} class="mt-0.5 shrink-0" />
                <span>{t('vms.linkedCrossPoolWarn')}</span>
              </p>
            {/if}
          </div>
        {/if}

        <!-- Clone strategy. Full is the safe default; linked trades
             independence for speed and space. -->
        <div class="space-y-1.5">
          <Label>{t('vms.cloneModeLabel')}</Label>
          <div class="grid grid-cols-2 gap-2">
            <button
              type="button"
              onclick={() => (instLinked = false)}
              class="flex flex-col items-start gap-0.5 p-2.5 rounded-lg border text-left transition-colors {!instLinked
                ? 'border-accent bg-accent/10'
                : 'border-border hover:bg-muted/30'}"
            >
              <span class="flex items-center gap-1.5 font-semibold text-xs">
                <Icon
                  name="copy"
                  size={13}
                  class={!instLinked ? 'text-accent' : 'text-muted-foreground'}
                />
                {t('vms.cloneFull')}
              </span>
              <span class="text-[10px] text-muted-foreground">{t('vms.cloneFullDesc')}</span>
            </button>
            <button
              type="button"
              onclick={() => (instLinked = true)}
              class="flex flex-col items-start gap-0.5 p-2.5 rounded-lg border text-left transition-colors {instLinked
                ? 'border-accent bg-accent/10'
                : 'border-border hover:bg-muted/30'}"
            >
              <span class="flex items-center gap-1.5 font-semibold text-xs">
                <Icon
                  name="zap"
                  size={13}
                  class={instLinked ? 'text-accent' : 'text-muted-foreground'}
                />
                {t('vms.cloneLinked')}
              </span>
              <span class="text-[10px] text-muted-foreground">{t('vms.cloneLinkedDesc')}</span>
            </button>
          </div>
          {#if instLinked}
            <p
              class="text-[11px] text-warning flex items-start gap-1.5 rounded-md border border-warning/30 bg-warning-subtle p-2"
            >
              <Icon name="shield" size={12} class="mt-0.5 shrink-0" />
              <span>{t('vms.cloneLinkedWarning')}</span>
            </p>
          {/if}
        </div>

        <label class="flex items-center gap-2 text-sm cursor-pointer select-none">
          <input type="checkbox" bind:checked={instCI} class="w-4 h-4 rounded border-border" />
          {t('vmCreate.cloudInitEnable')}
        </label>
        {#if instCI}
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label for="inst-ci-user" class="text-xs text-muted-foreground"
                >{t('vmCreate.cloudInitUser')}</label
              >
              <Input
                id="inst-ci-user"
                bind:value={instCIUser}
                placeholder="webkvm"
                class="w-full"
              />
            </div>
            <div>
              <label for="inst-ci-password" class="text-xs text-muted-foreground"
                >{t('common.password')} *</label
              >
              <div class="relative">
                <Input
                  id="inst-ci-password"
                  bind:value={instCIPassword}
                  type={showInstPass ? 'text' : 'password'}
                  placeholder={t('vmCreate.cloudInitPasswordPlaceholder')}
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
              <label for="inst-ci-hostname" class="text-xs text-muted-foreground"
                >{t('vmCreate.cloudInitHostname')}</label
              >
              <Input
                id="inst-ci-hostname"
                bind:value={instCIHostname}
                placeholder="my-vm"
                class="w-full"
              />
            </div>
            <div class="col-span-2">
              <label for="inst-ci-sshkey" class="text-xs text-muted-foreground"
                >{t('vmCreate.cloudInitSSHKey')}</label
              >
              <textarea
                id="inst-ci-sshkey"
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
                              <label
                                for={'appuser-' + app.id}
                                class="text-[11px] text-muted-foreground"
                                >{t('login.username')}</label
                              >
                              <Input
                                id={'appuser-' + app.id}
                                type="text"
                                class="w-full"
                                placeholder="webkvm"
                                bind:value={appCIUsers[app.id]}
                              />
                            </div>
                            <div>
                              <label
                                for={'apppass-' + app.id}
                                class="text-[11px] text-muted-foreground"
                                >{t('common.password')}</label
                              >
                              <div class="relative">
                                <Input
                                  id={'apppass-' + app.id}
                                  type={showAppPass[app.id] ? 'text' : 'password'}
                                  class="w-full pr-10"
                                  placeholder={t('vmCreate.cloudInitPasswordPlaceholder')}
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
                              <option value={n.name}>{netDisplay(n)}</option>
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
                              aria-label="Edit appliance"
                              title="Edit appliance"
                            >
                              <Pencil class="w-3.5 h-3.5" />
                            </button>
                            <button
                              type="button"
                              onclick={() => askDeleteApp(app)}
                              class="p-1.5 text-muted-foreground hover:text-destructive hover:bg-destructive/10 rounded-md transition-colors"
                              aria-label="Delete appliance"
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
<ManageGroupsDialog bind:open={showManageGroups} {groups} {palette} onChanged={onGroupsChanged} />

<!-- Appliance editor (create / edit) -->
<Dialog.Root bind:open={showAppEditor}>
  <Dialog.Content class="sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>
        {appEditorMode === 'create'
          ? t('appstore.addAppliance')
          : t('appstore.editAppliance', { id: appEditId })}
      </Dialog.Title>
      <Dialog.Description>
        {appEditorMode === 'create'
          ? t('appstore.addApplianceDesc')
          : t('appstore.editApplianceDesc')}
      </Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      {#if appEditorMode === 'create'}
        <div>
          <label for="app-id" class="text-xs font-medium text-muted-foreground"
            >{t('appstore.applianceId')}</label
          >
          <Input id="app-id" bind:value={appForm.id} placeholder="ubuntu-24.04" class="w-full" />
        </div>
      {/if}
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div>
          <label for="app-name" class="text-xs font-medium text-muted-foreground"
            >{t('appstore.applianceName')}</label
          >
          <Input
            id="app-name"
            bind:value={appForm.name}
            placeholder="Ubuntu Server"
            class="w-full"
          />
        </div>
        <div>
          <label for="app-cat" class="text-xs font-medium text-muted-foreground"
            >{t('appstore.applianceCategory')}</label
          >
          <select id="app-cat" bind:value={appForm.category} class="input w-full">
            <option value="cloud">Cloud OS</option>
            <option value="nas">Network storage</option>
            <option value="router">Router / firewall</option>
            <option value="home">Home automation</option>
            <option value="app">Application</option>
          </select>
        </div>
      </div>
      <div>
        <label for="app-url" class="text-xs font-medium text-muted-foreground"
          >{t('appstore.applianceUrl')}</label
        >
        <Input
          id="app-url"
          bind:value={appForm.url}
          placeholder="https://cloud-images.ubuntu.com/..."
          class="w-full font-mono text-xs"
        />
      </div>
      <div>
        <label for="app-desc" class="text-xs font-medium text-muted-foreground"
          >{t('appstore.applianceDescription')}</label
        >
        <Input id="app-desc" bind:value={appForm.description} class="w-full" />
      </div>
      <div class="rounded-lg border border-border/80 bg-muted/20 p-3 space-y-2">
        <div class="flex items-center justify-between mb-1">
          <label for="app-script" class="text-xs font-semibold flex items-center gap-1.5">
            <FileCode2 class="w-4 h-4" />
            {t('appstore.applianceProvisionScript')}
          </label>
          {#if appEditorMode === 'edit' && appIsBuiltin && appScript !== ''}
            <button
              type="button"
              class="text-xs underline text-muted-foreground hover:text-foreground"
              onclick={restoreOriginalScript}
              title={t('appstore.applianceRestoreOriginalHint')}
            >
              {t('appstore.applianceRestoreOriginal')}
            </button>
          {/if}
        </div>
        {#if appScriptLoading}
          <div class="h-32 flex items-center justify-center"><Spinner size="sm" /></div>
        {:else}
          <textarea
            id="app-script"
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
          {t('appstore.applianceProvisionHint')}
        </p>
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <div>
          <label for="app-format" class="text-xs font-medium text-muted-foreground"
            >{t('appstore.applianceFormat')}</label
          >
          <select id="app-format" bind:value={appForm.format} class="input w-full">
            <option value="qcow2">qcow2</option>
            <option value="raw">raw</option>
          </select>
        </div>
        <div>
          <label for="app-compression" class="text-xs font-medium text-muted-foreground"
            >{t('appstore.applianceCompression')}</label
          >
          <select id="app-compression" bind:value={appForm.compression} class="input w-full">
            <option value="none">none</option>
            <option value="gz">gz</option>
            <option value="xz">xz</option>
            <option value="bz2">bz2</option>
          </select>
        </div>
        <div>
          <span class="text-xs font-medium text-muted-foreground block"
            >{t('appstore.applianceCloudInit')}</span
          >
          <label class="flex items-center gap-2 text-sm mt-1.5 cursor-pointer">
            <input
              type="checkbox"
              bind:checked={appForm.cloud_init_supported}
              class="w-4 h-4 rounded border-border"
            />
            {t('appstore.applianceSupported')}
          </label>
        </div>
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <div>
          <label for="app-vcpus" class="text-xs font-medium text-muted-foreground"
            >{t('common.vcpu')}</label
          >
          <Input id="app-vcpus" type="number" bind:value={appForm.vcpus} class="w-full" />
        </div>
        <div>
          <label for="app-ram" class="text-xs font-medium text-muted-foreground"
            >{t('common.ram')} (MB)</label
          >
          <Input id="app-ram" type="number" bind:value={appForm.ram_mb} class="w-full" />
        </div>
        <div>
          <label for="app-disk" class="text-xs font-medium text-muted-foreground"
            >{t('common.size')} (GB)</label
          >
          <Input id="app-disk" type="number" bind:value={appForm.disk_gb} class="w-full" />
        </div>
      </div>
      <div>
        <label for="app-notes" class="text-xs font-medium text-muted-foreground"
          >{t('appstore.applianceNotes')}</label
        >
        <Input id="app-notes" bind:value={appForm.notes} class="w-full" />
      </div>
      {#if appEditorError}
        <p class="text-sm text-destructive">{appEditorError}</p>
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showAppEditor = false)} disabled={appSaving}>
        {t('common.cancel')}
      </Button>
      <Button onclick={saveAppliance} disabled={appSaving}>
        {#if appSaving}<Spinner size="sm" color="text-white" />{:else}{t('common.save')}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Appliance delete (double confirmation) -->
<Dialog.Root bind:open={showAppDelete}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('appstore.deleteAppliance')}</Dialog.Title>
      <Dialog.Description>
        {t('appstore.deleteApplianceConfirm', { name: appDeleteTarget?.name || '' })}
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
              <option value={n.name}>{netDisplay(n)}</option>
            {/each}
          </select>
        </div>
        <div class="space-y-1.5">
          <Label for="deploy-pool">{t('vms.deployPool')}</Label>
          <select
            id="deploy-pool"
            bind:value={appPools[deployApp.id]}
            class="input w-full"
            disabled={deployPools.length === 0}
          >
            {#if deployPools.length === 0}
              <option value="">{t('vms.deployPoolDefault')}</option>
            {/if}
            {#each deployPools as p (p.name)}
              <option value={p.name}>{p.name}</option>
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
      <Button
        disabled={deployBusy}
        onclick={() => deployAppliance(deployApp, appNames[deployApp.id]?.trim() || '')}
      >
        {#if deployBusy}<Spinner size="sm" color="text-white" />{:else}{t(
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
