<script>
  /**
   * CloudInitPreviewDialog — read-only preview of the rendered
   * cloud-init user-data, extracted from VmCreate.svelte.
   *
   * Purely presentational: VmCreate still owns the fetch (it needs the
   * whole in-progress form state to build the preview request) and
   * only passes the result in via `content`/`loading`.
   */
  import * as Dialog from './ui/dialog';
  import { Button } from './ui/button';
  import Icon from './Icon.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let { open = $bindable(false), content = '', loading = false } = $props();
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-2xl max-h-[85vh] flex flex-col">
    <Dialog.Header>
      <Dialog.Title class="flex items-center gap-2">
        <Icon name="code" size={18} class="text-accent" />
        {t('snippets.previewTitle')}
      </Dialog.Title>
    </Dialog.Header>

    <div class="flex-1 min-w-0 overflow-y-auto">
      {#if loading}
        <div class="text-center py-12 text-sm text-muted-foreground">
          {t('common.evaluating')}
        </div>
      {:else}
        <pre
          class="p-4 rounded-lg bg-muted/70 border border-border text-xs font-mono whitespace-pre overflow-x-auto text-foreground leading-relaxed">{content}</pre>
      {/if}
    </div>

    <Dialog.Footer
      class="!bg-transparent !border-0 !p-0 !mx-0 !mb-0 items-center justify-between flex-row"
    >
      <span class="text-xs text-muted-foreground">
        {t('snippets.previewFootnote')}
      </span>
      <Button size="sm" onclick={() => (open = false)}>
        {t('common.close')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
