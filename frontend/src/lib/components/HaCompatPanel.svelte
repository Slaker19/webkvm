<script>
  // Cluster compatibility: what each member runs, with plain-language
  // warnings (mixed distro / Incus / kernel, members that could not be read).
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { Button } from '$lib/components/ui/button';
  import { Card } from '$lib/components/ui/card';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';

  const K = 'nodes.haCompat.';
  let data = $state(null);
  let loading = $state(true);
  let failed = $state(false);

  const members = $derived(Array.isArray(data?.members) ? data.members : []);
  const warnings = $derived(Array.isArray(data?.warnings) ? data.warnings : []);
  const mixed = $derived(warnings.some((w) => w.startsWith('mixed_')));

  const WARN = {
    mixed_distro: ['warnMixedDistro', 'todoMixedDistro'],
    mixed_distro_version: ['warnMixedDistroVersion', 'todoMixedDistroVersion'],
    mixed_incus_version: ['warnMixedIncus', 'todoMixedIncus'],
    mixed_kernel_major: ['warnMixedKernel', 'todoMixedKernel'],
    members_unknown: ['warnMembersUnknown', 'todoMembersUnknown'],
  };
  const STATUS = {
    online: 'statusOnline',
    offline: 'statusOffline',
    evacuated: 'statusEvacuated',
  };

  const statusText = (s) => t(K + (STATUS[String(s || '').toLowerCase()] || 'statusUnknown'));
  const kernelMajor = (k) =>
    String(k || '')
      .split('.')
      .slice(0, 2)
      .join('.');

  // Per-member badge: only differences against the first readable member.
  function badges(member) {
    if (member.error) return [{ key: 'compatUnknown', warn: true }];
    const ref = members.find((m) => !m.error) || member;
    const out = [];
    if (member.os_name && member.os_name !== ref.os_name)
      out.push({ key: 'compatDistro', warn: true });
    else if (member.os_version && member.os_version !== ref.os_version)
      out.push({ key: 'compatVersion', warn: true });
    if (member.incus_version && member.incus_version !== ref.incus_version)
      out.push({ key: 'compatIncus', warn: true });
    if (kernelMajor(member.kernel_version) !== kernelMajor(ref.kernel_version))
      out.push({ key: 'compatKernel', warn: true });
    return out.length ? out : [{ key: 'compatOk', warn: false }];
  }

  async function load() {
    loading = true;
    failed = false;
    try {
      data = await api.getHAModuleCompat();
    } catch {
      failed = true;
    } finally {
      loading = false;
    }
  }
  onMount(load);
</script>

<Card class="space-y-3 border border-border bg-card/60 p-4" data-testid="ha-compat-panel">
  <div class="flex flex-wrap items-start justify-between gap-2">
    <div class="space-y-1">
      <h2 class="text-sm font-semibold text-foreground">{t(K + 'title')}</h2>
      <p class="text-xs text-muted-foreground">{t(K + 'description')}</p>
    </div>
    <Button size="sm" variant="outline" class="min-h-11" onclick={load} disabled={loading}>
      {#if loading}<Spinner size="sm" />{/if}
      {t(K + 'refresh')}
    </Button>
  </div>

  {#if loading && !data}
    <p class="text-xs text-muted-foreground" role="status">{t(K + 'loading')}</p>
  {:else if failed}
    <Alert variant="error">{t(K + 'loadError')}</Alert>
  {:else if data}
    {#if mixed}
      <Alert variant="warning">
        <p data-testid="mixed-banner">{t(K + 'mixedBanner')}</p>
      </Alert>
    {/if}
    {#each warnings as w (w)}
      {#if WARN[w]}
        <Alert variant="warning">
          <p class="font-medium" data-testid="warn-{w}">{t(K + WARN[w][0])}</p>
          <p>
            <span class="font-medium">{t(K + 'whatToDo')}:</span>
            {t(K + WARN[w][1])}
          </p>
        </Alert>
      {/if}
    {/each}
    {#if !data.incus_available}
      <p class="text-xs text-muted-foreground">{t(K + 'incusUnavailable')}</p>
    {:else if !data.clustered}
      <p class="text-xs text-muted-foreground">{t(K + 'notClustered')}</p>
    {/if}
    {#if data.recommended && !warnings.length}
      <p class="text-xs text-success" data-testid="compat-recommended">{t(K + 'recommended')}</p>
    {/if}

    <div class="overflow-x-auto">
      <table class="w-full text-left text-xs">
        <thead class="text-muted-foreground">
          <tr>
            <th class="py-1 pr-3 font-medium">{t(K + 'colNode')}</th>
            <th class="py-1 pr-3 font-medium">{t(K + 'colOs')}</th>
            <th class="py-1 pr-3 font-medium">{t(K + 'colKernel')}</th>
            <th class="py-1 pr-3 font-medium">{t(K + 'colIncus')}</th>
            <th class="py-1 pr-3 font-medium">{t(K + 'colStatus')}</th>
            <th class="py-1 font-medium">{t(K + 'colCompat')}</th>
          </tr>
        </thead>
        <tbody>
          {#if members.length === 0}
            <tr class="border-t border-border/60" data-testid="compat-local-row">
              <td class="py-1.5 pr-3 font-medium">
                {data.local?.hostname || '—'}
                <span class="text-muted-foreground">({t(K + 'thisNode')})</span>
              </td>
              <td class="py-1.5 pr-3"
                >{data.local?.os_name || '—'} {data.local?.os_version || ''}</td
              >
              <td class="py-1.5 pr-3 font-mono">{data.local?.kernel_version || '—'}</td>
              <td class="py-1.5 pr-3">{data.local?.incus_version || '—'}</td>
              <td class="py-1.5 pr-3">—</td>
              <td class="py-1.5">—</td>
            </tr>
          {/if}
          {#each members as member (member.name)}
            <tr class="border-t border-border/60" data-testid="compat-row-{member.name}">
              <td class="py-1.5 pr-3 font-medium">
                {member.name}
                {#if member.name === data.local?.hostname}
                  <span class="text-muted-foreground">({t(K + 'thisNode')})</span>
                {/if}
              </td>
              <td class="py-1.5 pr-3">{member.os_name || '—'} {member.os_version || ''}</td>
              <td class="py-1.5 pr-3 font-mono">{member.kernel_version || '—'}</td>
              <td class="py-1.5 pr-3">{member.incus_version || '—'}</td>
              <td class="py-1.5 pr-3">{statusText(member.status)}</td>
              <td class="py-1.5">
                <span class="flex flex-wrap gap-1">
                  {#each badges(member) as b (b.key)}
                    <span
                      class="rounded border px-1.5 py-px text-[11px] font-medium text-foreground {b.warn
                        ? 'border-warning/50 bg-warning/15'
                        : 'border-success/40 bg-success/15'}">{t(K + b.key)}</span
                    >
                  {/each}
                  {#if member.error}
                    <span class="text-muted-foreground">{t(K + 'memberUnreachable')}</span>
                  {/if}
                </span>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Card>
