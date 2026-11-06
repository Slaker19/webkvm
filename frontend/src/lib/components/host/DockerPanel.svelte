<script>
  import { t } from '$lib/i18n.svelte.js';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  let { docker, onRefresh } = $props();

  // Tab switcher: 'containers' | 'images'
  let activeTab = $state('containers');

  // Confirmation dialog state
  let confirmState = $state({
    open: false,
    title: '',
    description: '',
    confirmLabel: '',
    variant: 'destructive',
    onConfirm: () => {},
    loading: false,
  });

  function askConfirm(opts) {
    confirmState = {
      ...opts,
      open: true,
      loading: false,
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await opts.onConfirm();
          confirmState.open = false;
          onRefresh?.();
        } catch (e) {
          toast.error(e.message);
        } finally {
          confirmState.loading = false;
        }
      },
    };
  }

  function getStateColor(state) {
    if (state === 'running') return 'bg-success';
    if (state === 'exited' || state === 'dead') return 'bg-destructive';
    return 'bg-warning';
  }

  function getStateText(state) {
    if (state === 'running') return t('host.containerRunning');
    if (state === 'exited') return t('host.containerStopped');
    return state;
  }

  function fmtBytes(bytes) {
    if (!bytes && bytes !== 0) return '—';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let idx = 0;
    let val = bytes;
    while (val >= 1024 && idx < units.length - 1) {
      val /= 1024;
      idx++;
    }
    return `${val.toFixed(1)} ${units[idx]}`;
  }

  async function containerAction(id, action) {
    try {
      await api.dockerContainerAction(id, action);
      toast.success(t('host.dockerActionSuccess', { action, id: id.slice(0, 12) }));
      onRefresh?.();
    } catch (e) {
      toast.error(e.message);
    }
  }

  async function deleteContainer(id) {
    askConfirm({
      title: t('host.dockerDeleteContainerTitle'),
      description: t('host.dockerDeleteContainerDesc', { id: id.slice(0, 12) }),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        await api.deleteDockerContainer(id);
        toast.success(t('host.dockerContainerDeleted'));
      },
    });
  }

  async function deleteImage(id) {
    askConfirm({
      title: t('host.dockerDeleteImageTitle'),
      description: t('host.dockerDeleteImageDesc', { id: id.slice(0, 12) }),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        await api.deleteDockerImage(id);
        toast.success(t('host.dockerImageDeleted'));
      },
    });
  }

  async function pruneImages() {
    askConfirm({
      title: t('host.dockerPruneImagesTitle'),
      description: t('host.dockerPruneImagesDesc'),
      confirmLabel: t('host.dockerPrune'),
      variant: 'default',
      onConfirm: async () => {
        const result = await api.pruneDockerImages();
        toast.success(t('host.dockerImagesPruned'));
      },
    });
  }

  const isAdmin = $derived(auth.isAdmin());
</script>

<div class="space-y-4">
  {#if !docker}
    <!-- Docker not available -->
    <div class="flex items-center gap-3 p-4 bg-muted/30 rounded-lg text-sm text-muted-foreground">
      <Icon name="alert-circle" size={18} class="shrink-0" />
      <span>{t('host.dockerNotAvailable')}</span>
    </div>
  {:else}
  <!-- Docker Summary -->
  <div class="flex items-center gap-4 px-3 py-2 bg-muted/30 rounded-lg text-xs flex-wrap">
    <span class="text-muted-foreground">
      Docker <span class="font-mono">{docker?.version || '—'}</span>
    </span>
    {#if docker?.compose_available}
      <span class="text-success flex items-center gap-1">
        <Icon name="check" size={12} />
        Compose
      </span>
    {/if}
    <span class="text-muted-foreground">
      {docker?.containers?.filter(c => c.state === 'running').length || 0} / {docker?.containers?.length || 0} {t('host.containersRunning')}
    </span>
  </div>

  <!-- Tab Switcher -->
  <div class="flex items-center gap-2 border-b border-border">
    <button
      onclick={() => activeTab = 'containers'}
      class="px-3 py-2 text-sm font-medium transition-colors border-b-2 {activeTab === 'containers'
        ? 'border-accent text-accent'
        : 'border-transparent text-muted-foreground hover:text-foreground'}"
    >
      {t('host.containers')} ({docker?.containers?.length || 0})
    </button>
    <button
      onclick={() => activeTab = 'images'}
      class="px-3 py-2 text-sm font-medium transition-colors border-b-2 {activeTab === 'images'
        ? 'border-accent text-accent'
        : 'border-transparent text-muted-foreground hover:text-foreground'}"
    >
      {t('host.images')} ({docker?.images_count || 0})
    </button>
    {#if activeTab === 'images' && isAdmin}
      <div class="ml-auto">
        <Button variant="outline" size="sm" onclick={pruneImages}>
          <Icon name="trash" size={14} class="mr-1" />
          {t('host.dockerPrune')}
        </Button>
      </div>
    {/if}
  </div>

  <!-- Containers Tab -->
  {#if activeTab === 'containers'}
    <div class="space-y-2">
      {#each docker?.containers || [] as container (container.id)}
        <div class="flex items-center gap-3 p-3 border border-border rounded-lg hover:bg-muted/30 transition-colors">
          <span class="w-2 h-2 rounded-full {getStateColor(container.state)} shrink-0"></span>
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium truncate">{container.name}</span>
              <span class="text-xs text-muted-foreground px-1.5 py-0.5 rounded bg-muted">
                {getStateText(container.state)}
              </span>
            </div>
            <div class="text-xs text-muted-foreground truncate">
              {container.image}
              {#if container.status}
                · {container.status}
              {/if}
            </div>
          </div>
          {#if container.state === 'running'}
            <div class="text-right text-xs shrink-0 mr-2">
              <div class="font-mono tnum">
                <span class={container.cpu_percent > 50 ? 'text-destructive' : ''}>
                  {container.cpu_percent?.toFixed(1) || '0.0'}% CPU
                </span>
              </div>
              {#if container.memory_usage}
                <div class="text-muted-foreground">{container.memory_usage}</div>
              {/if}
            </div>
          {/if}
          {#if isAdmin}
            <div class="flex items-center gap-1 shrink-0">
              {#if container.state === 'running'}
                <Button
                  variant="ghost"
                  size="sm"
                  onclick={() => containerAction(container.id, 'stop')}
                  title={t('host.dockerStop')}
                >
                  <Icon name="square" size={14} />
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onclick={() => containerAction(container.id, 'restart')}
                  title={t('host.dockerRestart')}
                >
                  <Icon name="refresh" size={14} />
                </Button>
              {:else}
                <Button
                  variant="ghost"
                  size="sm"
                  onclick={() => containerAction(container.id, 'start')}
                  title={t('host.dockerStart')}
                >
                  <Icon name="play" size={14} />
                </Button>
              {/if}
              <Button
                variant="ghost"
                size="sm"
                onclick={() => deleteContainer(container.id)}
                title={t('common.delete')}
                class="text-destructive hover:text-destructive"
              >
                <Icon name="trash" size={14} />
              </Button>
            </div>
          {/if}
        </div>
      {:else}
        <p class="text-sm text-muted-foreground text-center py-4">
          {t('host.noContainers')}
        </p>
      {/each}
    </div>
  {:else if activeTab === 'images'}
    <!-- Images Tab -->
    <div class="space-y-2">
      {#each docker?.images || [] as image (image.id)}
        <div class="flex items-center gap-3 p-3 border border-border rounded-lg hover:bg-muted/30 transition-colors">
          <Icon name="package" size={16} class="text-muted-foreground shrink-0" />
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium truncate">
                {image.repository !== '<none>' ? image.repository : image.id.slice(0, 12)}
              </span>
              {#if image.tag && image.tag !== '<none>'}
                <span class="text-xs text-muted-foreground px-1.5 py-0.5 rounded bg-muted font-mono">
                  {image.tag}
                </span>
              {/if}
            </div>
            <div class="text-xs text-muted-foreground">
              {fmtBytes(image.size)} · {image.created_since}
            </div>
          </div>
          {#if isAdmin}
            <Button
              variant="ghost"
              size="sm"
              onclick={() => deleteImage(image.id)}
              title={t('common.delete')}
              class="text-destructive hover:text-destructive shrink-0"
            >
              <Icon name="trash" size={14} />
            </Button>
          {/if}
        </div>
      {:else}
        <p class="text-sm text-muted-foreground text-center py-4">
          {t('host.noImages')}
        </p>
      {/each}
    </div>
  {/if}

  <!-- Stats Summary -->
  <div class="grid grid-cols-3 gap-3 pt-3 border-t border-border">
    <div class="text-center">
      <div class="text-lg font-semibold tnum">{docker?.images_count || 0}</div>
      <div class="text-xs text-muted-foreground">{t('host.images')}</div>
    </div>
    <div class="text-center">
      <div class="text-lg font-semibold tnum">{docker?.volumes_count || 0}</div>
      <div class="text-xs text-muted-foreground">{t('host.volumes')}</div>
    </div>
    <div class="text-center">
      <div class="text-lg font-semibold tnum">{docker?.networks_count || 0}</div>
      <div class="text-xs text-muted-foreground">{t('host.networks')}</div>
    </div>
  </div>
  {/if}
</div>

<!-- Confirmation Dialog -->
<ConfirmDialog
  bind:open={confirmState.open}
  title={confirmState.title}
  description={confirmState.description}
  confirmLabel={confirmState.confirmLabel}
  variant={confirmState.variant}
  loading={confirmState.loading}
  onConfirm={confirmState.onConfirm}
/>
