<script>
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import PasswordModal from '$lib/components/PasswordModal.svelte';
  import CredentialsModal from '$lib/components/CredentialsModal.svelte';
  import TerminalPanel from '$lib/components/TerminalPanel.svelte';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Tabs from '$lib/components/Tabs.svelte';
  import { upsertTask, updateTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { onMount } from 'svelte';
  import { fade } from 'svelte/transition';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { t } from '../lib/i18n.svelte.js';
  import { stateDotClass } from '$lib/utils/vmState.js';
  import { formatRate } from '$lib/utils/format.js';
  import { events } from '$lib/stores/events.svelte.js';
  import { navigate, getRoute } from '$lib/router.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
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
    Loader2,
  } from '@lucide/svelte';

  let { vmId } = $props();

  let vm = $state(null);
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
  // Power schedule.
  let schedStart = $state('');
  let schedStop = $state('');
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

  // Edit state
  let showEdit = $state(false);
  let eName = $state('');
  let eVcpus = $state(2);
  let eRamMB = $state(2048);
  let eCPUMode = $state('host-passthrough');
  let eVideoModel = $state('virtio');
  let eNetwork = $state('default');
  let eNetworkModel = $state('virtio');
  let eChipset = $state('q35');
  let eSecureBoot = $state(false);
  let eTPM = $state(false);
  let eFirmware = $state('uefi');
  let eOSType = $state('');
  let eOSVersion = $state('');
  let editSaving = $state(false);

  const networkModels = [
    { value: 'virtio', label: 'virtio (recommended)' },
    { value: 'e1000e', label: 'e1000e (Intel, ideal for Windows)' },
    { value: 'e1000', label: 'e1000 (legacy Intel)' },
    { value: 'rtl8139', label: 'rtl8139 (Realtek, very compatible)' },
    { value: 'pcnet', label: 'pcnet (AMD, legacy)' },
  ];

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

  // Change ISO state
  let showChangeISO = $state(false);
  let cISOTarget = $state('');
  let cISOSource = $state('');

  // Resize Disk state
  let showResizeDisk = $state(false);
  let resizeDiskTarget = $state('');
  let resizeDiskSize = $state(10);
  let resizeDiskCurrent = $state(0);

  // Add Net state
  let showAddNet = $state(false);
  let aNetNetwork = $state('default');
  let aNetModel = $state('virtio');

  // Clone state
  let showClone = $state(false);
  let cName = $state('');
  let cPool = $state('webkvm-disks');

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

  // Which of the 4 tabs is showing. 'overview' bundles the spec/metrics/
  // serial-console/firewall/schedule cards that used to be freestanding
  // blocks — see BlockCard/vmLayout.svelte.js.
  let activeSection = $state('overview');
  const sectionTabs = $derived([
    { id: 'overview', label: t('vmDetail.overview') },
    { id: 'disks', label: t('vmDetail.disks') },
    { id: 'net', label: t('vmDetail.networkInterfaces') },
    { id: 'snaps', label: t('vmDetail.snapshots') },
  ]);

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

  onMount(() => {
    load();
    loadMetrics();
    // Deep link: /vms/:id?serial=1 scrolls to the embedded serial console.
    if (getRoute().query?.serial === '1') setTimeout(gotoSerial, 400);
    const offMetrics = events.onVmMetrics((e) => {
      if (e.vm_id !== vmId) return;
      metrics = e.data;
    });

    // Subscribe to VM state events for this VM
    const off = events.onVmState((e) => {
      if (e.vm_id !== vmId) return;
      if (vm && vm.state !== e.state) {
        vm = { ...vm, state: e.state, name: e.name || vm.name };
        // Light refetch to update uptime/ip
        load(true);
      }
    });
    return () => {
      off();
      offMetrics();
    };
  });

  async function loadMetrics() {
    try {
      metrics = await api.getVMMetrics(vmId);
    } catch {
      metrics = null;
    }
  }

  async function load(silent = false) {
    if (!vmId) return;
    if (!silent) loading = true;
    error = '';
    try {
      const [vmData, snapData, bootData] = await Promise.all([
        api.getVM(vmId),
        api.listSnapshots(vmId),
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
      setTimeout(() => {
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

  async function uploadCover() {
    if (!coverFile) return;
    uploadingCover = true;
    try {
      const res = await api.uploadCover(vmId, coverFile);
      vm = { ...vm, cover: res.url };
      toast.success(t('vmDetail.coverUpdated'));
      coverFile = null;
      coverPreview = null;
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
    eName = vm.name;
    eVcpus = vm.vcpus;
    eRamMB = vm.ram_mb;
    eCPUMode = vm.cpu_mode || 'host-passthrough';
    eVideoModel = vm.video_model || 'virtio';
    eNetwork = vm.networks?.[0]?.network || networks[0]?.name || 'default';
    eNetworkModel = vm.networks?.[0]?.model || 'virtio';
    eChipset = vm.chipset || 'q35';
    eSecureBoot = vm.secure_boot;
    eTPM = vm.tpm_enabled;
    eFirmware = vm.chipset === 'i440fx' ? 'seabios' : vm.firmware || 'seabios';
    if (eChipset === 'i440fx') {
      eSecureBoot = false;
      eTPM = false;
    }
    eOSType = vm.os_type || '';
    eOSVersion = vm.os_version || '';
    showEdit = true;
  }

  async function saveEdit() {
    editSaving = true;
    try {
      const data = {};
      if (eName !== vm.name) data.name = eName;
      if (eVcpus !== vm.vcpus) data.vcpus = eVcpus;
      if (eRamMB !== vm.ram_mb) data.ram_mb = eRamMB;
      if (eCPUMode !== (vm.cpu_mode || 'host-passthrough')) data.cpu_mode = eCPUMode;
      if (eVideoModel !== (vm.video_model || 'virtio')) data.video_model = eVideoModel;
      if (eNetwork !== (vm.networks?.[0]?.network || networks[0]?.name || 'default'))
        data.network = eNetwork;
      if (eNetworkModel !== (vm.networks?.[0]?.model || 'virtio'))
        data.network_model = eNetworkModel;
      if (eOSType !== (vm.os_type || '')) data.os_type = eOSType;
      if (eOSVersion !== (vm.os_version || '')) data.os_version = eOSVersion;
      const effSecureBoot = eFirmware === 'uefi' ? eSecureBoot : false;
      const effTPM = eFirmware === 'uefi' ? eTPM : false;
      if (effSecureBoot !== vm.secure_boot) data.secure_boot = effSecureBoot;
      if (effTPM !== vm.tpm_enabled) data.tpm_enabled = effTPM;
      if (eFirmware !== (vm.firmware || 'uefi')) data.firmware = eFirmware;
      await api.updateVM(vmId, data);
      showEdit = false;
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

  // Two-step delete flow: step 1 = confirm + optional "delete disks"
  // checkbox; step 2 (only when deleting disks) = red, type-the-name
  // irreversible confirmation.
  let showDeleteFlow = $state(false);
  let deleteFlowDesc = $state('');
  let deleteWithDisks = $state(false);
  let showDeleteStep2 = $state(false);
  let deleteConfirmText = $state('');
  let deleteBusy = $state(false);

  const vmDisks = $derived(vm?.disks?.filter((d) => d.device === 'disk') ?? []);
  const confirmNameOk = $derived(
    Boolean(vm) && deleteConfirmText.trim().toLowerCase() === (vm?.name || '').toLowerCase()
  );

  async function performDelete(withDisks) {
    deleteBusy = true;
    try {
      const res = await api.deleteVM(vmId, withDisks);
      showDeleteFlow = false;
      showDeleteStep2 = false;
      toast.success(t('vmDetail.vmDeleted', { name: vm.name }));
      navigate('/vms');
      return res;
    } catch (e) {
      toast.error(e.message);
      return null;
    } finally {
      deleteBusy = false;
    }
  }

  function onDeleteFlowConfirm() {
    if (!deleteWithDisks) {
      performDelete(false);
      return;
    }
    showDeleteFlow = false;
    showDeleteStep2 = true;
  }

  let showAppCreds = $state(false);
  // Visible diagnostics: any render/JS error shows on screen.
  let boundaryMsg = $state(null);
  let boundaryStack = $state('');

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
          await doActionRun(action);
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
          await doActionRun(action);
        },
      });
      return;
    }
    if (action === 'deleteVM') {
      const active = vm.state === 'running' || vm.state === 'paused';
      deleteWithDisks = false;
      deleteConfirmText = '';
      showDeleteFlow = true;
      deleteFlowDesc = active ? t('vmDetail.deleteVmDescRunning') : t('vmDetail.deleteVmDescOff');
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
        }));
      const forwards = fwForwards
        .filter((f) => f.host_port && f.guest_port)
        .map((f) => ({
          id: f.id,
          proto: f.proto,
          host_port: Number(f.host_port),
          guest_port: Number(f.guest_port),
          target_ip: f.target_ip || '',
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
      await api.createSnapshot(vmId, {
        name: snapName,
        description: snapDesc,
        memory: snapMemory,
      });
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

  async function addDisk() {
    actionLoading = 'adddisk';
    try {
      const data = { device: aDiskDevice === 'existing' ? 'disk' : aDiskDevice, bus: aDiskBus };
      if (aDiskDevice === 'cdrom') {
        data.source = aDiskISO;
      } else if (aDiskDevice === 'existing') {
        const vol = aDiskVolumes.find((v) => v.path === aDiskExistingVol);
        data.source = aDiskExistingVol;
        data.format = vol?.format || 'qcow2';
      } else {
        data.format = aDiskFormat;
        data.size_gb = aDiskSize;
        data.pool = aDiskPool;
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
  async function loadHostUSBDevices() {
    try {
      hostUSBDevices = await api.listHostUSBDevices();
    } catch (e) {
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

  async function cloneVM() {
    if (!cName) return;
    actionLoading = 'clone';
    try {
      await api.cloneVM(vmId, { name: cName, pool: cPool });
      showClone = false;
      toast.success(t('vmDetail.vmCloned', { name: cName }));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      actionLoading = '';
    }
  }

  // Export
  function exportVM() {
    if (!requireShutoff(t('vmDetail.requireShutoffExporting'))) return;
    exportTarget = 'vmware';
    exportProgress = null;
    showExport = true;
  }

  // Download a .rdp / .vv console file with the Bearer token in the
  // Authorization header (never in the URL), then save it as a blob.
  async function downloadConsoleFile(kind) {
    const url = kind === 'rdp' ? api.getRDPUrl(vmId) : api.getSPICEUrl(vmId);
    try {
      const res = await fetch(url, {
        headers: { Authorization: `Bearer ${auth.token}` },
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
    if (!requireShutoff(t('vmDetail.requireShutoffExporting'))) {
      showExport = false;
      return;
    }
    const label =
      exportTarget === 'backup'
        ? t('vmDetail.exportBuildingBackup')
        : t('vmDetail.exportBuildingOva', { target: exportTarget });
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
      setTimeout(() => {
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
        setTimeout(() => {
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

<div class="p-4 sm:p-6 max-w-6xl">
  <div class="flex items-center gap-3 mb-6">
    <button
      onclick={onBack}
      class="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
      aria-label="Back"
    >
      <svg class="w-4 h-4" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">
        <polyline points="15 18 9 12 15 6" />
      </svg>
    </button>
    {#if vm}
      <div class="flex items-center gap-3 flex-1 flex-wrap min-w-0">
        <span class="status-dot {stateDotClass(vm.state)} shrink-0"></span>
        <h1 class="text-xl font-semibold tracking-tight truncate min-w-0">{vm.alias || vm.name}</h1>
        {#if vm.alias && vm.alias !== vm.name}
          <span class="text-xs text-muted-foreground font-mono truncate shrink-0">({vm.name})</span>
        {/if}
        <span class="text-xs text-muted-foreground capitalize shrink-0">{vm.state}</span>
        {#if vm.state === 'running' && vm.ip}
          <span
            class="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded bg-accent/10 border border-accent/20 text-accent font-mono truncate shrink-0 max-w-[15rem]"
          >
            {vm.ip}
          </span>
        {/if}
        {#if vmMeta?.template}
          <span
            class="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded bg-warning/10 border border-warning/30 text-warning font-medium shrink-0 whitespace-nowrap"
          >
            {t('vmDetail.templateBadge')}
          </span>
        {/if}
      </div>
      <div class="flex items-center gap-2 shrink-0">
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

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if loading}
    <div class="flex items-center justify-center py-24"><Spinner size="lg" /></div>
  {:else if vm}
    <div class="grid grid-cols-1 lg:grid-cols-[1fr_280px] gap-5">
      <!-- Main column -->
      <div class="space-y-5">
        <!-- Overview -->
        {#snippet sec_overview()}
          <BlockCard bid="overview" title={t('vmDetail.overview')}>
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
            <div class="grid grid-cols-2 gap-x-6 gap-y-1.5 text-sm">
              {#if vm.os_type}
                <div class="flex gap-2">
                  <span class="text-muted-foreground shrink-0">{t('vmDetail.os')}</span><span
                    class="truncate">{vm.os_type}{vm.os_version ? ' ' + vm.os_version : ''}</span
                  >
                </div>
              {/if}
              <div class="flex gap-2">
                <span class="text-muted-foreground shrink-0">{t('vmDetail.chipset')}</span><span
                  >{vm.chipset}</span
                >
              </div>
              <div class="flex gap-2">
                <span class="text-muted-foreground shrink-0">{t('vmDetail.secureBoot')}</span><span
                  >{vm.secure_boot ? t('common.yes') : t('common.no')}</span
                >
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
            </div>
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

        {#snippet sec_disks()}
          <BlockCard bid="disks" title={t('vmDetail.disks')}>
            <div class="flex items-center justify-between mb-3">
              <Button
                size="xs"
                variant="outline"
                onclick={() => {
                  aDiskDevice = 'disk';
                  aDiskBus = 'virtio';
                  aDiskSize = 10;
                  aDiskPool = pools.find((p) => p.purpose !== 'iso')?.name || 'webkvm-disks';
                  aDiskExistingVol = '';
                  aDiskVolumes = [];
                  showAddDisk = true;
                }}>+ Add Disk</Button
              >
            </div>
            {#if !vm.disks || vm.disks.length === 0}
              <p class="text-sm text-muted-foreground">{t('vmDetail.noDisks')}</p>
            {:else}
              <div class="space-y-1.5">
                {#each vm.disks as disk}
                  <div
                    class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
                  >
                    <div class="flex items-center gap-2 min-w-0">
                      <span class="text-xs font-mono text-muted-foreground w-8">{disk.target}</span>
                      <span
                        class="text-xs px-1.5 py-0.5 rounded border {disk.device === 'cdrom'
                          ? 'border-accent/30 bg-accent/10 text-accent'
                          : 'border-border bg-muted text-muted-foreground'}"
                        >{disk.device === 'cdrom' ? 'CDROM' : 'DISK'}</span
                      >
                      <span class="text-xs text-muted-foreground">{disk.bus}</span>
                      <span class="text-sm truncate">{diskLabel(disk)}</span>
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
                      <button
                        onclick={() => {
                          if (!requireShutoff(t('vmDetail.removing'))) return;
                          removeDisk(disk.target);
                        }}
                        title={t('vmDetail.requireShutoffTitle')}
                        class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10"
                        >{t('vmDetail.remove')}</button
                      >
                    </div>
                  </div>
                {/each}
              </div>
            {/if}
          </BlockCard>
        {/snippet}

        {#snippet sec_net()}
          <BlockCard bid="net" title={t('vmDetail.networkInterfaces')}>
            <div class="flex items-center justify-between mb-3">
              <Button
                size="xs"
                variant="outline"
                onclick={() => {
                  aNetNetwork = networks[0]?.name || 'default';
                  aNetModel = 'virtio';
                  showAddNet = true;
                }}>+ {t('vmDetail.addInterface')}</Button
              >
            </div>
            {#if !vm.networks || vm.networks.length === 0}
              <p class="text-sm text-muted-foreground">{t('vmDetail.noNetworkInterfaces')}</p>
            {:else}
              <div class="space-y-1.5">
                {#each vm.networks as iface, idx}
                  <div
                    class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
                  >
                    <div class="flex items-center gap-2 flex-wrap min-w-0">
                      <span class="text-xs font-mono text-muted-foreground">{iface.mac}</span>
                      <span
                        class="text-xs px-1.5 py-0.5 rounded border border-border bg-muted text-muted-foreground"
                        >{iface.model}</span
                      >
                      <span class="text-sm">{iface.network}</span>
                      {#if vm.state === 'running' && idx === 0 && vm.ip}
                        <span
                          class="text-xs px-1.5 py-0.5 rounded bg-accent/10 border border-accent/20 text-accent font-mono"
                          >{t('vmDetail.ipLabel', { ip: vm.ip })}</span
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
                  <p class="text-sm text-muted-foreground">{t('vmDetail.usbNoneAttached')}</p>
                {:else}
                  <div class="space-y-1.5">
                    {#each vm.usb_devices as dev}
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
                    onclick={loadHostUSBDevices}
                    class="text-xs text-accent hover:text-accent-hover"
                    >{t('common.refresh')}</button
                  >
                </div>
                {#if hostUSBDevices.length === 0}
                  <p class="text-sm text-muted-foreground">{t('vmDetail.usbNoneFound')}</p>
                {:else}
                  <div class="space-y-1.5">
                    {#each hostUSBDevices as dev (dev.vendor_id + dev.product_id)}
                      <div
                        class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
                      >
                        <div class="flex items-center gap-2 min-w-0">
                          <span class="text-sm truncate">{dev.name}</span>
                          <span class="text-xs font-mono text-muted-foreground"
                            >{dev.vendor_id}:{dev.product_id}</span
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
              <p class="text-sm text-muted-foreground">{t('vmDetail.noSnapshots')}</p>
            {:else}
              <div class="space-y-0.5">
                {#each snapshotTree.roots as root}
                  {@render snapshotNode(root, 0)}
                {/each}
              </div>
            {/if}
          </BlockCard>
        {/snippet}

        {#snippet sec_firewall()}
          <BlockCard bid="firewall" title={t('vmDetail.firewall')} anchor="firewall">
            <!-- Port forwards -->
            <div class="mb-4">
              <h3 class="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2">
                {t('vmDetail.portForwards')}
              </h3>
              <div class="space-y-2">
                {#each fwForwards as fw (fw.id)}
                  <div class="flex items-center gap-2">
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
                  <p class="text-sm text-muted-foreground">{t('vmDetail.noForwards')}</p>
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
                  <div class="flex items-center gap-2">
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
                  <p class="text-sm text-muted-foreground">{t('vmDetail.noRules')}</p>
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
            <div class="grid grid-cols-2 gap-3 mb-3">
              <div class="space-y-1.5">
                <Label for="sched-start">{t('vmDetail.scheduleStart')}</Label>
                <Input
                  id="sched-start"
                  bind:value={schedStart}
                  placeholder="0 8 * * *"
                  class="font-mono"
                />
              </div>
              <div class="space-y-1.5">
                <Label for="sched-stop">{t('vmDetail.scheduleStop')}</Label>
                <Input
                  id="sched-stop"
                  bind:value={schedStop}
                  placeholder="0 22 * * *"
                  class="font-mono"
                />
              </div>
            </div>
            <p class="text-xs text-muted-foreground mb-3">{t('vmDetail.scheduleHint')}</p>
            <Button size="sm" onclick={saveSchedule} disabled={schedSaving}>
              {schedSaving ? t('common.saving') : t('common.save')}
            </Button>
          </BlockCard>
        {/snippet}

        {#snippet sec_serial()}
          <div id="vm-serial-block">
            <BlockCard bid="serial" title="Consola serial">
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
        <div class={activeSection === 'disks' ? '' : 'hidden'}>
          {@render sec_disks()}
        </div>
        <div class={activeSection === 'net' ? '' : 'hidden'}>
          {@render sec_net()}
          {#if auth.isAdmin()}
            {@render sec_usb()}
          {/if}
        </div>
        <div class={activeSection === 'snaps' ? '' : 'hidden'}>
          {@render sec_snaps()}
        </div>
      </div>

      <!-- Sidebar -->
      <div class="space-y-4">
        <div class="border border-border rounded-lg bg-card p-4">
          <h2 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-3">
            {t('vmDetail.power')}
          </h2>
          <div class="space-y-2">
            {#key vm.state}
              <div transition:fade={{ duration: 150 }}>
                {#if vm.state === 'shutoff'}
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
        </div>

        <div class="border border-border rounded-lg bg-card p-4">
          <h2 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-3">
            {t('vmDetail.actions')}
          </h2>
          <div class="space-y-2">
            <Button onclick={openConsole} class="w-full">
              <Terminal class="w-4 h-4 mr-1.5" />
              {t('vmDetail.openConsole')}
            </Button>
            <Button variant="outline" onclick={gotoSerial} class="w-full">
              <Terminal class="w-4 h-4 mr-1.5" />
              Serial Console
            </Button>
            {#if appInfo}
              <Button variant="outline" onclick={showAppCredentials} class="w-full">
                <KeyRound class="w-4 h-4 mr-1.5" />
                Credenciales de la app
              </Button>
            {/if}
            {#if auth.isAdmin()}
              <Button
                variant="outline"
                onclick={resetPassword}
                disabled={busy || (vm && vm.state !== 'running')}
                title={vm && vm.state !== 'running'
                  ? 'The VM must be running with qemu-guest-agent to reset its password'
                  : ''}
                class="w-full"
              >
                Reset Password
              </Button>
            {/if}
            <Button
              variant="destructive"
              onclick={() => doAction('deleteVM')}
              disabled={busy}
              class="w-full"
            >
              <Trash2 class="w-4 h-4 mr-1.5" />
              {t('vmDetail.deleteVM')}
            </Button>
            <Button
              variant="outline"
              onclick={() => {
                cName = vm.name + '-clone';
                cPool = pools.find((p) => p.purpose !== 'iso')?.name || 'webkvm-disks';
                showClone = true;
              }}
              class="w-full"
            >
              <CopyPlus class="w-4 h-4 mr-1.5" />
              {t('vmDetail.cloneVM')}
            </Button>
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
              >
                SPICE
              </Button>
            </div>
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
        </div>
      </div>
    </div>
  {/if}
</div>

{#if boundaryMsg}
  <div class="p-4 m-4 rounded-lg border border-red-500/50 bg-red-500/10">
    <p class="font-semibold text-destructive mb-1">
      Error de interfaz — pega este texto al reportar:
    </p>
    <pre class="text-xs whitespace-pre-wrap break-all">{boundaryMsg}</pre>
    {#if boundaryStack}<pre
        class="text-[10px] whitespace-pre-wrap opacity-70 mt-2 max-h-40 overflow-auto">{boundaryStack}</pre>{/if}
  </div>
{/if}

<PasswordModal
  bind:open={showPasswordModal}
  username={resetUsername}
  password={resetPasswordValue}
  title="Password Reset"
/>

<CredentialsModal bind:open={showAppCreds} info={appCreds} />

<!-- Reset password error: guest agent not available or reset failed -->
<Dialog.Root bind:open={showResetError}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>Password Reset Failed</Dialog.Title>
      <Dialog.Description>
        The password could not be changed. To reset a VM password, the VM must be running with the
        QEMU guest agent installed and active.
      </Dialog.Description>
    </Dialog.Header>
    {#if resetError}
      <p class="text-sm text-muted-foreground break-words">{resetError}</p>
    {/if}
    <p class="text-xs text-muted-foreground mt-2">
      Inside the VM, run:
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

<!-- Delete flow — step 1: confirm + optional disk cleanup -->
<Dialog.Root bind:open={showDeleteFlow}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vmDetail.deleteVmTitle')}</Dialog.Title>
      <Dialog.Description>{deleteFlowDesc}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      <label class="flex items-start gap-2 text-sm cursor-pointer select-none">
        <input
          type="checkbox"
          bind:checked={deleteWithDisks}
          class="w-4 h-4 mt-0.5 rounded border-border"
        />
        <span>
          Eliminar también sus discos
          {#if vmDisks.length > 0}
            <span class="block text-xs text-muted-foreground mt-0.5">
              ({vmDisks.map((d) => d.name || d.source).join(', ')})
            </span>
          {/if}
        </span>
      </label>
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showDeleteFlow = false)} disabled={deleteBusy}>
        Cancel
      </Button>
      <Button variant="destructive" onclick={onDeleteFlowConfirm} disabled={deleteBusy}>
        {#if deleteBusy}<Loader2 class="h-4 w-4 animate-spin" />{:else}Delete{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Delete flow — step 2: irreversible, type the VM name -->
<Dialog.Root
  bind:open={showDeleteStep2}
  onOpenChange={(o) => {
    if (!o) deleteConfirmText = '';
  }}
>
  <Dialog.Content class="sm:max-w-md border-destructive/40">
    <Dialog.Header>
      <Dialog.Title class="text-destructive">Eliminación irreversible</Dialog.Title>
      <Dialog.Description>
        Se borrarán permanentemente los discos de <strong>{vm?.name}</strong> junto con todos sus datos.
        Esta acción NO se puede deshacer.
      </Dialog.Description>
    </Dialog.Header>
    <div class="space-y-2">
      <p class="text-sm">
        Escribe <strong>{vm?.name}</strong> para habilitar el botón:
      </p>
      <Input bind:value={deleteConfirmText} placeholder={vm?.name} autocomplete="off" />
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showDeleteStep2 = false)} disabled={deleteBusy}>
        Cancel
      </Button>
      <Button
        variant="destructive"
        disabled={!confirmNameOk || deleteBusy}
        onclick={() => performDelete(true)}
      >
        {#if deleteBusy}<Loader2 class="h-4 w-4 animate-spin" />{:else}Borrar VM y discos{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Edit Settings Dialog -->
<Dialog.Root bind:open={showEdit}>
  <Dialog.Content class="sm:max-w-lg max-h-[90vh] overflow-y-auto">
    <Dialog.Header>
      <Dialog.Title>{t('vmDetail.editVmSettings')}</Dialog.Title>
      <Dialog.Description>{t('vmDetail.someChangesRequireRestart')}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      <div>
        <label for="edit-name" class="block text-sm font-medium mb-1.5">{t('common.name')}</label>
        <Input id="edit-name" bind:value={eName} type="text" />
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="edit-vcpus" class="block text-sm font-medium mb-1.5">{t('common.vcpu')}</label
          >
          <Input id="edit-vcpus" type="number" min="1" max="64" bind:value={eVcpus} class="tnum" />
        </div>
        <div>
          <label for="edit-ram" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.ramLabel')}</label
          >
          <Input
            id="edit-ram"
            type="number"
            min="512"
            step="512"
            bind:value={eRamMB}
            class="tnum"
          />
        </div>
      </div>
      <div>
        <label for="edit-cpu" class="block text-sm font-medium mb-1.5"
          >{t('vmDetail.cpuModeLabel')}</label
        >
        <select id="edit-cpu" bind:value={eCPUMode} class="input">
          <option value="host-passthrough">host-passthrough</option>
          <option value="host-model">host-model</option>
          <option value="max">max</option>
          <option value="custom">custom</option>
        </select>
      </div>
      <div>
        <label for="edit-video" class="block text-sm font-medium mb-1.5"
          >{t('vmDetail.videoModelLabel')}</label
        >
        <select id="edit-video" bind:value={eVideoModel} class="input">
          <option value="virtio">virtio</option>
          <option value="qxl">qxl</option>
          <option value="vga">VGA</option>
          <option value="cirrus">cirrus</option>
          <option value="vmvga">vmvga</option>
          <option value="bochs">bochs</option>
          <option value="none">none</option>
        </select>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="edit-net" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.networkLabel')}</label
          >
          <select id="edit-net" bind:value={eNetwork} class="input">
            {#each networks as net}<option value={net.name}>{net.name}</option>{/each}
          </select>
        </div>
        <div>
          <label for="edit-netmodel" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.adapter')}</label
          >
          <select id="edit-netmodel" bind:value={eNetworkModel} class="input">
            {#each networkModels as m}<option value={m.value}>{m.label}</option>{/each}
          </select>
        </div>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="edit-chipset" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.chipset')}
            <span class="text-xs text-muted-foreground">{t('vmDetail.chipsetLocked')}</span></label
          >
          <select id="edit-chipset" bind:value={eChipset} disabled class="input opacity-50">
            <option value="q35">Q35</option>
            <option value="i440fx">i440fx</option>
          </select>
        </div>
        <div>
          <label for="edit-firmware" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.firmwareLabel')}</label
          >
          <select
            id="edit-firmware"
            bind:value={eFirmware}
            disabled={eChipset === 'i440fx'}
            class="input {eChipset === 'i440fx' ? 'opacity-50' : ''}"
          >
            <option value="uefi">UEFI</option>
            <option value="seabios">SeaBIOS</option>
          </select>
          {#if eChipset === 'i440fx'}<p class="text-xs text-muted-foreground mt-1">
              {t('vmDetail.i440fxRequiresSeabios')}
            </p>{/if}
        </div>
      </div>
      {#if eFirmware === 'uefi'}
        <div class="flex gap-4">
          <label class="flex items-center gap-2 text-sm cursor-pointer">
            <input
              type="checkbox"
              bind:checked={eSecureBoot}
              class="rounded border-border bg-background text-accent focus:ring-accent"
            />
            {t('vmDetail.secureBootLabel')}
          </label>
          <label class="flex items-center gap-2 text-sm cursor-pointer">
            <input
              type="checkbox"
              bind:checked={eTPM}
              class="rounded border-border bg-background text-accent focus:ring-accent"
            />
            {t('vmDetail.tpm2')}
          </label>
        </div>
      {/if}
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="edit-ostype" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.osTypeLabel')}</label
          >
          <select id="edit-ostype" bind:value={eOSType} class="input">
            <option value="">{t('vmDetail.auto')}</option>
            <option value="linux">{t('vmDetail.linux')}</option>
            <option value="windows">{t('vmDetail.windows')}</option>
            <option value="freebsd">{t('vmDetail.freebsd')}</option>
            <option value="other">{t('vmDetail.other')}</option>
          </select>
        </div>
        <div>
          <label for="edit-osver" class="block text-sm font-medium mb-1.5"
            >{t('vmDetail.osVersionLabel')}</label
          >
          <Input id="edit-osver" bind:value={eOSVersion} placeholder="e.g. ubuntu24.04" />
        </div>
      </div>
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showEdit = false)} disabled={editSaving}
        >{t('common.cancel')}</Button
      >
      <Button onclick={saveEdit} disabled={editSaving}>
        {#if editSaving}<Spinner size="sm" color="text-white" /> {t('vmDetail.saving')}{:else}{t(
            'common.save'
          )}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Add Disk Dialog -->
<Dialog.Root bind:open={showAddDisk}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title
        >{aDiskDevice === 'cdrom'
          ? t('vmDetail.attachIso')
          : aDiskDevice === 'existing'
            ? t('vmDetail.attachExistingDisk')
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
          }}
          class="input"
        >
          <option value="disk">{t('vmDetail.diskType')}</option>
          <option value="existing">{t('vmDetail.existingDisk')}</option>
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
            <option value="ide">IDE</option>
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
            {#each pools.filter((p) => p.purpose !== 'iso') as p}<option value={p.name}
                >{p.name}</option
              >{/each}
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
          <select id="adisk-existing" bind:value={aDiskExistingVol} class="input">
            <option value="">{t('vmDetail.empty')}</option>
            {#each aDiskVolumes.filter((v) => !v.is_snapshot) as v}
              <option value={v.path}>{v.name} ({bytesToStr(v.capacity)})</option>
            {/each}
          </select>
        </div>
      {:else}
        <div>
          <label for="adisk-iso" class="block text-sm font-medium mb-1.5">{t('vmDetail.iso')}</label
          >
          <select id="adisk-iso" bind:value={aDiskISO} class="input">
            <option value="">{t('vmDetail.empty')}</option>
            {#each isos as iso}<option value={iso.path}>{iso.name}</option>{/each}
          </select>
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
        {#each isos as iso}<option value={iso.path}>{iso.name}</option>{/each}
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
          {#each networks as net}<option value={net.name}>{net.name}</option>{/each}
        </select>
      </div>
      <div>
        <label for="anet-model" class="block text-sm font-medium mb-1.5"
          >{t('vmDetail.model')}</label
        >
        <select id="anet-model" bind:value={aNetModel} class="input">
          {#each networkModels as m}<option value={m.value}>{m.label}</option>{/each}
        </select>
      </div>
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
          {#each pools.filter((p) => p.purpose !== 'iso') as p}<option value={p.name}
              >{p.name}</option
            >{/each}
        </select>
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

<!-- Export Dialog -->
<Dialog.Root bind:open={showExport}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vmDetail.exportTitle')}</Dialog.Title>
    </Dialog.Header>
    {#if !exportProgress}
      <p class="text-sm text-muted-foreground">{t('vmDetail.exportDesc')}</p>
      <div class="space-y-2 mt-2">
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

    <div class="flex gap-1 border-b border-border mb-4">
      {#each [['alias', t('vmDetail.tabAlias')], ['cover', t('vmDetail.tabCover')], ['network', t('vmDetail.tabNetwork')], ['notes', t('vmDetail.tabNotes')], ['groups', t('vmDetail.tabGroups')]] as [k, label]}
        <button
          onclick={() => (identityTab = k)}
          class="px-3 py-2 text-sm border-b-2 -mb-px transition-colors {identityTab === k
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
          <label class="btn btn-outline cursor-pointer text-sm">
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
            {#each vm.networks as iface}
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
                      {#each networks as n}
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
  {#each node.children as child}
    {@render snapshotNode(child, depth + 1)}
  {/each}
{/snippet}
