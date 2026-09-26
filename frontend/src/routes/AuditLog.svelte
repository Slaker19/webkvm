<script>
  import { SvelteURLSearchParams } from 'svelte/reactivity';
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Pagination from '$lib/components/Pagination.svelte';
  import * as Dialog from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';

  let entries = $state([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state(null);

  let search = $state('');
  let selectedUser = $state('');
  let selectedAction = $state('');
  let page = $state(1);
  let pageSize = $state(25);
  let autoRefresh = $state(false);
  let refreshTimer = null;

  let detailModalEntry = $state(null);

  // Category values are exact action sets the backend matches (commas);
  // they mirror the real audit vocabulary — single-action values used
  // to match nothing (e.g. firewall.apply) or a fraction of the label.
  const actionCategories = $derived([
    { label: t('audit.catAll'), value: '' },
    {
      label: t('audit.catAuth'),
      value: 'auth.login,auth.logout,auth.login_failed,auth.login_2fa,auth.2fa_failed',
    },
    {
      label: t('audit.catVmPower'),
      value: 'vm.start,vm.shutdown,vm.reboot,vm.forceoff,vm.resume,vm.suspend',
    },
    {
      label: t('audit.catVmManage'),
      value:
        'vm.create,vm.delete,vm.clone,vm.import,vm.import_failed,vm.make_template,vm.unset_template,vm.instantiate,vm.restore,vm.restore_failed,vm.appliance_deploy,vm.appliance_deploy_blocked,vm.import_quota_rollback',
    },
    {
      label: t('audit.catVmSnapshot'),
      value: 'vm.snapshot_create,vm.snapshot_delete,vm.snapshot_revert',
    },
    {
      label: t('audit.catVmHardware'),
      value:
        'vm.update,vm.disk_attach,vm.disk_detach,vm.disk_resize,vm.disk_bus_change,vm.net_attach,vm.net_detach,vm.pci_attach,vm.pci_detach,vm.usb_attach,vm.usb_detach,vm.shared_folder_attach,vm.shared_folder_detach,vm.meta_update,vm.boot_set,vm.autostart_set,vm.schedule_set,vm.schedule_manual,vm.alert.rules,vm.firewall',
    },
    {
      label: t('audit.catUsers'),
      value:
        'user.create,user.update,user.delete,user.revoke_sessions,user.change_password,user.2fa_enabled,user.2fa_disabled,token.create,token.delete,token.revoke',
    },
    {
      label: t('audit.catFirewall'),
      value:
        'firewall.host.apply,firewall.host.confirm,firewall.host.rollback,firewall.host.import,network.create,network.update,network.delete,network.start,network.stop,network.lease.release',
    },
    {
      label: t('audit.catBackup'),
      value:
        'backup.run_started,backup.run.delete,backup.restore,backup.restore_failed,backup.target.create,backup.target.update,backup.target.delete,backup.schedule.create,backup.file.delete,backup.config.delete,system.backup,system.backup.failed',
    },
  ]);

  async function loadLogs() {
    loading = true;
    error = null;
    try {
      const offset = (page - 1) * pageSize;
      const res = await api.listAudit({
        q: search.trim() || undefined,
        user: selectedUser.trim() || undefined,
        action: selectedAction || undefined,
        limit: pageSize,
        offset: offset,
      });
      entries = res?.entries || [];
      total = res?.total || 0;
    } catch (err) {
      error = err?.message || t('audit.loadError');
    } finally {
      loading = false;
    }
  }

  function handleSearch(e) {
    e.preventDefault();
    page = 1;
    loadLogs();
  }

  function handlePageChange(newPage) {
    page = newPage;
    loadLogs();
  }

  // Human label for a raw audit action code (auth.login → "Sign in"),
  // keyed audit.actions.<code with dots as underscores>. Unknown or
  // dynamic codes fall back to the raw code so nothing is ever hidden.
  function actionLabel(action) {
    if (!action) return t('common.unknown');
    const key = `audit.actions.${action.replace(/[^a-zA-Z0-9]/g, '_')}`;
    const translated = t(key);
    return translated !== key ? translated : action;
  }

  function getActionBadge(action) {
    if (!action) return { bg: 'bg-muted text-muted-foreground', label: t('common.unknown') };
    const label = actionLabel(action);
    if (
      action.includes('delete') ||
      action.includes('forceoff') ||
      action.includes('destroy') ||
      action.includes('revoke')
    ) {
      return { bg: 'bg-destructive/15 text-destructive border-destructive/30', label };
    }
    if (
      action.includes('start') ||
      action.includes('create') ||
      action.includes('login') ||
      action.includes('apply')
    ) {
      return { bg: 'bg-success/15 text-success border-success/30', label };
    }
    if (
      action.includes('reboot') ||
      action.includes('update') ||
      action.includes('snapshot') ||
      action.includes('bus_change')
    ) {
      return { bg: 'bg-accent/15 text-accent border-accent/30', label };
    }
    return { bg: 'bg-muted text-muted-foreground border-border', label };
  }

  function formatTime(iso) {
    if (!iso) return '—';
    try {
      const d = new Date(iso);
      return d.toLocaleString();
    } catch {
      return iso;
    }
  }

  function exportLogs(format) {
    const params = new SvelteURLSearchParams();
    if (search.trim()) params.set('q', search.trim());
    if (selectedUser.trim()) params.set('user', selectedUser.trim());
    if (selectedAction) params.set('action', selectedAction);
    params.set('format', format);
    window.open(`/api/audit/export?${params.toString()}`, '_blank');
  }

  onMount(() => {
    loadLogs();
    return () => {
      if (refreshTimer) clearInterval(refreshTimer);
    };
  });

  $effect(() => {
    if (refreshTimer) {
      clearInterval(refreshTimer);
      refreshTimer = null;
    }
    if (autoRefresh) {
      refreshTimer = setInterval(() => {
        loadLogs();
      }, 8000);
    }
    // Return the cleanup instead of relying on the next run to clear
    // the handle. Without it, an unmount that races an effect re-run
    // leaves an orphaned 8-second interval polling the audit endpoint
    // for the rest of the session.
    return () => {
      if (refreshTimer) {
        clearInterval(refreshTimer);
        refreshTimer = null;
      }
    };
  });
</script>

<div class="p-3 sm:p-5 w-full max-w-[1700px] mx-auto space-y-6">
  <!-- Header -->
  <div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
    <div>
      <h1 class="text-2xl font-bold tracking-tight text-foreground flex items-center gap-2.5">
        <Icon name="fileText" size={24} class="text-accent" />
        {t('audit.title')}
      </h1>
      <p class="text-sm text-muted-foreground mt-1">
        {t('audit.subtitle')}
      </p>
    </div>

    <!-- Actions -->
    <div class="flex items-center gap-2">
      <button
        onclick={() => (autoRefresh = !autoRefresh)}
        class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg border transition-all {autoRefresh
          ? 'bg-success/10 border-success/30 text-success'
          : 'bg-card/60 border-border text-muted-foreground hover:text-foreground'}"
        title={t('audit.autoRefreshTitle')}
      >
        <span
          class="w-2 h-2 rounded-full {autoRefresh
            ? 'bg-success animate-pulse'
            : 'bg-muted-foreground'}"
        ></span>
        {t('audit.autoRefresh')}
      </button>

      <button
        onclick={loadLogs}
        disabled={loading}
        class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-card/60 border border-border text-muted-foreground hover:text-foreground hover:bg-card transition-all"
        title={t('audit.refreshTitle')}
      >
        <Icon name="refresh" size={14} class={loading ? 'animate-spin' : ''} />
        {t('common.refresh')}
      </button>

      <div class="relative inline-block">
        <button
          onclick={() => exportLogs('csv')}
          class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-accent/20 border border-accent/30 text-accent hover:bg-accent/30 transition-all"
          title={t('audit.exportCsvTitle')}
        >
          <Icon name="download" size={14} />
          {t('audit.exportCsv')}
        </button>
      </div>
    </div>
  </div>

  <!-- Filters -->
  <form
    onsubmit={handleSearch}
    class="p-4 rounded-xl bg-card/40 border border-border/80 backdrop-blur-sm space-y-3"
  >
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-12">
      <!-- Search Input -->
      <div class="sm:col-span-6 relative">
        <Icon
          name="search"
          size={15}
          class="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground"
        />
        <input
          type="text"
          bind:value={search}
          placeholder={t('audit.searchPlaceholder')}
          class="w-full pl-9 pr-3 py-2 text-sm rounded-lg bg-background/60 border border-border focus:border-ring focus:outline-none focus:ring-1 focus:ring-ring text-foreground placeholder:text-muted-foreground"
        />
      </div>

      <!-- Action Filter -->
      <div class="sm:col-span-4">
        <select
          bind:value={selectedAction}
          onchange={() => {
            page = 1;
            loadLogs();
          }}
          class="w-full px-3 py-2 text-sm rounded-lg bg-background/60 border border-border focus:border-accent focus:outline-none focus:ring-1 focus:ring-accent text-foreground"
        >
          {#each actionCategories as cat (cat.value)}
            <option value={cat.value}>{cat.label}</option>
          {/each}
        </select>
      </div>

      <!-- Search Submit Button -->
      <div class="sm:col-span-2 flex gap-2">
        <button
          type="submit"
          class="w-full px-3 py-2 text-sm font-medium rounded-lg bg-accent hover:bg-accent-hover text-accent-foreground shadow-sm transition-all"
        >
          {t('audit.filter')}
        </button>
        {#if search || selectedAction || selectedUser}
          <button
            type="button"
            onclick={() => {
              search = '';
              selectedAction = '';
              selectedUser = '';
              page = 1;
              loadLogs();
            }}
            class="px-2.5 py-2 text-xs rounded-lg border border-border text-muted-foreground hover:text-foreground"
            title={t('audit.clearFilters')}
          >
            <Icon name="x" size={14} />
          </button>
        {/if}
      </div>
    </div>
  </form>

  <!-- Table Container -->
  <div class="rounded-xl border border-border/80 bg-card/30 overflow-hidden backdrop-blur-sm">
    {#if loading && entries.length === 0}
      <div class="p-16 flex flex-col items-center justify-center text-muted-foreground gap-3">
        <Spinner size="lg" />
        <p class="text-sm">{t('audit.loading')}</p>
      </div>
    {:else if error}
      <div class="p-8 text-center text-destructive">
        <p class="font-medium">{error}</p>
        <button onclick={loadLogs} class="mt-3 text-xs underline hover:text-foreground"
          >{t('audit.retry')}</button
        >
      </div>
    {:else if entries.length === 0}
      <EmptyState
        icon="fileText"
        title={t('audit.emptyTitle')}
        description={t('audit.emptyDesc')}
      />
    {:else}
      <div class="overflow-x-auto">
        <table class="w-full text-left text-sm">
          <thead
            class="bg-muted/40 text-xs font-semibold text-muted-foreground uppercase border-b border-border/60"
          >
            <tr>
              <th class="px-4 py-3">{t('audit.colTime')}</th>
              <th class="px-4 py-3">{t('audit.colUser')}</th>
              <th class="px-4 py-3">{t('audit.colAction')}</th>
              <th class="px-4 py-3">{t('audit.colResource')}</th>
              <th class="px-4 py-3">{t('audit.colIp')}</th>
              <th class="px-4 py-3">{t('audit.colStatus')}</th>
              <th class="px-4 py-3 text-right">{t('common.details')}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border/40 font-mono text-xs">
            <!--
              Key by a stable tuple, not by the entry object.
              auto-refresh reloads `entries` every 8 s, producing fresh
              objects each time; with `(e)` every identity changed, so
              Svelte rebuilt the entire table and the operator lost
              their text selection and scroll position mid-read.
              Audit entries have no id, so the key is built from
              (time, action, resource) plus the row index. The index
              only matters for genuinely identical events — the live
              audit log really does contain pairs like
              "21:43:20Z host.terminal.disconnect host" — and Svelte
              requires unique keys.
            -->
            {#each entries as e, i (`${e.time}|${e.action}|${e.resource}|${i}`)}
              {@const badge = getActionBadge(e.action)}
              <tr class="hover:bg-muted/20 transition-colors">
                <!-- Timestamp -->
                <td class="px-4 py-3 whitespace-nowrap text-muted-foreground font-sans">
                  {formatTime(e.time)}
                </td>

                <!-- User & Role -->
                <td class="px-4 py-3 whitespace-nowrap font-sans">
                  <div class="flex items-center gap-2">
                    <div
                      class="w-6 h-6 rounded-full bg-accent/20 text-accent flex items-center justify-center text-xs font-semibold uppercase"
                    >
                      {e.user ? e.user.charAt(0) : '?'}
                    </div>
                    <div>
                      <span class="font-medium text-foreground"
                        >{e.user || t('audit.systemUser')}</span
                      >
                      {#if e.role}
                        <span class="text-[10px] text-muted-foreground block uppercase font-mono"
                          >{e.role}</span
                        >
                      {/if}
                    </div>
                  </div>
                </td>

                <!-- Action Badge -->
                <td class="px-4 py-3 whitespace-nowrap">
                  <span
                    class="inline-flex items-center px-2 py-0.5 rounded text-[11px] font-medium border {badge.bg}"
                    title={e.action}
                  >
                    {badge.label}
                  </span>
                </td>

                <!-- Resource -->
                <td class="px-4 py-3 whitespace-nowrap font-medium text-foreground">
                  {e.resource || '—'}
                </td>

                <!-- IP -->
                <td class="px-4 py-3 whitespace-nowrap text-muted-foreground">
                  {e.ip || '127.0.0.1'}
                </td>

                <!-- Status / Error -->
                <td class="px-4 py-3 whitespace-nowrap">
                  {#if e.error}
                    <span
                      class="inline-flex items-center gap-1 text-destructive font-sans font-medium text-xs"
                    >
                      <Icon name="error" size={13} />
                      {t('audit.statusFailed')}
                    </span>
                  {:else}
                    <span
                      class="inline-flex items-center gap-1 text-success font-sans font-medium text-xs"
                    >
                      <Icon name="check" size={13} />
                      {t('audit.statusSuccess')}
                    </span>
                  {/if}
                </td>

                <!-- Details Action Button -->
                <td class="px-4 py-3 text-right whitespace-nowrap font-sans">
                  <button
                    onclick={() => (detailModalEntry = e)}
                    class="inline-flex items-center gap-1 px-2.5 py-1 text-xs font-medium rounded-md bg-muted/60 hover:bg-muted text-foreground transition-all"
                  >
                    <Icon name="info" size={12} />
                    {t('audit.viewJson')}
                  </button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>

      <!-- Pagination -->
      <div class="p-4 border-t border-border/60 flex items-center justify-between">
        <div class="text-xs text-muted-foreground">
          {t('audit.showing', { shown: entries.length, total })}
        </div>
        <!--
          Pagination is 0-based (page 0 is the first page) while this
          view is 1-based (`page` starts at 1 and the request offset is
          (page - 1) * pageSize), so the index has to be shifted.
          Passing `current`/`onchange` did nothing at all — those are
          not props of the component, and its `page` was not bound, so
          clicking Next only moved a label while the table kept showing
          page 1. Every audit entry past the first page was
          unreachable.
        -->
        <Pagination
          page={page - 1}
          onpagechange={(i) => handlePageChange(i + 1)}
          {total}
          {pageSize}
        />
      </div>
    {/if}
  </div>
</div>

<!-- Event Detail Modal -->
<Dialog.Root
  open={!!detailModalEntry}
  onOpenChange={(v) => {
    if (!v) detailModalEntry = null;
  }}
>
  <Dialog.Content class="sm:max-w-2xl max-h-[85vh] flex flex-col">
    {#if detailModalEntry}
      <Dialog.Header>
        <Dialog.Title class="flex items-center gap-2">
          <Icon name="fileText" size={18} class="text-accent" />
          {t('audit.modalTitle')}
        </Dialog.Title>
      </Dialog.Header>

      <div class="space-y-4 overflow-y-auto flex-1 min-w-0">
        <div
          class="grid grid-cols-1 sm:grid-cols-2 gap-3 text-xs bg-muted/30 p-3 rounded-lg border border-border/50"
        >
          <div>
            <span class="text-muted-foreground">{t('audit.fieldAction')}:</span>
            <strong class="text-foreground" title={detailModalEntry.action}
              >{actionLabel(detailModalEntry.action)}</strong
            >
          </div>
          <div>
            <span class="text-muted-foreground">{t('audit.fieldDate')}:</span>
            <span class="text-foreground">{formatTime(detailModalEntry.time)}</span>
          </div>
          <div>
            <span class="text-muted-foreground">{t('audit.fieldUser')}:</span>
            <span class="text-foreground"
              >{detailModalEntry.user} ({detailModalEntry.role || 'n/a'})</span
            >
          </div>
          <div>
            <span class="text-muted-foreground">{t('audit.fieldIp')}:</span>
            <span class="text-foreground font-mono">{detailModalEntry.ip}</span>
          </div>
          {#if detailModalEntry.resource}
            <div class="col-span-2">
              <span class="text-muted-foreground">{t('audit.fieldResource')}:</span>
              <span class="text-foreground">{detailModalEntry.resource}</span>
            </div>
          {/if}
          {#if detailModalEntry.error}
            <div class="col-span-2 text-destructive">
              <span class="font-semibold">{t('common.error')}:</span>
              {detailModalEntry.error}
            </div>
          {/if}
        </div>

        <div>
          <!--
            This labels a read-only <pre>, not a form control, so <label>
            was the wrong element: screen readers announce a label that
            leads nowhere. A heading tied to the block by aria-labelledby
            conveys the same thing and is actually navigable.
          -->
          <div
            id="audit-json-label"
            class="block text-xs font-semibold text-muted-foreground uppercase mb-1.5"
          >
            {t('audit.jsonLabel')}
          </div>
          <pre
            aria-labelledby="audit-json-label"
            class="p-4 rounded-lg bg-background font-mono text-xs text-foreground/90 border border-border overflow-x-auto max-h-64 leading-relaxed">{JSON.stringify(
              detailModalEntry.Detail || detailModalEntry,
              null,
              2
            )}</pre>
        </div>
      </div>

      <Dialog.Footer>
        <Button onclick={() => (detailModalEntry = null)}>{t('common.close')}</Button>
      </Dialog.Footer>
    {/if}
  </Dialog.Content>
</Dialog.Root>
