<script>
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import { t } from '../lib/i18n.svelte.js';
  import { fleetSnapshots } from '$lib/stores/fleetSnapshots.svelte.js';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import DataTable from '$lib/components/DataTable.svelte';
  import SearchInput from '$lib/components/SearchInput.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';

  let snapshots = $state([]);
  let loading = $state(true);
  let error = $state('');
  let search = $state('');

  async function load() {
    loading = true;
    error = '';
    try {
      const res = await api.listAllSnapshots();
      // DataTable needs a globally unique row key; a snapshot's own
      // id/name is only unique within its VM (two VMs can each have a
      // snapshot literally named "before-upgrade"). Tag each row once,
      // here, rather than recomputing it on every reactive read.
      snapshots = (res.snapshots || []).map((s) => ({ ...s, _key: `${s.vm_id}:${s.id}` }));
      // Keep the sidebar's "show the Snapshots link?" flag in sync —
      // covers a direct/bookmarked visit while it was hidden.
      fleetSnapshots.hasAny = snapshots.length > 0;
      fleetSnapshots.checked = true;
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  onMount(load);

  const rows = $derived.by(() => {
    const q = search.toLowerCase().trim();
    if (!q) return snapshots;
    return snapshots.filter(
      (s) => s.vm_name.toLowerCase().includes(q) || s.name.toLowerCase().includes(q)
    );
  });

  const totalSize = $derived(snapshots.reduce((sum, s) => sum + (s.size_at_snap_bytes || 0), 0));
  const vmCount = $derived(new Set(snapshots.map((s) => s.vm_id)).size);

  function fmtBytes(n) {
    if (!n) return '—';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024;
      i++;
    }
    return `${n.toFixed(n >= 10 || i === 0 ? 0 : 1)} ${u[i]}`;
  }

  function fmtDate(epoch) {
    if (!epoch) return '—';
    const d = new Date(epoch * 1000);
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    const hh = String(d.getHours()).padStart(2, '0');
    const mm = String(d.getMinutes()).padStart(2, '0');
    return `${y}-${m}-${day} ${hh}:${mm}`;
  }

  function openIn(row) {
    navigate(`/vms/${row.vm_id}`, { query: { tab: 'snapshots' } });
  }
</script>

{#snippet vmCell(row)}
  <button
    type="button"
    onclick={() => openIn(row)}
    class="text-sm text-accent hover:text-accent-hover truncate block text-left"
  >
    {row.vm_name}
  </button>
{/snippet}

{#snippet nameCell(row)}
  <div class="flex items-center gap-2 min-w-0">
    <span class="font-mono text-xs truncate" title={row.name}>{row.name}</span>
    {#if row.current}
      <span
        class="text-[10px] px-1.5 py-0.5 rounded border border-accent/30 bg-accent/10 text-accent uppercase tracking-wide shrink-0"
      >
        {t('snapshots.current')}
      </span>
    {/if}
  </div>
{/snippet}

{#snippet createdCell(row)}
  <span class="text-xs text-muted-foreground tnum">{fmtDate(row.creation_time)}</span>
{/snippet}

{#snippet sizeCell(row)}
  <span class="text-xs text-muted-foreground tnum">{fmtBytes(row.size_at_snap_bytes)}</span>
{/snippet}

{#snippet actionsCell(row)}
  <Button size="sm" variant="outline" onclick={() => openIn(row)}>
    {t('snapshots.manage')}
    <Icon name="arrowRight" size={12} class="ml-1" />
  </Button>
{/snippet}

<div class="p-4 sm:p-6 max-w-6xl">
  <PageHeader title={t('snapshots.title')} subtitle={t('snapshots.subtitle')} />

  {#if !loading && snapshots.length > 0}
    <div class="grid grid-cols-3 gap-3 mb-4">
      <StatCard label={t('snapshots.title')} value={String(snapshots.length)} />
      <StatCard label={t('vms.title')} value={String(vmCount)} />
      <StatCard label={t('common.size')} value={fmtBytes(totalSize)} />
    </div>
  {/if}

  <div class="flex items-center gap-2 mb-4">
    <SearchInput bind:value={search} placeholder={t('snapshots.searchPlaceholder')} class="w-72" />
    <Button size="sm" variant="outline" onclick={load}>
      <Icon name="refresh" size={14} class="mr-1.5" />
      {t('common.refresh')}
    </Button>
  </div>

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if loading}
    <div class="flex items-center justify-center py-24"><Spinner size="lg" /></div>
  {:else}
    <DataTable
      columns={[
        { key: 'vm_name', label: t('vms.title'), width: '200px', render: vmCell },
        { key: 'name', label: t('snapshots.title'), render: nameCell },
        {
          key: 'creation_time',
          label: t('snapshots.created'),
          width: '160px',
          render: createdCell,
        },
        { key: 'size_at_snap_bytes', label: t('common.size'), width: '90px', render: sizeCell },
        { key: 'actions', label: '', width: '140px', align: 'right', render: actionsCell },
      ]}
      {rows}
      rowKey="_key"
      emptyMessage={t('snapshots.empty')}
    />
  {/if}
</div>
