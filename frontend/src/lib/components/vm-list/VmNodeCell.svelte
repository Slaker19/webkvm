<script>
  /**
   * VmNodeCell — node label of a VM row/card with its per-node status,
   * stale / member-offline badges and the "Managed" toggle.
   */
  import { t } from '$lib/i18n.svelte.js';
  import { auth } from '$lib/stores/auth.svelte.js';
  import { Server } from '@lucide/svelte';

  let { vm, nodeLabel = '', health = 'online', onToggleManaged = () => {} } = $props();

  const managed = $derived(vm?.managed !== false);
  const memberOffline = $derived(vm?.member_online === false);
  const statusText = $derived(
    health === 'no_access' ? t('nodeSelf.statusNoAccess') : t(`remote.status.${health}`)
  );
  const HEALTH_TONE = {
    online: 'bg-success',
    degraded: 'bg-warning',
    offline: 'bg-destructive',
    stale: 'bg-warning',
    no_access: 'bg-info',
    disabled: 'bg-muted-foreground',
    unknown: 'bg-muted-foreground',
  };
</script>

<span class="inline-flex flex-wrap items-center gap-1" data-testid="vm-node-cell">
  <span
    class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-mono bg-muted text-muted-foreground border border-border/70"
    title={t('remote.list.nodeStatusAria', {
      node: nodeLabel,
      status: statusText,
    })}
  >
    <Server class="w-2.5 h-2.5" />
    <span
      class="inline-block w-1.5 h-1.5 rounded-full {HEALTH_TONE[health] || 'bg-muted-foreground'}"
      role="img"
      aria-label={t('remote.list.nodeStatusAria', {
        node: nodeLabel,
        status: statusText,
      })}
    ></span>
    {nodeLabel}
  </span>
  {#if vm?.stale}
    <span
      class="px-1.5 py-0.5 rounded text-[10px] font-medium bg-warning/15 text-warning border border-warning/30"
      title={t('remote.list.staleHint')}
      data-testid="vm-stale-badge">{t('remote.list.staleBadge')}</span
    >
  {/if}
  {#if memberOffline}
    <span
      class="px-1.5 py-0.5 rounded text-[10px] font-medium bg-destructive/10 text-destructive border border-destructive/30"
      title={t('remote.list.memberOfflineHint')}
      data-testid="vm-member-offline">{t('remote.list.memberOffline')}</span
    >
  {/if}
  {#if auth.isAdmin()}
    <button
      type="button"
      role="switch"
      aria-checked={managed}
      aria-label={t('remote.list.managedAria', { name: vm?.alias || vm?.name })}
      title={t('remote.list.managedHint')}
      onclick={(e) => {
        e.stopPropagation();
        onToggleManaged(vm, !managed);
      }}
      onkeydown={(e) => e.stopPropagation()}
      class="px-1.5 py-0.5 rounded text-[10px] font-medium border min-h-5 {managed
        ? 'border-border text-muted-foreground hover:text-foreground'
        : 'border-warning/40 bg-warning/10 text-warning'}"
      data-testid="vm-managed-toggle"
    >
      {managed ? t('remote.list.managed') : t('remote.list.unmanaged')}
    </button>
  {:else if !managed}
    <span class="px-1.5 py-0.5 rounded text-[10px] border border-warning/40 text-warning"
      >{t('remote.list.unmanaged')}</span
    >
  {/if}
</span>
