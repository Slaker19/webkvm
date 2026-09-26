<script>
  import { onMount } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import DataTable from '$lib/components/DataTable.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import SearchInput from '$lib/components/SearchInput.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import UserFormDialog from '$lib/components/UserFormDialog.svelte';
  import { UserPlus } from '@lucide/svelte';
  import { t } from '../lib/i18n.svelte.js';
  import { CAPABILITIES, effectiveCapabilities } from '$lib/utils/permissions.js';

  let users = $state([]);
  let loading = $state(true);
  let error = $state('');
  let search = $state('');
  let page = $state(0);
  const PAGE_SIZE = 10;

  // Dialog state. One component serves both flows: `formMode` selects
  // which, `formUser` is the target when editing.
  let formOpen = $state(false);
  let formMode = $state('create');
  let formUser = $state(null);
  let formUsage = $state(null);
  let saving = $state(false);

  // Scope vocabularies, loaded once and shared with the dialog.
  let pools = $state([]);
  let networks = $state([]);
  let groups = $state([]);
  let tags = $state([]);

  let confirmState = $state({
    open: false,
    title: '',
    description: '',
    confirmLabel: t('common.delete'),
    variant: 'destructive',
    onConfirm: () => {},
    loading: false,
  });

  const usernames = $derived(users.map((u) => u.username));

  const filtered = $derived.by(() => {
    const q = search.toLowerCase().trim();
    if (!q) return users;
    return users.filter((u) => (u.username || '').toLowerCase().includes(q));
  });

  const paginated = $derived(filtered.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE));
  const totalPages = $derived(Math.max(1, Math.ceil(filtered.length / PAGE_SIZE)));

  $effect(() => {
    void search; // track search to re-run on change
    page = 0;
  });

  onMount(() => {
    if (auth.isAdmin()) {
      load();
      loadScopeVocabularies();
    }
  });

  async function load() {
    loading = true;
    error = '';
    try {
      users = await api.listUsers();
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  // Pools, networks, groups and tags are independent lookups; one
  // failing must not blank the others, so each falls back on its own.
  async function loadScopeVocabularies() {
    const [p, n, g, tg] = await Promise.allSettled([
      api.listPools(),
      api.listNetworks(),
      api.listGroups(),
      api.listTags(),
    ]);
    if (p.status === 'fulfilled') {
      const arr = Array.isArray(p.value) ? p.value : (p.value?.pools ?? []);
      // Keep the whole pool object: the quota editor needs the declared
      // purpose to decide which pools can hold disks at all.
      pools = arr
        .map((x) => (typeof x === 'string' ? { name: x, purpose: '' } : x))
        .filter((x) => x?.name);
    }
    if (n.status === 'fulfilled') {
      const arr = Array.isArray(n.value) ? n.value : (n.value?.networks ?? []);
      networks = arr.map((x) => x?.name ?? x).filter(Boolean);
    }
    if (g.status === 'fulfilled') {
      groups = (g.value?.groups ?? []).map((x) => x?.name ?? x).filter(Boolean);
    }
    if (tg.status === 'fulfilled') {
      tags = (tg.value?.tags ?? []).filter(Boolean);
    }
  }

  function askConfirm(opts) {
    confirmState = { ...opts, open: true, loading: false };
  }

  function openCreate() {
    formMode = 'create';
    formUser = null;
    formUsage = null;
    formOpen = true;
  }

  async function openEdit(user) {
    formMode = 'edit';
    formUser = user;
    formUsage = null;
    formOpen = true;
    // Live consumption, so limits are set against real numbers instead
    // of guesses. Best-effort: the quota tab degrades to plain inputs.
    try {
      formUsage = await api.userUsage(user.username);
    } catch {
      formUsage = null;
    }
  }

  async function submitForm(payload) {
    if (saving) return;
    saving = true;
    const isCreate = formMode === 'create';
    const name = isCreate ? payload.username : formUser.username;
    try {
      if (isCreate) {
        await api.createUser(payload);
        toast.success(t('users.created', { name }));
      } else {
        await api.updateUser(name, payload);
        toast.success(t('users.updated'));
      }
      formOpen = false;
      formUser = null;
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      saving = false;
    }
  }

  function revokeSessions(username) {
    if (username === auth.user) return;
    askConfirm({
      title: t('users.revokeSessionsConfirm', { name: username }),
      description: t('users.revokeSessionsConfirmDesc'),
      confirmLabel: t('users.revokeSessions'),
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.revokeUserSessions(username);
          confirmState.open = false;
          toast.success(t('users.revokeSessionsDone', { name: username }));
        } catch (e) {
          toast.error(e.message);
          confirmState.loading = false;
        }
      },
    });
  }

  function deleteUser(username) {
    if (username === auth.user) {
      toast.error(t('users.cannotDeleteSelf'));
      return;
    }
    askConfirm({
      title: t('users.deleteConfirm', { name: username }),
      description: t('users.deleteConfirmDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.deleteUser(username);
          confirmState.open = false;
          toast.success(t('users.deleted', { name: username }));
          await load();
        } catch (e) {
          toast.error(e.message);
          confirmState.loading = false;
        }
      },
    });
  }

  /**
   * Capability chips for the table.
   *
   * Resolved through the shared model rather than by testing
   * `!== false` on each raw flag: that older test reported a capability
   * as held whenever it was merely absent, so viewers — who hold none
   * by default — were listed as having all of them.
   */
  function grantedCaps(row) {
    if (row.role === 'admin') return [];
    const caps = effectiveCapabilities(row);
    return CAPABILITIES.filter((c) => c.roles.includes(row.role) && caps[c.key]).map((c) =>
      t(`users.perm.${c.key}.short`)
    );
  }

  function scopeSummary(row) {
    if (row.role === 'admin') return [];
    const out = [];
    if (row.allowed_pools?.length)
      out.push(t('users.allowedPoolsSummary', { pools: row.allowed_pools.join(', ') }));
    if (row.allowed_networks?.length)
      out.push(t('users.allowedNetworksSummary', { networks: row.allowed_networks.join(', ') }));
    if (row.allowed_groups?.length)
      out.push(t('users.summaryGroups', { list: row.allowed_groups.join(', ') }));
    // Tags are shown separately from groups: they are a distinct grant,
    // and collapsing them hid the fact that saving overwrote one with
    // the other.
    if (row.allowed_tags?.length)
      out.push(t('users.summaryTags', { list: row.allowed_tags.join(', ') }));
    return out;
  }
</script>

<div class="p-4 sm:p-6 w-full max-w-5xl mx-auto">
  <PageHeader title={t('users.title')} subtitle={t('users.subtitle')}>
    {#snippet actions()}
      <SearchInput
        bind:value={search}
        placeholder={t('users.searchPlaceholder')}
        class="w-44 sm:w-48"
      />
      <Button onclick={openCreate}>{t('users.addUser')}<UserPlus class="w-4 h-4 ml-1.5" /></Button>
    {/snippet}
  </PageHeader>

  {#if error}
    <div class="mb-4"><Alert variant="error">{error}</Alert></div>
  {/if}

  {#if loading}
    <div class="flex items-center justify-center py-24"><Spinner size="lg" /></div>
  {:else}
    <DataTable
      columns={[
        { key: 'username', label: t('users.username'), render: userCell },
        { key: 'role', label: t('users.role'), width: '110px', render: roleCell },
        { key: 'active', label: t('users.active'), width: '70px', render: activeCell },
        {
          key: 'created_at',
          label: t('users.createdAt'),
          width: '120px',
          class: 'tnum text-muted-foreground',
          render: createdCell,
        },
        {
          key: 'last_login_at',
          label: t('users.lastLogin'),
          width: '140px',
          class: 'tnum text-muted-foreground',
          render: lastLoginCell,
        },
        { key: 'actions', label: '', align: 'right', width: '150px', render: actionsCell },
      ]}
      rows={paginated}
      rowKey="username"
      emptyMessage={search ? t('users.noUsersSearch') : t('users.noUsers')}
      emptyIcon={search ? 'search' : 'users'}
    />

    {#if totalPages > 1}
      <div class="flex items-center justify-between mt-3 text-sm">
        <span class="text-muted-foreground tnum">
          {t('users.showing', {
            from: page * PAGE_SIZE + 1,
            to: Math.min((page + 1) * PAGE_SIZE, filtered.length),
            total: filtered.length,
          })}
        </span>
        <div class="flex items-center gap-1">
          <Button variant="outline" size="sm" disabled={page === 0} onclick={() => page--}
            >{t('users.previous')}</Button
          >
          <span class="px-3 text-muted-foreground tnum">{page + 1} / {totalPages}</span>
          <Button
            variant="outline"
            size="sm"
            disabled={page >= totalPages - 1}
            onclick={() => page++}>{t('users.next')}</Button
          >
        </div>
      </div>
    {/if}
  {/if}
</div>

{#snippet createdCell(row)}
  {row.created_at?.slice(0, 10) || '—'}
{/snippet}

{#snippet lastLoginCell(row)}
  {row.last_login_at ? row.last_login_at.replace('T', ' ').slice(0, 16) : '—'}
{/snippet}

{#snippet activeCell(row)}
  {#if row.active === false}
    <span
      class="inline-flex items-center gap-1.5 text-xs px-2 py-1 rounded-full bg-destructive/10 text-destructive font-medium"
    >
      <span class="w-1.5 h-1.5 rounded-full bg-destructive"></span>
      {t('users.statusDisabled')}
    </span>
  {:else}
    <span
      class="inline-flex items-center gap-1.5 text-xs px-2 py-1 rounded-full bg-success/10 text-success font-medium"
    >
      <span class="w-1.5 h-1.5 rounded-full bg-success"></span>
      {t('users.statusActive')}
    </span>
  {/if}
{/snippet}

{#snippet userCell(row)}
  <div class="flex items-center gap-2.5 min-w-0">
    {#if row.avatar}
      <img
        src={row.avatar}
        alt=""
        class="w-8 h-8 rounded-full shrink-0 object-cover bg-muted"
        onerror={(e) => (e.currentTarget.style.display = 'none')}
      />
    {:else}
      <div
        class="w-8 h-8 rounded-full shrink-0 flex items-center justify-center text-xs font-bold text-white {row.role ===
        'admin'
          ? 'bg-accent'
          : row.role === 'operator'
            ? 'bg-info'
            : 'bg-muted-foreground/50'}"
      >
        {row.username[0].toUpperCase()}
      </div>
    {/if}
    <div class="min-w-0">
      <div class="font-medium truncate">{row.username}</div>
      {#if row.email}
        <div class="text-xs text-muted-foreground truncate">{row.email}</div>
      {/if}
    </div>
  </div>
{/snippet}

{#snippet roleCell(row)}
  <div class="space-y-1">
    <div class="flex items-center gap-1.5 flex-wrap">
      <span
        class="inline-flex items-center gap-1 text-xs px-2.5 py-0.5 rounded-full font-medium {row.role ===
        'admin'
          ? 'bg-accent/10 text-accent'
          : row.role === 'operator'
            ? 'bg-info/10 text-info'
            : 'bg-muted text-muted-foreground'}"
      >
        {#if row.role === 'admin'}
          <Icon name="shield" size={12} />
        {:else if row.role === 'operator'}
          <Icon name="fileText" size={12} />
        {:else}
          <Icon name="eye" size={12} />
        {/if}
        {t(`users.roleInfo.${row.role}.title`)}
      </span>
      {#if row.totp_enabled}
        <span
          class="text-[10px] px-1.5 py-0.2 rounded bg-success/15 text-success font-mono font-medium border border-success/30 flex items-center gap-0.5"
        >
          <Icon name="lock" size={9} /> 2FA
        </span>
      {/if}
    </div>

    {#if row.role !== 'admin'}
      {@const granted = grantedCaps(row)}
      <div class="flex items-center gap-1 flex-wrap pt-0.5">
        {#if granted.length}
          {#each granted as cap (cap)}
            <span class="text-[9px] px-1 rounded bg-muted text-muted-foreground">{cap}</span>
          {/each}
        {:else}
          <span class="text-[9px] text-muted-foreground">{t('users.noCapabilities')}</span>
        {/if}
      </div>
    {/if}

    {#if row.quota?.max_vms || row.quota?.max_vcpus || row.quota?.max_ram_mb || row.quota?.max_disk_gb}
      <div class="text-[10px] text-muted-foreground">
        {t('users.quotaSummary', {
          max_vms: row.quota?.max_vms || 0,
          max_vcpus: row.quota?.max_vcpus || 0,
          max_ram_mb: row.quota?.max_ram_mb || 0,
          max_disk_gb: row.quota?.max_disk_gb || 0,
        })}
      </div>
    {/if}
    {#if row.quota?.pool_quotas && Object.keys(row.quota.pool_quotas).length}
      <div class="text-[10px] text-muted-foreground">
        {t('users.quotaPoolSummary', {
          pools: Object.entries(row.quota.pool_quotas)
            .map(([p, g]) => `${p}: ${g}GB`)
            .join(', '),
        })}
      </div>
    {/if}
    {#each scopeSummary(row) as line (line)}
      <div class="text-[10px] text-muted-foreground">{line}</div>
    {/each}
  </div>
{/snippet}

{#snippet actionsCell(row)}
  <div class="flex items-center gap-1 justify-end">
    <button
      onclick={() => openEdit(row)}
      class="text-xs text-accent hover:bg-muted px-2 py-1 rounded"
      aria-label={`${t('users.editUser', { name: row.username })}`}>{t('common.edit')}</button
    >
    {#if row.username !== auth.user}
      <button
        onclick={() => revokeSessions(row.username)}
        class="text-xs text-muted-foreground hover:text-foreground hover:bg-muted px-2 py-1 rounded"
        aria-label={`${t('users.revokeSessions')} ${row.username}`}
        title={t('users.revokeSessionsConfirmDesc')}>{t('users.revokeSessionsShort')}</button
      >
      <button
        onclick={() => deleteUser(row.username)}
        class="text-xs text-muted-foreground hover:text-destructive hover:bg-destructive/10 px-2 py-1 rounded"
        aria-label={`${t('users.deleteUser')} ${row.username}`}>{t('common.delete')}</button
      >
    {/if}
  </div>
{/snippet}

<UserFormDialog
  open={formOpen}
  mode={formMode}
  user={formUser}
  usage={formUsage}
  {pools}
  {networks}
  {groups}
  {tags}
  {saving}
  existingUsernames={usernames}
  onsubmit={submitForm}
  onclose={() => {
    formOpen = false;
    formUser = null;
  }}
/>

<ConfirmDialog
  bind:open={confirmState.open}
  title={confirmState.title}
  description={confirmState.description}
  confirmLabel={confirmState.confirmLabel}
  variant={confirmState.variant}
  loading={confirmState.loading}
  onConfirm={confirmState.onConfirm}
/>
