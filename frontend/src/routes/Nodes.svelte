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

  let nodes = $state([]);
  let loading = $state(true);
  let error = $state('');

  // Add-node form (admin only), toggled inline like Networks.svelte's
  // create card rather than a modal — same pattern used across the app.
  let showAdd = $state(false);
  let addName = $state('');
  let addUri = $state('');
  let addSaving = $state(false);
  let addError = $state('');

  // Inline edit: one node at a time, editing a copy so cancel is free.
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
      const res = await api.listNodes();
      nodes = res.nodes || res || [];
    } catch (e) {
      error = t('nodes.errorLoading', { error: e.message });
    } finally {
      loading = false;
    }
  }

  onMount(load);

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
</script>

<div class="p-4 sm:p-6 max-w-5xl">
  <div class="flex items-center justify-between gap-3 mb-1">
    <PageHeader title={t('nodes.title')} subtitle={t('nodes.subtitle')} />
    {#if auth.isAdmin() && !showAdd}
      <Button size="sm" onclick={openAdd}>
        <Icon name="plus" size={14} class="mr-1.5" />
        {t('nodes.addNode')}
      </Button>
    {/if}
  </div>

  <Alert variant="info" class="mb-4">{t('nodes.multiHostNotice')}</Alert>

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if showAdd}
    <div class="border border-border rounded-lg bg-card p-4 mb-4 space-y-3">
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
        <Button size="sm" variant="outline" onclick={() => (showAdd = false)}
          >{t('common.cancel')}</Button
        >
      </div>
    </div>
  {/if}

  {#if loading}
    <div class="flex items-center justify-center py-24"><Spinner size="lg" /></div>
  {:else if nodes.length === 0}
    <div class="border border-border rounded-lg bg-card p-12 text-center">
      <p class="text-muted-foreground text-sm">{t('nodes.comingSoonDesc')}</p>
    </div>
  {:else}
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
      {#each nodes as n (n.id)}
        <div class="border border-border rounded-lg bg-card p-4">
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
                <Button size="sm" variant="outline" onclick={cancelEdit}
                  >{t('common.cancel')}</Button
                >
              </div>
            </div>
          {:else}
            <div class="flex items-start justify-between gap-2 mb-3">
              <div class="flex items-center gap-2 min-w-0">
                <div class="w-8 h-8 rounded-lg bg-muted flex items-center justify-center shrink-0">
                  <Icon name="server" size={16} class="text-muted-foreground" />
                </div>
                <div class="min-w-0">
                  <div class="font-medium text-sm truncate">{n.name}</div>
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
              <button
                type="button"
                onclick={() => auth.isAdmin() && toggleEnabled(n)}
                disabled={!auth.isAdmin()}
                class="text-[10px] px-1.5 py-0.5 rounded shrink-0 {n.enabled
                  ? 'bg-success/10 text-success'
                  : 'bg-muted text-muted-foreground'}"
                title={auth.isAdmin() ? t('common.enabled') + ' / ' + t('common.disabled') : ''}
              >
                {n.enabled ? t('common.enabled') : t('common.disabled')}
              </button>
            </div>
            <p class="text-xs font-mono text-muted-foreground truncate mb-2" title={n.uri}>
              {n.uri}
            </p>
            <p class="text-xs text-muted-foreground mb-3">
              {t('nodes.createdLabel')}: {fmtDate(n.created_at)}
            </p>
            {#if auth.isAdmin()}
              <div class="flex items-center gap-1.5">
                <Button size="sm" variant="outline" onclick={() => startEdit(n)}>
                  <Icon name="pencil" size={13} class="mr-1" />
                  {t('common.edit')}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  onclick={() => askDelete(n)}
                  disabled={n.type === 'local'}
                  title={n.type === 'local' ? t('nodes.cannotDeleteLocal') : ''}
                >
                  <Icon name="trash" size={13} class="mr-1" />
                  {t('common.delete')}
                </Button>
              </div>
            {/if}
          {/if}
        </div>
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
