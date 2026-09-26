<script>
  /**
   * ManageGroupsDialog — VM group CRUD, extracted from VmList.svelte.
   *
   * `groups` is owned by the parent (it also drives the group filter
   * chips and the per-VM group picker outside this dialog), so it stays
   * a prop rather than being fetched here. Any mutation calls the
   * parent-supplied `onChanged` so it can re-fetch groups (and, for a
   * delete, the VM list too, in case the deleted group was the active
   * filter).
   */
  import * as Dialog from './ui/dialog';
  import { Button } from './ui/button';
  import { Input } from './ui/input';
  import Spinner from './Spinner.svelte';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from './ui/toast';
  import { api } from '$lib/stores/auth.svelte.js';

  let { open = $bindable(false), groups = [], palette = [], onChanged = () => {} } = $props();

  let newGroupName = $state('');
  let newGroupColor = $state('#7c3aed');
  let saving = $state(false);
  let error = $state('');

  // Reset the create-form fields every time the dialog opens, mirroring
  // the previous openManageGroups() behaviour in VmList.
  $effect(() => {
    if (open) {
      error = '';
      newGroupName = '';
      newGroupColor = palette[0] || '#7c3aed';
    }
  });

  async function createGroup() {
    if (!newGroupName.trim()) {
      error = t('vms.nameRequired');
      return;
    }
    saving = true;
    error = '';
    try {
      await api.createGroup({ name: newGroupName.trim(), color: newGroupColor });
      newGroupName = '';
      onChanged();
    } catch (e) {
      error = e.message;
    } finally {
      saving = false;
    }
  }

  async function updateGroupColor(g, color) {
    g.color = color;
    try {
      await api.updateGroup(g.name, { name: g.name, color });
      onChanged();
    } catch (e) {
      toast.error(e.message);
    }
  }

  async function deleteGroup(g) {
    try {
      await api.deleteGroup(g.name);
      onChanged(g.name);
      toast.success(`Group "${g.name}" removed`);
    } catch (e) {
      toast.error(e.message);
    }
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vms.manageGroupsTitle')}</Dialog.Title>
      <Dialog.Description>{t('vms.manageGroupsDesc')}</Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3">
      <div class="border border-border rounded-md bg-background p-3 space-y-2">
        <div class="flex gap-2">
          <Input bind:value={newGroupName} placeholder={t('vms.groupPlaceholder')} class="flex-1" />
          <Button onclick={createGroup} disabled={saving || !newGroupName.trim()}>
            {#if saving}<Spinner size="xs" color="text-white" />{:else}{t('common.add')}{/if}
          </Button>
        </div>
        <div class="flex items-center gap-1.5">
          <span class="text-xs text-muted-foreground mr-1">{t('vms.groupColor')}</span>
          {#each palette as c (c)}
            <button
              type="button"
              onclick={() => (newGroupColor = c)}
              class="w-5 h-5 rounded-full border-2 transition-all {newGroupColor === c
                ? 'border-foreground scale-110'
                : 'border-transparent'}"
              style="background-color: {c}"
              aria-label={c}
            ></button>
          {/each}
        </div>
        {#if error}<p class="text-xs text-destructive">{error}</p>{/if}
      </div>

      <div>
        <h3 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-2">
          {t('vms.existingGroups')}
        </h3>
        {#if groups.length === 0}
          <p class="text-sm text-muted-foreground">{t('vms.noGroups')}</p>
        {:else}
          <div class="space-y-1.5">
            {#each groups as g (g.name)}
              <div
                class="flex items-center justify-between border border-border rounded-md bg-background px-2.5 py-1.5 gap-2"
              >
                <div class="flex items-center gap-2 min-w-0 flex-1">
                  <span
                    class="inline-block w-2.5 h-2.5 rounded-full shrink-0"
                    style="background-color: {g.color}"
                  ></span>
                  <span class="text-sm font-medium truncate" title={g.name}>{g.name}</span>
                  <span class="text-xs text-muted-foreground tnum shrink-0"
                    >{g.member_count} VM{g.member_count !== 1 ? 's' : ''}</span
                  >
                </div>
                <div class="flex items-center gap-1.5 shrink-0">
                  {#each palette as c (c)}
                    <button
                      type="button"
                      onclick={() => updateGroupColor(g, c)}
                      class="w-3.5 h-3.5 rounded-full border {g.color === c
                        ? 'border-foreground'
                        : 'border-transparent'}"
                      style="background-color: {c}"
                      aria-label="color {c}"
                    ></button>
                  {/each}
                  <button
                    type="button"
                    onclick={() => deleteGroup(g)}
                    class="p-1 text-muted-foreground hover:text-destructive hover:bg-destructive/10 rounded transition-colors"
                    aria-label="Delete group"
                    title="Delete group"
                  >
                    <svg
                      class="w-3.5 h-3.5"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="2"
                      viewBox="0 0 24 24"
                      ><polyline points="3 6 5 6 21 6" /><path
                        d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"
                      /></svg
                    >
                  </button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </div>

    <Dialog.Footer>
      <Button variant="outline" onclick={() => (open = false)}>Close</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
