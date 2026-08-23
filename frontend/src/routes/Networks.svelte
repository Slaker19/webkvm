<script>
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { toast, dismiss } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import DataTable from '$lib/components/DataTable.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { t } from '../lib/i18n.svelte.js';

  let networks = $state([]);
  let hostInterfaces = $state([]);
  let hostBridges = $state([]);
  let loading = $state(true);
  let error = $state('');
  let showCreate = $state(false);
  let showBridgeCreate = $state(false);
  let editingNet = $state(null);
  let name = $state('');
  let cidr = $state('192.168.100.0/24');
  let forward = $state('nat');
  let hostDevice = $state('');
  let dhcp = $state(true);
  let dhcpStart = $state('');
  let dhcpEnd = $state('');
  let dnsText = $state('');
  let autostart = $state(true);
  let saving = $state(false);
  let toggling = $state({});
  // Linux-bridge creation form state.
  let bridgeName = $state('br0');
  let bridgeInterface = $state('');
  let bridgeMoveIP = $state(true);
  let bridgeVLanAware = $state(false);
  let bridgeSaving = $state(false);
  let bridgeError = $state('');

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

  onMount(() => load());

  $effect(() => {
    if (preview && dhcp) {
      if (!dhcpStart) dhcpStart = preview.dhcpStart;
      if (!dhcpEnd) dhcpEnd = preview.dhcpEnd;
    }
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

  function resetForm() {
    name = '';
    cidr = '192.168.100.0/24';
    forward = 'nat';
    hostDevice = '';
    dhcp = true;
    dhcpStart = '';
    dhcpEnd = '';
    dnsText = '';
    autostart = true;
    editingNet = null;
    showCreate = false;
  }

  function startEdit(net) {
    editingNet = net.name;
    cidr = net.cidr || '';
    forward = net.forward || 'nat';
    hostDevice = net.bridge || '';
    dhcp = !!net.dhcp;
    dhcpStart = net.dhcp_start || '';
    dhcpEnd = net.dhcp_end || '';
    dnsText = formatDNSList(net.dns);
    autostart = !!net.autostart;
    showCreate = true;
  }

  async function load() {
    loading = true;
    error = '';
    try {
      networks = await api.listNetworks();
      // Also refresh the Linux bridge list whenever the
      // page reloads, so the user can see their current
      // bridges without having to open the create dialog.
      loadHostBridges();
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  async function loadHostInterfaces() {
    try {
      hostInterfaces = await api.listHostInterfaces();
    } catch {
      hostInterfaces = [];
    }
  }

  async function loadHostBridges() {
    try {
      hostBridges = await api.listHostBridges();
    } catch {
      hostBridges = [];
    }
  }

  $effect(() => {
    if (showCreate) {
      if (hostInterfaces.length === 0) loadHostInterfaces();
      if (hostBridges.length === 0) loadHostBridges();
    }
    if (showBridgeCreate && hostInterfaces.length === 0) loadHostInterfaces();
  });

  // When the user picks forward=bridge, auto-select the first
  // available Linux bridge so the form is immediately submittable.
  // Without this, the user has to click the dropdown after the
  // list loads (which can be a flash of empty state on first
  // open) — easy to miss and produces a silent "Select a host
  // bridge for bridge mode" error if they hit Create.
  $effect(() => {
    if (forward === 'bridge' && !hostDevice && hostBridges.length > 0) {
      hostDevice = hostBridges[0].name;
    }
  });

  async function create() {
    // Defensive validation: surface a sticky toast + inline alert
    // instead of silently returning. The user reported that
    // clicking Create "did nothing" — that's exactly what the
    // old `if (!name) return;` did when the Name input was
    // empty (or its bind hadn't fired yet on the first click).
    if (!name || !name.trim()) {
      const msg = t('networks.nameRequired');
      error = msg;
      toast.error(msg, { duration: 0 });
      return;
    }
    if (forward === 'bridge' && !hostDevice) {
      const msg = t('networks.selectBridgeError');
      error = msg;
      toast.error(msg, { duration: 0 });
      return;
    }
    if (forward !== 'bridge' && cidr && !cidr.includes('/')) {
      const msg = t('networks.cidrPrefixError');
      error = msg;
      toast.error(msg, { duration: 0 });
      return;
    }
    error = '';
    saving = true;
    // For bridge-mode networks, the backend silently drops
    // cidr/dhcp/dhcp_start/dhcp_end because libvirt rejects
    // <forward mode='bridge'/> networks that carry an <ip>
    // block. Don't even send them — keeps the request body
    // clean and makes the network's intent obvious in
    // /api/networks output (cidr="" for bridged networks).
    const payload =
      forward === 'bridge'
        ? { name: name.trim(), forward, autostart, bridge: hostDevice, dns: parseDNSList(dnsText) }
        : { name: name.trim(), cidr, forward, dhcp, autostart };
    if (forward !== 'bridge' && dhcp) {
      payload.dhcp_start = dhcpStart || preview?.dhcpStart || '';
      payload.dhcp_end = dhcpEnd || preview?.dhcpEnd || '';
    }
    if (forward !== 'bridge' && dnsText) {
      payload.dns = parseDNSList(dnsText);
    }
    try {
      const created = await api.createNetwork(payload);
      // Don't reset the form on success — the user reported
      // the create flow "did nothing" because the form
      // cleared. Keep the values visible so it's obvious
      // what was just submitted, and include the network
      // name in the toast so they can confirm. They can
      // close the form manually with Cancel when done.
      const label = (created && created.name) || name;
      toast.success(t('networks.networkCreated', { label }), { duration: 6000 });
      await load();
      // Scroll the table into view so the new row is on
      // screen even if the user was scrolled up reading
      // the form.
      document
        .getElementById('networks-table-anchor')
        ?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    } catch (e) {
      // Sticky (duration: 0) so the user doesn't miss the
      // libvirt error message if they look away — the default
      // 3.5s toast was easy to miss and the message ("network
      // with forward mode='bridge' cannot have <ip>") needs to
      // be read.
      console.error('[Networks] create failed', { payload, error: e });
      error = e.message;
      toast.error(e.message, { duration: 0 });
    } finally {
      saving = false;
    }
  }

  async function createBridge() {
    bridgeError = '';
    if (!bridgeName) {
      bridgeError = t('networks.bridgeNameRequired');
      return;
    }
    // Warn loudly when the operator is about to promote a DHCP
    // lease onto a permanent Linux bridge. The lease can change
    // on renewal; if the router doesn't know the new IP is taken
    // (i.e. no DHCP reservation), it'll hand it to some other
    // device. The backend's `move_ip: true` does the right thing
    // — the warning is to make sure the operator also did the
    // router-side reservation.
    if (bridgeMoveIP && bridgeInterface) {
      const iface = hostInterfaces.find((i) => i.name === bridgeInterface);
      if (iface && iface.ip_source === 'dhcp') {
        const msg = t('networks.dhcpMoveWarning', {
          iface: iface.name,
          mac: iface.mac,
        });
        bridgeError = msg;
        toast.warning(msg, { duration: 0 });
        bridgeSaving = false;
        return;
      }
    }
    bridgeSaving = true;
    try {
      await api.createHostBridge({
        name: bridgeName,
        interface: bridgeInterface,
        move_ip: bridgeMoveIP,
        vlan_aware: bridgeVLanAware,
      });
      toast.success(t('networks.bridgeCreated', { name: bridgeName }));
      showBridgeCreate = false;
      bridgeName = 'br0';
      bridgeInterface = '';
      bridgeMoveIP = true;
      bridgeVLanAware = false;
      await loadHostBridges();
      // Re-pick the new bridge as the host device in the
      // open create-network form, so the user just has to
      // hit Create.
      hostDevice = bridgeName;
    } catch (e) {
      bridgeError = e.message;
      // Also surface as a sticky toast in case the inline
      // Alert is scrolled out of view.
      toast.error(e.message, { duration: 0 });
    } finally {
      bridgeSaving = false;
    }
  }

  async function toggleVLanAware(br) {
    try {
      await api.setHostBridgeVLanAware(br.name, !br.vlan_aware);
      await loadHostBridges();
      toast.success(
        t('networks.vlanFiltering', {
          state: !br.vlan_aware ? t('networks.enabled') : t('networks.disabled'),
          name: br.name,
        })
      );
    } catch (e) {
      toast.error(e.message);
    }
  }

  async function deleteBridge(name) {
    askConfirm({
      title: t('networks.deleteBridgeTitle', { name }),
      description: t('networks.deleteBridgeDesc'),
      confirmLabel: t('networks.deleteBridge'),
      onConfirm: async () => {
        // Close the dialog immediately + show an info toast
        // while we run the operation. The user reported that
        // the previous version "stayed thinking" because the
        // dialog stayed open with a spinner and no other
        // feedback. Now the dialog goes away instantly and
        // the user sees the page state (the bridge is gone
        // or a sticky error appears).
        confirmState.open = false;
        const pendingId = toast.info(t('networks.deletingBridge', { name }), { duration: 0 });
        try {
          await api.deleteHostBridge(name);
          dismiss(pendingId);
          toast.success(t('networks.bridgeDeleted', { name }), { duration: 4000 });
          await loadHostBridges();
        } catch (e) {
          dismiss(pendingId);
          toast.error(t('networks.deleteBridgeFailed', { name, error: e.message }), {
            duration: 0,
          });
        }
      },
    });
  }

  async function save() {
    if (!editingNet) return;
    error = '';
    saving = true;
    const payload = { dhcp, autostart };
    if (dhcp) {
      payload.dhcp_start = dhcpStart || preview?.dhcpStart || '';
      payload.dhcp_end = dhcpEnd || preview?.dhcpEnd || '';
    }
    payload.dns = parseDNSList(dnsText);
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
        // Close the dialog immediately so the user sees the
        // page state change. Show an info toast while the
        // operation runs, then a success/error toast.
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

<div class="p-6 max-w-6xl">
  <PageHeader title={t('networks.title')} subtitle={t('networks.subtitle')}>
    {#snippet actions()}
      {#if !showCreate}
        <Button
          onclick={() => {
            resetForm();
            showCreate = true;
          }}>{t('networks.createNetwork')}</Button
        >
      {/if}
    {/snippet}
  </PageHeader>

  {#if hostBridges.length > 0}
    <div class="mb-4 border border-border rounded-lg bg-card p-4">
      <div class="flex items-center justify-between mb-2">
        <div>
          <h3 class="text-sm font-semibold">{t('networks.hostBridges')}</h3>
          <p class="text-xs text-muted-foreground mt-0.5">{t('networks.bridgeDesc')}</p>
        </div>
        <Button
          size="sm"
          variant="outline"
          onclick={() => {
            showBridgeCreate = !showBridgeCreate;
            if (showBridgeCreate && hostInterfaces.length === 0) loadHostInterfaces();
          }}
        >
          {showBridgeCreate ? t('common.cancel') : t('networks.newBridge')}
        </Button>
      </div>
      {#if showBridgeCreate}
        <div class="border border-border rounded-md p-3 space-y-3 bg-muted/30 mt-2">
          {#if bridgeError}
            <Alert variant="error">{bridgeError}</Alert>
          {/if}
          <div class="grid grid-cols-2 gap-3">
            <div>
              <label for="br-name-list" class="block text-xs font-medium mb-1"
                >{t('networks.bridgeName')}</label
              >
              <Input id="br-name-list" bind:value={bridgeName} placeholder="br0" />
            </div>
            <div>
              <label for="br-iface-list" class="block text-xs font-medium mb-1"
                >{t('networks.physicalInterface')}</label
              >
              <select id="br-iface-list" bind:value={bridgeInterface} class="input">
                <option value="">{t('networks.noneEmptyBridge')}</option>
                {#each hostInterfaces as iface}
                  <option value={iface.name}
                    >{iface.name}
                    {iface.type !== 'other' ? `(${iface.type})` : ''} — {iface.state}</option
                  >
                {/each}
              </select>
            </div>
          </div>
          <label class="flex items-start gap-2 text-xs text-muted-foreground cursor-pointer">
            <input
              type="checkbox"
              bind:checked={bridgeMoveIP}
              class="mt-0.5 w-4 h-4 rounded border-border bg-background text-accent focus:ring-accent"
            />
            <span>
              <span class="text-foreground font-medium"
                >{t('networks.moveIpLabel', {
                  slave: bridgeInterface || t('networks.slaveInterface'),
                  bridge: bridgeName || 'br0',
                })}</span
              >
              <br />
              {t('networks.moveIpRecommended')}
            </span>
          </label>
          <label class="flex items-start gap-2 text-xs text-muted-foreground cursor-pointer">
            <input
              type="checkbox"
              bind:checked={bridgeVLanAware}
              class="mt-0.5 w-4 h-4 rounded border-border bg-background text-accent focus:ring-accent"
            />
            <span>
              <span class="text-foreground font-medium">{t('networks.vlanAwareLabel')}</span>
              <br />
              {@html t('networks.vlanAwareDesc', {
                code: '<code class="text-[10px]">vlan_filtering=1</code>',
              })}
            </span>
          </label>
          <div class="flex justify-end">
            <Button size="sm" onclick={createBridge} disabled={bridgeSaving || !bridgeName}>
              {#if bridgeSaving}<Spinner size="sm" color="text-white" />{:else}{t(
                  'networks.createBridgeButton'
                )}{/if}
            </Button>
          </div>
        </div>
      {/if}
      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-2 mt-3">
        {#each hostBridges as br (br.name)}
          <div class="flex items-center justify-between border border-border rounded-md px-3 py-2">
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-1.5">
                <span class="text-sm font-medium tnum">{br.name}</span>
                {#if br.protected}
                  <span
                    class="text-[10px] px-1.5 py-0.5 rounded border border-info/30 bg-info/10 text-info uppercase tracking-wide"
                    title={t('networks.managedTooltip')}>{t('networks.managed')}</span
                  >
                {/if}
                {#if br.vlan_aware}
                  <span
                    class="text-[10px] px-1.5 py-0.5 rounded border border-accent/30 bg-accent/10 text-accent uppercase tracking-wide"
                    title="vlan_filtering=1 on this bridge">{t('networks.vlanAwareBadge')}</span
                  >
                {/if}
              </div>
              <div class="text-xs text-muted-foreground truncate">
                {br.ip || t('networks.noIp')} · {t('networks.ports', {
                  n: br.slaves?.length || 0,
                  s: (br.slaves?.length || 0) === 1 ? '' : 's',
                })}{br.slaves?.length ? ` (${br.slaves.join(', ')})` : ''}
              </div>
            </div>
            <div class="flex items-center gap-1">
              <Button
                size="xs"
                variant="outline"
                onclick={() => toggleVLanAware(br)}
                title={br.vlan_aware
                  ? t('networks.disableVlanFiltering')
                  : t('networks.enableVlanFiltering')}
              >
                {br.vlan_aware ? t('networks.vlanOn') : t('networks.vlanOff')}
              </Button>
              <button
                type="button"
                onclick={() => deleteBridge(br.name)}
                disabled={br.protected}
                class="text-muted-foreground hover:text-destructive transition-colors p-1 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 rounded disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:text-muted-foreground"
                title={br.protected
                  ? t('networks.managedDeleteTooltip', { name: br.name })
                  : t('networks.deleteBridgeTooltip')}
                aria-label={`${t('networks.deleteBridge')} ${br.name}`}
              >
                <svg
                  class="w-3.5 h-3.5"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  viewBox="0 0 24 24"
                  ><path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6M1 7h22M9 3h6a1 1 0 011 1v3H8V4a1 1 0 011-1z"
                  /></svg
                >
              </button>
            </div>
          </div>
        {/each}
      </div>
    </div>
  {/if}

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if showCreate}
    <div class="border border-border rounded-lg bg-card p-5 mb-4 space-y-3">
      <div class="flex items-center justify-between">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
          {editingNet ? t('networks.editNetwork', { name: editingNet }) : t('networks.newNetwork')}
        </h2>
        {#if editingNet}
          <span class="text-xs text-muted-foreground">{t('networks.cannotChange')}</span>
        {/if}
      </div>

      {#if !editingNet}
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label for="net-name" class="block text-sm font-medium mb-1.5"
              >{t('networks.name')}</label
            >
            <Input id="net-name" bind:value={name} placeholder="my-network" />
          </div>
          <div>
            <label for="net-forward" class="block text-sm font-medium mb-1.5"
              >{t('networks.forwardMode')}</label
            >
            <select id="net-forward" bind:value={forward} class="input">
              <option value="nat">NAT</option>
              <option value="bridge">Bridge</option>
              <option value="isolated">{t('networks.isolated')}</option>
            </select>
          </div>
        </div>
        {#if forward === 'bridge'}
          <div>
            <label for="net-host-device" class="block text-sm font-medium mb-1.5"
              >{t('networks.linuxBridge')}</label
            >
            <select id="net-host-device" bind:value={hostDevice} class="input">
              <option value="" disabled>{t('networks.selectBridge')}</option>
              {#each hostBridges as br}
                <option value={br.name}
                  >{br.name}{br.ip ? ` (${br.ip})` : ''} — {t('networks.ports', {
                    n: br.slaves?.length || 0,
                    s: br.slaves?.length === 1 ? '' : 's',
                  })}</option
                >
              {/each}
            </select>
            <p class="text-xs text-muted-foreground mt-1">{t('networks.bridgeHelp')}</p>
            {#if hostBridges.length === 0}
              <button
                type="button"
                onclick={() => (showBridgeCreate = !showBridgeCreate)}
                class="mt-2 text-xs text-accent hover:underline"
              >
                {showBridgeCreate
                  ? t('networks.hideBridgeCreator')
                  : t('networks.createBridgeFirst')}
              </button>
              {#if showBridgeCreate}
                <div class="mt-3 border border-border rounded-md p-3 space-y-3 bg-muted/30">
                  {#if bridgeError}
                    <Alert variant="error">{bridgeError}</Alert>
                  {/if}
                  <div class="grid grid-cols-2 gap-3">
                    <div>
                      <label for="br-name" class="block text-xs font-medium mb-1"
                        >{t('networks.bridgeName')}</label
                      >
                      <Input id="br-name" bind:value={bridgeName} placeholder="br0" />
                    </div>
                    <div>
                      <label for="br-iface" class="block text-xs font-medium mb-1"
                        >{t('networks.physicalInterface')}</label
                      >
                      <select id="br-iface" bind:value={bridgeInterface} class="input">
                        <option value="">{t('networks.noneEmptyBridge')}</option>
                        {#each hostInterfaces as iface}
                          <option value={iface.name}
                            >{iface.name}
                            {iface.type !== 'other' ? `(${iface.type})` : ''} — {iface.state}</option
                          >
                        {/each}
                      </select>
                    </div>
                  </div>
                  <label
                    class="flex items-start gap-2 text-xs text-muted-foreground cursor-pointer"
                  >
                    <input
                      type="checkbox"
                      bind:checked={bridgeMoveIP}
                      class="mt-0.5 w-4 h-4 rounded border-border bg-background text-accent focus:ring-accent"
                    />
                    <span>
                      <span class="text-foreground font-medium"
                        >{t('networks.moveIpLabel', {
                          slave: bridgeInterface || t('networks.slaveInterface'),
                          bridge: bridgeName || 'br0',
                        })}</span
                      >
                      <br />
                      {t('networks.moveIpRecommended2')}
                    </span>
                  </label>
                  <div class="flex justify-end gap-2">
                    <Button variant="outline" size="sm" onclick={() => (showBridgeCreate = false)}
                      >{t('common.cancel')}</Button
                    >
                    <Button size="sm" onclick={createBridge} disabled={bridgeSaving || !bridgeName}>
                      {#if bridgeSaving}<Spinner size="sm" color="text-white" />{:else}{t(
                          'networks.createBridgeButton'
                        )}{/if}
                    </Button>
                  </div>
                </div>
              {/if}
            {/if}
          </div>
        {:else}
          <div>
            <label for="net-cidr" class="block text-sm font-medium mb-1.5">CIDR</label>
            <Input id="net-cidr" bind:value={cidr} placeholder="192.168.100.0/24" />
          </div>
        {/if}
      {:else}
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label for="net-edit-forward" class="block text-sm font-medium mb-1.5"
              >{t('networks.forwardMode')}</label
            >
            <Input id="net-edit-forward" value={forward} readonly class="opacity-50" />
          </div>
          {#if forward === 'bridge'}
            <div>
              <label for="net-edit-bridge" class="block text-sm font-medium mb-1.5"
                >{t('networks.bridgedTo')}</label
              >
              <Input
                id="net-edit-bridge"
                value={hostDevice || '—'}
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
      {/if}

      {#if forward !== 'bridge'}
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
          <div class="grid grid-cols-3 gap-3">
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
        {/if}
      {/if}

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
    </div>
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
        <Button
          onclick={() => {
            resetForm();
            showCreate = true;
          }}>{t('networks.createNetwork')}</Button
        >
      {/snippet}
    </EmptyState>
  {:else}
    <div id="networks-table-anchor"></div>
    <DataTable
      columns={[
        { key: 'name', label: t('networks.name'), render: nameCell },
        { key: 'forward', label: t('networks.forward'), width: '110px', render: forwardCell },
        { key: 'cidr', label: 'CIDR', width: '170px', render: cidrCell },
        { key: 'gateway', label: t('networks.gatewayCol'), width: '150px', render: gatewayCell },
        { key: 'dhcp', label: 'DHCP', width: '130px', render: dhcpCell },
        { key: 'actions', label: '', align: 'right', width: 'auto', render: actionsCell },
      ]}
      rows={networks}
      rowKey="name"
      emptyMessage={t('networks.noNetworks')}
    />
  {/if}
</div>

{#snippet nameCell(row)}
  <div class="flex items-center gap-2.5 min-w-0">
    <div
      class="w-8 h-8 rounded-lg shrink-0 flex items-center justify-center {row.active
        ? 'bg-success/10 text-success'
        : 'bg-muted text-muted-foreground'}"
    >
      <svg class="w-4 h-4" fill="none" stroke="currentColor" stroke-width="1.5" viewBox="0 0 24 24">
        <rect x="4" y="2" width="16" height="8" rx="2" /><rect
          x="4"
          y="14"
          width="16"
          height="8"
          rx="2"
        /><line x1="12" y1="10" x2="12" y2="14" />
      </svg>
    </div>
    <div class="min-w-0">
      <div class="font-medium truncate flex items-center gap-2">
        {row.name}
        {#if row.protected}
          <svg
            class="w-3.5 h-3.5 text-info shrink-0"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            viewBox="0 0 24 24"
            title={t('networks.managedTooltip')}
          >
            <rect x="3" y="11" width="18" height="11" rx="2" /><path d="M7 11V7a5 5 0 0 1 10 0v4" />
          </svg>
        {/if}
      </div>
      <div class="flex items-center gap-1.5 mt-1">
        <span
          class="inline-flex items-center gap-1 text-[10px] px-1.5 py-0.5 rounded-full font-medium {row.active
            ? 'bg-success/10 text-success'
            : 'bg-muted text-muted-foreground'}"
        >
          <span class="w-1.5 h-1.5 rounded-full {row.active ? 'bg-success' : 'bg-muted-foreground'}"
          ></span>
          {row.active ? t('networks.activeBadge') : t('networks.inactiveBadge')}
        </span>
        {#if row.autostart}
          <span
            class="inline-flex items-center text-[10px] px-1.5 py-0.5 rounded-full bg-accent/10 text-accent font-medium"
          >
            <svg
              class="w-2.5 h-2.5 mr-0.5"
              fill="none"
              stroke="currentColor"
              stroke-width="2.5"
              viewBox="0 0 24 24"><polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2" /></svg
            >
            {t('networks.autostartBadge')}
          </span>
        {/if}
      </div>
    </div>
  </div>
{/snippet}

{#snippet forwardCell(row)}
  <span
    class="inline-flex items-center text-xs px-2 py-1 rounded-full font-medium {row.forward ===
    'nat'
      ? 'bg-info/10 text-info'
      : row.forward === 'bridge'
        ? 'bg-accent/10 text-accent'
        : 'bg-muted text-muted-foreground'}"
  >
    {#if row.forward === 'nat'}
      <svg
        class="w-3 h-3 mr-1"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        viewBox="0 0 24 24"><path d="M5 12h14M13 6l6 6-6 6" /></svg
      >
    {:else if row.forward === 'bridge'}
      <svg
        class="w-3 h-3 mr-1"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        viewBox="0 0 24 24"><path d="M2 12h20M6 8v8M12 8v8M18 8v8" /></svg
      >
    {/if}
    {row.forward === 'nat' ? 'NAT' : row.forward === 'bridge' ? 'Bridge' : t('networks.isolated')}
  </span>
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
        disabled={toggling[row.name]}
        class="p-1.5 rounded-md text-warning hover:bg-warning/10 transition-colors disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        aria-label={`${t('networks.stop')} ${row.name}`}
        title={t('networks.stop')}
      >
        {#if toggling[row.name]}
          <Spinner size="xs" color="text-warning" />
        {:else}
          <svg class="w-4 h-4" fill="currentColor" viewBox="0 0 24 24"
            ><rect x="6" y="4" width="4" height="16" /><rect
              x="14"
              y="4"
              width="4"
              height="16"
            /></svg
          >
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
          <svg class="w-4 h-4" fill="currentColor" viewBox="0 0 24 24"
            ><polygon points="5 3 19 12 5 21 5 3" /></svg
          >
        {/if}
      </button>
    {/if}
    <button
      onclick={() => startEdit(row)}
      class="p-1.5 rounded-md text-muted-foreground hover:text-accent hover:bg-muted transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      aria-label={`${t('common.edit')} ${row.name}`}
    >
      <svg class="w-4 h-4" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">
        <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7" />
        <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" />
      </svg>
    </button>
    <button
      onclick={() => deleteNet(row.name)}
      disabled={row.protected}
      class="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:text-muted-foreground disabled:hover:bg-transparent"
      aria-label={`${t('common.delete')} ${row.name}`}
      title={row.protected
        ? t('networks.managedDeleteTooltip2', { name: row.name })
        : t('common.delete')}
    >
      <svg class="w-4 h-4" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">
        <polyline points="3 6 5 6 21 6" /><path
          d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"
        />
      </svg>
    </button>
  </div>
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
