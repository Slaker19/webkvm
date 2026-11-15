<script>
  /**
   * RemoteListAccessAlert — the pools/networks/ISOs of a remote KVM node
   * could not be listed because this server's managed SSH access to it is
   * not set up (409 no_key / host_key_unpinned / host_key_changed /
   * ssh_auth_denied). Says the real cause and links to the Cluster access
   * card on the Nodes page, instead of "no networks" or an empty dropdown.
   */
  import Alert from '$lib/components/Alert.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let { code = '', node = '' } = $props();

  const CAUSE_KEYS = {
    no_key: 'vmCreate.remoteListNoKey',
    host_key_unpinned: 'vmCreate.remoteListHostKeyUnpinned',
    host_key_changed: 'vmCreate.remoteListHostKeyChanged',
    ssh_auth_denied: 'vmCreate.remoteListAuthDenied',
  };
  const cause = $derived(t(CAUSE_KEYS[code] || CAUSE_KEYS.no_key, { node }));
</script>

<Alert variant="warning">
  <div data-testid="remote-list-access" data-code={code}>
    <p class="font-medium">{t('vmCreate.remoteListAccessTitle', { node })}</p>
    <p class="mt-0.5">{cause}</p>
    <a href="#/nodes" class="mt-1 inline-block font-medium underline underline-offset-2">
      {t('vmCreate.remoteListAccessLink')}
    </a>
  </div>
</Alert>
