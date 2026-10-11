<script>
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { tasks } from '$lib/stores/tasks.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let summary = $state(null);
  let loading = $state(true);
  let refreshing = $state(false);
  let upgrading = $state(false);
  let showConfirmUpgrade = $state(false);
  let search = $state('');
  let selectedCategory = $state('all');
  let currentJobId = $state(null);

  // Active task tracked in global tasks store
  const activeUpgradeTask = $derived(
    tasks.find(
      (tk) =>
        tk.status === 'running' &&
        (tk.id === 'async:' + currentJobId || tk.title?.toLowerCase().includes('apt'))
    )
  );

  async function loadUpdates() {
    loading = true;
    try {
      summary = await api.getHostUpdates();
    } catch (e) {
      toast.error(t('hostUpdates.loadError', { error: e.message }));
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    loadUpdates();
  });

  async function refreshRepositories() {
    refreshing = true;
    try {
      summary = await api.refreshHostUpdates();
      toast.success(t('hostUpdates.refreshSuccess'));
    } catch (e) {
      toast.error(t('hostUpdates.refreshError', { error: e.message }));
    } finally {
      refreshing = false;
    }
  }

  async function startUpgrade() {
    showConfirmUpgrade = false;
    upgrading = true;
    try {
      const job = await api.upgradeHostUpdates();
      currentJobId = job.id;
      toast.info(t('hostUpdates.upgradeStarted'));

      // Follow job via api.waitJob which registers in the global Task Center
      await api.waitJob(job.id, {
        title: t('hostUpdates.taskTitle'),
        timeout: 35 * 60 * 1000,
      });
      toast.success(t('hostUpdates.upgradeDone'));
      await loadUpdates();
    } catch (e) {
      toast.error(t('hostUpdates.upgradeFailed', { error: e.message }));
      await loadUpdates();
    } finally {
      upgrading = false;
      currentJobId = null;
    }
  }

  function getPackageCategory(pkgName) {
    const p = pkgName.toLowerCase();
    if (
      p.startsWith('linux-image') ||
      p.startsWith('linux-headers') ||
      p.startsWith('linux-modules') ||
      p.includes('firmware') ||
      p.includes('microcode') ||
      p.startsWith('linux-generic')
    ) {
      return 'kernel';
    }
    if (
      p.includes('libvirt') ||
      p.includes('qemu') ||
      p.includes('systemd') ||
      p.includes('caddy') ||
      p.includes('incus') ||
      p.includes('docker') ||
      p.includes('containerd') ||
      p.includes('lxd')
    ) {
      return 'core';
    }
    if (
      p.includes('ssl') ||
      p.includes('tls') ||
      p.includes('crypto') ||
      p.includes('cert') ||
      p.includes('libc') ||
      p.includes('glibc') ||
      p.includes('gnutls')
    ) {
      return 'security';
    }
    return 'apps';
  }

  const categoryCounts = $derived.by(() => {
    const counts = {
      all: summary?.packages?.length || 0,
      kernel: 0,
      core: 0,
      security: 0,
      apps: 0,
    };
    for (const p of summary?.packages || []) {
      const cat = getPackageCategory(p.package || '');
      counts[cat] = (counts[cat] || 0) + 1;
    }
    return counts;
  });

  const filteredPackages = $derived(
    (summary?.packages || []).filter((p) => {
      if (selectedCategory !== 'all') {
        if (getPackageCategory(p.package || '') !== selectedCategory) {
          return false;
        }
      }
      if (!search.trim()) return true;
      const q = search.toLowerCase();
      return (
        p.package?.toLowerCase().includes(q) ||
        p.suite?.toLowerCase().includes(q) ||
        p.current_version?.toLowerCase().includes(q) ||
        p.new_version?.toLowerCase().includes(q)
      );
    })
  );
</script>

<div class="space-y-6">
  <!-- Reboot required warning banner -->
  {#if summary?.reboot_required}
    <div
      class="rounded-xl border border-warning/40 bg-warning/10 p-4 flex items-start gap-3 text-warning-foreground"
    >
      <Icon name="alertTriangle" size={20} class="text-warning shrink-0 mt-0.5" />
      <div class="space-y-1">
        <h4 class="font-semibold text-sm text-foreground">
          {t('hostUpdates.rebootRequiredTitle')}
        </h4>
        <p class="text-xs text-muted-foreground">
          {t('hostUpdates.rebootRequiredDesc')}
        </p>
        {#if summary.reboot_packages?.length}
          <p class="text-[11px] font-mono text-muted-foreground mt-1">
            {t('hostUpdates.rebootPendingPkgs', { pkgs: summary.reboot_packages.join(', ') })}
          </p>
        {/if}
      </div>
    </div>
  {/if}

  <!-- Active upgrade progress banner -->
  {#if activeUpgradeTask || upgrading || summary?.upgrade_running}
    <div class="rounded-xl border border-accent/40 bg-accent/5 p-4 space-y-3">
      <div class="flex items-center justify-between text-xs">
        <div class="flex items-center gap-2">
          <Spinner size="xs" />
          <span class="font-semibold text-foreground">
            {t('hostUpdates.upgradingBannerTitle')}
          </span>
          <span class="text-muted-foreground font-mono truncate max-w-md">
            {activeUpgradeTask?.message || ''}
          </span>
        </div>
        <span class="font-mono font-bold text-accent">
          {activeUpgradeTask?.pct ?? 0}%
        </span>
      </div>
      <div class="w-full bg-muted/60 rounded-full h-2 overflow-hidden">
        <div
          class="bg-accent h-full transition-all duration-300 rounded-full"
          style="width: {activeUpgradeTask?.pct ?? 0}%;"
        ></div>
      </div>
    </div>
  {/if}

  <!-- Status summary header -->
  <div class="rounded-xl border border-border bg-card/60 p-4 space-y-4">
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="flex items-center gap-2">
        <Icon name="package" size={18} class="text-accent shrink-0" />
        <h3 class="font-semibold text-foreground text-sm">{t('hostUpdates.title')}</h3>
      </div>

      <div class="flex items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          onclick={refreshRepositories}
          disabled={loading || refreshing || upgrading || summary?.upgrade_running}
          class="gap-1.5 text-xs"
        >
          {#if refreshing}
            <Spinner size="xs" />
          {:else}
            <Icon name="refreshCw" size={13} />
          {/if}
          {t('hostUpdates.checkUpdates')}
        </Button>

        <Button
          variant="default"
          size="sm"
          onclick={() => (showConfirmUpgrade = true)}
          disabled={loading ||
            refreshing ||
            upgrading ||
            summary?.upgrade_running ||
            !summary?.packages?.length}
          class="gap-1.5 text-xs font-medium"
        >
          {#if upgrading || summary?.upgrade_running}
            <Spinner size="xs" />
            {t('hostUpdates.upgrading')}
          {:else}
            <Icon name="download" size={13} />
            {t('hostUpdates.upgradeAll')}
          {/if}
        </Button>
      </div>
    </div>

    <div class="grid grid-cols-2 sm:grid-cols-4 gap-3 text-xs">
      <div class="p-3 rounded-lg border border-border/60 bg-muted/20">
        <p class="text-muted-foreground">{t('hostUpdates.totalAvailable')}</p>
        <p class="text-lg font-bold font-mono mt-1 text-foreground">
          {summary?.total ?? 0}
        </p>
      </div>

      <div class="p-3 rounded-lg border border-border/60 bg-muted/20">
        <p class="text-muted-foreground">{t('hostUpdates.securityUpdates')}</p>
        <p
          class="text-lg font-bold font-mono mt-1 {summary?.security_count > 0
            ? 'text-destructive'
            : 'text-success'}"
        >
          {summary?.security_count ?? 0}
        </p>
      </div>

      <div class="p-3 rounded-lg border border-border/60 bg-muted/20">
        <p class="text-muted-foreground">{t('hostUpdates.status')}</p>
        <p class="font-medium mt-1.5 flex items-center gap-1.5">
          <span
            class="inline-block w-2 h-2 rounded-full {summary?.upgrade_running || upgrading
              ? 'bg-warning animate-pulse'
              : summary?.total > 0
                ? 'bg-accent'
                : 'bg-success'}"
          ></span>
          {#if summary?.upgrade_running || upgrading}
            {t('hostUpdates.upgrading')}
          {:else if summary?.total > 0}
            {t('hostUpdates.updatesAvailable')}
          {:else}
            {t('hostUpdates.upToDate')}
          {/if}
        </p>
      </div>

      <div class="p-3 rounded-lg border border-border/60 bg-muted/20">
        <p class="text-muted-foreground">{t('hostUpdates.lastChecked')}</p>
        <p class="font-mono text-muted-foreground mt-1 truncate">
          {summary?.last_checked
            ? new Date(summary.last_checked).toLocaleTimeString()
            : t('common.never')}
        </p>
      </div>
    </div>
  </div>

  <!-- Search and category tabs -->
  <div class="space-y-3">
    <div class="flex items-center gap-2 overflow-x-auto pb-1 text-xs">
      <Button
        variant={selectedCategory === 'all' ? 'default' : 'outline'}
        size="sm"
        onclick={() => (selectedCategory = 'all')}
        class="h-7 text-xs px-2.5 rounded-full"
      >
        {t('hostUpdates.catAll')} ({categoryCounts.all})
      </Button>
      <Button
        variant={selectedCategory === 'kernel' ? 'default' : 'outline'}
        size="sm"
        onclick={() => (selectedCategory = 'kernel')}
        class="h-7 text-xs px-2.5 rounded-full gap-1.5"
      >
        <span class="w-1.5 h-1.5 rounded-full bg-warning"></span>
        {t('hostUpdates.catKernel')} ({categoryCounts.kernel})
      </Button>
      <Button
        variant={selectedCategory === 'core' ? 'default' : 'outline'}
        size="sm"
        onclick={() => (selectedCategory = 'core')}
        class="h-7 text-xs px-2.5 rounded-full gap-1.5"
      >
        <span class="w-1.5 h-1.5 rounded-full bg-accent"></span>
        {t('hostUpdates.catCore')} ({categoryCounts.core})
      </Button>
      <Button
        variant={selectedCategory === 'security' ? 'default' : 'outline'}
        size="sm"
        onclick={() => (selectedCategory = 'security')}
        class="h-7 text-xs px-2.5 rounded-full gap-1.5"
      >
        <span class="w-1.5 h-1.5 rounded-full bg-destructive"></span>
        {t('hostUpdates.catSecurity')} ({categoryCounts.security})
      </Button>
      <Button
        variant={selectedCategory === 'apps' ? 'default' : 'outline'}
        size="sm"
        onclick={() => (selectedCategory = 'apps')}
        class="h-7 text-xs px-2.5 rounded-full"
      >
        {t('hostUpdates.catApps')} ({categoryCounts.apps})
      </Button>
    </div>

    <div class="flex items-center justify-between gap-3 flex-wrap">
      <div class="relative w-full max-w-sm">
        <Input
          bind:value={search}
          placeholder={t('hostUpdates.searchPlaceholder')}
          class="text-xs h-9"
        />
      </div>
      <div class="text-xs text-muted-foreground">
        {t('hostUpdates.packagesListed', { count: filteredPackages.length })}
      </div>
    </div>
  </div>

  <!-- Packages list table -->
  {#if loading}
    <div class="flex items-center justify-center p-12">
      <Spinner size="md" />
    </div>
  {:else if !summary?.packages?.length}
    <div
      class="text-center p-12 border border-border rounded-xl bg-card/40 text-muted-foreground text-sm space-y-2"
    >
      <Icon name="checkCircle" size={32} class="mx-auto text-success/80" />
      <p class="font-medium text-foreground">{t('hostUpdates.allUpdatedTitle')}</p>
      <p class="text-xs text-muted-foreground">{t('hostUpdates.allUpdatedDesc')}</p>
    </div>
  {:else if filteredPackages.length === 0}
    <div
      class="text-center p-8 border border-border rounded-xl bg-card/40 text-muted-foreground text-sm"
    >
      {t('hostUpdates.noPackagesMatch')}
    </div>
  {:else}
    <div class="border border-border rounded-xl bg-card overflow-hidden">
      <div class="overflow-x-auto">
        <table class="w-full text-xs text-left">
          <thead
            class="bg-muted/40 text-muted-foreground border-b border-border/60 uppercase text-[10px]"
          >
            <tr>
              <th class="px-4 py-2.5 font-semibold">{t('hostUpdates.colPackage')}</th>
              <th class="px-4 py-2.5 font-semibold">{t('hostUpdates.colCurrent')}</th>
              <th class="px-4 py-2.5 font-semibold">{t('hostUpdates.colNew')}</th>
              <th class="px-4 py-2.5 font-semibold">{t('hostUpdates.colSuite')}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border/60">
            {#each filteredPackages as pkg (pkg.package)}
              <tr class="hover:bg-muted/20 transition-colors">
                <td class="px-4 py-3 font-medium text-foreground">
                  <div class="flex items-center gap-2 flex-wrap">
                    <span class="font-mono">{pkg.package}</span>
                    {#if pkg.is_security}
                      <span
                        class="px-1.5 py-0.5 rounded text-[10px] bg-destructive/15 text-destructive font-semibold"
                      >
                        {t('hostUpdates.securityBadge')}
                      </span>
                    {/if}
                    {#if getPackageCategory(pkg.package) === 'kernel'}
                      <span
                        class="px-1.5 py-0.5 rounded text-[10px] bg-warning/15 text-warning font-medium"
                      >
                        kernel
                      </span>
                    {:else if getPackageCategory(pkg.package) === 'core'}
                      <span
                        class="px-1.5 py-0.5 rounded text-[10px] bg-accent/15 text-accent font-medium"
                      >
                        core
                      </span>
                    {/if}
                    <span class="text-[10px] text-muted-foreground font-mono">({pkg.arch})</span>
                  </div>
                </td>
                <td class="px-4 py-3 font-mono text-muted-foreground">
                  {pkg.current_version}
                </td>
                <td class="px-4 py-3 font-mono text-accent font-semibold">
                  {pkg.new_version}
                </td>
                <td class="px-4 py-3 text-muted-foreground text-[11px]">
                  {pkg.suite}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </div>
  {/if}
</div>

<!-- Confirm upgrade dialog -->
<ConfirmDialog
  bind:open={showConfirmUpgrade}
  title={t('hostUpdates.confirmTitle')}
  message={t('hostUpdates.confirmMessage', { count: summary?.total || 0 })}
  confirmLabel={t('hostUpdates.confirmButton')}
  variant="default"
  onConfirm={startUpgrade}
  onCancel={() => (showConfirmUpgrade = false)}
/>
