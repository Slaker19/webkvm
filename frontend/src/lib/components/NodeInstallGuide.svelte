<script>
  // "Install on each node": client-side guidance only (no API). For every
  // registered node it shows how to run the Cluster/HA dependency install
  // there: open that node's own WebKVM, or run the helper on the host.
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Card } from '$lib/components/ui/card';
  import { nodeConsoleURL, nodeAddressInfo, withoutSelfDuplicates } from '$lib/utils/nodes.js';

  let { nodes = [] } = $props();
  // This server once: a remote entry pointing back at it is not another node.
  const list = $derived(withoutSelfDuplicates(nodes));

  const INSTALL_CMD = 'sudo webkvm-cli ha deps install --yes';
  // Fallback for nodes that still ship only the helper script.
  const FALLBACK_CMD = 'sudo webkvm-ha-stack --install --yes';
  const VERIFY_CMD = 'sudo webkvm-cli ha deps check';

  function nodeUrl(n) {
    // An Incus URI is the Incus API, not a WebKVM UI.
    if (n.type === 'local' || n.backend === 'incus') return null;
    return nodeConsoleURL(n);
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(INSTALL_CMD);
      toast.success(t('nodes.haCompat.copied'));
    } catch {
      toast.error(t('nodes.haCompat.copyFailed'));
    }
  }
</script>

<Card class="space-y-3 border border-border bg-card/60 p-4">
  <div class="space-y-1">
    <h2 class="text-sm font-semibold text-foreground">{t('nodes.installEachTitle')}</h2>
    <p class="text-xs text-muted-foreground">{t('nodes.installEachDesc')}</p>
  </div>
  {#if list.length === 0}
    <p class="text-xs text-muted-foreground">{t('nodes.installEachNoNodes')}</p>
  {:else}
    <ul class="divide-y divide-border text-xs">
      {#each list as n (n.id)}
        {@const url = nodeUrl(n)}
        <li class="space-y-1.5 py-3 first:pt-0 last:pb-0" data-testid="install-node-{n.id}">
          <p class="text-sm font-medium text-foreground">
            {n.name}
            {#if n.type === 'local'}
              <span class="ml-1 text-xs font-normal text-muted-foreground"
                >({t('nodeSelf.thisServer')})</span
              >
            {/if}
          </p>
          {#if n.enabled === false}
            <p class="text-muted-foreground">{t('nodes.installEachDisabledNode')}</p>
          {/if}
          {#if n.type === 'local'}
            <p class="text-muted-foreground">{t('nodes.haCompat.howToLocal')}</p>
          {:else if url}
            <p class="text-muted-foreground">{t('nodes.haCompat.howToWeb')}</p>
            <a
              href={url}
              target="_blank"
              rel="noreferrer"
              class="inline-flex min-h-9 items-center break-all text-primary underline dark:text-accent-hover"
              aria-label={t('nodes.installEachOpen', { name: n.name })}>{url}</a
            >
          {:else}
            <p class="text-muted-foreground" data-testid="install-node-nourl-{n.id}">
              {nodeAddressInfo(n).needsHint && n.backend !== 'incus'
                ? t('nodes.haCompat.howToPrivate')
                : t('nodes.haCompat.howToNoUrl')}
            </p>
          {/if}
          {#if n.type !== 'local'}
            <p class="text-muted-foreground">{t('nodes.haCompat.howToCli')}</p>
            <div class="flex flex-wrap items-center gap-2">
              <code class="rounded bg-background px-2 py-1 font-mono text-[11px]"
                >{INSTALL_CMD}</code
              >
              <Button
                size="sm"
                variant="outline"
                class="min-h-9"
                onclick={copy}
                aria-label={t('nodes.haCompat.copyCommandFor', { node: n.name })}
              >
                {t('nodes.haCompat.copyCommand')}
              </Button>
            </div>
            <p class="text-muted-foreground">
              {t('nodes.haCompat.howToCliFallback')}
              <code class="rounded bg-background px-1.5 py-0.5 font-mono text-[11px]"
                >{FALLBACK_CMD}</code
              >
            </p>
            <p class="text-muted-foreground">
              {t('nodes.haCompat.howToVerify')}
              <code class="rounded bg-background px-1.5 py-0.5 font-mono text-[11px]"
                >{VERIFY_CMD}</code
              >
            </p>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</Card>
