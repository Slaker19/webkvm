<script>
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { formatBytes } from '$lib/utils/format.js';
  import { toast } from '$lib/components/ui/toast';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { hasPurpose } from '$lib/purpose.js';

  let {
    pools = [],
    selectedLxcPool = $bindable(''),
    askConfirm = () => {},
    showStorageErr = () => {},
  } = $props();

  let volumes = $state([]);
  let loadingVolumes = $state(false);
  let lxcDiskSearch = $state('');

  const lxcPools = $derived(
    pools.filter((p) => hasPurpose(p, 'container') || (p.type && p.type.includes('incus')))
  );

  $effect(() => {
    if (selectedLxcPool) {
      loadVolumes(selectedLxcPool);
    }
  });

  async function loadVolumes(pool) {
    if (!pool) return;
    loadingVolumes = true;
    try {
      volumes = (await api.listVolumes(pool).catch(() => [])) || [];
    } finally {
      loadingVolumes = false;
    }
  }

  function bytesToStr(b) {
    if (!b && b !== 0) return '-';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (b >= 1024 && i < u.length - 1) {
      b /= 1024;
      i++;
    }
    return b.toFixed(i > 0 ? 1 : 0) + ' ' + u[i];
  }

  const filteredLxcVolumes = $derived(
    volumes.filter(
      (v) => !lxcDiskSearch.trim() || v.name.toLowerCase().includes(lxcDiskSearch.toLowerCase())
    )
  );

  function deleteVolume(pool, name) {
    askConfirm({
      title: t('storage.deleteVolumeTitle', { name }),
      description: t('storage.deleteVolumeDesc', { pool }),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        try {
          await api.deleteVolume(pool, name);
          toast.success(t('storage.volumeDeleted', { name }));
          loadVolumes(pool);
        } catch (e) {
          showStorageErr(t('storage.deleteVolumeTitle', { name }), e);
        }
      },
    });
  }
</script>

<div class="p-4 sm:p-5 space-y-4">
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
    <div class="flex flex-wrap items-center gap-3">
      <div class="flex items-center gap-2">
        <span class="text-xs font-semibold text-muted-foreground"
          >{t('storage.incusPoolLabel')}</span
        >
        <select bind:value={selectedLxcPool} class="input !py-1 !text-xs !w-auto font-medium">
          {#each lxcPools as p (p.name)}
            <option value={p.name}>{p.name} ({p.type || 'dir'})</option>
          {/each}
        </select>
      </div>

      <div class="relative w-48 sm:w-60">
        <Icon
          name="search"
          size={13}
          class="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
        />
        <input
          type="text"
          placeholder={t('storage.searchLxcVolumes')}
          bind:value={lxcDiskSearch}
          class="w-full pl-7 pr-2.5 py-1 rounded-lg bg-background border border-border text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
        />
      </div>
    </div>
  </div>

  {#if loadingVolumes}
    <div class="flex justify-center py-12"><Spinner size="lg" /></div>
  {:else if filteredLxcVolumes.length === 0}
    <EmptyState compact icon="boxes" title={t('storage.noLxcDisks')} />
  {:else}
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
      {#each filteredLxcVolumes as vol (vol.name)}
        <div
          class="p-3.5 rounded-xl border border-border bg-background flex flex-col justify-between gap-3 shadow-xs"
        >
          <div>
            <div class="flex items-start justify-between gap-2 mb-1">
              <div class="flex items-center gap-2 min-w-0">
                <div
                  class="w-8 h-8 rounded-lg bg-success/10 text-success flex items-center justify-center shrink-0"
                >
                  <Icon name="cpu" size={16} />
                </div>
                <div class="min-w-0">
                  <h4 class="text-sm font-bold text-foreground truncate" title={vol.name}>
                    {vol.name}
                  </h4>
                  <span
                    class="text-[10px] px-1.5 py-0.2 rounded bg-muted font-mono uppercase text-muted-foreground"
                  >
                    {vol.format || 'filesystem'}
                  </span>
                </div>
              </div>
              <span class="text-xs font-mono font-semibold text-success shrink-0">
                {vol.allocated > 0 ? formatBytes(vol.allocated) : bytesToStr(vol.capacity)}
              </span>
            </div>
            <p
              class="text-[11px] text-muted-foreground font-mono truncate mt-2 bg-muted/20 p-1.5 rounded"
            >
              {vol.path}
            </p>
          </div>

          <div
            class="flex items-center justify-between pt-2 border-t border-border text-xs text-muted-foreground"
          >
            <span class="flex items-center gap-1">
              <Icon name="hardDrive" size={12} />
              {t('storage.poolLabelShort')}
              <strong class="text-foreground">{vol.pool || selectedLxcPool}</strong>
            </span>
            <button
              onclick={() => deleteVolume(vol.pool || selectedLxcPool, vol.name)}
              class="p-1 rounded text-muted-foreground hover:text-destructive hover:bg-destructive/10"
              aria-label={t('storage.deleteVolumeAria')}
              title={t('storage.deleteVolumeAria')}
            >
              <Icon name="trash" size={14} />
            </button>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>
