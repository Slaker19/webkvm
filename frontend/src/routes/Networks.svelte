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
  import Icon from '$lib/components/Icon.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import { t } from '../lib/i18n.svelte.js';

  let networks = $state([]);
  let hostInterfaces = $state([]);
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
  let dhcp = $state(true);
  let dhcpStart = $state('');
  let dhcpEnd = $state('');
  let dnsText = $state('');
  let autostart = $state(true);
  let saving = $state(false);
  let toggling = $state({});

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

  $effect(() => {
    if (showCreate && kind === 'direct' && hostInterfaces.length === 0) loadHostInterfaces();
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
    kind = 'isolated';
    directInterface = '';
    vlanAware = false;
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
    kind = net.kind || 'isolated';
    cidr = net.cidr || '';
    directInterface = net.interface || '';
    vlanAware = !!net.vlan_aware;
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
    error = '';
    saving = true;
    let payload;
    if (kind === 'direct') {
      payload = {
        name: name.trim(),
        kind,
        autostart,
        interface: directInterface,
        vlan_aware: vlanAware,
      };
    } else {
      payload = { name: name.trim(), kind, autostart, cidr: cidr || '', dhcp };
      if (dhcp) {
        payload.dhcp_start = dhcpStart || preview?.dhcpStart || '';
        payload.dhcp_end = dhcpEnd || preview?.dhcpEnd || '';
      }
      if (dnsText) payload.dns = parseDNSList(dnsText);
    }
    try {
      const created = await api.createNetwork(payload);
      const label = (created && created.name) || name;
      toast.success(t('networks.networkCreated', { label }), { duration: 6000 });
      resetForm();
      await load();
      document
        .getElementById('networks-table-anchor')
        ?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    } catch (e) {
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
    const payload = { dhcp, autostart, vlan_aware: vlanAware };
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

<div class="p-4 sm:p-6 max-w-6xl">
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

  {#if !loading && networks.length > 0}
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-4">
      <StatCard label={t('networks.title')} value={String(networks.length)} />
      <StatCard
        label={t('networks.activeBadge')}
        status="running"
        value={String(networks.filter((n) => n.active).length)}
      />
      <StatCard
        label={t('networks.autostartBadge')}
        value={String(networks.filter((n) => n.autostart).length)}
      />
      <StatCard
        label={t('networks.direct')}
        value={String(networks.filter((n) => n.kind === 'direct').length)}
      />
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
              {#each hostInterfaces as iface}
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
          {#if !editingNet}
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
        { key: 'kind', label: t('networks.forwardMode'), width: '110px', render: kindCell },
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
    <button
      onclick={() => startEdit(row)}
      class="p-1.5 rounded-md text-muted-foreground hover:text-accent hover:bg-muted transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      aria-label={`${t('common.edit')} ${row.name}`}
    >
      <Icon name="pencil" size={16} />
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
      <Icon name="trash" size={16} />
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
