<script>
  import * as Dialog from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let {
    open = $bindable(false),
    templates = [],
    netOptions = [],
    poolOptions = [],
    saving = false,
    onInstantiate = () => {},
  } = $props();

  let templateId = $state('');
  let name = $state('');
  let net = $state('default');
  let pool = $state('');
  let linked = $state(false);
  let showPass = $state(false);
  let ci = $state(false);
  let ciUser = $state('');
  let ciPassword = $state('');
  let ciKey = $state('');
  let ciHostname = $state('');

  $effect(() => {
    if (open) {
      templateId = templates[0]?.id || '';
      name = '';
      net = netOptions[0]?.name || 'default';
      pool = '';
      linked = false;
      showPass = false;
      ci = false;
      ciUser = '';
      ciPassword = '';
      ciKey = '';
      ciHostname = '';
    }
  });

  function handleSubmit() {
    if (!templateId || saving) return;
    onInstantiate({
      templateId,
      name,
      net,
      pool,
      linked,
      ci,
      ciUser,
      ciPassword,
      ciKey,
      ciHostname,
    });
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('vms.instantiateTitle')}</Dialog.Title>
      <Dialog.Description>{t('vms.instantiateDesc')}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      {#if templates.length === 0}
        <div class="text-center py-6">
          <p class="text-sm text-muted-foreground mb-3">{t('vms.noTemplates')}</p>
          <ol
            class="text-sm text-muted-foreground text-left space-y-1.5 list-decimal list-inside mx-auto w-fit"
          >
            <li>{t('vms.templateStep1')}</li>
            <li>{t('vms.templateStep2')}</li>
            <li>{t('vms.templateStep3')}</li>
          </ol>
        </div>
      {:else}
        <div class="space-y-1.5">
          <Label for="inst-template-comp">{t('vms.templateLabel')}</Label>
          <select id="inst-template-comp" bind:value={templateId} class="input w-full">
            {#each templates as tmpl (tmpl.id)}
              <option value={tmpl.id}
                >{tmpl.alias || tmpl.name} ({tmpl.vcpus} vCPU · {tmpl.ram_mb} MB)</option
              >
            {/each}
          </select>
        </div>
        <div class="space-y-1.5">
          <Label for="inst-name-comp">{t('vms.newVmName')}</Label>
          <Input id="inst-name-comp" bind:value={name} placeholder="my-vm" />
        </div>
        <div class="space-y-1.5">
          <Label for="inst-net-comp">{t('vmDetail.networkLabel')}</Label>
          <select id="inst-net-comp" bind:value={net} class="input w-full">
            {#each netOptions as n (n.name)}
              <option value={n.name}>{n.name} {n.bridge ? `(${n.bridge})` : ''}</option>
            {/each}
          </select>
        </div>

        {#if poolOptions.length > 1}
          <div class="space-y-1.5">
            <Label for="inst-pool-comp">{t('vms.poolLabel')}</Label>
            <select id="inst-pool-comp" bind:value={pool} class="input w-full">
              <option value="">{t('vms.poolDefault')}</option>
              {#each poolOptions as p (p.name)}
                <option value={p.name}>{p.name}</option>
              {/each}
            </select>
            {#if linked && pool}
              <p class="text-[11px] text-warning flex items-start gap-1">
                <Icon name="alertTriangle" size={12} class="mt-0.5 shrink-0" />
                <span>{t('vms.linkedCrossPoolWarn')}</span>
              </p>
            {/if}
          </div>
        {/if}

        <div class="space-y-1.5">
          <Label>{t('vms.cloneModeLabel')}</Label>
          <div class="grid grid-cols-2 gap-2">
            <button
              type="button"
              onclick={() => (linked = false)}
              class="flex flex-col items-start gap-0.5 p-2.5 rounded-lg border text-left transition-colors {!linked
                ? 'border-accent bg-accent/10'
                : 'border-border hover:bg-muted/30'}"
            >
              <span class="flex items-center gap-1.5 font-semibold text-xs">
                <Icon
                  name="copy"
                  size={13}
                  class={!linked ? 'text-accent' : 'text-muted-foreground'}
                />
                {t('vms.cloneFull')}
              </span>
              <span class="text-[10px] text-muted-foreground">{t('vms.cloneFullDesc')}</span>
            </button>
            <button
              type="button"
              onclick={() => (linked = true)}
              class="flex flex-col items-start gap-0.5 p-2.5 rounded-lg border text-left transition-colors {linked
                ? 'border-accent bg-accent/10'
                : 'border-border hover:bg-muted/30'}"
            >
              <span class="flex items-center gap-1.5 font-semibold text-xs">
                <Icon
                  name="zap"
                  size={13}
                  class={linked ? 'text-accent' : 'text-muted-foreground'}
                />
                {t('vms.cloneLinked')}
              </span>
              <span class="text-[10px] text-muted-foreground">{t('vms.cloneLinkedDesc')}</span>
            </button>
          </div>
        </div>

        <div class="pt-2 border-t border-border">
          <label class="flex items-center gap-2 cursor-pointer select-none">
            <input type="checkbox" bind:checked={ci} class="w-4 h-4 rounded border-border" />
            <span class="text-sm font-medium">{t('vmCreate.cloudInitEnable')}</span>
          </label>
        </div>

        {#if ci}
          <div class="space-y-2.5 pl-2 border-l-2 border-accent/40">
            <div>
              <Label for="inst-ci-user-comp">{t('vmCreate.cloudInitUser')}</Label>
              <Input id="inst-ci-user-comp" bind:value={ciUser} placeholder="webkvm" />
            </div>
            <div>
              <Label for="inst-ci-pass-comp">{t('passwordModal.password')}</Label>
              <div class="relative">
                <Input
                  id="inst-ci-pass-comp"
                  type={showPass ? 'text' : 'password'}
                  bind:value={ciPassword}
                  placeholder="••••••••"
                  class="pr-10"
                />
                <button
                  type="button"
                  onclick={() => (showPass = !showPass)}
                  class="absolute right-2 top-1/2 -translate-y-1/2 p-1 text-muted-foreground hover:text-foreground"
                >
                  <Icon name={showPass ? 'eyeOff' : 'eye'} size={14} />
                </button>
              </div>
            </div>
            <div>
              <Label for="inst-ci-key-comp">{t('vmCreate.cloudInitSSHKey')}</Label>
              <Input id="inst-ci-key-comp" bind:value={ciKey} placeholder="ssh-ed25519 AAAA..." />
            </div>
          </div>
        {/if}
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (open = false)} disabled={saving}
        >{t('common.cancel')}</Button
      >
      {#if templates.length > 0}
        <Button onclick={handleSubmit} disabled={saving || !templateId}>
          {#if saving}<Spinner size="sm" color="text-white" />{:else}{t('vms.instantiate')}{/if}
        </Button>
      {/if}
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
