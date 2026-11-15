<script>
  /**
   * RemoteNodeBanner — on a VM that lives on another node: which node, its
   * state, and a shortcut to Cluster access when SSH/host-key errors occur.
   */
  import { t } from '$lib/i18n.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import Alert from './Alert.svelte';
  import { Button } from '$lib/components/ui/button';
  import {
    needsClusterAccess,
    remoteErrorText,
    nodeHealth,
    nodeUiUrl,
  } from '$lib/utils/remoteRoutes.js';

  // vm: the loaded VM; node: registry entry (or null); error: last ApiError
  let { vm, node = null, error = null } = $props();

  const openUrl = $derived(nodeUiUrl(node));
  const isMember = $derived(!vm?.node_id);
  const health = $derived(nodeHealth(node, vm ? [vm] : []));
  const code = $derived(error?.code || '');
  const accessIssue = $derived(needsClusterAccess(code));
  const variant = $derived(
    accessIssue || health === 'offline' ? 'warning' : health === 'stale' ? 'warning' : 'info'
  );
</script>

<Alert {variant} class="mb-4">
  <div class="flex flex-wrap items-center gap-x-3 gap-y-1" data-testid="remote-node-banner">
    <span class="font-medium">
      {isMember
        ? t('remote.banner.livesMember', { node: vm?.node })
        : t('remote.banner.lives', { node: vm?.node })}
    </span>
    {#if !isMember}
      <span class="rounded border border-current/30 px-1.5 text-xs" data-testid="remote-node-state">
        {t(`remote.status.${health}`)}
      </span>
    {/if}
    {#if !isMember && openUrl}
      <a
        href={openUrl}
        target="_blank"
        rel="noopener noreferrer"
        class="text-xs font-medium underline underline-offset-2"
        data-testid="remote-node-open"
      >
        {t('remote.banner.open', { node: vm?.node })}
      </a>
    {/if}
  </div>
  {#if vm?.stale}
    <p class="mt-1 text-xs">{t('remote.banner.stale')}</p>
  {/if}
  {#if code}
    <p class="mt-1 text-xs" data-testid="remote-node-error">{remoteErrorText(t, error)}</p>
  {/if}
  {#if accessIssue}
    <Button
      size="sm"
      variant="outline"
      class="mt-2"
      onclick={() => navigate('/nodes', { query: { focus: 'access' } })}
    >
      {t('remote.banner.access')}
    </Button>
  {/if}
</Alert>
