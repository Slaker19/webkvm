<script>
  // Family-aware explanation of an HA install plan: what WebKVM installs
  // itself, what the operator must do by hand (repositories, AUR builds),
  // and how ready the node is for fencing. Pure presentation: no API calls.
  import { t } from '$lib/i18n.svelte.js';
  import { Button } from '$lib/components/ui/button';
  import Alert from '$lib/components/Alert.svelte';

  let { plan, advanced = false, onCopy = () => {} } = $props();

  const K = 'nodes.clusterModule.';
  const packages = $derived(Array.isArray(plan?.packages) ? plan.packages : []);
  const steps = $derived(Array.isArray(plan?.guided_steps) ? plan.guided_steps : []);
  // Packages WebKVM may install vs the operator's (AUR, vendor, manual).
  const ours = $derived(packages.filter((p) => !p.guided));
  const oursMissing = $derived(ours.filter((p) => !p.installed));
  const yours = $derived(packages.filter((p) => p.guided && !p.installed));
  const yoursAur = $derived(yours.every((p) => p.origin === 'aur'));
  const layers = $derived(
    ['base', 'fencing', 'storage'].filter((layer) => packages.some((p) => p.layer === layer))
  );
  const watchdog = $derived(plan?.preflight?.watchdog || null);
  const virt = $derived(plan?.preflight?.virt || 'none');
  const fencingHint = $derived(plan?.preflight?.fencing_hint || '');

  const ORIGIN_KEYS = {
    official: 'originOfficial',
    aur: 'originAur',
    third_party: 'originThirdParty',
    manual: 'originManual',
  };
  const STEP_KEYS = {
    enable_ha_repo: 'stepEnableHaRepo',
    sync_db: 'stepSyncDb',
    aur_build: 'stepAurBuild',
    storage_manual: 'stepStorageManual',
  };
  const LAYER_KEYS = { base: 'layerBase', storage: 'layerStorage', fencing: 'layerFencing' };
  const LAYER_WHY = {
    base: 'layerWhyBase',
    storage: 'layerWhyStorage',
    fencing: 'layerWhyFencing',
  };
  const WATCHDOG_KEYS = {
    hardware: 'fenceWdHardware',
    i6300esb: 'fenceWdI6300esb',
    softdog: 'fenceWdSoftdog',
    none: 'fenceWdNone',
  };

  function stepTitle(id) {
    return STEP_KEYS[id] ? t(K + STEP_KEYS[id]) : t(K + 'stepGuidedOther', { id });
  }
  function originLabel(origin) {
    return ORIGIN_KEYS[origin] ? t(K + ORIGIN_KEYS[origin]) : origin;
  }
  // Comment lines explain; only real commands get a copy button.
  const isComment = (line) => String(line).trim().startsWith('#');
</script>

{#snippet chip(pkg)}
  <li
    class="inline-flex items-center gap-1.5 rounded border border-border bg-background px-2 py-1 text-[11px]"
  >
    <span class="font-mono text-foreground">{pkg.name}</span>
    <span
      class="rounded border px-1 py-px text-[10px] font-medium {pkg.origin === 'official'
        ? 'border-success/40 text-foreground'
        : 'border-warning/50 text-foreground'}"
      data-testid="origin-badge">{originLabel(pkg.origin)}</span
    >
    {#if pkg.installed}
      <span class="text-muted-foreground">{t(K + 'planInstalled')}</span>
    {/if}
  </li>
{/snippet}

<div class="space-y-3" data-testid="ha-plan-guide">
  {#if steps.length}
    <Alert variant="warning">
      <p class="font-medium" data-testid="manual-summary">
        {t(K + 'manualSummaryTitle', { count: steps.length })}
      </p>
      <p>{t(K + 'manualSummaryBody')}</p>
      <ol class="mt-1 list-decimal space-y-0.5 pl-5">
        {#each steps as step (step.id)}
          <li>{stepTitle(step.id)}</li>
        {/each}
      </ol>
    </Alert>
  {/if}

  <section class="space-y-1.5" data-testid="part-webkvm">
    <h4 class="text-xs font-semibold text-foreground">{t(K + 'partWebkvmTitle')}</h4>
    {#if ours.length === 0}
      <p class="text-[11px] text-muted-foreground">{t(K + 'partWebkvmNone')}</p>
    {:else}
      {#if oursMissing.length === 0}
        <p class="text-[11px] text-muted-foreground">{t(K + 'partWebkvmAllDone')}</p>
      {/if}
      <ul class="flex flex-wrap gap-1.5">
        {#each ours as pkg (pkg.name)}
          {@render chip(pkg)}
        {/each}
      </ul>
    {/if}
  </section>

  {#if yours.length}
    <section class="space-y-1.5" data-testid="part-you">
      <h4 class="text-xs font-semibold text-foreground">
        {yoursAur ? t(K + 'partYouTitle') : t(K + 'partYouOther')}
      </h4>
      {#if yoursAur}
        <p class="text-[11px] text-muted-foreground">{t(K + 'partYouBody')}</p>
      {/if}
      <ul class="flex flex-wrap gap-1.5">
        {#each yours as pkg (pkg.name)}
          {@render chip(pkg)}
        {/each}
      </ul>
    </section>
  {/if}

  {#each steps as step, index (step.id)}
    <section
      class="space-y-1.5 rounded border border-border bg-background/70 p-2.5"
      data-testid="guided-step-{step.id}"
    >
      <div class="flex flex-wrap items-center gap-2">
        <h4 class="text-xs font-semibold text-foreground">{index + 1}. {stepTitle(step.id)}</h4>
        <span
          class="rounded border px-1.5 py-px text-[10px] font-medium text-foreground {step.run_as ===
          'user'
            ? 'border-warning/50 bg-warning/15'
            : 'border-border bg-muted'}"
          data-testid="run-as"
          >{step.run_as === 'user' ? t(K + 'runAsUser') : t(K + 'runAsRoot')}</span
        >
      </div>
      {#if STEP_KEYS[step.id]}
        <p class="text-[11px] text-muted-foreground">{t(K + STEP_KEYS[step.id] + 'Why')}</p>
      {/if}
      <ul class="space-y-1">
        {#each step.commands || [] as command, i (i)}
          {#if isComment(command)}
            <li class="text-[11px] text-muted-foreground">
              {String(command).replace(/^#\s*/, '')}
            </li>
          {:else}
            <li class="flex flex-wrap items-center gap-2">
              <code
                class="min-w-0 flex-1 rounded bg-background px-2 py-1 font-mono text-[11px] break-all text-foreground"
                >{command}</code
              >
              <Button
                size="sm"
                variant="outline"
                class="min-h-9"
                onclick={() => onCopy(command)}
                aria-label={t(K + 'copyOneAria', { command })}
              >
                {t(K + 'copyOne')}
              </Button>
            </li>
          {/if}
        {/each}
      </ul>
    </section>
  {/each}

  {#if layers.length}
    <section class="space-y-1" data-testid="layer-why">
      <ul class="space-y-1 text-[11px] text-muted-foreground">
        {#each layers as layer (layer)}
          <li>
            <span class="font-medium text-foreground">{t(K + LAYER_KEYS[layer])}:</span>
            {t(K + LAYER_WHY[layer])}
            {#if advanced}
              <span class="font-mono"
                >({packages
                  .filter((p) => p.layer === layer)
                  .map((p) => p.name)
                  .join(', ')})</span
              >
            {/if}
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if watchdog && WATCHDOG_KEYS[watchdog.type]}
    {@const good = watchdog.type === 'hardware' || watchdog.type === 'i6300esb'}
    <Alert variant={good ? 'info' : 'warning'}>
      <p class="font-medium">{t(K + 'fenceTitle')}</p>
      <p data-testid="watchdog-state">
        {t(K + WATCHDOG_KEYS[watchdog.type], { devices: (watchdog.devices || []).join(', ') })}
      </p>
      {#if !good && fencingHint === 'vm'}
        <p>{t(K + 'fencingHintVm', { virt })}</p>
      {:else if !good && fencingHint === 'physical'}
        <p>{t(K + 'fencingHintPhysical')}</p>
      {/if}
    </Alert>
  {/if}
</div>
