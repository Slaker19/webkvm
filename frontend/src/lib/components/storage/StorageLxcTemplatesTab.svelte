<script>
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import { formatBytes } from '$lib/utils/format.js';
  import { toast } from '$lib/components/ui/toast';
  import { trackImageJob } from '$lib/stores/imageJobs.svelte.js';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';

  let {
    lxcImages = [],
    onRefresh = () => {},
    askConfirm = () => {},
  } = $props();

  let lxcSearch = $state('');
  let lxcFilter = $state('all');
  let downloadingRef = $state('');

  const filteredTemplates = $derived.by(() => {
    let list = lxcImages || [];
    if (lxcFilter === 'local') {
      list = list.filter((img) => img.is_local);
    } else if (lxcFilter === 'official') {
      list = list.filter((img) => !img.is_local);
    }
    if (!lxcSearch.trim()) return list;
    const q = lxcSearch.toLowerCase();
    return list.filter(
      (img) =>
        (img.name && img.name.toLowerCase().includes(q)) ||
        (img.ref && img.ref.toLowerCase().includes(q)) ||
        (img.os && img.os.toLowerCase().includes(q))
    );
  });

  async function downloadLxcImage(img) {
    const ref = img.ref;
    downloadingRef = ref;
    try {
      const res = await api.pullContainerImage(ref);
      if (res?.job_id) {
        trackImageJob({
          jobId: res.job_id,
          taskId: 'container_pull_' + ref,
          title: 'Plantilla ' + (img.name || ref),
          kind: 'download',
        });
      }
      toast.success(t('storage.startingDownload') + ': ' + (img.name || ref));
    } catch (e) {
      toast.error(e.message || t('storage.requestDownloadError'));
    } finally {
      downloadingRef = '';
    }
  }

  function deleteLxcImage(img) {
    const fp = img.fingerprint || img.ref;
    askConfirm({
      title: t('storage.deleteImageConfirm', { name: img.name || img.ref }),
      description: t('storage.deleteIsoDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        try {
          await api.deleteContainerImage(fp);
          toast.success(t('common.deleted'));
          onRefresh();
        } catch (e) {
          toast.error(e.message);
        }
      },
    });
  }
</script>

<div class="p-4 sm:p-5 space-y-4">
  <div
    class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 bg-muted/20 border border-border p-3.5 rounded-xl"
  >
    <div class="flex items-center gap-3">
      <div
        class="w-8 h-8 rounded-lg bg-success/15 flex items-center justify-center text-success"
      >
        <Icon name="box" size={16} />
      </div>
      <div>
        <h3 class="text-sm font-bold text-foreground">
          {t('storage.lxcTemplatesTitle')}
        </h3>
        <p class="text-xs text-muted-foreground">
          {t('storage.lxcTemplatesDesc')}
        </p>
      </div>
    </div>

    <div class="flex items-center gap-2">
      <div
        class="flex items-center bg-background border border-border rounded-lg p-0.5 text-xs font-medium"
      >
        <button
          onclick={() => (lxcFilter = 'all')}
          class="px-2.5 py-1 rounded-md transition-all {lxcFilter === 'all'
            ? 'bg-accent text-accent-foreground font-bold'
            : 'text-muted-foreground'}"
        >
          {t('storage.filterAll')}
        </button>
        <button
          onclick={() => (lxcFilter = 'local')}
          class="px-2.5 py-1 rounded-md transition-all {lxcFilter === 'local'
            ? 'bg-success text-success-foreground font-bold'
            : 'text-muted-foreground'}"
        >
          {t('storage.filterLocal')}
        </button>
        <button
          onclick={() => (lxcFilter = 'official')}
          class="px-2.5 py-1 rounded-md transition-all {lxcFilter === 'official'
            ? 'bg-accent text-accent-foreground font-bold'
            : 'text-muted-foreground'}"
        >
          {t('storage.filterOfficial')}
        </button>
      </div>

      <div class="relative w-48 sm:w-56">
        <Icon
          name="search"
          size={13}
          class="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
        />
        <input
          type="text"
          placeholder={t('storage.searchTemplates')}
          bind:value={lxcSearch}
          class="w-full pl-7 pr-2.5 py-1 rounded-lg bg-background border border-border text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
        />
      </div>
    </div>
  </div>

  {#if filteredTemplates.length === 0}
    <EmptyState compact icon="boxes" title={t('storage.noLxcTemplates')} />
  {:else}
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3.5">
      {#each filteredTemplates as img (img.ref || img.fingerprint)}
        <div
          class="flex flex-col justify-between rounded-xl border bg-background p-3.5 transition-all duration-150 {img.is_local
            ? 'border-success/30 bg-success/[0.02]'
            : 'border-border'}"
        >
          <div>
            <div class="flex items-start justify-between gap-2 mb-2">
              <div class="flex items-center gap-2 min-w-0">
                <div
                  class="w-8 h-8 rounded-lg flex items-center justify-center font-bold text-xs {img.is_local
                    ? 'bg-success/15 text-success'
                    : 'bg-muted text-muted-foreground'}"
                >
                  {img.os ? img.os.slice(0, 3).toUpperCase() : 'LXC'}
                </div>
                <div class="min-w-0">
                  <h4 class="font-bold text-xs text-foreground truncate" title={img.name}>
                    {img.name || img.ref}
                  </h4>
                  <p class="text-[10px] text-muted-foreground font-mono truncate">
                    {img.ref}
                  </p>
                </div>
              </div>

              {#if img.is_local}
                <span
                  class="px-1.5 py-0.5 rounded-full bg-success/15 text-success font-mono text-[9px] font-bold border border-success/30 shrink-0"
                >
                  ✓ {t('storage.cached')}
                </span>
              {/if}
            </div>

            <div
              class="my-2 flex items-center gap-2 text-[10px] font-mono text-muted-foreground"
            >
              <span>{img.arch || 'x86_64'}</span>
              <span>•</span>
              <span>{img.size > 0 ? formatBytes(img.size) : '~130 MB'}</span>
              {#if img.fingerprint}
                <span>•</span>
                <span class="truncate max-w-[65px]">fp:{img.fingerprint}</span>
              {/if}
            </div>
          </div>

          <div class="pt-2.5 border-t border-border flex items-center justify-between gap-2">
            {#if img.is_local}
              <Button
                size="sm"
                variant="outline"
                class="!h-7 !text-xs text-destructive hover:bg-destructive/10 w-full"
                onclick={() => deleteLxcImage(img)}
              >
                <Icon name="trash" size={13} class="mr-1" />
                {t('storage.deleteFromCache')}
              </Button>
            {:else}
              <Button
                size="sm"
                class="!h-7 !text-xs w-full bg-success hover:bg-success/85 text-success-foreground"
                disabled={downloadingRef === img.ref}
                onclick={() => downloadLxcImage(img)}
              >
                {#if downloadingRef === img.ref}
                  <Spinner size="xs" class="mr-1" />
                {:else}
                  <Icon name="download" size={13} class="mr-1" />
                {/if}
                {t('storage.downloadToCache')}
              </Button>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>
