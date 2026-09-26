<script>
  /**
   * WizardNav — sticky Back / Next / Create footer for a stepped form.
   *
   * Next stays enabled even when the current step is invalid: clicking
   * it reveals the errors instead of leaving a dead button with no
   * explanation. Only the final Create is truly disabled, because by
   * then every error is already on screen.
   */
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import { Button } from '$lib/components/ui/button';
  import { t } from '$lib/i18n.svelte.js';

  let {
    isFirstStep = false,
    isLastStep = false,
    loading = false,
    blocked = false,
    canSubmit = false,
    errorText = '',
    onprev = null,
    oncancel = null,
    // Final button text; defaults to "Create VM". VmCreate passes
    // "Create container" for LXC so the label matches what is made.
    submitLabel = '',
  } = $props();
</script>

<div
  class="sticky bottom-0 z-20 bg-background/95 backdrop-blur-md border border-border rounded-xl p-3 shadow-lg flex flex-col-reverse sm:flex-row sm:items-center gap-3"
>
  <div class="flex items-center gap-2 shrink-0">
    {#if isFirstStep}
      <Button type="button" variant="ghost" onclick={oncancel} class="cursor-pointer">
        {t('common.cancel')}
      </Button>
    {:else}
      <Button type="button" variant="outline" onclick={onprev} class="cursor-pointer">
        <Icon name="chevronLeft" size={14} class="mr-1" />
        {t('common.back')}
      </Button>
    {/if}
  </div>

  <div class="flex-1 min-w-0 text-xs sm:text-center">
    {#if blocked && errorText}
      <p
        class="flex items-center gap-1.5 text-destructive font-medium sm:justify-center"
        role="alert"
      >
        <Icon name="alertTriangle" size={14} class="shrink-0" />
        <span class="truncate">{errorText}</span>
      </p>
    {/if}
  </div>

  <div class="flex items-center gap-2 shrink-0 sm:ml-auto">
    {#if isLastStep}
      <Button
        type="submit"
        disabled={loading || !canSubmit}
        class="cursor-pointer font-medium min-w-[130px]"
      >
        {#if loading}
          <Spinner size="sm" color="text-white" />
          {t('vmCreate.creating')}
        {:else}
          <Icon name="check" size={14} class="mr-1" />
          {submitLabel || t('vms.create')}
        {/if}
      </Button>
    {:else}
      <Button type="submit" class="cursor-pointer font-medium min-w-[110px]">
        {t('common.next')}
        <Icon name="chevronRight" size={14} class="ml-1" />
      </Button>
    {/if}
  </div>
</div>
