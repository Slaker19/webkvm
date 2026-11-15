<script>
  /**
   * RemoteUnsupportedNotice — shown instead of a raw 501 when an action
   * exists only on the node that owns the VM. Links to that node's own
   * WebKVM: ui_url (registry or the 501 body) first, else the URI host when
   * it is not private; otherwise it explains how to set the address.
   */
  import { t } from '$lib/i18n.svelte.js';
  import Alert from './Alert.svelte';
  import { nodeUiUrl } from '$lib/utils/remoteRoutes.js';
  import { nodeConsoleURL, nodeAddressInfo } from '$lib/utils/nodes.js';

  // uiUrl: ui_url taken from the 501 body when the registry entry is unknown
  let { node = null, nodeName = '', uiUrl = '', class: className = '' } = $props();
  const url = $derived(nodeUiUrl(node) || nodeConsoleURL({ ui_url: uiUrl }));
  const needsAddress = $derived(!url && !!node?.uri && nodeAddressInfo(node).needsHint);
  const name = $derived(node?.name || nodeName);
</script>

<Alert variant="info" title={t('remote.unsupported.title')} class={className}>
  <p data-testid="remote-unsupported">{t('remote.unsupported.body')}</p>
  {#if url}
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      class="mt-1 inline-block font-medium underline underline-offset-2"
      data-testid="remote-unsupported-link"
    >
      {t('remote.unsupported.open', { node: name })}
    </a>
  {:else if needsAddress}
    <p class="mt-1 text-xs" data-testid="remote-unsupported-nouiurl">
      {t('remote.unsupported.noAddress', { node: name })}
    </p>
  {/if}
</Alert>
