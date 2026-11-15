<script>
  /**
   * CopyCommands — a read-only block of shell commands with a copy button.
   * The commands are shown exactly as they will be copied; nothing is run.
   */
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';

  let { commands = [], title = '', copyAria = '' } = $props();

  const text = $derived((commands || []).join('\n'));

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(t('haWorkloads.copied'));
    } catch {
      toast.error(t('haWorkloads.copyFailed'));
    }
  }
</script>

{#if commands?.length}
  <div class="space-y-1" data-testid="copy-commands">
    {#if title}<div class="text-xs font-semibold">{title}</div>{/if}
    <pre
      class="rounded-md border border-border bg-muted p-2 text-xs font-mono text-foreground whitespace-pre-wrap break-all"
      data-testid="copy-commands-text">{text}</pre>
    <Button
      size="sm"
      variant="outline"
      class="min-h-11"
      aria-label={copyAria || t('haWorkloads.copyCommands')}
      onclick={copy}
    >
      {t('haWorkloads.copyCommands')}
    </Button>
  </div>
{/if}
