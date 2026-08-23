<script>
  import { onMount } from 'svelte';
  import { api, auth, passwordStrength } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import DataTable from '$lib/components/DataTable.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import SearchInput from '$lib/components/SearchInput.svelte';
  import { UserPlus } from '@lucide/svelte';
  import { t } from '../lib/i18n.svelte.js';

  let users = $state([]);
  let loading = $state(true);
  let error = $state('');
  let search = $state('');
  let page = $state(0);
  const PAGE_SIZE = 10;

  let showAdd = $state(false);
  let newUsername = $state('');
  let newPassword = $state('');
  let newRole = $state('operator');
  let newEmail = $state('');

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
    return users.filter((u) => u.username.toLowerCase().includes(q));
  });

  const paginated = $derived(filtered.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE));
  const totalPages = $derived(Math.max(1, Math.ceil(filtered.length / PAGE_SIZE)));

  $effect(() => {
    void search; // track search to re-run on change
    page = 0;
  });

  onMount(() => {
    if (auth.isAdmin()) load();
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

  function askConfirm(opts) {
    confirmState = { ...opts, open: true, loading: false };
  }

  async function addUser() {
    if (!newUsername || !newPassword || newPassword.length < 8) return;
    // Capture names BEFORE resetting (was a pre-existing toast bug —
    // the name was used after being cleared).
    const createdName = newUsername;
    const createdRole = newRole;
    try {
      await api.createUser({
        username: createdName,
        password: newPassword,
        role: createdRole,
        email: newEmail,
      });
      newUsername = '';
      newPassword = '';
      newRole = 'operator';
      newEmail = '';
      showAdd = false;
      toast.success(t('users.created', { name: createdName }));
      await load();
    } catch (e) {
      toast.error(e.message);
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
  }

  async function saveEdit() {
    const data = {};
    if (editPassword) data.password = editPassword;
    if (editRole) data.role = editRole;
    if (editEmail) data.email = editEmail;
    else data.email = '';
    if (typeof editActive === 'boolean') data.active = editActive;
    // Always send the quota (wholesale replace) so clearing fields
    // removes limits instead of leaving stale values.
    data.quota = {
      max_vms: editQMaxVMs || 0,
      max_vcpus: editQMaxVCPUs || 0,
      max_ram_mb: editQMaxRAMMB || 0,
      max_disk_gb: editQMaxDiskGB || 0,
    };
    try {
      await api.updateUser(editing, data);
      editing = null;
      toast.success(t('users.updated'));
      await load();
    } catch (e) {
      toast.error(e.message);
    }
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

<div class="p-6 max-w-5xl">
  <PageHeader title={t('users.title')} subtitle={t('users.subtitle')}>
    {#snippet actions()}
      <SearchInput bind:value={search} placeholder={t('users.searchPlaceholder')} class="w-48" />
      <Button onclick={() => (showAdd = !showAdd)}
        >{showAdd ? t('common.cancel') : t('users.addUser')}{#if !showAdd}<UserPlus
            class="w-4 h-4 ml-1.5"
          />{/if}</Button
      >
    {/snippet}
  </PageHeader>

  {#if error}
    <div class="mb-4"><Alert variant="error">{error}</Alert></div>
  {/if}

  {#if showAdd}
    <div class="border border-border rounded-lg bg-card p-4 mb-4 space-y-3">
      <div class="text-sm font-medium">{t('users.newUser')}</div>
      <div class="grid grid-cols-4 gap-2">
        <Input bind:value={newUsername} placeholder={t('users.username')} autocomplete="off" />
        <Input
          bind:value={newPassword}
          type="password"
          placeholder={t('users.passwordMin')}
          autocomplete="new-password"
        />
        <Input
          bind:value={newEmail}
          type="email"
          placeholder={t('users.emailOptional')}
          autocomplete="off"
        />
        <select bind:value={newRole} class="input">
          <option value="viewer">{t('users.viewer')}</option>
          <option value="operator">{t('users.operator')}</option>
          <option value="admin">{t('users.admin')}</option>
        </select>
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
      <div>
        <Button onclick={addUser} disabled={!newUsername || newPassword.length < 8}
          >{t('common.create')}</Button
        >
      </div>
    </div>
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
        { key: 'actions', label: '', align: 'right', width: 'auto', render: actionsCell },
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
  {#if editing === row.username}
    <div class="space-y-1">
      <Input
        bind:value={editEmail}
        type="email"
        placeholder={t('users.emailPlaceholder')}
        class="!py-1 !text-xs"
        autocomplete="off"
      />
      <Input
        bind:value={editPassword}
        type="password"
        placeholder={t('users.newPasswordPlaceholder')}
        class="!py-1 !text-xs"
        autocomplete="new-password"
      />
    </div>
  {:else}
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
  {/if}
{/snippet}

{#snippet roleCell(row)}
  {#if editing === row.username}
    <div class="space-y-1">
      <select bind:value={editRole} class="input !py-1 !text-xs w-28">
        <option value="viewer">{t('users.viewer')}</option>
        <option value="operator">{t('users.operator')}</option>
        <option value="admin">{t('users.admin')}</option>
      </select>
      <label class="flex items-center gap-1 text-xs text-muted-foreground">
        <input type="checkbox" bind:checked={editActive} class="rounded" />
        {t('users.statusActive')}
      </label>
    </div>
  {:else}
    <div class="space-y-1">
      <span
        class="inline-flex items-center gap-1 text-xs px-2.5 py-1 rounded-full font-medium capitalize {row.role ===
        'admin'
          ? 'bg-accent/10 text-accent'
          : row.role === 'operator'
            ? 'bg-info/10 text-info'
            : 'bg-muted text-muted-foreground'}"
      >
        {#if row.role === 'admin'}
          <svg
            class="w-3 h-3"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            viewBox="0 0 24 24"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" /></svg
          >
        {:else if row.role === 'operator'}
          <svg
            class="w-3 h-3"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            viewBox="0 0 24 24"
            ><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" /><polyline
              points="14 2 14 8 20 8"
            /></svg
          >
        {/if}
        {row.role}
      </span>
      {#if row.quota?.max_vms || row.quota?.max_vcpus || row.quota?.max_ram_mb || row.quota?.max_disk_gb}
        <div class="text-[10px] text-muted-foreground">{t('users.quotaSummary', row.quota)}</div>
      {/if}
    </div>
  {/if}
{/snippet}

{#snippet actionsCell(row)}
  {#if editing === row.username}
    <div class="flex items-center gap-1 justify-end">
      <button onclick={saveEdit} class="text-xs text-success hover:bg-success/10 px-2 py-1 rounded"
        >{t('common.save')}</button
      >
      <button
        onclick={() => (editing = null)}
        class="text-xs text-muted-foreground hover:bg-muted px-2 py-1 rounded"
        >{t('common.cancel')}</button
      >
    </div>
    <div class="mt-2 border-t border-border pt-2 grid grid-cols-2 gap-x-3 gap-y-2">
      <div>
        <label class="text-[10px] text-muted-foreground uppercase tracking-wide"
          >{t('users.quotaVMs')}</label
        >
        <input
          type="number"
          min="0"
          bind:value={editQMaxVMs}
          class="input !py-1 !text-xs tnum w-full"
        />
      </div>
      <div>
        <label class="text-[10px] text-muted-foreground uppercase tracking-wide"
          >{t('users.quotaVCPUs')}</label
        >
        <input
          type="number"
          min="0"
          bind:value={editQMaxVCPUs}
          class="input !py-1 !text-xs tnum w-full"
        />
      </div>
      <div>
        <label class="text-[10px] text-muted-foreground uppercase tracking-wide"
          >{t('users.quotaRAMMB')}</label
        >
        <input
          type="number"
          min="0"
          bind:value={editQMaxRAMMB}
          class="input !py-1 !text-xs tnum w-full"
        />
      </div>
      <div>
        <label class="text-[10px] text-muted-foreground uppercase tracking-wide"
          >{t('users.quotaDiskGB')}</label
        >
        <input
          type="number"
          min="0"
          bind:value={editQMaxDiskGB}
          class="input !py-1 !text-xs tnum w-full"
        />
      </div>
      <p class="col-span-2 text-[10px] text-muted-foreground">{t('users.quotaHint')}</p>
    </div>
  {:else}
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
  {/if}
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
