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
  import { onMount, onDestroy } from 'svelte';
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
    ShieldCheck,
    ArrowDownToLine,
    ArrowUpFromLine,
    Eye,
  } from '@lucide/svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import { t } from '../lib/i18n.svelte.js';
  import {
    newInputRule,
    newForwardRule,
    moveRule,
    FIREWALL_TEMPLATES,
    buildTemplate,
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

  let timer = $state(null);

  async function load() {
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
  onDestroy(() => {
    if (timer) clearInterval(timer);
  });

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
    inputRules = built.input;
    forwardRules = built.forwards;
    templateId = '';
    toast.info(t('firewall.templateLoaded'));
  }
  let templateId = $state('');

  function currentFirewall() {
    return {
      input: inputRules.map((r) => ({
        id: r.id,
        name: r.name || '',
        proto: r.proto || 'tcp',
        port: Number(r.port) || 0,
        src: (r.src || '').trim(),
        action: r.action || 'allow',
      })),
      forwards: forwardRules.map((f) => ({
        id: f.id,
        name: f.name || '',
        proto: f.proto || 'tcp',
        host_port: Number(f.host_port) || 0,
        guest_ip: (f.guest_ip || '').trim(),
        guest_port: Number(f.guest_port) || 0,
      })),
    };
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
</script>

<div class="max-w-6xl mx-auto px-4 py-6 space-y-6">
  <PageHeader title={t('firewall.title')} subtitle={t('firewall.subtitle')}>
    <div class="flex items-center gap-2">
      <Button size="sm" variant="outline" onclick={preview} disabled={previewing || loading}>
        <Eye class="w-4 h-4 mr-1.5" />{previewing
          ? t('firewall.previewing')
          : t('firewall.preview')}
      </Button>
      <Button size="sm" onclick={applyNow} disabled={applying || hasPending || loading}>
        {applying ? t('firewall.applying') : t('firewall.apply')}
      </Button>
    </div>
  </PageHeader>

  {#if loading}
    <div class="flex justify-center py-20"><Spinner size="lg" /></div>
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
              title="Always open">:{port}</span
            >
          {/each}
        </div>
      </div>
    </div>

    <!-- Templates -->
    <div class="rounded-xl border border-border bg-background p-4 space-y-2">
      <p class="text-sm font-semibold">{t('firewall.templates')}</p>
      <p class="text-xs text-muted-foreground">{t('firewall.templatesDesc')}</p>
      <div class="flex items-center gap-2">
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
      </div>
      {#if templateId}
        <p class="text-xs text-muted-foreground">
          {t(FIREWALL_TEMPLATES.find((x) => x.id === templateId)?.descKey || '')}
        </p>
      {/if}
    </div>

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
              <th class="px-4 py-2 font-medium">{t('firewall.name')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.proto')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.hostPort')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.guestIp')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.guestPort')}</th>
              <th class="px-4 py-2 font-medium w-24">{t('firewall.order')}</th>
              <th class="px-4 py-2 font-medium w-10"></th>
            </tr>
          </thead>
          <tbody>
            {#if forwardRules.length === 0}
              <tr>
                <td colspan="7" class="px-4 py-6 text-center text-muted-foreground">
                  {t('firewall.noForwards')}
                </td>
              </tr>
            {/if}
            {#each forwardRules as f, i (f.id)}
              <tr class="border-b border-border/60 last:border-0">
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
                  <Input
                    class="h-8 w-36 font-mono"
                    bind:value={f.guest_ip}
                    placeholder="192.168.1.50"
                  />
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
                  <Button
                    size="icon"
                    variant="ghost"
                    onclick={() => removeForward(i)}
                    class="text-destructive hover:text-destructive"
                    aria-label={t('firewall.remove')}><Trash2 class="w-4 h-4" /></Button
                  >
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
              <th class="px-4 py-2 font-medium">{t('firewall.name')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.proto')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.port')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.source')}</th>
              <th class="px-4 py-2 font-medium">{t('firewall.action')}</th>
              <th class="px-4 py-2 font-medium w-24">{t('firewall.order')}</th>
              <th class="px-4 py-2 font-medium w-10"></th>
            </tr>
          </thead>
          <tbody>
            {#if inputRules.length === 0}
              <tr>
                <td colspan="7" class="px-4 py-6 text-center text-muted-foreground">
                  {t('firewall.noInput')}
                </td>
              </tr>
            {/if}
            {#each inputRules as r, i (r.id)}
              <tr class="border-b border-border/60 last:border-0">
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
                  <Button
                    size="icon"
                    variant="ghost"
                    onclick={() => removeInput(i)}
                    class="text-destructive hover:text-destructive"
                    aria-label={t('firewall.remove')}><Trash2 class="w-4 h-4" /></Button
                  >
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
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
