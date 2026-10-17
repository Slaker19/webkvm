<script>
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import StorageBreakdownBar from '$lib/components/StorageBreakdownBar.svelte';
  import { Button } from '$lib/components/ui/button';
  import { formatBytes } from '$lib/utils/format.js';
  import { auth, api } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { t } from '$lib/i18n.svelte.js';
  import { hasPurpose, isIsoOnly, purposeLabel, isBuiltinPool } from '$lib/purpose.js';
  import CreatePoolDialog from './CreatePoolDialog.svelte';
  import RetagPoolDialog from './RetagPoolDialog.svelte';

  let {
    pools = [],
    breakdownByPool = {},
    hostPoolsDir = '/opt/webkvm/pools',
    showCreatePool = $bindable(false),
    onOpenPool = () => {},
    onRefresh = () => {},
    askConfirm = () => {},
    showStorageErr = () => {},
  } = $props();

  let showRetagPool = $state(false);
  let selectedRetagPool = $state(null);

  function openRetag(p) {
    selectedRetagPool = p;
    showRetagPool = true;
  }

  function deletePool(name) {
    askConfirm({
      title: t('storage.deletePoolTitle', { name }),
      description: t('storage.deletePoolDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        try {
          await api.deletePool(name);
          toast.success(t('storage.poolDeleted', { name }));
          onRefresh();
        } catch (e) {
          showStorageErr(t('storage.deletePoolTitle', { name }), e);
        }
      },
    });
  }
</script>

<div class="p-4 sm:p-5 space-y-4">
  <div class="flex items-center justify-between">
    <div>
      <h3 class="text-sm font-bold text-foreground">{t('storage.availablePoolsTitle')}</h3>
      <p class="text-xs text-muted-foreground">
        {t('storage.poolsHint')}
      </p>
    </div>
  </div>

  {#if pools.length === 0}
    <div class="py-12">
      <EmptyState icon="disk" title={t('storage.noPools')} description={t('storage.noPoolsHint')}>
        {#snippet action()}
          {#if auth.isAdmin()}
            <Button size="sm" onclick={() => (showCreatePool = true)}>
              {t('storage.createPool')}
            </Button>
          {/if}
        {/snippet}
      </EmptyState>
    </div>
  {:else}
    <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3.5">
      {#each pools as p (p.name)}
        {@const isISO = isIsoOnly(p)}
        {@const isLXC = hasPurpose(p, 'container') || (p.type && p.type.includes('incus'))}
        <div
          class="relative flex flex-col justify-between gap-3 p-4 rounded-xl border border-border bg-background hover:border-accent/40 hover:shadow-sm transition-all duration-150 cursor-pointer group"
          onclick={() => onOpenPool(p)}
          onkeydown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault();
              onOpenPool(p);
            }
          }}
          role="button"
          tabindex="0"
        >
          <div>
            <div class="flex items-start justify-between gap-2 mb-2">
              <div class="flex items-center gap-2.5 min-w-0">
                <div
                  class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0 {isISO
                    ? 'bg-warning/10 text-warning'
                    : isLXC
                      ? 'bg-success/10 text-success'
                      : 'bg-accent/10 text-accent'}"
                >
                  <Icon name={isISO ? 'disc' : isLXC ? 'cpu' : 'hardDrive'} size={17} />
                </div>
                <div class="min-w-0">
                  <h4
                    class="text-sm font-bold text-foreground group-hover:text-accent transition-colors truncate"
                  >
                    {p.name}
                  </h4>
                  <span class="text-[10px] font-mono text-muted-foreground truncate block"
                    >{p.path || '-'}</span
                  >
                </div>
              </div>

              <span
                class="text-[9px] font-mono px-2 py-0.5 rounded-full font-bold uppercase shrink-0 {isISO
                  ? 'bg-warning/10 text-warning border border-warning/20'
                  : isLXC
                    ? 'bg-success/10 text-success border border-success/20'
                    : 'bg-accent/10 text-accent border border-accent/20'}"
              >
                {purposeLabel(p)}
              </span>
            </div>

            {#if p.capacity > 0}
              {@const bd = breakdownByPool[p.name]}
              <div class="space-y-1 my-3">
                {#if bd}
                  <StorageBreakdownBar breakdown={bd} />
                {:else}
                  <ProgressBar
                    value={(p.allocated / p.capacity) * 100}
                    size="sm"
                    variant={isISO ? 'warning' : isLXC ? 'success' : 'default'}
                  />
                {/if}
                <div
                  class="flex items-center justify-between text-[11px] text-muted-foreground font-mono"
                >
                  <span>{formatBytes(p.allocated)} {t('storage.usedSpace')}</span>
                  <span>{formatBytes(p.capacity)}</span>
                </div>
              </div>
            {/if}
          </div>

          <div
            class="flex items-center justify-between pt-2.5 border-t border-border text-xs text-muted-foreground"
          >
            <span class="text-accent group-hover:underline font-medium"
              >{t('storage.openDisks')}</span
            >
            {#if auth.role === 'admin' && p.name !== 'default' && !isBuiltinPool(p.name)}
              <div class="flex items-center gap-0.5">
                <button
                  onclick={(e) => {
                    e.stopPropagation();
                    openRetag(p);
                  }}
                  class="p-1 rounded text-muted-foreground hover:text-accent hover:bg-accent/10"
                  aria-label={t('storage.retagPoolAria')}
                  title={t('storage.retagPoolAria')}
                >
                  <Icon name="tag" size={14} />
                </button>
                <button
                  onclick={(e) => {
                    e.stopPropagation();
                    deletePool(p.name);
                  }}
                  class="p-1 rounded text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                  aria-label={t('storage.deletePoolAria')}
                  title={t('storage.deletePoolAria')}
                >
                  <Icon name="trash" size={14} />
                </button>
              </div>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<CreatePoolDialog
  bind:open={showCreatePool}
  {hostPoolsDir}
  onCreated={(createdPool) => {
    onRefresh();
    onOpenPool(createdPool);
  }}
/>

<RetagPoolDialog
  bind:open={showRetagPool}
  pool={selectedRetagPool}
  onRetagged={onRefresh}
  onError={showStorageErr}
/>
