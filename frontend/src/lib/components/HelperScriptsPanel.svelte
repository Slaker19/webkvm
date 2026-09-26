<script>
  /**
   * Browser for the community-scripts catalog (widely known as the
   * "Proxmox VE Helper-Scripts").
   *
   * This is deliberately a read-and-inspect surface rather than a
   * one-click installer. Every entry is third-party shell that runs as
   * root inside the container it provisions, so the panel's job is to
   * make that fact impossible to miss and to let an admin read the exact
   * script before trusting it.
   */
  import { onMount } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import * as Dialog from '$lib/components/ui/dialog';
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { t } from '$lib/i18n.svelte.js';

  /**
   * onDeploy receives a script shaped like a catalog entry, so the App
   * Store can open its existing deploy dialog for it. Reusing that
   * dialog rather than building a second one keeps name validation,
   * network selection and progress reporting in a single place.
   */
  let { onDeploy = null } = $props();

  let loading = $state(true);
  let refreshing = $state(false);
  let scripts = $state([]);
  let fetchedAt = $state(null);
  let stale = $state(true);
  let sourceUrl = $state('');
  let search = $state('');
  let activeTag = $state('all');

  // Script inspection dialog.
  let detail = $state(null);
  let detailLoading = $state(false);

  // Deploying runs an unaudited third-party installer as root inside the
  // new container. That deserves a deliberate second step rather than a
  // single click next to "inspect", which is easy to hit by accident.
  let pendingDeploy = $state(null);

  // auth.user is the username string, not an object: "auth.user?.role"
  // silently evaluated to undefined, so every admin was treated as a
  // plain viewer and the import button never rendered — leaving the
  // catalog impossible to populate from the UI.
  const isAdmin = $derived(auth.role === 'admin');

  async function load() {
    loading = true;
    try {
      const res = await api.listHelperScripts();
      scripts = res.scripts || [];
      fetchedAt = res.fetched_at || null;
      stale = !!res.stale;
      sourceUrl = res.source || '';
    } catch (err) {
      toast.error(err.message || t('helperScripts.loadError'));
    } finally {
      loading = false;
    }
  }

  async function refresh() {
    refreshing = true;
    try {
      const res = await api.refreshHelperScripts();
      scripts = res.scripts || [];
      fetchedAt = res.fetched_at || null;
      stale = false;
      toast.success(t('helperScripts.refreshed').replace('{n}', scripts.length));
    } catch (err) {
      // The importer keeps the previous catalog on failure, so the list
      // on screen is still valid — say so instead of implying data loss.
      toast.error(err.message || t('helperScripts.refreshError'));
    } finally {
      refreshing = false;
    }
  }

  async function inspect(script) {
    detail = { ...script, provision: '' };
    detailLoading = true;
    try {
      const res = await api.getHelperScriptProvision(script.slug);
      detail = { ...script, ...res };
    } catch (err) {
      toast.error(err.message || t('helperScripts.inspectError'));
      detail = null;
    } finally {
      detailLoading = false;
    }
  }

  function confirmDeploy(script) {
    pendingDeploy = script;
  }

  function acceptDeploy() {
    const script = pendingDeploy;
    pendingDeploy = null;
    if (!script || !onDeploy) return;
    // The parent's deploy dialog speaks the catalog's vocabulary, so the
    // script is handed over already shaped like an appliance. deploy_id
    // comes from the backend: rebuilding the "cs:" prefix here would put
    // the namespacing rule in two places that could drift apart.
    onDeploy({
      id: script.deploy_id,
      name: script.name,
      port: script.port,
      web_path: script.web_path,
      vcpus: script.vcpus,
      ram_mb: script.ram_mb,
      disk_gb: script.disk_gb,
      default_type: 'container',
      is_helper_script: true,
    });
  }

  const tags = $derived.by(() => {
    // A plain object rather than a Map: this is a throwaway tally
    // recomputed inside the derived, so it never needs to be reactive.
    const seen = Object.create(null);
    for (const s of scripts) {
      for (const tag of s.tags || []) {
        seen[tag] = (seen[tag] || 0) + 1;
      }
    }
    return Object.entries(seen)
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .slice(0, 14)
      .map(([tag, count]) => ({ tag, count }));
  });

  const filtered = $derived.by(() => {
    const q = search.trim().toLowerCase();
    return scripts.filter((s) => {
      if (activeTag !== 'all' && !(s.tags || []).includes(activeTag)) return false;
      if (!q) return true;
      return (
        s.name?.toLowerCase().includes(q) ||
        s.slug?.toLowerCase().includes(q) ||
        (s.tags || []).some((tg) => tg.toLowerCase().includes(q))
      );
    });
  });

  function formatDate(value) {
    if (!value) return '';
    const d = new Date(value);
    if (Number.isNaN(d.getTime())) return '';
    // A zero timestamp is a "never happened" marker, not a date. It used
    // to render as "31/12/1" next to "last import", which reads as a
    // real event. The backend now sends null, but a stale client or an
    // older server must not resurrect it.
    if (d.getUTCFullYear() < 1990) return '';
    return d.toLocaleString();
  }

  onMount(load);
</script>

<div class="space-y-4">
  <!--
    The security notice is shown unconditionally and before the list.
    Burying it behind a "details" toggle would defeat its purpose: the
    user needs it while deciding, not after.
  -->
  <div
    class="rounded-xl border border-warning/40 bg-warning/10 p-4 text-sm"
    role="note"
    aria-label={t('helperScripts.securityTitle')}
  >
    <div class="flex items-start gap-3">
      <Icon name="alert-triangle" size={18} class="text-warning mt-0.5 shrink-0" />
      <div class="space-y-1">
        <p class="font-semibold text-foreground">{t('helperScripts.securityTitle')}</p>
        <p class="text-muted-foreground">{t('helperScripts.securityBody')}</p>
        {#if sourceUrl}
          <a
            href={sourceUrl}
            target="_blank"
            rel="noopener noreferrer"
            class="inline-flex items-center gap-1 text-accent hover:underline"
          >
            {t('helperScripts.viewUpstream')}
            <Icon name="external-link" size={12} />
          </a>
        {/if}
      </div>
    </div>
  </div>

  <div class="flex flex-wrap items-center gap-3">
    <div class="relative flex-1 min-w-[220px]">
      <Input
        bind:value={search}
        placeholder={t('helperScripts.searchPlaceholder')}
        aria-label={t('helperScripts.searchPlaceholder')}
      />
    </div>

    <div class="text-xs text-muted-foreground">
      {#if formatDate(fetchedAt)}
        {t('helperScripts.lastImport')}: {formatDate(fetchedAt)}
      {:else}
        {t('helperScripts.neverImported')}
      {/if}
    </div>

    {#if isAdmin}
      <!-- A stale or missing catalog is the one case where importing is
           the obvious next action, so the button stops being secondary. -->
      <Button
        variant={stale ? 'default' : 'outline'}
        size="sm"
        onclick={refresh}
        disabled={refreshing}
      >
        {#if refreshing}
          <Spinner size={14} />
          <span class="ml-2">{t('helperScripts.refreshing')}</span>
        {:else}
          <Icon name="refresh-cw" size={14} />
          <span class="ml-2">{t('helperScripts.refresh')}</span>
        {/if}
      </Button>
    {/if}
  </div>

  {#if refreshing}
    <p class="text-xs text-muted-foreground">{t('helperScripts.refreshSlow')}</p>
  {/if}

  {#if tags.length > 0}
    <div class="flex items-center gap-1.5 overflow-x-auto pb-1 scrollbar-none text-xs">
      <button
        onclick={() => (activeTag = 'all')}
        class="px-3 py-1.5 rounded-xl shrink-0 font-medium transition-all {activeTag === 'all'
          ? 'bg-accent text-accent-foreground font-semibold shadow-sm'
          : 'bg-card text-muted-foreground hover:text-foreground hover:bg-muted border border-border'}"
      >
        {t('helperScripts.allTags')}
      </button>
      {#each tags as { tag, count } (tag)}
        <button
          onclick={() => (activeTag = tag)}
          class="px-3 py-1.5 rounded-xl shrink-0 font-medium transition-all {activeTag === tag
            ? 'bg-accent text-accent-foreground font-semibold shadow-sm'
            : 'bg-card text-muted-foreground hover:text-foreground hover:bg-muted border border-border'}"
        >
          {tag} <span class="opacity-60">{count}</span>
        </button>
      {/each}
    </div>
  {/if}

  {#if loading}
    <div class="flex justify-center py-12"><Spinner /></div>
  {:else if scripts.length === 0}
    <EmptyState
      icon="package"
      title={t('helperScripts.emptyTitle')}
      description={isAdmin ? t('helperScripts.emptyAdminDesc') : t('helperScripts.emptyUserDesc')}
    />
  {:else if filtered.length === 0}
    <EmptyState icon="search" title={t('helperScripts.noMatches')} />
  {:else}
    <p class="text-xs text-muted-foreground">
      {t('helperScripts.showing')
        .replace('{shown}', filtered.length)
        .replace('{total}', scripts.length)}
    </p>

    <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {#each filtered as s (s.slug)}
        <div class="rounded-xl border border-border bg-card p-4 flex flex-col gap-2">
          <div class="flex items-start justify-between gap-2">
            <h3 class="font-semibold text-foreground leading-tight">{s.name}</h3>
            {#if s.needs_gpu}
              <span
                class="text-[10px] px-1.5 py-0.5 rounded bg-muted text-muted-foreground shrink-0"
                title={t('helperScripts.needsGpuHint')}
              >
                GPU
              </span>
            {/if}
          </div>

          <div class="text-xs text-muted-foreground flex flex-wrap gap-x-3 gap-y-1">
            {#if s.vcpus}<span>{s.vcpus} vCPU</span>{/if}
            {#if s.ram_mb}<span>{s.ram_mb} MB</span>{/if}
            {#if s.disk_gb}<span>{s.disk_gb} GB</span>{/if}
            {#if s.port}<span>:{s.port}{s.web_path || ''}</span>{/if}
          </div>

          {#if s.os}
            <div class="text-xs text-muted-foreground">{s.os} {s.version || ''}</div>
          {/if}

          <div class="mt-auto pt-2 flex items-center gap-2">
            {#if onDeploy}
              <Button size="sm" onclick={() => confirmDeploy(s)}>
                <Icon name="play" size={13} />
                <span class="ml-1.5">{t('helperScripts.deploy')}</span>
              </Button>
            {/if}
            {#if isAdmin}
              <Button variant="outline" size="sm" onclick={() => inspect(s)}>
                <Icon name="file-text" size={13} />
                <span class="ml-1.5">{t('helperScripts.inspect')}</span>
              </Button>
            {/if}
            {#if s.website}
              <a
                href={s.website}
                target="_blank"
                rel="noopener noreferrer"
                class="text-xs text-accent hover:underline inline-flex items-center gap-1"
              >
                {t('helperScripts.project')}
                <Icon name="external-link" size={11} />
              </a>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<Dialog.Root open={!!pendingDeploy} onOpenChange={(v) => !v && (pendingDeploy = null)}>
  <Dialog.Content class="max-w-lg">
    <Dialog.Header>
      <Dialog.Title>{t('helperScripts.confirmTitle')}</Dialog.Title>
      <Dialog.Description>
        {t('helperScripts.confirmDesc').replace('{name}', pendingDeploy?.name || '')}
      </Dialog.Description>
    </Dialog.Header>

    {#if pendingDeploy}
      <div class="space-y-3 text-sm">
        <div class="rounded-lg border border-warning/40 bg-warning/10 p-3 text-xs">
          {t('helperScripts.confirmWarning')}
        </div>
        <div class="text-xs text-muted-foreground space-y-1">
          {#if pendingDeploy.os}
            <div>
              {t('helperScripts.confirmBase')}: {pendingDeploy.os}
              {pendingDeploy.version || ''}
            </div>
          {/if}
          {#if pendingDeploy.install_url}
            <div>
              {t('helperScripts.confirmInstaller')}:
              <a
                href={pendingDeploy.install_url}
                target="_blank"
                rel="noopener noreferrer"
                class="text-accent hover:underline break-all">{pendingDeploy.slug}-install.sh</a
              >
            </div>
          {/if}
        </div>
      </div>
    {/if}

    <Dialog.Footer>
      <Button variant="outline" onclick={() => (pendingDeploy = null)}>
        {t('common.cancel')}
      </Button>
      <Button onclick={acceptDeploy}>{t('helperScripts.confirmAccept')}</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<Dialog.Root open={!!detail} onOpenChange={(v) => !v && (detail = null)}>
  <Dialog.Content class="max-w-3xl">
    <Dialog.Header>
      <Dialog.Title>{detail?.name || ''}</Dialog.Title>
      <Dialog.Description>{t('helperScripts.inspectDesc')}</Dialog.Description>
    </Dialog.Header>

    {#if detailLoading}
      <div class="flex justify-center py-8"><Spinner /></div>
    {:else if detail}
      <div class="space-y-3 text-sm">
        <div class="flex flex-wrap gap-3 text-xs">
          {#if detail.install_url}
            <a
              href={detail.install_url}
              target="_blank"
              rel="noopener noreferrer"
              class="text-accent hover:underline"
            >
              {t('helperScripts.installer')}
            </a>
          {/if}
          {#if detail.launcher_url}
            <a
              href={detail.launcher_url}
              target="_blank"
              rel="noopener noreferrer"
              class="text-accent hover:underline"
            >
              {t('helperScripts.launcher')}
            </a>
          {/if}
          {#if detail.license}
            <span class="text-muted-foreground">{detail.license}</span>
          {/if}
        </div>

        <pre
          class="max-h-[50vh] overflow-auto rounded-lg bg-muted/60 p-3 text-xs leading-relaxed"><code
            >{detail.provision || ''}</code
          ></pre>
      </div>
    {/if}
  </Dialog.Content>
</Dialog.Root>
