<script>
  import { api } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { t } from '$lib/i18n.svelte.js';

  let { vmId, open = false, onClose = () => {}, onSuccess = () => {} } = $props();

  let count = $state(2);
  let prefix = $state('clone');
  let linked = $state(true);
  let autostart = $state(false);
  let cloning = $state(false);

  let namesPreview = $derived(
    Array.from(
      { length: Math.min(Math.max(count, 1), 10) },
      (_, i) => `${prefix || 'clone'}-${i + 1}`
    )
  );

  async function handleBatchClone() {
    if (!vmId || count < 1) return;
    cloning = true;
    try {
      const res = await api.batchCloneVM(vmId, {
        count: parseInt(count, 10),
        base_name: prefix.trim(),
        prefix: prefix.trim(),
        linked: linked,
        autostart: autostart,
      });
      if (res && res.job) {
        await api.waitJob(res.job);
      }
      toast.success(t('batchClone.successToast', { count }));
      onSuccess();
      onClose();
    } catch (e) {
      toast.error(t('batchClone.errorToast', { error: e.message }));
    } finally {
      cloning = false;
    }
  }
</script>

{#if open}
  <div
    class="fixed inset-0 z-50 bg-background/80 backdrop-blur-sm flex items-center justify-center p-4"
  >
    <div
      class="bg-card border border-border rounded-xl shadow-2xl max-w-md w-full p-6 space-y-4 animate-in fade-in zoom-in-95"
    >
      <div class="flex items-start justify-between border-b border-border pb-3">
        <div>
          <h2 class="text-base font-bold text-foreground">{t('batchClone.title')}</h2>
          <p class="text-xs text-muted-foreground mt-0.5">
            {t('batchClone.desc')}
          </p>
        </div>
        <button
          onclick={onClose}
          class="text-muted-foreground hover:text-foreground text-sm font-semibold p-1"
        >
          ✕
        </button>
      </div>

      <div class="space-y-3.5">
        <div>
          <Label class="text-xs font-semibold">{t('batchClone.countLabel')}</Label>
          <Input type="number" min="1" max="10" bind:value={count} class="mt-1" />
        </div>

        <div>
          <Label class="text-xs font-semibold">{t('batchClone.prefixLabel')}</Label>
          <Input
            type="text"
            bind:value={prefix}
            placeholder={t('batchClone.prefixPlaceholder')}
            class="mt-1 font-mono text-xs"
          />
        </div>

        <div class="p-3 bg-muted/30 border border-border rounded-lg space-y-1">
          <span class="text-[10px] font-semibold uppercase text-muted-foreground"
            >{t('batchClone.previewLabel')}</span
          >
          <div class="flex flex-wrap gap-1 max-h-24 overflow-y-auto pt-1">
            {#each namesPreview as name (name)}
              <span
                class="px-2 py-0.5 rounded bg-card border border-border font-mono text-[11px] text-foreground"
              >
                {name}
              </span>
            {/each}
          </div>
        </div>

        <div class="space-y-2 pt-1">
          <label class="flex items-start gap-2 cursor-pointer">
            <input type="checkbox" bind:checked={linked} class="mt-0.5 rounded border-border" />
            <div class="text-xs">
              <span class="font-medium text-foreground">{t('batchClone.linkedClone')}</span>
              <p class="text-muted-foreground text-[11px]">
                {t('batchClone.linkedDesc')}
              </p>
            </div>
          </label>

          <label class="flex items-start gap-2 cursor-pointer">
            <input type="checkbox" bind:checked={autostart} class="mt-0.5 rounded border-border" />
            <div class="text-xs">
              <span class="font-medium text-foreground">{t('batchClone.autostartLabel')}</span>
              <p class="text-muted-foreground text-[11px]">
                {t('batchClone.autostartDesc')}
              </p>
            </div>
          </label>
        </div>
      </div>

      <div class="border-t border-border pt-4 flex items-center justify-end gap-2">
        <Button variant="outline" size="sm" onclick={onClose} disabled={cloning}
          >{t('batchClone.cancel')}</Button
        >
        <Button size="sm" onclick={handleBatchClone} disabled={cloning}>
          {cloning ? t('batchClone.cloning') : t('batchClone.createBtn', { count })}
        </Button>
      </div>
    </div>
  </div>
{/if}
