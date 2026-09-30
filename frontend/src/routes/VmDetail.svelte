<script>
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import VmDetailSkeleton from '$lib/components/VmDetailSkeleton.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import PasswordModal from '$lib/components/PasswordModal.svelte';
  import CredentialsModal from '$lib/components/CredentialsModal.svelte';
  import TerminalPanel from '$lib/components/TerminalPanel.svelte';
  import CronPicker from '$lib/components/CronPicker.svelte';
  import Gauge from '$lib/components/Gauge.svelte';
  import CpuFlagPicker from '$lib/components/CpuFlagPicker.svelte';
  import CpuPrioritySelector from '$lib/components/CpuPrioritySelector.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import SettingRow from '$lib/components/SettingRow.svelte';
  import PciPreflightPanel from '$lib/components/PciPreflightPanel.svelte';
  import PciGroupCard from '$lib/components/PciGroupCard.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Tabs from '$lib/components/Tabs.svelte';
  import { upsertTask, updateTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { onMount, onDestroy } from 'svelte';
  import { fade } from 'svelte/transition';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import {
    capabilities,
    loadCapabilities,
    supports,
    unavailableReason,
  } from '$lib/stores/capabilities.svelte.js';
  import { t } from '../lib/i18n.svelte.js';
  import { vmDiskPools, deployablePools, movablePools } from '$lib/purpose.js';
  import {
    buildVMTemplate,
    mergeFirewall,
    VM_TEMPLATE_PRESETS,
  } from '$lib/utils/firewallTemplates.js';
  import { isContainer } from '$lib/utils/computeType.js';
  import { networkLabel, networkLabelFor } from '$lib/utils/networkLabel.js';
  import { vmIps } from '$lib/utils/vmIps.js';
  import { formatRate, formatBytes } from '$lib/utils/format.js';
  import { events } from '$lib/stores/events.svelte.js';
  import { navigate, getRoute } from '$lib/router.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button, buttonVariants } from '$lib/components/ui/button';
  import { cn } from '$lib/utils.js';
  import { Input } from '$lib/components/ui/input';
  import { Card } from '$lib/components/ui/card';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import DeleteVmDialog from '$lib/components/DeleteVmDialog.svelte';
  import * as Dialog from '$lib/components/ui/dialog';
  import Chart from '$lib/components/Chart.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import {
    Play,
    PowerOff,
    Power as PowerIcon,
    RotateCw,
    Pause,
    PlayCircle,
    Terminal,
    Trash2,
    CopyPlus,
    Pencil,
    Info,
    Download,
    KeyRound,
  } from '@lucide/svelte';

  let { vmId } = $props();

  let vm = $state(null);
  // v1.4 Fase 4: an LXD container has no KVM concepts (no chipset/UEFI/
  // TPM, no cdrom, no VNC, no per-disk attach). Sections and controls
  // below are gated on this derived flag (PLAN-LXD 5.4).
  const isContainerVm = $derived(vm && isContainer(vm));
  let snapshots = $state([]);
  let bootDevice = $state('hd');
  let loading = $state(true);
  let error = $state('');
  let actionLoading = $state('');
  // Firewall rules + port forwards (local editing state).
  let fwRules = $state([]);
  let fwForwards = $state([]);
  let fwSaving = $state(false);
  // VM metadata (template flag, owner, etc.).
  let vmMeta = $state(null);
  // Power & Snapshot schedule.
  let schedStart = $state('');
  let schedStop = $state('');
  let schedSnap = $state('');
  let schedSnapMax = $state(3);
  let schedSaving = $state(false);
  // Inline notice shown when the user tries an action that requires
  // the VM to be shut off while it isn't. Shown as a pop-up dialog.
  let blockedNotice = $state('');
  let showBlocked = $state(false);
  // Password reset modal.
  let showPasswordModal = $state(false);
  let resetUsername = $state('');
  let resetPasswordValue = $state('');
  // Reset password error dialog.
  let showResetError = $state(false);
  let resetError = $state('');
  // Derived: `actionLoading` is a string ('' when idle). Without this
  // coercion, `disabled={actionLoading}` becomes `disabled=""` in
  // the rendered HTML, which is truthy and dims every button even
  // when no action is in flight.
  const busy = $derived(!!actionLoading);

  // Autostart: separate flag from actionLoading because the
  // autostart toggle is a fast PATCH-style operation that
  // shouldn't dim the Start/Shutdown buttons while in flight.
  // The visual state lives on `vm.autostart` (the Switch
  // mirrors it via its `checked` prop); on failure we restore
  // the previous value.
  let autostartSaving = $state(false);

  let snapName = $state('');
  let snapDesc = $state('');
  let snapMemory = $state(false);

  // Metrics (Phase 21): in-memory time series per metric, updated via
  // SSE and on mount via a single REST fetch.
  let metrics = $state(null);
  const last60 = (arr) => (Array.isArray(arr) ? arr.slice(-60) : []);
  const cpuPoints = $derived(last60(metrics?.cpu?.points));
  const ramPoints = $derived(last60(metrics?.ram?.points));
  const diskRPoints = $derived(last60(metrics?.disk_read?.points));
  const diskWPoints = $derived(last60(metrics?.disk_write?.points));
  const netRxPoints = $derived(last60(metrics?.net_rx?.points));
  const netTxPoints = $derived(last60(metrics?.net_tx?.points));

  // QEMU guest-agent telemetry (OS, real in-guest filesystem usage and
  // every NIC address). Loaded on demand when the Guest tab is opened
  // — it's a synchronous round-trip to the agent, so it must not be
  // part of the page's initial load.
  let guestInfo = $state(null);
  let guestLoading = $state(false);

  async function loadGuestInfo() {
    if (!vmId || deleting) return;
    guestLoading = true;
    try {
      guestInfo = await api.getGuestInfo(vmId);
    } catch (e) {
      guestInfo = { available: false, error: e.message };
    } finally {
      guestLoading = false;
    }
  }

  async function runGuestFSTrim() {
    if (!vm || vm.state !== 'running') return;
    actionLoading = 'fstrim';
    try {
      const res = await api.guestFSTrim(vm.id);
      const totalTrimmed = (res.paths || []).reduce((acc, p) => acc + (p.trimmed || 0), 0);
      toast.success(t('vmDetail.fstrimSuccess', { size: formatBytes(totalTrimmed) }));
      await loadGuestInfo();
    } catch (e) {
      toast.error(e.message || t('vmDetail.fstrimError'));
    } finally {
      actionLoading = '';
    }
  }

  $effect(() => {
    if (activeSection === 'guest' && guestInfo === null && !guestLoading) {
      loadGuestInfo();
    }
  });

  // V13-C-03: metric history (downsampled 24h/7d/30d from the backend).
  let historyWindow = $state('24h');
  let history = $state(null); // VMMetrics-like series
  let historyLoading = $state(false);
  const historyCpu = $derived(history?.cpu?.points || []);
  const historyRam = $derived(history?.ram?.points || []);
  const historyDiskR = $derived(history?.disk_read?.points || []);
  const historyDiskW = $derived(history?.disk_write?.points || []);
  const historyNetRx = $derived(history?.net_rx?.points || []);
  const historyNetTx = $derived(history?.net_tx?.points || []);

  // V13-C-04: alert rules + live state machine for this VM.
  let alertRules = $state([]);
  let alertStatuses = $state([]);
  let alertsLoading = $state(false);

  // Hypervisor logs
  let vmLogs = $state('');
  let vmLogsLoading = $state(false);
  let vmLogsLines = $state(200);

  async function loadVMLogs() {
    if (!vmId || deleting) return;
    vmLogsLoading = true;
    try {
      const res = await api.getVMLogs(vmId, vmLogsLines);
      const text = res?.logs || '';
      // The backend reports a missing log file as an English
      // placeholder string; treat it as "no logs" so the translated
      // empty state renders instead.
      vmLogs = /^\(No hypervisor log file found/.test(text) ? '' : text;
    } catch (e) {
      vmLogs = `(${t('common.error')}: ${e.message})`;
    } finally {
      vmLogsLoading = false;
    }
  }

  function downloadVMLogs() {
    if (!vmLogs) return;
    const blob = new Blob([vmLogs], { type: 'text/plain;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${vm?.name || vmId}-qemu.log`;
    a.click();
    URL.revokeObjectURL(url);
  }

  async function loadHistory() {
    if (!vmId || deleting) return;
    historyLoading = true;
    try {
      history = await api.getVMMetricsHistory(vmId, historyWindow);
    } catch (err) {
      toast.error(err.message);
    } finally {
      historyLoading = false;
    }
  }

  async function loadAlerts() {
    if (!vmId || deleting) return;
    alertsLoading = true;
    try {
      const r = await api.getVMAlerterRules(vmId);
      alertRules = (r.rules || []).map((x) => ({ ...x }));
      alertStatuses = r.statuses || [];
    } catch (err) {
      toast.error(err.message);
    } finally {
      alertsLoading = false;
    }
  }

  function newAlertRule() {
    return {
      id: `al_${Date.now().toString(36)}`,
      name: '',
      metric: 'cpu',
      threshold: 90,
      above: true,
      duration_secs: 300,
      cooldown_secs: 3600,
      enabled: true,
    };
  }

  function ruleStatus(ruleId) {
    const s = alertStatuses.find((x) => x.rule?.id === ruleId);
    return s?.state || 'idle';
  }

  async function saveAlertRules() {
    try {
      await api.setVMAlerterRules(vmId, alertRules);
      toast.success(t('vmDetail.alertsSaved'));
      await loadAlerts();
    } catch (err) {
      toast.error(err.message);
    }
  }

  // Edit state
  // Hardware & Settings edits inline, in-place, inside the same card
  // (no floating dialog) — hwEditing toggles the whole "Hardware &
  // Ajustes" BlockCard between its read-only inventory view and the
  // editable form built from the same fields/tabs used to.
  let hwEditing = $state(false);
  let editTab = $state('general');
  let eName = $state('');
  let eVcpus = $state(2);
  let eRamMB = $state(2048);
  let eMinRamMB = $state(0);
  let eIOThreads = $state(0);
  let eCPUMode = $state('host-passthrough');
  let eCPUModel = $state('');
  let eCPUUnits = $state(1024);
  let eKVMHidden = $state(false);
  let eCPUFlags = $state([]);

  const COMMON_CPU_FLAGS = [
    { value: 'aes', label: 'aes', desc: 'AES-NI Hardware Encryption' },
    { value: 'avx2', label: 'avx2', desc: 'Advanced Vector Extensions 2' },
    { value: 'topoext', label: 'topoext', desc: 'AMD SMT / Core Topology' },
    { value: 'pdpe1gb', label: 'pdpe1gb', desc: '1 GB Hugepages Support' },
    { value: 'pcid', label: 'pcid', desc: 'Process-Context Identifiers (faster TLB)' },
    {
      value: 'hypervisor',
      label: 'hypervisor',
      desc: 'Hide Hypervisor Feature Bit (default: off)',
    },
  ];

  const cpuModelPresets = [
    'EPYC-Genoa',
    'EPYC-Milan',
    'EPYC-Rome',
    'EPYC',
    'SapphireRapids',
    'Icelake-Server',
    'Cascadelake-Server',
    'Skylake-Server',
    'Skylake-Client',
    'Broadwell-noTSX',
    'Broadwell',
    'Haswell-noTSX',
    'Haswell',
    'IvyBridge',
    'SandyBridge',
    'Nehalem',
    'Westmere',
    'Penryn',
    'Conroe',
    'core2duo',
    'Opteron_G5',
    'athlon',
    'phenom',
    'kvm64',
    'qemu64',
  ];

  let eVideoModel = $state('virtio');
  let eAudioModel = $state('none');
  let eSerialPort = $state(true);
  let eAutostart = $state(false);
  let eSelectedGroups = $state([]);
  let eNetwork = $state('default');
  let eNetworkModel = $state('virtio');
  let eChipset = $state('q35');
  let eSecureBoot = $state(false);
  let eTPM = $state(false);
  let eTPMVersion = $state('2.0');
  let eWatchdog = $state(false);
  let eFirmware = $state('uefi');
  let eOSType = $state('');
  let eOSVersion = $state('');
  // Fase 5: advanced edit state.
  let eBootOrder = $state('disk');
  let ePrivileged = $state(false);
  let eNesting = $state(false);
  let eProfiles = $state(['default']);
  let incusProfiles = $state([]);
  let editSaving = $state(false);

  const networkModels = [
    { value: 'virtio', label: 'virtio (recommended)' },
    { value: 'e1000e', label: 'e1000e (Intel, ideal for Windows)' },
    { value: 'e1000', label: 'e1000 (legacy Intel)' },
    { value: 'rtl8139', label: 'rtl8139 (Realtek, very compatible)' },
    { value: 'pcnet', label: 'pcnet (AMD, legacy)' },
  ];

  const audioModels = [
    { value: 'none', label: 'Sin audio (Desactivado)' },
    { value: 'ich9', label: 'Intel ICH9 HD Audio (Recomendado)' },
    { value: 'ac97', label: 'AC97 Audio (Legacy)' },
    { value: 'es1370', label: 'Ensoniq ES1370' },
  ];

  const bootOrderOptions = $derived([
    { value: 'disk', label: t('vmCreate.bootOrderDisk') },
    { value: 'cdrom', label: t('vmCreate.bootOrderCdrom') },
    { value: 'network', label: t('vmCreate.bootOrderNetwork') },
  ]);

  const quickRamPresets = [1024, 2048, 4096, 8192, 16384, 32768, 65536];

  // Add Disk state
  let showAddDisk = $state(false);
  let aDiskDevice = $state('disk');
  let aDiskBus = $state('virtio');
  let aDiskSize = $state(10);
  let aDiskPool = $state('webkvm-disks');
  let aDiskISO = $state('');
  let aDiskFormat = $state('qcow2');
  let aDiskVolumes = $state([]);
  let aDiskExistingVol = $state('');
  let aDiskZVol = $state('');
  let aDiskZVols = $state([]);
  let aDiskZVolsLoading = $state(false);
  let aDiskZVolCustom = $state(false);
  // Result of probing the selected existing disk (null = not probed yet).
  // Used to warn that the image already contains data before attaching.
  let aDiskProbe = $state(null);
  let aDiskProbing = $state(false);
  let aDiskForce = $state(false);
  let aDiskWWN = $state('');
  let aDiskSerial = $state('');
  let aDiskAlias = $state('');

  // Change ISO state
  let showChangeISO = $state(false);
  let cISOTarget = $state('');
  let cISOSource = $state('');

  // Resize Disk state
  let showResizeDisk = $state(false);
  let resizeDiskTarget = $state('');
  let resizeDiskSize = $state(10);
  let resizeDiskCurrent = $state(0);

  // Change Disk Bus state
  let showChangeBus = $state(false);
  let changeBusTarget = $state('');
  let changeBusCurrent = $state('');
  let changeBusNew = $state('virtio');

  // Add Net state
  let showAddNet = $state(false);
  let aNetNetwork = $state('default');
  let aNetModel = $state('virtio');

  // Clone state
  let showClone = $state(false);
  let cName = $state('');
  let cPool = $state('webkvm-disks');
  let cLinked = $state(false);

  // Move-storage state. Unlike a clone this produces no new instance:
  // the same VM ends up with its disks in a different pool.
  let showMove = $state(false);
  let movePool = $state('');
  let moveProgress = $state(null); // { pct, stage } while the job runs
  // Where the instance's storage lives now, read off its first writable
  // disk. Used only to drop that pool from the destination list — the
  // backend is the authority and rejects a same-pool move outright.
  const currentPool = $derived(
    (vm?.disks || []).find((d) => d.device !== 'cdrom' && d.pool)?.pool || ''
  );
  // Only pools of the instance's own world are offered: a VM cannot
  // live in a container pool and vice versa.
  //
  // A VM flagged as a template gains one extra destination: the
  // template pools, which exist to keep golden images apart from the
  // disks in daily use. Containers never get them — Incus owns their
  // storage and knows nothing of a libvirt template pool.
  const movePoolOptions = $derived(
    [
      ...deployablePools(pools, isContainerVm ? 'container' : 'vm'),
      ...(vmMeta?.template && !isContainerVm ? movablePools(pools, 'template') : []),
    ].filter((p) => p.name !== currentPool)
  );

  // Cloud-Init tab state
  let ciStatus = $state(null); // { has_seed_iso, cdrom_dev, user, supported }
  let ciStatusLoading = $state(false);
  let ciUser = $state('');
  let ciPassword = $state('');
  let ciSSHKey = $state('');
  let ciHostname = $state('');
  let ciApplying = $state(false);
  let ciSnippetIds = $state([]);
  let ciAvailableSnippets = $state([]);

  async function loadCloudInitStatus() {
    if (isContainerVm) return;
    ciStatusLoading = true;
    try {
      ciStatus = await api.getCloudInitStatus(vmId);
      if (ciStatus?.user && !ciUser) ciUser = ciStatus.user;
    } catch (_e) {
      ciStatus = null;
    } finally {
      ciStatusLoading = false;
    }
  }

  async function loadCloudInitSnippets() {
    if (ciAvailableSnippets.length) return;
    try {
      const list = await api.listCloudInitSnippets();
      ciAvailableSnippets = list || [];
    } catch (_e) {
      ciAvailableSnippets = [];
    }
  }

  async function reapplyCloudInit() {
    if (!ciUser && !ciSSHKey && !ciHostname && ciSnippetIds.length === 0) {
      toast.warning(t('vmDetail.cloudInitEmptyWarning'));
      return;
    }
    if (ciUser && !ciPassword) {
      toast.warning(t('vmDetail.cloudInitPasswordRequired'));
      return;
    }
    ciApplying = true;
    try {
      const payload = {
        user: ciUser || undefined,
        password: ciPassword || undefined,
        ssh_key: ciSSHKey || undefined,
        hostname: ciHostname || undefined,
        snippet_ids: ciSnippetIds.length ? ciSnippetIds : undefined,
      };
      const res = await api.reapplyCloudInit(vmId, payload);
      ciPassword = '';
      toast.success(
        res?.reprovisioned
          ? t('vmDetail.cloudInitAppliedLive')
          : t('vmDetail.cloudInitAppliedNeedsReboot')
      );
      await loadCloudInitStatus();
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      ciApplying = false;
    }
  }

  // Export state
  let showExport = $state(false);
  let exportTarget = $state('vmware');
  let exportProgress = $state(null);
  let exportAbort = $state(null);

  // Identity / metadata state (Phase 16)
  let showIdentity = $state(false);
  let identityTab = $state('alias'); // 'alias' | 'cover' | 'network' | 'notes' | 'groups'
  let eAlias = $state('');
  let eNotes = $state('');
  let eNotesOriginal = $state(''); // tracks the last-saved value for blur autosave
  // Selected group names for this VM. A Set, not free text — a group
  // name can itself contain spaces (e.g. "APPS WEBS"), and the old
  // comma-or-space-separated text field silently split those into
  // separate, unregistered tags that never matched the real group.
  let eGroupsSet = $state(new Set());
  let eGroupsList = $state([]); // groups available to assign

  let coverFile = $state(null);
  let coverPreview = $state(null);
  let uploadingCover = $state(false);
  // The cover picker reads from the media library instead of keeping its
  // own private store of images: an image uploaded once should be usable
  // as a cover here, as an avatar, or both, without a second copy.
  let mediaItems = $state([]);
  let mediaLoading = $state(false);
  let mediaError = $state('');
  let vlanSupportByNetwork = $state({}); // networkName -> { supported, reason }
  let ifaceEdits = $state({}); // mac -> { mac, network, vlan, busy, error }
  let savingIdentity = $state(false);
  let notesStatus = $state(''); // '' | 'saving' | 'saved' | 'error'
  let notesError = $state('');

  // Confirm dialogs
  let confirmState = $state({
    open: false,
    title: '',
    description: '',
    confirmLabel: t('common.confirm'),
    variant: 'default',
    onConfirm: () => {},
    loading: false,
  });

  let pools = $state([]);
  let networks = $state([]);
  let isos = $state([]);
  // Current user's network allowlist (null until loaded). Empty/absent or
  // admin = every network; otherwise restricts the dropdowns below. Mirrors
  // VmCreate.svelte's myAllowedNetworks/vmNetworks exactly.
  let myAllowedNetworks = $state(null);
  const vmNetworks = $derived.by(() => {
    if (auth.role === 'admin') return networks;
    if (myAllowedNetworks && myAllowedNetworks.length) {
      return networks.filter((n) => myAllowedNetworks.includes(n.name));
    }
    return networks;
  });

  // Which of the 4 tabs is showing. 'overview' bundles the spec/metrics/
  // serial-console/firewall/schedule cards that used to be freestanding
  // blocks — see BlockCard/vmLayout.svelte.js.
  let activeSection = $state('overview');
  const sectionTabs = $derived(
    [
      { id: 'overview', label: t('vmDetail.overview') },
      { id: 'hardware', label: t('vmDetail.hardware') },
      !isContainerVm ? { id: 'cloudinit', label: 'Cloud-Init' } : null,
      { id: 'disks', label: t('vmDetail.disks') },
      { id: 'net', label: t('vmDetail.networkInterfaces') },
      // Guest-agent telemetry is a KVM-only concept: containers have
      // no qemu-guest-agent and the host sees their state directly.
      !isContainerVm ? { id: 'guest', label: t('vmDetail.guestTab') } : null,
      { id: 'history', label: t('vmDetail.history') },
      { id: 'alerts', label: t('vmDetail.alerts') },
      { id: 'snaps', label: t('vmDetail.snapshots') },
    ].filter(Boolean)
  );

  // Serial console lives in the Overview tab; switch to it (if we're
  // elsewhere) before scrolling so the target isn't display:none.
  function gotoSerial() {
    activeSection = 'overview';
    queueMicrotask(() => {
      document.getElementById('vm-serial-block')?.scrollIntoView({
        behavior: 'smooth',
        block: 'start',
      });
    });
  }

  async function openConsole() {
    try {
      const { vnc_ticket } = await api.getVNCTicket(vmId);
      window.open(
        `/console/${vmId}?vt=${encodeURIComponent(vnc_ticket)}`,
        '_blank',
        'noopener,noreferrer'
      );
    } catch (e) {
      toast.error(e.message);
    }
  }

  // Set while a delete is in flight / done: every loader and event
  // handler bails out so the page stops hitting endpoints of a VM that
  // no longer exists (/boot 500, /metrics 404) before navigating away.
  // Not $state — nothing renders from it.
  let deleting = false;
  let unsubscribeEvents = () => {};

  function onDeleteStart() {
    deleting = true;
  }
  function onDeleteFailed() {
    deleting = false;
  }
  function onVmDeleted() {
    deleting = true;
    unsubscribeEvents();
    clearAllTimers();
    navigate('/vms');
  }

  onMount(() => {
    // A route without an id (e.g. an inaccessible VM link) must not
    // fire /api/vms/undefined/* requests.
    if (!vmId) {
      loading = false;
      return;
    }
    loadCapabilities();
    load();
    loadMetrics();
    loadAlerts();
    // Deep link: /vms/:id?serial=1 scrolls to the embedded serial console.
    if (getRoute().query?.serial === '1') later(gotoSerial, 400);
    const offMetrics = events.onVmMetrics((e) => {
      if (deleting || e.vm_id !== vmId) return;
      metrics = e.data;
    });

    // Subscribe to VM state events for this VM
    const off = events.onVmState((e) => {
      if (deleting || e.vm_id !== vmId) return;
      if (vm && vm.state !== e.state) {
        vm = { ...vm, state: e.state, name: e.name || vm.name };
        // Light refetch to update uptime/ip
        load(true);
      }
    });
    let unsubscribed = false;
    unsubscribeEvents = () => {
      if (unsubscribed) return;
      unsubscribed = true;
      off();
      offMetrics();
    };
    return unsubscribeEvents;
  });

  // One-shot timers (deep-link scroll, notes/export feedback)
  // are tracked and cancelled on unmount.
  let timers = [];

  function later(fn, ms) {
    const id = setTimeout(() => {
      timers = timers.filter((x) => x !== id);
      fn();
    }, ms);
    timers.push(id);
    return id;
  }

  function clearAllTimers() {
    for (const id of timers) clearTimeout(id);
    timers = [];
  }

  onDestroy(() => {
    clearAllTimers();
  });

  async function loadMetrics() {
    if (!vmId || deleting) return;
    try {
      metrics = await api.getVMMetrics(vmId);
    } catch {
      metrics = null;
    }
  }

  async function load(silent = false) {
    if (!vmId || deleting) return;
    if (!silent) loading = true;
    error = '';
    try {
      const [vmData, snapData, bootData] = await Promise.all([
        api.getVM(vmId),
        api.listSnapshots(vmId).catch(() => []),
        api.getBootDevice(vmId).catch(() => ({ boot_device: 'hd' })),
      ]);
      vm = vmData;
      snapshots = snapData;
      if (bootData) bootDevice = bootData.boot_device;
      api
        .getVMMeta(vmId)
        .then((m) => (vmMeta = m))
        .catch(() => {});
      api
        .getVMFirewall(vmId)
        .then((fw) => {
          fwRules = (fw.rules || []).map((r) => ({ ...r }));
          fwForwards = (fw.forwards || []).map((f) => ({ ...f }));
        })
        .catch(() => {});
      api
        .getVMSchedule(vmId)
        .then((sc) => {
          schedStart = sc.start_cron || '';
          schedStop = sc.stop_cron || '';
          schedSnap = sc.snapshot_cron || '';
          schedSnapMax = sc.snapshot_max || 3;
        })
        .catch(() => {});
      Promise.all([
        api
          .listNetworks()
          .then((n) => (networks = n))
          .catch(() => {}),
        api
          .listPools()
          .then((p) => (pools = p))
          .catch(() => {}),
        api
          .listISOs()
          .then((i) => (isos = i))
          .catch(() => {}),
        api
          .me()
          .then((me) => (myAllowedNetworks = me?.allowed_networks || []))
          .catch(() => (myAllowedNetworks = [])),
        api
          .getVMMeta(vmId)
          .then((m) => {
            // Cover changes are picked up by re-render, alias/groups
            // in vm are already populated from ListVMs. We only need
            // to keep them in sync after detail reloads.
            if (vm && m) {
              vm = { ...vm, alias: m.alias || '', cover: m.cover || '', groups: m.groups || [] };
            }
          })
          .catch(() => {}),
      ]);
    } catch (e) {
      if (
        e.status === 403 ||
        (e.message && (e.message.toLowerCase().includes('forbidden') || e.message.includes('403')))
      ) {
        toast.error(t('vmDetail.noAccess'));
        error = '';
        navigate('/vms');
        return;
      }
      error = e.message;
    } finally {
      if (!silent) loading = false;
    }
  }

  async function openIdentity() {
    identityTab = 'alias';
    coverFile = null;
    coverPreview = null;
    notesStatus = '';
    notesError = '';
    try {
      const meta = await api.getVMMeta(vmId);
      eAlias = meta.alias || '';
      eNotes = meta.notes || '';
      eNotesOriginal = meta.notes || '';
      eGroupsSet = new Set(meta.groups || []);
    } catch {
      eAlias = vm?.alias || '';
      eNotes = '';
      eNotesOriginal = '';
      eGroupsSet = new Set(vm?.groups || []);
    }
    // Initialize iface edit state for each network interface.
    const edits = {};
    for (const iface of vm.networks || []) {
      edits[iface.mac] = {
        mac: iface.mac,
        network: iface.network,
        vlan: '',
        busy: false,
        error: '',
      };
    }
    ifaceEdits = edits;
    vlanSupportByNetwork = {};
    // Pre-fetch VLAN support for each network this VM uses.
    const uniqueNets = [...new Set((vm.networks || []).map((n) => n.network).filter(Boolean))];
    await Promise.all(
      uniqueNets.map(async (net) => {
        try {
          vlanSupportByNetwork[net] = await api.checkVLANSupport(net);
        } catch (e) {
          vlanSupportByNetwork[net] = { supported: false, reason: e.message };
        }
      })
    );
    // Load available groups (for the alias/cover tab also has a groups list).
    try {
      const grp = await api.listGroups();
      eGroupsList = grp.groups || [];
    } catch {
      eGroupsList = [];
    }
    showIdentity = true;
    if (identityTab === 'cover') loadMediaLibrary();
  }

  async function saveIdentityBasics() {
    // Save alias + notes + groups in a single PUT.
    savingIdentity = true;
    try {
      const groups = Array.from(eGroupsSet);
      await api.updateVMMeta(vmId, {
        alias: eAlias,
        notes: eNotes,
        groups: groups,
      });
      eNotesOriginal = eNotes;
      vm = { ...vm, alias: eAlias, groups };
      toast.success('Identity updated');
      // Close on success — leaving the dialog open with no visible
      // change (besides a toast easy to miss) read as "the button
      // doesn't do anything" even though the save worked. On error,
      // stay open so the message and fields remain visible to retry.
      showIdentity = false;
    } catch (e) {
      toast.error(e.message);
    } finally {
      savingIdentity = false;
    }
  }

  // Notes blur-autosave: only fires if the value actually changed since
  // the last save (openIdentity / successful save updates eNotesOriginal).
  async function saveNotesIfChanged() {
    if (eNotes === eNotesOriginal) return;
    notesStatus = 'saving';
    notesError = '';
    try {
      await api.updateVMMeta(vmId, { notes: eNotes });
      eNotesOriginal = eNotes;
      notesStatus = 'saved';
      later(() => {
        if (notesStatus === 'saved') notesStatus = '';
      }, 2000);
    } catch (e) {
      notesStatus = 'error';
      notesError = e.message;
    }
  }

  function onCoverPicked(e) {
    const f = e.target.files?.[0];
    if (!f) return;
    coverFile = f;
    const reader = new FileReader();
    reader.onload = () => (coverPreview = reader.result);
    reader.readAsDataURL(f);
  }

  async function loadMediaLibrary() {
    if (mediaLoading) return;
    mediaLoading = true;
    mediaError = '';
    try {
      const res = await api.listMedia();
      const items = Array.isArray(res) ? res : res?.items || [];
      // Only raster formats make sense as a cover, matching what the
      // upload path accepts.
      mediaItems = items.filter((i) => !/\.(svg|ico)$/i.test(i.id || ''));
    } catch (e) {
      mediaError = e.message;
    } finally {
      mediaLoading = false;
    }
  }

  // Picking an existing image never copies bytes: it stores a reference
  // to the library item, so deleting or reusing it stays coherent.
  async function pickCoverFromLibrary(item) {
    uploadingCover = true;
    try {
      const res = await api.applyMediaUsage({
        media_id: item.id,
        usage: 'vm_cover',
        vm_id: vmId,
      });
      vm = { ...vm, cover: res.url };
      coverFile = null;
      coverPreview = null;
      toast.success(t('vmDetail.coverUpdated'));
    } catch (e) {
      toast.error(e.message);
    } finally {
      uploadingCover = false;
    }
  }

  // Uploading from the cover tab goes through the media library, so the
  // image shows up in Multimedia like any other. The old per-VM upload
  // wrote to a separate directory that nothing else could see.
  async function uploadCover() {
    if (!coverFile) return;
    uploadingCover = true;
    try {
      const item = await api.uploadMedia(coverFile);
      const res = await api.applyMediaUsage({
        media_id: item.id,
        usage: 'vm_cover',
        vm_id: vmId,
      });
      vm = { ...vm, cover: res.url };
      toast.success(t('vmDetail.coverUpdated'));
      coverFile = null;
      coverPreview = null;
      loadMediaLibrary();
    } catch (e) {
      toast.error(e.message);
    } finally {
      uploadingCover = false;
    }
  }

  async function removeCover() {
    try {
      await api.deleteCover(vmId);
      vm = { ...vm, cover: '' };
      toast.success(t('vmDetail.coverRemoved'));
    } catch (e) {
      toast.error(e.message);
    }
  }

  async function saveIface(mac) {
    if (!requireShutoff(t('vmDetail.requireShutoffEditingNic'))) return;
    const cur = ifaceEdits[mac];
    if (!cur) return;
    cur.error = '';
    cur.busy = true;
    ifaceEdits = { ...ifaceEdits };
    const newMac = cur.mac.trim();
    const vlanRaw = cur.vlan.trim();
    let vlanTag = null;
    if (vlanRaw !== '') {
      const n = parseInt(vlanRaw, 10);
      if (isNaN(n) || n < 0 || n > 4094) {
        cur.error = t('vmDetail.vlanTagRange');
        cur.busy = false;
        ifaceEdits = { ...ifaceEdits };
        return;
      }
      vlanTag = n;
    }
    const payload = {};
    if (newMac && newMac !== mac) payload.mac = newMac;
    if (cur.network && cur.network !== (vm.networks.find((i) => i.mac === mac)?.network || '')) {
      payload.network = cur.network;
    }
    if (vlanTag !== null) payload.vlan_tag = vlanTag;
    if (Object.keys(payload).length === 0) {
      cur.busy = false;
      ifaceEdits = { ...ifaceEdits };
      toast.info(t('vmDetail.nothingToSave'));
      return;
    }
    try {
      await api.updateNetIface(vmId, mac, payload);
      toast.success(t('vmDetail.networkInterfaceUpdated'));
      await load();
      // Re-seed the edit state for the (possibly new) MAC.
      const updatedIface = (vm.networks || []).find(
        (i) => i.mac === newMac || i.mac === cur.network
      );
      if (newMac && newMac !== mac) {
        delete ifaceEdits[mac];
      }
      if (updatedIface) {
        ifaceEdits[updatedIface.mac] = {
          mac: updatedIface.mac,
          network: updatedIface.network,
          vlan: '',
          busy: false,
          error: '',
        };
      }
    } catch (e) {
      // Revert: keep the form value the user typed, but show the error.
      cur.error = e.message;
    } finally {
      cur.busy = false;
      ifaceEdits = { ...ifaceEdits };
    }
  }

  function openEdit() {
    editTab = 'general';
    eName = vm.name;
    eVcpus = vm.vcpus;
    eRamMB = vm.ram_mb;
    eMinRamMB = vm.min_ram_mb || 0;
    eIOThreads = vm.iothreads || 0;
    eCPUMode = vm.cpu_mode || 'host-passthrough';
    eCPUModel = vm.cpu_model || '';
    eCPUUnits = vm.cpu_units || 1024;
    eKVMHidden = !!vm.kvm_hidden;
    eCPUFlags = Array.isArray(vm.cpu_flags) ? [...vm.cpu_flags] : [];
    eVideoModel = vm.video_model || 'virtio';
    eAudioModel = vm.audio_model || 'none';
    eSerialPort = vm.serial_port !== false;
    eNetwork = vm.networks?.[0]?.network || vmNetworks[0]?.name || 'default';
    eNetworkModel = vm.networks?.[0]?.model || 'virtio';
    eChipset = vm.chipset || 'q35';
    eSecureBoot = vm.secure_boot;
    eTPM = vm.tpm_enabled;
    eTPMVersion = vm.tpm_version || '2.0';
    eWatchdog = vm.watchdog_enabled || false;
    eFirmware = vm.chipset === 'i440fx' ? 'seabios' : vm.firmware || 'seabios';
    if (eChipset === 'i440fx') {
      eSecureBoot = false;
      eTPM = false;
    }
    eOSType = vm.os_type || '';
    eOSVersion = vm.os_version || '';
    eBootOrder = vm.boot_order || 'disk';
    eAutostart = !!vm.autostart;
    eSelectedGroups = Array.isArray(vm.groups) ? [...vm.groups] : [];
    ePrivileged = vm.privileged;
    eNesting = vm.nesting;
    eProfiles = vm.profiles?.length ? vm.profiles : ['default'];
    if (isContainerVm && !incusProfiles.length) {
      api
        .listIncusProfiles()
        .then((pr) => {
          if (pr?.profiles?.length) incusProfiles = pr.profiles;
        })
        .catch(() => {});
    }
    if (eGroupsList.length === 0) {
      api
        .listGroups()
        .then((grp) => {
          if (grp?.groups?.length) eGroupsList = grp.groups;
        })
        .catch(() => {});
    }
    // Editing lives inside the "Hardware & Ajustes" tab now (inline,
    // no floating dialog) — jump there so the form is actually visible
    // regardless of which tab/button triggered openEdit().
    activeSection = 'hardware';
    hwEditing = true;
  }

  function openEditTab(tab = 'general') {
    openEdit();
    editTab = tab;
  }

  async function saveEdit() {
    editSaving = true;
    try {
      const data = {};
      if (eName !== vm.name) data.name = eName;
      if (eVcpus !== vm.vcpus) data.vcpus = eVcpus;
      if (eRamMB !== vm.ram_mb) data.ram_mb = eRamMB;
      if (!isContainerVm) {
        if (eMinRamMB !== (vm.min_ram_mb || 0)) data.min_ram_mb = eMinRamMB;
        if (eIOThreads !== (vm.iothreads || 0)) data.iothreads = eIOThreads;
      }
      if (eCPUMode !== (vm.cpu_mode || 'host-passthrough')) data.cpu_mode = eCPUMode;
      if (eCPUMode === 'custom' && eCPUModel !== (vm.cpu_model || '')) data.cpu_model = eCPUModel;
      if (eCPUUnits !== (vm.cpu_units || 1024)) data.cpu_units = eCPUUnits;
      if (eKVMHidden !== !!vm.kvm_hidden) data.kvm_hidden = eKVMHidden;
      if (JSON.stringify(eCPUFlags) !== JSON.stringify(vm.cpu_flags || []))
        data.cpu_flags = eCPUFlags;
      if (eVideoModel !== (vm.video_model || 'virtio')) data.video_model = eVideoModel;
      if (eAudioModel !== (vm.audio_model || 'none')) data.audio_model = eAudioModel;
      if (eSerialPort !== (vm.serial_port !== false)) data.serial_port = eSerialPort;
      if (eNetwork !== (vm.networks?.[0]?.network || vmNetworks[0]?.name || 'default'))
        data.network = eNetwork;
      if (eNetworkModel !== (vm.networks?.[0]?.model || 'virtio'))
        data.network_model = eNetworkModel;
      if (eOSType !== (vm.os_type || '')) data.os_type = eOSType;
      if (eOSVersion !== (vm.os_version || '')) data.os_version = eOSVersion;
      const effSecureBoot = eFirmware === 'uefi' ? eSecureBoot : false;
      const effTPM = eFirmware === 'uefi' ? eTPM : false;
      if (effSecureBoot !== vm.secure_boot) data.secure_boot = effSecureBoot;
      if (effTPM !== vm.tpm_enabled) data.tpm_enabled = effTPM;
      if (effTPM && eTPMVersion !== (vm.tpm_version || '2.0')) data.tpm_version = eTPMVersion;
      if (eWatchdog !== (vm.watchdog_enabled || false)) data.watchdog_enabled = eWatchdog;
      if (eFirmware !== (vm.firmware || 'uefi')) data.firmware = eFirmware;
      if (eBootOrder !== (vm.boot_order || 'disk')) data.boot_order = eBootOrder;
      if (eAutostart !== !!vm.autostart) data.autostart = eAutostart;
      if (ePrivileged !== vm.privileged) data.privileged = ePrivileged;
      if (eNesting !== vm.nesting) data.nesting = eNesting;
      const curProfiles = JSON.stringify(vm.profiles || ['default']);
      if (JSON.stringify(eProfiles || []) !== curProfiles) data.profiles = eProfiles;

      await api.updateVM(vmId, data);

      const oldGroups = Array.isArray(vm.groups) ? vm.groups : [];
      if (JSON.stringify(oldGroups.sort()) !== JSON.stringify([...eSelectedGroups].sort())) {
        await api.updateVMMeta(vmId, { groups: eSelectedGroups }).catch(() => {});
      }

      hwEditing = false;
      toast.success(t('vmDetail.settingsUpdated'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      editSaving = false;
    }
  }

  function askConfirm(opts) {
    confirmState = { ...opts, open: true, loading: false };
  }

  // Delete flow: see lib/components/DeleteVmDialog.svelte. VmDetail only
  // owns the trigger flag and the disks it passes in as a prop.
  let showDeleteFlow = $state(false);
  const vmDisks = $derived(vm?.disks?.filter((d) => d.device === 'disk') ?? []);

  let showAppCreds = $state(false);

  // Visible diagnostics: any render/JS error shows as a toast (this is
  // how the "JS: TypeError ..." overlay originated). Keeping it in one
  // effect avoids the never-assigned boundaryMsg/boundaryStack block
  // that used to render a second, dead error panel.
  $effect(() => {
    const onErr = (e) => {
      const msg = (e && (e.message || e.reason)) || 'error desconocido';
      console.error('[WebKVM UI]', e);
      try {
        toast.error('JS: ' + msg, { duration: 0 });
      } catch (_e) {
        // ignore toast error
      }
    };
    window.addEventListener('error', onErr);
    window.addEventListener('unhandledrejection', onErr);
    return () => {
      window.removeEventListener('error', onErr);
      window.removeEventListener('unhandledrejection', onErr);
    };
  });
  let appCreds = $state(null);
  // Embedded serial console panel (lazy: WS opens on toggle / ?serial=1).

  // App credentials only exist for VMs deployed from an appliance;
  // vmMeta.app_info is loaded by load().
  const appInfo = $derived.by(() => {
    if (!vmMeta?.app_info) return null;
    try {
      return JSON.parse(vmMeta.app_info);
    } catch {
      return null;
    }
  });

  function showAppCredentials() {
    if (appInfo) {
      appCreds = appInfo;
      showAppCreds = true;
    }
  }

  async function resetPassword() {
    actionLoading = 'resetPassword';
    try {
      const result = await api.resetVMPassword(vm.id);
      resetUsername = result.username || 'admin';
      resetPasswordValue = result.password;
      showPasswordModal = true;
    } catch (e) {
      resetError = e.message;
      showResetError = true;
    } finally {
      actionLoading = '';
    }
  }

  async function doAction(action) {
    if (action === 'forceOffVM') {
      askConfirm({
        title: t('vmDetail.forceOffTitle'),
        description: t('vmDetail.forceOffDesc'),
        confirmLabel: t('vmDetail.forceOff'),
        variant: 'destructive',
        onConfirm: async () => {
          // Unlike every other confirm-dialog action here, this one
          // never toggled confirmState.loading — the Confirm button
          // stayed enabled with no spinner for the whole call (up to
          // 30s for forceRebootVM, which waits for shutoff before
          // restarting), looking exactly like "accepted the click but
          // never closes". doActionRun still closes the dialog itself
          // on success; this only covers the wait and the error case.
          confirmState.loading = true;
          await doActionRun(action);
          confirmState.loading = false;
        },
      });
      return;
    }
    if (action === 'forceRebootVM') {
      askConfirm({
        title: t('vmDetail.forceRebootTitle'),
        description: t('vmDetail.forceRebootDesc'),
        confirmLabel: t('vmDetail.forceReboot'),
        variant: 'destructive',
        onConfirm: async () => {
          confirmState.loading = true;
          await doActionRun(action);
          confirmState.loading = false;
        },
      });
      return;
    }
    if (action === 'deleteVM') {
      showDeleteFlow = true;
      return;
    }
    await doActionRun(action);
  }

  // toggleAutostart flips libvirtd's per-VM autostart flag. It
  // is intentionally separate from doActionRun (which dims the
  // whole Actions card) — the round-trip is fast and the user
  // reported that accidentally toggling autostart was the
  // annoyance that motivated this control, so a quick, narrow
  // visual confirmation is the right feedback.
  //
  // On failure we restore the previous value (the Switch's
  // local `checked` already flipped optimistically via the
  // Switch's onclick handler; we re-set it from the still-
  // unchanged vm.autostart). The toast is sticky so the user
  // doesn't miss the libvirt error.
  async function toggleAutostart(next) {
    if (!vm) return;
    const previous = vm.autostart;
    autostartSaving = true;
    try {
      await api.setVMAutostart(vmId, next);
      // Reflect the new value on the VM object so any other
      // UI reading vm.autostart (e.g. a future list view
      // badge) stays in sync.
      vm = { ...vm, autostart: next };
      toast.success(next ? t('vmDetail.autostartEnabled') : t('vmDetail.autostartDisabled'), {
        duration: 3000,
      });
    } catch (e) {
      // Roll the Switch back to the previous value.
      vm = { ...vm, autostart: previous };
      toast.error(t('vmDetail.setAutostartFailed', { error: e.message }), { duration: 0 });
    } finally {
      autostartSaving = false;
    }
  }

  async function doActionRun(action) {
    actionLoading = action;
    try {
      if (action === 'forceRebootVM') {
        await api.forceOffVM(vmId);
        await waitForState('shutoff', 30000);
        await api.startVM(vmId);
      } else {
        await api[action](vmId);
      }
      const labels = {
        startVM: t('vmDetail.actionStarted'),
        shutdownVM: t('vmDetail.actionShutDown'),
        rebootVM: t('vmDetail.actionRebooted'),
        forceRebootVM: t('vmDetail.actionForceRebooted'),
        suspendVM: t('vmDetail.actionSuspended'),
        resumeVM: t('vmDetail.actionResumed'),
        forceOffVM: t('vmDetail.actionForceOff'),
      };
      toast.success(
        t('vmDetail.vmActionToast', { label: labels[action] || t('vmDetail.actionUpdated') })
      );
      confirmState.open = false;
      await load();
    } catch (e) {
      toast.error(e.message, { duration: 0 });
    } finally {
      actionLoading = '';
    }
  }

  async function waitForState(targetState, timeoutMs = 30000) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      const cur = await api.getVM(vmId);
      if (cur.state === targetState) return;
      await new Promise((r) => setTimeout(r, 500));
    }
    throw new Error(t('vmDetail.timeoutState', { state: targetState }));
  }

  function fwUid() {
    return 'fw_' + Math.random().toString(36).slice(2, 10);
  }

  function addForward() {
    fwForwards = [
      ...fwForwards,
      { id: fwUid(), proto: 'tcp', host_port: '', guest_port: '', target_ip: '' },
    ];
  }

  function removeForward(id) {
    fwForwards = fwForwards.filter((f) => f.id !== id);
  }

  function addRule() {
    fwRules = [...fwRules, { id: fwUid(), proto: 'tcp', port: '', action: 'allow' }];
  }

  function removeRule(id) {
    fwRules = fwRules.filter((r) => r.id !== id);
  }

  let fwTplSel = $state('');

  // Merge a per-VM preset into the editor (same dedupe as the host
  // page; mergeFirewall keys fit both shapes).
  function applyVmTemplate() {
    if (!fwTplSel) return;
    const built = buildVMTemplate(fwTplSel);
    const merged = mergeFirewall(
      { input: fwRules, forwards: fwForwards },
      { input: built.rules, forwards: built.forwards || [] }
    );
    const n = merged.addedInput + merged.addedForwards;
    fwRules = merged.input;
    fwForwards = merged.forwards;
    fwTplSel = '';
    toast.success(t('firewall.vmTemplateApplied', { added: n }));
  }

  async function saveFirewall() {
    if (!vmId) return;
    fwSaving = true;
    try {
      const rules = fwRules
        .filter((r) => r.port)
        .map((r) => ({
          id: r.id,
          proto: r.proto,
          port: Number(r.port),
          action: r.action,
          disabled: !!r.disabled,
        }));
      const forwards = fwForwards
        .filter((f) => f.host_port && f.guest_port)
        .map((f) => ({
          id: f.id,
          proto: f.proto,
          host_port: Number(f.host_port),
          guest_port: Number(f.guest_port),
          target_ip: f.target_ip || '',
          disabled: !!f.disabled,
        }));
      const res = await api.setVMFirewall(vmId, { rules, forwards });
      fwForwards = (res.vm?.forwards || []).map((f) => ({ ...f }));
      toast.success(t('vmDetail.firewallSaved'));
    } catch (e) {
      toast.error(e.message);
    } finally {
      fwSaving = false;
    }
  }

  // Admin-only "share with all users" flag on a template. Same
  // optimistic pattern as toggleAutostart: the Switch has already
  // flipped locally; on failure we restore vmMeta.shared.
  let shareSaving = $state(false);
  async function toggleShared(next) {
    if (!vmId || !vmMeta?.template) return;
    const previous = !!vmMeta.shared;
    shareSaving = true;
    try {
      const m = await api.updateVMMeta(vmId, { shared: next });
      vmMeta = m && typeof m === 'object' ? m : { ...vmMeta, shared: next };
      toast.success(next ? t('vmDetail.shareTemplateOn') : t('vmDetail.shareTemplateOff'), {
        duration: 3000,
      });
    } catch (e) {
      vmMeta = { ...vmMeta, shared: previous };
      toast.error(t('vmDetail.shareTemplateFailed', { error: e.message }), { duration: 0 });
    } finally {
      shareSaving = false;
    }
  }

  async function toggleTemplate() {
    if (!vmId || vm.state !== 'shutoff') return;
    actionLoading = 'template';
    try {
      if (vmMeta?.template) {
        await api.unsetVMTemplate(vmId);
        toast.success(t('vmDetail.unsetTemplateDone'));
      } else {
        await api.makeVMTemplate(vmId);
        toast.success(t('vmDetail.makeTemplateDone'));
      }
      const m = await api.getVMMeta(vmId);
      vmMeta = m;
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  async function saveSchedule() {
    if (!vmId) return;
    schedSaving = true;
    try {
      await api.setVMSchedule(vmId, {
        start_cron: schedStart.trim(),
        stop_cron: schedStop.trim(),
        snapshot_cron: schedSnap.trim(),
        snapshot_max: Math.min(999, Math.max(1, Number(schedSnapMax) || 3)),
      });
      toast.success(t('vmDetail.scheduleSaved'));
    } catch (e) {
      toast.error(e.message);
    } finally {
      schedSaving = false;
    }
  }

  async function createSnapshot() {
    if (!snapName) return;
    actionLoading = 'snapshot';
    try {
      const res = await api.createSnapshot(vmId, {
        name: snapName,
        description: snapDesc,
        memory: snapMemory,
      });
      await api.waitJob(res.job);
      snapName = '';
      snapDesc = '';
      snapMemory = false;
      toast.success(t('vmDetail.snapshotCreated'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  async function revertSnapshot(sid) {
    if (!requireShutoff(t('vmDetail.requireShutoffRevertingSnapshot'))) return;
    askConfirm({
      title: t('vmDetail.revertSnapshotTitle'),
      description: t('vmDetail.revertSnapshotDesc'),
      confirmLabel: t('vmDetail.revert'),
      variant: 'destructive',
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.revertSnapshot(vmId, sid);
          confirmState.open = false;
          toast.success(t('vmDetail.reverted'));
          await load();
        } catch (e) {
          toast.error(e.message);
          confirmState.loading = false;
        }
      },
    });
  }

  function deleteSnapshot(sid) {
    askConfirm({
      title: t('vmDetail.deleteSnapshotTitle'),
      description: t('vmDetail.deleteSnapshotDesc'),
      confirmLabel: t('common.delete'),
      variant: 'destructive',
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.deleteSnapshot(vmId, sid);
          confirmState.open = false;
          toast.success(t('vmDetail.snapshotDeleted'));
          await load();
        } catch (e) {
          toast.error(e.message);
          confirmState.loading = false;
        }
      },
    });
  }

  async function loadDiskVolumesForPool() {
    try {
      aDiskVolumes = (await api.listVolumes(aDiskPool)) || [];
    } catch (_e) {
      aDiskVolumes = [];
    }
  }

  async function loadHostZVols() {
    if (!auth.isAdmin() || isContainerVm) return;
    aDiskZVolsLoading = true;
    try {
      aDiskZVols = (await api.listHostZVols()) || [];
    } catch (_e) {
      aDiskZVols = [];
    } finally {
      aDiskZVolsLoading = false;
    }
  }

  async function probeZVolDisk() {
    aDiskProbe = null;
    aDiskForce = false;
    const name = aDiskZVol?.trim();
    if (!name) return;
    const devPath = name.startsWith('/dev/zvol/') ? name : `/dev/zvol/${name}`;
    aDiskProbing = true;
    try {
      aDiskProbe = await api.probeDisk(devPath, false);
    } catch (_e) {
      aDiskProbe = null;
    } finally {
      aDiskProbing = false;
    }
  }

  // Inspect the selected existing disk so we can warn if it already has
  // data before it is attached. Basic probe (qemu-img) is fast and
  // always available; never blocks the dialog on failure.
  async function probeExistingDisk() {
    aDiskProbe = null;
    aDiskForce = false;
    const path = aDiskExistingVol;
    if (!path) return;
    aDiskProbing = true;
    try {
      aDiskProbe = await api.probeDisk(path, false);
    } catch (_e) {
      aDiskProbe = null;
    } finally {
      aDiskProbing = false;
    }
  }

  async function addDisk() {
    actionLoading = 'adddisk';
    try {
      const data = {
        device: aDiskDevice === 'existing' || aDiskDevice === 'zvol' ? 'disk' : aDiskDevice,
        bus: aDiskBus,
      };
      if (aDiskDevice === 'cdrom') {
        data.source = aDiskISO;
      } else if (aDiskDevice === 'zvol') {
        const zvolName = aDiskZVol?.trim();
        if (!zvolName) {
          toast.error(t('vmDetail.zvolSelectPlaceholder'));
          actionLoading = '';
          return;
        }
        data.zvol = zvolName;
        data.format = 'raw';
        if (aDiskProbe?.has_data && !aDiskForce) {
          toast.warning(t('vmDetail.diskHasDataWarning'));
          aDiskProbing = false;
          actionLoading = '';
          return;
        }
        data.force = aDiskForce;
      } else if (aDiskDevice === 'existing') {
        const vol = aDiskVolumes.find((v) => v.path === aDiskExistingVol);
        data.source = aDiskExistingVol;
        data.format = vol?.format || 'qcow2';
        // If the disk holds data and the operator has not confirmed,
        // surface the same guard the backend enforces instead of a raw
        // 409 round-trip.
        if (aDiskProbe?.has_data && !aDiskForce) {
          toast.warning(t('vmDetail.diskHasDataWarning'));
          aDiskProbing = false;
          actionLoading = '';
          return;
        }
        data.force = aDiskForce;
      } else {
        data.format = aDiskFormat;
        data.size_gb = aDiskSize;
        data.pool = aDiskPool;
      }
      if (aDiskDevice !== 'cdrom') {
        if (aDiskWWN.trim()) data.wwn = aDiskWWN.trim();
        if (aDiskSerial.trim()) data.serial = aDiskSerial.trim();
        if (aDiskAlias.trim()) data.alias = aDiskAlias.trim();
      }
      await api.createDisk(vmId, data);
      showAddDisk = false;
      toast.success(t('vmDetail.diskAdded'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  async function changeISO() {
    if (!cISOTarget) return;
    actionLoading = 'changeiso';
    try {
      await api.updateDiskSource(vmId, cISOTarget, cISOSource);
      showChangeISO = false;
      toast.success(t('vmDetail.isoChanged'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  async function resizeDisk() {
    if (!resizeDiskTarget) return;
    if (resizeDiskSize < resizeDiskCurrent) {
      if (!requireShutoff(t('vmDetail.requireShutoffShrinkingDisk'))) return;
    }
    actionLoading = 'resizedisk';
    try {
      await api.resizeVmDisk(vm.id, resizeDiskTarget, resizeDiskSize);
      showResizeDisk = false;
      toast.success(t('vmDetail.diskResized'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  async function changeDiskBus() {
    if (!changeBusTarget || !changeBusNew) return;
    if (!requireShutoff(t('vmDetail.requireShutoffChangeBus'))) return;
    actionLoading = 'changebus';
    try {
      await api.changeDiskBus(vm.id, changeBusTarget, changeBusNew);
      showChangeBus = false;
      toast.success(t('vmDetail.diskBusChanged'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  // requireShutoff gates disk operations that cannot be applied to a
  // running VM (resize uses qemu-img on a live disk; detach risks the
  // guest still having the device mounted). Returns false and explains
  // why when the VM isn't shut off.
  function requireShutoff(action) {
    if (!vm) return false;
    if (vm.state !== 'shutoff') {
      const msg = t('vmDetail.requireShutoffMsg', { action, state: vm.state });
      blockedNotice = msg;
      showBlocked = true;
      toast.warning(msg, { duration: 5000 });
      return false;
    }
    return true;
  }

  // Once the VM is actually shut off, close the pop-up and clear the notice.
  $effect(() => {
    if (vm && vm.state === 'shutoff') {
      blockedNotice = '';
      showBlocked = false;
    }
  });

  $effect(() => {
    if (activeSection === 'cloudinit' && vm && !isContainerVm) {
      loadCloudInitStatus();
      loadCloudInitSnippets();
    }
  });

  $effect(() => {
    if (activeSection === 'history' && vmId) {
      loadHistory();
      loadVMLogs();
    }
  });

  function removeDisk(target) {
    askConfirm({
      title: t('vmDetail.removeDiskTitle', { target }),
      description: t('vmDetail.removeDiskDesc'),
      confirmLabel: t('vmDetail.remove'),
      variant: 'destructive',
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.deleteDisk(vmId, target);
          confirmState.open = false;
          toast.success(t('vmDetail.diskRemoved'));
          await load();
        } catch (e) {
          toast.error(e.message);
          confirmState.loading = false;
        }
      },
    });
  }

  async function addNet() {
    actionLoading = 'addnet';
    try {
      await api.createNetIface(vmId, { network: aNetNetwork, model: aNetModel });
      showAddNet = false;
      toast.success(t('vmDetail.netAdded'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  function removeNet(mac) {
    askConfirm({
      title: t('vmDetail.removeNetTitle'),
      description: t('vmDetail.removeNetDesc', { mac }),
      confirmLabel: t('vmDetail.remove'),
      variant: 'destructive',
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.deleteNetIface(vmId, mac);
          confirmState.open = false;
          toast.success(t('vmDetail.netRemoved'));
          await load();
        } catch (e) {
          toast.error(e.message);
          confirmState.loading = false;
        }
      },
    });
  }

  // USB passthrough (admin only)
  let hostUSBDevices = $state([]);
  let hostUSBLoaded = false;
  async function loadHostUSBDevices(force = false) {
    if (hostUSBLoaded && !force) return;
    hostUSBLoaded = true;
    try {
      hostUSBDevices = await api.listHostUSBDevices();
    } catch (e) {
      // Allow a later retry (the button calls with force=true) but do
      // not let the auto-effect spam an error toast on every re-run.
      hostUSBLoaded = false;
      toast.error(e.message);
    }
  }
  $effect(() => {
    if (auth.isAdmin()) loadHostUSBDevices();
  });
  async function attachUSB(vendorId, productId) {
    actionLoading = 'usb';
    try {
      await api.attachUSBDevice(vmId, vendorId, productId);
      toast.success(t('vmDetail.usbDeviceAttached'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }
  async function detachUSB(vendorId, productId) {
    actionLoading = 'usb';
    try {
      await api.detachUSBDevice(vmId, vendorId, productId);
      toast.success(t('vmDetail.usbDeviceDetached'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  // PCI passthrough (admin only, KVM only — VM must be shut off).
  let hostPCIGroups = $state([]);
  let hostPCIError = $state('');
  let hostPCILoaded = false;
  let hostPCIPreflight = $state(null);
  let pciPreflightLoading = $state(false);

  async function loadHostPCIPreflight() {
    pciPreflightLoading = true;
    try {
      hostPCIPreflight = await api.getHostPCIPreflight();
    } catch {
      hostPCIPreflight = null;
    } finally {
      pciPreflightLoading = false;
    }
  }

  async function loadHostPCIDevices(force = false) {
    if (hostPCILoaded && !force) return;
    hostPCILoaded = true;
    try {
      hostPCIGroups = await api.listHostPCIDevices();
      hostPCIError = '';
    } catch (e) {
      hostPCIGroups = [];
      hostPCIError = e.message;
      hostPCILoaded = false;
    }
  }
  $effect(() => {
    if (auth.isAdmin() && !isContainerVm) {
      loadHostPCIDevices();
      loadHostPCIPreflight();
    }
  });
  async function attachPCIGroup(group) {
    actionLoading = 'pci';
    try {
      await api.attachPCIDevices(
        vmId,
        group.devices.map((d) => d.address)
      );
      toast.success(t('vmDetail.pciDeviceAttached'));
      await load();
      await loadHostPCIDevices(true);
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }
  async function detachPCI(address) {
    actionLoading = 'pci';
    try {
      await api.detachPCIDevice(vmId, address);
      toast.success(t('vmDetail.pciDeviceDetached'));
      await load();
      await loadHostPCIDevices(true);
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  // 9p shared folders (admin only — VM must be shut off).
  let newSharedFolderPath = $state('');
  let newSharedFolderTag = $state('');
  let newSharedFolderReadOnly = $state(false);
  async function attachSharedFolder() {
    actionLoading = 'sharedfolder';
    try {
      await api.attachSharedFolder(
        vmId,
        newSharedFolderPath,
        newSharedFolderTag,
        newSharedFolderReadOnly
      );
      toast.success(t('vmDetail.sharedFolderAttached'));
      newSharedFolderPath = '';
      newSharedFolderTag = '';
      newSharedFolderReadOnly = false;
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }
  async function detachSharedFolder(tag) {
    actionLoading = 'sharedfolder';
    try {
      await api.detachSharedFolder(vmId, tag);
      toast.success(t('vmDetail.sharedFolderDetached'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }
  function sharedFolder9pMountCommand(tag) {
    return `mount -t 9p -o trans=virtio,version=9p2000.L ${tag} /mnt/${tag}`;
  }

  async function cloneVM() {
    if (!cName) return;
    if (!requireShutoff(t('vmDetail.requireShutoffCloning'))) return;
    actionLoading = 'clone';
    try {
      const res = await api.cloneVM(vmId, { name: cName, pool: cPool, linked: cLinked });
      const cloned = await api.waitJob(res.job);
      showClone = false;
      toast.success(t('vmDetail.vmCloned', { name: cloned?.name || cName }));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  // Stage names come from the backend as stable identifiers. Mapping
  // them explicitly (rather than interpolating into t()) keeps every
  // key greppable, so check-i18n can still tell used from orphaned.
  const MOVE_STAGES = {
    move_preparing: 'vmDetail.movePreparing',
    move_copying: 'vmDetail.moveCopying',
    move_cleanup: 'vmDetail.moveCleanup',
    move_done: 'vmDetail.moveDone',
  };
  function moveStageLabel(stage) {
    const key = MOVE_STAGES[stage];
    return key ? t(key) : '';
  }

  // Move this instance's storage to another pool. Cold only: the disk
  // is copied byte-for-byte, which cannot be done safely underneath a
  // running hypervisor process.
  async function moveStorage() {
    if (!movePool) return;
    if (!requireShutoff(t('vmDetail.requireShutoffMoving'))) return;
    actionLoading = 'move';
    moveProgress = { pct: 0, stage: 'move_preparing' };
    try {
      const res = await api.moveVMStorage(vmId, movePool);
      await api.waitJob(res.id, {
        // A cross-device copy of a large disk can run well past the
        // default 10-minute ceiling; a timeout here would report
        // failure on a move that is still progressing fine.
        timeout: 4 * 60 * 60 * 1000,
        onPoll: (job) => {
          moveProgress = { pct: job.progress || 0, stage: job.stage || '' };
        },
      });
      showMove = false;
      toast.success(t('vmDetail.storageMoved', { pool: movePool }));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
      moveProgress = null;
    }
  }

  // Export
  function exportVM() {
    // Containers can export while running (Incus daemon handles it natively).
    // KVM VMs must be shut off.
    if (!isContainerVm && !requireShutoff(t('vmDetail.requireShutoffExporting'))) return;
    // Default target differs by type: containers default to Incus backup,
    // KVM VMs default to VMware OVA.
    exportTarget = isContainerVm ? 'backup' : 'vmware';
    exportProgress = null;
    showExport = true;
  }

  // Download a .rdp / .vv console file with the session cookie
  // (credentials: include — never a token in the URL), then save it as a blob.
  async function downloadConsoleFile(kind) {
    const url = kind === 'rdp' ? api.getRDPUrl(vmId) : api.getSPICEUrl(vmId);
    try {
      const res = await fetch(url, {
        credentials: 'include',
      });
      if (!res.ok)
        throw new Error((await res.json().catch(() => ({}))).error || `HTTP ${res.status}`);
      const blob = await res.blob();
      const objectUrl = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = objectUrl;
      a.download = kind === 'rdp' ? `${vm.name || vmId}.rdp` : `${vm.name || vmId}.vv`;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(objectUrl);
    } catch (e) {
      toast.error(e.message);
    }
  }

  async function startExport() {
    // Containers can export while running; KVM VMs need to be shut off.
    if (!isContainerVm && !requireShutoff(t('vmDetail.requireShutoffExporting'))) {
      return;
    }
    // The export dialog's confirm button keys off actionLoading ===
    // 'export'; without this it was never set, so the button stayed
    // enabled and a double-click fired a second export on top of the
    // first.
    actionLoading = 'export';
    try {
      await performExport();
    } finally {
      actionLoading = '';
    }
  }

  async function performExport() {
    let label;
    if (exportTarget === 'backup') {
      label = isContainerVm
        ? t('vmDetail.exportIncusBackupLabel')
        : t('vmDetail.exportBuildingBackup');
    } else if (exportTarget === 'proxmox') {
      label = t('vmDetail.exportBuildingProxmox');
    } else {
      label = t('vmDetail.exportBuildingOva', { target: exportTarget });
    }
    exportProgress = { received: 0, total: 0, percent: 0, label };
    const taskId = 'export:' + vm.name;
    upsertTask({
      id: taskId,
      kind: 'export',
      title: vm.name,
      pct: 0,
      message: label,
      status: 'running',
    });
    const ac = new AbortController();
    exportAbort = ac;
    try {
      const opts = {
        signal: ac.signal,
        onProgress: (p) => {
          exportProgress = { received: p.received, total: p.total, percent: p.percent, label };
          updateTask(taskId, {
            pct: Math.round(p.percent || 0),
            message: label,
          });
        },
      };
      if (exportTarget === 'backup') {
        opts.format = 'backup';
        opts.compress = true;
      } else if (exportTarget === 'proxmox') {
        opts.format = 'proxmox';
      } else {
        opts.format = 'ova';
        opts.target = exportTarget;
      }
      const result = await api.exportVM(vm.name, opts);
      exportProgress = {
        received: result.size,
        total: result.size,
        percent: 100,
        label: t('vmDetail.exportDone'),
      };
      finishTask(taskId, 'success', t('vmDetail.exportComplete'), 100);
      toast.success(t('vmDetail.exportComplete'));
      later(() => {
        showExport = false;
        exportProgress = null;
        exportAbort = null;
      }, 800);
    } catch (e) {
      if (e.name === 'AbortError') {
        exportProgress = {
          received: 0,
          total: 0,
          percent: 0,
          label: t('vmDetail.exportCancelled'),
        };
        finishTask(taskId, 'error', t('vmDetail.exportCancelled'), 0);
        later(() => {
          showExport = false;
          exportProgress = null;
          exportAbort = null;
        }, 600);
      } else {
        finishTask(taskId, 'error', e.message, exportProgress?.percent || 0);
        toast.error(e.message);
        showExport = false;
        exportProgress = null;
        exportAbort = null;
      }
    }
  }

  function cancelExport() {
    if (exportAbort) exportAbort.abort();
  }

  function onBack() {
    navigate('/vms');
  }

  function formatUptime(s) {
    if (!s) return '—';
    const d = Math.floor(s / 86400),
      h = Math.floor((s % 86400) / 3600),
      m = Math.floor((s % 3600) / 60);
    const parts = [];
    if (d) parts.push(d + 'd');
    if (h) parts.push(h + 'h');
    if (m) parts.push(m + 'm');
    return parts.join(' ') || '<1m';
  }

  function bytesToStr(b) {
    if (!b) return '0 B';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    let n = b;
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024;
      i++;
    }
    return n.toFixed(i > 0 ? 1 : 0) + ' ' + u[i];
  }

  function diskLabel(d) {
    if (d.device === 'cdrom') return d.name || (d.source ? d.source.split('/').pop() : '(empty)');
    if (d.zvol) return `zvol: ${d.zvol}`;
    if (d.block_dev) return d.block_dev;
    return d.name || (d.source ? d.source.split('/').pop() : d.target);
  }

  // Build a snapshot tree from the flat list. Roots have parent_name == "".
  const snapshotTree = $derived.by(() => buildSnapshotTree(snapshots));

  function buildSnapshotTree(flat) {
    if (!Array.isArray(flat) || flat.length === 0) return { roots: [], byId: {} };
    const byId = {};
    for (const s of flat) byId[s.name] = { ...s, children: [] };
    const roots = [];
    for (const k of Object.keys(byId)) {
      const node = byId[k];
      if (node.parent_name && byId[node.parent_name]) {
        byId[node.parent_name].children.push(node);
      } else {
        roots.push(node);
      }
    }
    // Sort by creation time ascending so children appear below parents.
    const sortRec = (nodes) => {
      nodes.sort((a, b) => (a.creation_time || 0) - (b.creation_time || 0));
      nodes.forEach((n) => sortRec(n.children));
    };
    sortRec(roots);
    return { roots, byId };
  }

  function formatSnapshotDate(epoch) {
    if (!epoch) return '—';
    const d = new Date(epoch * 1000);
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    const hh = String(d.getHours()).padStart(2, '0');
    const mm = String(d.getMinutes()).padStart(2, '0');
    return `${y}-${m}-${day} ${hh}:${mm}`;
  }

  // Deep-link from Storage: ?tab=snapshots opens the Snapshots tab.
  $effect(() => {
    const r = getRoute();
    if (r.query?.tab === 'snapshots') activeSection = 'snaps';
  });
</script>

<div class="p-3 sm:p-5 w-full max-w-[1700px] mx-auto">
  <div class="flex flex-wrap items-center justify-between gap-3 mb-6">
    <div class="flex items-center gap-3 flex-wrap min-w-0 flex-1">
      <button
        onclick={onBack}
        class="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors shrink-0"
        aria-label="Back"
      >
        <svg class="w-4 h-4" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">
          <polyline points="15 18 9 12 15 6" />
        </svg>
      </button>
      {#if vm}
        <h1 class="text-xl font-semibold tracking-tight truncate min-w-0">{vm.alias || vm.name}</h1>
        {#if vm.alias && vm.alias !== vm.name}
          <span class="text-xs text-muted-foreground font-mono truncate shrink-0">({vm.name})</span>
        {/if}
        <StatusBadge state={vm.state} size="sm" class="shrink-0" />
        {#if vm.state === 'running' && vmIps(vm).length}
          <span
            class="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded bg-accent/10 border border-accent/20 text-accent font-mono truncate shrink-0 max-w-[24rem]"
          >
            {vmIps(vm).join(', ')}
          </span>
        {/if}
        {#if vmMeta?.template}
          <span
            class="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded bg-warning/10 border border-warning/30 text-warning font-medium shrink-0 whitespace-nowrap"
          >
            {t('vmDetail.templateBadge')}
          </span>
          {#if vmMeta?.shared}
            <span
              class="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded bg-accent/10 border border-accent/20 text-accent font-medium shrink-0 whitespace-nowrap"
              title={t('vmDetail.sharedBadgeTitle')}
            >
              {t('vmDetail.sharedBadge')}
            </span>
          {/if}
        {/if}
      {/if}
    </div>
    {#if vm && auth.canMutate()}
      <!-- Wraps (and may shrink) instead of shrink-0: on a phone the
           share switch + template button overflowed the viewport and
           clipped "Quitar como plantilla". -->
      <div class="flex flex-wrap items-center gap-2 min-w-0 max-w-full">
        {#if auth.isAdmin() && vmMeta?.template}
          <div class="max-w-xs min-w-0">
            <Switch
              size="sm"
              checked={!!vmMeta?.shared}
              disabled={shareSaving}
              label={t('vmDetail.shareTemplate')}
              description={t('vmDetail.shareTemplateDesc')}
              ariaLabel={t('vmDetail.shareTemplate')}
              onchange={toggleShared}
            />
          </div>
        {/if}
        <Button
          size="sm"
          variant="outline"
          onclick={toggleTemplate}
          disabled={vm.state !== 'shutoff' || actionLoading === 'template'}
        >
          {vmMeta?.template ? t('vmDetail.unsetTemplate') : t('vmDetail.makeTemplate')}
        </Button>
      </div>
    {/if}
  </div>

  {#if error && !error.toLowerCase().includes('forbidden')}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if loading}
    <VmDetailSkeleton />
  {:else if vm}
    <!-- minmax(0,1fr) + min-w-0: a plain `1fr` track has an auto minimum,
         so the tab row's min-content (~930px with all KVM tabs) widened
         the page past the viewport and clipped the sidebar actions. The
         tab strip scrolls horizontally instead (Tabs has overflow-x-auto). -->
    <div class="grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_280px] gap-5">
      <!-- Main column -->
      <div class="space-y-5 min-w-0">
        <!-- Overview -->
        {#snippet sec_overview()}
          <BlockCard bid="overview" title={t('vmDetail.overview')}>
            {#snippet headerActions()}
              {#if auth.canMutate()}
                <Button
                  size="sm"
                  variant="outline"
                  class="!h-7 !text-xs"
                  onclick={() => openEditTab('general')}
                >
                  <Pencil class="w-3 h-3 mr-1.5" />
                  {t('vmDetail.editSettings')}
                </Button>
              {/if}
            {/snippet}

            {#if vm.cover}
              <div
                class="relative w-full h-32 rounded-xl overflow-hidden border border-border bg-muted/40 shadow-2xs mb-4"
              >
                <img src={vm.cover} alt="" class="w-full h-full object-cover" />
                <div
                  class="absolute inset-0 bg-gradient-to-t from-background/90 via-background/20 to-transparent flex items-end p-3"
                >
                  <div class="flex items-center gap-2">
                    <span
                      class="text-xs font-semibold px-2.5 py-0.5 rounded-full bg-background/80 backdrop-blur border border-border/80"
                    >
                      {vm.alias || vm.name}
                    </span>
                    {#if vm.os_type}
                      <span
                        class="text-[11px] px-2 py-0.5 rounded-full bg-accent/20 text-accent font-medium backdrop-blur"
                      >
                        {vm.os_type}
                      </span>
                    {/if}
                  </div>
                </div>
              </div>
            {/if}

            <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 mb-4">
              <div class="border border-border rounded-md p-3 bg-background">
                <p class="text-2xl font-semibold tnum">{vm.vcpus}</p>
                <p class="text-xs text-muted-foreground mt-0.5">{t('common.vcpu')}</p>
                {#if vm.cpu_usage != null}<p class="text-xs text-accent mt-0.5 tnum">
                    {vm.cpu_usage.toFixed(1)}% used
                  </p>{/if}
              </div>
              <div class="border border-border rounded-md p-3 bg-background">
                <p class="text-2xl font-semibold tnum">{vm.ram_mb}</p>
                <p class="text-xs text-muted-foreground mt-0.5">{t('vmDetail.ramLabel')}</p>
                {#if vm.ram_used_mb != null}<p class="text-xs text-accent mt-0.5 tnum">
                    {vm.ram_used_mb} MB used
                  </p>{/if}
              </div>
              <div class="border border-border rounded-md p-3 bg-background">
                <p class="text-lg font-semibold tnum">{formatUptime(vm.uptime_sec)}</p>
                <p class="text-xs text-muted-foreground mt-0.5">{t('vmDetail.uptime')}</p>
              </div>
            </div>
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-1.5 text-sm">
              <div class="flex gap-2">
                <span class="text-muted-foreground shrink-0">{t('vmDetail.typeLabel')}</span>
                <span
                  class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded border text-[10px] uppercase tracking-wider font-medium {isContainerVm
                    ? 'border-warning/30 bg-warning/10 text-warning'
                    : 'border-accent/30 bg-accent/10 text-accent'}"
                >
                  {isContainerVm ? 'Incus' : 'KVM'}
                </span>
              </div>
              {#if vm.os_type}
                <div class="flex gap-2">
                  <span class="text-muted-foreground shrink-0">{t('vmDetail.os')}</span><span
                    class="truncate">{vm.os_type}{vm.os_version ? ' ' + vm.os_version : ''}</span
                  >
                </div>
              {/if}
              {#if !isContainerVm}
                <div class="flex gap-2">
                  <span class="text-muted-foreground shrink-0">{t('vmDetail.chipset')}</span><span
                    >{vm.chipset}</span
                  >
                </div>
                <div class="flex gap-2">
                  <span class="text-muted-foreground shrink-0">{t('vmDetail.secureBoot')}</span
                  ><span>{vm.secure_boot ? t('common.yes') : t('common.no')}</span>
                </div>
                <div class="flex gap-2">
                  <span class="text-muted-foreground shrink-0">{t('vmDetail.tpm')}</span><span
                    >{vm.tpm_enabled ? t('common.yes') : t('common.no')}</span
                  >
                </div>
                <div class="flex gap-2">
                  <span class="text-muted-foreground shrink-0">{t('vmDetail.bios')}</span><span
                    class="capitalize">{vm.firmware || '?'}</span
                  >
                </div>
                <div class="flex gap-2">
                  <span class="text-muted-foreground shrink-0">{t('vmDetail.cpuMode')}</span><span
                    >{vm.cpu_mode || 'host-passthrough'}</span
                  >
                </div>
                <div class="flex gap-2">
                  <span class="text-muted-foreground shrink-0">{t('vmDetail.video')}</span><span
                    >{vm.video_model || 'virtio'}</span
                  >
                </div>
                <div class="flex items-center gap-2">
                  <span class="text-muted-foreground shrink-0">{t('vmDetail.boot')}</span>
                  <select
                    bind:value={bootDevice}
                    onchange={() =>
                      api
                        .setBootDevice(vmId, bootDevice)
                        .then(() => load())
                        .catch((e) => toast.error(e.message))}
                    class="input !py-1 !text-xs w-auto"
                  >
                    <option value="hd">{t('vmDetail.hardDisk')}</option>
                    <option value="cdrom">{t('vmDetail.cdrom')}</option>
                    <option value="network">{t('vmDetail.network')}</option>
                  </select>
                </div>
              {/if}
            </div>
          </BlockCard>
        {/snippet}

        <!-- Hardware & Settings: read-only inventory. Editing is
             consolidated into a single entry point (the "Editar
             ajustes" button in this card's header) instead of a
             separate "Editar" affordance per row/section, which used
             to make ~15 buttons all opening the same dialog. The `tab`
             param is unused visually now but kept so a future
             "double-click a row to jump to its tab" shortcut can reuse
             it without re-plumbing every call site. -->
        {#snippet hwRow(icon, label, value, _tab, badge = '', accent = false, badgeVariant = '')}
          <div class="flex items-center justify-between gap-3 py-2.5 px-3.5">
            <div class="flex items-center gap-3 min-w-0">
              <div
                class="w-7 h-7 rounded-md flex items-center justify-center shrink-0 {accent
                  ? 'bg-accent/15 text-accent'
                  : 'bg-muted text-muted-foreground'}"
              >
                <Icon name={icon} size={14} />
              </div>
              <div class="min-w-0">
                <p
                  class="text-[11px] font-medium text-muted-foreground uppercase tracking-wide leading-tight"
                >
                  {label}
                </p>
                <div class="flex items-center gap-2 mt-0.5">
                  <p class="text-sm font-semibold text-foreground truncate">{value}</p>
                  {#if badge}
                    {@const variant =
                      badgeVariant ||
                      (['active', 'on', 'yes', 'ich9', 'virtio', 'uefi', 'true'].includes(
                        String(badge).toLowerCase()
                      )
                        ? 'success'
                        : ['disabled', 'off', 'no', 'false'].includes(String(badge).toLowerCase())
                          ? 'muted'
                          : 'accent')}
                    <span
                      class="text-[10px] font-mono px-1.5 py-0.5 rounded border uppercase font-medium {variant ===
                      'success'
                        ? 'bg-success/10 text-success border-success/20'
                        : variant === 'muted'
                          ? 'bg-muted text-muted-foreground border-border'
                          : 'bg-accent/10 text-accent border-accent/20'}"
                    >
                      {badge}
                    </span>
                  {/if}
                </div>
              </div>
            </div>
          </div>
        {/snippet}

        {#snippet sec_hardware()}
          <BlockCard bid="hardware" title={t('vmDetail.hardware')}>
            {#snippet headerActions()}
              {#if auth.canMutate()}
                {#if hwEditing}
                  <Button
                    size="sm"
                    variant="outline"
                    class="!h-7 !text-xs"
                    onclick={() => (hwEditing = false)}
                    disabled={editSaving}
                  >
                    {t('common.cancel')}
                  </Button>
                  <Button
                    size="sm"
                    class="!h-7 !text-xs min-w-[100px]"
                    onclick={saveEdit}
                    disabled={editSaving}
                  >
                    {#if editSaving}
                      <Spinner size="sm" color="text-white" />
                    {:else}
                      <Icon name="check" size={14} class="mr-1.5" />
                      {t('common.save')}
                    {/if}
                  </Button>
                {:else}
                  <Button size="sm" class="!h-7 !text-xs" onclick={() => openEditTab('general')}>
                    <Pencil class="w-3 h-3 mr-1.5" />
                    {t('vmDetail.editAllSettings')}
                  </Button>
                {/if}
              {/if}
            {/snippet}

            {#if hwEditing}
              {#if vm.state === 'running'}
                <div
                  class="mb-3 p-2.5 rounded-lg border border-warning/30 bg-warning-subtle text-warning text-xs flex items-center gap-2"
                >
                  <Icon name="alert-triangle" size={14} class="shrink-0" />
                  <span>{t('vmDetail.someChangesRequireRestart')}</span>
                </div>
              {/if}

              <!-- Horizontal Tab Selector -->
              <div
                class="flex items-center gap-1 overflow-x-auto pt-2 border-b border-border mb-3 no-scrollbar"
              >
                <button
                  type="button"
                  onclick={() => (editTab = 'general')}
                  class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-all cursor-pointer whitespace-nowrap {editTab ===
                  'general'
                    ? 'border-accent text-accent'
                    : 'border-transparent text-muted-foreground hover:text-foreground'}"
                >
                  <Icon name="tag" size={13} />
                  {t('vmCreate.general')}
                </button>

                <button
                  type="button"
                  onclick={() => (editTab = 'cpu')}
                  class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-all cursor-pointer whitespace-nowrap {editTab ===
                  'cpu'
                    ? 'border-accent text-accent'
                    : 'border-transparent text-muted-foreground hover:text-foreground'}"
                >
                  <Icon name="cpu" size={13} />
                  CPU & RAM
                </button>

                <button
                  type="button"
                  onclick={() => (editTab = 'display')}
                  class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-all cursor-pointer whitespace-nowrap {editTab ===
                  'display'
                    ? 'border-accent text-accent'
                    : 'border-transparent text-muted-foreground hover:text-foreground'}"
                >
                  <Icon name="monitor" size={13} />
                  {t('vmCreate.graphicsAudio')}
                </button>

                {#if !isContainerVm}
                  <button
                    type="button"
                    onclick={() => (editTab = 'boot')}
                    class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-all cursor-pointer whitespace-nowrap {editTab ===
                    'boot'
                      ? 'border-accent text-accent'
                      : 'border-transparent text-muted-foreground hover:text-foreground'}"
                  >
                    <Icon name="lock" size={13} />
                    {t('vmCreate.bootSecurity')}
                  </button>
                {:else}
                  <button
                    type="button"
                    onclick={() => (editTab = 'container')}
                    class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-all cursor-pointer whitespace-nowrap {editTab ===
                    'container'
                      ? 'border-accent text-accent'
                      : 'border-transparent text-muted-foreground hover:text-foreground'}"
                  >
                    <Icon name="box" size={13} />
                    Incus / LXC
                  </button>
                {/if}

                <button
                  type="button"
                  onclick={() => (editTab = 'os')}
                  class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-all cursor-pointer whitespace-nowrap {editTab ===
                  'os'
                    ? 'border-accent text-accent'
                    : 'border-transparent text-muted-foreground hover:text-foreground'}"
                >
                  <Icon name="layers" size={13} />
                  {t('vmDetail.os')}
                </button>
              </div>

              <!-- Tab Content Body -->
              <div class="space-y-4 divide-y divide-border/40">
                {#if editTab === 'general'}
                  <div class="space-y-3 pt-1">
                    <SettingRow label={t('common.name')} helper={t('vmCreate.nameHelper')}>
                      <Input
                        id="edit-name"
                        bind:value={eName}
                        type="text"
                        class="max-w-xs font-medium"
                      />
                    </SettingRow>

                    <SettingRow
                      label={t('vmDetail.groupsLabel')}
                      helper="Organiza esta instancia asociándola a uno o varios grupos de trabajo."
                    >
                      {#if eGroupsList.length > 0}
                        <div class="flex flex-wrap gap-1.5 max-w-sm justify-end pt-1">
                          {#each eGroupsList as g (g.name)}
                            {@const active = eSelectedGroups.includes(g.name)}
                            <button
                              type="button"
                              onclick={() => {
                                if (active) {
                                  eSelectedGroups = eSelectedGroups.filter((x) => x !== g.name);
                                } else {
                                  eSelectedGroups = [...eSelectedGroups, g.name];
                                }
                              }}
                              class="text-xs px-2.5 py-1 rounded-full border transition-all cursor-pointer font-medium select-none {active
                                ? 'border-transparent text-white shadow-sm ring-1 ring-white/20'
                                : 'hover:bg-muted/60 opacity-80 hover:opacity-100'}"
                              style={active
                                ? `background-color: ${g.color}`
                                : `border-color: ${g.color}50; color: ${g.color}; background-color: ${g.color}15`}
                            >
                              {g.name}
                            </button>
                          {/each}
                        </div>
                      {:else}
                        <span class="text-xs text-muted-foreground">{t('vmDetail.noGroups')}</span>
                      {/if}
                    </SettingRow>

                    <SettingRow
                      label={t('vmCreate.startAtBoot')}
                      helper={t('vmCreate.startAtBootHelper')}
                    >
                      <Switch bind:checked={eAutostart} ariaLabel={t('vmCreate.startAtBoot')} />
                    </SettingRow>

                    <SettingRow
                      label={t('vmDetail.networkLabel')}
                      helper={t('vmCreate.networkHelper')}
                    >
                      <select id="edit-net" bind:value={eNetwork} class="input max-w-xs">
                        {#each vmNetworks as net (net.name)}
                          <option value={net.name}>{networkLabel(net)}</option>
                        {/each}
                      </select>
                    </SettingRow>

                    {#if !isContainerVm}
                      <SettingRow
                        label={t('vmDetail.adapter')}
                        helper={t('vmCreate.adapterHelper')}
                      >
                        <select
                          id="edit-netmodel"
                          bind:value={eNetworkModel}
                          class="input max-w-xs"
                        >
                          {#each networkModels as m (m.value)}
                            <option value={m.value} disabled={!supports('network', m.value)}>
                              {m.label}{!supports('network', m.value)
                                ? ' — ' + unavailableReason('network', m.value)
                                : ''}
                            </option>
                          {/each}
                        </select>
                      </SettingRow>
                    {/if}
                  </div>
                {:else if editTab === 'cpu'}
                  <div class="space-y-3 pt-1">
                    <SettingRow label={t('common.vcpu')} helper={t('vmCreate.vcpusHelper')}>
                      <div class="flex items-center gap-2">
                        <Input
                          id="edit-vcpus"
                          type="number"
                          min="1"
                          max="64"
                          bind:value={eVcpus}
                          class="tnum w-24 text-center font-medium"
                        />
                        <span class="text-xs text-muted-foreground">vCPU</span>
                      </div>
                    </SettingRow>

                    {#if !isContainerVm}
                      <SettingRow
                        label={t('vmDetail.cpuModeLabel')}
                        helper={t('vmCreate.cpuModeHelper')}
                      >
                        <select id="edit-cpu" bind:value={eCPUMode} class="input max-w-xs">
                          <option value="host-passthrough"
                            >host-passthrough (Rendimiento nativo)</option
                          >
                          <option value="host-model">host-model (Compatible con migración)</option>
                          <option value="max">max (Todas las características QEMU)</option>
                          <option value="custom">custom</option>
                        </select>
                      </SettingRow>

                      {#if eCPUMode === 'custom'}
                        <SettingRow
                          label={t('vmCreate.cpuModel')}
                          helper={t('vmCreate.cpuModelHelper')}
                        >
                          <select bind:value={eCPUModel} class="input max-w-xs">
                            {#each cpuModelPresets as m (m)}
                              <option value={m} disabled={!supports('cpu', m)}
                                >{m}{!supports('cpu', m)
                                  ? ' — ' + unavailableReason('cpu', m)
                                  : ''}</option
                              >
                            {/each}
                          </select>
                        </SettingRow>
                      {/if}

                      <SettingRow
                        label={t('vmDetail.cpuPriorityLabel')}
                        helper={t('vmDetail.cpuPriorityHelper')}
                        stacked={true}
                      >
                        <CpuPrioritySelector bind:value={eCPUUnits} />
                      </SettingRow>

                      <SettingRow
                        label={t('vmDetail.kvmHidden')}
                        helper={t('vmDetail.kvmHiddenHelper')}
                      >
                        <Switch bind:checked={eKVMHidden} ariaLabel={t('vmDetail.kvmHidden')} />
                      </SettingRow>

                      <SettingRow
                        label={t('vmDetail.cpuFlags')}
                        helper={t('vmDetail.cpuFlagsHelper')}
                        stacked={true}
                      >
                        <CpuFlagPicker
                          bind:flags={eCPUFlags}
                          available={capabilities.cpuFlags}
                          common={COMMON_CPU_FLAGS}
                        />
                      </SettingRow>
                    {/if}

                    <SettingRow label={t('vmDetail.ramLabel')} helper={t('vmCreate.ramHelper')}>
                      <div class="space-y-2 max-w-md flex flex-col items-end">
                        <div class="flex flex-wrap items-center justify-end gap-2">
                          <Input
                            id="edit-ram"
                            type="number"
                            min="256"
                            step="256"
                            bind:value={eRamMB}
                            class="tnum w-32 text-center font-medium"
                          />
                          <span class="text-xs text-muted-foreground"
                            >MB ({(eRamMB / 1024).toFixed(1)} GB)</span
                          >
                        </div>
                        <!-- Quick RAM pills -->
                        <div class="flex flex-wrap gap-1 justify-end">
                          {#each quickRamPresets as p (p)}
                            <button
                              type="button"
                              onclick={() => (eRamMB = p)}
                              class="text-[11px] px-2 py-0.5 rounded border transition-colors {eRamMB ===
                              p
                                ? 'border-accent bg-accent/15 text-accent font-semibold'
                                : 'border-border bg-background text-muted-foreground hover:bg-muted/60'}"
                            >
                              {p >= 1024 ? `${p / 1024} GB` : `${p} MB`}
                            </button>
                          {/each}
                        </div>
                      </div>
                    </SettingRow>

                    {#if !isContainerVm}
                      <SettingRow
                        label={t('vmDetail.minRamLabel')}
                        helper={t('vmDetail.minRamHelper')}
                      >
                        <div class="flex items-center gap-2">
                          <Input
                            id="edit-min-ram"
                            type="number"
                            min="0"
                            max={eRamMB}
                            step="256"
                            bind:value={eMinRamMB}
                            class="tnum w-32 text-center font-medium"
                          />
                          <span class="text-xs text-muted-foreground">
                            {eMinRamMB > 0
                              ? `MB (${(eMinRamMB / 1024).toFixed(1)} GB)`
                              : t('vmDetail.minRamDynamic')}
                          </span>
                        </div>
                      </SettingRow>

                      <SettingRow
                        label={t('vmDetail.iothreadsLabel')}
                        helper={t('vmDetail.iothreadsHelper')}
                      >
                        <div class="flex items-center gap-2">
                          <Input
                            id="edit-iothreads"
                            type="number"
                            min="0"
                            max="16"
                            bind:value={eIOThreads}
                            class="tnum w-24 text-center font-medium"
                          />
                          <span class="text-xs text-muted-foreground"
                            >{eIOThreads > 0
                              ? t('vmDetail.iothreadsDedicated')
                              : t('vmDetail.iothreadsDisabled')}</span
                          >
                        </div>
                      </SettingRow>
                    {/if}
                  </div>
                {:else if editTab === 'display'}
                  <div class="space-y-3 pt-1">
                    {#if !isContainerVm}
                      <SettingRow
                        label={t('vmDetail.videoModelLabel')}
                        helper={t('vmCreate.videoModelHelper')}
                      >
                        <select id="edit-video" bind:value={eVideoModel} class="input max-w-xs">
                          <option value="virtio" disabled={!supports('video', 'virtio')}
                            >VirtIO GPU (Moderno y rápido)</option
                          >
                          <option value="qxl" disabled={!supports('video', 'qxl')}
                            >QXL (Excelente soporte SPICE/Windows){!supports('video', 'qxl')
                              ? ' — ' + unavailableReason('video', 'qxl')
                              : ''}</option
                          >
                          <option value="vga" disabled={!supports('video', 'vga')}
                            >VGA Estándar (Máxima compatibilidad)</option
                          >
                          <option value="cirrus" disabled={!supports('video', 'cirrus')}
                            >Cirrus (Legado / SO antiguos)</option
                          >
                          <option value="vmvga" disabled={!supports('video', 'vmvga')}
                            >VMware SVGA II</option
                          >
                          <option value="bochs" disabled={!supports('video', 'bochs')}
                            >Bochs Display</option
                          >
                          <option value="none">{t('vmCreate.serialOnly')}</option>
                        </select>
                      </SettingRow>

                      <SettingRow
                        label={t('vmCreate.audioDevice')}
                        helper={t('vmCreate.audioDeviceHelper')}
                      >
                        <select bind:value={eAudioModel} class="input max-w-xs">
                          {#each audioModels as m (m.value)}
                            <option value={m.value} disabled={!supports('sound', m.value)}>
                              {m.label}{!supports('sound', m.value)
                                ? ' — ' + unavailableReason('sound', m.value)
                                : ''}
                            </option>
                          {/each}
                        </select>
                      </SettingRow>

                      <SettingRow
                        label={t('vmCreate.serialPort')}
                        helper={t('vmCreate.serialPortHelper')}
                      >
                        <Switch bind:checked={eSerialPort} ariaLabel={t('vmCreate.serialPort')} />
                      </SettingRow>

                      <SettingRow
                        label={t('vmCreate.watchdog')}
                        helper={t('vmCreate.watchdogHelper')}
                      >
                        <Switch bind:checked={eWatchdog} ariaLabel={t('vmCreate.watchdog')} />
                      </SettingRow>
                    {:else}
                      <div
                        class="p-4 rounded-lg bg-muted/20 text-center text-xs text-muted-foreground"
                      >
                        {t('vmDetail.containerConsoleDesc')}
                      </div>
                    {/if}
                  </div>
                {:else if editTab === 'boot' && !isContainerVm}
                  <div class="space-y-3 pt-1">
                    <SettingRow
                      label={t('vmCreate.bootOrder')}
                      helper={t('vmCreate.bootOrderHelper')}
                    >
                      <select id="edit-boot" bind:value={eBootOrder} class="input max-w-xs">
                        {#each bootOrderOptions as opt (opt.value)}
                          <option value={opt.value}>{opt.label}</option>
                        {/each}
                      </select>
                    </SettingRow>

                    <SettingRow label={t('vmDetail.chipset')} helper={t('vmDetail.chipsetLocked')}>
                      <select
                        id="edit-chipset"
                        bind:value={eChipset}
                        disabled
                        class="input max-w-xs opacity-60"
                      >
                        <option value="q35">Q35 (PCIe / Moderno)</option>
                        <option value="i440fx">i440fx (PCI / Legacy)</option>
                      </select>
                    </SettingRow>

                    <SettingRow
                      label={t('vmDetail.firmwareLabel')}
                      helper={eChipset === 'i440fx'
                        ? t('vmDetail.i440fxRequiresSeabios')
                        : t('vmDetail.firmwareHelperDesc')}
                    >
                      <select
                        id="edit-firmware"
                        bind:value={eFirmware}
                        disabled={eChipset === 'i440fx'}
                        class="input max-w-xs {eChipset === 'i440fx' ? 'opacity-60' : ''}"
                      >
                        <option value="uefi">{t('vmDetail.uefiRecommended')}</option>
                        <option value="seabios">{t('vmDetail.biosTraditional')}</option>
                      </select>
                    </SettingRow>

                    {#if eFirmware === 'uefi'}
                      <SettingRow
                        label={t('vmDetail.secureBootLabel')}
                        helper={t('vmCreate.secureBootHelper')}
                      >
                        <Switch
                          bind:checked={eSecureBoot}
                          ariaLabel={t('vmDetail.secureBootLabel')}
                        />
                      </SettingRow>

                      <SettingRow label={t('vmDetail.tpm2')} helper={t('vmCreate.tpmHelper')}>
                        <div class="flex items-center gap-3">
                          <Switch bind:checked={eTPM} ariaLabel={t('vmDetail.tpm2')} />
                          <select
                            bind:value={eTPMVersion}
                            disabled={!eTPM}
                            class="input !py-1 !text-xs w-28 {!eTPM ? 'opacity-50' : ''}"
                          >
                            <option value="2.0">v2.0 (Win11)</option>
                            <option value="1.2">v1.2 (Legacy)</option>
                          </select>
                        </div>
                      </SettingRow>
                    {/if}
                  </div>
                {:else if editTab === 'container' && isContainerVm}
                  <div class="space-y-3 pt-1">
                    <SettingRow
                      label={t('vmCreate.privileged')}
                      helper={t('vmCreate.privilegedHelper')}
                    >
                      <Switch bind:checked={ePrivileged} ariaLabel={t('vmCreate.privileged')} />
                    </SettingRow>

                    <SettingRow label={t('vmCreate.nesting')} helper={t('vmCreate.nestingHelper')}>
                      <Switch bind:checked={eNesting} ariaLabel={t('vmCreate.nesting')} />
                    </SettingRow>

                    <SettingRow
                      label={t('vmCreate.profiles')}
                      helper={t('vmCreate.profilesHelper')}
                    >
                      <select
                        id="edit-profiles"
                        bind:value={eProfiles}
                        multiple
                        class="input h-28 max-w-xs"
                      >
                        {#each incusProfiles as p (p)}
                          <option value={p}>{p}</option>
                        {/each}
                      </select>
                    </SettingRow>
                  </div>
                {:else if editTab === 'os'}
                  <div class="space-y-3 pt-1">
                    <SettingRow
                      label={t('vmDetail.osTypeLabel')}
                      helper="Familia del sistema operativo invitado para optimizar drivers e iconografía."
                    >
                      <select id="edit-ostype" bind:value={eOSType} class="input max-w-xs">
                        <option value="">{t('vmDetail.auto')}</option>
                        <option value="linux">{t('vmDetail.linux')}</option>
                        <option value="windows">{t('vmDetail.windows')}</option>
                        <option value="freebsd">{t('vmDetail.freebsd')}</option>
                        <option value="other">{t('vmDetail.other')}</option>
                      </select>
                    </SettingRow>

                    <SettingRow
                      label={t('vmDetail.osVersionLabel')}
                      helper="Versión específica o variante de la distribución (ej. ubuntu24.04, win11, arch)."
                    >
                      <Input
                        id="edit-osver"
                        bind:value={eOSVersion}
                        placeholder="ej. ubuntu24.04"
                        class="max-w-xs"
                      />
                    </SettingRow>
                  </div>
                {/if}
              </div>

              <div class="flex items-center justify-end gap-2 pt-4 mt-2 border-t border-border">
                <Button variant="outline" onclick={() => (hwEditing = false)} disabled={editSaving}>
                  {t('common.cancel')}
                </Button>
                <Button onclick={saveEdit} disabled={editSaving} class="min-w-[130px]">
                  {#if editSaving}
                    <Spinner size="sm" color="text-white" /> {t('vmDetail.saving')}
                  {:else}
                    <Icon name="check" size={14} class="mr-1.5" />
                    {t('common.save')}
                  {/if}
                </Button>
              </div>
            {:else}
              <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
                <!-- Cómputo -->
                <div
                  class="rounded-xl border border-border bg-card/60 overflow-hidden flex flex-col"
                >
                  <div
                    class="px-4 py-2.5 bg-muted/30 border-b border-border flex items-center gap-2"
                  >
                    <Icon name="cpu" size={14} class="text-accent" />
                    <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
                      {t('vmCreate.compute')}
                    </span>
                  </div>
                  <div class="divide-y divide-border/50 flex-1">
                    {@render hwRow(
                      'cpu',
                      t('common.vcpu'),
                      `${vm.vcpus} vCPU`,
                      'cpu',
                      `${vm.vcpus} cores`,
                      true
                    )}
                    {@render hwRow(
                      'activity',
                      t('vmDetail.ramLabel'),
                      `${vm.ram_mb} MB (${(vm.ram_mb / 1024).toFixed(1)} GB)`,
                      'cpu',
                      `${(vm.ram_mb / 1024).toFixed(1)} GB`,
                      true
                    )}
                    {#if !isContainerVm}
                      {@render hwRow(
                        'settings',
                        t('vmDetail.cpuMode'),
                        vm.cpu_mode === 'custom' && vm.cpu_model
                          ? `custom (${vm.cpu_model})`
                          : vm.cpu_mode || 'host-passthrough',
                        'cpu',
                        vm.cpu_mode === 'host-passthrough' ? 'passthrough' : ''
                      )}
                      {#if vm.cpu_units && vm.cpu_units !== 1024}
                        {@render hwRow(
                          'gauge',
                          t('vmDetail.cpuPriorityLabel'),
                          vm.cpu_units === 512
                            ? t('vmDetail.cpuPriorityLow')
                            : vm.cpu_units === 2048
                              ? t('vmDetail.cpuPriorityHigh')
                              : `${vm.cpu_units} shares`,
                          'cpu'
                        )}
                      {/if}
                      {#if vm.kvm_hidden}
                        {@render hwRow(
                          'shield',
                          t('vmDetail.kvmHidden'),
                          t('vmDetail.kvmHiddenActive'),
                          'cpu',
                          'hidden'
                        )}
                      {/if}
                      {#if vm.cpu_flags && vm.cpu_flags.length > 0}
                        {@render hwRow(
                          'zap',
                          t('vmDetail.cpuFlags'),
                          vm.cpu_flags.join(', '),
                          'cpu'
                        )}
                      {/if}
                    {/if}
                  </div>
                </div>

                <!-- Pantalla & Audio / Periféricos -->
                {#if !isContainerVm}
                  <div
                    class="rounded-xl border border-border bg-card/60 overflow-hidden flex flex-col"
                  >
                    <div
                      class="px-4 py-2.5 bg-muted/30 border-b border-border flex items-center gap-2"
                    >
                      <Icon name="monitor" size={14} class="text-accent" />
                      <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
                        {t('vmCreate.graphicsAudio')}
                      </span>
                    </div>
                    <div class="divide-y divide-border/50 flex-1">
                      {@render hwRow(
                        'monitor',
                        t('vmDetail.video'),
                        (vm.video_model || 'virtio').toUpperCase(),
                        'display',
                        vm.video_model || 'virtio',
                        true
                      )}
                      {@render hwRow(
                        'volume-2',
                        t('vmCreate.audioDevice'),
                        !vm.audio_model || vm.audio_model === 'none'
                          ? t('common.disabled')
                          : vm.audio_model.toUpperCase(),
                        'display',
                        !vm.audio_model || vm.audio_model === 'none'
                          ? t('common.off')
                          : vm.audio_model,
                        false,
                        !vm.audio_model || vm.audio_model === 'none' ? 'muted' : 'accent'
                      )}
                      {@render hwRow(
                        'terminal',
                        t('vmCreate.serialPort'),
                        vm.serial_port !== false
                          ? `${t('common.enabled')} (ttyS0 PTY)`
                          : t('common.disabled'),
                        'display',
                        vm.serial_port !== false ? t('common.on') : t('common.off'),
                        vm.serial_port !== false,
                        vm.serial_port !== false ? 'success' : 'muted'
                      )}
                      {@render hwRow(
                        'shield',
                        t('vmCreate.watchdog'),
                        vm.watchdog_enabled ? t('vmDetail.watchdogModel') : t('common.disabled'),
                        'display',
                        vm.watchdog_enabled ? t('common.on') : t('common.off'),
                        false,
                        vm.watchdog_enabled ? 'success' : 'muted'
                      )}
                    </div>
                  </div>

                  <!-- Arranque & Seguridad -->
                  <div
                    class="rounded-xl border border-border bg-card/60 overflow-hidden flex flex-col"
                  >
                    <div
                      class="px-4 py-2.5 bg-muted/30 border-b border-border flex items-center gap-2"
                    >
                      <Icon name="lock" size={14} class="text-accent" />
                      <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
                        {t('vmCreate.bootSecurity')}
                      </span>
                    </div>
                    <div class="divide-y divide-border/50 flex-1">
                      {@render hwRow(
                        'play',
                        t('vmCreate.bootOrder'),
                        vm.boot_order === 'cdrom'
                          ? t('vmCreate.bootOrderCdrom')
                          : vm.boot_order === 'network'
                            ? t('vmCreate.bootOrderNetwork')
                            : t('vmCreate.bootOrderDisk'),
                        'boot',
                        vm.boot_order || 'disk',
                        true
                      )}
                      {@render hwRow(
                        'server',
                        t('vmDetail.chipset'),
                        vm.chipset || 'q35',
                        'boot',
                        vm.chipset || 'q35'
                      )}
                      {@render hwRow(
                        'zap',
                        t('vmDetail.bios'),
                        vm.firmware === 'uefi' ? 'UEFI OVMF (x86_64)' : 'SeaBIOS (Legacy)',
                        'boot',
                        (vm.firmware || 'seabios').toUpperCase()
                      )}
                      {@render hwRow(
                        'lock',
                        t('vmDetail.secureBoot'),
                        vm.secure_boot ? t('common.yes') : t('common.no'),
                        'boot',
                        vm.secure_boot ? t('common.yes') : t('common.no'),
                        vm.secure_boot,
                        vm.secure_boot ? 'success' : 'muted'
                      )}
                      {@render hwRow(
                        'key',
                        t('vmDetail.tpm'),
                        vm.tpm_enabled
                          ? `TPM ${vm.tpm_version || '2.0'} (swtpm)`
                          : t('common.disabled'),
                        'boot',
                        vm.tpm_enabled ? t('common.on') : t('common.off'),
                        vm.tpm_enabled,
                        vm.tpm_enabled ? 'success' : 'muted'
                      )}
                    </div>
                  </div>
                {:else}
                  <!-- Contenedor Incus / LXC -->
                  <div
                    class="rounded-xl border border-border bg-card/60 overflow-hidden flex flex-col"
                  >
                    <div
                      class="px-4 py-2.5 bg-muted/30 border-b border-border flex items-center gap-2"
                    >
                      <Icon name="box" size={14} class="text-accent" />
                      <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
                        Incus / LXC
                      </span>
                    </div>
                    <div class="divide-y divide-border/50 flex-1">
                      {@render hwRow(
                        'shield',
                        t('vmCreate.privileged'),
                        vm.privileged
                          ? t('vmDetail.privilegedRoot')
                          : t('vmDetail.privilegedIsolated'),
                        'container',
                        vm.privileged ? t('common.yes') : t('common.no'),
                        vm.privileged,
                        vm.privileged ? 'success' : 'muted'
                      )}
                      {@render hwRow(
                        'box',
                        t('vmCreate.nesting'),
                        vm.nesting ? t('vmDetail.nestingEnabled') : t('common.disabled'),
                        'container',
                        vm.nesting ? t('common.yes') : t('common.no'),
                        vm.nesting,
                        vm.nesting ? 'success' : 'muted'
                      )}
                      {@render hwRow(
                        'layers',
                        t('vmCreate.profiles'),
                        (vm.profiles || ['default']).join(', '),
                        'container',
                        `${(vm.profiles || ['default']).length} profiles`
                      )}
                    </div>
                  </div>
                {/if}

                <!-- Sistema & Identidad -->
                <div
                  class="rounded-xl border border-border bg-card/60 overflow-hidden flex flex-col"
                >
                  <div
                    class="px-4 py-2.5 bg-muted/30 border-b border-border flex items-center gap-2"
                  >
                    <Icon name="tag" size={14} class="text-accent" />
                    <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
                      {t('vmCreate.general')}
                    </span>
                  </div>
                  <div class="divide-y divide-border/50 flex-1">
                    {@render hwRow('tag', t('common.name'), vm.name, 'general', '', true)}
                    {@render hwRow(
                      'layers',
                      t('vmDetail.os'),
                      vm.os_type
                        ? `${vm.os_type}${vm.os_version ? ' (' + vm.os_version + ')' : ''}`
                        : t('vmDetail.auto'),
                      'os',
                      vm.os_type || 'auto'
                    )}
                    {@render hwRow(
                      'network',
                      t('vmDetail.networkLabel'),
                      vm.networks?.[0]?.network || vm.networks?.[0]?.bridge || 'default',
                      'general',
                      vm.networks?.[0]?.model || 'virtio'
                    )}
                    {@render hwRow(
                      'power',
                      t('vmCreate.startAtBoot'),
                      vm.autostart ? 'Inicio Automático con el Host' : 'Manual',
                      'general',
                      vm.autostart ? 'yes' : 'no',
                      vm.autostart
                    )}
                    <!-- Groups row with actual colored chips -->
                    <div class="flex items-center justify-between gap-3 py-2.5 px-3.5">
                      <div class="flex items-center gap-3 min-w-0">
                        <div
                          class="w-7 h-7 rounded-md flex items-center justify-center shrink-0 bg-muted text-muted-foreground"
                        >
                          <Icon name="tag" size={14} />
                        </div>
                        <div class="min-w-0">
                          <p
                            class="text-[11px] font-medium text-muted-foreground uppercase tracking-wide leading-tight"
                          >
                            {t('vmDetail.groupsLabel')}
                          </p>
                          <div class="flex flex-wrap items-center gap-1.5 mt-1">
                            {#if Array.isArray(vm.groups) && vm.groups.length > 0}
                              {#each vm.groups as gName (gName)}
                                {@const groupObj = eGroupsList.find((x) => x.name === gName)}
                                <span
                                  class="text-xs px-2 py-0.5 rounded-full border font-medium"
                                  style={groupObj?.color
                                    ? `border-color: ${groupObj.color}50; color: ${groupObj.color}; background-color: ${groupObj.color}15`
                                    : 'border-border bg-muted text-muted-foreground'}
                                >
                                  {gName}
                                </span>
                              {/each}
                            {:else}
                              <span class="text-xs text-muted-foreground"
                                >{t('vmDetail.noGroups')}</span
                              >
                            {/if}
                          </div>
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            {/if}
          </BlockCard>
        {/snippet}

        {#snippet sec_metrics()}
          <BlockCard bid="metrics" title={t('vmDetail.metrics')}>
            <div class="flex items-center justify-between mb-3">
              <span class="text-xs text-muted-foreground tnum"
                >{t('vmDetail.updatedAt', {
                  time: metrics?.sampled_at
                    ? new Date(metrics.sampled_at * 1000).toLocaleTimeString()
                    : '—',
                })}</span
              >
            </div>
            {#if vm?.state !== 'running'}
              <p class="text-sm text-muted-foreground">
                {t('vmDetail.metricsOff')}
              </p>
            {:else if !metrics || (cpuPoints.length === 0 && ramPoints.length === 0)}
              <p class="text-sm text-muted-foreground">{t('vmDetail.collectingSamples')}</p>
            {:else}
              <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div>
                  <div class="flex items-baseline justify-between mb-1.5">
                    <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >{t('vms.cpu')}</span
                    >
                    <span class="text-sm tnum"
                      >{cpuPoints.length
                        ? cpuPoints[cpuPoints.length - 1].v.toFixed(1)
                        : '0.0'}%</span
                    >
                  </div>
                  <Chart points={cpuPoints} yMax={100} height={70} />
                </div>
                <div>
                  <div class="flex items-baseline justify-between mb-1.5">
                    <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >{t('common.ram')}</span
                    >
                    <span class="text-sm tnum"
                      >{ramPoints.length
                        ? ramPoints[ramPoints.length - 1].v.toFixed(1)
                        : '0.0'}%</span
                    >
                  </div>
                  <Chart points={ramPoints} yMax={100} height={70} />
                </div>
                <div>
                  <div class="flex items-baseline justify-between mb-1.5">
                    <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >{t('vmDetail.diskRead')}</span
                    >
                    <span class="text-sm tnum"
                      >{diskRPoints.length
                        ? formatRate(diskRPoints[diskRPoints.length - 1].v)
                        : '0 B/s'}</span
                    >
                  </div>
                  <Chart points={diskRPoints} height={70} color="var(--success)" />
                </div>
                <div>
                  <div class="flex items-baseline justify-between mb-1.5">
                    <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >{t('vmDetail.diskWrite')}</span
                    >
                    <span class="text-sm tnum"
                      >{diskWPoints.length
                        ? formatRate(diskWPoints[diskWPoints.length - 1].v)
                        : '0 B/s'}</span
                    >
                  </div>
                  <Chart points={diskWPoints} height={70} color="var(--warning)" />
                </div>
                <div>
                  <div class="flex items-baseline justify-between mb-1.5">
                    <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >{t('vmDetail.netRx')}</span
                    >
                    <span class="text-sm tnum"
                      >{netRxPoints.length
                        ? formatRate(netRxPoints[netRxPoints.length - 1].v)
                        : '0 B/s'}</span
                    >
                  </div>
                  <Chart points={netRxPoints} height={70} color="var(--info, var(--accent))" />
                </div>
                <div>
                  <div class="flex items-baseline justify-between mb-1.5">
                    <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >{t('vmDetail.netTx')}</span
                    >
                    <span class="text-sm tnum"
                      >{netTxPoints.length
                        ? formatRate(netTxPoints[netTxPoints.length - 1].v)
                        : '0 B/s'}</span
                    >
                  </div>
                  <Chart points={netTxPoints} height={70} color="var(--info, var(--accent))" />
                </div>
              </div>
            {/if}
          </BlockCard>
        {/snippet}

        {#snippet sec_cloudinit()}
          <BlockCard bid="cloudinit" title="Cloud-Init">
            {#snippet headerActions()}
              {#if auth.canMutate() && vm.state !== 'shutoff'}
                <span
                  class="text-[11px] px-2 py-0.5 rounded border border-accent/30 bg-accent/10 text-accent"
                >
                  {t('vmDetail.cloudInitLiveHint')}
                </span>
              {/if}
            {/snippet}

            {#if ciStatusLoading}
              <div class="flex justify-center py-6"><Spinner size="md" /></div>
            {:else}
              <div class="space-y-4">
                <!-- Current status strip -->
                <div
                  class="flex flex-wrap items-center gap-3 p-3 rounded-lg border border-border bg-muted/20"
                >
                  <div class="flex items-center gap-1.5">
                    <div
                      class="w-2 h-2 rounded-full {ciStatus?.has_seed_iso
                        ? 'bg-success'
                        : 'bg-muted-foreground/40'}"
                    ></div>
                    <span class="text-sm font-medium">
                      {ciStatus?.has_seed_iso
                        ? t('vmDetail.cloudInitSeedAttached')
                        : t('vmDetail.cloudInitNoSeed')}
                    </span>
                  </div>
                  {#if ciStatus?.user}
                    <span
                      class="text-xs font-mono px-2 py-0.5 rounded bg-background border border-border"
                    >
                      {t('vmCreate.cloudInitUser')}: {ciStatus.user}
                    </span>
                  {/if}
                </div>

                <p class="text-xs text-muted-foreground">
                  {t('vmDetail.cloudInitDesc')}
                </p>

                <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
                  <div>
                    <label
                      for="ci-detail-user"
                      class="text-xs font-semibold text-foreground block mb-1"
                      >{t('vmCreate.cloudInitUser')}</label
                    >
                    <Input
                      id="ci-detail-user"
                      bind:value={ciUser}
                      placeholder="webkvm"
                      class="w-full"
                    />
                  </div>
                  <div>
                    <label
                      for="ci-detail-password"
                      class="text-xs font-semibold text-foreground block mb-1"
                      >{t('common.password')}</label
                    >
                    <Input
                      id="ci-detail-password"
                      type="password"
                      bind:value={ciPassword}
                      placeholder="••••••••"
                      class="w-full"
                    />
                  </div>
                  <div>
                    <label
                      for="ci-detail-hostname"
                      class="text-xs font-semibold text-foreground block mb-1"
                      >{t('vmCreate.cloudInitHostname')}</label
                    >
                    <Input
                      id="ci-detail-hostname"
                      bind:value={ciHostname}
                      placeholder="my-vm"
                      class="w-full"
                    />
                  </div>
                  <div>
                    <label
                      for="ci-detail-sshkey"
                      class="text-xs font-semibold text-foreground block mb-1"
                      >{t('vmCreate.cloudInitSSHKey')}</label
                    >
                    <Input
                      id="ci-detail-sshkey"
                      bind:value={ciSSHKey}
                      placeholder="ssh-ed25519 AAAA..."
                      class="w-full"
                    />
                  </div>
                </div>

                {#if ciAvailableSnippets.length > 0}
                  <div>
                    <span class="text-xs font-semibold text-foreground block mb-1.5"
                      >{t('snippets.selectPreset')}</span
                    >
                    <div class="flex flex-wrap gap-1.5">
                      {#each ciAvailableSnippets as sn (sn.id)}
                        {@const active = ciSnippetIds.includes(sn.id)}
                        <button
                          type="button"
                          onclick={() => {
                            if (active) {
                              ciSnippetIds = ciSnippetIds.filter((x) => x !== sn.id);
                            } else {
                              ciSnippetIds = [...ciSnippetIds, sn.id];
                            }
                          }}
                          class="text-xs px-2.5 py-1 rounded-full border transition-all cursor-pointer font-medium {active
                            ? 'border-accent bg-accent/15 text-accent'
                            : 'border-border bg-background text-muted-foreground hover:bg-muted/60'}"
                        >
                          {sn.name}
                        </button>
                      {/each}
                    </div>
                  </div>
                {/if}

                {#if auth.canMutate()}
                  <div class="flex justify-end pt-1">
                    <Button onclick={reapplyCloudInit} disabled={ciApplying}>
                      {#if ciApplying}
                        <Spinner size="sm" color="text-white" />
                        {t('vmDetail.cloudInitApplying')}
                      {:else}
                        <Icon name="refresh" size={14} class="mr-1.5" />
                        {t('vmDetail.cloudInitRegenerate')}
                      {/if}
                    </Button>
                  </div>
                {/if}
              </div>
            {/if}
          </BlockCard>
        {/snippet}

        {#snippet sec_disks()}
          <BlockCard bid="disks" title={t('vmDetail.disks')}>
            {#if !isContainerVm}
              <div class="flex items-center justify-between mb-3">
                <Button
                  size="xs"
                  variant="outline"
                  onclick={() => {
                    aDiskDevice = 'disk';
                    aDiskBus = 'virtio';
                    aDiskSize = 10;
                    aDiskPool = vmDiskPools(pools)[0]?.name || 'webkvm-disks';
                    aDiskExistingVol = '';
                    aDiskVolumes = [];
                    aDiskWWN = '';
                    aDiskSerial = '';
                    aDiskAlias = '';
                    showAddDisk = true;
                  }}>+ Add Disk</Button
                >
              </div>
            {/if}
            {#if !vm.disks || vm.disks.length === 0}
              <EmptyState compact icon="hardDrive" title={t('vmDetail.noDisks')} />
            {:else}
              <div class="space-y-1.5">
                {#each vm.disks as disk (disk.target || disk.source || disk.name)}
                  <div
                    class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
                  >
                    <div class="flex items-center gap-2 min-w-0">
                      <span class="text-xs font-mono text-muted-foreground w-8">{disk.target}</span>
                      <span
                        class="text-xs px-1.5 py-0.5 rounded border {disk.device === 'cdrom'
                          ? 'border-accent/30 bg-accent/10 text-accent'
                          : disk.zvol
                            ? 'border-primary/30 bg-primary/10 text-primary'
                            : 'border-border bg-muted text-muted-foreground'}"
                        >{disk.device === 'cdrom' ? 'CDROM' : disk.zvol ? 'ZVOL' : 'DISK'}</span
                      >
                      <span class="text-xs text-muted-foreground">{disk.bus}</span>
                      <span class="text-sm truncate">{diskLabel(disk)}</span>
                      {#if disk.serial}
                        <span
                          class="text-[11px] font-mono px-1.5 py-0.5 rounded bg-muted text-muted-foreground"
                          title={t('vmDetail.diskSerialLabel')}>SN: {disk.serial}</span
                        >
                      {/if}
                      {#if disk.wwn}
                        <span
                          class="text-[11px] font-mono px-1.5 py-0.5 rounded bg-muted text-muted-foreground"
                          title={t('vmDetail.diskWwnLabel')}>WWN: {disk.wwn}</span
                        >
                      {/if}
                      {#if disk.alias}
                        <span
                          class="text-[11px] font-mono px-1.5 py-0.5 rounded bg-muted text-muted-foreground"
                          title={t('vmDetail.diskAliasLabel')}>{disk.alias}</span
                        >
                      {/if}
                    </div>
                    <div class="flex items-center gap-1 shrink-0">
                      {#if disk.device === 'cdrom'}
                        <button
                          onclick={() => {
                            cISOTarget = disk.target;
                            cISOSource = disk.source || '';
                            showChangeISO = true;
                          }}
                          class="text-xs text-accent hover:text-accent-hover px-2 py-1 rounded hover:bg-muted"
                          >{t('vmDetail.changeIso')}</button
                        >
                      {:else if disk.pool}
                        <span class="text-xs text-muted-foreground tnum"
                          >{disk.size_gb ? disk.size_gb + ' GB' : ''}</span
                        >
                        <button
                          onclick={() => {
                            resizeDiskTarget = disk.target;
                            resizeDiskSize = disk.size_gb || 10;
                            resizeDiskCurrent = disk.size_gb || 0;
                            showResizeDisk = true;
                          }}
                          class="text-xs text-accent hover:text-accent-hover px-2 py-1 rounded hover:bg-muted"
                          >{t('vmDetail.resize')}</button
                        >
                      {/if}
                      {#if !isContainerVm}
                        <button
                          onclick={() => {
                            changeBusTarget = disk.target;
                            changeBusCurrent = disk.bus || '';
                            changeBusNew = disk.bus || 'virtio';
                            showChangeBus = true;
                          }}
                          class="text-xs text-accent hover:text-accent-hover px-2 py-1 rounded hover:bg-muted"
                          >{t('vmDetail.changeBus')}</button
                        >
                        <button
                          onclick={() => {
                            if (!requireShutoff(t('vmDetail.removing'))) return;
                            removeDisk(disk.target);
                          }}
                          title={t('vmDetail.requireShutoffTitle')}
                          class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10"
                          >{t('vmDetail.remove')}</button
                        >
                      {/if}
                    </div>
                  </div>
                {/each}
              </div>
            {/if}
          </BlockCard>
        {/snippet}

        {#snippet sec_shared_folders()}
          <BlockCard bid="sharedfolders" title={t('vmDetail.sharedFolders')}>
            <div class="space-y-3">
              <p class="text-xs text-muted-foreground">{t('vmDetail.sharedFoldersHint')}</p>
              {#if !vm.shared_folders || vm.shared_folders.length === 0}
                <EmptyState
                  compact
                  icon="folderOpen"
                  title={t('vmDetail.sharedFoldersNoneAttached')}
                />
              {:else}
                <div class="space-y-2">
                  {#each vm.shared_folders as f (f.tag)}
                    <div class="rounded-md border border-border bg-background p-2 space-y-1">
                      <div class="flex items-center justify-between">
                        <div class="text-xs min-w-0">
                          <div class="font-mono truncate">{f.host_path}</div>
                          <div class="text-muted-foreground">
                            {t('vmDetail.sharedFolderTag')}: <span class="font-mono">{f.tag}</span>
                            {#if f.read_only}· {t('vmDetail.sharedFolderReadOnly')}{/if}
                          </div>
                        </div>
                        <button
                          onclick={() => detachSharedFolder(f.tag)}
                          disabled={actionLoading === 'sharedfolder' || vm.state !== 'shutoff'}
                          title={vm.state !== 'shutoff'
                            ? t('vmDetail.sharedFolderRequiresShutoff')
                            : ''}
                          class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10 disabled:opacity-50 disabled:cursor-not-allowed shrink-0"
                          >{t('vmDetail.sharedFolderDetach')}</button
                        >
                      </div>
                      <div class="text-[10px] font-mono text-muted-foreground truncate">
                        {sharedFolder9pMountCommand(f.tag)}
                      </div>
                    </div>
                  {/each}
                </div>
              {/if}
              <div class="border-t border-border pt-3 space-y-2">
                <div class="text-xs font-medium text-muted-foreground">
                  {t('vmDetail.sharedFolderAdd')}
                </div>
                <Input
                  bind:value={newSharedFolderPath}
                  placeholder={t('vmDetail.sharedFolderHostPath')}
                  class="!text-xs"
                />
                <Input
                  bind:value={newSharedFolderTag}
                  placeholder={t('vmDetail.sharedFolderTag')}
                  class="!text-xs"
                />
                <label class="flex items-center gap-1.5 text-xs">
                  <input type="checkbox" bind:checked={newSharedFolderReadOnly} />
                  {t('vmDetail.sharedFolderReadOnly')}
                </label>
                <Button
                  size="xs"
                  variant="outline"
                  onclick={attachSharedFolder}
                  disabled={actionLoading === 'sharedfolder' ||
                    vm.state !== 'shutoff' ||
                    !newSharedFolderPath ||
                    !newSharedFolderTag}
                  title={vm.state !== 'shutoff' ? t('vmDetail.sharedFolderRequiresShutoff') : ''}
                  >{t('vmDetail.sharedFolderAttach')}</Button
                >
              </div>
            </div>
          </BlockCard>
        {/snippet}

        {#snippet sec_net()}
          <BlockCard bid="net" title={t('vmDetail.networkInterfaces')}>
            <div class="flex items-center justify-between mb-3">
              <Button
                size="xs"
                variant="outline"
                onclick={() => {
                  aNetNetwork = vmNetworks[0]?.name || 'default';
                  aNetModel = isContainerVm ? 'incus' : 'virtio';
                  showAddNet = true;
                }}>+ {t('vmDetail.addInterface')}</Button
              >
            </div>
            {#if !vm.networks || vm.networks.length === 0}
              <EmptyState compact icon="network" title={t('vmDetail.noNetworkInterfaces')} />
            {:else}
              <div class="space-y-1.5">
                {#each vm.networks as iface (iface.mac || iface.target || iface.name)}
                  <div
                    class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
                  >
                    <div class="flex items-center gap-2 flex-wrap min-w-0">
                      <span class="text-xs font-mono text-muted-foreground">{iface.mac}</span>
                      <span
                        class="text-xs px-1.5 py-0.5 rounded border border-border bg-muted text-muted-foreground"
                        >{iface.model}</span
                      >
                      <span class="text-sm">{networkLabelFor(iface.network, networks)}</span>
                      {#if vm.state === 'running' && iface.ips?.length}
                        <span
                          class="text-xs px-1.5 py-0.5 rounded bg-accent/10 border border-accent/20 text-accent font-mono"
                          >{iface.ips.join(', ')}</span
                        >
                      {/if}
                    </div>
                    <button
                      onclick={() => removeNet(iface.mac)}
                      class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10 disabled:opacity-50 disabled:cursor-not-allowed"
                      >{t('vmDetail.remove')}</button
                    >
                  </div>
                {/each}
              </div>
            {/if}
          </BlockCard>
        {/snippet}

        {#snippet sec_usb()}
          <BlockCard bid="usb" title={t('vmDetail.usbDevices')}>
            <div class="space-y-3">
              <div>
                <div class="text-xs font-medium text-muted-foreground mb-1.5">
                  {t('vmDetail.usbAttached')}
                </div>
                {#if !vm.usb_devices || vm.usb_devices.length === 0}
                  <EmptyState compact icon="usb" title={t('vmDetail.usbNoneAttached')} />
                {:else}
                  <div class="space-y-1.5">
                    <!-- Keyed on position, not on the device identity.
                         parseUSBHostdevs (libvirt/domain.go) only reads
                         vendor and product out of the hostdev XML, so Name,
                         Bus and Device arrive empty and the old key
                         collapsed to "vendor:product:". Two identical USB
                         devices — two of the same stick, mouse or webcam —
                         then produced the same key, and Svelte 5 aborts the
                         whole render on each_key_duplicate, leaving the
                         section empty. The list is a static set of attached
                         devices, so position is a sound identity here. -->
                    {#each vm.usb_devices as dev, i (i)}
                      <div
                        class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
                      >
                        <span class="text-xs font-mono text-muted-foreground"
                          >{dev.vendor_id}:{dev.product_id}</span
                        >
                        <button
                          onclick={() => detachUSB(dev.vendor_id, dev.product_id)}
                          disabled={actionLoading === 'usb'}
                          class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10 disabled:opacity-50 disabled:cursor-not-allowed"
                          >{t('vmDetail.usbDetach')}</button
                        >
                      </div>
                    {/each}
                  </div>
                {/if}
              </div>
              <div>
                <div class="flex items-center justify-between mb-1.5">
                  <div class="text-xs font-medium text-muted-foreground">
                    {t('vmDetail.usbAvailable')}
                  </div>
                  <button
                    onclick={() => loadHostUSBDevices(true)}
                    class="text-xs text-accent hover:text-accent-hover"
                    >{t('common.refresh')}</button
                  >
                </div>
                {#if hostUSBDevices.length === 0}
                  <EmptyState compact icon="usb" title={t('vmDetail.usbNoneFound')} />
                {:else}
                  <div class="space-y-1.5">
                    {#each hostUSBDevices as dev (`${dev.bus}:${dev.device}:${dev.vendor_id}:${dev.product_id}`)}
                      <div
                        class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
                      >
                        <div class="flex items-center gap-2 min-w-0">
                          <span class="text-sm truncate">{dev.name}</span>
                          <span class="text-xs font-mono text-muted-foreground"
                            >{dev.vendor_id}:{dev.product_id} · bus {dev.bus} dev {dev.device}</span
                          >
                        </div>
                        <button
                          onclick={() => attachUSB(dev.vendor_id, dev.product_id)}
                          disabled={actionLoading === 'usb'}
                          class="text-xs text-accent hover:text-accent-hover px-2 py-1 rounded hover:bg-muted disabled:opacity-50 disabled:cursor-not-allowed"
                          >{t('vmDetail.usbAttach')}</button
                        >
                      </div>
                    {/each}
                  </div>
                {/if}
              </div>
            </div>
          </BlockCard>
        {/snippet}

        {#snippet sec_pci()}
          <BlockCard bid="pci" title={t('vmDetail.pciDevices')}>
            <div class="space-y-3">
              <p class="text-xs text-muted-foreground">{t('vmDetail.pciHint')}</p>
              <div>
                <div class="text-xs font-medium text-muted-foreground mb-1.5">
                  {t('vmDetail.pciAttached')}
                </div>
                {#if !vm.pci_devices || vm.pci_devices.length === 0}
                  <p class="text-sm text-muted-foreground">{t('vmDetail.pciNoneAttached')}</p>
                {:else}
                  <div class="space-y-1.5">
                    {#each vm.pci_devices as dev (dev.address)}
                      <div
                        class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
                      >
                        <span class="text-xs font-mono text-muted-foreground">{dev.address}</span>
                        <button
                          onclick={() => detachPCI(dev.address)}
                          disabled={actionLoading === 'pci' || vm.state !== 'shutoff'}
                          title={vm.state !== 'shutoff' ? t('vmDetail.pciRequiresShutoff') : ''}
                          class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10 disabled:opacity-50 disabled:cursor-not-allowed"
                          >{t('vmDetail.pciDetach')}</button
                        >
                      </div>
                    {/each}
                  </div>
                {/if}
              </div>
              <div class="space-y-3">
                <PciPreflightPanel
                  info={hostPCIPreflight}
                  loading={pciPreflightLoading}
                  onrefresh={() => {
                    loadHostPCIPreflight();
                    loadHostPCIDevices(true);
                  }}
                />

                <div>
                  <div class="flex items-center justify-between mb-2">
                    <div class="text-xs font-medium text-muted-foreground">
                      {t('vmDetail.pciAvailable')}
                    </div>
                    <button
                      onclick={() => {
                        loadHostPCIDevices(true);
                        loadHostPCIPreflight();
                      }}
                      class="text-xs text-accent hover:text-accent-hover"
                      >{t('common.refresh')}</button
                    >
                  </div>
                  {#if hostPCIError}
                    <p class="text-sm text-destructive">{hostPCIError}</p>
                  {:else if hostPCIGroups.length === 0}
                    <EmptyState compact icon="circuitBoard" title={t('vmDetail.pciNoneFound')} />
                  {:else}
                    <div class="space-y-2">
                      {#each hostPCIGroups as group (group.group)}
                        <PciGroupCard
                          {group}
                          vmState={vm.state}
                          loading={actionLoading === 'pci'}
                          onattach={attachPCIGroup}
                        />
                      {/each}
                    </div>
                  {/if}
                </div>
              </div>
            </div>
          </BlockCard>
        {/snippet}

        {#snippet sec_guest()}
          <BlockCard bid="guest" title={t('vmDetail.guestTab')} anchor="guest">
            {#snippet headerActions()}
              <div class="flex items-center gap-1.5">
                {#if vm?.state === 'running' && guestInfo?.available}
                  <Button
                    size="xs"
                    variant="outline"
                    onclick={runGuestFSTrim}
                    disabled={actionLoading === 'fstrim'}
                    title={t('vmDetail.fstrimDesc')}
                  >
                    {#if actionLoading === 'fstrim'}<Spinner size="xs" />{:else}<Icon
                        name="sparkles"
                        size={13}
                        class="mr-1"
                      />{t('vmDetail.fstrimBtn')}{/if}
                  </Button>
                {/if}
                <Button size="xs" variant="outline" onclick={loadGuestInfo} disabled={guestLoading}>
                  {#if guestLoading}<Spinner size="xs" />{:else}{t('common.refresh')}{/if}
                </Button>
              </div>
            {/snippet}

            {#if guestLoading && !guestInfo}
              <div class="flex justify-center py-10"><Spinner size="lg" /></div>
            {:else if !guestInfo?.available}
              <div class="rounded-lg border border-border bg-muted/20 p-4 text-center">
                <Icon name="info" size={20} class="mx-auto mb-2 text-muted-foreground" />
                <p class="text-sm text-muted-foreground mb-1">
                  {guestInfo?.error || t('vmDetail.guestUnavailable')}
                </p>
                <p class="text-xs text-muted-foreground">{t('vmDetail.guestInstallHint')}</p>
                <code
                  class="mt-2 inline-block text-left p-2 rounded bg-muted font-mono text-[11px] select-all"
                >
                  sudo apt install qemu-guest-agent && sudo systemctl enable --now qemu-guest-agent
                </code>
              </div>
            {:else}
              <!-- OS identity -->
              {#if guestInfo.os || guestInfo.hostname || guestInfo.timezone}
                <div class="mb-4 grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-1.5 text-sm">
                  {#if guestInfo.os?.pretty_name || guestInfo.os?.name}
                    <div class="flex gap-2">
                      <span class="text-muted-foreground shrink-0">{t('vmDetail.guestOs')}</span>
                      <span class="truncate">{guestInfo.os.pretty_name || guestInfo.os.name}</span>
                    </div>
                  {/if}
                  {#if guestInfo.hostname}
                    <div class="flex gap-2">
                      <span class="text-muted-foreground shrink-0"
                        >{t('vmDetail.guestHostname')}</span
                      >
                      <span class="font-mono truncate">{guestInfo.hostname}</span>
                    </div>
                  {/if}
                  {#if guestInfo.os?.kernel_release}
                    <div class="flex gap-2">
                      <span class="text-muted-foreground shrink-0">{t('vmDetail.guestKernel')}</span
                      >
                      <span class="font-mono truncate">{guestInfo.os.kernel_release}</span>
                    </div>
                  {/if}
                  {#if guestInfo.os?.machine}
                    <div class="flex gap-2">
                      <span class="text-muted-foreground shrink-0">{t('vmDetail.guestArch')}</span>
                      <span class="font-mono">{guestInfo.os.machine}</span>
                    </div>
                  {/if}
                  {#if guestInfo.timezone?.zone}
                    <div class="flex gap-2">
                      <span class="text-muted-foreground shrink-0"
                        >{t('vmDetail.guestTimezone')}</span
                      >
                      <span class="font-mono">{guestInfo.timezone.zone}</span>
                    </div>
                  {/if}
                </div>
              {/if}

              <!-- Active Guest User Sessions -->
              {#if guestInfo.users?.length}
                <h3 class="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2">
                  {t('vmDetail.guestUsers')}
                </h3>
                <div class="flex flex-wrap gap-1.5 mb-4">
                  {#each guestInfo.users as u (u.user)}
                    <span
                      class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded border border-border bg-background text-xs font-mono"
                    >
                      <Icon name="user" size={13} class="text-muted-foreground" />
                      <span>{u.user}</span>
                      {#if u.domain}<span class="text-[10px] text-muted-foreground"
                          >({u.domain})</span
                        >{/if}
                    </span>
                  {/each}
                </div>
              {/if}

              <!-- Real in-guest filesystem usage -->
              {#if guestInfo.filesystems?.length}
                <h3 class="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2">
                  {t('vmDetail.guestFilesystems')}
                </h3>
                <div class="space-y-2 mb-4">
                  {#each guestInfo.filesystems as fs (fs.mountpoint + fs.name)}
                    <div class="rounded-md border border-border bg-background p-2.5">
                      <div class="flex items-center justify-between gap-2 mb-1.5">
                        <div class="flex items-center gap-2 min-w-0">
                          <span class="font-mono text-sm font-medium truncate">{fs.mountpoint}</span
                          >
                          <span
                            class="text-[10px] px-1.5 py-0.5 rounded border border-border bg-muted text-muted-foreground uppercase"
                            >{fs.type}</span
                          >
                        </div>
                        {#if fs.total_bytes > 0}
                          <span class="text-xs text-muted-foreground tnum shrink-0">
                            {formatBytes(fs.used_bytes)} / {formatBytes(fs.total_bytes)}
                          </span>
                        {/if}
                      </div>
                      {#if fs.total_bytes > 0}
                        <Gauge value={fs.used_pct} variant="linear" showValue={true} label="" />
                      {/if}
                    </div>
                  {/each}
                </div>
              {/if}

              <!-- Every address the guest actually holds -->
              {#if guestInfo.interfaces?.length}
                <h3 class="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2">
                  {t('vmDetail.guestInterfaces')}
                </h3>
                <div class="space-y-1.5">
                  {#each guestInfo.interfaces as iface (iface.name)}
                    <div
                      class="flex items-start justify-between gap-3 px-3 py-2 rounded-md border border-border bg-background"
                    >
                      <div class="min-w-0">
                        <span class="font-mono text-sm font-medium">{iface.name}</span>
                        {#if iface.mac}
                          <span class="ml-2 text-[11px] font-mono text-muted-foreground"
                            >{iface.mac}</span
                          >
                        {/if}
                      </div>
                      <div class="flex flex-col items-end gap-0.5 min-w-0">
                        {#each iface.ipv4 || [] as ip (ip)}
                          <span class="font-mono text-xs text-accent truncate">{ip}</span>
                        {/each}
                        {#each iface.ipv6 || [] as ip (ip)}
                          <span class="font-mono text-[10px] text-muted-foreground truncate"
                            >{ip}</span
                          >
                        {/each}
                      </div>
                    </div>
                  {/each}
                </div>
              {/if}
            {/if}
          </BlockCard>
        {/snippet}

        {#snippet snapRetention()}
          <div class="pt-3 border-t border-border/50 space-y-2">
            <label for="sched-snap-max" class="text-xs text-muted-foreground block">
              {t('vmDetail.snapMaxKeep')}
            </label>
            <div class="flex flex-wrap items-center gap-2">
              <select
                value={schedSnapMax}
                onchange={(e) => (schedSnapMax = Number(e.target.value) || 3)}
                class="input !h-8 text-xs w-auto"
              >
                <option value={1}>1</option>
                <option value={3}>3</option>
                <option value={5}>5</option>
                <option value={7}>7</option>
                <option value={14}>14</option>
                <option value={30}>30</option>
              </select>
              <span class="text-xs text-muted-foreground">·</span>
              <Input
                id="sched-snap-max"
                type="number"
                min="1"
                max="999"
                bind:value={schedSnapMax}
                placeholder={t('vmDetail.retentionCustom')}
                class="!h-8 w-24 tnum text-xs"
              />
              <span class="text-xs text-muted-foreground">
                {t('vmDetail.retentionCount', { n: schedSnapMax }) ||
                  `${schedSnapMax} instantáneas`}
              </span>
            </div>
          </div>
        {/snippet}

        {#snippet sec_snaps()}
          <BlockCard bid="snaps" title={t('vmDetail.snapshots')} anchor="snapshots">
            <div class="flex gap-2 mb-3">
              <Input bind:value={snapName} placeholder="Snapshot name" class="flex-1" />
              <Button onclick={createSnapshot} disabled={!snapName || actionLoading === 'snapshot'}>
                {#if actionLoading === 'snapshot'}<Spinner size="xs" color="text-white" />{:else}{t(
                    'vmDetail.createSnapshot'
                  )}{/if}
              </Button>
            </div>
            <label
              class="flex items-center gap-2 text-sm text-muted-foreground mb-3 cursor-pointer select-none"
            >
              <input
                type="checkbox"
                bind:checked={snapMemory}
                class="w-4 h-4 rounded border-border"
              />
              {t('vmDetail.snapshotWithMemory')}
            </label>
            {#if snapshots.length === 0}
              <EmptyState compact icon="camera" title={t('vmDetail.noSnapshots')} />
            {:else}
              <div class="space-y-0.5">
                {#each snapshotTree.roots as root (root.name)}
                  {@render snapshotNode(root, 0)}
                {/each}
              </div>
            {/if}

            <!-- Automated Snapshot Policy -->
            <div class="mt-4 pt-3 border-t border-border">
              <div class="flex items-center justify-between mb-2">
                <div class="flex items-center gap-2">
                  <Icon name="clock" size={13} class="text-accent" />
                  <span class="text-xs font-semibold text-foreground">
                    {t('vmDetail.snapScheduleTitle')}
                  </span>
                </div>
                <Switch
                  size="sm"
                  checked={schedSnap !== ''}
                  onchange={(v) => (schedSnap = v ? '0 2 * * *' : '')}
                />
              </div>
              {#if !schedSnap}
                <p class="text-xs text-muted-foreground py-1">
                  {t('vmDetail.noSnapshotsScheduled')}
                </p>
              {:else}
                <CronPicker bind:expression={schedSnap} />
                {@render snapRetention()}
              {/if}
              <div class="mt-3 flex justify-end">
                <Button size="xs" onclick={saveSchedule} disabled={schedSaving}>
                  {#if schedSaving}<Spinner size="xs" color="text-white" />{:else}{t(
                      'common.save'
                    )}{/if}
                </Button>
              </div>
            </div>
          </BlockCard>
        {/snippet}

        {#snippet sec_firewall()}
          <BlockCard bid="firewall" title={t('vmDetail.firewall')} anchor="firewall">
            <!-- Quick per-VM template (merge, never replaces) -->
            <div class="mb-4 flex flex-wrap items-center gap-2">
              <select
                class="input h-8 text-xs"
                bind:value={fwTplSel}
                aria-label={t('firewall.vmPickTemplate')}
              >
                <option value="">{t('firewall.vmPickTemplate')}</option>
                {#each VM_TEMPLATE_PRESETS as tpl (tpl.id)}
                  <option value={tpl.id}>{t(tpl.labelKey)}</option>
                {/each}
              </select>
              <Button size="xs" variant="outline" disabled={!fwTplSel} onclick={applyVmTemplate}>
                {t('firewall.vmApplyTemplate')}
              </Button>
            </div>
            <!-- Port forwards -->
            <div class="mb-4">
              <h3 class="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2">
                {t('vmDetail.portForwards')}
              </h3>
              <div class="space-y-2">
                {#each fwForwards as fw (fw.id)}
                  <div class="flex items-center gap-2 {fw.disabled ? 'opacity-50' : ''}">
                    <span
                      title={fw.disabled ? t('firewall.enableRule') : t('firewall.disableRule')}
                    >
                      <Switch
                        size="sm"
                        checked={!fw.disabled}
                        onchange={() => (fw.disabled = !fw.disabled)}
                      />
                    </span>
                    <select class="input w-24" bind:value={fw.proto}>
                      <option value="tcp">tcp</option>
                      <option value="udp">udp</option>
                      <option value="both">both</option>
                    </select>
                    <Input
                      type="number"
                      class="w-28 tnum"
                      bind:value={fw.host_port}
                      min="1"
                      max="65535"
                      placeholder="Host port"
                    />
                    <span class="text-muted-foreground">→</span>
                    <Input
                      type="number"
                      class="w-28 tnum"
                      bind:value={fw.guest_port}
                      min="1"
                      max="65535"
                      placeholder="Guest port"
                    />
                    {#if fw.applied}
                      <span class="text-xs text-success">{t('vmDetail.fwApplied')}</span>
                    {:else if fw.target_ip}
                      <span class="text-xs text-muted-foreground">{fw.target_ip}</span>
                    {:else}
                      <span class="text-xs text-warning">{t('vmDetail.fwPending')}</span>
                    {/if}
                    <Button size="xs" variant="ghost" onclick={() => removeForward(fw.id)}>×</Button
                    >
                  </div>
                {/each}
                {#if fwForwards.length === 0}
                  <EmptyState compact icon="shieldOff" title={t('vmDetail.noForwards')} />
                {/if}
              </div>
              <Button size="xs" variant="outline" class="mt-2" onclick={addForward}>
                {t('vmDetail.addForward')}
              </Button>
            </div>

            <!-- Inbound rules -->
            <div class="mb-4">
              <h3 class="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2">
                {t('vmDetail.inboundRules')}
              </h3>
              <div class="space-y-2">
                {#each fwRules as r (r.id)}
                  <div class="flex items-center gap-2 {r.disabled ? 'opacity-50' : ''}">
                    <span title={r.disabled ? t('firewall.enableRule') : t('firewall.disableRule')}>
                      <Switch
                        size="sm"
                        checked={!r.disabled}
                        onchange={() => (r.disabled = !r.disabled)}
                      />
                    </span>
                    <select class="input w-24" bind:value={r.proto}>
                      <option value="tcp">tcp</option>
                      <option value="udp">udp</option>
                      <option value="both">both</option>
                    </select>
                    <Input
                      type="number"
                      class="w-28 tnum"
                      bind:value={r.port}
                      min="1"
                      max="65535"
                      placeholder="Port"
                    />
                    <select class="input w-28" bind:value={r.action}>
                      <option value="allow">{t('vmDetail.fwAllow')}</option>
                      <option value="drop">{t('vmDetail.fwDrop')}</option>
                    </select>
                    <Button size="xs" variant="ghost" onclick={() => removeRule(r.id)}>×</Button>
                  </div>
                {/each}
                {#if fwRules.length === 0}
                  <EmptyState compact icon="shieldOff" title={t('vmDetail.noRules')} />
                {/if}
              </div>
              <Button size="xs" variant="outline" class="mt-2" onclick={addRule}>
                {t('vmDetail.addRule')}
              </Button>
            </div>

            <p class="text-xs text-muted-foreground mb-3">{t('vmDetail.firewallHint')}</p>
            <Button size="sm" onclick={saveFirewall} disabled={fwSaving}>
              {fwSaving ? t('common.saving') : t('common.save')}
            </Button>
          </BlockCard>
        {/snippet}

        {#snippet sec_schedule()}
          <BlockCard bid="schedule" title={t('vmDetail.scheduleTitle')} anchor="schedule">
            <div class="space-y-4 mb-4">
              <!-- Encendido Programado -->
              <div class="p-4 rounded-xl border border-border bg-card/40 space-y-3">
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-2">
                    <Icon name="play" size={14} class="text-success" />
                    <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
                      {t('vmDetail.scheduleStart')}
                    </span>
                  </div>
                  <Switch
                    size="sm"
                    checked={schedStart !== ''}
                    onchange={(v) => (schedStart = v ? '0 8 * * 1-5' : '')}
                  />
                </div>
                {#if !schedStart}
                  <p class="text-xs text-muted-foreground py-1">
                    {t('vmDetail.noActiveSchedule')}
                  </p>
                {:else}
                  <CronPicker bind:expression={schedStart} />
                {/if}
              </div>

              <!-- Apagado Programado -->
              <div class="p-4 rounded-xl border border-border bg-card/40 space-y-3">
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-2">
                    <Icon name="power" size={14} class="text-destructive" />
                    <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
                      {t('vmDetail.scheduleStop')}
                    </span>
                  </div>
                  <Switch
                    size="sm"
                    checked={schedStop !== ''}
                    onchange={(v) => (schedStop = v ? '0 20 * * 1-5' : '')}
                  />
                </div>
                {#if !schedStop}
                  <p class="text-xs text-muted-foreground py-1">
                    {t('vmDetail.noActiveSchedule')}
                  </p>
                {:else}
                  <CronPicker bind:expression={schedStop} />
                {/if}
              </div>

              <!-- Instantáneas Automáticas -->
              <div class="p-4 rounded-xl border border-border bg-card/40 space-y-3">
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-2">
                    <Icon name="camera" size={14} class="text-accent" />
                    <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
                      {t('vmDetail.snapScheduleTitle')}
                    </span>
                  </div>
                  <Switch
                    size="sm"
                    checked={schedSnap !== ''}
                    onchange={(v) => (schedSnap = v ? '0 2 * * *' : '')}
                  />
                </div>
                {#if !schedSnap}
                  <p class="text-xs text-muted-foreground py-1">
                    {t('vmDetail.noSnapshotsScheduled')}
                  </p>
                {:else}
                  <CronPicker bind:expression={schedSnap} />
                  {@render snapRetention()}
                {/if}
              </div>
            </div>

            <p class="text-xs text-muted-foreground mb-3">{t('vmDetail.scheduleHint')}</p>
            <Button size="sm" onclick={saveSchedule} disabled={schedSaving}>
              {#if schedSaving}<Spinner size="xs" color="text-white" />{:else}{t(
                  'common.save'
                )}{/if}
            </Button>
          </BlockCard>
        {/snippet}

        {#snippet sec_history()}
          <BlockCard bid="history" title={t('vmDetail.historyTitle')} anchor="history">
            <div class="flex items-center justify-between mb-3 gap-3 flex-wrap">
              <span class="text-xs text-muted-foreground">{t('vmDetail.historyDesc')}</span>
              <div class="flex items-center gap-1">
                {#each ['24h', '168h', '720h'] as w (w)}
                  <Button
                    size="xs"
                    variant={historyWindow === w ? 'default' : 'outline'}
                    onclick={() => {
                      historyWindow = w;
                      loadHistory();
                    }}
                    >{w === '24h'
                      ? t('vmDetail.hist24h')
                      : w === '168h'
                        ? t('vmDetail.hist7d')
                        : t('vmDetail.hist30d')}</Button
                  >
                {/each}
              </div>
            </div>
            {#if historyLoading}
              <div class="flex justify-center py-10"><Spinner size="lg" /></div>
            {:else if historyCpu.length === 0 && historyRam.length === 0}
              <p class="text-sm text-muted-foreground">{t('vmDetail.historyEmpty')}</p>
            {:else}
              <div class="grid grid-cols-1 md:grid-cols-2 gap-5">
                <div>
                  <div class="flex items-baseline justify-between mb-1.5">
                    <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >{t('vms.cpu')}</span
                    >
                    <span class="text-sm tnum"
                      >{historyCpu.length
                        ? historyCpu[historyCpu.length - 1].v.toFixed(1)
                        : '0.0'}%</span
                    >
                  </div>
                  <Chart points={historyCpu} yMax={100} height={80} />
                </div>
                <div>
                  <div class="flex items-baseline justify-between mb-1.5">
                    <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >{t('common.ram')}</span
                    >
                    <span class="text-sm tnum"
                      >{historyRam.length
                        ? historyRam[historyRam.length - 1].v.toFixed(1)
                        : '0.0'}%</span
                    >
                  </div>
                  <Chart
                    points={historyRam}
                    yMax={100}
                    height={80}
                    color="var(--info, var(--accent))"
                  />
                </div>
                <div>
                  <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
                    >{t('vmDetail.diskIo')}</span
                  >
                  <Chart
                    points={[...historyDiskR, ...historyDiskW]}
                    height={80}
                    color="var(--success)"
                  />
                </div>
                <div>
                  <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
                    >{t('vmDetail.netIo')}</span
                  >
                  <Chart
                    points={[...historyNetRx, ...historyNetTx]}
                    height={80}
                    color="var(--info, var(--accent))"
                  />
                </div>
              </div>
            {/if}
          </BlockCard>

          <BlockCard bid="hypervisor-logs" title={t('vmDetail.hypervisorLogs')}>
            {#snippet headerActions()}
              <div class="flex items-center gap-1.5">
                <select
                  bind:value={vmLogsLines}
                  onchange={loadVMLogs}
                  class="input !h-7 !text-xs !py-0 w-24"
                >
                  <option value={50}>50 líneas</option>
                  <option value={100}>100 líneas</option>
                  <option value={200}>200 líneas</option>
                  <option value={500}>500 líneas</option>
                  <option value={1000}>1000 líneas</option>
                </select>
                <Button
                  variant="outline"
                  size="sm"
                  class="!h-7 !text-xs"
                  onclick={loadVMLogs}
                  disabled={vmLogsLoading}
                >
                  <Icon
                    name="refresh"
                    size={12}
                    class={vmLogsLoading ? 'animate-spin mr-1' : 'mr-1'}
                  />
                  {t('common.refresh')}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  class="!h-7 !text-xs"
                  onclick={() => {
                    navigator.clipboard.writeText(vmLogs);
                    toast.success(t('common.copied'));
                  }}
                  disabled={!vmLogs || vmLogsLoading}
                >
                  <Icon name="copy" size={12} class="mr-1" />
                  {t('common.copy')}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  class="!h-7 !text-xs"
                  onclick={downloadVMLogs}
                  disabled={!vmLogs || vmLogsLoading}
                >
                  <Icon name="download" size={12} class="mr-1" />
                  {t('common.download')}
                </Button>
              </div>
            {/snippet}

            {#if vmLogsLoading}
              <div class="flex justify-center py-10"><Spinner size="lg" /></div>
            {:else if !vmLogs}
              <p class="text-sm text-muted-foreground">
                {t('vmDetail.noLogs')}
              </p>
            {:else}
              <div
                class="rounded-lg border border-border bg-slate-950 p-3.5 font-mono text-xs text-slate-200 overflow-x-auto max-h-96 overflow-y-auto select-text"
              >
                <pre class="whitespace-pre leading-relaxed">{vmLogs}</pre>
              </div>
            {/if}
          </BlockCard>
        {/snippet}

        {#snippet sec_alerts()}
          <BlockCard bid="alerts" title={t('vmDetail.alertsTitle')} anchor="alerts">
            <p class="text-xs text-muted-foreground mb-3">{t('vmDetail.alertsDesc')}</p>
            {#if alertsLoading}
              <div class="flex justify-center py-10"><Spinner size="lg" /></div>
            {:else}
              <div class="space-y-2 mb-4">
                {#each alertRules as rule (rule.id)}
                  <div
                    class="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-muted/20 p-2.5"
                  >
                    <select
                      class="h-8 rounded-lg border border-border bg-background px-1 text-sm w-24"
                      bind:value={rule.metric}
                    >
                      <option value="cpu">CPU</option>
                      <option value="ram">RAM</option>
                      <option value="disk_r">Disk R</option>
                      <option value="disk_w">Disk W</option>
                      <option value="net_rx">Net RX</option>
                      <option value="net_tx">Net TX</option>
                    </select>
                    <select
                      class="h-8 rounded-lg border border-border bg-background px-1 text-sm w-20"
                      bind:value={rule.above}
                    >
                      <option value={true}>{t('vmDetail.alertAbove')}</option>
                      <option value={false}>{t('vmDetail.alertBelow')}</option>
                    </select>
                    <Input
                      type="number"
                      class="h-8 w-24 tnum"
                      bind:value={rule.threshold}
                      min="0"
                    />
                    <span class="text-xs text-muted-foreground">{t('vmDetail.alertFor')}</span>
                    <Input
                      type="number"
                      class="h-8 w-20 tnum"
                      bind:value={rule.duration_secs}
                      min="1"
                    />
                    <span class="text-xs text-muted-foreground">{t('vmDetail.alertSecs')}</span>
                    {#if ruleStatus(rule.id)}
                      <span
                        class="text-xs px-2 py-0.5 rounded-md {ruleStatus(rule.id) === 'firing'
                          ? 'bg-destructive/15 text-destructive'
                          : ruleStatus(rule.id) === 'pending'
                            ? 'bg-warning/15 text-warning'
                            : 'bg-muted/50 text-muted-foreground'}"
                        >{t('vmDetail.alertState' + ruleStatus(rule.id))}</span
                      >
                    {/if}
                    <Button
                      size="icon"
                      variant="ghost"
                      class="text-destructive"
                      onclick={() => (alertRules = alertRules.filter((x) => x.id !== rule.id))}
                      aria-label={t('common.delete')}>×</Button
                    >
                  </div>
                {/each}
                {#if alertRules.length === 0}
                  <EmptyState compact icon="bellOff" title={t('vmDetail.alertsEmpty')} />
                {/if}
              </div>
              <div class="flex items-center gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  onclick={() => (alertRules = [...alertRules, newAlertRule()])}
                >
                  {t('vmDetail.addAlertRule')}
                </Button>
                <Button size="sm" onclick={saveAlertRules} disabled={alertsLoading}>
                  {t('common.save')}
                </Button>
              </div>
            {/if}
          </BlockCard>
        {/snippet}

        {#snippet sec_serial()}
          <div id="vm-serial-block">
            <BlockCard bid="serial" title={t('vmDetail.serialConsoleTitle')}>
              <TerminalPanel mode="vm" {vmId} />
            </BlockCard>
          </div>
        {/snippet}

        <Tabs tabs={sectionTabs} bind:active={activeSection} class="mb-1" />

        <!-- Each panel stays mounted (CSS-hidden, not {#if}-removed) so
             switching tabs never tears down the serial WebSocket or a
             VNC connection living inside one of these cards. -->
        <div class="space-y-5 {activeSection === 'overview' ? '' : 'hidden'}">
          {@render sec_overview()}
          {@render sec_metrics()}
          {@render sec_serial()}
          {@render sec_firewall()}
          {@render sec_schedule()}
        </div>
        <div class={activeSection === 'hardware' ? '' : 'hidden'}>
          {@render sec_hardware()}
        </div>
        {#if !isContainerVm}
          <div class={activeSection === 'cloudinit' ? '' : 'hidden'}>
            {@render sec_cloudinit()}
          </div>
        {/if}
        <div class={activeSection === 'disks' ? '' : 'hidden'}>
          {@render sec_disks()}
          {#if auth.isAdmin() && !isContainerVm}
            {@render sec_shared_folders()}
          {/if}
        </div>
        <div class={activeSection === 'net' ? '' : 'hidden'}>
          {@render sec_net()}
          {#if auth.isAdmin()}
            {@render sec_usb()}
            {#if !isContainerVm}
              {@render sec_pci()}
            {/if}
          {/if}
        </div>
        {#if !isContainerVm}
          <div class={activeSection === 'guest' ? '' : 'hidden'}>
            {@render sec_guest()}
          </div>
        {/if}
        <div class={activeSection === 'snaps' ? '' : 'hidden'}>
          {@render sec_snaps()}
        </div>
        <div class={activeSection === 'history' ? '' : 'hidden'}>
          {@render sec_history()}
        </div>
        <div class={activeSection === 'alerts' ? '' : 'hidden'}>
          {@render sec_alerts()}
        </div>
      </div>

      <!-- Sidebar -->
      <div class="space-y-4">
        {#if auth.canMutate()}
          <Card class="p-4">
            <h2 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-3">
              {t('vmDetail.power')}
            </h2>
            <div class="space-y-2">
              {#key vm.state}
                <div transition:fade={{ duration: 150 }}>
                  {#if vm.state === 'shutoff' && vmMeta?.template}
                    <p class="text-xs text-muted-foreground text-center py-2">
                      {t('vmDetail.templateStartBlocked')}
                    </p>
                  {:else if vm.state === 'shutoff'}
                    <Button
                      onclick={() => doAction('startVM')}
                      disabled={busy}
                      class="w-full {actionLoading === 'startVM' ? 'opacity-100!' : ''}"
                    >
                      {#if actionLoading === 'startVM'}<Spinner
                          size="sm"
                          color="text-white"
                        />{:else}<Play class="w-4 h-4 mr-1.5" />{t('vmDetail.start')}{/if}
                    </Button>
                  {:else if vm.state === 'running'}
                    <Button
                      variant="outline"
                      onclick={() => doAction('shutdownVM')}
                      disabled={busy}
                      class="w-full {actionLoading === 'shutdownVM' ? 'opacity-100!' : ''}"
                    >
                      {#if actionLoading === 'shutdownVM'}<Spinner
                          size="sm"
                          color="text-white"
                        />{:else}<PowerOff class="w-4 h-4 mr-1.5" />{t('vmDetail.shutdown')}{/if}
                    </Button>
                    <Button
                      variant="destructive"
                      onclick={() => doAction('forceOffVM')}
                      disabled={busy}
                      class="w-full {actionLoading === 'forceOffVM' ? 'opacity-100!' : ''}"
                    >
                      {#if actionLoading === 'forceOffVM'}<Spinner
                          size="sm"
                          color="text-white"
                        />{:else}<PowerIcon class="w-4 h-4 mr-1.5" />{t('vmDetail.forceOff')}{/if}
                    </Button>
                    <Button
                      variant="destructive"
                      onclick={() => doAction('forceRebootVM')}
                      disabled={busy}
                      class="w-full {actionLoading === 'forceRebootVM' ? 'opacity-100!' : ''}"
                    >
                      {#if actionLoading === 'forceRebootVM'}<Spinner
                          size="sm"
                          color="text-white"
                        />{:else}<RotateCw class="w-4 h-4 mr-1.5" />{t('vmDetail.forceReboot')}{/if}
                    </Button>
                    <Button
                      variant="outline"
                      onclick={() => doAction('suspendVM')}
                      disabled={busy}
                      class="w-full {actionLoading === 'suspendVM' ? 'opacity-100!' : ''}"
                    >
                      {#if actionLoading === 'suspendVM'}<Spinner
                          size="sm"
                          color="text-white"
                        />{:else}<Pause class="w-4 h-4 mr-1.5" />{t('vmDetail.suspend')}{/if}
                    </Button>
                  {:else if vm.state === 'paused'}
                    <Button
                      onclick={() => doAction('resumeVM')}
                      disabled={busy}
                      class="w-full {actionLoading === 'resumeVM' ? 'opacity-100!' : ''}"
                    >
                      {#if actionLoading === 'resumeVM'}<Spinner
                          size="sm"
                          color="text-white"
                        />{:else}<PlayCircle class="w-4 h-4 mr-1.5" />{t('vmDetail.resume')}{/if}
                    </Button>
                  {/if}
                </div>
              {/key}
            </div>
          </Card>
        {/if}

        <Card class="p-4">
          <h2 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-3">
            {t('vmDetail.actions')}
          </h2>
          <div class="space-y-2">
            {#if !isContainerVm}
              <Button onclick={openConsole} class="w-full">
                <Terminal class="w-4 h-4 mr-1.5" />
                {t('vmDetail.openConsole')}
              </Button>
            {/if}
            <Button variant="outline" onclick={gotoSerial} class="w-full">
              <Terminal class="w-4 h-4 mr-1.5" />
              {t('vmDetail.serialConsole')}
            </Button>
            {#if appInfo}
              <Button variant="outline" onclick={showAppCredentials} class="w-full">
                <KeyRound class="w-4 h-4 mr-1.5" />
                {t('vmDetail.appCredentials')}
              </Button>
            {/if}
            {#if auth.isAdmin() && !isContainerVm}
              <Button
                variant="outline"
                onclick={resetPassword}
                disabled={busy || (vm && vm.state !== 'running')}
                title={vm && vm.state !== 'running' ? t('vmDetail.resetPasswordHint') : ''}
                class="w-full"
              >
                {t('vmDetail.resetPassword')}
              </Button>
            {/if}
            {#if auth.canMutate()}
              <Button
                variant="destructive"
                onclick={() => doAction('deleteVM')}
                disabled={busy}
                class="w-full"
              >
                <Trash2 class="w-4 h-4 mr-1.5" />
                {t('vmDetail.deleteVM')}
              </Button>
              {#if !isContainerVm}
                <Button
                  variant="outline"
                  onclick={() => {
                    cName = vm.name + '-clone';
                    cPool = vmDiskPools(pools)[0]?.name || 'webkvm-disks';
                    showClone = true;
                  }}
                  class="w-full"
                >
                  <CopyPlus class="w-4 h-4 mr-1.5" />
                  {t('vmDetail.cloneVM')}
                </Button>
              {/if}
              {#if movePoolOptions.length > 0}
                <!-- Hidden outright when there is nowhere to move to:
                     a button that can only ever fail is worse than no
                     button. -->
                <Button
                  variant="outline"
                  onclick={() => {
                    movePool = movePoolOptions[0]?.name || '';
                    showMove = true;
                  }}
                  class="w-full"
                >
                  <Icon name="hard-drive" size={16} class="mr-1.5" />
                  {t('vmDetail.moveStorage')}
                </Button>
              {/if}
              <Button variant="outline" onclick={openEdit} class="w-full">
                <Pencil class="w-4 h-4 mr-1.5" />
                {t('vmDetail.editSettings')}
              </Button>
              <Button variant="outline" onclick={openIdentity} class="w-full">
                <Info class="w-4 h-4 mr-1.5" />
                {t('vmDetail.identityNotes')}
              </Button>
              <Button
                variant="outline"
                onclick={exportVM}
                disabled={actionLoading === 'export'}
                class="w-full"
              >
                {#if actionLoading === 'export'}<Spinner size="sm" color="text-white" />{t(
                    'vmDetail.exportingShort'
                  )}{:else}<Download class="w-4 h-4 mr-1.5" />{t('vmDetail.exportBackup')}{/if}
              </Button>
            {/if}
            {#if !isContainerVm}
              <div class="grid grid-cols-2 gap-2">
                <Button
                  variant="outline"
                  class="w-full justify-center"
                  onclick={() => downloadConsoleFile('rdp')}
                >
                  RDP
                </Button>
                <Button
                  variant="outline"
                  class="w-full justify-center"
                  onclick={() => downloadConsoleFile('spice')}
                  disabled={capabilities.loaded &&
                    capabilities.parsed &&
                    !capabilities.spiceSupported}
                  title={capabilities.loaded && capabilities.parsed && !capabilities.spiceSupported
                    ? 'SPICE is not available on this host (QEMU has no spice module)'
                    : ''}
                >
                  SPICE
                </Button>
              </div>
            {/if}
            <!-- Autostart toggle: lives at the bottom of the
						     Actions card so it doesn't compete with the
						     primary action (Open Console). The Switch's
						     `checked` is bound to vm.autostart; onchange
						     fires toggleAutostart() which PATCHes the
						     server and rolls back on failure. -->
            <div class="pt-3 mt-2 border-t border-border">
              <Switch
                checked={!!vm.autostart}
                disabled={autostartSaving}
                onchange={toggleAutostart}
                label={t('vmDetail.autostart')}
                description={autostartSaving
                  ? t('vmDetail.notesSaving')
                  : vm.autostart
                    ? t('vmDetail.autostartDescOn')
                    : t('vmDetail.autostartDescOff')}
              />
            </div>
          </div>
        </Card>
      </div>
    </div>
  {/if}
</div>

<PasswordModal
  bind:open={showPasswordModal}
  username={resetUsername}
  password={resetPasswordValue}
  title={t('vmDetail.passwordResetTitle')}
/>

<CredentialsModal bind:open={showAppCreds} info={appCreds} />

<!-- Reset password error: guest agent not available or reset failed -->
<Dialog.Root bind:open={showResetError}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vmDetail.passwordResetFailedTitle')}</Dialog.Title>
      <Dialog.Description>
        {t('vmDetail.passwordResetFailedDesc')}
      </Dialog.Description>
    </Dialog.Header>
    {#if resetError}
      <p class="text-sm text-muted-foreground break-words">{resetError}</p>
    {/if}
    <p class="text-xs text-muted-foreground mt-2">
      {t('vmDetail.passwordResetGuestAgentHint')}
      <code class="block mt-1 p-2 rounded bg-muted font-mono text-xs"
        >sudo apt install qemu-guest-agent && sudo systemctl enable --now qemu-guest-agent</code
      >
    </p>
    <Dialog.Footer class="gap-2">
      <Button onclick={() => (showResetError = false)}>OK</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Pop-up: action blocked because the VM isn't shut off -->
<Dialog.Root bind:open={showBlocked}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vmDetail.requireShutoffTitle')}</Dialog.Title>
      <Dialog.Description>
        {t('vmDetail.blockedDialogDesc')}
      </Dialog.Description>
    </Dialog.Header>
    {#if blockedNotice}
      <p class="text-sm text-muted-foreground">{blockedNotice}</p>
    {/if}
    <Dialog.Footer class="gap-2">
      <Button onclick={() => (showBlocked = false)}>OK</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Shared confirm dialog (replaces all window.confirm) -->
<ConfirmDialog
  bind:open={confirmState.open}
  title={confirmState.title}
  description={confirmState.description}
  confirmLabel={confirmState.confirmLabel}
  variant={confirmState.variant}
  loading={confirmState.loading}
  onConfirm={confirmState.onConfirm}
/>

<DeleteVmDialog
  bind:open={showDeleteFlow}
  vmId={vm?.id}
  vmName={vm?.name}
  {vmDisks}
  active={vm?.state === 'running' || vm?.state === 'paused'}
  isContainer={isContainerVm}
  {onDeleteStart}
  {onDeleteFailed}
  onDeleted={onVmDeleted}
/>

<!-- Add Disk Dialog -->
<Dialog.Root bind:open={showAddDisk}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title
        >{aDiskDevice === 'cdrom'
          ? t('vmDetail.attachIso')
          : aDiskDevice === 'existing'
            ? t('vmDetail.attachExistingDisk')
            : aDiskDevice === 'zvol'
              ? t('vmDetail.attachZVol')
              : t('vmDetail.addDisk')}</Dialog.Title
      >
    </Dialog.Header>
    <div class="space-y-3">
      <div>
        <label for="adisk-type" class="block text-sm font-medium mb-1.5">{t('common.type')}</label>
        <select
          id="adisk-type"
          bind:value={aDiskDevice}
          onchange={() => {
            aDiskBus = aDiskDevice === 'cdrom' ? 'scsi' : 'virtio';
            if (aDiskDevice === 'existing') loadDiskVolumesForPool();
            if (aDiskDevice === 'zvol') loadHostZVols();
          }}
          class="input"
        >
          <option value="disk">{t('vmDetail.diskType')}</option>
          <option value="existing">{t('vmDetail.existingDisk')}</option>
          {#if auth.isAdmin() && !isContainerVm}
            <option value="zvol">{t('vmDetail.zvolType')}</option>
          {/if}
          <option value="cdrom">{t('vmDetail.cdromIso')}</option>
        </select>
      </div>
      <div>
        <label for="adisk-bus" class="block text-sm font-medium mb-1.5"
          >{t('vmDetail.busLabel')}</label
        >
        <select id="adisk-bus" bind:value={aDiskBus} class="input">
          {#if aDiskDevice === 'cdrom'}
            <option value="scsi">{t('vmDetail.scsiRecommended')}</option>
            <option value="sata">SATA</option>
          {:else}
            <option value="virtio">{t('vmDetail.virtioRecommended')}</option>
            <option value="sata">SATA</option>
            <option value="scsi">SCSI</option>
            <!-- IDE has no controller at all on Q35 (every VM here
                 unless created with the legacy i440fx chipset) —
                 offering it there just guarantees a failed attach. -->
            {#if vm?.chipset === 'i440fx'}
              <option value="ide">IDE</option>
            {/if}
          {/if}
        </select>
      </div>
      {#if aDiskDevice === 'disk' || aDiskDevice === 'existing'}
        <div>
          <label for="adisk-pool" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.storagePool')}</label
          >
          <select
            id="adisk-pool"
            bind:value={aDiskPool}
            onchange={() => {
              if (aDiskDevice === 'existing') loadDiskVolumesForPool();
            }}
            class="input"
          >
            {#each vmDiskPools(pools) as p (p.name)}<option value={p.name}>{p.name}</option>{/each}
          </select>
        </div>
      {/if}
      {#if aDiskDevice === 'disk'}
        <div>
          <label for="adisk-size" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.sizeGb')}</label
          >
          <Input id="adisk-size" type="number" min="1" bind:value={aDiskSize} class="tnum" />
        </div>
        <div>
          <label for="adisk-fmt" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.format')}</label
          >
          <select id="adisk-fmt" bind:value={aDiskFormat} class="input">
            <option value="qcow2">qcow2</option>
            <option value="raw">raw</option>
          </select>
        </div>
      {:else if aDiskDevice === 'existing'}
        <div>
          <label for="adisk-existing" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.existingDisk')}</label
          >
          <select
            id="adisk-existing"
            bind:value={aDiskExistingVol}
            onchange={probeExistingDisk}
            class="input"
          >
            <option value="">{t('vmDetail.empty')}</option>
            {#each aDiskVolumes.filter((v) => !v.is_snapshot) as v (v.id || v.name)}
              <option value={v.path}>{v.name} ({bytesToStr(v.capacity)})</option>
            {/each}
          </select>
        </div>
        {#if aDiskProbing}
          <p class="text-xs text-muted-foreground">{t('vmDetail.diskProbing')}</p>
        {:else if aDiskProbe?.has_data}
          <div class="rounded-lg border border-warning/40 bg-warning/5 p-2.5 space-y-2">
            <p class="text-[11px] text-warning flex items-start gap-1.5">
              <Icon name="alertTriangle" size={12} class="mt-0.5 shrink-0" />
              <span>{t('vmDetail.diskHasDataDetail', { format: aDiskProbe.format })}</span>
            </p>
            <label class="flex items-center gap-2 text-xs cursor-pointer">
              <Checkbox bind:checked={aDiskForce} />
              <span>{t('vmDetail.diskForceAttach')}</span>
            </label>
          </div>
        {/if}
      {:else if aDiskDevice === 'zvol'}
        <div>
          <label for="adisk-zvol" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.zvolLabel')}</label
          >
          {#if aDiskZVolCustom}
            <div class="space-y-2">
              <Input
                id="adisk-zvol-input"
                type="text"
                placeholder={t('vmDetail.zvolPlaceholder')}
                bind:value={aDiskZVol}
                onblur={probeZVolDisk}
              />
              <button
                type="button"
                class="text-xs text-accent hover:underline"
                onclick={() => {
                  aDiskZVolCustom = false;
                  aDiskZVol = '';
                  aDiskProbe = null;
                }}
              >
                ← {t('vmDetail.zvolSelectPlaceholder')}
              </button>
            </div>
          {:else}
            <select
              id="adisk-zvol"
              bind:value={aDiskZVol}
              disabled={aDiskZVolsLoading}
              onchange={(e) => {
                if (e.target.value === '__custom__') {
                  aDiskZVolCustom = true;
                  aDiskZVol = '';
                  aDiskProbe = null;
                } else {
                  probeZVolDisk();
                }
              }}
              class="input"
            >
              <option value="">{t('vmDetail.zvolSelectPlaceholder')}</option>
              {#each aDiskZVols as z (z.name)}
                <option value={z.name} disabled={!!z.used_by && z.used_by.vm_id !== vmId}>
                  {z.name} ({bytesToStr(z.volsize)})
                  {#if z.used_by}
                    - {t('vmDetail.zvolInUseWarning', {
                      vm: z.used_by.vm_name,
                      target: z.used_by.target,
                    })}
                  {/if}
                </option>
              {/each}
              <option value="__custom__">➕ {t('vmDetail.zvolCustomOption')}</option>
            </select>
          {/if}
          <p class="text-[11px] text-muted-foreground mt-1">{t('vmDetail.zvolHint')}</p>
        </div>
        {#if aDiskProbing}
          <p class="text-xs text-muted-foreground">{t('vmDetail.diskProbing')}</p>
        {:else if aDiskProbe?.has_data}
          <div class="rounded-lg border border-warning/40 bg-warning/5 p-2.5 space-y-2">
            <p class="text-[11px] text-warning flex items-start gap-1.5">
              <Icon name="alertTriangle" size={12} class="mt-0.5 shrink-0" />
              <span>{t('vmDetail.diskHasDataDetail', { format: aDiskProbe.format })}</span>
            </p>
            <label class="flex items-center gap-2 text-xs cursor-pointer">
              <Checkbox bind:checked={aDiskForce} />
              <span>{t('vmDetail.diskForceAttach')}</span>
            </label>
          </div>
        {/if}
      {:else}
        <div>
          <label for="adisk-iso" class="block text-sm font-medium mb-1.5">{t('vmDetail.iso')}</label
          >
          <select id="adisk-iso" bind:value={aDiskISO} class="input">
            <option value="">{t('vmDetail.empty')}</option>
            {#each isos as iso (iso.path || iso.name)}<option value={iso.path}>{iso.name}</option
              >{/each}
          </select>
        </div>
      {/if}
      {#if aDiskDevice === 'disk' || aDiskDevice === 'existing' || aDiskDevice === 'zvol'}
        <div class="border-t border-border pt-3 space-y-3">
          <div>
            <label for="adisk-serial" class="block text-xs font-medium mb-1"
              >{t('vmDetail.diskSerialLabel')}</label
            >
            <Input
              id="adisk-serial"
              type="text"
              placeholder={t('vmDetail.diskSerialPlaceholder')}
              bind:value={aDiskSerial}
            />
          </div>
          <div>
            <label for="adisk-wwn" class="block text-xs font-medium mb-1"
              >{t('vmDetail.diskWwnLabel')}</label
            >
            <Input
              id="adisk-wwn"
              type="text"
              placeholder={t('vmDetail.diskWwnPlaceholder')}
              bind:value={aDiskWWN}
            />
          </div>
          <div>
            <label for="adisk-alias" class="block text-xs font-medium mb-1"
              >{t('vmDetail.diskAliasLabel')}</label
            >
            <Input
              id="adisk-alias"
              type="text"
              placeholder={t('vmDetail.diskAliasPlaceholder')}
              bind:value={aDiskAlias}
            />
          </div>
        </div>
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button
        variant="outline"
        onclick={() => (showAddDisk = false)}
        disabled={actionLoading === 'adddisk'}>{t('common.cancel')}</Button
      >
      <Button onclick={addDisk} disabled={actionLoading === 'adddisk'}>
        {#if actionLoading === 'adddisk'}<Spinner size="sm" color="text-white" />{:else}{t(
            'common.add'
          )}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Change ISO Dialog -->
<Dialog.Root bind:open={showChangeISO}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vmDetail.changeIsoTitle', { target: cISOTarget })}</Dialog.Title>
    </Dialog.Header>
    <div>
      <label for="ciso-src" class="block text-sm font-medium mb-1.5">{t('vmDetail.iso')}</label>
      <select id="ciso-src" bind:value={cISOSource} class="input">
        <option value="">{t('vmDetail.ejectNoIso')}</option>
        {#each isos as iso (iso.path || iso.name)}<option value={iso.path}>{iso.name}</option
          >{/each}
      </select>
    </div>
    <Dialog.Footer class="gap-2">
      <Button
        variant="outline"
        onclick={() => (showChangeISO = false)}
        disabled={actionLoading === 'changeiso'}>{t('common.cancel')}</Button
      >
      <Button onclick={changeISO} disabled={actionLoading === 'changeiso'}>
        {#if actionLoading === 'changeiso'}<Spinner size="sm" color="text-white" />{:else}{t(
            'vmDetail.changeIso'
          )}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Resize Disk Dialog -->
<Dialog.Root bind:open={showResizeDisk}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vmDetail.resizeDiskTitle', { target: resizeDiskTarget })}</Dialog.Title>
      <Dialog.Description
        >{t('vmDetail.resizeDiskDesc', { current: resizeDiskCurrent })}</Dialog.Description
      >
      {#if vm && vm.state !== 'shutoff'}
        <p class="text-xs text-muted-foreground mt-1">{t('vmDetail.resizeDiskLiveNotice')}</p>
      {/if}
    </Dialog.Header>
    <div>
      <label for="rdisk-size" class="block text-sm font-medium mb-1.5"
        >{t('vmDetail.newSizeGb')}</label
      >
      <Input
        id="rdisk-size"
        type="number"
        min={resizeDiskCurrent}
        bind:value={resizeDiskSize}
        class="tnum"
      />
    </div>
    <Dialog.Footer class="gap-2">
      <Button
        variant="outline"
        onclick={() => (showResizeDisk = false)}
        disabled={actionLoading === 'resizedisk'}>{t('common.cancel')}</Button
      >
      <Button onclick={resizeDisk} disabled={actionLoading === 'resizedisk' || !resizeDiskSize}>
        {#if actionLoading === 'resizedisk'}<Spinner size="sm" color="text-white" />{:else}{t(
            'vmDetail.resize'
          )}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Change Disk Bus Dialog -->
<Dialog.Root bind:open={showChangeBus}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vmDetail.changeBusTitle', { target: changeBusTarget })}</Dialog.Title>
      <Dialog.Description
        >{t('vmDetail.changeBusDesc', { current: changeBusCurrent })}</Dialog.Description
      >
    </Dialog.Header>
    <div>
      <label for="cbus-new" class="block text-sm font-medium mb-1.5">{t('vmDetail.busLabel')}</label
      >
      <select id="cbus-new" bind:value={changeBusNew} class="input">
        <option value="virtio">{t('vmDetail.virtioRecommended')}</option>
        <option value="sata">SATA</option>
        <option value="scsi">SCSI</option>
        <!-- IDE has no controller at all on Q35 (every VM here unless
             created with the legacy i440fx chipset) — offering it
             there just guarantees a failed attach. -->
        {#if vm?.chipset === 'i440fx'}
          <option value="ide">IDE</option>
        {/if}
      </select>
    </div>
    <Dialog.Footer class="gap-2">
      <Button
        variant="outline"
        onclick={() => (showChangeBus = false)}
        disabled={actionLoading === 'changebus'}>{t('common.cancel')}</Button
      >
      <Button
        onclick={changeDiskBus}
        disabled={actionLoading === 'changebus' || changeBusNew === changeBusCurrent}
      >
        {#if actionLoading === 'changebus'}<Spinner size="sm" color="text-white" />{:else}{t(
            'vmDetail.changeBus'
          )}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Add Net Dialog -->
<Dialog.Root bind:open={showAddNet}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vmDetail.addNetTitle')}</Dialog.Title>
    </Dialog.Header>
    <div class="space-y-3">
      <div>
        <label for="anet-net" class="block text-sm font-medium mb-1.5"
          >{t('vmDetail.networkLabel')}</label
        >
        <select id="anet-net" bind:value={aNetNetwork} class="input">
          {#each vmNetworks as net (net.name)}<option value={net.name}>{networkLabel(net)}</option
            >{/each}
        </select>
      </div>
      {#if !isContainerVm}
        <div>
          <label for="anet-model" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.model')}</label
          >
          <select id="anet-model" bind:value={aNetModel} class="input">
            {#each networkModels as m (m)}
              <option value={m.value} disabled={!supports('network', m.value)}>{m.label}</option>
            {/each}
          </select>
        </div>
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button
        variant="outline"
        onclick={() => (showAddNet = false)}
        disabled={actionLoading === 'addnet'}>{t('common.cancel')}</Button
      >
      <Button onclick={addNet} disabled={actionLoading === 'addnet'}>
        {#if actionLoading === 'addnet'}<Spinner size="sm" color="text-white" />{:else}{t(
            'common.add'
          )}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Clone Dialog -->
<Dialog.Root bind:open={showClone}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vmDetail.cloneTitle')}</Dialog.Title>
      <Dialog.Description>{t('vmDetail.cloneDesc')}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      {#if vm?.state !== 'shutoff'}
        <div
          class="flex items-start gap-2 p-2.5 rounded-lg border border-warning/30 bg-warning-subtle text-warning text-xs"
        >
          <Icon name="alert-triangle" size={14} class="shrink-0 mt-0.5" />
          <span>{t('vmDetail.cloneRequiresShutoff')}</span>
        </div>
      {/if}
      <div>
        <label for="clone-name" class="block text-sm font-medium mb-1.5"
          >{t('vmDetail.newName')}</label
        >
        <Input id="clone-name" bind:value={cName} type="text" />
      </div>
      <div>
        <label for="clone-pool" class="block text-sm font-medium mb-1.5"
          >{t('vmDetail.storagePool')}</label
        >
        <select id="clone-pool" bind:value={cPool} class="input">
          {#each vmDiskPools(pools) as p (p.name)}<option value={p.name}>{p.name}</option>{/each}
        </select>
      </div>
      <div class="rounded-lg border border-border p-3 space-y-2">
        <div class="flex items-center justify-between gap-2">
          <span class="text-sm font-medium">{t('vmDetail.linkedClone')}</span>
          <Switch bind:checked={cLinked} ariaLabel={t('vmDetail.linkedClone')} />
        </div>
        <p class="text-xs text-muted-foreground">
          {cLinked ? t('vmDetail.linkedCloneHelperOn') : t('vmDetail.linkedCloneHelperOff')}
        </p>
      </div>
    </div>
    <Dialog.Footer class="gap-2">
      <Button
        variant="outline"
        onclick={() => (showClone = false)}
        disabled={actionLoading === 'clone'}>{t('common.cancel')}</Button
      >
      <Button onclick={cloneVM} disabled={actionLoading === 'clone' || !cName}>
        {#if actionLoading === 'clone'}<Spinner size="sm" color="text-white" />{:else}{t(
            'vmDetail.clone'
          )}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Move storage to another pool. Not a clone: the same instance ends
     up with its disks somewhere else, and the originals are removed
     once the copy is complete and the VM points at it. -->
<Dialog.Root bind:open={showMove}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vmDetail.moveStorageTitle')}</Dialog.Title>
      <Dialog.Description>{t('vmDetail.moveStorageDesc')}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      {#if vm?.state !== 'shutoff'}
        <div
          class="flex items-start gap-2 p-2.5 rounded-lg border border-warning/30 bg-warning-subtle text-warning text-xs"
        >
          <Icon name="alert-triangle" size={14} class="shrink-0 mt-0.5" />
          <span>{t('vmDetail.moveRequiresShutoff')}</span>
        </div>
      {/if}
      {#if currentPool}
        <div class="text-xs text-muted-foreground">
          {t('vmDetail.moveCurrentPool', { pool: currentPool })}
        </div>
      {/if}
      <div>
        <label for="move-pool" class="block text-sm font-medium mb-1.5"
          >{t('vmDetail.moveDestPool')}</label
        >
        <select id="move-pool" bind:value={movePool} class="input" disabled={!!moveProgress}>
          {#each movePoolOptions as p (p.name)}<option value={p.name}>{p.name}</option>{/each}
        </select>
      </div>
      {#if moveProgress}
        <div class="space-y-1.5">
          <div class="flex items-center justify-between text-xs">
            <span class="text-muted-foreground">{moveStageLabel(moveProgress.stage)}</span>
            <span class="tnum">{Math.round(moveProgress.pct)}%</span>
          </div>
          <div class="h-1.5 rounded-full bg-muted overflow-hidden">
            <div
              class="h-full bg-accent transition-[width] duration-300"
              style="width: {Math.max(2, Math.min(100, moveProgress.pct))}%"
            ></div>
          </div>
        </div>
      {:else}
        <p class="text-xs text-muted-foreground">{t('vmDetail.moveStorageHint')}</p>
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button
        variant="outline"
        onclick={() => (showMove = false)}
        disabled={actionLoading === 'move'}>{t('common.cancel')}</Button
      >
      <Button onclick={moveStorage} disabled={actionLoading === 'move' || !movePool}>
        {#if actionLoading === 'move'}<Spinner size="sm" color="text-white" />{:else}{t(
            'vmDetail.moveStorageConfirm'
          )}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Export Dialog -->
<Dialog.Root bind:open={showExport}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title
        >{isContainerVm
          ? t('vmDetail.exportContainerTitle')
          : t('vmDetail.exportTitle')}</Dialog.Title
      >
    </Dialog.Header>
    {#if !exportProgress}
      <p class="text-sm text-muted-foreground">
        {isContainerVm ? t('vmDetail.exportContainerDesc') : t('vmDetail.exportDesc')}
      </p>
      <div class="space-y-2 mt-2">
        {#if !isContainerVm}
          <label
            class="flex items-start gap-3 p-3 rounded border border-border cursor-pointer hover:border-border-hover"
          >
            <input type="radio" bind:group={exportTarget} value="vmware" class="mt-1" />
            <div>
              <div class="text-sm font-medium">{t('vmDetail.exportVmware')}</div>
              <div class="text-xs text-muted-foreground">{t('vmDetail.exportVmwareDesc')}</div>
            </div>
          </label>
          <label
            class="flex items-start gap-3 p-3 rounded border border-border cursor-pointer hover:border-border-hover"
          >
            <input type="radio" bind:group={exportTarget} value="libvirt" class="mt-1" />
            <div>
              <div class="text-sm font-medium">{t('vmDetail.exportLibvirt')}</div>
              <div class="text-xs text-muted-foreground">{t('vmDetail.exportLibvirtDesc')}</div>
            </div>
          </label>
          <label
            class="flex items-start gap-3 p-3 rounded border border-border cursor-pointer hover:border-border-hover"
          >
            <input type="radio" bind:group={exportTarget} value="backup" class="mt-1" />
            <div>
              <div class="text-sm font-medium">{t('vmDetail.exportBackupLabel')}</div>
              <div class="text-xs text-muted-foreground">{t('vmDetail.exportBackupDesc')}</div>
            </div>
          </label>
        {:else}
          <label
            class="flex items-start gap-3 p-3 rounded border border-border cursor-pointer hover:border-border-hover"
          >
            <input type="radio" bind:group={exportTarget} value="backup" class="mt-1" />
            <div>
              <div class="text-sm font-medium">{t('vmDetail.exportIncusBackupLabel')}</div>
              <div class="text-xs text-muted-foreground">{t('vmDetail.exportIncusBackupDesc')}</div>
            </div>
          </label>
          <label
            class="flex items-start gap-3 p-3 rounded border border-border cursor-pointer hover:border-border-hover"
          >
            <input type="radio" bind:group={exportTarget} value="proxmox" class="mt-1" />
            <div>
              <div class="text-sm font-medium">{t('vmDetail.exportProxmoxLabel')}</div>
              <div class="text-xs text-muted-foreground">{t('vmDetail.exportProxmoxDesc')}</div>
            </div>
          </label>
        {/if}
      </div>
      <Dialog.Footer class="gap-2">
        <Button variant="outline" onclick={() => (showExport = false)}>{t('common.cancel')}</Button>
        <Button onclick={startExport}>{t('vmDetail.export')}</Button>
      </Dialog.Footer>
    {:else}
      <div class="space-y-2">
        <ProgressBar
          value={exportProgress.total > 0 ? exportProgress.percent : undefined}
          label={exportProgress.label}
          showValue
          size="md"
        />
        <div class="flex justify-between text-xs text-muted-foreground tnum">
          <span
            >{(exportProgress.received / 1e9).toFixed(2)} GB
            {exportProgress.total > 0
              ? `/ ${(exportProgress.total / 1e9).toFixed(2)} GB`
              : ''}</span
          >
        </div>
      </div>
      <Dialog.Footer>
        <Button variant="outline" onclick={cancelExport}>{t('common.cancel')}</Button>
      </Dialog.Footer>
    {/if}
  </Dialog.Content>
</Dialog.Root>

<!-- Identity & Notes Dialog -->
<Dialog.Root bind:open={showIdentity}>
  <Dialog.Content class="sm:max-w-2xl max-h-[90vh] overflow-y-auto">
    <Dialog.Header>
      <Dialog.Title>{t('vmDetail.identityTitle')}</Dialog.Title>
      <Dialog.Description>{t('vmDetail.identityDesc')}</Dialog.Description>
    </Dialog.Header>

    <div class="flex gap-1 border-b border-border mb-4 overflow-x-auto">
      {#each [['alias', t('vmDetail.tabAlias')], ['cover', t('vmDetail.tabCover')], ['network', t('vmDetail.tabNetwork')], ['notes', t('vmDetail.tabNotes')], ['groups', t('vmDetail.tabGroups')]] as [k, label], __i (__i)}
        <button
          onclick={() => {
            identityTab = k;
            if (k === 'cover') loadMediaLibrary();
          }}
          class="px-3 py-2 text-sm border-b-2 -mb-px transition-colors whitespace-nowrap {identityTab ===
          k
            ? 'border-accent text-foreground'
            : 'border-transparent text-muted-foreground hover:text-foreground'}">{label}</button
        >
      {/each}
    </div>

    {#if identityTab === 'alias'}
      <div class="space-y-3">
        <div>
          <label for="ident-alias" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.aliasLabel')}</label
          >
          <Input id="ident-alias" bind:value={eAlias} placeholder={vm?.name} />
          <p class="text-xs text-muted-foreground mt-1">{t('vmDetail.aliasHelper')}</p>
        </div>
        <div class="flex justify-end gap-2 pt-2">
          <Button variant="outline" onclick={() => (showIdentity = false)}
            >{t('common.close')}</Button
          >
          <Button onclick={saveIdentityBasics} disabled={savingIdentity}
            >{savingIdentity ? t('vmDetail.notesSaving') : t('common.save')}</Button
          >
        </div>
      </div>
    {:else if identityTab === 'cover'}
      <div class="space-y-3">
        <div
          class="aspect-video w-full border border-border rounded-md bg-muted overflow-hidden flex items-center justify-center"
        >
          {#if coverPreview}
            <img src={coverPreview} alt="cover preview" class="w-full h-full object-cover" />
          {:else if vm?.cover}
            <img src={vm.cover} alt="cover" class="w-full h-full object-cover" />
          {:else}
            <div class="text-center text-muted-foreground p-6">
              <svg
                class="w-10 h-10 mx-auto mb-2 opacity-40"
                fill="none"
                stroke="currentColor"
                stroke-width="1.5"
                viewBox="0 0 24 24"
                ><rect x="3" y="3" width="18" height="18" rx="2" /><circle
                  cx="8.5"
                  cy="8.5"
                  r="1.5"
                /><path d="m21 15-5-5L5 21" /></svg
              >
              <p class="text-xs">{t('vmDetail.noCover')}</p>
            </div>
          {/if}
        </div>
        <div class="flex items-center gap-2">
          <label class={cn(buttonVariants({ variant: 'outline' }), 'cursor-pointer')}>
            {coverFile ? t('vmDetail.changeLabel') : t('vmDetail.chooseImage')}
            <input
              type="file"
              accept="image/png,image/jpeg,image/webp"
              class="hidden"
              onchange={onCoverPicked}
            />
          </label>
          {#if coverFile}
            <span class="text-xs text-muted-foreground truncate"
              >{coverFile.name} ({(coverFile.size / 1024).toFixed(0)} KB)</span
            >
            <Button size="sm" onclick={uploadCover} disabled={uploadingCover}
              >{uploadingCover ? t('vmDetail.uploading') : t('vmDetail.upload')}</Button
            >
          {/if}
          {#if vm?.cover}
            <Button size="sm" variant="outline" onclick={removeCover}
              >{t('vmDetail.removeCurrent')}</Button
            >
          {/if}
        </div>
        <p class="text-xs text-muted-foreground">{t('vmDetail.coverHint')}</p>

        <!-- Library picker: the same images available in Multimedia, so a
             cover can be reused without uploading it a second time. -->
        <div class="border-t border-border pt-3 space-y-2">
          <div class="flex items-center justify-between">
            <span class="text-xs font-medium">{t('vmDetail.coverFromLibrary')}</span>
            {#if mediaLoading}
              <span class="text-xs text-muted-foreground">{t('common.loading')}</span>
            {/if}
          </div>
          {#if mediaError}
            <p class="text-xs text-destructive">{mediaError}</p>
          {:else if !mediaLoading && mediaItems.length === 0}
            <p class="text-xs text-muted-foreground">{t('vmDetail.coverLibraryEmpty')}</p>
          {:else}
            <div class="grid grid-cols-2 sm:grid-cols-4 gap-2 max-h-52 overflow-y-auto">
              {#each mediaItems as item (item.id)}
                <button
                  type="button"
                  class={cn(
                    'aspect-video rounded border overflow-hidden bg-muted transition-opacity hover:opacity-80 disabled:opacity-50',
                    vm?.cover === `/api/media/${item.id}/raw`
                      ? 'border-primary ring-1 ring-primary'
                      : 'border-border'
                  )}
                  title={item.name || item.id}
                  disabled={uploadingCover}
                  onclick={() => pickCoverFromLibrary(item)}
                >
                  <img
                    src="/api/media/{item.id}/raw"
                    alt={item.name || item.id}
                    loading="lazy"
                    class="w-full h-full object-cover"
                  />
                </button>
              {/each}
            </div>
          {/if}
        </div>
      </div>
    {:else if identityTab === 'network'}
      <div class="space-y-3">
        {#if vm?.state !== 'shutoff'}
          <div
            class="p-3 border border-warning/30 bg-warning/10 rounded-md text-warning text-xs flex items-start gap-2"
          >
            <svg
              class="w-4 h-4 shrink-0 mt-0.5"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              viewBox="0 0 24 24"
              ><path
                d="M12 9v2m0 4h.01M5 19h14a2 2 0 0 0 1.84-2.75L13.74 4a2 2 0 0 0-3.48 0L3.16 16.25A2 2 0 0 0 5 19z"
              /></svg
            >
            <span>{t('vmDetail.networkEditShutoff')}</span>
          </div>
        {/if}
        {#if !vm?.networks || vm.networks.length === 0}
          <p class="text-sm text-muted-foreground">{t('vmDetail.noNetworksToEdit')}</p>
        {:else}
          <div class="space-y-2">
            {#each vm.networks as iface (iface.mac || iface.target || iface.name)}
              {@const edit = ifaceEdits[iface.mac] || {
                mac: iface.mac,
                network: iface.network,
                vlan: '',
                busy: false,
                error: '',
              }}
              {@const support = vlanSupportByNetwork[iface.network]}
              <div class="border border-border rounded-md bg-background p-3 space-y-2">
                <div class="grid grid-cols-1 sm:grid-cols-3 gap-2">
                  <div>
                    <div class="block text-xs font-medium text-muted-foreground mb-1">
                      {t('vmDetail.mac')}
                    </div>
                    <Input
                      bind:value={edit.mac}
                      disabled={vm.state !== 'shutoff'}
                      class="font-mono text-xs"
                    />
                  </div>
                  <div>
                    <div class="block text-xs font-medium text-muted-foreground mb-1">
                      {t('vmDetail.tabNetwork')}
                    </div>
                    <select
                      bind:value={edit.network}
                      disabled={vm.state !== 'shutoff'}
                      class="input !text-xs"
                    >
                      {#each vmNetworks as n (n)}
                        <option value={n.name}>{n.name}</option>
                      {/each}
                    </select>
                  </div>
                  <div>
                    <div class="block text-xs font-medium text-muted-foreground mb-1">
                      {t('vmDetail.vlanTag')}
                      <span class="text-muted-foreground font-normal">{t('vmDetail.vlanHint')}</span
                      >
                    </div>
                    <Input
                      bind:value={edit.vlan}
                      disabled={vm.state !== 'shutoff' || (support && !support.supported)}
                      placeholder="—"
                      class="tnum text-xs"
                    />
                  </div>
                </div>
                {#if support && !support.supported}
                  <p class="text-xs text-warning">
                    {t('vmDetail.vlanUnavailable', { reason: support.reason })}
                  </p>
                {/if}
                {#if edit.error}
                  <p class="text-xs text-destructive">{edit.error}</p>
                {/if}
                <div class="flex justify-end">
                  <Button
                    size="xs"
                    onclick={() => saveIface(iface.mac)}
                    disabled={edit.busy || vm.state !== 'shutoff'}
                  >
                    {#if edit.busy}<Spinner size="xs" color="text-white" />{:else}{t(
                        'vmDetail.saveInterface'
                      )}{/if}
                  </Button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {:else if identityTab === 'notes'}
      <div class="space-y-3">
        <div>
          <label for="ident-notes" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.notesLabel')}</label
          >
          <textarea
            id="ident-notes"
            bind:value={eNotes}
            onblur={() => saveNotesIfChanged()}
            rows="8"
            class="input !text-sm font-mono"
            placeholder={t('vmDetail.notesPlaceholder')}
          ></textarea>
          <p class="text-xs text-muted-foreground mt-1 flex items-center gap-1.5">
            {#if notesStatus === 'saving'}
              <Spinner size="xs" />
              <span>{t('vmDetail.notesSaving')}</span>
            {:else if notesStatus === 'saved'}
              <svg
                class="w-3 h-3 text-success"
                fill="none"
                stroke="currentColor"
                stroke-width="2.5"
                viewBox="0 0 24 24"><polyline points="20 6 9 17 4 12" /></svg
              >
              <span class="text-success">{t('vmDetail.notesSaved')}</span>
            {:else if notesStatus === 'error'}
              <svg
                class="w-3 h-3 text-destructive"
                fill="none"
                stroke="currentColor"
                stroke-width="2"
                viewBox="0 0 24 24"
                ><circle cx="12" cy="12" r="10" /><line x1="12" y1="8" x2="12" y2="12" /><line
                  x1="12"
                  y1="16"
                  x2="12.01"
                  y2="16"
                /></svg
              >
              <span class="text-destructive">{notesError}</span>
            {:else}
              <span>{t('vmDetail.autoSaves')}</span>
            {/if}
          </p>
        </div>
      </div>
    {:else if identityTab === 'groups'}
      <div class="space-y-3">
        <div>
          <span class="block text-sm font-medium mb-1.5">{t('vmDetail.groupsLabel')}</span>
          {#if eGroupsList.length === 0}
            <p class="text-sm text-muted-foreground">{t('vmDetail.noGroupsCreateFirst')}</p>
          {:else}
            <!-- Toggle chips only — never free text. A group name can
                 contain spaces (e.g. "APPS WEBS"); typed comma/space-
                 separated text used to split that into unregistered
                 fragments that never matched the real group. -->
            <div class="flex flex-wrap gap-1.5">
              {#each eGroupsList as g (g.name)}
                {@const selected = eGroupsSet.has(g.name)}
                <button
                  type="button"
                  onclick={() => {
                    const next = new Set(eGroupsSet);
                    if (next.has(g.name)) next.delete(g.name);
                    else next.add(g.name);
                    eGroupsSet = next;
                  }}
                  aria-pressed={selected}
                  class="text-xs px-2.5 py-1 rounded-full border transition-colors {selected
                    ? 'border-transparent text-white'
                    : ''}"
                  style={selected
                    ? `background-color: ${g.color}`
                    : `border-color: ${g.color}40; background-color: ${g.color}15; color: ${g.color}`}
                  >{g.name}</button
                >
              {/each}
            </div>
          {/if}
          <p class="text-xs text-muted-foreground mt-1.5">{@html t('vmDetail.groupsHelper')}</p>
        </div>
        <div class="flex justify-end gap-2 pt-2">
          <Button variant="outline" onclick={() => (showIdentity = false)}
            >{t('common.close')}</Button
          >
          <Button onclick={saveIdentityBasics} disabled={savingIdentity}
            >{savingIdentity ? t('vmDetail.notesSaving') : t('common.save')}</Button
          >
        </div>
      </div>
    {/if}
  </Dialog.Content>
</Dialog.Root>

{#snippet snapshotNode(node, depth)}
  <div
    class="flex items-center justify-between py-1.5 border-b border-border last:border-0"
    style="padding-left: {depth * 20}px"
  >
    <div class="flex items-center gap-2 min-w-0">
      {#if depth > 0}
        <svg
          class="w-3 h-3 text-muted-foreground/40 shrink-0"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          viewBox="0 0 24 24"><polyline points="9 6 15 12 9 18" /></svg
        >
      {/if}
      <div class="min-w-0">
        <p class="text-sm font-medium truncate">
          {node.name}
          {#if node.current}
            <span
              class="text-[10px] text-success ml-1.5 px-1.5 py-0.5 rounded border border-success/30 bg-success/10 uppercase tracking-wider"
              >{t('vmDetail.current')}</span
            >
          {/if}
        </p>
        <p class="text-xs text-muted-foreground tnum">
          {formatSnapshotDate(node.creation_time)}
          {#if node.size_at_snap_bytes}
            <span class="mx-1.5 text-border">·</span>
            <span class="text-accent">{bytesToStr(node.size_at_snap_bytes)}</span>
            <span class="text-muted-foreground/70">{t('vmDetail.atCreation')}</span>
          {/if}
        </p>
      </div>
    </div>
    <div class="flex gap-1 shrink-0">
      {#if !node.current}
        <button
          onclick={() => revertSnapshot(node.id)}
          class="text-xs text-warning hover:text-warning px-2 py-1 rounded hover:bg-warning/10"
          >{t('vmDetail.revert')}</button
        >
      {/if}
      <button
        onclick={() => deleteSnapshot(node.id)}
        class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10"
        >{t('common.delete')}</button
      >
    </div>
  </div>
  {#each node.children as child (child.name)}
    {@render snapshotNode(child, depth + 1)}
  {/each}
{/snippet}
