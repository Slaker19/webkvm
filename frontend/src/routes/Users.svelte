<script>
  import { onMount } from 'svelte';
  import { api, auth, passwordStrength } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import DataTable from '$lib/components/DataTable.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import * as Dialog from '$lib/components/ui/dialog';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import SearchInput from '$lib/components/SearchInput.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { UserPlus } from '@lucide/svelte';
  import { t } from '../lib/i18n.svelte.js';

  let users = $state([]);
  let loading = $state(true);
  let error = $state('');
  let search = $state('');
  let page = $state(0);
  const PAGE_SIZE = 10;

  let showAdd = $state(false);
  // V12-FE-02: in-flight mutation guards.
  let userAdding = $state(false);
  let userSaving = $state(false);
  let newUsername = $state('');
  let newPassword = $state('');
  let newPasswordConfirm = $state('');
  let newRole = $state('operator');
  let newEmail = $state('');
  // Defaults to true: an admin-chosen password is meant as a one-time
  // temporary credential, not the user's real one.
  let newMustChangePassword = $state(true);
  let newAllowedPools = $state([]);
  let newAllowedNetworks = $state([]);
  let newAllowedTags = $state([]);
  let newQMaxVMs = $state(0);
  let newQMaxVCPUs = $state(0);
  let newQMaxRAMMB = $state(0);
  let newQMaxDiskGB = $state(0);
  let newQPoolRows = $state([{ pool: '', gb: 0 }]);

  let editing = $state(null);
  let editPassword = $state('');
  let editRole = $state('');
  let editEmail = $state('');
  let editActive = $state(true);
  // Quota fields (0 = unlimited).
  let editQMaxVMs = $state(0);
  let editQMaxVCPUs = $state(0);
  let editQMaxRAMMB = $state(0);
  let editQMaxDiskGB = $state(0);
  // Per-pool disk quota: list of { pool, gb } rows (gb 0/empty = no limit).
  let editQPoolRows = $state([{ pool: '', gb: 0 }]);
  // Available storage pools (for the per-pool editor + allowlist).
  let pools = $state([]);
  // Per-user pool allowlist (empty = all pools; operators only).
  let editAllowedPools = $state([]);
  // Available networks + per-user network allowlist — same shape as pools.
  let networks = $state([]);
  let editAllowedNetworks = $state([]);

  let confirmState = $state({
    open: false,
    title: '',
    description: '',
    confirmLabel: t('common.delete'),
    variant: 'destructive',
    onConfirm: () => {},
    loading: false,
  });

  const newStrength = $derived(passwordStrength(newPassword));

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
      loadPools();
      loadNetworks();
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

  async function loadPools() {
    try {
      const data = await api.listPools();
      const arr = Array.isArray(data) ? data : (data?.pools ?? []);
      pools = arr.map((p) => p?.name ?? p).filter(Boolean);
    } catch {
      pools = [];
    }
  }

  async function loadNetworks() {
    try {
      const data = await api.listNetworks();
      const arr = Array.isArray(data) ? data : (data?.networks ?? []);
      networks = arr.map((n) => n?.name ?? n).filter(Boolean);
    } catch {
      networks = [];
    }
  }

  function askConfirm(opts) {
    confirmState = { ...opts, open: true, loading: false };
  }

  function resetAddForm() {
    newUsername = '';
    newPassword = '';
    newPasswordConfirm = '';
    newRole = 'operator';
    newEmail = '';
    newMustChangePassword = true;
    newAllowedPools = [];
    newAllowedNetworks = [];
    newAllowedTags = [];
    newQMaxVMs = 0;
    newQMaxVCPUs = 0;
    newQMaxRAMMB = 0;
    newQMaxDiskGB = 0;
    newQPoolRows = [{ pool: '', gb: 0 }];
  }

  function openAddForm() {
    resetAddForm();
    showAdd = true;
    loadAllTags();
  }

  function addNewPoolRow() {
    newQPoolRows = [...newQPoolRows, { pool: '', gb: 0 }];
  }

  function removeNewPoolRow(i) {
    newQPoolRows = newQPoolRows.filter((_, idx) => idx !== i);
  }

  async function addUser() {
    if (userAdding) return;
    if (!newUsername || !newPassword || newPassword.length < 8) return;
    if (newPassword !== newPasswordConfirm) {
      toast.error(t('users.passwordMismatch'));
      return;
    }
    userAdding = true;
    // Capture names BEFORE resetting (was a pre-existing toast bug —
    // the name was used after being cleared).
    const createdName = newUsername;
    const createdRole = newRole;
    const poolQuotas = {};
    for (const row of newQPoolRows) {
      if (row.pool && Number(row.gb) > 0) {
        poolQuotas[row.pool] = Number(row.gb);
      }
    }
    try {
      await api.createUser({
        username: createdName,
        password: newPassword,
        role: createdRole,
        email: newEmail,
        must_change_password: newMustChangePassword,
        allowed_pools: newAllowedPools,
        allowed_networks: newAllowedNetworks,
        allowed_tags: newAllowedTags,
        quota: {
          max_vms: newQMaxVMs || 0,
          max_vcpus: newQMaxVCPUs || 0,
          max_ram_mb: newQMaxRAMMB || 0,
          max_disk_gb: newQMaxDiskGB || 0,
          pool_quotas: poolQuotas,
        },
      });
      resetAddForm();
      showAdd = false;
      toast.success(t('users.created', { name: createdName }));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      userAdding = false;
    }
  }

  function startEdit(u) {
    editing = u.username;
    editPassword = '';
    editRole = u.role;
    editEmail = u.email || '';
    editActive = u.active !== false;
    editQMaxVMs = u.quota?.max_vms || 0;
    editQMaxVCPUs = u.quota?.max_vcpus || 0;
    editQMaxRAMMB = u.quota?.max_ram_mb || 0;
    editQMaxDiskGB = u.quota?.max_disk_gb || 0;
    const pq = u.quota?.pool_quotas || {};
    const rows = Object.entries(pq).map(([pool, gb]) => ({ pool, gb }));
    editQPoolRows = rows.length ? rows : [{ pool: '', gb: 0 }];
    editAllowedPools = u.allowed_pools ? [...u.allowed_pools] : [];
    editAllowedNetworks = u.allowed_networks ? [...u.allowed_networks] : [];
    // V13-D-01: tag allowlist (RBAC). Loaded eagerly for the editor.
    editAllowedTags = u.allowed_tags ? [...u.allowed_tags] : [];
    loadAllTags();
  }

  let allTags = $state([]);
  let editAllowedTags = $state([]);

  async function loadAllTags() {
    try {
      const r = await api.listTags();
      allTags = r.tags || [];
    } catch {
      allTags = [];
    }
  }

  function addPoolRow() {
    editQPoolRows = [...editQPoolRows, { pool: '', gb: 0 }];
  }

  function removePoolRow(i) {
    editQPoolRows = editQPoolRows.filter((_, idx) => idx !== i);
  }

  async function saveEdit() {
    if (userSaving) return;
    const data = {};
    if (editPassword) data.password = editPassword;
    if (editRole) data.role = editRole;
    if (editEmail) data.email = editEmail;
    else data.email = '';
    if (typeof editActive === 'boolean') data.active = editActive;
    // Always send the quota (wholesale replace) so clearing fields
    // removes limits instead of leaving stale values.
    const poolQuotas = {};
    for (const row of editQPoolRows) {
      if (row.pool && Number(row.gb) > 0) {
        poolQuotas[row.pool] = Number(row.gb);
      }
    }
    data.quota = {
      max_vms: editQMaxVMs || 0,
      max_vcpus: editQMaxVCPUs || 0,
      max_ram_mb: editQMaxRAMMB || 0,
      max_disk_gb: editQMaxDiskGB || 0,
      pool_quotas: poolQuotas,
    };
    // Per-user pool allowlist (empty = all pools). Sent as an array so
    // a cleared selection resets to "all pools".
    data.allowed_pools = editAllowedPools;
    // Per-user network allowlist — same "empty = all networks" contract.
    data.allowed_networks = editAllowedNetworks;
    // V13-D-01: tag allowlist (empty = no tag grants).
    data.allowed_tags = editAllowedTags;
    userSaving = true;
    try {
      await api.updateUser(editing, data);
      editing = null;
      toast.success(t('users.updated'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      userSaving = false;
    }
  }

  function revokeSessions(username) {
    if (username === auth.user) return; // guarded in the UI too; button is hidden for self
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
</script>

<div class="p-4 sm:p-6 max-w-5xl">
  <PageHeader title={t('users.title')} subtitle={t('users.subtitle')}>
    {#snippet actions()}
      <SearchInput bind:value={search} placeholder={t('users.searchPlaceholder')} class="w-48" />
      <Button onclick={openAddForm}>{t('users.addUser')}<UserPlus class="w-4 h-4 ml-1.5" /></Button>
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
        { key: 'actions', label: '', align: 'right', width: '120px', render: actionsCell },
      ]}
      rows={paginated}
      rowKey="username"
      emptyMessage={search ? t('users.noUsersSearch') : t('users.noUsers')}
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
    <span
      class="inline-flex items-center gap-1 text-xs px-2.5 py-1 rounded-full font-medium capitalize {row.role ===
      'admin'
        ? 'bg-accent/10 text-accent'
        : row.role === 'operator'
          ? 'bg-info/10 text-info'
          : 'bg-muted-foreground/10 text-muted-foreground'}"
    >
      {#if row.role === 'admin'}
        <Icon name="shield" size={12} />
      {:else if row.role === 'operator'}
        <Icon name="fileText" size={12} />
      {:else}
        <Icon name="eye" size={12} />
      {/if}
      {row.role}
    </span>
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
    {#if row.allowed_pools && row.allowed_pools.length && row.role !== 'admin'}
      <div class="text-[10px] text-muted-foreground">
        {t('users.allowedPoolsSummary', { pools: row.allowed_pools.join(', ') })}
      </div>
    {/if}
    {#if row.allowed_networks && row.allowed_networks.length && row.role !== 'admin'}
      <div class="text-[10px] text-muted-foreground">
        {t('users.allowedNetworksSummary', { networks: row.allowed_networks.join(', ') })}
      </div>
    {/if}
  </div>
{/snippet}

{#snippet actionsCell(row)}
  <div class="flex items-center gap-1 justify-end">
    <button
      onclick={() => startEdit(row)}
      class="text-xs text-accent hover:bg-muted px-2 py-1 rounded"
      aria-label={`${t('users.editUser')} ${row.username}`}>{t('common.edit')}</button
    >
    {#if row.username !== auth.user}
      <button
        onclick={() => deleteUser(row.username)}
        class="text-xs text-muted-foreground hover:text-destructive hover:bg-destructive/10 px-2 py-1 rounded"
        aria-label={`${t('users.deleteUser')} ${row.username}`}>{t('common.delete')}</button
      >
    {/if}
  </div>
{/snippet}

<!-- New-user dialog: mirrors the edit dialog below field-for-field
     (quota, per-pool quota, pool/tag allowlists) so an admin can set
     everything up in one pass instead of create-then-immediately-edit. -->
<Dialog.Root
  open={showAdd}
  onOpenChange={(v) => {
    if (!v) showAdd = false;
  }}
>
  <Dialog.Content class="sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>{t('users.newUser')}</Dialog.Title>
    </Dialog.Header>
    <div class="space-y-3 text-left">
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-3 gap-y-3 items-start">
        <div class="flex flex-col gap-1">
          <label
            for="new-user-username"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.username')}</label
          >
          <Input
            id="new-user-username"
            bind:value={newUsername}
            placeholder={t('users.username')}
            class="!py-1 !text-xs"
            autocomplete="off"
          />
        </div>
        <div class="flex flex-col gap-1">
          <label
            for="new-user-email"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.emailPlaceholder')}</label
          >
          <Input
            id="new-user-email"
            bind:value={newEmail}
            type="email"
            placeholder={t('users.emailOptional')}
            class="!py-1 !text-xs"
            autocomplete="off"
          />
        </div>
        <div class="flex flex-col gap-1">
          <label
            for="new-user-password"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.passwordLabel')}</label
          >
          <Input
            id="new-user-password"
            bind:value={newPassword}
            type="password"
            placeholder={t('users.passwordMin')}
            class="!py-1 !text-xs"
            autocomplete="new-password"
          />
        </div>
        <div class="flex flex-col gap-1">
          <label
            for="new-user-password-confirm"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.passwordConfirm')}</label
          >
          <Input
            id="new-user-password-confirm"
            bind:value={newPasswordConfirm}
            type="password"
            placeholder={t('users.passwordConfirm')}
            class="!py-1 !text-xs {newPasswordConfirm && newPasswordConfirm !== newPassword
              ? '!border-destructive'
              : ''}"
            autocomplete="new-password"
          />
        </div>
      </div>
      {#if newPassword}
        <div class="flex items-center gap-2 text-xs">
          <div class="flex-1 h-1.5 rounded bg-muted overflow-hidden">
            <div
              class="h-full transition-all {newStrength.color}"
              style="width: {(newStrength.score + 1) * 20}%"
            ></div>
          </div>
          <span class="text-muted-foreground w-16 text-right">{newStrength.label}</span>
        </div>
      {/if}
      {#if newPasswordConfirm && newPasswordConfirm !== newPassword}
        <p class="text-[10px] text-destructive">{t('users.passwordMismatch')}</p>
      {/if}
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-3 gap-y-3 items-end">
        <div class="flex flex-col gap-1">
          <label
            for="new-user-role"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.role')}</label
          >
          <select id="new-user-role" bind:value={newRole} class="input !py-1 !text-xs w-full">
            <option value="viewer">{t('users.viewer')}</option>
            <option value="operator">{t('users.operator')}</option>
            <option value="admin">{t('users.admin')}</option>
          </select>
        </div>
        <label class="flex items-center gap-1 text-xs text-muted-foreground">
          <input type="checkbox" bind:checked={newMustChangePassword} class="rounded" />
          {t('users.mustChangePassword')}
        </label>
      </div>
      <!-- Per-user pool allowlist (visibility/ACL) -->
      <div class="border-t border-border pt-2">
        <div class="text-[10px] text-muted-foreground uppercase tracking-wide mb-1">
          {t('users.allowedPoolsTitle')}
        </div>
        {#if newRole === 'admin'}
          <p class="text-[10px] text-muted-foreground">{t('users.allowedPoolsAdmin')}</p>
        {:else if pools.length}
          <div class="flex flex-wrap gap-x-3 gap-y-1">
            {#each pools as p (p)}
              <label class="flex items-center gap-1 text-xs text-muted-foreground">
                <input type="checkbox" bind:group={newAllowedPools} value={p} class="rounded" />
                {p}
              </label>
            {/each}
          </div>
          <p class="text-[10px] text-muted-foreground mt-1">{t('users.allowedPoolsHint')}</p>
        {:else}
          <p class="text-[10px] text-muted-foreground">{t('users.allowedPoolsLoading')}</p>
        {/if}
      </div>
      <!-- Per-user network allowlist (visibility/ACL) -->
      <div class="border-t border-border pt-2">
        <div class="text-[10px] text-muted-foreground uppercase tracking-wide mb-1">
          {t('users.allowedNetworksTitle')}
        </div>
        {#if newRole === 'admin'}
          <p class="text-[10px] text-muted-foreground">{t('users.allowedNetworksAdmin')}</p>
        {:else if networks.length}
          <div class="flex flex-wrap gap-x-3 gap-y-1">
            {#each networks as n (n)}
              <label class="flex items-center gap-1 text-xs text-muted-foreground">
                <input type="checkbox" bind:group={newAllowedNetworks} value={n} class="rounded" />
                {n}
              </label>
            {/each}
          </div>
          <p class="text-[10px] text-muted-foreground mt-1">{t('users.allowedNetworksHint')}</p>
        {:else}
          <p class="text-[10px] text-muted-foreground">{t('users.allowedNetworksLoading')}</p>
        {/if}
      </div>
      <!-- V13-D-01: tag allowlist (RBAC policy) -->
      <div class="border-t border-border pt-2">
        <div class="text-[10px] text-muted-foreground uppercase tracking-wide mb-1">
          {t('users.allowedTagsTitle')}
        </div>
        {#if newRole === 'admin'}
          <p class="text-[10px] text-muted-foreground">{t('users.allowedTagsAdmin')}</p>
        {:else if allTags.length}
          <div class="flex flex-wrap gap-x-3 gap-y-1">
            {#each allTags as tag (tag)}
              <label class="flex items-center gap-1 text-xs text-muted-foreground">
                <input type="checkbox" bind:group={newAllowedTags} value={tag} class="rounded" />
                {tag}
              </label>
            {/each}
          </div>
          <p class="text-[10px] text-muted-foreground mt-1">{t('users.allowedTagsHint')}</p>
        {:else}
          <p class="text-[10px] text-muted-foreground">{t('users.allowedTagsEmpty')}</p>
        {/if}
      </div>
      <div class="border-t border-border pt-2 grid grid-cols-1 sm:grid-cols-2 gap-x-3 gap-y-2">
        <div>
          <label
            for="new-user-qvms"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.quotaVMs')}</label
          >
          <input
            id="new-user-qvms"
            type="number"
            min="0"
            bind:value={newQMaxVMs}
            class="input !py-1 !text-xs tnum w-full"
          />
        </div>
        <div>
          <label
            for="new-user-qvcpus"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.quotaVCPUs')}</label
          >
          <input
            id="new-user-qvcpus"
            type="number"
            min="0"
            bind:value={newQMaxVCPUs}
            class="input !py-1 !text-xs tnum w-full"
          />
        </div>
        <div>
          <label
            for="new-user-qram"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.quotaRAMMB')}</label
          >
          <input
            id="new-user-qram"
            type="number"
            min="0"
            bind:value={newQMaxRAMMB}
            class="input !py-1 !text-xs tnum w-full"
          />
        </div>
        <div>
          <label
            for="new-user-qdisk"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.quotaDiskGB')}</label
          >
          <input
            id="new-user-qdisk"
            type="number"
            min="0"
            bind:value={newQMaxDiskGB}
            class="input !py-1 !text-xs tnum w-full"
          />
        </div>
        <!-- Per-pool disk quota -->
        <div class="col-span-full border-t border-border pt-2 mt-1">
          <div class="flex items-center justify-between mb-1">
            <span class="text-[10px] text-muted-foreground uppercase tracking-wide"
              >{t('users.quotaPoolTitle')}</span
            >
            <button
              type="button"
              onclick={addNewPoolRow}
              class="text-[10px] text-accent hover:underline">{t('users.quotaPoolAdd')}</button
            >
          </div>
          {#each newQPoolRows as row, i (i)}
            <div class="flex items-center gap-2 mb-1">
              {#if pools.length}
                <select bind:value={row.pool} class="input !py-1 !text-xs w-2/5">
                  <option value="">{t('users.quotaPoolPool')}</option>
                  {#each newRole === 'admin' ? pools : pools.filter((p) => !/iso/i.test(p)) as p (p)}
                    <option value={p}>{p}</option>
                  {/each}
                  {#if row.pool && !pools.includes(row.pool)}
                    <option value={row.pool}>{row.pool}</option>
                  {/if}
                </select>
              {:else}
                <input
                  placeholder={t('users.quotaPoolPool')}
                  bind:value={row.pool}
                  class="input !py-1 !text-xs w-2/5"
                />
              {/if}
              <input
                type="number"
                min="0"
                placeholder={t('users.quotaPoolGB')}
                bind:value={row.gb}
                class="input !py-1 !text-xs tnum w-24"
              />
              <button
                type="button"
                onclick={() => removeNewPoolRow(i)}
                aria-label={t('common.remove')}
                class="text-[10px] text-muted-foreground hover:text-destructive ml-auto">✕</button
              >
            </div>
          {/each}
          <p class="text-[10px] text-muted-foreground">{t('users.quotaPoolHint')}</p>
        </div>
        <p class="col-span-full text-[10px] text-muted-foreground">{t('users.quotaHint')}</p>
      </div>
    </div>
    <Dialog.Footer>
      <Button variant="outline" onclick={() => (showAdd = false)}>{t('common.cancel')}</Button>
      <Button
        onclick={addUser}
        disabled={userAdding ||
          !newUsername ||
          newPassword.length < 8 ||
          newPassword !== newPasswordConfirm}>{t('common.create')}</Button
      >
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Edit-user dialog. Previously this form rendered inline inside the
     DataTable's actions cell — since the cell's column width is 'auto',
     a wide inline form forced the whole table to widen and scroll
     horizontally on every row, not just the one being edited. A dialog
     keeps the table's layout stable regardless of form size. -->
<Dialog.Root
  open={editing !== null}
  onOpenChange={(v) => {
    if (!v) editing = null;
  }}
>
  <Dialog.Content class="sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>{t('users.editUser')}: {editing}</Dialog.Title>
    </Dialog.Header>
    <div class="space-y-3 text-left">
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-3 gap-y-3 items-start">
        <div class="flex flex-col gap-1">
          <label
            for="edit-user-email"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.emailPlaceholder')}</label
          >
          <Input
            id="edit-user-email"
            bind:value={editEmail}
            type="email"
            placeholder={t('users.emailPlaceholder')}
            class="!py-1 !text-xs"
            autocomplete="off"
          />
        </div>
        <div class="flex flex-col gap-1">
          <label
            for="edit-user-password"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.passwordLabel')}</label
          >
          <Input
            id="edit-user-password"
            bind:value={editPassword}
            type="password"
            placeholder={t('users.newPasswordPlaceholder')}
            class="!py-1 !text-xs"
            autocomplete="new-password"
          />
        </div>
        <div class="flex flex-col gap-1">
          <label
            for="edit-user-role"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.role')}</label
          >
          <select id="edit-user-role" bind:value={editRole} class="input !py-1 !text-xs w-full">
            <option value="viewer">{t('users.viewer')}</option>
            <option value="operator">{t('users.operator')}</option>
            <option value="admin">{t('users.admin')}</option>
          </select>
        </div>
        <div class="flex items-end">
          <label class="flex items-center gap-1 text-xs text-muted-foreground">
            <input type="checkbox" bind:checked={editActive} class="rounded" />
            {t('users.statusActive')}
          </label>
        </div>
      </div>
      <!-- Per-user pool allowlist (visibility/ACL) -->
      <div class="border-t border-border pt-2">
        <div class="text-[10px] text-muted-foreground uppercase tracking-wide mb-1">
          {t('users.allowedPoolsTitle')}
        </div>
        {#if editRole === 'admin'}
          <p class="text-[10px] text-muted-foreground">{t('users.allowedPoolsAdmin')}</p>
        {:else if pools.length}
          <div class="flex flex-wrap gap-x-3 gap-y-1">
            {#each pools as p (p)}
              <label class="flex items-center gap-1 text-xs text-muted-foreground">
                <input type="checkbox" bind:group={editAllowedPools} value={p} class="rounded" />
                {p}
              </label>
            {/each}
          </div>
          <p class="text-[10px] text-muted-foreground mt-1">{t('users.allowedPoolsHint')}</p>
        {:else}
          <p class="text-[10px] text-muted-foreground">{t('users.allowedPoolsLoading')}</p>
        {/if}
      </div>
      <!-- Per-user network allowlist (visibility/ACL) -->
      <div class="border-t border-border pt-2">
        <div class="text-[10px] text-muted-foreground uppercase tracking-wide mb-1">
          {t('users.allowedNetworksTitle')}
        </div>
        {#if editRole === 'admin'}
          <p class="text-[10px] text-muted-foreground">{t('users.allowedNetworksAdmin')}</p>
        {:else if networks.length}
          <div class="flex flex-wrap gap-x-3 gap-y-1">
            {#each networks as n (n)}
              <label class="flex items-center gap-1 text-xs text-muted-foreground">
                <input type="checkbox" bind:group={editAllowedNetworks} value={n} class="rounded" />
                {n}
              </label>
            {/each}
          </div>
          <p class="text-[10px] text-muted-foreground mt-1">{t('users.allowedNetworksHint')}</p>
        {:else}
          <p class="text-[10px] text-muted-foreground">{t('users.allowedNetworksLoading')}</p>
        {/if}
      </div>
      <!-- V13-D-01: tag allowlist (RBAC policy) -->
      <div class="border-t border-border pt-2">
        <div class="text-[10px] text-muted-foreground uppercase tracking-wide mb-1">
          {t('users.allowedTagsTitle')}
        </div>
        {#if editRole === 'admin'}
          <p class="text-[10px] text-muted-foreground">{t('users.allowedTagsAdmin')}</p>
        {:else if allTags.length}
          <div class="flex flex-wrap gap-x-3 gap-y-1">
            {#each allTags as tag (tag)}
              <label class="flex items-center gap-1 text-xs text-muted-foreground">
                <input type="checkbox" bind:group={editAllowedTags} value={tag} class="rounded" />
                {tag}
              </label>
            {/each}
          </div>
          <p class="text-[10px] text-muted-foreground mt-1">{t('users.allowedTagsHint')}</p>
        {:else}
          <p class="text-[10px] text-muted-foreground">{t('users.allowedTagsEmpty')}</p>
        {/if}
      </div>
      <div class="border-t border-border pt-2 grid grid-cols-1 sm:grid-cols-2 gap-x-3 gap-y-2">
        <div>
          <label
            for="edit-user-qvms"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.quotaVMs')}</label
          >
          <input
            id="edit-user-qvms"
            type="number"
            min="0"
            bind:value={editQMaxVMs}
            class="input !py-1 !text-xs tnum w-full"
          />
        </div>
        <div>
          <label
            for="edit-user-qvcpus"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.quotaVCPUs')}</label
          >
          <input
            id="edit-user-qvcpus"
            type="number"
            min="0"
            bind:value={editQMaxVCPUs}
            class="input !py-1 !text-xs tnum w-full"
          />
        </div>
        <div>
          <label
            for="edit-user-qram"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.quotaRAMMB')}</label
          >
          <input
            id="edit-user-qram"
            type="number"
            min="0"
            bind:value={editQMaxRAMMB}
            class="input !py-1 !text-xs tnum w-full"
          />
        </div>
        <div>
          <label
            for="edit-user-qdisk"
            class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >{t('users.quotaDiskGB')}</label
          >
          <input
            id="edit-user-qdisk"
            type="number"
            min="0"
            bind:value={editQMaxDiskGB}
            class="input !py-1 !text-xs tnum w-full"
          />
        </div>
        <!-- Per-pool disk quota -->
        <div class="col-span-full border-t border-border pt-2 mt-1">
          <div class="flex items-center justify-between mb-1">
            <span class="text-[10px] text-muted-foreground uppercase tracking-wide"
              >{t('users.quotaPoolTitle')}</span
            >
            <button
              type="button"
              onclick={addPoolRow}
              class="text-[10px] text-accent hover:underline">{t('users.quotaPoolAdd')}</button
            >
          </div>
          {#each editQPoolRows as row, i (i)}
            <div class="flex items-center gap-2 mb-1">
              {#if pools.length}
                <select bind:value={row.pool} class="input !py-1 !text-xs w-2/5">
                  <option value="">{t('users.quotaPoolPool')}</option>
                  {#each editRole === 'admin' ? pools : pools.filter((p) => !/iso/i.test(p)) as p (p)}
                    <option value={p}>{p}</option>
                  {/each}
                  {#if row.pool && !pools.includes(row.pool)}
                    <option value={row.pool}>{row.pool}</option>
                  {/if}
                </select>
              {:else}
                <input
                  placeholder={t('users.quotaPoolPool')}
                  bind:value={row.pool}
                  class="input !py-1 !text-xs w-2/5"
                />
              {/if}
              <input
                type="number"
                min="0"
                placeholder={t('users.quotaPoolGB')}
                bind:value={row.gb}
                class="input !py-1 !text-xs tnum w-24"
              />
              <button
                type="button"
                onclick={() => removePoolRow(i)}
                aria-label={t('common.remove')}
                class="text-[10px] text-muted-foreground hover:text-destructive ml-auto">✕</button
              >
            </div>
          {/each}
          <p class="text-[10px] text-muted-foreground">{t('users.quotaPoolHint')}</p>
        </div>
        <p class="col-span-full text-[10px] text-muted-foreground">{t('users.quotaHint')}</p>
      </div>
    </div>
    <Dialog.Footer>
      {#if editing !== auth.user}
        <Button variant="outline" onclick={() => revokeSessions(editing)}
          >{t('users.revokeSessions')}</Button
        >
      {/if}
      <Button variant="outline" onclick={() => (editing = null)}>{t('common.cancel')}</Button>
      <Button onclick={saveEdit} disabled={userSaving}>{t('common.save')}</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<ConfirmDialog
  bind:open={confirmState.open}
  title={confirmState.title}
  description={confirmState.description}
  confirmLabel={confirmState.confirmLabel}
  variant={confirmState.variant}
  loading={confirmState.loading}
  onConfirm={confirmState.onConfirm}
/>
