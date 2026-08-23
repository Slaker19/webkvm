<script>
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
  import { t } from '../lib/i18n.svelte.js';

  let schema = $state(null);
  let values = $state({});
  let pendingRestart = $state([]);
  let editing = $state({});
  let loading = $state(true);
  let saving = $state(false);
  let restarting = $state(false);
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

  // Group fields by Section, preserving schema order.
  const sections = $derived.by(() => {
    if (!schema) return [];
    const order = [];
    const map = new Map();
    for (const f of schema.fields) {
      if (!map.has(f.section)) {
        order.push(f.section);
        map.set(f.section, []);
      }
      map.get(f.section).push(f);
    }
    return order.map((name) => ({ name, fields: map.get(name) }));
  });

  const activeFields = $derived(
    sections.find((s) => s.name.toLowerCase() === activeTab)?.fields || []
  );

  // True if the user has unsaved changes.
  const isDirty = $derived(Object.keys(editing).length > 0);

  function setEdit(key, value) {
    editing = { ...editing, [key]: value };
  }

  async function save() {
    if (!isDirty) return;
    saving = true;
    try {
      const result = await api.setSettings(editing);
      if (result.failed && Object.keys(result.failed).length > 0) {
        toast.error(t('settings.valuesRejected'));
        return;
      }
      // Refresh from server.
      const g = await api.getSettings();
      values = g.values;
      pendingRestart = g.pending_restart || [];

      // Push live fields to the running backend. The response
      // tells us which keys were applied — the server may have
      // decided some don't need explicit action (e.g. token_ttl,
      // which is read per request).
      const liveKeys = Object.keys(editing).filter((k) => {
        const f = schema.fields.find((f) => f.key === k);
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

  const tabs = $derived([
    ...sections.map((s) => ({ name: s.name.toLowerCase(), label: s.name })),
    { name: 'notifications', label: t('settings.notifications') },
  ]);
</script>

<div class="p-6 max-w-4xl">
  <PageHeader title={t('settings.title')} subtitle={t('settings.subtitle', { n: sections.length })}>
    {#snippet actions()}
      {#if isDirty}
        <Button variant="outline" onclick={discard} disabled={saving}
          >{t('settings.discard')}</Button
        >
        <Button onclick={save} disabled={saving}>
          {saving ? t('settings.saving') : t('settings.saveChanges')}
        </Button>
      {/if}
    {/snippet}
  </PageHeader>

  {#if loading}
    <p class="text-sm text-muted-foreground">{t('common.loading')}</p>
  {:else if !schema}
    <p class="text-sm text-destructive">{t('settings.loadFailed')}</p>
  {:else}
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

    <div class="flex gap-1 mb-4 border-b border-border">
      {#each tabs as tab (tab.name)}
        <button
          class="px-3 py-2 text-sm font-medium border-b-2 transition-colors {activeTab === tab.name
            ? 'border-accent text-foreground'
            : 'border-transparent text-muted-foreground hover:text-foreground'}"
          onclick={() => (activeTab = tab.name)}
        >
          {tab.label}
        </button>
      {/each}
    </div>

    {#if activeTab === 'notifications'}
      <NotificationsTab />
    {:else}
      <SettingsTab fields={activeFields} {values} {editing} onChange={setEdit} />
    {/if}
  {/if}
</div>
