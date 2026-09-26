<script>
  import { onMount } from 'svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import CardGridSkeleton from '$lib/components/CardGridSkeleton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import SearchInput from '$lib/components/SearchInput.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { refreshBranding } from '$lib/stores/branding.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import * as Dialog from '$lib/components/ui/dialog';
  import { t } from '../lib/i18n.svelte.js';

  let items = $state([]);
  let loading = $state(true);
  let error = $state('');
  let search = $state('');
  let activeFilter = $state('all'); // all | custom | system
  let uploading = $state(false);
  let uploadProgress = $state(0);
  let dragActive = $state(false);
  let fileInput;

  // Preview modal
  let previewItem = $state(null);

  // Assign modal
  let assignItem = $state(null);
  let assignUsage = $state('avatar');
  let assignVMID = $state('');
  let assignBusy = $state(false);
  let vms = $state([]);

  // Delete confirm
  let confirmDeleteItem = $state(null);
  let deleteForce = $state(false);
  let inUseItem = $state(null); // { item, usages } when a delete hit a 409

  async function load() {
    loading = true;
    error = '';
    try {
      const res = await api.listMedia();
      items = res.items || [];
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  async function loadVMs() {
    try {
      const res = await api.listVMs();
      vms = res.vms || res || [];
    } catch {
      vms = [];
    }
  }

  onMount(() => {
    load();
    loadVMs();
  });

  const filtered = $derived.by(() => {
    const q = search.toLowerCase().trim();
    return items.filter((it) => {
      if (activeFilter === 'custom' && it.is_system) return false;
      if (activeFilter === 'system' && !it.is_system) return false;
      if (q && !it.name.toLowerCase().includes(q)) return false;
      return true;
    });
  });

  const customCount = $derived(items.filter((i) => !i.is_system).length);
  const systemCount = $derived(items.filter((i) => i.is_system).length);
  const totalSize = $derived(items.reduce((s, i) => s + (i.size || 0), 0));

  function fmtBytes(n) {
    if (!n) return '0 B';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024;
      i++;
    }
    return `${n.toFixed(n >= 10 || i === 0 ? 0 : 1)} ${u[i]}`;
  }

  async function handleFiles(files) {
    if (!files || files.length === 0) return;
    uploading = true;
    uploadProgress = 0;
    let ok = 0;
    let failed = 0;
    for (const file of files) {
      try {
        await api.uploadMedia(file, (p) => (uploadProgress = p));
        ok++;
      } catch (e) {
        failed++;
        toast.error(`${file.name}: ${e.message}`);
      }
    }
    uploading = false;
    uploadProgress = 0;
    if (ok > 0) {
      toast.success(t('media.uploadSuccess', { count: ok }));
      await load();
    }
    if (failed > 0 && ok === 0) {
      // individual errors already surfaced
    }
  }

  function onDrop(e) {
    e.preventDefault();
    dragActive = false;
    if (e.dataTransfer?.files?.length) {
      handleFiles(e.dataTransfer.files);
    }
  }

  function onDragOver(e) {
    e.preventDefault();
    dragActive = true;
  }

  function onDragLeave() {
    dragActive = false;
  }

  function openAssign(item, usage) {
    assignItem = item;
    assignUsage = usage;
    assignVMID = vms[0]?.id || '';
  }

  async function applyUsage() {
    if (!assignItem) return;
    assignBusy = true;
    try {
      const payload = { media_id: assignItem.id, usage: assignUsage };
      if (assignUsage === 'vm_cover') payload.vm_id = assignVMID;
      const res = await api.applyMediaUsage(payload);
      // Reflect the change in the running UI: otherwise "applied" is
      // something the user has to take on faith until the next reload.
      if (assignUsage === 'avatar') auth.setAvatar(res.url);
      else if (assignUsage === 'logo' || assignUsage === 'favicon') await refreshBranding();
      const usageLabels = {
        avatar: t('media.usageAvatar'),
        logo: t('media.usageLogo'),
        favicon: t('media.usageFavicon'),
        vm_cover: t('media.usageVmCover'),
      };
      toast.success(t('media.usageApplied', { usage: usageLabels[assignUsage] || assignUsage }));
      assignItem = null;
    } catch (e) {
      toast.error(e.message);
    } finally {
      assignBusy = false;
    }
  }

  async function copyUrl(item) {
    const url = `${window.location.origin}${item.url}`;
    try {
      await navigator.clipboard.writeText(url);
      toast.success(t('media.urlCopied'));
    } catch {
      toast.error(t('media.clipboardUnavailable'));
    }
  }

  async function doDelete() {
    if (!confirmDeleteItem) return;
    const item = confirmDeleteItem;
    try {
      await api.deleteMedia(item.id, deleteForce);
      toast.success(t('media.deleted'));
      confirmDeleteItem = null;
      deleteForce = false;
      inUseItem = null;
      await load();
    } catch (e) {
      // 409 means the image is still referenced. Rather than failing with
      // an opaque message, show exactly what would break and let the user
      // decide; deleting anyway is a deliberate second step.
      if (e.status === 409 && e.data?.usages?.length) {
        confirmDeleteItem = null;
        inUseItem = { item, usages: e.data.usages };
        return;
      }
      toast.error(deleteErrorMessage(e, item));
      confirmDeleteItem = null;
      deleteForce = false;
    }
  }

  // The backend answers delete failures with fixed English/Spanish
  // strings; map the statuses we know to the UI language.
  function deleteErrorMessage(e, item) {
    if (e?.status === 404) return t('media.notFound');
    if (e?.status === 403 && item?.id?.startsWith('system:')) return t('media.systemDeleteDenied');
    return e?.message || t('common.error');
  }

  // Second step: the user has seen the usage list and still wants it gone.
  function confirmForcedDelete() {
    const pending = inUseItem;
    inUseItem = null;
    if (!pending) return;
    confirmDeleteItem = pending.item;
    deleteForce = true;
    doDelete();
  }

  function usageLabel(u) {
    if (u.kind === 'avatar') return t('media.inUseAvatar', { name: u.name || '' });
    if (u.kind === 'logo') return t('media.inUseLogo');
    if (u.kind === 'favicon') return t('media.inUseFavicon');
    if (u.kind === 'vm_cover') return t('media.inUseVmCover', { name: u.name || u.id || '' });
    return u.kind;
  }
</script>

<div class="p-4 sm:p-6 w-full max-w-6xl mx-auto">
  <PageHeader title={t('media.title')} subtitle={t('media.subtitle')}>
    {#snippet actions()}
      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" onclick={load}>
          <Icon name="refresh" size={14} class="mr-1.5" />
          {t('common.refresh')}
        </Button>
        {#if auth.canMutate()}
          <Button size="sm" onclick={() => fileInput?.click()} disabled={uploading}>
            {#if uploading}
              <Spinner size="xs" color="text-white" />
            {:else}
              <Icon name="upload" size={14} class="mr-1.5" />
            {/if}
            {t('media.upload')}
          </Button>
        {/if}
      </div>
    {/snippet}
  </PageHeader>

  <input
    bind:this={fileInput}
    type="file"
    accept=".png,.jpg,.jpeg,.webp,.svg,.ico,.gif"
    multiple
    class="hidden"
    onchange={(e) => {
      handleFiles(e.target.files);
      e.target.value = '';
    }}
  />

  {#if !loading && items.length > 0}
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-3 mb-4">
      <StatCard label={t('media.totalAssets')} value={String(items.length)} />
      <StatCard label={t('media.customAssets')} value={String(customCount)} />
      <StatCard label={t('common.size')} value={fmtBytes(totalSize)} />
    </div>
  {/if}

  <!-- Drag & Drop upload zone -->
  {#if auth.canMutate()}
    <div
      role="button"
      tabindex="0"
      ondragover={onDragOver}
      ondragleave={onDragLeave}
      ondrop={onDrop}
      onclick={() => fileInput?.click()}
      onkeydown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') fileInput?.click();
      }}
      class="mb-4 rounded-xl border-2 border-dashed p-5 text-center cursor-pointer transition-colors {dragActive
        ? 'border-accent bg-accent/10'
        : 'border-border hover:border-accent/50 hover:bg-muted/20'}"
    >
      {#if uploading}
        <div class="flex flex-col items-center gap-2">
          <Spinner size="md" />
          <span class="text-xs text-muted-foreground">{uploadProgress}%</span>
        </div>
      {:else}
        <div class="flex items-center justify-center gap-2 text-muted-foreground">
          <Icon name="image" size={18} />
          <span class="text-sm">{t('media.dropHint')}</span>
        </div>
        <p class="text-[11px] text-muted-foreground mt-1">{t('media.allowedFormats')}</p>
      {/if}
    </div>
  {/if}

  <!-- Filters -->
  <div class="flex flex-wrap items-center justify-between gap-2 mb-4">
    <div
      class="flex items-center bg-background border border-border rounded-lg p-0.5 text-xs font-medium"
    >
      <button
        type="button"
        onclick={() => (activeFilter = 'all')}
        class="px-3 py-1.5 rounded-md transition-colors {activeFilter === 'all'
          ? 'bg-accent text-white'
          : 'text-muted-foreground hover:text-foreground'}"
      >
        {t('media.filterAll')} ({items.length})
      </button>
      <button
        type="button"
        onclick={() => (activeFilter = 'custom')}
        class="px-3 py-1.5 rounded-md transition-colors {activeFilter === 'custom'
          ? 'bg-accent text-white'
          : 'text-muted-foreground hover:text-foreground'}"
      >
        {t('media.filterCustom')} ({customCount})
      </button>
      <button
        type="button"
        onclick={() => (activeFilter = 'system')}
        class="px-3 py-1.5 rounded-md transition-colors {activeFilter === 'system'
          ? 'bg-accent text-white'
          : 'text-muted-foreground hover:text-foreground'}"
      >
        {t('media.filterSystem')} ({systemCount})
      </button>
    </div>
    <SearchInput
      bind:value={search}
      placeholder={t('media.searchPlaceholder')}
      class="w-full sm:w-64"
    />
  </div>

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  <!-- System protection notice -->
  {#if activeFilter !== 'custom' && systemCount > 0}
    <div
      class="mb-4 rounded-xl border border-warning/40 bg-warning-subtle p-3.5 flex items-start gap-3"
    >
      <Icon name="warning" size={18} class="text-warning shrink-0 mt-0.5" />
      <div>
        <p class="text-sm font-semibold text-warning">{t('media.systemProtectedTitle')}</p>
        <p class="text-xs text-warning/80 mt-0.5">{t('media.systemProtectedNotice')}</p>
      </div>
    </div>
  {/if}

  {#if loading}
    <CardGridSkeleton count={8} cols="grid-cols-2 sm:grid-cols-3 lg:grid-cols-4" />
  {:else if filtered.length === 0}
    <EmptyState title={t('media.empty')} description={t('media.emptyHint')} icon="empty" />
  {:else}
    <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-4">
      {#each filtered as item (item.id)}
        <div
          class="group rounded-xl border bg-card overflow-hidden flex flex-col transition-all hover:shadow-md {item.is_system
            ? 'border-warning/30'
            : 'border-border'}"
        >
          <!-- Thumbnail -->
          <button
            type="button"
            onclick={() => (previewItem = item)}
            class="relative aspect-video bg-muted/20 flex items-center justify-center overflow-hidden cursor-pointer group-hover:bg-muted/30 transition-colors"
            title={t('media.preview')}
          >
            <div class="absolute inset-0 opacity-40 checkerboard-bg pointer-events-none"></div>
            <img
              src={item.url}
              alt={item.name}
              loading="lazy"
              class="w-full h-full object-contain p-2 relative z-10 transition-transform duration-200 group-hover:scale-105"
            />
            {#if item.is_system}
              <span
                class="absolute top-2 right-2 px-1.5 py-0.5 rounded bg-warning text-warning-foreground text-[10px] font-semibold flex items-center gap-1 z-20 shadow-2xs"
              >
                <Icon name="lock" size={9} />
                {t('media.protected')}
              </span>
            {/if}
          </button>

          <!-- Meta -->
          <div class="p-2.5 flex-1 flex flex-col">
            <p class="text-xs font-medium text-foreground truncate" title={item.name}>
              {item.name}
            </p>
            <p class="text-[10px] text-muted-foreground mt-0.5">
              {item.size_human || fmtBytes(item.size)} · {item.mime_type}
            </p>
          </div>

          <!-- Actions -->
          <div class="border-t border-border/60 p-1.5 flex items-center gap-1">
            <button
              type="button"
              title={t('media.setAvatar')}
              onclick={() => openAssign(item, 'avatar')}
              class="p-1.5 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            >
              <Icon name="users" size={14} />
            </button>
            <button
              type="button"
              title={t('media.setCover')}
              onclick={() => openAssign(item, 'vm_cover')}
              class="p-1.5 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            >
              <Icon name="monitor" size={14} />
            </button>
            <button
              type="button"
              title={t('media.setLogo')}
              onclick={() => openAssign(item, 'logo')}
              class="p-1.5 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            >
              <Icon name="sparkles" size={14} />
            </button>
            <button
              type="button"
              title={t('media.setFavicon')}
              onclick={() => openAssign(item, 'favicon')}
              class="p-1.5 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            >
              <Icon name="globe" size={14} />
            </button>
            <button
              type="button"
              title={t('media.copyUrl')}
              onclick={() => copyUrl(item)}
              class="p-1.5 rounded hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
            >
              <Icon name="copy" size={14} />
            </button>
            {#if auth.canMutate() && !item.is_system}
              <button
                type="button"
                aria-label={t('common.delete')}
                title={t('common.delete')}
                onclick={() => (confirmDeleteItem = item)}
                class="ml-auto p-1.5 rounded hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-colors"
              >
                <Icon name="trash" size={14} />
              </button>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Preview modal -->
<Dialog.Root open={!!previewItem} onOpenChange={(o) => !o && (previewItem = null)}>
  <Dialog.Content class="sm:max-w-2xl [&>*]:min-w-0">
    <Dialog.Header>
      <Dialog.Title>{previewItem?.name}</Dialog.Title>
      <Dialog.Description>
        {previewItem?.size_human || fmtBytes(previewItem?.size)} · {previewItem?.mime_type}
      </Dialog.Description>
    </Dialog.Header>
    {#if previewItem}
      <div class="rounded-lg bg-muted/30 p-4 flex items-center justify-center max-h-[60vh]">
        <img src={previewItem.url} alt={previewItem.name} class="max-w-full max-h-[55vh]" />
      </div>
    {/if}
  </Dialog.Content>
</Dialog.Root>

<!-- Assign usage modal -->
<Dialog.Root open={!!assignItem} onOpenChange={(o) => !o && (assignItem = null)}>
  <Dialog.Content class="sm:max-w-md [&>*]:min-w-0">
    <Dialog.Header>
      <Dialog.Title>
        {#if assignUsage === 'avatar'}{t('media.setAvatar')}
        {:else if assignUsage === 'vm_cover'}{t('media.setCover')}
        {:else if assignUsage === 'logo'}{t('media.setLogo')}
        {:else}{t('media.setFavicon')}{/if}
      </Dialog.Title>
      <Dialog.Description>{assignItem?.name}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3 my-2">
      {#if assignItem}
        <div class="flex items-center gap-3 p-2.5 rounded-xl border border-border bg-muted/20">
          <div
            class="w-12 h-12 rounded-lg bg-muted/40 overflow-hidden shrink-0 flex items-center justify-center border border-border/60"
          >
            <img src={assignItem.url} alt="" class="w-full h-full object-contain" />
          </div>
          <div class="min-w-0 flex-1">
            <p class="text-xs font-semibold truncate text-foreground">{assignItem.name}</p>
            <p class="text-[11px] text-muted-foreground">
              {assignItem.size_human || fmtBytes(assignItem.size)} · {assignItem.mime_type}
            </p>
          </div>
        </div>
      {/if}

      {#if assignUsage === 'vm_cover'}
        <div>
          <label for="assign-vm" class="text-xs font-semibold block mb-1"
            >{t('media.selectVM')}</label
          >
          <select id="assign-vm" bind:value={assignVMID} class="input w-full text-xs">
            {#each vms as vm (vm.id)}
              <option value={vm.id}>{vm.alias || vm.name} ({vm.type || 'vm'})</option>
            {/each}
          </select>
        </div>
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (assignItem = null)} disabled={assignBusy}>
        {t('common.cancel')}
      </Button>
      <Button
        onclick={applyUsage}
        disabled={assignBusy || (assignUsage === 'vm_cover' && !assignVMID)}
      >
        {#if assignBusy}<Spinner size="xs" color="text-white" />{:else}{t('media.apply')}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<ConfirmDialog
  open={!!confirmDeleteItem}
  title={t('media.deleteTitle')}
  message={t('media.deleteMessage', { name: confirmDeleteItem?.name || '' })}
  confirmLabel={t('common.delete')}
  variant="destructive"
  onConfirm={doDelete}
  onCancel={() => {
    confirmDeleteItem = null;
    deleteForce = false;
  }}
/>

<!-- The image is still referenced somewhere. Name every usage so the
     consequence of deleting is visible before it happens. -->
<Dialog.Root open={!!inUseItem} onOpenChange={(o) => !o && (inUseItem = null)}>
  <Dialog.Content class="sm:max-w-md [&>*]:min-w-0">
    <Dialog.Header>
      <Dialog.Title>{t('media.inUseTitle')}</Dialog.Title>
      <Dialog.Description>
        {t('media.inUseDesc', { name: inUseItem?.item?.name || '' })}
      </Dialog.Description>
    </Dialog.Header>
    <ul class="text-sm space-y-1 my-2 max-h-48 overflow-y-auto">
      {#each inUseItem?.usages || [] as u, i (i)}
        <li class="flex items-center gap-2">
          <Icon name="alert-triangle" size={14} class="text-warning shrink-0" />
          <span>{usageLabel(u)}</span>
        </li>
      {/each}
    </ul>
    <Dialog.Footer>
      <Button variant="outline" onclick={() => (inUseItem = null)}>{t('common.cancel')}</Button>
      <Button variant="destructive" onclick={confirmForcedDelete}>
        {t('media.inUseDeleteAnyway')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
