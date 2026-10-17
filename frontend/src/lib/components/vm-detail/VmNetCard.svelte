<script>
  import { t } from '$lib/i18n.svelte.js';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import { networkLabelFor } from '$lib/utils/networkLabel.js';

  let {
    vm = {},
    networks = [],
    vmNetworks = [],
    isContainerVm = false,
    actionLoading = '',
    toggleLinkState,
    removeNet,
    aNetNetwork = $bindable('default'),
    aNetModel = $bindable('virtio'),
    showAddNet = $bindable(false),
  } = $props();
</script>

<BlockCard bid="net" title={t('vmDetail.networkInterfaces')}>
  <div class="flex items-center justify-between mb-3">
    <Button
      size="xs"
      variant="outline"
      onclick={() => {
        aNetNetwork = vmNetworks[0]?.name || 'default';
        aNetModel = isContainerVm ? 'incus' : 'virtio';
        showAddNet = true;
      }}>+ {t('vmDetail.addInterface')}</Button
    >
  </div>
  {#if !vm?.networks || vm.networks.length === 0}
    <EmptyState compact icon="network" title={t('vmDetail.noNetworkInterfaces')} />
  {:else}
    <div class="space-y-1.5">
      {#each vm.networks as iface (iface.mac || iface.target || iface.name)}
        <div
          class="flex items-center justify-between px-3 py-2 rounded-md border border-border bg-background"
        >
          <div class="flex items-center gap-2 flex-wrap min-w-0">
            <span class="text-xs font-mono text-muted-foreground">{iface.mac}</span>
            <span
              class="text-xs px-1.5 py-0.5 rounded border border-border bg-muted text-muted-foreground"
              >{iface.model}</span
            >
            <span class="text-sm">{networkLabelFor(iface.network, networks)}</span>
            {#if iface.link_state === 'down'}
              <span
                class="text-xs px-1.5 py-0.5 rounded bg-destructive/10 border border-destructive/20 text-destructive font-mono"
              >
                Cable desconectado
              </span>
            {:else}
              <span
                class="text-xs px-1.5 py-0.5 rounded bg-emerald-500/10 border border-emerald-500/20 text-emerald-500 font-mono"
              >
                Enlace activo
              </span>
            {/if}
            {#if vm?.state === 'running' && iface.ips?.length}
              <span
                class="text-xs px-1.5 py-0.5 rounded bg-accent/10 border border-accent/20 text-accent font-mono"
                >{iface.ips.join(', ')}</span
              >
            {/if}
          </div>
          <div class="flex items-center gap-1.5 shrink-0">
            {#if vm?.state === 'running'}
              <button
                onclick={() => toggleLinkState?.(iface)}
                disabled={actionLoading === 'netlink'}
                class="text-xs px-2 py-1 rounded border border-border hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
                title="Conectar o desconectar cable virtual en caliente"
              >
                <span class="inline-flex items-center gap-1">
                  <Icon name={iface.link_state === 'down' ? 'link' : 'unlink'} size={12} />
                  <span>{iface.link_state === 'down' ? 'Conectar cable' : 'Desconectar cable'}</span>
                </span>
              </button>
            {/if}
            <button
              onclick={() => removeNet?.(iface.mac)}
              class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10 disabled:opacity-50 disabled:cursor-not-allowed"
              >{t('vmDetail.remove')}</button
            >
          </div>
        </div>
      {/each}
    </div>
  {/if}
</BlockCard>
