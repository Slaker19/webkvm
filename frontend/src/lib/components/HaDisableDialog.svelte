<script>
  /**
   * HaDisableDialog — turn HA off for one workload with an explicit mode
   * (stop | pause | forget) and plain explanations. `forget` also needs the
   * workload key typed back, like the backend demands.
   */
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import Alert from '$lib/components/Alert.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { refreshHaWorkloads } from '$lib/stores/haWorkloads.svelte.js';
  import { refreshHaReleased } from '$lib/stores/haReleased.svelte.js';
  import {
    haErrorText,
    DISABLE_MODES,
    DEFAULT_DISABLE_MODE,
    releasesKvmStorage,
  } from '$lib/utils/haWorkloads.js';

  // ref: { kind, name, project, key, label }
  let {
    ref = null,
    defaultMode = DEFAULT_DISABLE_MODE,
    onDone = () => {},
    onCancel = () => {},
  } = $props();

  let mode = $state(DEFAULT_DISABLE_MODE);
  let typed = $state('');
  let busy = $state(false);
  let error = $state('');
  // Set when the backend could not save the VM definition first
  // (release_capture_failed): the operator may continue knowingly.
  let captureFailed = $state(false);
  let acceptUnrestorable = $state(false);
  let lastKey = '';

  $effect(() => {
    if (ref && ref.key !== lastKey) {
      lastKey = ref.key;
      mode = defaultMode;
      typed = '';
      error = '';
      captureFailed = false;
      acceptUnrestorable = false;
    }
    if (!ref) lastKey = '';
  });

  const modes = DISABLE_MODES;
  const cap = (m) => m.charAt(0).toUpperCase() + m.slice(1);

  const releases = $derived(releasesKvmStorage(ref, mode));

  async function confirm() {
    if (!ref || busy) return;
    if (mode === 'forget' && typed !== ref.key) return;
    busy = true;
    error = '';
    try {
      const opts = { mode, confirm: mode === 'forget' ? typed : '' };
      // Only after the backend refused to release without a saved definition
      // and the operator ticked the explicit acceptance.
      if (releases && captureFailed && acceptUnrestorable) opts.acceptUnrestorable = true;
      await api.disableHAWorkload(ref.kind, ref.name, ref.project, opts);
      toast.success(t('haWorkloads.disabled', { name: ref.label || ref.name }));
      refreshHaWorkloads();
      if (releases) refreshHaReleased();
      onDone(mode);
    } catch (e) {
      if (e?.code === 'release_capture_failed') captureFailed = true;
      error = haErrorText(t, e);
    } finally {
      busy = false;
    }
  }
</script>

<ConfirmDialog
  open={!!ref}
  title={t('haWorkloads.disableTitle', { name: ref?.label || ref?.name || '' })}
  description={t('haWorkloads.disableBody')}
  confirmLabel={t('haWorkloads.disableConfirm')}
  variant="destructive"
  loading={busy}
  onConfirm={confirm}
  onCancel={() => onCancel()}
>
  <fieldset class="space-y-2">
    <legend class="text-xs font-semibold text-foreground">{t('haWorkloads.disableModes')}</legend>
    {#each modes as m (m)}
      <label class="flex items-start gap-2 text-sm cursor-pointer min-h-11">
        <input
          type="radio"
          name="ha-disable-mode"
          class="mt-1 h-4 w-4"
          value={m}
          checked={mode === m}
          onchange={() => (mode = m)}
        />
        <span>
          <span class="block font-medium"
            >{t(`haWorkloads.mode${cap(m)}`)}{#if m === 'pause'}
              <span class="ml-1 text-xs font-normal text-muted-foreground"
                >({t('haWorkloads.modePauseRecommended')})</span
              >{/if}</span
          >
          <span class="block text-xs text-muted-foreground"
            >{t(`haWorkloads.mode${cap(m)}Desc`)}</span
          >
        </span>
      </label>
    {/each}
  </fieldset>
  {#if releases}
    <div data-testid="ha-kvm-release-warning">
      <Alert variant="warning">
        <strong class="block">{t('haWorkloads.kvmReleaseTitle')}</strong>
        <span class="block">{t('haWorkloads.kvmReleaseWarn')}</span>
        <span class="block">{t('haWorkloads.kvmReleaseRestore')}</span>
      </Alert>
    </div>
    {#if captureFailed}
      <div class="space-y-1" data-testid="ha-accept-unrestorable">
        <p class="text-xs text-muted-foreground">{t('haWorkloads.acceptUnrestorableHint')}</p>
        <label class="flex items-start gap-2 text-sm cursor-pointer min-h-11">
          <input type="checkbox" class="mt-1 h-4 w-4" bind:checked={acceptUnrestorable} />
          <span>{t('haWorkloads.acceptUnrestorable')}</span>
        </label>
      </div>
    {/if}
  {/if}
  {#if mode === 'forget'}
    <div class="space-y-1">
      <label for="ha-forget-confirm" class="text-xs font-semibold">
        {t('haWorkloads.forgetConfirmLabel', { key: ref?.key || '' })}
      </label>
      <input
        id="ha-forget-confirm"
        class="input w-full min-h-11 font-mono text-sm"
        autocomplete="off"
        bind:value={typed}
      />
    </div>
  {/if}
  {#if error}<Alert variant="error">{error}</Alert>{/if}
</ConfirmDialog>
