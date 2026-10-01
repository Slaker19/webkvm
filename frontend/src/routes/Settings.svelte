<script>
  import { SvelteMap } from 'svelte/reactivity';
  /**
   * Settings — the configuration UI.
   *
   * Schema-driven, four-tab layout, Proxmox-style.
   *
   *   - Save commits pending changes to the config store. The
   *     response tells us which fields are live-applied and which
   *     require a restart. A banner invites the user to restart
   *     when the latter set is non-empty.
   *   - Live fields (e.g. logging.level, backup.retention_count)
   *     are also pushed through POST /api/settings/apply-live so
   *     the running backend picks them up without restart.
   *   - "Apply & restart" hits POST /api/system/apply-restart and
   *     reloads the page after 3s once the new version is back.
   */
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SettingsTab from '$lib/components/SettingsTab.svelte';
  import NotificationsTab from '$lib/components/NotificationsTab.svelte';
  import SnippetsTab from '$lib/components/SnippetsTab.svelte';
  import SecurityJailTab from '$lib/components/SecurityJailTab.svelte';
  import { Skeleton } from '$lib/components/ui/skeleton';
  import { t } from '../lib/i18n.svelte.js';

  let schema = $state(null);
  let values = $state({});
  let pendingRestart = $state([]);
  let editing = $state({});
  let errors = $state({});
  let loading = $state(true);
  let saving = $state(false);
  let restarting = $state(false);
  let confirmingReset = $state(false);
  let resetting = $state(false);
  let activeTab = $state('server');

  onMount(async () => {
    try {
      const [s, g] = await Promise.all([api.getSettingsSchema(), api.getSettings()]);
      schema = s.schema;
      values = g.values;
      pendingRestart = g.pending_restart || [];
    } catch (err) {
      toast.error(t('settings.loadError', { error: err.message }));
    } finally {
      loading = false;
    }
  });

  // Group fields by Section, preserving schema order. Defensive:
  // a malformed schema (missing section name) must never crash the page.
  const sections = $derived.by(() => {
    if (!schema?.fields) return [];
    const order = [];
    const map = new SvelteMap();
    for (const f of schema.fields) {
      const section = f.section || 'general';
      if (!map.has(section)) {
        order.push(section);
        map.set(section, []);
      }
      map.get(section).push(f);
    }
    return order.map((name) => ({ name: name || '', fields: map.get(name) || [] }));
  });

  // Field labels come from the backend schema in English. Translate them
  // client-side, keyed by setting name (server.bind_addr →
  // settings.field_server_bind_addr), falling back to the schema label for
  // any field the frontend doesn't know yet.
  function fieldLabel(f) {
    const key = `settings.field_${(f.key || '').replace(/[^a-zA-Z0-9]/g, '_')}`;
    const translated = t(key);
    return translated !== key ? translated : f.label || f.key;
  }

  const activeFields = $derived(
    (sections.find((s) => (s.name || '').toLowerCase() === activeTab)?.fields || []).map((f) => ({
      ...f,
      label: fieldLabel(f),
    }))
  );

  // True if the user has unsaved changes.
  const isDirty = $derived(Object.keys(editing).length > 0);

  function setEdit(key, value) {
    editing = { ...editing, [key]: value };
    // Clear this field's server-side error as soon as the user touches it.
    if (errors[key]) {
      const next = { ...errors };
      delete next[key];
      errors = next;
    }
  }

  async function save() {
    if (!isDirty) return;
    saving = true;
    try {
      const result = await api.setSettings(editing);
      if (result.failed && Object.keys(result.failed).length > 0) {
        // Per-field reasons are rendered inline under each offending input.
        errors = result.failed;
        toast.error(t('settings.valuesRejected'));
        return;
      }
      errors = {};
      // Refresh from server.
      const g = await api.getSettings();
      values = g.values;
      pendingRestart = g.pending_restart || [];

      // Push live fields to the running backend. The response
      // tells us which keys were applied — the server may have
      // decided some don't need explicit action (e.g. token_ttl,
      // which is read per request).
      const liveKeys = Object.keys(editing).filter((k) => {
        const f = schema?.fields?.find((f) => f.key === k);
        return f && f.hot_reload;
      });
      if (liveKeys.length > 0) {
        try {
          const r = await api.applyLiveSettings(liveKeys);
          toast.success(
            t('settings.savedLive', {
              applied: result.applied.length,
              live: r.applied.length,
              restart: pendingRestart.length,
            })
          );
        } catch (err) {
          toast.warning(t('settings.liveApplyFailed', { error: err.message }));
        }
      } else {
        toast.success(
          t('settings.savedRestart', {
            applied: result.applied.length,
            restart: pendingRestart.length,
          })
        );
      }
      editing = {};
    } catch (err) {
      toast.error(t('settings.saveFailed', { error: err.message }));
    } finally {
      saving = false;
    }
  }

  function discard() {
    editing = {};
    errors = {};
  }

  async function resetAll() {
    resetting = true;
    try {
      await api.resetSettings();
      const g = await api.getSettings();
      values = g.values;
      pendingRestart = g.pending_restart || [];
      editing = {};
      errors = {};
      confirmingReset = false;
      toast.success(t('settings.resetAllDone'));
    } catch (err) {
      toast.error(t('settings.resetAllFailed', { error: err.message }));
    } finally {
      resetting = false;
    }
  }

  async function applyAndRestart() {
    if (pendingRestart.length === 0) return;
    restarting = true;
    try {
      await api.applyRestart(pendingRestart);
      toast.success(t('settings.restartingMsg'));
      // Reload once the new version is up. 3s gives the
      // systemd/Docker supervisor time to bring the new process
      // back listening.
      setTimeout(() => location.reload(), 3000);
    } catch (err) {
      toast.error(t('settings.restartFailed', { error: err.message }));
      restarting = false;
    }
  }

  function sectionLabel(name) {
    const key = `settings.section_${(name || '').toLowerCase()}`;
    const translated = t(key);
    return translated !== key ? translated : name || '';
  }

  const tabs = $derived([
    ...sections.map((s) => ({
      name: (s.name || '').toLowerCase(),
      label: sectionLabel(s.name),
    })),
    { name: 'notifications', label: t('settings.notifications') },
    { name: 'snippets', label: t('settings.snippets') },
    { name: 'jail', label: 'Seguridad (Jail)' },
  ]);
</script>

<div class="p-4 sm:p-6 w-full max-w-7xl mx-auto">
  <PageHeader title={t('settings.title')} subtitle={t('settings.subtitle', { n: tabs.length })}>
    {#snippet actions()}
      {#if isDirty}
        <Button variant="outline" onclick={discard} disabled={saving}
          >{t('settings.discard')}</Button
        >
        <Button onclick={save} disabled={saving}>
          {saving ? t('settings.saving') : t('settings.saveChanges')}
        </Button>
      {:else if activeTab !== 'notifications' && activeTab !== 'snippets' && activeTab !== 'jail'}
        <Button variant="outline" onclick={() => (confirmingReset = true)} disabled={loading}>
          {t('settings.resetAll')}
        </Button>
      {/if}
    {/snippet}
  </PageHeader>

  {#if loading}
    <div
      class="border border-border rounded-lg bg-card divide-y divide-border"
      role="presentation"
      aria-hidden="true"
    >
      {#each Array(6) as _, i (i)}
        <div class="flex items-center justify-between gap-4 p-4">
          <div class="space-y-1.5">
            <Skeleton class="h-3.5 w-32" />
            <Skeleton class="h-3 w-48" />
          </div>
          <Skeleton class="h-8 w-40 rounded-md" />
        </div>
      {/each}
    </div>
  {:else if !schema}
    <p class="text-sm text-destructive">{t('settings.loadFailed')}</p>
  {:else}
    {#if confirmingReset}
      <div
        class="mb-4 border border-destructive/40 bg-destructive/10 rounded-lg p-4 flex items-center gap-4"
      >
        <div class="flex-1">
          <p class="text-sm font-medium">{t('settings.resetAllTitle')}</p>
          <p class="text-xs text-muted-foreground mt-0.5">{t('settings.resetAllDesc')}</p>
        </div>
        <Button variant="outline" onclick={() => (confirmingReset = false)} disabled={resetting}
          >{t('common.cancel')}</Button
        >
        <Button variant="destructive" onclick={resetAll} disabled={resetting}
          >{t('settings.resetAll')}</Button
        >
      </div>
    {/if}

    {#if pendingRestart.length > 0}
      <div
        class="mb-4 border border-warning/40 bg-warning/10 rounded-lg p-4 flex items-center gap-4"
      >
        <div class="flex-1">
          <p class="text-sm font-medium">
            {t('settings.pendingRestartCount', {
              n: pendingRestart.length,
              s: pendingRestart.length === 1 ? '' : 's',
            })}
          </p>
          <p class="text-xs text-muted-foreground mt-0.5">
            {pendingRestart.map((k) => k.split('.').slice(-1)[0]).join(', ')}
          </p>
        </div>
        <Button onclick={applyAndRestart} disabled={restarting}>
          {restarting ? t('settings.restarting') : t('settings.applyRestart')}
        </Button>
      </div>
    {/if}

    <div class="grid grid-cols-1 md:grid-cols-4 gap-6 items-start">
      <nav
        class="flex flex-row md:flex-col gap-1 overflow-x-auto pb-2 md:pb-0 border-b md:border-b-0 md:border-r border-border md:pr-4"
      >
        {#each tabs as tab (tab.name)}
          <button
            class="flex items-center gap-2.5 px-3 py-2 text-xs font-medium rounded-lg text-left transition-all whitespace-nowrap {activeTab ===
            tab.name
              ? 'bg-accent/15 text-accent font-semibold border-l-2 md:border-l-2 border-accent'
              : 'text-muted-foreground hover:bg-muted/30 hover:text-foreground'}"
            onclick={() => (activeTab = tab.name)}
          >
            <span>{tab.label}</span>
          </button>
        {/each}
      </nav>

      <div class="md:col-span-3">
        {#if activeTab === 'notifications'}
          <NotificationsTab />
        {:else if activeTab === 'snippets'}
          <SnippetsTab />
        {:else if activeTab === 'jail'}
          <SecurityJailTab />
        {:else}
          <SettingsTab fields={activeFields} {values} {editing} {errors} onChange={setEdit} />
        {/if}
      </div>
    </div>
  {/if}
</div>
