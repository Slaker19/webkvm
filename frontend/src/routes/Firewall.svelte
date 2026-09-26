<script>
  /**
   * Firewall — host-level nftables editor (V13-C-01 / V13-C-02).
   *
   * Two clearly separated sections:
   *   - Forward (VM traffic): DNAT port forwards host:port → guest IP:port.
   *   - Input (traffic to this host): filter rules on the host's own
   *     input chain (policy accept; the management ports are always
   *     open — see the locked "Protected" strip below).
   *
   * Safety net (Safe Apply):
   *   1. "Apply" validates + applies the whole ruleset ATOMICALLY
   *      (single nft -f transaction) and starts a 30s confirm window.
   *   2. "Confirm" within the window makes the rules permanent.
   *   3. If the operator does not confirm — e.g. they cut their own
   *      SSH/UI connection — the backend auto-rolls-back to the
   *      previous ruleset when the timer expires. The bar below always
   *      shows a manual Rollback button too.
   */
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import * as Dialog from '$lib/components/ui/dialog';
  import {
    Lock,
    Plus,
    Trash2,
    ChevronUp,
    ChevronDown,
    ChevronRight,
    ShieldCheck,
    ArrowDownToLine,
    ArrowUpFromLine,
    Eye,
    Download,
    Upload,
    Copy,
    Power,
  } from '@lucide/svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import TableSkeleton from '$lib/components/TableSkeleton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { navigate } from '$lib/router.svelte.js';
  import { t } from '../lib/i18n.svelte.js';
  import {
    newInputRule,
    newForwardRule,
    moveRule,
    FIREWALL_TEMPLATES,
    buildTemplate,
    mergeFirewall,
    findConflicts,
    duplicateItem,
    buildVMTemplate,
    VM_TEMPLATE_PRESETS,
  } from '$lib/utils/firewallTemplates.js';

  let loading = $state(true);
  let inputRules = $state([]);
  let forwardRules = $state([]);
  let protectedPorts = $state([]);

  // Safe Apply pending state (from the server; survives page reloads).
  let pendingDeadline = $state(0);
  let now = $state(Date.now());

  let previewRuleset = $state(null); // dialog content
  let previewing = $state(false);
  let applying = $state(false);
  let importing = $state(false);
  let fileInputEl = $state(null);

  async function load() {
    try {
      const list = await api.listVMs().catch(() => []);
      vms = Array.isArray(list) ? list : [];
    } catch {
      vms = [];
    }
    try {
      const r = await api.getHostFirewall();
      inputRules = (r.firewall?.input || []).map((x) => ({ ...x }));
      forwardRules = (r.firewall?.forwards || []).map((x) => ({ ...x }));
      protectedPorts = r.protected_ports || [];
      if (r.pending) {
        pendingDeadline = r.pending.deadline * 1000;
      } else {
        pendingDeadline = 0;
      }
    } catch (err) {
      toast.error(err.message);
    } finally {
      loading = false;
    }
  }

  // Countdown + expiry refresh. When the deadline passes with no
  // confirm, the backend has already auto-rolled-back; reload to show
  // the restored (previous) rules.
  $effect(() => {
    if (pendingDeadline === 0) return;
    const id = setInterval(async () => {
      now = Date.now();
      if (now >= pendingDeadline) {
        clearInterval(id);
        pendingDeadline = 0;
        toast.info(t('firewall.applyRolledBack'));
        await load();
      }
    }, 500);
    return () => clearInterval(id);
  });

  onMount(load);

  const remaining = $derived(
    pendingDeadline ? Math.max(0, Math.ceil((pendingDeadline - now) / 1000)) : 0
  );
  const hasPending = $derived(pendingDeadline > 0);

  // --- Forward rules ---
  function addForward() {
    forwardRules = [...forwardRules, newForwardRule()];
  }
  function removeForward(i) {
    forwardRules = forwardRules.filter((_, idx) => idx !== i);
  }
  function moveForward(i, dir) {
    forwardRules = moveRule(forwardRules, i, dir);
  }

  // --- Input rules ---
  function addInput() {
    inputRules = [...inputRules, newInputRule()];
  }
  function removeInput(i) {
    inputRules = inputRules.filter((_, idx) => idx !== i);
  }
  function moveInput(i, dir) {
    inputRules = moveRule(inputRules, i, dir);
  }

  function applyTemplate() {
    const sel = templateId;
    if (!sel) return;
    const built = buildTemplate(sel);
    if (templateMode === 'merge') {
      const merged = mergeFirewall({ input: inputRules, forwards: forwardRules }, built);
      inputRules = merged.input;
      forwardRules = merged.forwards;
      toast.info(t('firewall.templateMerged', { added: merged.addedInput + merged.addedForwards }));
    } else {
      inputRules = built.input;
      forwardRules = built.forwards;
      toast.info(t('firewall.templateLoaded'));
    }
    templateId = '';
  }
  let templateId = $state('');
  // 'merge' appends template rows skipping exact duplicates (default:
  // never loses work); 'replace' swaps the whole editor content.
  let templateMode = $state('merge');

  // Conflict warnings: nft evaluates top-down, so duplicates are noise
  // and a later row with a different action on the same proto+port+src
  // can never match (shadowed). Pure helper, recomputed on every edit.
  const conflicts = $derived(findConflicts(inputRules, forwardRules));

  // VMs for the guest-IP picker and the collapsible per-VM section.
  // Loaded best-effort: the editor works fine without it.
  let vms = $state([]);
  const vmsWithIp = $derived(
    vms
      .filter((v) => v && v.ip)
      .map((v) => ({ id: v.id, label: `${v.alias || v.name} (${v.ip})`, ip: v.ip }))
  );

  // Per-VM collapsible section state: expanded rows, cached firewalls
  // and the selected quick template per VM.
  let expandedVm = $state({});
  let vmFwCache = $state({});
  let vmTplSel = $state({});

  async function loadVmFw(vmId) {
    vmFwCache = { ...vmFwCache, [vmId]: { ...(vmFwCache[vmId] || {}), loading: true } };
    try {
      const fw = await api.getVMFirewall(vmId);
      vmFwCache = {
        ...vmFwCache,
        [vmId]: { loading: false, rules: fw.rules || [], forwards: fw.forwards || [] },
      };
    } catch (err) {
      vmFwCache = { ...vmFwCache, [vmId]: { loading: false, error: err.message } };
    }
  }

  function toggleVm(vmId) {
    const open = !expandedVm[vmId];
    expandedVm = { ...expandedVm, [vmId]: open };
    if (open && !vmFwCache[vmId]) loadVmFw(vmId);
  }

  async function applyVmTemplate(vmId) {
    const sel = vmTplSel[vmId];
    if (!sel) return;
    const cached = vmFwCache[vmId];
    if (!cached || cached.loading) return;
    const built = buildVMTemplate(sel);
    // mergeFirewall keys work for both shapes (VM rules lack src;
    // VM forwards carry target_ip instead of guest_ip).
    const merged = mergeFirewall(
      { input: cached.rules || [], forwards: cached.forwards || [] },
      { input: built.rules, forwards: built.forwards || [] }
    );
    const rules = merged.input;
    const forwards = merged.forwards;
    try {
      const res = await api.setVMFirewall(vmId, { rules, forwards });
      vmFwCache = {
        ...vmFwCache,
        [vmId]: {
          loading: false,
          rules: res.vm?.rules || rules,
          forwards: res.vm?.forwards || forwards,
        },
      };
      vmTplSel = { ...vmTplSel, [vmId]: '' };
      toast.success(t('firewall.vmTemplateApplied', { added: built.rules.length }));
    } catch (err) {
      toast.error(err.message);
    }
  }

  function currentFirewall() {
    return {
      input: inputRules.map((r) => ({
        id: r.id,
        name: r.name || '',
        proto: r.proto || 'tcp',
        port: Number(r.port) || 0,
        src: (r.src || '').trim(),
        action: r.action || 'allow',
        disabled: !!r.disabled,
      })),
      forwards: forwardRules.map((f) => ({
        id: f.id,
        name: f.name || '',
        proto: f.proto || 'tcp',
        host_port: Number(f.host_port) || 0,
        guest_ip: (f.guest_ip || '').trim(),
        guest_port: Number(f.guest_port) || 0,
        disabled: !!f.disabled,
      })),
    };
  }

  function duplicateInput(i) {
    const d = duplicateItem(inputRules[i]);
    inputRules = [...inputRules.slice(0, i + 1), d, ...inputRules.slice(i + 1)];
  }

  function duplicateForward(i) {
    const d = duplicateItem(forwardRules[i]);
    forwardRules = [...forwardRules.slice(0, i + 1), d, ...forwardRules.slice(i + 1)];
  }

  async function preview() {
    previewing = true;
    try {
      const r = await api.previewHostFirewall(currentFirewall());
      previewRuleset = r.ruleset;
    } catch (err) {
      toast.error(err.message);
    } finally {
      previewing = false;
    }
  }

  async function applyNow() {
    if (hasPending) {
      toast.error(t('firewall.alreadyPending'));
      return;
    }
    if (applying) return;
    applying = true;
    try {
      const r = await api.applyHostFirewall(currentFirewall());
      pendingDeadline = r.deadline * 1000;
      now = Date.now();
      toast.info(t('firewall.applyStaged', { secs: r.window_secs || 30 }));
    } catch (err) {
      toast.error(err.message);
    } finally {
      applying = false;
    }
  }

  async function confirmNow() {
    try {
      await api.confirmHostFirewall();
      pendingDeadline = 0;
      await load();
      toast.success(t('firewall.confirmed'));
    } catch (err) {
      toast.error(err.message);
    }
  }

  async function rollbackNow() {
    try {
      const r = await api.rollbackHostFirewall();
      pendingDeadline = 0;
      await load();
      if (r.status === 'no_pending') return;
      toast.info(t('firewall.rolledBack'));
    } catch (err) {
      toast.error(err.message);
    }
  }

  // V13-D-03: export/import. Import runs the EXACT hardened chain as the
  // editor (anti-lockout validation + nft -c + Safe Apply 30s) via the
  // dedicated backend endpoint — the imported file is never trusted
  // blindly and always lands in the confirm/rollback window.
  async function exportFirewall() {
    try {
      const fw = await api.exportHostFirewall();
      const blob = new Blob([JSON.stringify(fw, null, 2)], {
        type: 'application/json',
      });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = 'webkvm-firewall.json';
      a.click();
      URL.revokeObjectURL(url);
      toast.success(t('firewall.exported'));
    } catch (err) {
      toast.error(err.message);
    }
  }

  function pickImportFile() {
    fileInputEl?.click();
  }

  async function onImportFile(e) {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file) return;
    importing = true;
    try {
      const text = await file.text();
      const fw = JSON.parse(text);
      const r = await api.importHostFirewall(fw);
      pendingDeadline = r.deadline * 1000;
      now = Date.now();
      await load();
      toast.info(t('firewall.importStaged', { secs: r.window_secs || 30 }));
    } catch (err) {
      toast.error(err.message);
    } finally {
      importing = false;
    }
  }
</script>

<div class="max-w-[1700px] w-full mx-auto p-3 sm:p-5 space-y-5">
  <PageHeader title={t('firewall.title')} subtitle={t('firewall.subtitle')}>
    {#snippet actions()}
      <div class="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="outline" onclick={preview} disabled={previewing || loading}>
          <Eye class="w-4 h-4 mr-1.5" />{previewing
            ? t('firewall.previewing')
            : t('firewall.preview')}
        </Button>
        <Button size="sm" variant="outline" onclick={exportFirewall} disabled={loading}>
          <Download class="w-4 h-4 mr-1.5" />{t('firewall.export')}
        </Button>
        <Button
          size="sm"
          variant="outline"
          onclick={pickImportFile}
          disabled={importing || loading}
        >
          <Upload class="w-4 h-4 mr-1.5" />{importing
            ? t('firewall.importing')
            : t('firewall.import')}
        </Button>
        <input
          bind:this={fileInputEl}
          type="file"
          accept="application/json,.json"
          class="hidden"
          onchange={onImportFile}
        />
        <Button size="sm" onclick={applyNow} disabled={applying || hasPending || loading}>
          {applying ? t('firewall.applying') : t('firewall.apply')}
        </Button>
      </div>
    {/snippet}
  </PageHeader>

  {#if loading}
    <div class="space-y-4" role="presentation" aria-hidden="true">
      <div class="rounded-xl border border-border bg-background overflow-hidden">
        <TableSkeleton
          columns={[{}, { width: '2fr' }, { width: '1fr' }, { width: '1fr' }, { width: '80px' }]}
          rows={4}
        />
      </div>
      <div class="rounded-xl border border-border bg-background overflow-hidden">
        <TableSkeleton
          columns={[{}, { width: '2fr' }, { width: '1fr' }, { width: '80px' }]}
          rows={4}
        />
      </div>
    </div>
  {:else}
    <!-- Safe Apply bar -->
    {#if hasPending}
      <div
        class="rounded-xl border border-warning/40 bg-warning/10 p-4 flex flex-wrap items-center gap-3"
        role="alert"
      >
        <div class="flex-1 min-w-[220px]">
          <p class="text-sm font-semibold text-warning-foreground">{t('firewall.pendingTitle')}</p>
          <p class="text-xs text-muted-foreground mt-0.5">
            {t('firewall.pendingDesc')}
            {t('firewall.countdown', { secs: remaining })}
          </p>
        </div>
        <div class="flex gap-2">
          <Button size="sm" variant="outline" onclick={rollbackNow}>{t('firewall.rollback')}</Button
          >
          <Button size="sm" onclick={confirmNow}>{t('firewall.confirm')}</Button>
        </div>
      </div>
    {/if}

    <!-- Protected ports (anti-lockout, non-deletable) -->
    <div
      class="rounded-xl border border-border bg-background p-4 flex items-start gap-3"
      data-testid="protected-ports"
    >
      <span class="mt-0.5 shrink-0 rounded-lg bg-accent/15 text-accent p-1.5">
        <Lock class="w-4 h-4" />
      </span>
      <div>
        <p class="text-sm font-semibold flex items-center gap-2">
          {t('firewall.protectedTitle')}
          <span class="text-[10px] uppercase tracking-wide text-success font-bold">●</span>
        </p>
        <p class="text-xs text-muted-foreground mt-0.5 max-w-2xl">{t('firewall.protectedDesc')}</p>
        <div class="flex flex-wrap gap-1.5 mt-2">
          {#each protectedPorts as port (port)}
            <span
              class="px-2 py-0.5 rounded-md bg-muted/50 text-xs font-mono border border-border"
              title={t('firewall.protectedPortTooltip')}>:{port}</span
            >
          {/each}
        </div>
      </div>
    </div>

    <!-- Templates -->
    <div class="rounded-xl border border-border bg-background p-4 space-y-2">
      <p class="text-sm font-semibold">{t('firewall.templates')}</p>
      <p class="text-xs text-muted-foreground">{t('firewall.templatesDesc')}</p>
      <div class="flex flex-wrap items-center gap-2">
        <select
          bind:value={templateId}
          class="w-full sm:w-72 h-9 rounded-lg border border-border bg-background px-2 text-sm"
        >
          <option value="">{t('firewall.pickTemplate')}</option>
          {#each FIREWALL_TEMPLATES as tpl (tpl.id)}
            <option value={tpl.id}>{t(tpl.labelKey)}</option>
          {/each}
        </select>
        <Button size="sm" variant="outline" onclick={applyTemplate} disabled={!templateId}>
          {t('firewall.loadTemplate')}
        </Button>
        <div
          class="flex items-center gap-1 text-xs"
          role="radiogroup"
          aria-label={t('firewall.templateMode')}
        >
          <button
            type="button"
            role="radio"
            aria-checked={templateMode === 'merge'}
            onclick={() => (templateMode = 'merge')}
            class="px-2.5 py-1.5 rounded-lg border font-medium transition-colors {templateMode ===
            'merge'
              ? 'bg-primary/15 border-primary/40 text-primary'
              : 'border-border text-muted-foreground hover:text-foreground'}"
          >
            {t('firewall.templateModeMerge')}
          </button>
          <button
            type="button"
            role="radio"
            aria-checked={templateMode === 'replace'}
            onclick={() => (templateMode = 'replace')}
            class="px-2.5 py-1.5 rounded-lg border font-medium transition-colors {templateMode ===
            'replace'
              ? 'bg-destructive/10 border-destructive/40 text-destructive'
              : 'border-border text-muted-foreground hover:text-foreground'}"
          >
            {t('firewall.templateModeReplace')}
          </button>
        </div>
      </div>
      {#if templateId}
        <p class="text-xs text-muted-foreground">
          {t(FIREWALL_TEMPLATES.find((x) => x.id === templateId)?.descKey || '')}
        </p>
      {/if}
    </div>

    <!-- Conflict warnings (duplicates + shadowed rows) -->
    {#if conflicts.length > 0}
      <div class="rounded-xl border border-warning/40 bg-warning/10 p-4 space-y-1" role="alert">
        <p class="text-sm font-semibold text-warning-foreground">{t('firewall.conflictsTitle')}</p>
        {#each conflicts as c (`${c.scope}-${c.index}`)}
          <p class="text-xs text-muted-foreground">
            {#if c.kind === 'duplicate'}
              {t('firewall.conflictDuplicate', {
                scope: c.scope === 'input' ? t('firewall.inputTitle') : t('firewall.forwardTitle'),
                row: c.index + 1,
                proto: c.proto,
                port: c.port,
              })}
            {:else}
              {t('firewall.conflictShadowed', {
                scope: c.scope === 'input' ? t('firewall.inputTitle') : t('firewall.forwardTitle'),
                row: c.index + 1,
                proto: c.proto,
                port: c.port,
                by: c.byIndex + 1,
              })}
            {/if}
          </p>
        {/each}
      </div>
    {/if}

    <!-- Forward section (VM traffic) -->
    <div class="rounded-xl border border-border bg-background overflow-hidden">
      <div class="p-4 pb-3 flex items-start justify-between gap-3 border-b border-border">
        <div class="flex items-start gap-3">
          <span class="mt-0.5 shrink-0 rounded-lg bg-primary/15 text-primary p-1.5">
            <ArrowDownToLine class="w-4 h-4" />
          </span>
          <div>
            <p class="text-sm font-semibold">{t('firewall.forwardTitle')}</p>
            <p class="text-xs text-muted-foreground mt-0.5 max-w-2xl">
              {t('firewall.forwardDesc')}
            </p>
          </div>
        </div>
        <Button size="sm" variant="outline" onclick={addForward}>
          <Plus class="w-4 h-4 mr-1" />{t('firewall.addForward')}
        </Button>
      </div>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr
              class="text-left text-xs uppercase tracking-wide text-muted-foreground border-b border-border"
            >
              <th class="px-2 py-2 font-medium w-10"></th>
              <th class="px-4 py-2 font-medium">{t('firewall.name')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.proto')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.hostPort')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.guestIp')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.guestPort')}</th>
              <th class="px-4 py-2 font-medium w-24">{t('firewall.order')}</th>
              <th class="px-4 py-2 font-medium w-20"></th>
            </tr>
          </thead>
          <tbody>
            {#if forwardRules.length === 0}
              <tr>
                <td colspan="8">
                  <EmptyState compact icon="shieldOff" title={t('firewall.noForwards')} />
                </td>
              </tr>
            {/if}
            {#each forwardRules as f, i (f.id)}
              <tr class="border-b border-border/60 last:border-0 {f.disabled ? 'opacity-50' : ''}">
                <td class="px-2 py-1.5">
                  <button
                    type="button"
                    onclick={() => (f.disabled = !f.disabled)}
                    title={f.disabled ? t('firewall.enableRule') : t('firewall.disableRule')}
                    aria-label={f.disabled ? t('firewall.enableRule') : t('firewall.disableRule')}
                    aria-pressed={!f.disabled}
                    class="p-1.5 rounded-lg transition-colors {f.disabled
                      ? 'text-muted-foreground hover:text-foreground'
                      : 'text-success hover:text-success'}"
                  >
                    <Power class="w-4 h-4" />
                  </button>
                </td>
                <td class="px-4 py-1.5">
                  <Input class="h-8 min-w-28" bind:value={f.name} />
                </td>
                <td class="px-4 py-1.5">
                  <select
                    class="h-8 rounded-lg border border-border bg-background px-1 text-sm"
                    bind:value={f.proto}
                  >
                    <option value="tcp">TCP</option>
                    <option value="udp">UDP</option>
                    <option value="both">TCP+UDP</option>
                  </select>
                </td>
                <td class="px-4 py-1.5">
                  <Input
                    class="h-8 w-24"
                    type="number"
                    min="1"
                    max="65535"
                    bind:value={f.host_port}
                  />
                </td>
                <td class="px-4 py-1.5">
                  <div class="flex items-center gap-1">
                    <Input
                      class="h-8 w-36 font-mono"
                      bind:value={f.guest_ip}
                      placeholder="192.168.1.50"
                    />
                    {#if vmsWithIp.length > 0}
                      <select
                        class="h-8 max-w-28 rounded-lg border border-border bg-background px-1 text-xs"
                        title={t('firewall.pickVmIp')}
                        aria-label={t('firewall.pickVmIp')}
                        onchange={(e) => {
                          if (e.currentTarget.value) f.guest_ip = e.currentTarget.value;
                          e.currentTarget.value = '';
                        }}
                      >
                        <option value="">VM…</option>
                        {#each vmsWithIp as v (v.id)}
                          <option value={v.ip}>{v.label}</option>
                        {/each}
                      </select>
                    {/if}
                  </div>
                </td>
                <td class="px-4 py-1.5">
                  <Input
                    class="h-8 w-24"
                    type="number"
                    min="1"
                    max="65535"
                    bind:value={f.guest_port}
                  />
                </td>
                <td class="px-4 py-1.5">
                  <div class="flex gap-1">
                    <Button
                      size="icon"
                      variant="ghost"
                      disabled={i === 0}
                      onclick={() => moveForward(i, -1)}
                      aria-label={t('firewall.moveUp')}><ChevronUp class="w-4 h-4" /></Button
                    >
                    <Button
                      size="icon"
                      variant="ghost"
                      disabled={i === forwardRules.length - 1}
                      onclick={() => moveForward(i, 1)}
                      aria-label={t('firewall.moveDown')}><ChevronDown class="w-4 h-4" /></Button
                    >
                  </div>
                </td>
                <td class="px-4 py-1.5">
                  <div class="flex gap-1">
                    <Button
                      size="icon"
                      variant="ghost"
                      onclick={() => duplicateForward(i)}
                      aria-label={t('firewall.duplicate')}><Copy class="w-4 h-4" /></Button
                    >
                    <Button
                      size="icon"
                      variant="ghost"
                      onclick={() => removeForward(i)}
                      class="text-destructive hover:text-destructive"
                      aria-label={t('firewall.remove')}><Trash2 class="w-4 h-4" /></Button
                    >
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>

    <!-- Input section (traffic to this host) -->
    <div class="rounded-xl border border-border bg-background overflow-hidden">
      <div class="p-4 pb-3 flex items-start justify-between gap-3 border-b border-border">
        <div class="flex items-start gap-3">
          <span class="mt-0.5 shrink-0 rounded-lg bg-primary/15 text-primary p-1.5">
            <ArrowUpFromLine class="w-4 h-4" />
          </span>
          <div>
            <p class="text-sm font-semibold">{t('firewall.inputTitle')}</p>
            <p class="text-xs text-muted-foreground mt-0.5 max-w-2xl">{t('firewall.inputDesc')}</p>
          </div>
        </div>
        <Button size="sm" variant="outline" onclick={addInput}>
          <Plus class="w-4 h-4 mr-1" />{t('firewall.addInput')}
        </Button>
      </div>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr
              class="text-left text-xs uppercase tracking-wide text-muted-foreground border-b border-border"
            >
              <th class="px-2 py-2 font-medium w-10"></th>
              <th class="px-4 py-2 font-medium">{t('firewall.name')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.proto')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.port')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.source')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.action')}</th>
              <th class="px-4 py-2 font-medium w-24">{t('firewall.order')}</th>
              <th class="px-4 py-2 font-medium w-20"></th>
            </tr>
          </thead>
          <tbody>
            {#if inputRules.length === 0}
              <tr>
                <td colspan="8">
                  <EmptyState compact icon="shieldOff" title={t('firewall.noInput')} />
                </td>
              </tr>
            {/if}
            {#each inputRules as r, i (r.id)}
              <tr class="border-b border-border/60 last:border-0 {r.disabled ? 'opacity-50' : ''}">
                <td class="px-2 py-1.5">
                  <button
                    type="button"
                    onclick={() => (r.disabled = !r.disabled)}
                    title={r.disabled ? t('firewall.enableRule') : t('firewall.disableRule')}
                    aria-label={r.disabled ? t('firewall.enableRule') : t('firewall.disableRule')}
                    aria-pressed={!r.disabled}
                    class="p-1.5 rounded-lg transition-colors {r.disabled
                      ? 'text-muted-foreground hover:text-foreground'
                      : 'text-success hover:text-success'}"
                  >
                    <Power class="w-4 h-4" />
                  </button>
                </td>
                <td class="px-4 py-1.5">
                  <Input class="h-8 min-w-28" bind:value={r.name} />
                </td>
                <td class="px-4 py-1.5">
                  <select
                    class="h-8 rounded-lg border border-border bg-background px-1 text-sm"
                    bind:value={r.proto}
                  >
                    <option value="tcp">TCP</option>
                    <option value="udp">UDP</option>
                    <option value="both">TCP+UDP</option>
                  </select>
                </td>
                <td class="px-4 py-1.5">
                  <Input class="h-8 w-24" type="number" min="1" max="65535" bind:value={r.port} />
                </td>
                <td class="px-4 py-1.5">
                  <Input
                    class="h-8 w-36 font-mono"
                    bind:value={r.src}
                    placeholder={t('firewall.sourcePlaceholder')}
                  />
                </td>
                <td class="px-4 py-1.5">
                  <select
                    class="h-8 rounded-lg border border-border bg-background px-1 text-sm"
                    bind:value={r.action}
                  >
                    <option value="allow">Allow</option>
                    <option value="drop">Drop</option>
                  </select>
                </td>
                <td class="px-4 py-1.5">
                  <div class="flex gap-1">
                    <Button
                      size="icon"
                      variant="ghost"
                      disabled={i === 0}
                      onclick={() => moveInput(i, -1)}
                      aria-label={t('firewall.moveUp')}><ChevronUp class="w-4 h-4" /></Button
                    >
                    <Button
                      size="icon"
                      variant="ghost"
                      disabled={i === inputRules.length - 1}
                      onclick={() => moveInput(i, 1)}
                      aria-label={t('firewall.moveDown')}><ChevronDown class="w-4 h-4" /></Button
                    >
                  </div>
                </td>
                <td class="px-4 py-1.5">
                  <div class="flex gap-1">
                    <Button
                      size="icon"
                      variant="ghost"
                      onclick={() => duplicateInput(i)}
                      aria-label={t('firewall.duplicate')}><Copy class="w-4 h-4" /></Button
                    >
                    <Button
                      size="icon"
                      variant="ghost"
                      onclick={() => removeInput(i)}
                      class="text-destructive hover:text-destructive"
                      aria-label={t('firewall.remove')}><Trash2 class="w-4 h-4" /></Button
                    >
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>

    <!-- Per-VM compact management (collapsible rows) -->
    <div class="rounded-xl border border-border bg-background overflow-hidden">
      <div class="p-4 pb-3 border-b border-border">
        <p class="text-sm font-semibold">{t('firewall.vmSectionTitle')}</p>
        <p class="text-xs text-muted-foreground mt-0.5 max-w-2xl">{t('firewall.vmSectionDesc')}</p>
      </div>
      {#if vms.length === 0}
        <EmptyState compact icon="server" title={t('firewall.vmNoVms')} />
      {:else}
        <div class="divide-y divide-border/60">
          {#each vms as vm (vm.id)}
            {@const cached = vmFwCache[vm.id]}
            {@const open = !!expandedVm[vm.id]}
            {@const ruleCount =
              cached && !cached.loading && !cached.error ? (cached.rules || []).length : null}
            {@const fwdCount =
              cached && !cached.loading && !cached.error ? (cached.forwards || []).length : null}
            <div>
              <button
                type="button"
                onclick={() => toggleVm(vm.id)}
                aria-expanded={open}
                class="w-full flex items-center gap-3 px-4 py-2.5 text-left text-sm hover:bg-muted/30 transition-colors"
              >
                <ChevronRight
                  class="w-4 h-4 shrink-0 text-muted-foreground transition-transform {open
                    ? 'rotate-90'
                    : ''}"
                />
                <span
                  class="w-2 h-2 rounded-full shrink-0 {vm.state === 'running'
                    ? 'bg-success'
                    : 'bg-muted-foreground'}"
                  title={vm.state}
                ></span>
                <span class="font-medium truncate">{vm.alias || vm.name}</span>
                {#if vm.ip}
                  <span class="font-mono text-xs text-muted-foreground">{vm.ip}</span>
                {/if}
                <span class="ml-auto text-xs text-muted-foreground">
                  {#if cached?.loading}
                    {t('common.loading')}
                  {:else if cached?.error}
                    <span class="text-destructive">{cached.error}</span>
                  {:else if ruleCount !== null}
                    {t('firewall.vmCounts', { rules: ruleCount, forwards: fwdCount })}
                  {/if}
                </span>
              </button>
              {#if open}
                <div class="px-4 pb-4 pt-1 space-y-3 bg-muted/20">
                  {#if !cached || cached.loading}
                    <div class="flex justify-center py-4"><Spinner size="md" /></div>
                  {:else if cached.error}
                    <p class="text-xs text-destructive py-2">{cached.error}</p>
                  {:else}
                    {#if (cached.rules || []).length === 0 && (cached.forwards || []).length === 0}
                      <p class="text-xs text-muted-foreground py-1">{t('firewall.vmNoFirewall')}</p>
                    {:else}
                      <ul class="text-xs font-mono space-y-1">
                        {#each cached.rules || [] as r (r.id)}
                          <li
                            class="flex items-center gap-2 {r.disabled
                              ? 'opacity-50 line-through'
                              : ''}"
                          >
                            <span
                              class="px-1.5 py-0.5 rounded text-[10px] font-semibold {r.action ===
                              'drop'
                                ? 'bg-destructive/15 text-destructive'
                                : 'bg-success/15 text-success'}"
                            >
                              {r.action}
                            </span>
                            <span>{r.proto} :{r.port}</span>
                          </li>
                        {/each}
                        {#each cached.forwards || [] as f (f.id)}
                          <li
                            class="flex items-center gap-2 {f.disabled
                              ? 'opacity-50 line-through'
                              : ''}"
                          >
                            <span
                              class="px-1.5 py-0.5 rounded text-[10px] font-semibold bg-accent/15 text-accent"
                            >
                              fwd
                            </span>
                            <span>
                              {f.proto} :{f.host_port} → {f.target_ip || '?'}:{f.guest_port}
                              {#if f.applied}
                                <span class="text-success">· {t('vmDetail.fwApplied')}</span>
                              {:else}
                                <span class="text-warning">· {t('vmDetail.fwPending')}</span>
                              {/if}
                            </span>
                          </li>
                        {/each}
                      </ul>
                    {/if}
                    <div class="flex flex-wrap items-center gap-2">
                      <select
                        class="h-8 rounded-lg border border-border bg-background px-2 text-xs"
                        value={vmTplSel[vm.id] || ''}
                        onchange={(e) =>
                          (vmTplSel = { ...vmTplSel, [vm.id]: e.currentTarget.value })}
                        aria-label={t('firewall.vmPickTemplate')}
                      >
                        <option value="">{t('firewall.vmPickTemplate')}</option>
                        {#each VM_TEMPLATE_PRESETS as tpl (tpl.id)}
                          <option value={tpl.id}>{t(tpl.labelKey)}</option>
                        {/each}
                      </select>
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={!vmTplSel[vm.id]}
                        onclick={() => applyVmTemplate(vm.id)}
                      >
                        {t('firewall.vmApplyTemplate')}
                      </Button>
                      <Button size="sm" variant="ghost" onclick={() => navigate('/vms/' + vm.id)}>
                        {t('firewall.vmOpenDetail')}
                      </Button>
                    </div>
                  {/if}
                </div>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <p class="text-xs text-muted-foreground flex items-center gap-1.5">
      <ShieldCheck class="w-3.5 h-3.5" />
      {t('firewall.footnote')}
    </p>
  {/if}
</div>

<!-- Preview dialog -->
<Dialog.Root open={!!previewRuleset} onOpenChange={(v) => !v && (previewRuleset = null)}>
  <Dialog.Content class="sm:max-w-2xl [&>*]:min-w-0">
    <Dialog.Header>
      <Dialog.Title>{t('firewall.previewTitle')}</Dialog.Title>
      <Dialog.Description>{t('firewall.previewDesc')}</Dialog.Description>
    </Dialog.Header>
    <pre
      class="max-h-96 overflow-auto rounded-lg bg-muted/40 p-3 text-xs font-mono leading-relaxed"><code
        >{previewRuleset}</code
      ></pre>
    <Dialog.Footer>
      <Button class="w-full" onclick={() => (previewRuleset = null)}>{t('common.close')}</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
