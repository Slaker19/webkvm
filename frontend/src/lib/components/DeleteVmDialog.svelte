<script>
  /**
   * DeleteVmDialog — the VM deletion flow, extracted from VmDetail.svelte.
   *
   * Two steps:
   *   1. Confirm + optional "also delete its disks" checkbox.
   *   2. Only when disks are included: a red, type-the-VM-name
   *      irreversible confirmation (disk deletion cannot be undone).
   *
   * Containers (isContainer): Incus always removes the root disk with
   * the instance, so there is no disk checkbox — the text says so and
   * the flow always goes through the step-2 name confirmation. The
   * delete is still sent WITHOUT ?disks=true: the libvirt pool sweep
   * that flag triggers is name-based and has nothing to do with Incus.
   *
   * Fully self-contained: owns its own busy/step state and calls the
   * delete API itself, only notifying the parent on success via
   * onDeleted so VmDetail can navigate away. `open` is bindable so the
   * parent can trigger step 1 by setting it to true (see
   * VmDetail.svelte's deleteVM action handler).
   */
  import * as Dialog from './ui/dialog';
  import { Button } from './ui/button';
  import { Input } from './ui/input';
  import { Loader2 } from '@lucide/svelte';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from './ui/toast';
  import { api } from '$lib/stores/auth.svelte.js';

  let {
    open = $bindable(false),
    vmId,
    vmName,
    vmDisks = [],
    active = false,
    isContainer = false,
    // onDeleteStart fires right before the API call so the parent can
    // stop polling a VM that is about to disappear; onDeleteFailed
    // lets it resume if the delete errors.
    onDeleteStart = () => {},
    onDeleteFailed = () => {},
    onDeleted = () => {},
  } = $props();

  let showStep2 = $state(false);
  let withDisks = $state(false);
  let confirmText = $state('');
  let busy = $state(false);

  const desc = $derived(
    isContainer
      ? active
        ? t('vmDetail.deleteCtDescRunning')
        : t('vmDetail.deleteCtDescOff')
      : (active ? t('vmDetail.deleteVmDescRunning') : t('vmDetail.deleteVmDescOff')) +
          ' ' +
          (withDisks ? t('vmDetail.deleteDisksRemovedNote') : t('vmDetail.deleteDisksKeptNote'))
  );

  // Split a "{name}" translation around the placeholder so the VM name
  // renders as a real <strong> element without {@html}: t() escapes
  // vars, so the old `<strong>` markup showed up literally, and {@html}
  // with a user-chosen name would be an injection vector anyway.
  const NAME_MARK = '\u0000';
  function nameParts(key) {
    const [before = '', after = ''] = t(key, { name: NAME_MARK }).split(NAME_MARK);
    return { before, after };
  }
  const irreversibleParts = $derived(nameParts('vmDetail.irreversibleDeleteDesc'));
  const typeNameParts = $derived(nameParts('vmDetail.typeNameToConfirm'));
  const nameOk = $derived(confirmText.trim().toLowerCase() === (vmName || '').toLowerCase());

  // Reset transient state whenever the dialog is (re)opened, so a
  // previous run's checkbox/typed-name state never leaks into the next.
  $effect(() => {
    if (open) {
      withDisks = false;
      confirmText = '';
      showStep2 = false;
    }
  });

  async function performDelete(deleteDisks) {
    busy = true;
    onDeleteStart();
    try {
      const res = await api.deleteVM(vmId, deleteDisks);
      open = false;
      showStep2 = false;
      toast.success(t('vmDetail.vmDeleted', { name: vmName }));
      // A disk the sweep refused to remove — normally because linked
      // clones are still backed by it — would otherwise vanish from
      // view as unexplained used space, with the VM already gone.
      const kept = res?.disks_kept || [];
      if (kept.length > 0) {
        toast.warning(t('vmDetail.disksKept', { disks: kept.join(', ') }), {
          duration: 15000,
        });
      }
      onDeleted(deleteDisks);
    } catch (e) {
      onDeleteFailed();
      toast.error(e.message);
    } finally {
      busy = false;
    }
  }

  function onStep1Confirm() {
    if (!withDisks && !isContainer) {
      performDelete(false);
      return;
    }
    open = false;
    showStep2 = true;
  }
</script>

<!-- Delete flow — step 1: confirm + optional disk cleanup -->
<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title
        >{isContainer ? t('vmDetail.deleteCtTitle') : t('vmDetail.deleteVmTitle')}</Dialog.Title
      >
      <Dialog.Description>{desc}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3" hidden={isContainer}>
      <label class="flex items-start gap-2 text-sm cursor-pointer select-none">
        <input
          type="checkbox"
          bind:checked={withDisks}
          class="w-4 h-4 mt-0.5 rounded border-border"
        />
        <span>
          {t('vmDetail.alsoDeleteDisks')}
          {#if vmDisks.length > 0}
            <span class="block text-xs text-muted-foreground mt-0.5 break-all">
              ({vmDisks.map((d) => d.name || d.source).join(', ')})
            </span>
          {/if}
        </span>
      </label>
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (open = false)} disabled={busy}>
        {t('common.cancel')}
      </Button>
      <Button variant="destructive" onclick={onStep1Confirm} disabled={busy}>
        {#if busy}<Loader2 class="h-4 w-4 animate-spin" />{:else}{t('common.delete')}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Delete flow — step 2: irreversible, type the VM name -->
<Dialog.Root
  bind:open={showStep2}
  onOpenChange={(o) => {
    if (!o) confirmText = '';
  }}
>
  <Dialog.Content class="sm:max-w-md border-destructive/40">
    <Dialog.Header>
      <Dialog.Title class="text-destructive">{t('vmDetail.irreversibleDeleteTitle')}</Dialog.Title>
      <Dialog.Description>
        {irreversibleParts.before}<strong>{vmName || ''}</strong>{irreversibleParts.after}
      </Dialog.Description>
    </Dialog.Header>
    <div class="space-y-2">
      <p class="text-sm">
        {typeNameParts.before}<strong>{vmName || ''}</strong>{typeNameParts.after}
      </p>
      <Input bind:value={confirmText} placeholder={vmName} autocomplete="off" />
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showStep2 = false)} disabled={busy}>
        {t('common.cancel')}
      </Button>
      <Button
        variant="destructive"
        disabled={!nameOk || busy}
        onclick={() => performDelete(!isContainer)}
      >
        {#if busy}<Loader2 class="h-4 w-4 animate-spin" />{:else}{isContainer
            ? t('vmDetail.deleteCtConfirm')
            : t('vmDetail.deleteVmAndDisks')}{/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
