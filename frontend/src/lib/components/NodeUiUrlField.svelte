<script>
  /**
   * NodeUiUrlField — optional "Address on your network" of a node (ui_url).
   * The node URI is what WebKVM itself uses (often a private cluster NIC);
   * this is where the operator's PC reaches the node's own WebKVM. A one-click
   * suggestion is offered only when the URI host is not private.
   */
  import { t } from '$lib/i18n.svelte.js';
  import { Input } from '$lib/components/ui/input';
  import { Button } from '$lib/components/ui/button';
  import { validateUIUrl, suggestUIUrl, uriHost, isPrivateHost } from '$lib/utils/nodes.js';

  let { value = $bindable(''), nodeUri = '', id = 'node-ui-url' } = $props();

  const check = $derived(validateUIUrl(value));
  const suggestion = $derived(suggestUIUrl(nodeUri));
  const host = $derived(uriHost(nodeUri));
  const showPrivate = $derived(!!host && isPrivateHost(host));
  const showSuggest = $derived(!!suggestion && !value.trim());
</script>

<div class="space-y-1.5" data-testid="ui-url-field">
  <label class="text-xs text-muted-foreground" for={id}>{t('nodes.uiUrl')}</label>
  <Input
    {id}
    bind:value
    inputmode="url"
    autocomplete="off"
    spellcheck="false"
    placeholder={t('nodes.uiUrlPlaceholder')}
    aria-invalid={!check.ok}
    aria-describedby="{id}-help {id}-error"
    class="w-full min-h-11 font-mono text-xs"
  />
  {#if !check.ok}
    <p id="{id}-error" class="text-xs text-destructive" role="alert" data-testid="ui-url-error">
      {t(`nodes.uiUrlErrors.${check.error}`)}
    </p>
  {:else}
    <span id="{id}-error" class="sr-only"></span>
  {/if}
  <p id="{id}-help" class="text-xs text-muted-foreground">{t('nodes.uiUrlHelp')}</p>
  {#if showSuggest}
    <Button
      type="button"
      size="sm"
      variant="outline"
      class="min-h-11 font-mono text-xs"
      data-testid="ui-url-suggest"
      onclick={() => (value = suggestion)}
    >
      {t('nodes.uiUrlSuggest', { url: suggestion })}
    </Button>
  {:else if showPrivate}
    <p class="text-xs text-foreground" data-testid="ui-url-private">
      {t('nodes.uiUrlPrivateExplain', { host })}
    </p>
  {/if}
</div>
