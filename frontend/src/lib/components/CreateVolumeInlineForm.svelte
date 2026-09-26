<script>
  /**
   * CreateVolumeInlineForm — inline (non-modal) "create a new disk
   * volume" form, extracted from Storage.svelte's VM Disks tab. Shown
   * bindable via `open`; calls `onCreated` after a successful create so
   * the parent can refresh its volume list.
   */
  import { Input } from './ui/input';
  import { Button } from './ui/button';
  import Spinner from './Spinner.svelte';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from './ui/toast';
  import { api } from '$lib/stores/auth.svelte.js';

  let { open = $bindable(false), pool = '', onCreated = () => {} } = $props();

  let name = $state('');
  let size = $state(20);
  let format = $state('qcow2');
  let creating = $state(false);

  async function createVolume() {
    if (creating || !pool || !name) return;
    creating = true;
    // Capture before the form resets, otherwise the success toast
    // interpolates the now-empty field ("Volume "" created").
    const createdName = name;
    try {
      await api.createVolume({ name: createdName, pool, capacity: size, format });
      name = '';
      size = 20;
      format = 'qcow2';
      open = false;
      toast.success(t('storage.volumeCreated', { name: createdName }));
      await onCreated();
    } catch (e) {
      toast.error(e.message);
    } finally {
      creating = false;
    }
  }
</script>

{#if open}
  <div class="bg-muted/30 rounded-lg p-3.5 border border-border space-y-2">
    <div class="text-xs font-semibold text-foreground">
      {t('storage.newVolumeInPool', { pool })}
    </div>
    <div class="flex flex-wrap gap-2 items-end">
      <Input bind:value={name} placeholder={t('storage.volumeName')} class="flex-1 min-w-[200px]" />
      <Input type="number" bind:value={size} min="1" class="w-24 tnum" />
      <span class="text-xs text-muted-foreground">GB</span>
      <select bind:value={format} class="input w-32 !text-xs">
        <option value="qcow2">qcow2</option>
        <option value="raw">raw</option>
      </select>
      <Button disabled={creating} onclick={createVolume} class="!h-8 !text-xs">
        {#if creating}<Spinner size="xs" />{:else}{t('common.create')}{/if}
      </Button>
      <Button variant="outline" class="!h-8 !text-xs" onclick={() => (open = false)}>
        {t('common.cancel')}
      </Button>
    </div>
  </div>
{/if}
