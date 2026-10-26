<script>
  import { SvelteSet } from 'svelte/reactivity';
  import * as Dialog from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import { t } from '$lib/i18n.svelte.js';

  let {
    open = $bindable(false),
    selectedCount = 0,
    groups = [],
    onAssign = () => {},
    onManageGroups = () => {},
  } = $props();

  let selectedGroupNames = new SvelteSet();

  $effect(() => {
    if (open) {
      selectedGroupNames.clear();
    }
  });

  function toggleGroup(name) {
    if (selectedGroupNames.has(name)) selectedGroupNames.delete(name);
    else selectedGroupNames.add(name);
  }

  function handleConfirm() {
    if (selectedGroupNames.size === 0) return;
    onAssign(Array.from(selectedGroupNames));
    open = false;
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content>
    <Dialog.Header>
      <Dialog.Title>{t('vms.assignGroupTitle', { n: selectedCount })}</Dialog.Title>
      <Dialog.Description>{t('vms.addToGroupTitle')}</Dialog.Description>
    </Dialog.Header>
    <div class="py-2 space-y-2">
      <span class="text-sm font-medium block">{t('vms.groupNameLabel')}</span>
      {#if groups.length === 0}
        <p class="text-sm text-muted-foreground">{t('vms.noGroupsCreateFirst')}</p>
        <Button
          size="sm"
          variant="outline"
          onclick={() => {
            open = false;
            onManageGroups();
          }}
        >
          {t('vms.manageGroups')}
        </Button>
      {:else}
        <div class="flex flex-wrap gap-1.5">
          {#each groups as g (g.name)}
            {@const active = selectedGroupNames.has(g.name)}
            <button
              onclick={() => toggleGroup(g.name)}
              type="button"
              class="text-xs px-2.5 py-1 rounded-full border transition-colors {active
                ? 'border-transparent text-white'
                : ''}"
              style={active
                ? `background-color: ${g.color}`
                : `border-color: ${g.color}40; color: ${g.color}`}
            >
              {g.name}
            </button>
          {/each}
        </div>
      {/if}
    </div>
    <Dialog.Footer>
      <Button variant="outline" onclick={() => (open = false)}>{t('common.cancel')}</Button>
      <Button disabled={selectedGroupNames.size === 0} onclick={handleConfirm}>
        {t('vms.assignBtn')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
