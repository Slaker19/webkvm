<script>
  import { onMount } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { t } from '../lib/i18n.svelte.js';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Card } from '$lib/components/ui/card';
  import { formatBytes } from '$lib/utils/format.js';

  let nodes = $state([]);
  let summary = $state(null);
  let loading = $state(true);
  let pingingAll = $state(false);
  let pingingNode = $state({});
  let error = $state('');

  // Add-node form (admin only)
  let showAdd = $state(false);
  let addName = $state('');
  let addUri = $state('');
  let addSaving = $state(false);
  let addError = $state('');

  // Inline edit
  let editingId = $state('');
  let editName = $state('');
  let editUri = $state('');
  let editSaving = $state(false);
  let editError = $state('');

  let confirmDeleteOpen = $state(false);
  let confirmDeleteNode = $state(null);
  let confirmDeleteLoading = $state(false);

  async function load() {
    loading = true;
    error = '';
    try {
      const [resNodes, resSummary] = await Promise.all([
        api.listNodes(),
        api.getClusterSummary().catch(() => null),
      ]);
      nodes = resNodes.nodes || resNodes || [];
      summary = resSummary;
    } catch (e) {
      error = t('nodes.errorLoading', { error: e.message });
    } finally {
      loading = false;
    }
  }

  async function handlePingAll() {
    pingingAll = true;
    try {
      const res = await api.pingAllNodes();
      if (res.nodes) nodes = res.nodes;
      if (res.summary) summary = res.summary;
      toast.success(t('nodes.pingAllBtn'));
    } catch (e) {
      toast.error(e.message);
    } finally {
      pingingAll = false;
    }
  }

  async function handlePingSingle(n) {
    pingingNode[n.id] = true;
    try {
      const updated = await api.pingNode(n.id);
      nodes = nodes.map((item) => (item.id === n.id ? updated : item));
      if (updated.status === 'online') {
        toast.success(t('nodes.pingSuccess', { name: n.name, latency: updated.latency_ms }));
      } else {
        toast.error(
          t('nodes.pingError', { name: n.name, error: updated.last_error || 'unreachable' })
        );
      }
      summary = await api.getClusterSummary().catch(() => summary);
    } catch (e) {
      toast.error(t('nodes.pingError', { name: n.name, error: e.message }));
    } finally {
      pingingNode[n.id] = false;
    }
  }

  onMount(() => {
    load();
    const interval = setInterval(async () => {
      try {
        const resNodes = await api.listNodes();
        nodes = resNodes.nodes || resNodes || [];
        summary = await api.getClusterSummary().catch(() => summary);
      } catch {
        // silent background polling
      }
    }, 20000);
    return () => clearInterval(interval);
  });

  function openAdd() {
    addName = '';
    addUri = '';
    addError = '';
    showAdd = true;
  }

  async function createNode() {
    if (!addName.trim()) return (addError = t('nodes.nameRequired'));
    if (!addUri.trim()) return (addError = t('nodes.uriRequired'));
    addSaving = true;
    addError = '';
    try {
      await api.createNode(addName.trim(), addUri.trim());
      toast.success(t('nodes.nodeCreated'));
      showAdd = false;
      await load();
      // Probe new node immediately
      await handlePingAll();
    } catch (e) {
      addError = e.message;
    } finally {
      addSaving = false;
    }
  }

  function startEdit(n) {
    editingId = n.id;
    editName = n.name;
    editUri = n.uri;
    editError = '';
  }

  function cancelEdit() {
    editingId = '';
  }

  async function saveEdit(n) {
    if (!editName.trim()) return (editError = t('nodes.nameRequired'));
    editSaving = true;
    editError = '';
    try {
      const body = { name: editName.trim() };
      if (n.type !== 'local') body.uri = editUri.trim();
      await api.updateNode(n.id, body);
      toast.success(t('nodes.nodeUpdated'));
      editingId = '';
      await load();
    } catch (e) {
      editError = e.message;
    } finally {
      editSaving = false;
    }
  }

  async function toggleEnabled(n) {
    try {
      await api.updateNode(n.id, { enabled: !n.enabled });
      await load();
    } catch (e) {
      toast.error(e.message);
    }
  }

  function askDelete(n) {
    confirmDeleteNode = n;
    confirmDeleteOpen = true;
  }

  async function doDelete() {
    if (!confirmDeleteNode) return;
    confirmDeleteLoading = true;
    try {
      await api.deleteNode(confirmDeleteNode.id);
      toast.success(t('nodes.nodeDeleted'));
      confirmDeleteOpen = false;
      confirmDeleteNode = null;
      await load();
    } catch (e) {
      toast.error(e.message);
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

  function getWebConsoleURL(uri) {
    if (!uri) return null;
    const clean = uri.trim();
    if (clean.startsWith('http://') || clean.startsWith('https://')) {
      return clean;
    }
    // Extract IP from qemu+ssh://user@192.168.1.215/system
    const m = clean.match(/@([^/:]+)/);
    if (m && m[1]) {
      return `https://${m[1]}:8080`;
    }
    return null;
  }
</script>

<div class="p-4 sm:p-6 w-full max-w-6xl mx-auto space-y-6">
  <PageHeader title={t('nodes.title')} subtitle={t('nodes.subtitle')}>
    {#snippet actions()}
      <div class="flex items-center gap-2">
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
          <Button size="sm" class="!h-8 !text-xs" onclick={openAdd}>
            <Icon name="plus" size={13} class="mr-1.5" />
            {t('nodes.addNode')}
          </Button>
        {/if}
      </div>
    {/snippet}
  </PageHeader>

  <!-- Summary Metric Cards -->
  <div class="grid grid-cols-2 sm:grid-cols-4 gap-4">
    <Card class="p-4 border border-border bg-card/60">
      <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        {t('nodes.summaryTotal')}
      </div>
      <div class="mt-2 text-2xl font-bold font-mono text-foreground">
        {nodes.length}
      </div>
      <div class="mt-1 text-xs text-muted-foreground">{t('nodes.subtitle')}</div>
    </Card>

    <Card class="p-4 border border-border bg-card/60">
      <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        {t('nodes.summaryOnline')}
      </div>
      <div class="mt-2 text-2xl font-bold font-mono text-emerald-500 flex items-center gap-2">
        <span>{summary?.online_nodes ?? nodes.filter((n) => n.status === 'online').length}</span>
        <span class="w-2.5 h-2.5 rounded-full bg-emerald-500 animate-pulse"></span>
      </div>
      <div class="mt-1 text-xs text-muted-foreground">
        {summary?.offline_nodes ? `${summary.offline_nodes} offline` : '100% reachable'}
      </div>
    </Card>

    <Card class="p-4 border border-border bg-card/60">
      <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        {t('nodes.summaryAvgLatency')}
      </div>
      <div class="mt-2 text-2xl font-bold font-mono text-foreground">
        {summary?.avg_latency_ms != null ? `${summary.avg_latency_ms} ms` : '—'}
      </div>
      <div class="mt-1 text-xs text-muted-foreground">RTT latency probe</div>
    </Card>

    <Card class="p-4 border border-border bg-card/60">
      <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        {t('nodes.summaryStorage')}
      </div>
      <div class="mt-2 text-2xl font-bold font-mono text-foreground">
        {summary?.total_disk_free ? formatBytes(summary.total_disk_free) : '—'}
      </div>
      <div class="mt-1 text-xs text-muted-foreground">
        {#if summary?.total_disk_total}
          Free of {formatBytes(summary.total_disk_total)}
        {:else}
          Fleet storage free
        {/if}
      </div>
    </Card>
  </div>

  <Alert variant="info">{t('nodes.multiHostNotice')}</Alert>

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if showAdd}
    <Card class="p-4 space-y-3 border border-border bg-card">
      <div class="text-xs font-semibold text-muted-foreground uppercase tracking-wider">
        {t('nodes.addNode')}
      </div>
      {#if addError}
        <Alert variant="error">{addError}</Alert>
      {/if}
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <div>
          <label class="text-xs text-muted-foreground" for="node-add-name">{t('common.name')}</label
          >
          <Input id="node-add-name" bind:value={addName} placeholder="node-2" class="w-full mt-1" />
        </div>
        <div>
          <label class="text-xs text-muted-foreground" for="node-add-uri">{t('nodes.uri')}</label>
          <Input
            id="node-add-uri"
            bind:value={addUri}
            placeholder={t('nodes.uriPlaceholder')}
            class="w-full mt-1 font-mono text-xs"
          />
        </div>
      </div>
      <p class="text-xs text-muted-foreground">{t('nodes.uriHelper')}</p>
      <div class="flex items-center gap-2">
        <Button size="sm" onclick={createNode} disabled={addSaving}>
          {#if addSaving}<Spinner size="sm" color="text-white" />{/if}
          {t('common.save')}
        </Button>
        <Button size="sm" variant="outline" onclick={() => (showAdd = false)}>
          {t('common.cancel')}
        </Button>
      </div>
    </Card>
  {/if}

  {#if loading}
    <div class="flex items-center justify-center py-24"><Spinner size="lg" /></div>
  {:else if nodes.length === 0}
    <Card class="p-12 text-center">
      <p class="text-muted-foreground text-sm">{t('nodes.comingSoonDesc')}</p>
    </Card>
  {:else}
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
      {#each nodes as n (n.id)}
        <Card class="p-4 border border-border bg-card/60 flex flex-col justify-between space-y-3">
          {#if editingId === n.id}
            {#if editError}
              <p class="text-xs text-destructive mb-2">{editError}</p>
            {/if}
            <div class="space-y-2">
              <Input bind:value={editName} class="w-full text-sm" />
              <Input
                bind:value={editUri}
                disabled={n.type === 'local'}
                title={n.type === 'local' ? t('nodes.cannotEditLocalUri') : ''}
                class="w-full text-xs font-mono {n.type === 'local' ? 'opacity-50' : ''}"
              />
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
              <div class="flex items-start justify-between gap-2">
                <div class="flex items-center gap-2 min-w-0">
                  <div
                    class="w-8 h-8 rounded-lg bg-muted flex items-center justify-center shrink-0"
                  >
                    <Icon name="server" size={16} class="text-muted-foreground" />
                  </div>
                  <div class="min-w-0">
                    <div class="font-medium text-sm truncate flex items-center gap-1.5">
                      <span>{n.name}</span>
                      {#if n.version}
                        <span
                          class="text-[10px] px-1.5 py-0.2 rounded font-mono bg-muted text-muted-foreground"
                        >
                          v{n.version}
                        </span>
                      {/if}
                    </div>
                    <span
                      class="text-[10px] px-1.5 py-0.5 rounded uppercase tracking-wide {n.type ===
                      'local'
                        ? 'bg-accent/10 text-accent'
                        : 'bg-muted text-muted-foreground'}"
                    >
                      {n.type === 'local' ? t('nodes.local') : t('nodes.remote')}
                    </span>
                  </div>
                </div>

                <!-- Status Badge & Enabled Toggle -->
                <div class="flex items-center gap-1.5 shrink-0">
                  <button
                    type="button"
                    onclick={() => auth.isAdmin() && toggleEnabled(n)}
                    disabled={!auth.isAdmin()}
                    class="text-[10px] px-1.5 py-0.5 rounded shrink-0 {n.enabled
                      ? 'bg-emerald-500/10 text-emerald-500'
                      : 'bg-muted text-muted-foreground'}"
                    title={auth.isAdmin() ? t('common.enabled') + ' / ' + t('common.disabled') : ''}
                  >
                    {n.enabled ? t('common.enabled') : t('common.disabled')}
                  </button>

                  {#if n.status === 'online'}
                    <span
                      class="inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-mono font-medium bg-emerald-500/10 text-emerald-500"
                    >
                      <span class="w-1.5 h-1.5 rounded-full bg-emerald-500 animate-pulse"></span>
                      {n.latency_ms > 0 ? `${n.latency_ms} ms` : t('nodes.statusOnline')}
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
                      {n.status || t('nodes.statusUnknown')}
                    </span>
                  {/if}
                </div>
              </div>

              <!-- Node URI -->
              <p class="text-xs font-mono text-muted-foreground truncate" title={n.uri}>
                {n.uri}
              </p>

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
                    <div class="w-full h-1.5 bg-muted rounded-full overflow-hidden">
                      <div
                        class="h-full bg-primary"
                        style="width: {Math.max(
                          5,
                          Math.min(
                            100,
                            Math.round(((n.disk_total - n.disk_free) / n.disk_total) * 100)
                          )
                        )}%"
                      ></div>
                    </div>
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

                {#if n.last_error}
                  <div class="text-[10px] text-destructive truncate" title={n.last_error}>
                    {n.last_error}
                  </div>
                {/if}
              </div>
            </div>

            <!-- Card Actions -->
            <div class="pt-3 border-t border-border flex items-center justify-between gap-1">
              <div class="flex items-center gap-1.5">
                <Button
                  size="sm"
                  variant="outline"
                  class="!h-7 !text-[11px] !px-2 gap-1"
                  onclick={() => handlePingSingle(n)}
                  disabled={pingingNode[n.id]}
                >
                  <Icon name="activity" size={11} class={pingingNode[n.id] ? 'animate-spin' : ''} />
                  {t('nodes.pingBtn')}
                </Button>

                {#if getWebConsoleURL(n.uri) && n.type !== 'local'}
                  <a
                    href={getWebConsoleURL(n.uri)}
                    target="_blank"
                    rel="noreferrer"
                    class="inline-flex items-center gap-1 h-7 px-2 text-[11px] rounded border border-border bg-card text-foreground hover:bg-muted/40 transition-colors"
                  >
                    <Icon name="external-link" size={11} />
                    {t('nodes.openConsole')}
                  </a>
                {/if}
              </div>

              {#if auth.isAdmin()}
                <div class="flex items-center gap-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    class="!h-7 !w-7 !p-0"
                    onclick={() => startEdit(n)}
                  >
                    <Icon name="pencil" size={12} />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    class="!h-7 !w-7 !p-0 text-destructive hover:bg-destructive/10"
                    onclick={() => askDelete(n)}
                    disabled={n.type === 'local'}
                    title={n.type === 'local' ? t('nodes.cannotDeleteLocal') : ''}
                  >
                    <Icon name="trash" size={12} />
                  </Button>
                </div>
              {/if}
            </div>
          {/if}
        </Card>
      {/each}
    </div>
  {/if}
</div>

<ConfirmDialog
  bind:open={confirmDeleteOpen}
  title={t('nodes.deleteConfirmTitle', { name: confirmDeleteNode?.name || '' })}
  description={t('nodes.deleteConfirmDesc')}
  confirmLabel={t('common.delete')}
  variant="destructive"
  loading={confirmDeleteLoading}
  onConfirm={doDelete}
/>
