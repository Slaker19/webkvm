<script>
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import { onMount } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { upsertTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { toast, dismiss } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Card } from '$lib/components/ui/card';
  import DataTable from '$lib/components/DataTable.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import { t } from '../lib/i18n.svelte.js';

  let networks = $state([]);
  let hostInterfaces = $state([]);
  // Tracks whether the host-interface fetch has been attempted at least
  // once. The effect below keys off this flag, not off the array being
  // empty: on a host with no listable interfaces (or on a fetch error,
  // which also yields []), keying off `length === 0` re-triggered the
  // fetch every time it assigned the new empty array — an infinite loop.
  let hostInterfacesLoaded = $state(false);
  let loading = $state(true);
  let error = $state('');
  let showCreate = $state(false);
  let editingNet = $state(null);
  let name = $state('');
  let cidr = $state('192.168.100.0/24');
  // Exactly 3 kinds: isolated (no internet) is the safest default.
  let kind = $state('isolated');
  let directInterface = $state('');
  let vlanAware = $state(false);
  // Seeded from Settings -> Network -> "New bridges VLAN-aware by
  // default" so the create-network checkbox reflects the operator's
  // configured default instead of always starting unchecked.
  let vlanAwareDefault = $state(false);
  let dhcp = $state(true);
  let dhcpStart = $state('');
  let dhcpEnd = $state('');
  let dnsText = $state('');
  let gateway = $state('');
  let dns1 = $state('');
  let dns2 = $state('');
  // MTU 0 = kernel/bridge default. Reservations = fixed MAC→IP DHCP
  // leases ({ mac, ip, name }), only meaningful when DHCP is on.
  let mtu = $state(0);
  let reservations = $state([]);
  let autostart = $state(true);
  let saving = $state(false);
  let toggling = $state({});
  let leasesByNetwork = $state({}); // { [networkName]: DHCPLease[] }
  let viewingLeasesFor = $state(null); // network name, or null
  let leases = $state([]);
  let loadingLeases = $state(false);

  // Interface IP modal & Bond creation states
  let editingIface = $state(null); // Interface being edited for IP/gateway
  let ifaceIPv4 = $state('');
  let ifaceGateway = $state('');
  let savingIface = $state(false);

  let showBondModal = $state(false);
  let bondName = $state('bond0');
  let bondMode = $state('active-backup');
  let bondSlaves = $state([]);
  let bondBridge = $state('vmbr0');
  let bondIPv4 = $state('');
  let bondGateway = $state('');
  let savingBond = $state(false);

  // Attach interface to bridge modal state
  let attachingIface = $state(null);
  let targetBridge = $state('vmbr0');
  let savingAttach = $state(false);

  let totalLeases = $derived(Object.values(leasesByNetwork).reduce((sum, l) => sum + l.length, 0));

  let preview = $derived.by(() => computeCIDRPreview(cidr));

  let confirmState = $state({
    open: false,
    title: '',
    description: '',
    confirmLabel: t('common.confirm'),
    variant: 'destructive',
    onConfirm: () => {},
    loading: false,
  });

  onMount(() => {
    load();
    loadHostInterfaces();
    // /api/settings is admin-only, and only the (admin-only) create form
    // uses this default, so non-admins skip the request entirely.
    if (!auth.isAdmin()) return;
    api
      .getSettings()
      .then((s) => {
        vlanAwareDefault = !!s?.values?.['network.vlan_aware_default'];
        vlanAware = vlanAwareDefault;
      })
      .catch(() => {}); // non-fatal: the create form just keeps its plain default
  });

  $effect(() => {
    if (preview && dhcp) {
      if (!dhcpStart) dhcpStart = preview.dhcpStart;
      if (!dhcpEnd) dhcpEnd = preview.dhcpEnd;
    }
  });

  $effect(() => {
    if (showCreate && kind === 'direct' && !hostInterfacesLoaded) loadHostInterfaces();
  });

  function computeCIDRPreview(c) {
    if (!c || !c.includes('/')) return null;
    const [ip, prefixStr] = c.split('/');
    const prefix = parseInt(prefixStr, 10);
    if (!ip || isNaN(prefix) || prefix < 0 || prefix > 32) return null;
    const parts = ip.split('.').map(Number);
    if (parts.length !== 4 || parts.some((p) => isNaN(p) || p < 0 || p > 255)) return null;

    const numToIp = (n) =>
      [(n >>> 24) & 0xff, (n >>> 16) & 0xff, (n >>> 8) & 0xff, n & 0xff].join('.');

    const networkMask = (0xffffffff << (32 - prefix)) >>> 0;
    const hostMask = ~networkMask >>> 0;
    const ipNum = ((parts[0] << 24) | (parts[1] << 16) | (parts[2] << 8) | parts[3]) >>> 0;
    const network = (ipNum & networkMask) >>> 0;
    const broadcast = (network | hostMask) >>> 0;
    const first = prefix >= 31 ? network : (network + 1) >>> 0;
    const last = prefix >= 31 ? broadcast : (broadcast - 1) >>> 0;

    return {
      gateway: numToIp(first),
      dhcpStart: numToIp(first + 1),
      dhcpEnd: numToIp(last),
    };
  }

  function parseDNSList(text) {
    return text
      .split(/[\s,;]+/)
      .map((s) => s.trim())
      .filter((s) => s.length > 0);
  }

  function formatDNSList(list) {
    return (list || []).join(', ');
  }

  // Fixed leases: trim, lowercase MACs and drop empty rows so a blank
  // "add" row never reaches the API.
  function cleanReservations() {
    return reservations
      .map((r) => ({
        mac: (r.mac || '').trim().toLowerCase(),
        ip: (r.ip || '').trim(),
        name: (r.name || '').trim(),
      }))
      .filter((r) => r.mac && r.ip);
  }

  function addReservation() {
    reservations = [...reservations, { mac: '', ip: '', name: '' }];
  }

  function removeReservation(i) {
    reservations = reservations.filter((_, idx) => idx !== i);
  }

  function resetForm() {
    name = '';
    cidr = '192.168.100.0/24';
    kind = 'isolated';
    directInterface = '';
    vlanAware = vlanAwareDefault;
    dhcp = true;
    dhcpStart = '';
    dhcpEnd = '';
    dnsText = '';
    gateway = '';
    dns1 = '';
    dns2 = '';
    mtu = 0;
    reservations = [];
    autostart = true;
    editingNet = null;
    showCreate = false;
  }

  function startEdit(net) {
    editingNet = net.name;
    kind = net.kind || 'isolated';
    cidr = net.cidr || '';
    directInterface = net.interface || '';
    vlanAware = !!net.vlan_aware;
    dhcp = !!net.dhcp;
    dhcpStart = net.dhcp_start || '';
    dhcpEnd = net.dhcp_end || '';
    dnsText = formatDNSList(net.dns);
    gateway = net.gateway || '';
    dns1 = (Array.isArray(net.dns) && net.dns[0]) || '';
    dns2 = (Array.isArray(net.dns) && net.dns[1]) || '';
    mtu = net.mtu || 0;
    reservations = Array.isArray(net.reservations)
      ? net.reservations.map((r) => ({ mac: r.mac || '', ip: r.ip || '', name: r.name || '' }))
      : [];
    autostart = !!net.autostart;
    showCreate = true;
  }

  async function load() {
    loading = true;
    error = '';
    try {
      networks = await api.listNetworks();
      await loadAllLeases();
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  async function loadHostInterfaces() {
    hostInterfacesLoaded = true;
    try {
      hostInterfaces = await api.listHostInterfaces();
    } catch {
      hostInterfaces = [];
    }
  }

  async function toggleIfaceState(iface) {
    const nextState = iface.state === 'up' ? 'down' : 'up';
    try {
      await api.configureHostInterface(iface.name, { state: nextState });
      toast.success(
        t('networks.ifaceStateToggled', { name: iface.name, state: nextState.toUpperCase() })
      );
      await loadHostInterfaces();
    } catch (e) {
      toast.error(e.message, { duration: 0 });
    }
  }

  function isolateIface(iface) {
    askConfirm({
      title: t('networks.isolateIfaceTitle', { name: iface.name }),
      description: t('networks.isolateIfaceDesc', { name: iface.name, master: iface.master }),
      confirmLabel: t('networks.isolateIface'),
      variant: 'destructive',
      onConfirm: async () => {
        confirmState.loading = true;
        const taskId = 'net-isolate:' + iface.name;
        upsertTask({
          id: taskId,
          kind: 'network',
          title: `Desvinculando interfaz: ${iface.name}`,
          pct: 30,
          message: `Extrayendo de ${iface.master || 'bridge'}...`,
          status: 'running',
        });
        try {
          await api.configureHostInterface(iface.name, { isolate: true });
          finishTask(taskId, 'success', `Interfaz ${iface.name} aislada correctamente`, 100);
          confirmState.open = false;
          toast.success(t('networks.isolateIfaceSuccess', { name: iface.name }));
          await Promise.all([load(), loadHostInterfaces()]);
        } catch (e) {
          finishTask(taskId, 'error', e.message || 'Error al aislar interfaz', 30);
          toast.error(e.message, { duration: 0 });
        } finally {
          confirmState.loading = false;
        }
      },
    });
  }

  function openEditIface(iface) {
    editingIface = iface;
    ifaceIPv4 = iface.ipv4 || '';
    ifaceGateway = '';
  }

  async function saveIfaceConfig() {
    if (!editingIface) return;
    savingIface = true;
    try {
      const payload = { ipv4: ifaceIPv4.trim() };
      if (ifaceGateway.trim()) payload.gateway = ifaceGateway.trim();
      await api.configureHostInterface(editingIface.name, payload);
      toast.success(t('common.saved'));
      editingIface = null;
      await loadHostInterfaces();
    } catch (e) {
      toast.error(e.message, { duration: 0 });
    } finally {
      savingIface = false;
    }
  }

  async function createBond() {
    if (bondSlaves.length < 2) {
      toast.error(t('networks.selectBondSlavesHelp'));
      return;
    }
    savingBond = true;
    const bondN = bondName.trim();
    const taskId = 'net-bond:' + bondN;
    upsertTask({
      id: taskId,
      kind: 'network',
      title: `Configurando enlace Bond: ${bondN} (${bondMode})`,
      pct: 30,
      message: 'Aplicando configuración Netplan y esclavos...',
      status: 'running',
    });
    try {
      await api.createHostBond({
        name: bondN,
        mode: bondMode,
        interfaces: bondSlaves,
        bridge: bondBridge ? bondBridge.trim() : undefined,
        ipv4: !bondBridge && bondIPv4.trim() ? bondIPv4.trim() : undefined,
        gateway: !bondBridge && bondGateway.trim() ? bondGateway.trim() : undefined,
      });
      finishTask(taskId, 'success', `Enlace Bond ${bondN} creado con éxito`, 100);
      toast.success(t('networks.bondCreatedSuccess', { name: bondName }));
      showBondModal = false;
      bondSlaves = [];
      await Promise.all([load(), loadHostInterfaces()]);
    } catch (e) {
      finishTask(taskId, 'error', e.message || 'Error al crear enlace Bond', 30);
      toast.error(e.message, { duration: 0 });
    } finally {
      savingBond = false;
    }
  }

  async function deleteBond(name) {
    if (!confirm(t('networks.deleteBondConfirm', { name }))) return;
    const taskId = 'net-delbond:' + name;
    upsertTask({
      id: taskId,
      kind: 'network',
      title: `Eliminando enlace Bond: ${name}`,
      pct: 30,
      message: 'Liberando interfaces y actualizando Netplan...',
      status: 'running',
    });
    try {
      await api.deleteHostBond(name);
      finishTask(taskId, 'success', `Enlace Bond ${name} eliminado con éxito`, 100);
      toast.success(t('networks.bondDeletedSuccess', { name }));
      await Promise.all([load(), loadHostInterfaces()]);
    } catch (e) {
      finishTask(taskId, 'error', e.message || 'Error al eliminar enlace Bond', 30);
      toast.error(e.message, { duration: 0 });
    }
  }

  async function attachIfaceToBridge() {
    if (!attachingIface || !targetBridge) return;
    savingAttach = true;
    const taskId = 'net-attach:' + attachingIface.name;
    upsertTask({
      id: taskId,
      kind: 'network',
      title: `Asociando ${attachingIface.name} a ${targetBridge}`,
      pct: 30,
      message: 'Vinculando interfaz al puente...',
      status: 'running',
    });
    try {
      await api.updateNetwork(targetBridge, { add_slaves: [attachingIface.name] });
      finishTask(taskId, 'success', `${attachingIface.name} vinculado a ${targetBridge}`, 100);
      toast.success(
        t('networks.attachSuccess', { name: attachingIface.name, bridge: targetBridge })
      );
      attachingIface = null;
      await Promise.all([load(), loadHostInterfaces()]);
    } catch (e) {
      finishTask(taskId, 'error', e.message || 'Error al asociar a puente', 30);
      toast.error(e.message, { duration: 0 });
    } finally {
      savingAttach = false;
    }
  }

  async function loadAllLeases() {
    const eligible = networks.filter((n) => n.kind !== 'direct' && n.dhcp);
    const results = await Promise.all(
      eligible.map((n) => api.listNetworkLeases(n.name).catch(() => []))
    );
    const next = {};
    eligible.forEach((n, i) => {
      next[n.name] = results[i];
    });
    leasesByNetwork = next;
  }

  async function viewLeases(net) {
    viewingLeasesFor = net.name;
    loadingLeases = true;
    try {
      leases = await api.listNetworkLeases(net.name);
    } catch (e) {
      toast.error(e.message, { duration: 0 });
      leases = [];
    } finally {
      loadingLeases = false;
    }
  }

  function releaseLease(lease) {
    askConfirm({
      title: t('networks.releaseLeaseTitle', { ip: lease.ip }),
      description: t('networks.releaseLeaseDesc'),
      confirmLabel: t('networks.release'),
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.releaseNetworkLease(viewingLeasesFor, lease.mac, lease.ip);
          confirmState.open = false;
          toast.success(t('networks.releaseLeaseToast', { ip: lease.ip }));
          await viewLeases({ name: viewingLeasesFor });
          await loadAllLeases();
        } catch (e) {
          toast.error(e.message, { duration: 0 });
        } finally {
          confirmState.loading = false;
        }
      },
    });
  }

  async function create() {
    if (!name || !name.trim()) {
      const msg = t('networks.nameRequired');
      error = msg;
      toast.error(msg, { duration: 0 });
      return;
    }
    if (kind === 'direct' && !directInterface) {
      const msg = t('networks.selectInterfaceError');
      error = msg;
      toast.error(msg, { duration: 0 });
      return;
    }
    if (kind !== 'direct' && cidr && !cidr.includes('/')) {
      const msg = t('networks.cidrPrefixError');
      error = msg;
      toast.error(msg, { duration: 0 });
      return;
    }
    if (kind === 'direct' && cidr && cidr.trim() && !cidr.includes('/')) {
      const msg = t('networks.cidrPrefixError');
      error = msg;
      toast.error(msg, { duration: 0 });
      return;
    }
    error = '';
    saving = true;
    let payload;
    if (kind === 'direct') {
      const dnsList = [dns1.trim(), dns2.trim()].filter(Boolean);
      payload = {
        name: name.trim(),
        kind,
        autostart,
        interface: directInterface,
        vlan_aware: vlanAware,
      };
      if (cidr && cidr.trim()) payload.cidr = cidr.trim();
      if (gateway && gateway.trim()) payload.gateway = gateway.trim();
      if (dnsList.length > 0) payload.dns = dnsList;
      if (Number(mtu)) payload.mtu = Number(mtu);
    } else {
      const dnsList = dnsText ? parseDNSList(dnsText) : [dns1.trim(), dns2.trim()].filter(Boolean);
      payload = { name: name.trim(), kind, autostart, cidr: cidr || '', dhcp };
      if (gateway && gateway.trim()) payload.gateway = gateway.trim();
      if (dhcp) {
        payload.dhcp_start = dhcpStart || preview?.dhcpStart || '';
        payload.dhcp_end = dhcpEnd || preview?.dhcpEnd || '';
      }
      if (dnsList.length > 0) payload.dns = dnsList;
      if (Number(mtu)) payload.mtu = Number(mtu);
      if (dhcp) payload.reservations = cleanReservations();
    }
    const taskId = 'net-create:' + name.trim();
    upsertTask({
      id: taskId,
      kind: 'network',
      title: `Creando red virtual: ${name.trim()}`,
      pct: 30,
      message: 'Definiendo puente, DHCP y reglas de aislamiento...',
      status: 'running',
    });
    try {
      const created = await api.createNetwork(payload);
      const label = (created && created.name) || name;
      finishTask(taskId, 'success', `Red virtual ${label} creada con éxito`, 100);
      toast.success(t('networks.networkCreated', { label }), { duration: 6000 });
      resetForm();
      await load();
      document
        .getElementById('networks-table-anchor')
        ?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    } catch (e) {
      finishTask(taskId, 'error', e.message || 'Error al crear red virtual', 30);
      console.error('[Networks] create failed', { payload, error: e });
      error = e.message;
      toast.error(e.message, { duration: 0 });
    } finally {
      saving = false;
    }
  }

  async function save() {
    if (!editingNet) return;
    error = '';
    saving = true;
    const dnsList = [dns1.trim(), dns2.trim()].filter(Boolean);
    const payload = { dhcp, autostart, vlan_aware: vlanAware };
    if (kind === 'direct') {
      if (gateway !== undefined) payload.gateway = gateway.trim();
      payload.dns = dnsList;
    } else {
      if (gateway !== undefined) payload.gateway = gateway.trim();
      if (dhcp) {
        payload.dhcp_start = dhcpStart || preview?.dhcpStart || '';
        payload.dhcp_end = dhcpEnd || preview?.dhcpEnd || '';
      }
      payload.dns = dnsText ? parseDNSList(dnsText) : dnsList;
      // Always send the (possibly empty) set so clearing reservations is
      // possible; the backend replaces the whole list.
      payload.reservations = dhcp ? cleanReservations() : [];
    }
    payload.mtu = Number(mtu) || 0;
    try {
      await api.updateNetwork(editingNet, payload);
      resetForm();
      toast.success(t('networks.networkUpdated'));
      await load();
    } catch (e) {
      toast.error(e.message, { duration: 0 });
    } finally {
      saving = false;
    }
  }

  function askConfirm(opts) {
    confirmState = { ...opts, open: true, loading: false };
  }

  function deleteNet(id) {
    askConfirm({
      title: t('networks.deleteNetworkTitle', { name: id }),
      description: t('networks.deleteNetworkDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        confirmState.open = false;
        const pendingId = toast.info(t('networks.deletingNetwork', { name: id }), {
          duration: 0,
        });
        try {
          await api.deleteNetwork(id);
          dismiss(pendingId);
          toast.success(t('networks.deleteNetworkToast', { name: id }), { duration: 4000 });
          await load();
        } catch (e) {
          dismiss(pendingId);
          toast.error(t('networks.deleteNetworkFailed', { name: id, error: e.message }), {
            duration: 0,
          });
        }
      },
    });
  }

  function toggleNet(net) {
    if (net.active) {
      askConfirm({
        title: t('networks.stopTitle', { name: net.name }),
        description: t('networks.stopDesc'),
        confirmLabel: t('networks.stop'),
        variant: 'default',
        onConfirm: async () => {
          confirmState.loading = true;
          toggling = { ...toggling, [net.name]: true };
          try {
            await api.stopNetwork(net.name);
            confirmState.open = false;
            toast.success(t('networks.stopToast', { name: net.name }));
            await load();
          } catch (e) {
            toast.error(e.message, { duration: 0 });
          } finally {
            confirmState.loading = false;
            toggling = { ...toggling, [net.name]: false };
          }
        },
      });
    } else {
      toggling = { ...toggling, [net.name]: true };
      api
        .startNetwork(net.name)
        .then(async () => {
          toast.success(t('networks.startToast', { name: net.name }));
          await load();
        })
        .catch((e) => toast.error(e.message, { duration: 0 }))
        .finally(() => (toggling = { ...toggling, [net.name]: false }));
    }
  }
</script>

<div class="p-3 sm:p-5 w-full max-w-[1700px] mx-auto">
  <PageHeader title={t('networks.title')} subtitle={t('networks.subtitle')}>
    {#snippet actions()}
      <!-- Creating a network is admin-only server-side; operators only
           get start/stop and lease release, so the button is hidden. -->
      {#if !showCreate && auth.isAdmin()}
        <Button
          onclick={() => {
            resetForm();
            showCreate = true;
          }}>{t('networks.createNetwork')}</Button
        >
      {/if}
    {/snippet}
  </PageHeader>

  {#if !loading && networks.length > 0}
    <div class="grid grid-cols-2 sm:grid-cols-3 xl:grid-cols-5 gap-3 mb-4">
      <StatCard label={t('networks.title')} value={String(networks.length)} />
      <StatCard
        label={t('networks.nat')}
        value={String(networks.filter((n) => n.kind === 'nat').length)}
      />
      <StatCard
        label={t('networks.isolated')}
        value={String(networks.filter((n) => n.kind === 'isolated').length)}
      />
      <StatCard
        label={t('networks.direct')}
        value={String(networks.filter((n) => n.kind === 'direct').length)}
      />
      <StatCard label={t('networks.activeLeasesBadge')} value={String(totalLeases)} />
    </div>
  {/if}

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if showCreate}
    <Card class="p-5 mb-4 space-y-3">
      <div class="flex items-center justify-between">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
          {editingNet ? t('networks.editNetwork', { name: editingNet }) : t('networks.newNetwork')}
        </h2>
        {#if editingNet}
          <span class="text-xs text-muted-foreground">{t('networks.cannotChange')}</span>
        {/if}
      </div>

      {#if !editingNet}
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label for="net-name" class="block text-sm font-medium mb-1.5"
              >{t('networks.name')}</label
            >
            <Input id="net-name" bind:value={name} placeholder="my-network" />
          </div>
          <div>
            <label for="net-kind" class="block text-sm font-medium mb-1.5"
              >{t('networks.forwardMode')}</label
            >
            <select id="net-kind" bind:value={kind} class="input">
              <option value="isolated">{t('networks.isolated')}</option>
              <option value="nat">{t('networks.nat')}</option>
              <option value="direct">{t('networks.direct')}</option>
            </select>
          </div>
        </div>

        {#if kind === 'direct'}
          <div>
            <label for="net-direct-iface" class="block text-sm font-medium mb-1.5"
              >{t('networks.directInterfaceLabel')}</label
            >
            <select id="net-direct-iface" bind:value={directInterface} class="input">
              <option value="" disabled>{t('networks.selectInterfacePlaceholder')}</option>
              {#each hostInterfaces as iface (iface.name || iface)}
                <option value={iface.name}
                  >{iface.name}
                  {iface.type !== 'other' ? `(${iface.type})` : ''} — {iface.state}</option
                >
              {/each}
            </select>
            <p class="text-xs text-muted-foreground mt-1">{t('networks.directHelp')}</p>
          </div>
          <label class="flex items-center gap-2 text-sm cursor-pointer">
            <input
              type="checkbox"
              bind:checked={vlanAware}
              class="w-4 h-4 rounded border-border bg-background text-accent focus:ring-accent"
            />
            {t('networks.vlanAwareLabel')}
          </label>

          <div class="pt-3 border-t border-border space-y-3">
            <div>
              <span class="text-sm font-medium">{t('networks.directStaticConfigTitle')}</span>
              <p class="text-xs text-muted-foreground">{t('networks.directStaticConfigDesc')}</p>
            </div>
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label for="net-direct-cidr" class="block text-sm font-medium mb-1.5"
                  >{t('networks.ipSubnet')}</label
                >
                <Input
                  id="net-direct-cidr"
                  bind:value={cidr}
                  placeholder="192.168.1.50/24"
                  class="tnum"
                />
                <p class="text-xs text-muted-foreground mt-1">{t('networks.directCidrHelp')}</p>
              </div>
              <div>
                <label for="net-direct-gw" class="block text-sm font-medium mb-1.5"
                  >{t('networks.gateway')}</label
                >
                <Input
                  id="net-direct-gw"
                  bind:value={gateway}
                  placeholder="192.168.1.1"
                  class="tnum"
                />
                <p class="text-xs text-muted-foreground mt-1">{t('networks.gatewayHelp')}</p>
              </div>
            </div>
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label for="net-direct-dns1" class="block text-sm font-medium mb-1.5"
                  >{t('networks.dns1')}</label
                >
                <Input id="net-direct-dns1" bind:value={dns1} placeholder="1.1.1.1" class="tnum" />
              </div>
              <div>
                <label for="net-direct-dns2" class="block text-sm font-medium mb-1.5"
                  >{t('networks.dns2')}</label
                >
                <Input id="net-direct-dns2" bind:value={dns2} placeholder="8.8.8.8" class="tnum" />
              </div>
            </div>
          </div>
        {:else}
          <div>
            <label for="net-cidr" class="block text-sm font-medium mb-1.5">CIDR</label>
            <Input id="net-cidr" bind:value={cidr} placeholder="192.168.100.0/24" />
            <p class="text-xs text-muted-foreground mt-1">
              {kind === 'nat' ? t('networks.natHelp') : t('networks.isolatedHelp')}
            </p>
          </div>
        {/if}
      {:else}
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label for="net-edit-kind" class="block text-sm font-medium mb-1.5"
              >{t('networks.forwardMode')}</label
            >
            <Input
              id="net-edit-kind"
              value={kind === 'direct'
                ? t('networks.direct')
                : kind === 'nat'
                  ? t('networks.nat')
                  : t('networks.isolated')}
              readonly
              class="opacity-50"
            />
          </div>
          {#if kind === 'direct'}
            <div>
              <label for="net-edit-iface" class="block text-sm font-medium mb-1.5"
                >{t('networks.directBoundTo')}</label
              >
              <Input
                id="net-edit-iface"
                value={directInterface || '—'}
                readonly
                class="opacity-50 tnum"
              />
            </div>
          {:else}
            <div>
              <label for="net-edit-cidr" class="block text-sm font-medium mb-1.5">CIDR</label>
              <Input id="net-edit-cidr" value={cidr} readonly class="opacity-50" />
            </div>
          {/if}
        </div>
        {#if kind === 'direct'}
          <label class="flex items-center gap-2 text-sm cursor-pointer">
            <input
              type="checkbox"
              bind:checked={vlanAware}
              class="w-4 h-4 rounded border-border bg-background text-accent focus:ring-accent"
            />
            {t('networks.vlanAwareLabel')}
          </label>

          <div class="pt-3 border-t border-border space-y-3">
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label for="net-edit-direct-cidr" class="block text-sm font-medium mb-1.5"
                  >{t('networks.ipSubnet')}</label
                >
                <Input
                  id="net-edit-direct-cidr"
                  value={cidr || '—'}
                  readonly
                  class="opacity-50 tnum"
                />
              </div>
              <div>
                <label for="net-edit-direct-gw" class="block text-sm font-medium mb-1.5"
                  >{t('networks.gateway')}</label
                >
                <Input
                  id="net-edit-direct-gw"
                  bind:value={gateway}
                  placeholder="192.168.1.1"
                  class="tnum"
                />
              </div>
            </div>
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label for="net-edit-direct-dns1" class="block text-sm font-medium mb-1.5"
                  >{t('networks.dns1')}</label
                >
                <Input
                  id="net-edit-direct-dns1"
                  bind:value={dns1}
                  placeholder="1.1.1.1"
                  class="tnum"
                />
              </div>
              <div>
                <label for="net-edit-direct-dns2" class="block text-sm font-medium mb-1.5"
                  >{t('networks.dns2')}</label
                >
                <Input
                  id="net-edit-direct-dns2"
                  bind:value={dns2}
                  placeholder="8.8.8.8"
                  class="tnum"
                />
              </div>
            </div>
          </div>
        {/if}
      {/if}

      {#if kind !== 'direct'}
        <div class="flex items-center gap-2 pt-2 border-t border-border">
          <input
            id="net-dhcp"
            type="checkbox"
            bind:checked={dhcp}
            class="w-4 h-4 rounded border-border bg-background text-accent focus:ring-accent"
          />
          <label for="net-dhcp" class="text-sm select-none cursor-pointer"
            >{t('networks.enableDhcp')}</label
          >
        </div>

        {#if dhcp}
          <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
            <div>
              <label for="net-gw" class="block text-sm font-medium mb-1.5"
                >{t('networks.gateway')}</label
              >
              <Input id="net-gw" value={preview?.gateway || ''} readonly class="opacity-50 tnum" />
            </div>
            <div>
              <label for="net-dhcp-start" class="block text-sm font-medium mb-1.5"
                >{t('networks.dhcpStart')}</label
              >
              <Input
                id="net-dhcp-start"
                bind:value={dhcpStart}
                placeholder={preview?.dhcpStart || ''}
                class="tnum"
              />
            </div>
            <div>
              <label for="net-dhcp-end" class="block text-sm font-medium mb-1.5"
                >{t('networks.dhcpEnd')}</label
              >
              <Input
                id="net-dhcp-end"
                bind:value={dhcpEnd}
                placeholder={preview?.dhcpEnd || ''}
                class="tnum"
              />
            </div>
          </div>
          <div>
            <label for="net-dns" class="block text-sm font-medium mb-1.5">
              {t('networks.dnsForwarders')}
              <span class="text-xs text-muted-foreground ml-1">{t('networks.dnsOptional')}</span>
            </label>
            <Input id="net-dns" bind:value={dnsText} placeholder="1.1.1.1, 8.8.8.8" />
            <p class="text-xs text-muted-foreground mt-1">{t('networks.dnsHelp')}</p>
          </div>

          <div class="pt-2 border-t border-border">
            <div class="flex items-start justify-between gap-3 mb-2">
              <div>
                <span class="text-sm font-medium">{t('networks.reservationsTitle')}</span>
                <p class="text-xs text-muted-foreground">{t('networks.reservationsDesc')}</p>
              </div>
              <Button size="sm" variant="outline" type="button" onclick={addReservation}>
                {t('networks.addReservation')}
              </Button>
            </div>
            {#if reservations.length === 0}
              <EmptyState compact icon="network" title={t('networks.noReservations')} />
            {:else}
              <div class="space-y-2">
                {#each reservations as r, i (i)}
                  <div class="grid grid-cols-1 sm:grid-cols-[1fr_1fr_1fr_auto] gap-2 items-end">
                    <div>
                      <label class="block text-[11px] text-muted-foreground mb-1" for="res-mac-{i}"
                        >{t('networks.resMac')}</label
                      >
                      <Input
                        id="res-mac-{i}"
                        bind:value={r.mac}
                        placeholder="52:54:00:aa:bb:cc"
                        class="font-mono"
                      />
                    </div>
                    <div>
                      <label class="block text-[11px] text-muted-foreground mb-1" for="res-ip-{i}"
                        >{t('networks.resIp')}</label
                      >
                      <Input
                        id="res-ip-{i}"
                        bind:value={r.ip}
                        placeholder={preview?.dhcpStart || '192.168.100.50'}
                        class="tnum"
                      />
                    </div>
                    <div>
                      <label class="block text-[11px] text-muted-foreground mb-1" for="res-name-{i}"
                        >{t('networks.resName')}</label
                      >
                      <Input
                        id="res-name-{i}"
                        bind:value={r.name}
                        placeholder={t('networks.resNameOptional')}
                      />
                    </div>
                    <button
                      type="button"
                      onclick={() => removeReservation(i)}
                      class="text-muted-foreground hover:text-destructive transition-colors p-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 rounded"
                      title={t('common.delete')}
                      aria-label={t('common.delete')}
                    >
                      <Icon name="trash" size={14} />
                    </button>
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        {/if}
      {/if}

      <div>
        <label for="net-mtu" class="block text-sm font-medium mb-1.5">
          {t('networks.mtu')}
          <span class="text-xs text-muted-foreground ml-1">{t('networks.mtuOptional')}</span>
        </label>
        <Input
          id="net-mtu"
          type="number"
          min="576"
          max="9216"
          bind:value={mtu}
          placeholder="1500"
          class="tnum"
        />
        <p class="text-xs text-muted-foreground mt-1">{t('networks.mtuHelp')}</p>
      </div>

      <div class="flex items-center gap-2 pt-2 border-t border-border">
        <input
          id="net-autostart"
          type="checkbox"
          bind:checked={autostart}
          class="w-4 h-4 rounded border-border bg-background text-accent focus:ring-accent"
        />
        <label for="net-autostart" class="text-sm select-none cursor-pointer"
          >{t('networks.startOnBoot')}</label
        >
      </div>

      <div class="flex gap-2 pt-1">
        {#if editingNet}
          <Button onclick={save} disabled={saving}
            >{saving ? t('networks.saving') : t('networks.saveChanges')}</Button
          >
        {:else}
          <Button onclick={create} disabled={saving}
            >{saving ? t('networks.creating') : t('common.create')}</Button
          >
        {/if}
        <Button variant="outline" onclick={resetForm} disabled={saving}>{t('common.cancel')}</Button
        >
      </div>
      {#if editingNet}
        <p class="text-xs text-warning pt-1">{t('networks.updateNote')}</p>
      {/if}
    </Card>
  {/if}

  {#if loading}
    <div class="flex items-center justify-center py-24"><Spinner size="lg" /></div>
  {:else if networks.length === 0}
    <EmptyState
      icon="network"
      title={t('networks.noNetworksConfigured')}
      description={t('networks.noNetworksHint')}
    >
      {#snippet action()}
        {#if auth.isAdmin()}
          <Button
            onclick={() => {
              resetForm();
              showCreate = true;
            }}>{t('networks.createNetwork')}</Button
          >
        {/if}
      {/snippet}
    </EmptyState>
  {:else}
    <div id="networks-table-anchor"></div>
    <DataTable
      columns={[
        { key: 'name', label: t('networks.name'), width: 'minmax(180px, 1fr)', render: nameCell },
        { key: 'kind', label: t('networks.forwardMode'), width: '110px', render: kindCell },
        { key: 'cidr', label: 'CIDR', width: '170px', render: cidrCell },
        { key: 'gateway', label: t('networks.gatewayCol'), width: '150px', render: gatewayCell },
        { key: 'dhcp', label: 'DHCP', width: '130px', render: dhcpCell },
        { key: 'actions', label: '', align: 'right', width: '160px', render: actionsCell },
      ]}
      rows={networks}
      rowKey="name"
      emptyMessage={t('networks.noNetworks')}
      emptyIcon="network"
    />
  {/if}

  <!-- Physical Host Interfaces Card -->
  {#if hostInterfaces.length > 0}
    <Card class="p-5 mt-6 space-y-4">
      <div class="flex items-center justify-between flex-wrap gap-2">
        <div class="flex items-center gap-2.5">
          <div class="p-2 rounded-xl bg-accent/10 text-accent">
            <Icon name="network" size={20} />
          </div>
          <div>
            <h2 class="text-sm font-semibold uppercase tracking-wider text-foreground">
              {t('networks.hostInterfacesTitle')}
            </h2>
            <p class="text-xs text-muted-foreground mt-0.5">
              {t('networks.hostInterfacesDesc')}
            </p>
          </div>
        </div>
        <div class="flex items-center gap-2">
          {#if auth.isAdmin()}
            <Button
              size="xs"
              variant="outline"
              onclick={() => {
                bondSlaves = [];
                showBondModal = true;
              }}
            >
              <Icon name="plus" size={12} class="mr-1" />
              {t('networks.createBondTitle')}
            </Button>
          {/if}
          <span class="text-xs font-mono px-2 py-0.5 rounded-full bg-muted text-muted-foreground">
            {hostInterfaces.length}
            {hostInterfaces.length === 1 ? 'NIC' : 'NICs'}
          </span>
        </div>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3 pt-1">
        {#each hostInterfaces as iface (iface.name)}
          <div
            class="p-4 rounded-xl border border-border/80 bg-background/50 hover:border-border transition-all flex flex-col justify-between space-y-3"
          >
            <div>
              <div class="flex items-center justify-between gap-2 mb-2">
                <div class="flex items-center gap-2 min-w-0">
                  <span
                    class="w-2.5 h-2.5 rounded-full shrink-0 {iface.state === 'up'
                      ? 'bg-success'
                      : 'bg-muted-foreground/40'}"
                  ></span>
                  <span class="font-mono font-semibold text-sm text-foreground truncate"
                    >{iface.name}</span
                  >
                </div>
                <div class="flex items-center gap-1.5 shrink-0">
                  {#if iface.type === 'bond'}
                    <span
                      class="text-[10px] uppercase font-semibold px-1.5 py-0.5 rounded bg-accent/15 text-accent"
                    >
                      BOND ({iface.bond_mode || 'active-backup'})
                    </span>
                  {:else if iface.type === 'wifi'}
                    <span
                      class="text-[10px] uppercase font-semibold px-1.5 py-0.5 rounded bg-warning/10 text-warning"
                    >
                      Wi-Fi
                    </span>
                  {:else}
                    <span
                      class="text-[10px] uppercase font-semibold px-1.5 py-0.5 rounded bg-info/10 text-info"
                    >
                      {iface.speed ? `${iface.speed}M` : 'Ethernet'}
                    </span>
                  {/if}
                </div>
              </div>

              <div class="space-y-1.5 text-xs text-muted-foreground">
                <div class="flex items-center justify-between">
                  <span>MAC:</span>
                  <span class="font-mono text-foreground text-[11px]">{iface.mac || '—'}</span>
                </div>
                {#if iface.slaves && iface.slaves.length > 0}
                  <div class="flex items-center justify-between">
                    <span>{t('networks.bondSlaves')}:</span>
                    <span class="font-mono text-accent font-semibold text-[11px]"
                      >{iface.slaves.join(', ')}</span
                    >
                  </div>
                {/if}
                {#if iface.driver}
                  <div class="flex items-center justify-between">
                    <span>{t('networks.ifaceDriver')}:</span>
                    <span class="font-mono text-foreground">{iface.driver}</span>
                  </div>
                {/if}
                <div class="flex items-center justify-between">
                  <span>{t('networks.ifaceMaster')}:</span>
                  {#if iface.master}
                    <span class="font-semibold text-accent font-mono">{iface.master}</span>
                  {:else}
                    <span class="text-warning text-[11px]">{t('networks.ifaceUnassigned')}</span>
                  {/if}
                </div>
                {#if iface.ipv4}
                  <div class="flex items-center justify-between">
                    <span>{t('networks.ifaceIP')}:</span>
                    <span class="font-mono font-semibold text-foreground text-[11px]"
                      >{iface.ipv4}</span
                    >
                  </div>
                {/if}
              </div>
            </div>

            <div
              class="pt-2 border-t border-border/50 flex flex-wrap items-center gap-1.5 justify-between"
            >
              {#if auth.isAdmin()}
                <div class="flex items-center gap-1">
                  <Button
                    size="xs"
                    variant="ghost"
                    onclick={() => toggleIfaceState(iface)}
                    class="text-[11px] h-7 px-2"
                    title={iface.state === 'up'
                      ? t('networks.toggleStateDown')
                      : t('networks.toggleStateUp')}
                  >
                    <Icon
                      name={iface.state === 'up' ? 'power' : 'zap'}
                      size={12}
                      class={iface.state === 'up' ? 'text-warning' : 'text-success'}
                    />
                    <span class="ml-1">{iface.state === 'up' ? 'Down' : 'Up'}</span>
                  </Button>

                  <Button
                    size="xs"
                    variant="ghost"
                    onclick={() => openEditIface(iface)}
                    class="text-[11px] h-7 px-2"
                    title={t('networks.configureIface')}
                  >
                    <Icon name="edit" size={12} />
                    <span class="ml-1">IP</span>
                  </Button>
                </div>
              {/if}

              {#if iface.type === 'bond' && auth.isAdmin()}
                <div class="flex items-center gap-1.5 ml-auto">
                  {#if iface.master}
                    <span class="text-[11px] text-muted-foreground flex items-center gap-1">
                      <Icon name="check" size={12} class="text-success" />
                      {iface.master}
                    </span>
                  {/if}
                  <Button
                    size="xs"
                    variant="outline"
                    onclick={() => deleteBond(iface.name)}
                    class="text-[11px] h-7 px-2 text-destructive hover:bg-destructive/10"
                    title={t('networks.deleteBond')}
                  >
                    <Icon name="trash" size={12} class="mr-1" />
                    {t('networks.deleteBond')}
                  </Button>
                </div>
              {:else if iface.master}
                <div class="flex items-center gap-1 ml-auto">
                  <span class="text-[11px] text-muted-foreground flex items-center gap-1">
                    <Icon name="check" size={12} class="text-success" />
                    {iface.master}
                  </span>
                  {#if auth.isAdmin()}
                    <Button
                      size="xs"
                      variant="outline"
                      onclick={() => isolateIface(iface)}
                      class="text-[11px] h-7 px-2 text-destructive hover:bg-destructive/10"
                      title={t('networks.isolateIface')}
                    >
                      <Icon name="unlink" size={12} class="mr-1" />
                      {t('networks.isolateIface')}
                    </Button>
                  {/if}
                </div>
              {:else if auth.isAdmin()}
                <div class="flex items-center gap-1.5 ml-auto">
                  <Button
                    size="xs"
                    variant="outline"
                    onclick={() => {
                      attachingIface = iface;
                      targetBridge = networks[0]?.name || 'vmbr0';
                    }}
                    class="text-xs h-7"
                    title={t('networks.attachToBridge')}
                  >
                    <Icon name="link" size={12} class="mr-1" />
                    {t('networks.attachToBridge')}
                  </Button>

                  <Button
                    size="xs"
                    variant="outline"
                    onclick={() => {
                      resetForm();
                      name = `br-${iface.name.slice(0, 8)}`;
                      kind = 'direct';
                      directInterface = iface.name;
                      showCreate = true;
                    }}
                    class="text-xs h-7"
                  >
                    <Icon name="plus" size={12} class="mr-1" />
                    {t('networks.createBridgeWithIface')}
                  </Button>
                </div>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    </Card>
  {/if}

  {#if viewingLeasesFor}
    <Card class="p-5 mt-4 space-y-3">
      <div class="flex items-center justify-between">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
          {t('networks.leasesFor', { name: viewingLeasesFor })}
        </h2>
        <Button variant="outline" onclick={() => (viewingLeasesFor = null)}>
          {t('common.close')}
        </Button>
      </div>
      {#if loadingLeases}
        <div class="flex items-center justify-center py-12"><Spinner size="lg" /></div>
      {:else}
        <DataTable
          columns={[
            { key: 'hostname', label: t('networks.leaseHostname'), render: leaseHostnameCell },
            { key: 'ip', label: t('networks.leaseIP'), render: leaseIPCell },
            { key: 'mac', label: t('networks.leaseMAC'), render: leaseMACCell },
            { key: 'expiry', label: t('networks.leaseExpiry'), render: leaseExpiryCell },
            { key: 'actions', label: '', align: 'right', render: leaseActionsCell },
          ]}
          rows={leases}
          rowKey="mac"
          emptyMessage={t('networks.noLeases')}
          emptyIcon="clock"
        />
      {/if}
    </Card>
  {/if}

  <!-- Configure Interface IP Modal -->
  {#if editingIface}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <Card class="w-full max-w-md p-6 space-y-4 shadow-xl">
        <div class="flex items-center justify-between border-b border-border pb-3">
          <div class="flex items-center gap-2">
            <Icon name="network" size={18} class="text-accent" />
            <h3 class="font-semibold text-foreground text-sm">
              {t('networks.configureIface')} — {editingIface.name}
            </h3>
          </div>
          <button
            onclick={() => (editingIface = null)}
            class="text-muted-foreground hover:text-foreground text-sm"
          >
            ✕
          </button>
        </div>

        <div class="space-y-3 text-sm">
          <div>
            <label for="iface-ip-input" class="block font-medium mb-1">
              {t('networks.ipSubnet')}
            </label>
            <Input
              id="iface-ip-input"
              bind:value={ifaceIPv4}
              placeholder="192.168.1.100/24 (o vacío para limpiar)"
              class="tnum font-mono"
            />
            <p class="text-xs text-muted-foreground mt-1">
              Introduce una dirección CIDR o déjalo vacío para limpiar IPs en esta tarjeta.
            </p>
          </div>

          <div>
            <label for="iface-gw-input" class="block font-medium mb-1">
              {t('networks.gateway')}
            </label>
            <Input
              id="iface-gw-input"
              bind:value={ifaceGateway}
              placeholder="192.168.1.1 (opcional)"
              class="tnum font-mono"
            />
          </div>
        </div>

        <div class="flex justify-end gap-2 pt-3 border-t border-border">
          <Button
            variant="outline"
            size="sm"
            onclick={() => (editingIface = null)}
            disabled={savingIface}
          >
            {t('common.cancel')}
          </Button>
          <Button size="sm" onclick={saveIfaceConfig} disabled={savingIface}>
            {savingIface ? t('common.saving') : t('common.save')}
          </Button>
        </div>
      </Card>
    </div>
  {/if}

  <!-- Create Bond Modal -->
  {#if showBondModal}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <Card class="w-full max-w-lg p-6 space-y-4 shadow-xl">
        <div class="flex items-center justify-between border-b border-border pb-3">
          <div class="flex items-center gap-2">
            <Icon name="network" size={18} class="text-accent" />
            <h3 class="font-semibold text-foreground text-sm">
              {t('networks.createBondTitle')}
            </h3>
          </div>
          <button
            onclick={() => (showBondModal = false)}
            class="text-muted-foreground hover:text-foreground text-sm"
          >
            ✕
          </button>
        </div>

        <p class="text-xs text-muted-foreground">
          {t('networks.createBondDesc')}
        </p>

        <div class="space-y-3 text-sm">
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label for="bond-name-input" class="block font-medium mb-1">
                {t('networks.bondName')}
              </label>
              <Input
                id="bond-name-input"
                bind:value={bondName}
                placeholder="bond0"
                class="font-mono"
              />
            </div>
            <div>
              <label for="bond-mode-select" class="block font-medium mb-1">
                {t('networks.bondMode')}
              </label>
              <select id="bond-mode-select" bind:value={bondMode} class="input">
                <option value="active-backup">{t('networks.bondModeActiveBackup')}</option>
                <option value="balance-rr">{t('networks.bondModeBalanceRR')}</option>
                <option value="802.3ad">{t('networks.bondMode8023ad')}</option>
                <option value="balance-xor">{t('networks.bondModeBalanceXOR')}</option>
              </select>
            </div>
          </div>

          <div>
            <label for="bond-bridge-select" class="block font-medium mb-1">
              {t('networks.bondBridge')}
            </label>
            <select id="bond-bridge-select" bind:value={bondBridge} class="input">
              <option value="">{t('networks.bondBridgeNone')}</option>
              {#each networks as net (net.name)}
                <option value={net.name}>{net.name} ({net.kind})</option>
              {/each}
            </select>
            <p class="text-[11px] text-muted-foreground mt-1">
              {t('networks.bondBridgeHelp')}
            </p>
          </div>

          <div>
            <span class="block font-medium mb-1">
              {t('networks.bondSlaves')}
            </span>
            <div class="grid grid-cols-2 gap-2 border border-border rounded-lg p-3 bg-muted/20">
              {#each hostInterfaces.filter((i) => i.type !== 'bond') as iface (iface.name)}
                <label class="flex items-center gap-2 cursor-pointer text-xs">
                  <input
                    type="checkbox"
                    value={iface.name}
                    checked={bondSlaves.includes(iface.name)}
                    onchange={(e) => {
                      if (e.target.checked) {
                        bondSlaves = [...bondSlaves, iface.name];
                      } else {
                        bondSlaves = bondSlaves.filter((n) => n !== iface.name);
                      }
                    }}
                    class="rounded border-border"
                  />
                  <span class="font-mono font-medium">{iface.name}</span>
                  <span class="text-[10px] text-muted-foreground">({iface.state})</span>
                </label>
              {/each}
            </div>
          </div>

          {#if bondBridge}
            <div
              class="p-3 rounded-lg bg-accent/10 border border-accent/20 text-xs text-foreground flex items-start gap-2.5"
            >
              <Icon name="info" size={16} class="text-accent shrink-0 mt-0.5" />
              <div>
                <p class="font-medium text-foreground">
                  {t('networks.bondAttachedBridgeNoteTitle')}
                </p>
                <p class="text-muted-foreground mt-0.5 leading-relaxed">
                  {t('networks.bondAttachedBridgeNoteDesc')}
                </p>
              </div>
            </div>
          {:else}
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label for="bond-ip-input" class="block font-medium mb-1">
                  {t('networks.ipSubnet')} (Opcional)
                </label>
                <Input
                  id="bond-ip-input"
                  bind:value={bondIPv4}
                  placeholder="192.168.1.80/24"
                  class="font-mono text-xs"
                />
              </div>
              <div>
                <label for="bond-gw-input" class="block font-medium mb-1">
                  {t('networks.gateway')} (Opcional)
                </label>
                <Input
                  id="bond-gw-input"
                  bind:value={bondGateway}
                  placeholder="192.168.1.1"
                  class="font-mono text-xs"
                />
              </div>
            </div>
          {/if}
        </div>

        <div class="flex justify-end gap-2 pt-3 border-t border-border">
          <Button
            variant="outline"
            size="sm"
            onclick={() => (showBondModal = false)}
            disabled={savingBond}
          >
            {t('common.cancel')}
          </Button>
          <Button size="sm" onclick={createBond} disabled={savingBond || bondSlaves.length < 2}>
            {savingBond ? t('common.creating') : t('common.create')}
          </Button>
        </div>
      </Card>
    </div>
  {/if}

  <!-- Attach Interface to Existing Bridge Modal -->
  {#if attachingIface}
    <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
      <Card class="w-full max-w-md p-6 space-y-4 shadow-xl">
        <div class="flex items-center justify-between border-b border-border pb-3">
          <div class="flex items-center gap-2">
            <Icon name="link" size={18} class="text-accent" />
            <h3 class="font-semibold text-foreground text-sm">
              {t('networks.attachToBridgeTitle', { name: attachingIface.name })}
            </h3>
          </div>
          <button
            onclick={() => (attachingIface = null)}
            class="text-muted-foreground hover:text-foreground text-sm"
          >
            ✕
          </button>
        </div>

        <div class="space-y-3 text-sm">
          <div>
            <label for="target-bridge-select" class="block font-medium mb-1">
              {t('networks.selectBridge')}
            </label>
            <select id="target-bridge-select" bind:value={targetBridge} class="input">
              {#each networks as net (net.name)}
                <option value={net.name}>{net.name} ({net.kind})</option>
              {/each}
            </select>
            <p class="text-xs text-muted-foreground mt-1.5">
              La interfaz física se incorporará como puerto esclavo del puente seleccionado (ej. {targetBridge}).
            </p>
            <div
              class="mt-2.5 p-2.5 rounded border border-amber-500/30 bg-amber-500/10 text-amber-500 text-xs space-y-1"
            >
              <span class="font-semibold block">⚠️ Advertencia de Bucles / STP:</span>
              <p>
                Si ambas tarjetas van conectadas al mismo switch o router, se activará Spanning Tree
                (STP) para evitar bucles. Para balanceo de carga real o tolerancia a fallos
                (failover), la opción recomendada es utilizar <strong
                  >"Crear Enlace Agregado / Bond"</strong
                > y asociarlo al puente.
              </p>
            </div>
          </div>
        </div>

        <div class="flex justify-end gap-2 pt-3 border-t border-border">
          <Button
            variant="outline"
            size="sm"
            onclick={() => (attachingIface = null)}
            disabled={savingAttach}
          >
            {t('common.cancel')}
          </Button>
          <Button size="sm" onclick={attachIfaceToBridge} disabled={savingAttach || !targetBridge}>
            {savingAttach ? t('common.saving') : t('networks.attachToBridge')}
          </Button>
        </div>
      </Card>
    </div>
  {/if}
</div>

{#snippet nameCell(row)}
  <div class="flex items-center gap-2.5 min-w-0">
    <div
      class="w-8 h-8 rounded-lg shrink-0 flex items-center justify-center {row.active
        ? 'bg-success/10 text-success'
        : 'bg-muted text-muted-foreground'}"
    >
      <Icon name="network" size={16} />
    </div>
    <div class="min-w-0">
      <div class="font-medium truncate flex items-center gap-2">
        {row.name}
        {#if row.protected}
          <span class="shrink-0" title={t('networks.managedTooltip')}>
            <Icon name="lock" size={14} class="text-info" />
          </span>
        {/if}
      </div>
      <!-- flex-wrap: on a narrow grid track the "autostart" pill used to
           spill over into the forward-mode column (e.g. "enp1s0"). -->
      <div class="flex flex-wrap items-center gap-1.5 mt-1">
        <span
          class="inline-flex items-center gap-1 text-[10px] px-1.5 py-0.5 rounded-full font-medium whitespace-nowrap {row.active
            ? 'bg-success/10 text-success'
            : 'bg-muted text-muted-foreground'}"
        >
          <span class="w-1.5 h-1.5 rounded-full {row.active ? 'bg-success' : 'bg-muted-foreground'}"
          ></span>
          {row.active ? t('networks.activeBadge') : t('networks.inactiveBadge')}
        </span>
        {#if row.autostart}
          <span
            class="inline-flex items-center text-[10px] px-1.5 py-0.5 rounded-full bg-accent/10 text-accent font-medium whitespace-nowrap"
          >
            <Icon name="zap" size={10} class="mr-0.5" />
            {t('networks.autostartBadge')}
          </span>
        {/if}
      </div>
    </div>
  </div>
{/snippet}

{#snippet kindCell(row)}
  <span
    class="inline-flex items-center text-xs px-2 py-1 rounded-full font-medium {row.kind === 'nat'
      ? 'bg-info/10 text-info'
      : row.kind === 'direct'
        ? 'bg-accent/10 text-accent'
        : 'bg-muted text-muted-foreground'}"
  >
    {#if row.kind === 'nat'}
      <Icon name="arrowRight" size={12} class="mr-1" />
    {:else if row.kind === 'direct'}
      <Icon name="network" size={12} class="mr-1" />
    {/if}
    {row.kind === 'nat'
      ? t('networks.nat')
      : row.kind === 'direct'
        ? t('networks.direct')
        : t('networks.isolated')}
  </span>
  {#if row.kind === 'direct' && row.interface}
    <div class="text-[11px] text-muted-foreground font-mono tnum mt-0.5">{row.interface}</div>
  {/if}
{/snippet}

{#snippet cidrCell(row)}
  {#if row.cidr}
    <span class="font-mono text-xs text-foreground tnum px-2 py-1 rounded bg-muted/50 inline-block"
      >{row.cidr}</span
    >
  {:else}
    <span class="text-xs text-muted-foreground">—</span>
  {/if}
{/snippet}

{#snippet gatewayCell(row)}
  {#if row.gateway}
    <span class="font-mono text-xs text-foreground tnum">{row.gateway}</span>
  {:else}
    <span class="text-xs text-muted-foreground">—</span>
  {/if}
{/snippet}

{#snippet dhcpCell(row)}
  {#if row.dhcp}
    <div class="flex items-center gap-2">
      <span class="relative inline-flex w-8 h-4 rounded-full bg-success/20">
        <span
          class="absolute top-0.5 right-0.5 w-3 h-3 rounded-full bg-success transition-transform"
        ></span>
      </span>
      <div class="min-w-0">
        <div class="text-xs font-medium text-success">{t('networks.dhcpEnabled')}</div>
        {#if row.dhcp_start && row.dhcp_end}
          <div class="text-[11px] text-muted-foreground font-mono tnum truncate">
            {row.dhcp_start} – {row.dhcp_end}
          </div>
        {/if}
      </div>
    </div>
  {:else}
    <div class="flex items-center gap-2">
      <span class="relative inline-flex w-8 h-4 rounded-full bg-muted">
        <span
          class="absolute top-0.5 left-0.5 w-3 h-3 rounded-full bg-muted-foreground/60 transition-transform"
        ></span>
      </span>
      <span class="text-xs text-muted-foreground">{t('networks.dhcpDisabled')}</span>
    </div>
  {/if}
{/snippet}

{#snippet actionsCell(row)}
  <div class="flex items-center justify-end gap-1">
    {#if row.active}
      <button
        onclick={() => toggleNet(row)}
        disabled={toggling[row.name] || row.protected}
        class="p-1.5 rounded-md text-warning hover:bg-warning/10 transition-colors disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        aria-label={`${t('networks.stop')} ${row.name}`}
        title={t('networks.stop')}
      >
        {#if toggling[row.name]}
          <Spinner size="xs" color="text-warning" />
        {:else}
          <Icon name="pause" size={16} />
        {/if}
      </button>
    {:else}
      <button
        onclick={() => toggleNet(row)}
        disabled={toggling[row.name]}
        class="p-1.5 rounded-md text-success hover:bg-success/10 transition-colors disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        aria-label={`${t('networks.start')} ${row.name}`}
        title={t('networks.start')}
      >
        {#if toggling[row.name]}
          <Spinner size="xs" color="text-success" />
        {:else}
          <Icon name="play" size={16} />
        {/if}
      </button>
    {/if}
    {#if row.kind !== 'direct' && row.dhcp}
      <button
        onclick={() => viewLeases(row)}
        class="p-1.5 rounded-md text-muted-foreground hover:text-accent hover:bg-muted transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        aria-label={`${t('networks.viewLeases')} ${row.name}`}
        title={t('networks.viewLeases')}
      >
        <Icon name="users" size={16} />
      </button>
    {/if}
    <button
      onclick={() => startEdit(row)}
      disabled={!auth.isAdmin()}
      class="p-1.5 rounded-md text-muted-foreground hover:text-accent hover:bg-muted transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:text-muted-foreground disabled:hover:bg-transparent"
      aria-label={`${t('common.edit')} ${row.name}`}
      title={auth.isAdmin() ? t('common.edit') : t('networks.networkAdminOnly')}
    >
      <Icon name="pencil" size={16} />
    </button>
    <button
      onclick={() => deleteNet(row.name)}
      disabled={row.protected || !auth.isAdmin()}
      class="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:text-muted-foreground disabled:hover:bg-transparent"
      aria-label={`${t('common.delete')} ${row.name}`}
      title={!auth.isAdmin()
        ? t('networks.networkAdminOnly')
        : row.protected
          ? t('networks.managedDeleteTooltip2', { name: row.name })
          : t('common.delete')}
    >
      <Icon name="trash" size={16} />
    </button>
  </div>
{/snippet}

{#snippet leaseHostnameCell(row)}
  <span class="text-sm">{row.hostname || '—'}</span>
{/snippet}

{#snippet leaseIPCell(row)}
  <span class="font-mono text-xs tnum">{row.ip}</span>
{/snippet}

{#snippet leaseMACCell(row)}
  <span class="font-mono text-xs tnum text-muted-foreground">{row.mac}</span>
{/snippet}

{#snippet leaseExpiryCell(row)}
  <span class="text-xs tnum">{new Date(row.expiry).toLocaleString()}</span>
{/snippet}

{#snippet leaseActionsCell(row)}
  <button
    onclick={() => releaseLease(row)}
    class="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
    aria-label={`${t('networks.release')} ${row.ip}`}
    title={t('networks.release')}
  >
    <Icon name="trash" size={16} />
  </button>
{/snippet}

<ConfirmDialog
  bind:open={confirmState.open}
  title={confirmState.title}
  description={confirmState.description}
  confirmLabel={confirmState.confirmLabel}
  variant={confirmState.variant}
  loading={confirmState.loading}
  onConfirm={confirmState.onConfirm}
/>
