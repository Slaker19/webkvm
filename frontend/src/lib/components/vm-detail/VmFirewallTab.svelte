<script>
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import {
    buildVMTemplate,
    mergeFirewall,
    VM_TEMPLATE_PRESETS,
  } from '$lib/utils/firewallTemplates.js';

  let {
    vmId,
    fwRules = $bindable([]),
    fwForwards = $bindable([]),
  } = $props();

  let fwTplSel = $state('');
  let fwSaving = $state(false);

  function fwUid() {
    return 'fw_' + Math.random().toString(36).slice(2, 10);
  }

  function addForward() {
    fwForwards = [
      ...fwForwards,
      { id: fwUid(), proto: 'tcp', host_port: '', guest_port: '', target_ip: '' },
    ];
  }

  function removeForward(id) {
    fwForwards = fwForwards.filter((f) => f.id !== id);
  }

  function addRule() {
    fwRules = [...fwRules, { id: fwUid(), proto: 'tcp', port: '', action: 'allow' }];
  }

  function removeRule(id) {
    fwRules = fwRules.filter((r) => r.id !== id);
  }

  function applyVmTemplate() {
    if (!fwTplSel) return;
    const built = buildVMTemplate(fwTplSel);
    const merged = mergeFirewall(
      { input: fwRules, forwards: fwForwards },
      { input: built.rules, forwards: built.forwards || [] }
    );
    const n = merged.addedInput + merged.addedForwards;
    fwRules = merged.input;
    fwForwards = merged.forwards;
    fwTplSel = '';
    toast.success(t('firewall.vmTemplateApplied', { added: n }));
  }

  async function saveFirewall() {
    if (!vmId) return;
    fwSaving = true;
    try {
      const rules = fwRules
        .filter((r) => r.port)
        .map((r) => ({
          id: r.id,
          proto: r.proto,
          port: Number(r.port),
          action: r.action,
          disabled: !!r.disabled,
        }));
      const forwards = fwForwards
        .filter((f) => f.host_port && f.guest_port)
        .map((f) => ({
          id: f.id,
          proto: f.proto,
          host_port: Number(f.host_port),
          guest_port: Number(f.guest_port),
          target_ip: f.target_ip || '',
          disabled: !!f.disabled,
        }));
      const res = await api.setVMFirewall(vmId, { rules, forwards });
      fwForwards = (res.vm?.forwards || []).map((f) => ({ ...f }));
      toast.success(t('vmDetail.firewallSaved'));
    } catch (e) {
      toast.error(e.message);
    } finally {
      fwSaving = false;
    }
  }
</script>

<BlockCard bid="firewall" title={t('vmDetail.firewall')} anchor="firewall">
  <!-- Quick per-VM template (merge, never replaces) -->
  <div class="mb-4 flex flex-wrap items-center gap-2">
    <select
      class="input h-8 text-xs"
      bind:value={fwTplSel}
      aria-label={t('firewall.vmPickTemplate')}
    >
      <option value="">{t('firewall.vmPickTemplate')}</option>
      {#each VM_TEMPLATE_PRESETS as tpl (tpl.id)}
        <option value={tpl.id}>{t(tpl.labelKey)}</option>
      {/each}
    </select>
    <Button size="xs" variant="outline" disabled={!fwTplSel} onclick={applyVmTemplate}>
      {t('firewall.vmApplyTemplate')}
    </Button>
  </div>
  <!-- Port forwards -->
  <div class="mb-4">
    <h3 class="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2">
      {t('vmDetail.portForwards')}
    </h3>
    <div class="space-y-2">
      {#each fwForwards as fw (fw.id)}
        <div class="flex items-center gap-2 {fw.disabled ? 'opacity-50' : ''}">
          <span
            title={fw.disabled ? t('firewall.enableRule') : t('firewall.disableRule')}
          >
            <Switch
              size="sm"
              checked={!fw.disabled}
              onchange={() => (fw.disabled = !fw.disabled)}
            />
          </span>
          <select class="input w-24" bind:value={fw.proto}>
            <option value="tcp">tcp</option>
            <option value="udp">udp</option>
            <option value="both">both</option>
          </select>
          <Input
            type="number"
            class="w-28 tnum"
            bind:value={fw.host_port}
            min="1"
            max="65535"
            placeholder="Host port"
          />
          <span class="text-muted-foreground">→</span>
          <Input
            type="number"
            class="w-28 tnum"
            bind:value={fw.guest_port}
            min="1"
            max="65535"
            placeholder="Guest port"
          />
          {#if fw.applied}
            <span class="text-xs text-success">{t('vmDetail.fwApplied')}</span>
          {:else if fw.target_ip}
            <span class="text-xs text-muted-foreground">{fw.target_ip}</span>
          {:else}
            <span class="text-xs text-warning">{t('vmDetail.fwPending')}</span>
          {/if}
          <Button size="xs" variant="ghost" onclick={() => removeForward(fw.id)}>×</Button>
        </div>
      {/each}
      {#if fwForwards.length === 0}
        <EmptyState compact icon="shieldOff" title={t('vmDetail.noForwards')} />
      {/if}
    </div>
    <Button size="xs" variant="outline" class="mt-2" onclick={addForward}>
      {t('vmDetail.addForward')}
    </Button>
  </div>

  <!-- Inbound rules -->
  <div>
    <h3 class="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2">
      {t('vmDetail.inboundRules')}
    </h3>
    <div class="space-y-2">
      {#each fwRules as r (r.id)}
        <div class="flex items-center gap-2 {r.disabled ? 'opacity-50' : ''}">
          <span
            title={r.disabled ? t('firewall.enableRule') : t('firewall.disableRule')}
          >
            <Switch
              size="sm"
              checked={!r.disabled}
              onchange={() => (r.disabled = !r.disabled)}
            />
          </span>
          <select class="input w-24" bind:value={r.proto}>
            <option value="tcp">tcp</option>
            <option value="udp">udp</option>
            <option value="both">both</option>
          </select>
          <Input
            type="number"
            class="w-28 tnum"
            bind:value={r.port}
            min="1"
            max="65535"
            placeholder="Port"
          />
          <select class="input w-24" bind:value={r.action}>
            <option value="allow">{t('vmDetail.fwAllow')}</option>
            <option value="drop">{t('vmDetail.fwDrop')}</option>
            <option value="reject">{t('vmDetail.fwReject')}</option>
          </select>
          <Button size="xs" variant="ghost" onclick={() => removeRule(r.id)}>×</Button>
        </div>
      {/each}
      {#if fwRules.length === 0}
        <EmptyState compact icon="shieldOff" title={t('vmDetail.noRules')} />
      {/if}
    </div>
    <Button size="xs" variant="outline" class="mt-2" onclick={addRule}>
      {t('vmDetail.addRule')}
    </Button>
  </div>

  <Button size="sm" onclick={saveFirewall} disabled={fwSaving} class="mt-4">
    {t('common.save')}
  </Button>
</BlockCard>
