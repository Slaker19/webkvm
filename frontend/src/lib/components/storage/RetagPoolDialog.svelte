<script>
  import * as Dialog from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import Spinner from '$lib/components/Spinner.svelte';
  import { toast } from '$lib/components/ui/toast';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { purposeList } from '$lib/purpose.js';

  let {
    open = $bindable(false),
    pool = null,
    onRetagged = () => {},
    onError = () => {},
  } = $props();

  let retagPurpose = $state('disk');
  let retagCurrent = $state('');
  let saving = $state(false);

  $effect(() => {
    if (open && pool) {
      retagCurrent = purposeList(pool)[0] || 'disk';
      retagPurpose = retagCurrent;
    }
  });

  async function save() {
    if (saving || retagPurpose === retagCurrent || !pool) return;
    saving = true;
    try {
      await api.updatePool(pool.name, { purpose: retagPurpose });
      open = false;
      toast.success(t('storage.poolRetagged', { name: pool.name, purpose: retagPurpose }));
      onRetagged();
    } catch (e) {
      onError(t('storage.retagPoolTitle', { name: pool.name }), e);
    } finally {
      saving = false;
    }
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('storage.retagPoolTitle', { name: pool?.name || '' })}</Dialog.Title>
      <Dialog.Description>{t('storage.retagPoolDesc')}</Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3 my-2">
      <div>
        <label for="retag-pool-purpose" class="block text-xs font-semibold mb-1"
          >{t('storage.purpose')}</label
        >
        <select
          id="retag-pool-purpose"
          bind:value={retagPurpose}
          class="input w-full !text-xs font-medium"
        >
          {#if retagCurrent === 'container'}
            <option value="container">{t('storage.purposeLxcOption')}</option>
          {:else}
            <option value="disk">{t('storage.purposeKvmOption')}</option>
            <option value="iso">{t('storage.purposeIsoOption')}</option>
            <option value="backup">{t('storage.purposeBackupOption')}</option>
            <option value="template">{t('storage.purposeTemplateOption')}</option>
          {/if}
        </select>
      </div>
      <p class="text-[11px] text-muted-foreground">
        {t('storage.retagPoolHint')}
      </p>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (open = false)}>{t('common.cancel')}</Button>
      <Button disabled={saving || retagPurpose === retagCurrent} onclick={save}>
        {#if saving}<Spinner size="xs" class="mr-1" />{/if}
        {t('common.save')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
