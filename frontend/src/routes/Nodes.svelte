<script>
  import { onMount, tick } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { t } from '../lib/i18n.svelte.js';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import CopyCommands from '$lib/components/CopyCommands.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import HaNodeOps from '$lib/components/HaNodeOps.svelte';
  import HaDrbdRepair from '$lib/components/HaDrbdRepair.svelte';
  import ClusterModuleCard from '$lib/components/ClusterModuleCard.svelte';
  import NodeInstallGuide from '$lib/components/NodeInstallGuide.svelte';
  import NodeUiUrlField from '$lib/components/NodeUiUrlField.svelte';
  import HaCompatPanel from '$lib/components/HaCompatPanel.svelte';
  import ClusterAccessCard from '$lib/components/ClusterAccessCard.svelte';
  import IncusMemberOps from '$lib/components/IncusMemberOps.svelte';
  import { getRoute } from '$lib/router.svelte.js';
  import HaWorkloadsPanel from '$lib/components/HaWorkloadsPanel.svelte';
  import {
    accessState,
    withProbeAccess,
    accessStateLabel,
    codeLabel,
    sshErrorText,
  } from '$lib/utils/clusterAccess.js';
  import {
    nodeConsoleURL,
    nodeAddressInfo,
    validateUIUrl,
    usagePercent,
    healingSecondsValid,
    codeText,
    pingAllOutcome,
    isSshPortOnly,
    nodeHealth,
    nodeHealthCounts,
    selfDuplicates,
    withoutSelfDuplicates,
  } from '$lib/utils/nodes.js';
  import { clusterModule } from '$lib/stores/clusterModule.svelte.js';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Card } from '$lib/components/ui/card';
  import { formatBytes } from '$lib/utils/format.js';

  let nodes = $state([]);
  // Managed SSH overview (admin only) shared by the access card and the
  // per-node access badge.
  let sshOverview = $state(null);
  let sshLoading = $state(false);
  let sshError = $state('');
  let accessCard = $state(null);
  let accessTesting = $state({});
  let summary = $state(null);
  let loading = $state(true);
  let refreshing = $state(false);
  // Monotonic request counter: a response whose number is no longer the
  // latest (a newer load, or a ping result applied meanwhile) is dropped.
  let loadSeq = 0;
  let togglingNode = $state({});
  let pingingAll = $state(false);
  let pingingNode = $state({});
  let error = $state('');
  let activeSection = $state('fleet');

  // Add-node form (admin only)
  let showAdd = $state(false);
  let addCardEl = $state(null);
  let addButtonEl = $state(null);
  let addName = $state('');
  let addBackend = $state('libvirt');
  let addMode = $state('easy');
  let sshHost = $state('');
  let sshUser = $state('');
  let sshPort = $state('22');
  let addUri = $state('');
  let addUiUrl = $state('');
  let incusProject = $state('default');
  let incusTrustToken = $state('');
  let addSaving = $state(false);
  let testingNode = $state(false);
  let testedUri = $state('');
  let testResult = $state(null);
  let fingerprintVerified = $state(false);
  let addError = $state('');
  let installConfirmOpen = $state(false);
  let installingNode = $state(false);
  let allowSystemUpgrade = $state(false);
  let downloadingInstaller = $state(false);
  let incusInventory = $state({});
  let loadingIncusInventory = $state({});
  let incusInventoryErrors = $state({});
  let haReadiness = $state(null);
  let loadingHAReadiness = $state(false);
  let haReadinessError = $state('');
  let haViewMode = $state('easy');
  let incusCluster = $state(null);
  let fleetVms = $state([]);
  let incusHealing = $state(null);
  let healingSeconds = $state(300);
  let savingHealing = $state(false);
  let healingDirty = $state(false);
  let healingError = $state('');
  let healingBlockers = $state([]);
  let healingConfirmOpen = $state(false);
  let forceDeleteOpen = $state(false);
  let pveFenceVerifying = $state(false);
  let pveFenceResult = $state(null);

  const incusMembers = $derived(incusCluster?.members || []);
  const incusOnline = $derived(incusMembers.filter((member) => member.status === 'Online').length);

  const loadFailed = $derived(!!error && nodes.length === 0);
  // This server is shown once: a remote entry that points back at it (left
  // over from a control-plane move) is listed apart, never as another node.
  const visibleNodes = $derived(withoutSelfDuplicates(nodes));
  const noAccessCount = $derived(nodes.filter((n) => nodeHealth(n) === 'no_access').length);
  const unmanagedCount = $derived((fleetVms || []).filter((v) => v.managed === false).length);

  // A tab whose content disappeared must not leave an empty panel.
  $effect(() => {
    if (activeSection === 'incus' && !incusMembers.length) activeSection = 'fleet';
  });
  const selfDups = $derived(selfDuplicates(nodes));
  // Every count on this page comes from the same classification
  // (nodeHealth), so the first load and later refreshes agree.
  const healthCounts = $derived(nodeHealthCounts(nodes));
  const totalNodes = $derived(summary?.total_nodes ?? visibleNodes.length);
  const disabledCount = $derived(healthCounts.disabled);
  const onlineCount = $derived(nodes.length ? healthCounts.online : (summary?.online_nodes ?? 0));
  // The backend reports 0 when no enabled node answered: that is "no data",
  // not a 0 ms latency.
  const avgLatencyText = $derived(
    onlineCount > 0 && summary?.avg_latency_ms != null && summary.avg_latency_ms > 0
      ? `${summary.avg_latency_ms} ms`
      : '—'
  );

  const addNodeUri = $derived.by(() => {
    if (addMode === 'advanced') return addUri.trim();
    const host = sshHost.trim();
    const port = Number(sshPort);
    if (
      !host ||
      !/^[A-Za-z0-9.:-]+$/.test(host) ||
      !Number.isInteger(port) ||
      port < 1 ||
      port > 65535
    ) {
      return '';
    }
    const uriHost = host.includes(':') && !host.startsWith('[') ? `[${host}]` : host;
    if (addBackend === 'incus') return `https://${uriHost}:${port}`;
    const user = sshUser.trim();
    if (!/^[A-Za-z_][A-Za-z0-9_.-]{0,31}$/.test(user)) return '';
    return `qemu+ssh://${user}@${uriHost}${port === 22 ? '' : `:${port}`}/system`;
  });
  const canInstallOverSSH = $derived.by(() => {
    if (addBackend !== 'libvirt') return false;
    const match = addNodeUri.match(/^qemu\+ssh:\/\/([^/]+)\/system$/);
    if (!match) return false;
    const authority = match[1];
    const at = authority.indexOf('@');
    const user = authority.slice(0, at);
    return at > 0 && /^[A-Za-z_][A-Za-z0-9_.-]{0,31}$/.test(user);
  });
  const tcpUriSelected = $derived(
    addMode === 'advanced' && addBackend === 'libvirt' && /^qemu\+tcp:\/\//i.test(addUri.trim())
  );
  // Reachability is computed from ENABLED nodes only: disabled and unknown
  // nodes would otherwise count as offline (or as reachable).
  const reachText = $derived.by(() => {
    const c = healthCounts;
    if (c.enabled === 0 || c.unknown === c.enabled) return t('nodes.summaryNoData');
    const offline = c.offline + c.degraded;
    const parts = [];
    if (offline > 0) parts.push(t('nodes.summaryOfflineCount', { count: offline }));
    if (c.no_access > 0) parts.push(t('nodeSelf.summaryNoAccess', { count: c.no_access }));
    return parts.length ? parts.join(' · ') : t('nodes.summaryAllReachable', { total: c.enabled });
  });
  const invalidEasyConnection = $derived(
    addMode === 'easy' &&
      (addBackend === 'incus'
        ? !!sshHost.trim() && !addNodeUri
        : !!(sshHost.trim() || sshUser.trim()) && !addNodeUri)
  );
  const currentTestConfig = $derived(
    JSON.stringify([
      addBackend,
      addNodeUri,
      addBackend === 'incus' ? incusProject.trim() : '',
      addBackend === 'incus' ? incusTrustToken.trim() : '',
    ])
  );

  // Inline edit
  let editingId = $state('');
  let editName = $state('');
  let editUri = $state('');
  let editUiUrl = $state('');
  let editSaving = $state(false);
  let editError = $state('');

  // Removing the self-duplicate entry: always an explicit, confirmed action.
  let selfDeleteNode = $state(null);
  let selfDeleteOpen = $state(false);
  let selfDeleteLoading = $state(false);

  function askRemoveSelfDuplicate(n) {
    selfDeleteNode = n;
    selfDeleteOpen = true;
  }

  async function removeSelfDuplicate() {
    if (!selfDeleteNode || !auth.isAdmin()) return;
    selfDeleteLoading = true;
    try {
      await api.deleteNode(selfDeleteNode.id);
      toast.success(t('nodeSelf.removed'));
      selfDeleteOpen = false;
      selfDeleteNode = null;
      await load({ background: true });
      loadSSH();
    } catch (e) {
      toast.error(e.message);
    } finally {
      selfDeleteLoading = false;
    }
  }

  // "Set up access" on a node that answers but cannot be logged into yet.
  function openAccessFor(n) {
    activeSection = 'access';
    tick().then(() => {
      if (accessCard?.openNode) accessCard.openNode(n.id);
      else document.getElementById('cluster-access-card')?.scrollIntoView?.({ block: 'start' });
    });
  }

  let confirmDeleteOpen = $state(false);
  let confirmDeleteNode = $state(null);
  let confirmDeleteLoading = $state(false);

  async function load({ background = false } = {}) {
    const seq = ++loadSeq;
    // Full-page spinner only while there is nothing to show yet; later
    // refreshes keep the previous cards and show a subtle busy state.
    if (background && nodes.length > 0) refreshing = true;
    else loading = true;
    error = '';
    try {
      const [resNodes, resSummary, resCluster] = await Promise.all([
        api.listNodes(),
        api.getClusterSummary().catch(() => null),
        api.getIncusClusterMembers().catch(() => null),
      ]);
      if (seq !== loadSeq) return;
      nodes = resNodes.nodes || resNodes || [];
      summary = resSummary;
      incusCluster = resCluster;
    } catch (e) {
      if (seq !== loadSeq) return;
      error = t('nodes.errorLoading', { error: e.message });
    } finally {
      if (seq === loadSeq) {
        loading = false;
        refreshing = false;
      }
    }
  }

  async function pollNodes() {
    const seq = ++loadSeq;
    try {
      const resNodes = await api.listNodes();
      const resSummary = await api.getClusterSummary().catch(() => summary);
      const resCluster = await api.getIncusClusterMembers().catch(() => incusCluster);
      if (seq !== loadSeq) return;
      nodes = resNodes.nodes || resNodes || [];
      summary = resSummary;
      incusCluster = resCluster;
      // A node added/removed elsewhere (another tab or admin): refresh the
      // Cluster access card too.
      const sshIds = new Set((sshOverview?.nodes || []).map((x) => x.id));
      const remoteKvm = nodes.filter((n) => n.type !== 'local' && n.backend !== 'incus');
      if (
        sshOverview &&
        (remoteKvm.some((n) => !sshIds.has(n.id)) ||
          [...sshIds].some((id) => !nodes.some((n) => n.id === id)))
      ) {
        loadSSH();
      }
    } catch {
      // silent background polling
    }
  }

  async function handlePingAll() {
    pingingAll = true;
    let ok = false;
    try {
      const res = await api.pingAllNodes();
      loadSeq++; // invalidate any slower list request still in flight
      if (res.nodes) nodes = res.nodes;
      if (res.summary) summary = res.summary;
      ok = true;
      const outcome = pingAllOutcome(res.nodes || nodes);
      if (outcome.kind === 'none') toast.info(t('nodes.pingAllNoNodes'));
      else if (outcome.kind === 'ok') toast.success(t('nodes.pingAllDone', outcome));
      else toast.warning(t('nodes.pingAllDone', outcome));
    } catch (e) {
      toast.error(e.message);
    } finally {
      pingingAll = false;
    }
    return ok;
  }

  async function handlePingSingle(n) {
    pingingNode[n.id] = true;
    try {
      const updated = await api.pingNode(n.id);
      loadSeq++;
      nodes = nodes.map((item) => (item.id === n.id ? updated : item));
      if (
        updated.status === 'online' &&
        (isSshPortOnly(updated) || nodeHealth(updated) === 'no_access')
      ) {
        // Only the SSH port answered: say so, and say whether access works.
        const ready =
          nodeHealth(updated) !== 'no_access' && accessState(sshNodeOf(updated)) === 'ready';
        if (ready) {
          toast.success(t('nodes.pingSuccess', { name: n.name, latency: updated.latency_ms }));
        } else {
          toast.warning(t('nodes.pingSshPortOnly', { name: n.name, latency: updated.latency_ms }));
        }
      } else if (updated.status === 'online') {
        toast.success(t('nodes.pingSuccess', { name: n.name, latency: updated.latency_ms }));
      } else {
        toast.error(
          t('nodes.pingError', {
            name: n.name,
            error: updated.last_error || t('nodes.statusOffline'),
          })
        );
      }
      summary = await api.getClusterSummary().catch(() => summary);
    } catch (e) {
      toast.error(t('nodes.pingError', { name: n.name, error: e.message }));
    } finally {
      pingingNode[n.id] = false;
    }
  }

  let destroyed = false;
  async function loadSSH() {
    if (!auth.isAdmin()) return;
    sshLoading = true;
    sshError = '';
    try {
      const ov = await api.getClusterSSH();
      if (destroyed) return;
      sshOverview = ov;
    } catch (e) {
      if (destroyed) return;
      sshError = e?.message || String(e);
    } finally {
      sshLoading = false;
    }
  }

  // The SSH overview row with the node-list probe verdict overlaid: one
  // source of "access works" for the card badge, toasts and Cluster access.
  const sshNodeOf = (n) =>
    withProbeAccess(sshOverview?.nodes?.find((x) => x.id === n.id) || null, n);
  const showAccessBadge = (n) =>
    auth.isAdmin() && n.backend !== 'incus' && n.type !== 'local' && !!sshNodeOf(n);

  async function quickTestAccess(n) {
    accessTesting = { ...accessTesting, [n.id]: true };
    try {
      const r = await api.testNodeSSH(n.id);
      const result = r?.ok
        ? t('clusterAccess.resultOk')
        : codeLabel(t, 'clusterAccess.codes', r?.code, 'clusterAccess.unknownCode');
      (r?.ok ? toast.success : toast.error)(
        t('clusterAccess.shortcutTested', { name: n.name, result })
      );
      await loadSSH();
    } catch (e) {
      toast.error(sshErrorText(t, e));
    } finally {
      accessTesting = { ...accessTesting, [n.id]: false };
    }
  }

  // Unmanaged-VM count for the overview strip. Best effort: the strip
  // still renders (with 0) when the listing is not available.
  async function loadFleetVms() {
    try {
      const list = await api.listVMs();
      if (destroyed) return;
      fleetVms = Array.isArray(list) ? list : [];
    } catch {
      fleetVms = [];
    }
  }

  onMount(() => {
    load();
    loadSSH();
    loadFleetVms();
    // Deep link from VM pages (?focus=access): bring the access card into view.
    if (getRoute().query?.focus === 'access') {
      setTimeout(
        () => document.getElementById('cluster-access-card')?.scrollIntoView?.({ block: 'start' }),
        300
      );
    }
    const interval = setInterval(pollNodes, 20000);
    return () => {
      clearInterval(interval);
      // Leaving the page: requests still in flight drop their result
      // (state written after unmount and read through a $derived is
      // Svelte's `derived_inert`).
      destroyed = true;
      loadSeq++;
    };
  });

  function openAdd() {
    addName = '';
    addBackend = 'libvirt';
    addMode = 'easy';
    sshHost = '';
    sshUser = '';
    sshPort = '22';
    addUri = '';
    addUiUrl = '';
    incusProject = 'default';
    incusTrustToken = '';
    testedUri = '';
    testResult = null;
    fingerprintVerified = false;
    addError = '';
    allowSystemUpgrade = false;
    showAdd = true;
    // The form now sits right under the header; bring it into view and put
    // the cursor in its first field (keyboard / screen-reader users).
    tick().then(() => {
      const reduce = window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches;
      addCardEl?.scrollIntoView?.({ block: 'start', behavior: reduce ? 'auto' : 'smooth' });
      document.getElementById('node-add-name')?.focus();
    });
  }

  function closeAdd() {
    showAdd = false;
    tick().then(() => addButtonEl?.focus());
  }

  function invalidateNodeTest() {
    testedUri = '';
    testResult = null;
    fingerprintVerified = false;
    addError = '';
    allowSystemUpgrade = false;
  }

  function setAddMode(mode) {
    // Carry the address built in easy mode into the advanced field so the
    // user does not lose it. A previous connection test stays valid only if
    // the resulting config is identical (checked via currentTestConfig).
    if (mode === 'advanced' && addMode === 'easy' && addNodeUri) addUri = addNodeUri;
    addMode = mode;
    addError = '';
  }

  function setAddBackend(backend) {
    addBackend = backend;
    sshPort = backend === 'incus' ? '8443' : '22';
    invalidateNodeTest();
  }

  async function testNodeConnection() {
    const uri = addNodeUri;
    const config = currentTestConfig;
    const backend = addBackend;
    const project = backend === 'incus' ? incusProject.trim() : '';
    const trustToken = backend === 'incus' ? incusTrustToken.trim() : '';
    if (!uri) return (addError = t('nodes.uriRequired'));
    if (backend === 'incus' && (!project || !trustToken)) {
      return (addError = t('nodes.incusTokenRequired'));
    }
    testingNode = true;
    testedUri = '';
    testResult = null;
    fingerprintVerified = false;
    addError = '';
    try {
      const result = await api.testNode(uri, backend, project, trustToken);
      if (config !== currentTestConfig) return false;
      testResult = result;
      testedUri = config;
      return true;
    } catch (e) {
      addError = e.message;
      return false;
    } finally {
      testingNode = false;
    }
  }

  async function createNode() {
    if (!addName.trim()) return (addError = t('nodes.nameRequired'));
    if (!addNodeUri) return (addError = t('nodes.uriRequired'));
    const addUi = validateUIUrl(addBackend === 'libvirt' ? addUiUrl : '');
    if (!addUi.ok) return (addError = t(`nodes.uiUrlErrors.${addUi.error}`));
    if (!(testResult?.ok || testResult?.setup_pending) || testedUri !== currentTestConfig) {
      return (addError = t('nodes.testRequired'));
    }
    if (addBackend === 'incus' && !fingerprintVerified) {
      return (addError = t('nodes.fingerprintRequired'));
    }
    addSaving = true;
    addError = '';
    try {
      await api.createNode(
        addName.trim(),
        addNodeUri,
        addBackend,
        addBackend === 'incus' ? incusProject.trim() : '',
        addBackend === 'incus' ? incusTrustToken.trim() : '',
        addUi.value
      );
      toast.success(t('nodes.nodeCreated'));
      incusTrustToken = '';
      closeAdd();
      // One round trip: ping-all returns the refreshed list (including the
      // new node). Only fall back to a plain reload if it failed.
      if (!(await handlePingAll())) await load({ background: true });
      loadSSH();
    } catch (e) {
      const known = e?.status === 409 && !e?.code ? t('apiErrors.nodeExists') : e.message;
      if (addBackend === 'incus') {
        incusTrustToken = '';
        testedUri = '';
        testResult = null;
        addError = t('nodes.incusEnrollRetry', { error: known });
      } else {
        addError = known;
      }
    } finally {
      addSaving = false;
    }
  }

  async function downloadNodeInstaller() {
    downloadingInstaller = true;
    try {
      const { script } = await api.getNodeDependencyInstaller();
      const url = URL.createObjectURL(new Blob([script], { type: 'text/x-shellscript' }));
      const link = document.createElement('a');
      link.href = url;
      link.download = 'webkvm-libvirt-node-deps.sh';
      document.body.appendChild(link);
      link.click();
      link.remove();
      const revokeURL = URL.revokeObjectURL.bind(URL);
      setTimeout(() => revokeURL(url), 1000);
      toast.success(t('nodes.installerDownloaded'));
    } catch (e) {
      toast.error(e.message);
    } finally {
      downloadingInstaller = false;
    }
  }

  async function toggleIncusInventory(n) {
    if (incusInventory[n.id]) {
      incusInventory = { ...incusInventory, [n.id]: null };
      return;
    }
    loadingIncusInventory = { ...loadingIncusInventory, [n.id]: true };
    incusInventoryErrors = { ...incusInventoryErrors, [n.id]: '' };
    try {
      const data = await api.getNodeIncusInventory(n.id);
      incusInventory = { ...incusInventory, [n.id]: data };
    } catch (e) {
      incusInventoryErrors = { ...incusInventoryErrors, [n.id]: e.message };
    } finally {
      loadingIncusInventory = { ...loadingIncusInventory, [n.id]: false };
    }
  }

  async function checkHAReadiness(opts) {
    if (!clusterModule.enabled) return;
    // silent refreshes (after standby / fencing test) keep the panel mounted
    // so its result messages stay visible.
    const silent = opts?.silent === true;
    if (!silent) loadingHAReadiness = true;
    haReadinessError = '';
    try {
      haReadiness = await api.getHAReadiness();
      incusHealing = await api.getIncusHealing().catch(() => null);
      // Never overwrite a value the user is editing.
      if (!healingDirty && incusHealing?.threshold_seconds > 0) {
        healingSeconds = incusHealing.threshold_seconds;
      }
    } catch (e) {
      haReadinessError = e.message;
    } finally {
      if (!silent) loadingHAReadiness = false;
    }
  }

  function requestSaveIncusHealing() {
    healingError = '';
    healingBlockers = [];
    if (!healingSecondsValid(healingSeconds)) {
      healingError = t('nodes.healingInvalid');
      return;
    }
    // Enabling (or changing a positive threshold) triggers automatic
    // evacuation: ask first. Turning it off needs no confirmation.
    if (Number(healingSeconds) > 0) healingConfirmOpen = true;
    else saveIncusHealing();
  }

  async function saveIncusHealing() {
    savingHealing = true;
    healingError = '';
    healingBlockers = [];
    try {
      incusHealing = await api.setIncusHealing(Number(healingSeconds));
      healingDirty = false;
      healingConfirmOpen = false;
      toast.success(t('nodes.incusHealingSaved'));
    } catch (e) {
      healingConfirmOpen = false;
      if (e?.code === 'incus_healing_blocked' && Array.isArray(e?.data?.blockers)) {
        healingBlockers = e.data.blockers;
      } else {
        healingError = e.message;
        toast.error(e.message);
      }
    } finally {
      savingHealing = false;
    }
  }

  function haBlockerText(blocker) {
    return codeText(t, 'nodes.haBlockers', blocker, 'nodes.haBlockerUnknownCode');
  }

  function haWarningText(warning) {
    return codeText(t, 'nodes.haWarnings', warning, 'nodes.haWarningUnknownCode');
  }

  async function copyFenceCommands() {
    const text = (haReadiness?.fencing?.pve?.commands || []).join('\n');
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      toast.success(t('nodes.pveFenceCopied'));
    } catch {
      toast.error(t('nodes.pveFenceCopyFailed'));
    }
  }

  async function verifyFenceToken() {
    pveFenceVerifying = true;
    pveFenceResult = null;
    try {
      pveFenceResult = await api.verifyFencePVE();
    } catch (e) {
      pveFenceResult = { valid: false, power_mgmt: false, message: e.message };
    } finally {
      pveFenceVerifying = false;
    }
  }

  async function installNodeDependencies() {
    if (!canInstallOverSSH) return;
    installingNode = true;
    addError = '';
    testedUri = '';
    testResult = null;
    try {
      await api.installNodeDependencies(addNodeUri, allowSystemUpgrade);
      installConfirmOpen = false;
      toast.success(t('nodes.installerComplete'));
      await testNodeConnection();
    } catch (e) {
      addError = e.message;
      installConfirmOpen = false;
    } finally {
      installingNode = false;
    }
  }

  function startEdit(n) {
    editingId = n.id;
    editName = n.name;
    editUri = n.uri;
    editUiUrl = n.ui_url || '';
    editError = '';
  }

  function cancelEdit() {
    editingId = '';
  }

  async function saveEdit(n) {
    if (!editName.trim()) return (editError = t('nodes.nameRequired'));
    const editUi = validateUIUrl(editUiUrl);
    if (!editUi.ok) return (editError = t(`nodes.uiUrlErrors.${editUi.error}`));
    editSaving = true;
    editError = '';
    try {
      const body = { name: editName.trim() };
      if (n.type !== 'local') body.uri = editUri.trim();
      // "" clears the address; omitted keeps it. Incus nodes have no WebKVM UI.
      if (n.type !== 'local' && n.backend !== 'incus') body.ui_url = editUi.value;
      await api.updateNode(n.id, body);
      toast.success(t('nodes.nodeUpdated'));
      editingId = '';
      await load();
      loadSSH();
    } catch (e) {
      editError = e.message;
    } finally {
      editSaving = false;
    }
  }

  async function toggleEnabled(n) {
    if (togglingNode[n.id]) return;
    togglingNode[n.id] = true;
    try {
      await api.updateNode(n.id, { enabled: !n.enabled });
      await load({ background: true });
      loadSSH();
    } catch (e) {
      toast.error(e.message);
    } finally {
      togglingNode[n.id] = false;
    }
  }

  function askDelete(n) {
    confirmDeleteNode = n;
    confirmDeleteOpen = true;
  }

  async function doDelete({ force = false } = {}) {
    if (!confirmDeleteNode) return;
    confirmDeleteLoading = true;
    try {
      await api.deleteNode(confirmDeleteNode.id, { force });
      toast.success(t('nodes.nodeDeleted'));
      confirmDeleteOpen = false;
      forceDeleteOpen = false;
      confirmDeleteNode = null;
      await load({ background: true });
      loadSSH();
    } catch (e) {
      // The remote Incus server refused the certificate revoke: offer an
      // explicit second confirmation to remove the node anyway.
      if (!force && confirmDeleteNode?.backend === 'incus' && e?.code === 'incus_revoke_failed') {
        confirmDeleteOpen = false;
        forceDeleteOpen = true;
      } else {
        toast.error(e.message);
      }
    } finally {
      confirmDeleteLoading = false;
    }
  }

  function fmtDate(iso) {
    if (!iso) return '—';
    try {
      return new Date(iso).toLocaleString();
    } catch {
      return iso;
    }
  }

  function fmtUptime(sec) {
    if (!sec || sec <= 0) return '—';
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    if (d > 0) return `${d}d ${h}h`;
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
  }

  // ui_url first (the operator's real network), then the URI host only when
  // it is not private; null disables the button.
  const getWebConsoleURL = (n) => nodeConsoleURL(n);

  const tabClass = (active) =>
    `inline-flex min-h-9 cursor-pointer items-center gap-1.5 whitespace-nowrap rounded-md px-3 py-1.5 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/50 ${
      active
        ? 'bg-accent/10 text-accent'
        : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground'
    }`;

  async function copyAddress(text) {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(t('nodes.addressCopied'));
    } catch {
      toast.error(t('nodes.haCompat.copyFailed'));
    }
  }
</script>

<div class="p-4 sm:p-6 w-full max-w-6xl mx-auto flex flex-col gap-6">
  <PageHeader title={t('nodes.title')} subtitle={t('nodes.subtitle')}>
    {#snippet actions()}
      <div class="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          variant="outline"
          class="!h-8 !text-xs gap-1.5"
          onclick={handlePingAll}
          disabled={pingingAll || loading}
        >
          <Icon name="activity" size={13} class={pingingAll ? 'animate-spin' : ''} />
          {pingingAll ? t('nodes.pinging') : t('nodes.pingAllBtn')}
        </Button>

        {#if auth.isAdmin() && !showAdd}
          <Button size="sm" class="!h-8 !text-xs" onclick={openAdd} bind:ref={addButtonEl}>
            <Icon name="plus" size={13} class="mr-1.5" />
            {t('nodes.addNode')}
          </Button>
        {/if}
      </div>
    {/snippet}
  </PageHeader>

  <div
    role="tablist"
    aria-label={t('nodes.sectionNav')}
    class="flex items-center gap-1 overflow-x-auto rounded-lg border border-border bg-card/50 p-1"
  >
    <button
      type="button"
      role="tab"
      id="nodes-fleet-tab"
      aria-controls="nodes-fleet"
      aria-selected={activeSection === 'fleet'}
      class={tabClass(activeSection === 'fleet')}
      onclick={() => (activeSection = 'fleet')}
    >
      {t('nodes.sectionFleet')}
      <span class="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px]">{visibleNodes.length}</span
      >
    </button>
    {#if auth.isAdmin() && !loadFailed}
      <button
        type="button"
        role="tab"
        id="nodes-access-tab"
        aria-controls="nodes-access"
        aria-selected={activeSection === 'access'}
        class={tabClass(activeSection === 'access')}
        onclick={() => (activeSection = 'access')}
      >
        {t('nodes.sectionAccess')}
        {#if noAccessCount}
          <span class="rounded bg-warning/15 px-1.5 py-0.5 text-[10px] font-medium text-warning"
            >{noAccessCount}</span
          >
        {/if}
      </button>
    {/if}
    <button
      type="button"
      role="tab"
      id="nodes-ha-tab"
      aria-controls="nodes-ha"
      aria-selected={activeSection === 'cluster'}
      class={tabClass(activeSection === 'cluster')}
      onclick={() => (activeSection = 'cluster')}
    >
      <span
        class="h-1.5 w-1.5 rounded-full {clusterModule.enabled &&
        clusterModule.status?.state === 'installed'
          ? 'bg-success'
          : 'bg-muted-foreground/40'}"
        aria-hidden="true"
      ></span>
      {t('nodes.sectionCluster')}
    </button>
    {#if incusMembers.length}
      <button
        type="button"
        role="tab"
        id="nodes-incus-tab"
        aria-controls="nodes-incus"
        aria-selected={activeSection === 'incus'}
        class={tabClass(activeSection === 'incus')}
        onclick={() => (activeSection = 'incus')}
      >
        {t('nodes.sectionIncus')}
        <span class="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px]"
          >{incusMembers.length}</span
        >
      </button>
    {/if}
  </div>

  {#if error}
    <Alert variant="error">
      <p>{error}</p>
      <Button
        size="sm"
        variant="outline"
        class="mt-2 min-h-11"
        onclick={() => load()}
        disabled={loading}
      >
        {#if loading}<Spinner size="sm" />{/if}
        {t('nodes.errorRetry')}
      </Button>
    </Alert>
  {/if}

  {#if showAdd}
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      bind:this={addCardEl}
      class="scroll-mt-4"
      data-testid="add-node-card"
      onkeydown={(e) => {
        if (e.key === 'Escape' && !addSaving) closeAdd();
      }}
    >
      <Card class="p-4 space-y-3 border border-border bg-card">
        <div class="text-xs font-semibold text-muted-foreground uppercase tracking-wider">
          {t('nodes.addNode')}
        </div>
        {#if addError}
          <Alert variant="error">{addError}</Alert>
        {/if}
        <div class="space-y-1">
          <span class="text-xs text-muted-foreground">{t('nodes.backend')}</span>
          <div class="flex flex-wrap gap-2" role="group" aria-label={t('nodes.backend')}>
            <Button
              size="sm"
              variant={addBackend === 'libvirt' ? 'default' : 'outline'}
              class="min-h-11"
              aria-pressed={addBackend === 'libvirt'}
              onclick={() => setAddBackend('libvirt')}
            >
              {t('nodes.kvmBackend')}
            </Button>
            <Button
              size="sm"
              variant={addBackend === 'incus' ? 'default' : 'outline'}
              class="min-h-11"
              aria-pressed={addBackend === 'incus'}
              onclick={() => setAddBackend('incus')}
            >
              {t('nodes.incusBackend')}
            </Button>
          </div>
        </div>
        <div class="flex flex-wrap gap-2" role="group" aria-label={t('nodes.connectionMode')}>
          <Button
            size="sm"
            variant={addMode === 'easy' ? 'default' : 'outline'}
            class="min-h-11"
            aria-pressed={addMode === 'easy'}
            onclick={() => setAddMode('easy')}
          >
            {t('nodes.easyMode')}
          </Button>
          <Button
            size="sm"
            variant={addMode === 'advanced' ? 'default' : 'outline'}
            class="min-h-11"
            aria-pressed={addMode === 'advanced'}
            onclick={() => setAddMode('advanced')}
          >
            {t('nodes.advancedMode')}
          </Button>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label class="text-xs text-muted-foreground" for="node-add-name"
              >{t('common.name')}</label
            >
            <Input
              id="node-add-name"
              bind:value={addName}
              placeholder={t('nodes.namePlaceholder')}
              class="w-full min-h-11 mt-1"
            />
          </div>
          {#if addMode === 'easy'}
            <div>
              <label class="text-xs text-muted-foreground" for="node-add-host"
                >{t('nodes.host')}</label
              >
              <Input
                id="node-add-host"
                bind:value={sshHost}
                placeholder={t('nodes.hostPlaceholder')}
                class="w-full min-h-11 mt-1"
              />
            </div>
            {#if addBackend === 'libvirt'}
              <div>
                <label class="text-xs text-muted-foreground" for="node-add-user"
                  >{t('nodes.sshUser')}</label
                >
                <Input
                  id="node-add-user"
                  bind:value={sshUser}
                  placeholder={t('nodes.userPlaceholder')}
                  class="w-full min-h-11 mt-1"
                />
              </div>
            {/if}
            <div>
              <label class="text-xs text-muted-foreground" for="node-add-port"
                >{addBackend === 'incus' ? t('nodes.httpsPort') : t('nodes.sshPort')}</label
              >
              <Input
                id="node-add-port"
                type="number"
                min="1"
                max="65535"
                bind:value={sshPort}
                class="w-full min-h-11 mt-1"
              />
            </div>
          {:else}
            <div class="sm:col-span-2">
              <label class="text-xs text-muted-foreground" for="node-add-uri"
                >{t('nodes.uri')}</label
              >
              <Input
                id="node-add-uri"
                bind:value={addUri}
                placeholder={addBackend === 'incus'
                  ? t('nodes.incusUriPlaceholder')
                  : t('nodes.uriPlaceholder')}
                class="w-full min-h-11 mt-1 font-mono text-xs"
              />
            </div>
          {/if}
          {#if addBackend === 'libvirt'}
            <div class="sm:col-span-2">
              <NodeUiUrlField id="node-add-ui-url" bind:value={addUiUrl} nodeUri={addNodeUri} />
            </div>
          {/if}
          {#if addBackend === 'incus'}
            <div>
              <label class="text-xs text-muted-foreground" for="node-incus-project"
                >{t('nodes.incusProject')}</label
              >
              <Input
                id="node-incus-project"
                bind:value={incusProject}
                placeholder="default"
                class="w-full min-h-11 mt-1"
              />
            </div>
            <div class="sm:col-span-2">
              <label class="text-xs text-muted-foreground" for="node-incus-token"
                >{t('nodes.incusTrustToken')}</label
              >
              <Input
                id="node-incus-token"
                type="password"
                autocomplete="off"
                bind:value={incusTrustToken}
                class="w-full min-h-11 mt-1 font-mono text-xs"
              />
              <p class="mt-1 text-xs text-muted-foreground">{t('nodes.incusTokenHelper')}</p>
            </div>
          {/if}
        </div>
        {#if tcpUriSelected}
          <Alert variant="warning">
            <p class="font-medium">{t('nodes.tcpWarningTitle')}</p>
            <p>{t('nodes.tcpWarningBody')}</p>
          </Alert>
        {/if}
        {#if invalidEasyConnection}
          <Alert variant="error">
            {addBackend === 'incus'
              ? t('nodes.invalidIncusEasyConnection')
              : t('nodes.invalidEasyConnection')}
          </Alert>
        {/if}
        <p class="text-xs text-muted-foreground">
          {addBackend === 'incus' ? t('nodes.incusUriHelper') : t('nodes.uriHelper')}
        </p>
        <p class="text-xs text-muted-foreground">
          {addBackend === 'incus' ? t('nodes.incusPrereq') : t('nodes.nodeStoragePrereq')}
        </p>
        {#if addNodeUri}
          <p
            class="break-all rounded-md border border-border bg-muted/30 p-2 font-mono text-xs"
            aria-label={t('nodes.generatedURI')}
          >
            {addNodeUri}
          </p>
        {/if}
        <div class="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            class="min-h-11"
            onclick={testNodeConnection}
            disabled={addSaving || testingNode || installingNode || tcpUriSelected}
          >
            {#if testingNode}<Spinner size="sm" />{/if}
            {testingNode
              ? addBackend === 'incus'
                ? t('nodes.testingIncusConnection')
                : t('nodes.testingConnection')
              : t('nodes.testConnection')}
          </Button>
          <Button
            size="sm"
            class="min-h-11"
            onclick={createNode}
            disabled={addSaving ||
              testingNode ||
              installingNode ||
              !addName.trim() ||
              !(testResult?.ok || testResult?.setup_pending) ||
              testedUri !== currentTestConfig ||
              tcpUriSelected ||
              (addBackend === 'incus' && !fingerprintVerified)}
          >
            {#if addSaving}<Spinner size="sm" color="text-white" />{/if}
            {t('common.save')}
          </Button>
          <Button size="sm" variant="outline" class="min-h-11" onclick={closeAdd}>
            {t('common.cancel')}
          </Button>
        </div>
        {#if testResult?.setup_pending && testedUri === currentTestConfig}
          <Alert variant="warning">
            {t('nodes.setupPending')}
          </Alert>
        {/if}
        {#if addNodeUri && !testResult?.ok && testedUri !== currentTestConfig && !addError}
          <p class="text-xs text-muted-foreground">{t('nodes.testBeforeSave')}</p>
        {/if}
        {#if testResult?.ok && testedUri === currentTestConfig}
          {#if addBackend === 'incus'}
            <Alert variant="success">
              {t('nodes.incusTestSuccess', {
                name: testResult.server?.name || addNodeUri,
                version: testResult.server?.version || '',
              })}
            </Alert>
            <p class="break-all text-xs text-muted-foreground">
              {t('nodes.incusFingerprint')}:
              <span class="font-mono">{testResult.server_fingerprint}</span>
            </p>
            <label class="flex min-h-11 items-start gap-2 text-xs">
              <input
                type="checkbox"
                bind:checked={fingerprintVerified}
                class="mt-0.5 h-4 w-4"
                data-testid="fingerprint-verified"
              />
              <span>{t('nodes.fingerprintVerify')}</span>
            </label>
            <p class="text-xs text-muted-foreground">{t('nodes.fingerprintVerifyHint')}</p>
            {#if !fingerprintVerified}
              <p class="text-xs text-warning">{t('nodes.fingerprintRequired')}</p>
            {/if}
          {:else}
            <Alert variant="success">{t('nodes.testSuccess', { count: testResult.domains })}</Alert>
          {/if}
          {#if addBackend === 'libvirt' && testResult.kvm_available === false}
            <Alert variant="warning">{t('nodes.kvmUnavailable')}</Alert>
          {/if}
        {/if}

        {#if addNodeUri && addBackend === 'libvirt'}
          <div class="space-y-2 rounded-lg border border-border bg-card/50 p-3">
            <div class="font-medium text-sm">{t('nodes.dependenciesTitle')}</div>
            <p class="text-xs text-muted-foreground">{t('nodes.sshInstallPrerequisites')}</p>
            <div class="flex flex-wrap gap-2">
              <Button
                size="sm"
                variant="outline"
                class="min-h-11"
                onclick={downloadNodeInstaller}
                disabled={downloadingInstaller || installingNode}
              >
                {#if downloadingInstaller}<Spinner size="sm" />{/if}
                {t('nodes.downloadInstaller')}
              </Button>
              {#if canInstallOverSSH}
                <Button
                  size="sm"
                  variant="outline"
                  class="min-h-11"
                  onclick={() => (installConfirmOpen = true)}
                  disabled={downloadingInstaller || installingNode}
                >
                  {t('nodes.installBySSH')}
                </Button>
              {:else}
                <span class="self-center text-xs text-muted-foreground"
                  >{t('nodes.installManualOnly')}</span
                >
              {/if}
            </div>
            {#if canInstallOverSSH}
              <label class="flex min-h-11 items-center gap-2 text-xs cursor-pointer select-none">
                <input
                  type="checkbox"
                  bind:checked={allowSystemUpgrade}
                  class="w-4 h-4 rounded border-border"
                />
                <span>{t('nodes.allowArchUpgrade')}</span>
              </label>
            {/if}
            <p class="text-xs text-muted-foreground">{t('nodes.manualInstallerHelper')}</p>
          </div>
        {/if}
      </Card>
    </div>
  {/if}

  {#if !loadFailed && selfDups.length}
    <div
      role="status"
      class="rounded-md border border-warning/30 bg-warning/10 p-3 text-sm text-foreground"
      data-testid="self-duplicate-notice"
    >
      <p class="font-semibold text-warning">{t('nodeSelf.dupTitle')}</p>
      <ul class="mt-1 space-y-2">
        {#each selfDups as d (d.id)}
          <li class="flex flex-wrap items-center gap-2">
            <span class="min-w-0 break-all"
              >{t('nodeSelf.dupBody', { name: d.name, uri: d.uri || '' })}</span
            >
            {#if auth.isAdmin()}
              <Button
                size="sm"
                variant="outline"
                class="min-h-9"
                aria-label={t('nodeSelf.removeDuplicateAria', { name: d.name })}
                onclick={() => askRemoveSelfDuplicate(d)}
              >
                {t('nodeSelf.removeDuplicate')}
              </Button>
            {/if}
          </li>
        {/each}
      </ul>
      <p class="mt-1 text-xs text-muted-foreground">
        {auth.isAdmin() ? t('nodeSelf.dupAdminHint') : t('nodeSelf.dupViewerHint')}
      </p>
    </div>
  {/if}

  {#if !loadFailed}
    {#if activeSection === 'fleet'}
      <!-- Summary: one surface, four scannable figures. -->
      <Card
        class="overflow-hidden border border-border bg-card/60 {error ? 'opacity-60' : ''}"
        data-testid="summary-cards"
      >
        <div class="grid grid-cols-2 lg:grid-cols-4">
          <div
            data-testid="summary-total"
            class="border-b border-border p-4 sm:p-5 lg:border-b-0 lg:border-r"
          >
            <p class="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
              {t('nodes.summaryTotal')}
            </p>
            <p class="mt-1.5 font-mono text-2xl font-semibold text-foreground">{totalNodes}</p>
            {#if disabledCount > 0}
              <p class="mt-1 text-xs text-muted-foreground" data-testid="disabled-hint">
                {t('nodes.summaryDisabledHint', { count: disabledCount })}
              </p>
            {/if}
          </div>
          <div
            data-testid="summary-online"
            class="border-b border-border p-4 sm:p-5 lg:border-b-0 lg:border-r"
          >
            <p class="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
              {t('nodes.summaryOnline')}
            </p>
            <p class="mt-1.5 flex items-center gap-2 font-mono text-2xl font-semibold text-success">
              {onlineCount}
              <span class="h-2 w-2 rounded-full bg-success" aria-hidden="true"></span>
            </p>
            <p class="mt-1 text-xs text-muted-foreground">{reachText}</p>
            {#if incusMembers.length}
              <p class="mt-1 text-xs text-muted-foreground">
                {t('nodes.summaryIncusMembers', {
                  online: incusOnline,
                  total: incusMembers.length,
                })}
              </p>
            {/if}
          </div>
          <div
            data-testid="summary-latency"
            class="border-b border-border p-4 sm:p-5 lg:border-b-0 lg:border-r"
          >
            <p class="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
              {t('nodes.summaryAvgLatency')}
            </p>
            <p class="mt-1.5 font-mono text-2xl font-semibold text-foreground">{avgLatencyText}</p>
            <p class="mt-1 text-xs text-muted-foreground">{t('nodes.summaryRttProbe')}</p>
          </div>
          <div data-testid="summary-storage" class="p-4 sm:p-5">
            <p class="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
              {t('nodes.summaryStorage')}
            </p>
            <p class="mt-1.5 font-mono text-2xl font-semibold text-foreground">
              {summary?.total_disk_free ? formatBytes(summary.total_disk_free) : '—'}
            </p>
            <p class="mt-1 text-xs text-muted-foreground">
              {#if summary?.total_disk_total}
                {t('nodes.summaryFreeOf', { total: formatBytes(summary.total_disk_total) })}
              {:else}
                {t('nodes.summaryFleetFree')}
              {/if}
            </p>
          </div>
        </div>
        <div
          class="flex flex-wrap items-center gap-x-5 gap-y-2 border-t border-border px-4 py-3 text-xs sm:px-5"
          data-testid="cluster-overview"
        >
          <span class="inline-flex items-center gap-1.5">
            <span class="text-muted-foreground">{t('remote.overview.enabled')}</span>
            <span class="font-mono text-success" data-testid="overview-enabled"
              >{healthCounts.enabled}</span
            >
          </span>
          <span class="inline-flex items-center gap-1.5">
            <span class="text-muted-foreground">{t('nodeSelf.overviewOnline')}</span>
            <span class="font-mono text-success" data-testid="overview-online"
              >{healthCounts.online}</span
            >
          </span>
          <span class="inline-flex items-center gap-1.5">
            <span class="text-muted-foreground">{t('nodeSelf.overviewNoAccess')}</span>
            <span
              class="font-mono {healthCounts.no_access ? 'text-info' : 'text-foreground'}"
              data-testid="overview-no_access">{healthCounts.no_access}</span
            >
          </span>
          <span class="inline-flex items-center gap-1.5">
            <span class="text-muted-foreground">{t('remote.overview.degraded')}</span>
            <span
              class="font-mono {healthCounts.degraded + healthCounts.offline
                ? 'text-warning'
                : 'text-foreground'}"
              data-testid="overview-degraded">{healthCounts.degraded + healthCounts.offline}</span
            >
          </span>
          <span class="inline-flex items-center gap-1.5">
            <span class="text-muted-foreground">{t('remote.overview.disabled')}</span>
            <span class="font-mono text-muted-foreground" data-testid="overview-disabled"
              >{healthCounts.disabled}</span
            >
          </span>
          <span class="inline-flex items-center gap-1.5">
            <span class="text-muted-foreground">{t('remote.overview.unmanaged')}</span>
            <span class="font-mono text-foreground" data-testid="overview-unmanaged"
              >{unmanagedCount}</span
            >
          </span>
        </div>
      </Card>
    {/if}
    {#if incusMembers.length}
      <Card
        id="nodes-incus"
        role="tabpanel"
        aria-labelledby="nodes-incus-tab"
        class="scroll-mt-24 p-4 space-y-3 border border-border bg-card/60 {activeSection !== 'incus'
          ? 'hidden'
          : ''}"
      >
        <div class="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
          <div class="space-y-1">
            <h2 class="text-sm font-semibold text-foreground">{t('nodes.incusClusterTitle')}</h2>
            <p class="text-xs text-muted-foreground">{t('nodes.incusClusterDesc')}</p>
          </div>
          <span
            class="inline-flex w-fit items-center gap-2 text-xs font-medium {incusOnline ===
            incusMembers.length
              ? 'text-success'
              : 'text-warning'}"
          >
            <span
              class="h-2 w-2 rounded-full {incusOnline === incusMembers.length
                ? 'bg-success'
                : 'bg-warning'}"
            ></span>
            {t('nodes.incusClusterOnline', {
              online: incusOnline,
              total: incusMembers.length,
            })}
          </span>
        </div>
        <ul class="divide-y divide-border text-xs">
          {#each incusMembers as member (member.name)}
            <li
              class="flex flex-col gap-1 py-2 first:pt-0 last:pb-0 sm:flex-row sm:items-center sm:justify-between"
            >
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="font-medium text-foreground">{member.name}</span>
                  {#if member.database}
                    <span
                      class="rounded border border-border px-1.5 py-0.5 text-[10px] tracking-wide text-muted-foreground uppercase"
                      >{t('nodes.incusDatabaseBadge')}</span
                    >
                  {/if}
                  {#if member.roles?.length}
                    <span class="text-[11px] text-muted-foreground">{member.roles.join(' · ')}</span
                    >
                  {/if}
                </div>
                <p class="truncate text-muted-foreground">
                  {member.url}{member.failure_domain ? ` · ${member.failure_domain}` : ''}
                </p>
              </div>
              <div class="flex flex-col items-start gap-1.5 sm:items-end">
                <span
                  class="inline-flex w-fit items-center rounded border px-2 py-0.5 text-[11px] font-medium {member.status ===
                  'Online'
                    ? 'border-success/30 bg-success/15 text-success'
                    : member.status === 'Evacuated'
                      ? 'border-warning/30 bg-warning/15 text-warning'
                      : 'border-destructive/30 bg-destructive/15 text-destructive'}"
                >
                  {member.status === 'Online'
                    ? t('nodes.incusStatusOnline')
                    : member.status === 'Evacuated'
                      ? t('nodes.incusStatusEvacuated')
                      : t('nodes.incusStatusOffline')}
                </span>
                <IncusMemberOps {member} onDone={() => load({ background: true })} />
              </div>
            </li>
          {/each}
        </ul>
      </Card>
    {/if}
  {/if}

  <div
    id="nodes-ha"
    role="tabpanel"
    aria-labelledby="nodes-ha-tab"
    class="space-y-6 {activeSection !== 'cluster' ? 'hidden' : ''}"
  >
    <div class="space-y-1">
      <h2 class="text-base font-semibold text-foreground">{t('nodes.sectionCluster')}</h2>
      <p class="text-xs text-muted-foreground">{t('nodes.sectionClusterDesc')}</p>
    </div>
    <Alert variant="info">{t('nodes.multiHostNotice')}</Alert>
    <ClusterModuleCard onCheckReadiness={checkHAReadiness} />

    {#if !loadFailed}
      <div id="nodes-guides" class="scroll-mt-24 space-y-6">
        <h2 class="text-base font-semibold text-foreground">{t('nodes.sectionGuides')}</h2>
        <NodeInstallGuide nodes={visibleNodes} />
        <HaCompatPanel />
      </div>
    {/if}

    {#if clusterModule.enabled}
      <Card class="p-4 space-y-3 border border-border bg-card/60">
        <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div class="space-y-1">
            <h2 class="text-sm font-semibold text-foreground">{t('nodes.haTitle')}</h2>
            <p class="text-xs text-muted-foreground">{t('nodes.haDescription')}</p>
          </div>
          <Button
            size="sm"
            variant="outline"
            class="min-h-11 shrink-0"
            onclick={checkHAReadiness}
            disabled={loadingHAReadiness}
            aria-busy={loadingHAReadiness}
          >
            {#if loadingHAReadiness}<Spinner size="sm" />{/if}
            {loadingHAReadiness ? t('nodes.checkingHA') : t('nodes.checkHA')}
          </Button>
        </div>
        {#if haReadinessError}
          <Alert variant="error">{t('nodes.haReadinessError', { error: haReadinessError })}</Alert>
        {:else if loadingHAReadiness}
          <p class="text-xs text-muted-foreground" role="status">{t('nodes.loadingHA')}</p>
        {:else if !haReadiness}
          <p class="text-xs text-muted-foreground" role="status">{t('nodes.haNotChecked')}</p>
        {:else}
          {#if haReadiness.state === 'ready'}
            <Alert variant="success">{t('nodes.haActive')}</Alert>
          {:else}
            <Alert variant="warning">{t('nodes.haNotActive')}</Alert>
          {/if}
          <div class="flex flex-wrap gap-2" role="group" aria-label={t('nodes.haViewMode')}>
            <Button
              size="sm"
              variant={haViewMode === 'easy' ? 'default' : 'outline'}
              class="min-h-11"
              aria-pressed={haViewMode === 'easy'}
              onclick={() => (haViewMode = 'easy')}
            >
              {t('nodes.easyMode')}
            </Button>
            <Button
              size="sm"
              variant={haViewMode === 'advanced' ? 'default' : 'outline'}
              class="min-h-11"
              aria-pressed={haViewMode === 'advanced'}
              onclick={() => (haViewMode = 'advanced')}
            >
              {t('nodes.advancedMode')}
            </Button>
          </div>
          <div class="space-y-2" aria-live="polite">
            <p class="text-sm font-medium">
              {haReadiness.state === 'ready' ? t('nodes.haReadyTitle') : t('nodes.haBlockedTitle')}
            </p>
            {#if haReadiness.blockers.length}
              <ul class="list-disc space-y-1 pl-5 text-xs text-muted-foreground">
                {#each haReadiness.blockers as blocker, index (`${blocker}-${index}`)}
                  <li>{haBlockerText(blocker)}</li>
                {/each}
              </ul>
            {:else}
              <p class="text-xs text-muted-foreground">{t('nodes.haNoReportedBlockers')}</p>
            {/if}
            {#if haReadiness.warnings?.length}
              <div class="space-y-1 rounded border border-warning/30 bg-warning/10 p-2">
                <p class="text-xs font-medium">{t('nodes.haWarningsTitle')}</p>
                <ul class="list-disc space-y-0.5 pl-5 text-xs text-muted-foreground">
                  {#each haReadiness.warnings as warning (`${warning}`)}
                    <li>{haWarningText(warning)}</li>
                  {/each}
                </ul>
                {#each haReadiness.default_profile_nic || [] as issue, i (`${i}`)}
                  <p class="text-xs text-foreground">{issue.detail}</p>
                  <CopyCommands
                    commands={issue.commands}
                    title={t('haWorkloads.fixCommandsTitle')}
                  />
                {/each}
              </div>
            {/if}
            {#if haReadiness.drbd?.issues?.length}
              <HaDrbdRepair issues={haReadiness.drbd.issues} advanced={haViewMode === 'advanced'} />
            {/if}
            {#if haReadiness.fencing?.critical}
              <div class="space-y-2 rounded border border-destructive/40 bg-destructive/10 p-3">
                <p class="text-sm font-semibold text-destructive">
                  {t('nodes.pveFenceCriticalTitle')}
                </p>
                <p class="text-xs text-muted-foreground">{t('nodes.pveFenceCriticalDesc')}</p>
                <p class="text-xs">
                  {t('nodes.fenceAgents')}:
                  <code>{haReadiness.fencing.agents?.join(', ') || '—'}</code>
                  · {t('nodes.watchdogFencing')}:
                  {haReadiness.fencing.watchdog ? t('common.enabled') : t('common.disabled')}
                </p>
                {#if haReadiness.fencing.pve?.commands?.length}
                  <p class="text-xs font-medium">{t('nodes.pveFenceCommandsTitle')}</p>
                  <pre
                    class="rounded bg-background/80 p-2 text-[11px] leading-relaxed break-all whitespace-pre-wrap">{haReadiness.fencing.pve.commands.join(
                      '\n'
                    )}</pre>
                  <div class="flex flex-wrap gap-2">
                    <Button size="sm" variant="outline" onclick={copyFenceCommands}>
                      {t('nodes.pveFenceCopy')}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      onclick={verifyFenceToken}
                      disabled={pveFenceVerifying}
                    >
                      {t('nodes.pveFenceVerify')}
                    </Button>
                  </div>
                {:else}
                  <p class="text-xs text-muted-foreground">{t('nodes.pveFenceCheckHint')}</p>
                  {#if haReadiness.fencing.pve?.notes?.length}
                    <ul class="list-disc space-y-0.5 pl-5 text-xs text-muted-foreground">
                      {#each haReadiness.fencing.pve.notes as note (`${note}`)}
                        <li>{note}</li>
                      {/each}
                    </ul>
                  {/if}
                {/if}
                {#if pveFenceResult}
                  <p
                    class="text-xs {pveFenceResult.valid && pveFenceResult.power_mgmt
                      ? 'text-success'
                      : 'text-destructive'}"
                  >
                    {pveFenceResult.valid
                      ? pveFenceResult.power_mgmt
                        ? t('nodes.pveFenceTokenValid')
                        : t('nodes.pveFenceTokenMissing')
                      : t('nodes.pveFenceTokenInvalid')}{pveFenceResult.message
                      ? ` — ${pveFenceResult.message}`
                      : ''}
                  </p>
                {/if}
              </div>
            {/if}
            {#if haReadiness.install?.critical || haReadiness.install?.divergent_jwt}
              <div class="space-y-2 rounded border border-destructive/40 bg-destructive/10 p-3">
                <p class="text-sm font-semibold text-destructive">
                  {t('nodes.localInstallCriticalTitle')}
                </p>
                <p class="text-xs text-muted-foreground">{t('nodes.localInstallCriticalDesc')}</p>
                {#if haReadiness.install.details?.length}
                  <ul class="list-disc space-y-0.5 pl-5 text-xs text-muted-foreground">
                    {#each haReadiness.install.details as detail (`${detail}`)}
                      <li>{detail}</li>
                    {/each}
                  </ul>
                {/if}
                <p class="text-xs text-muted-foreground">{t('nodes.localInstallNextSteps')}</p>
              </div>
            {/if}
          </div>
          {#if haReadiness.prerequisites?.length}
            <div class="space-y-2 border-t border-border pt-3">
              <h3 class="text-xs font-semibold text-foreground">{t('nodes.haPrereqTitle')}</h3>
              <p class="text-xs text-muted-foreground">{t('nodes.haPrereqDesc')}</p>
              <ul class="space-y-1.5 text-xs">
                {#each haReadiness.prerequisites as prerequisite (prerequisite.id)}
                  <li class="flex items-start gap-2">
                    <span
                      class="mt-0.5 inline-flex min-w-16 justify-center rounded border px-2 py-0.5 text-[11px] font-medium {prerequisite.state ===
                      'ok'
                        ? 'border-success/30 bg-success/15 text-success'
                        : 'border-warning/30 bg-warning/15 text-warning'}"
                    >
                      {prerequisite.state === 'ok'
                        ? t('nodes.haPrereqOk')
                        : prerequisite.state === 'inactive'
                          ? t('nodes.haPrereqInactive')
                          : t('nodes.haPrereqMissing')}
                    </span>
                    <span class="text-muted-foreground"
                      >{t(`nodes.haPrereq.${prerequisite.id}`)}</span
                    >
                  </li>
                {/each}
              </ul>
              <p class="text-xs text-muted-foreground">{t('nodes.haInstallHelper')}</p>
            </div>
          {/if}
          {#if haViewMode === 'advanced'}
            <div class="grid grid-cols-1 gap-3 border-t border-border pt-3 text-xs sm:grid-cols-2">
              <div class="space-y-1">
                <h3 class="font-medium text-foreground">{t('nodes.pacemakerStatus')}</h3>
                <p>{t('nodes.clusterState')}: <code>{haReadiness.pacemaker.state}</code></p>
                <p>{t('nodes.clusterName')}: {haReadiness.pacemaker.cluster_name || '—'}</p>
                <p>
                  {t('nodes.quorum')}: {haReadiness.pacemaker.quorum == null
                    ? t('nodes.unknownState')
                    : haReadiness.pacemaker.quorum
                      ? t('common.enabled')
                      : t('common.disabled')}
                </p>
                <p>
                  {t('nodes.clusterNodes')}: {haReadiness.pacemaker.nodes_online}/{haReadiness
                    .pacemaker.nodes_configured}
                </p>
                <p>
                  {t('nodes.fencing')}: {haReadiness.pacemaker.fencing_enabled == null
                    ? t('nodes.unknownState')
                    : haReadiness.pacemaker.fencing_enabled
                      ? t('common.enabled')
                      : t('common.disabled')}
                </p>
                <p>{t('nodes.fenceDevices')}: {haReadiness.pacemaker.fence_devices}</p>
                <p>
                  {t('nodes.watchdogFencing')}: {haReadiness.pacemaker.watchdog_fencing == null
                    ? t('nodes.unknownState')
                    : haReadiness.pacemaker.watchdog_fencing
                      ? t('common.enabled')
                      : t('common.disabled')}
                </p>
                <p>
                  {t('nodes.hypervisorFencing')}: {haReadiness.fencing?.hypervisor
                    ? t('common.enabled')
                    : t('common.disabled')}
                </p>
                <p>
                  {t('nodes.fenceAgents')}:
                  <code>{haReadiness.fencing?.agents?.join(', ') || '—'}</code>
                </p>
              </div>
              <div class="space-y-1">
                <h3 class="font-medium text-foreground">{t('nodes.controlPlaneHA')}</h3>
                <p>
                  {t('nodes.sharedDataDir')}: {haReadiness.control_plane.shared_data_dir_verified
                    ? t('nodes.verified')
                    : t('nodes.notVerified')}
                </p>
                <p>
                  {t('nodes.dataDirShared')}: {haReadiness.install?.shared
                    ? t('nodes.verified')
                    : t('nodes.notVerified')}
                </p>
                <p>
                  {t('nodes.virtualIP')}: {haReadiness.control_plane.virtual_ip_configured
                    ? t('nodes.configured')
                    : t('nodes.notConfigured')}
                </p>
                <p>
                  {t('nodes.sharedStorage')}: {haReadiness.control_plane.shared_storage_verified
                    ? t('nodes.verified')
                    : t('nodes.notVerified')}
                </p>
                <p>{t('nodes.incusHATitle')}</p>
                {#if haReadiness.incus_targets.length}
                  <ul class="list-disc space-y-1 pl-5 text-muted-foreground">
                    {#each haReadiness.incus_targets as cluster (cluster.node_id)}
                      <li>
                        {cluster.name} ({cluster.project}): {t(
                          'nodes.incusHAAdminCertificateRequired'
                        )}
                      </li>
                    {/each}
                  </ul>
                {:else}
                  <p class="text-muted-foreground">{t('nodes.noRemoteIncusClusters')}</p>
                {/if}
              </div>
              {#if incusHealing}
                <div class="space-y-2 border-t border-border pt-3">
                  <h3 class="font-medium text-foreground">{t('nodes.incusHealingTitle')}</h3>
                  <p class="text-xs text-muted-foreground">{t('nodes.incusHealingDesc')}</p>
                  {#if !incusHealing.available}
                    <p class="text-xs text-muted-foreground">
                      {t('nodes.incusHealingNotAvailable')}
                    </p>
                  {:else}
                    <p class="text-xs">
                      {incusHealing.enabled
                        ? t('nodes.incusHealingActive', { seconds: incusHealing.threshold_seconds })
                        : t('nodes.incusHealingInactive')}
                      · {incusHealing.members_online}/{incusHealing.members_total}
                    </p>
                    {#if incusHealing.blockers.length}
                      <ul class="list-disc space-y-1 pl-5 text-xs text-warning">
                        {#each incusHealing.blockers as blocker (`${blocker}`)}
                          <li>{haBlockerText(blocker)}</li>
                        {/each}
                      </ul>
                    {/if}
                    <div class="flex flex-wrap items-end gap-2">
                      <label class="space-y-1 text-xs" for="healing-seconds">
                        <span class="text-muted-foreground">{t('nodes.incusHealingSeconds')}</span>
                        <Input
                          id="healing-seconds"
                          aria-label={t('nodes.incusHealingSeconds')}
                          type="number"
                          min="0"
                          max="86400"
                          step="60"
                          bind:value={healingSeconds}
                          oninput={() => (healingDirty = true)}
                          aria-invalid={!!healingError}
                          class="w-32"
                        />
                      </label>
                      <Button
                        size="sm"
                        id="healing-save"
                        onclick={requestSaveIncusHealing}
                        disabled={savingHealing || (!incusHealing.can_change && healingSeconds > 0)}
                      >
                        {savingHealing ? t('common.saving') : t('common.save')}
                      </Button>
                    </div>
                    {#if healingError}
                      <p class="text-xs text-destructive" role="alert">{healingError}</p>
                    {/if}
                    {#if healingBlockers.length}
                      <Alert variant="warning">
                        <p class="font-medium">{t('nodes.healingBlockedTitle')}</p>
                        <ul class="mt-1 list-disc space-y-0.5 pl-5">
                          {#each healingBlockers as blocker (`${blocker}`)}
                            <li>{haBlockerText(blocker)}</li>
                          {/each}
                        </ul>
                      </Alert>
                    {/if}
                    <p class="text-xs text-muted-foreground">{t('nodes.incusHealingHint')}</p>
                  {/if}
                </div>
              {/if}
            </div>
            <HaNodeOps
              readiness={haReadiness}
              advanced={haViewMode === 'advanced'}
              onChanged={() => checkHAReadiness({ silent: true })}
            />
            {#if haReadiness.pacemaker.resources?.length}
              <div class="space-y-2 border-t border-border pt-3">
                <h3 class="text-xs font-semibold text-foreground">
                  {t('nodes.pacemakerResources')}
                </h3>
                <ul class="space-y-1 text-xs">
                  {#each haReadiness.pacemaker.resources as resource (`${resource.id}-${resource.node}-${resource.role}`)}
                    <li
                      class="flex flex-col gap-0.5 sm:flex-row sm:items-center sm:justify-between"
                    >
                      <span class="font-medium text-foreground">
                        {resource.id}
                        <span class="font-normal text-muted-foreground">{resource.agent}</span>
                      </span>
                      <span class="text-muted-foreground">
                        {resource.role}{resource.node ? ` · ${resource.node}` : ''}
                      </span>
                    </li>
                  {/each}
                </ul>
              </div>
            {/if}
          {/if}
        {/if}
      </Card>
    {/if}

    {#if clusterModule.enabled}
      <div>
        <HaWorkloadsPanel
          nodeNames={haReadiness?.pacemaker?.node_names ||
            (haReadiness?.pacemaker?.node_states || []).map((x) => x.name)}
          advanced={haViewMode === 'advanced'}
        />
      </div>
    {/if}
  </div>

  {#if !loadFailed}
    {#if auth.isAdmin()}
      <div
        id="nodes-access"
        role="tabpanel"
        aria-labelledby="nodes-access-tab"
        class="space-y-4 {activeSection !== 'access' ? 'hidden' : ''}"
      >
        <div id="cluster-access-card" class="scroll-mt-4"></div>
        <ClusterAccessCard
          bind:this={accessCard}
          overview={sshOverview}
          probeNodes={nodes}
          loading={sshLoading}
          error={sshError}
          onRefresh={loadSSH}
        />
      </div>
    {/if}
  {/if}

  <div
    id="nodes-fleet"
    role="tabpanel"
    aria-labelledby="nodes-fleet-tab"
    class="space-y-4 {activeSection !== 'fleet' ? 'hidden' : ''}"
  >
    <div class="flex flex-wrap items-end justify-between gap-2">
      <div class="space-y-1">
        <h2 class="text-base font-semibold text-foreground">{t('nodes.sectionFleet')}</h2>
        <p class="text-xs text-muted-foreground">{t('nodes.sectionFleetDesc')}</p>
      </div>
      <span class="text-xs text-muted-foreground"
        >{visibleNodes.length === 1
          ? t('nodes.fleetCountOne')
          : t('nodes.fleetCount', { count: visibleNodes.length })}</span
      >
    </div>

    {#if loading && nodes.length === 0}
      <div class="flex flex-col items-center justify-center gap-2 py-24" role="status">
        <Spinner size="lg" />
        <p class="text-xs text-muted-foreground">{t('nodes.loadingNodes')}</p>
      </div>
    {:else if nodes.length === 0}
      {#if !error}
        <Card class="p-10 text-center">
          <div
            class="mx-auto mb-3 flex h-10 w-10 items-center justify-center rounded-full bg-muted"
          >
            <Icon name="server" size={18} class="text-muted-foreground" />
          </div>
          <p class="text-sm font-medium text-foreground">{t('nodes.emptyTitle')}</p>
          <p class="mt-1 text-xs text-muted-foreground">{t('nodes.emptyList')}</p>
          {#if auth.isAdmin()}
            <Button size="sm" class="mt-4" onclick={openAdd}>
              <Icon name="plus" size={13} class="mr-1.5" />
              {t('nodes.addNode')}
            </Button>
          {/if}
        </Card>
      {/if}
    {:else}
      {#if refreshing}
        <p class="text-xs text-muted-foreground" role="status">{t('nodes.refreshing')}</p>
      {/if}
      <div
        class="grid grid-cols-1 sm:grid-cols-2 gap-5 transition-opacity {refreshing
          ? 'opacity-70'
          : ''}"
        aria-busy={refreshing}
      >
        {#each visibleNodes as n (n.id)}
          {@const health = nodeHealth(n)}
          <Card
            class="p-4 border border-border bg-card/60 flex flex-col justify-between space-y-3"
            data-testid="node-card-{n.id}"
          >
            {#if editingId === n.id}
              {#if editError}
                <p class="text-xs text-destructive mb-2">{editError}</p>
              {/if}
              <div class="space-y-2">
                <Input
                  bind:value={editName}
                  aria-label={t('nodes.editNameLabel')}
                  class="w-full text-sm"
                />
                <Input
                  bind:value={editUri}
                  aria-label={t('nodes.editUriLabel')}
                  disabled={n.type === 'local' || n.backend === 'incus'}
                  title={n.type === 'local'
                    ? t('nodes.cannotEditLocalUri')
                    : n.backend === 'incus'
                      ? t('nodes.cannotEditIncusUri')
                      : ''}
                  class="w-full text-xs font-mono {n.type === 'local' || n.backend === 'incus'
                    ? 'opacity-50'
                    : ''}"
                />
                {#if n.type !== 'local' && n.backend !== 'incus'}
                  <NodeUiUrlField
                    id="node-edit-ui-url-{n.id}"
                    bind:value={editUiUrl}
                    nodeUri={editUri}
                  />
                {/if}
                <div class="flex items-center gap-1.5">
                  <Button size="sm" onclick={() => saveEdit(n)} disabled={editSaving}>
                    {#if editSaving}<Spinner size="sm" color="text-white" />{/if}
                    {t('common.save')}
                  </Button>
                  <Button size="sm" variant="outline" onclick={cancelEdit}>
                    {t('common.cancel')}
                  </Button>
                </div>
              </div>
            {:else}
              <!-- Card Header -->
              <div class="space-y-2">
                <div class="flex flex-wrap items-start justify-between gap-2">
                  <div class="flex min-w-0 flex-1 items-center gap-2">
                    <div
                      class="w-8 h-8 rounded-lg bg-muted flex items-center justify-center shrink-0"
                    >
                      <Icon name="server" size={16} class="text-muted-foreground" />
                    </div>
                    <div class="min-w-0">
                      <div class="flex min-w-0 flex-wrap items-center gap-1.5 text-sm font-medium">
                        <span
                          class="min-w-0 max-w-full truncate"
                          title={n.name}
                          data-testid="node-name-{n.id}">{n.name}</span
                        >
                        {#if n.type === 'local'}
                          <span
                            class="shrink-0 rounded bg-accent/10 px-1.5 py-0.5 text-[10px] font-normal text-primary dark:text-accent-hover"
                            >{t('nodeSelf.thisServer')}</span
                          >
                        {/if}
                        {#if n.version}
                          <span
                            class="shrink-0 text-[10px] px-1.5 py-0.2 rounded font-mono bg-muted text-muted-foreground"
                          >
                            v{n.version}
                          </span>
                        {/if}
                      </div>
                      <span
                        class="text-[11px] px-1.5 py-0.5 rounded uppercase tracking-wide {n.type ===
                        'local'
                          ? 'bg-accent/10 text-primary dark:text-accent-hover'
                          : 'bg-muted text-muted-foreground'}"
                      >
                        {n.backend === 'incus' ? t('nodes.incusBackend') : t('nodes.kvmBackend')}
                      </span>
                    </div>
                  </div>

                  <!-- Status Badge & Enabled Toggle -->
                  <div class="flex flex-wrap items-center justify-end gap-1.5">
                    <button
                      type="button"
                      onclick={() => auth.isAdmin() && toggleEnabled(n)}
                      disabled={!auth.isAdmin() || togglingNode[n.id] || n.type === 'local'}
                      title={n.type === 'local'
                        ? t('remote.enable.localTip')
                        : t('remote.enable.explain')}
                      aria-pressed={!!n.enabled}
                      aria-label={t('nodes.toggleEnabledAria', { name: n.name })}
                      class="text-[10px] px-1.5 py-0.5 min-h-6 rounded shrink-0 {n.enabled
                        ? 'bg-success/10 text-success'
                        : 'bg-muted text-muted-foreground'}"
                    >
                      {n.enabled ? t('common.enabled') : t('common.disabled')}
                    </button>

                    {#if health === 'no_access'}
                      <span
                        class="inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-medium bg-info/10 text-info"
                        title={t('nodeSelf.statusNoAccessHint')}
                        data-testid="node-status-{n.id}"
                      >
                        <span class="w-1.5 h-1.5 rounded-full bg-info"></span>
                        {t('nodeSelf.statusNoAccess')}
                      </span>
                    {:else if n.status === 'online'}
                      <span
                        class="inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-mono font-medium bg-success/10 text-success"
                        data-testid="node-status-{n.id}"
                      >
                        <span class="w-1.5 h-1.5 rounded-full bg-success"></span>
                        {n.latency_ms > 0 ? `${n.latency_ms} ms` : t('nodes.statusOnline')}
                      </span>
                      {#if isSshPortOnly(n)}
                        <span
                          class="inline-flex items-center px-2 py-0.5 rounded text-[11px] font-medium bg-warning/10 text-warning"
                          title={t('nodes.sshPortOpenHint')}
                          data-testid="node-ssh-port-open-{n.id}"
                        >
                          {t('nodes.sshPortOpen')}
                        </span>
                      {/if}
                    {:else if n.status === 'degraded'}
                      <span
                        class="inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-mono font-medium bg-warning/10 text-warning"
                        title={t('nodes.degradedHint')}
                      >
                        <span class="w-1.5 h-1.5 rounded-full bg-warning"></span>
                        {t('nodes.statusDegraded')}
                      </span>
                    {:else if n.status === 'offline'}
                      <span
                        class="inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-mono font-medium bg-destructive/10 text-destructive"
                      >
                        <span class="w-1.5 h-1.5 rounded-full bg-destructive"></span>
                        {t('nodes.statusOffline')}
                      </span>
                    {:else}
                      <span
                        class="px-2 py-0.5 rounded text-[11px] font-mono bg-muted text-muted-foreground"
                      >
                        {!n.status || n.status === 'unknown' ? t('nodes.statusUnknown') : n.status}
                      </span>
                    {/if}
                  </div>
                </div>

                <!-- Node addresses: the cluster one (what WebKVM uses) and the
                   operator's own network (ui_url, used for links). -->
                {#if n.type !== 'local' && n.backend !== 'incus'}
                  {@const addr = nodeAddressInfo(n)}
                  <dl class="space-y-1 text-xs" data-testid="node-addresses-{n.id}">
                    <div class="flex items-center gap-1.5">
                      <dt class="shrink-0 text-muted-foreground">{t('nodes.clusterAddress')}</dt>
                      <dd class="min-w-0 truncate font-mono text-foreground" title={n.uri}>
                        {addr.cluster || n.uri}
                      </dd>
                      <Button
                        size="sm"
                        variant="ghost"
                        class="!h-9 shrink-0 !px-2 !text-[11px]"
                        onclick={() => copyAddress(n.uri)}
                        aria-label={t('nodes.copyAddressAria', {
                          label: t('nodes.clusterAddress'),
                          name: n.name,
                        })}
                      >
                        {t('nodes.copyAddress')}
                      </Button>
                    </div>
                    <div class="flex items-center gap-1.5">
                      <dt class="shrink-0 text-muted-foreground">{t('nodes.yourNetwork')}</dt>
                      {#if addr.uiUrl}
                        <dd
                          class="min-w-0 truncate font-mono text-foreground"
                          title={addr.uiUrl}
                          data-testid="node-ui-url-{n.id}"
                        >
                          {addr.uiUrl}
                        </dd>
                        <Button
                          size="sm"
                          variant="ghost"
                          class="!h-9 shrink-0 !px-2 !text-[11px]"
                          onclick={() => copyAddress(addr.uiUrl)}
                          aria-label={t('nodes.copyAddressAria', {
                            label: t('nodes.yourNetwork'),
                            name: n.name,
                          })}
                        >
                          {t('nodes.copyAddress')}
                        </Button>
                      {:else}
                        <dd class="text-muted-foreground">{t('nodes.notSet')}</dd>
                      {/if}
                    </div>
                  </dl>
                  {#if addr.privateBadge}
                    <div class="space-y-1" data-testid="node-private-{n.id}">
                      <span
                        class="inline-block rounded border border-border bg-muted px-2 py-0.5 text-[11px] text-foreground"
                        title={t('nodes.privateBadgeTitle')}
                      >
                        {t('nodes.privateBadge')}
                      </span>
                      <p class="text-xs text-muted-foreground">{t('nodes.setUiUrlHint')}</p>
                    </div>
                  {/if}
                {:else}
                  <p class="text-xs font-mono text-muted-foreground truncate" title={n.uri}>
                    {n.uri}
                  </p>
                {/if}

                {#if showAccessBadge(n)}
                  {@const as = accessState(sshNodeOf(n))}
                  <div class="flex flex-wrap items-center gap-1.5" data-testid="node-access-{n.id}">
                    <span
                      class="text-[11px] px-2 py-0.5 rounded {as === 'ready'
                        ? 'bg-success/10 text-success'
                        : as === 'changed'
                          ? 'bg-destructive/10 text-destructive'
                          : as === 'unverified'
                            ? 'bg-muted text-foreground'
                            : 'bg-warning/10 text-warning'}"
                      aria-label={t('clusterAccess.badgeAria', {
                        name: n.name,
                        state: accessStateLabel(t, as),
                      })}
                    >
                      {accessStateLabel(t, as)}
                    </span>
                    {#if sshNodeOf(n).host_key?.state !== 'pinned'}
                      <Button
                        size="sm"
                        variant="outline"
                        class="min-h-11"
                        aria-label={t('clusterAccess.goToAccess', { name: n.name })}
                        onclick={() => accessCard?.openNode(n.id)}
                      >
                        {t('clusterAccess.pinShortcut')}
                      </Button>
                    {:else}
                      <Button
                        size="sm"
                        variant="outline"
                        class="min-h-11"
                        disabled={accessTesting[n.id]}
                        onclick={() => quickTestAccess(n)}
                      >
                        {as === 'unverified'
                          ? t('clusterAccess.testNow')
                          : t('clusterAccess.testShortcut')}
                      </Button>
                    {/if}
                  </div>
                {/if}

                <!-- Node Telemetry Metrics -->
                <div class="pt-2 border-t border-border space-y-1.5 text-xs text-muted-foreground">
                  {#if n.disk_total > 0}
                    <div>
                      <div class="flex items-center justify-between text-[11px] mb-1">
                        <span>{t('nodes.diskUsage')}</span>
                        <span class="font-mono text-foreground">
                          {formatBytes(n.disk_free)} / {formatBytes(n.disk_total)}
                        </span>
                      </div>
                      <div
                        class="w-full h-1.5 bg-muted rounded-full overflow-hidden"
                        role="progressbar"
                        aria-label={t('nodes.usageAria', { label: t('nodes.diskUsage') })}
                        aria-valuemin="0"
                        aria-valuemax="100"
                        aria-valuenow={usagePercent(n.disk_total, n.disk_free)}
                      >
                        <div
                          class="h-full bg-primary"
                          style="width: {usagePercent(n.disk_total, n.disk_free)}%"
                        ></div>
                      </div>
                    </div>
                  {/if}

                  {#if n.ram_total > 0}
                    <div>
                      <div class="flex items-center justify-between text-[11px] mb-1">
                        <span>{t('nodes.memory')}</span>
                        <span class="font-mono text-foreground">
                          {formatBytes(n.ram_free)} / {formatBytes(n.ram_total)}
                        </span>
                      </div>
                      <div
                        class="w-full h-1.5 bg-muted rounded-full overflow-hidden"
                        role="progressbar"
                        aria-label={t('nodes.usageAria', { label: t('nodes.memory') })}
                        aria-valuemin="0"
                        aria-valuemax="100"
                        aria-valuenow={usagePercent(n.ram_total, n.ram_free)}
                      >
                        <div
                          class="h-full bg-primary"
                          style="width: {usagePercent(n.ram_total, n.ram_free)}%"
                        ></div>
                      </div>
                    </div>
                  {/if}

                  {#if n.cpus > 0}
                    <div class="flex items-center justify-between text-[11px]">
                      <span>{t('nodes.cpus')}</span>
                      <span class="font-mono text-foreground">{n.cpus}</span>
                    </div>
                  {/if}

                  {#if n.uptime_sec > 0}
                    <div class="flex items-center justify-between text-[11px]">
                      <span>{t('nodes.uptime')}</span>
                      <span class="font-mono text-foreground">{fmtUptime(n.uptime_sec)}</span>
                    </div>
                  {/if}

                  <div class="flex items-center justify-between text-[11px]">
                    <span>{t('nodes.lastSeen')}</span>
                    <span class="font-mono text-muted-foreground"
                      >{fmtDate(n.last_seen || n.updated_at)}</span
                    >
                  </div>

                  {#if n.status === 'degraded'}
                    <div class="text-[10px] text-warning">{t('nodes.degradedHint')}</div>
                  {/if}
                  {#if health === 'no_access'}
                    <div class="space-y-1" data-testid="node-no-access-{n.id}">
                      <p class="text-[11px] text-info">{t('nodeSelf.statusNoAccessHint')}</p>
                      {#if auth.isAdmin()}
                        <Button
                          size="sm"
                          variant="outline"
                          class="min-h-9 !text-[11px]"
                          aria-label={t('nodeSelf.setUpAccessAria', { name: n.name })}
                          onclick={() => openAccessFor(n)}
                        >
                          {t('nodeSelf.setUpAccess')}
                        </Button>
                      {/if}
                    </div>
                  {/if}
                  {#if n.last_error}
                    <!-- Only the status decides offline; an error of a node that
                       still answers is informative, not red. -->
                    <div
                      class="text-[10px] truncate {health === 'offline' || health === 'degraded'
                        ? 'text-destructive'
                        : 'text-muted-foreground'}"
                      title={n.last_error}
                    >
                      {n.last_error}
                    </div>
                  {/if}
                </div>
              </div>

              <!-- Card Actions -->
              <div
                class="pt-3 border-t border-border flex flex-wrap items-center justify-between gap-1"
              >
                <div class="flex min-w-0 flex-wrap items-center gap-1.5">
                  <Button
                    size="sm"
                    variant="outline"
                    class="!h-9 min-h-9 !text-[11px] !px-2 gap-1"
                    onclick={() => handlePingSingle(n)}
                    disabled={pingingNode[n.id]}
                    aria-label={t('nodes.pingNodeAria', { name: n.name })}
                  >
                    <Icon
                      name="activity"
                      size={11}
                      class={pingingNode[n.id] ? 'animate-spin' : ''}
                    />
                    {t('nodes.pingBtn')}
                  </Button>

                  {#if n.type !== 'local' && n.backend !== 'incus'}
                    {@const consoleUrl = getWebConsoleURL(n)}
                    {#if consoleUrl}
                      <a
                        href={consoleUrl}
                        target="_blank"
                        rel="noreferrer"
                        aria-label={t('nodes.openConsoleAria', { name: n.name })}
                        class="inline-flex items-center gap-1 h-9 px-2 text-[11px] rounded border border-border bg-card text-foreground hover:bg-muted/40 transition-colors"
                      >
                        <Icon name="external-link" size={11} />
                        {t('nodes.openConsole')}
                      </a>
                    {:else}
                      <button
                        type="button"
                        disabled
                        title={nodeAddressInfo(n).needsHint
                          ? t('nodes.consoleNeedsUiUrl')
                          : t('nodes.consoleUnavailable')}
                        aria-label="{t('nodes.openConsoleAria', {
                          name: n.name,
                        })} — {nodeAddressInfo(n).needsHint
                          ? t('nodes.consoleNeedsUiUrl')
                          : t('nodes.consoleUnavailable')}"
                        class="inline-flex items-center gap-1 h-9 px-2 text-[11px] rounded border border-border bg-card text-foreground opacity-50"
                      >
                        <Icon name="external-link" size={11} />
                        {t('nodes.openConsole')}
                      </button>
                    {/if}
                  {/if}
                  {#if n.backend === 'incus'}
                    <Button
                      size="sm"
                      variant="outline"
                      class="min-h-11 !text-[11px] !px-2 gap-1"
                      onclick={() => toggleIncusInventory(n)}
                      disabled={loadingIncusInventory[n.id]}
                      aria-expanded={!!incusInventory[n.id]}
                      aria-busy={loadingIncusInventory[n.id]}
                    >
                      {#if loadingIncusInventory[n.id]}<Spinner size="sm" />{/if}
                      {loadingIncusInventory[n.id]
                        ? t('nodes.loadingInventory')
                        : incusInventory[n.id]
                          ? t('nodes.hideInventory')
                          : t('nodes.showInventory')}
                    </Button>
                  {/if}
                </div>

                {#if auth.isAdmin()}
                  <div class="flex items-center gap-1">
                    <Button
                      size="sm"
                      variant="ghost"
                      class="!h-9 !w-9 !p-0"
                      onclick={() => startEdit(n)}
                      aria-label={t('nodes.editNodeAria', { name: n.name })}
                    >
                      <Icon name="pencil" size={12} />
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      class="!h-9 !w-9 !p-0 text-destructive hover:bg-destructive/10"
                      onclick={() => askDelete(n)}
                      aria-label={t('nodes.deleteNodeAria', { name: n.name })}
                      disabled={n.type === 'local'}
                      title={n.type === 'local' ? t('nodes.cannotDeleteLocal') : ''}
                    >
                      <Icon name="trash" size={12} />
                    </Button>
                  </div>
                {/if}
              </div>
            {/if}
            {#if n.backend === 'incus' && (incusInventory[n.id] || incusInventoryErrors[n.id])}
              <div class="space-y-2 border-t border-border pt-3" aria-live="polite">
                {#if incusInventoryErrors[n.id]}
                  <Alert variant="error"
                    >{t('nodes.inventoryError', { error: incusInventoryErrors[n.id] })}</Alert
                  >
                  <Button
                    size="sm"
                    variant="outline"
                    class="min-h-11"
                    onclick={() => toggleIncusInventory(n)}
                    disabled={loadingIncusInventory[n.id]}
                  >
                    {t('nodes.retryInventory')}
                  </Button>
                {:else if incusInventory[n.id]}
                  {@const inventory = incusInventory[n.id]}
                  <div class="text-xs text-muted-foreground">
                    {t('nodes.incusProject')}:
                    <span class="text-foreground">{inventory.project}</span>
                    <span class="mx-1">·</span>
                    {t('nodes.architecture')}:
                    <span class="text-foreground">{inventory.host_arch}</span>
                  </div>
                  <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 text-xs">
                    <div>
                      <div class="font-medium text-foreground">{t('nodes.storagePools')}</div>
                      {#if inventory.pools.length}
                        <ul class="mt-1 list-disc pl-4 text-muted-foreground">
                          {#each inventory.pools as pool (pool.name)}<li>
                              {pool.name} ({pool.type})
                            </li>{/each}
                        </ul>
                      {:else}<p class="mt-1 text-muted-foreground">
                          {t('nodes.inventoryEmpty')}
                        </p>{/if}
                    </div>
                    <div>
                      <div class="font-medium text-foreground">{t('nodes.networks')}</div>
                      {#if inventory.networks.length}
                        <ul class="mt-1 list-disc pl-4 text-muted-foreground">
                          {#each inventory.networks as item (item.name)}<li>{item.name}</li>{/each}
                        </ul>
                      {:else}<p class="mt-1 text-muted-foreground">
                          {t('nodes.inventoryEmpty')}
                        </p>{/if}
                    </div>
                    <div>
                      <div class="font-medium text-foreground">{t('nodes.incusProfiles')}</div>
                      <p class="mt-1 break-words text-muted-foreground">
                        {inventory.profiles.join(', ') || t('nodes.inventoryEmpty')}
                      </p>
                    </div>
                    <div>
                      <div class="font-medium text-foreground">{t('nodes.incusImages')}</div>
                      <p class="mt-1 break-words text-muted-foreground">
                        {inventory.images
                          .slice(0, 8)
                          .map((image) => image.label || image.ref)
                          .join(', ') || t('nodes.inventoryEmpty')}
                      </p>
                    </div>
                  </div>
                {/if}
              </div>
            {/if}
          </Card>
        {/each}
      </div>
    {/if}
  </div>
</div>

<ConfirmDialog
  bind:open={installConfirmOpen}
  title={t('nodes.installConfirmTitle')}
  description={t('nodes.installConfirmDesc')}
  confirmLabel={t('nodes.installBySSH')}
  loading={installingNode}
  onConfirm={installNodeDependencies}
/>

<ConfirmDialog
  bind:open={confirmDeleteOpen}
  title={t('nodes.deleteConfirmTitle', { name: confirmDeleteNode?.name || '' })}
  description={confirmDeleteNode?.backend === 'incus'
    ? t('nodes.incusDeleteConfirmDesc')
    : t('nodes.deleteConfirmDesc')}
  confirmLabel={t('common.delete')}
  variant="destructive"
  loading={confirmDeleteLoading}
  onConfirm={() => doDelete()}
/>

<ConfirmDialog
  bind:open={selfDeleteOpen}
  title={t('nodeSelf.removeConfirmTitle', { name: selfDeleteNode?.name || '' })}
  description={t('nodeSelf.removeConfirmDesc')}
  confirmLabel={t('nodeSelf.removeDuplicate')}
  variant="destructive"
  loading={selfDeleteLoading}
  onConfirm={removeSelfDuplicate}
/>

<ConfirmDialog
  bind:open={forceDeleteOpen}
  title={t('nodes.forceDeleteTitle', { name: confirmDeleteNode?.name || '' })}
  description={t('nodes.forceDeleteDesc')}
  confirmLabel={t('nodes.forceDeleteConfirm')}
  variant="destructive"
  loading={confirmDeleteLoading}
  onConfirm={() => doDelete({ force: true })}
/>

<ConfirmDialog
  bind:open={healingConfirmOpen}
  title={t('nodes.healingConfirmTitle')}
  description={t('nodes.healingConfirmBody', { seconds: Number(healingSeconds) || 0 })}
  confirmLabel={t('nodes.healingConfirmEnable')}
  loading={savingHealing}
  onConfirm={saveIncusHealing}
/>
