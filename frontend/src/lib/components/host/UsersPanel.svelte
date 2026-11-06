<script>
  import { t } from '$lib/i18n.svelte.js';
  import Icon from '$lib/components/Icon.svelte';

  let { users = [] } = $props();

  function formatDuration(seconds) {
    if (!seconds) return '—';
    const h = Math.floor(seconds / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
  }

  function formatLoginTime(timeStr) {
    if (!timeStr) return '—';
    return timeStr;
  }
</script>

<div class="space-y-3">
  {#if users.length === 0}
    <p class="text-sm text-muted-foreground text-center py-4">
      {t('host.noUsersLoggedIn')}
    </p>
  {:else}
    <div class="overflow-x-auto border border-border rounded-lg">
      <table class="w-full text-xs">
        <thead class="bg-muted/50">
          <tr class="text-left text-muted-foreground border-b border-border">
            <th class="px-3 py-2 font-medium">{t('host.user')}</th>
            <th class="px-3 py-2 font-medium">{t('host.terminal')}</th>
            <th class="px-3 py-2 font-medium">{t('host.from')}</th>
            <th class="px-3 py-2 font-medium">{t('host.loginTime')}</th>
            <th class="px-3 py-2 font-medium">{t('host.duration')}</th>
          </tr>
        </thead>
        <tbody class="font-mono">
          {#each users as user}
            <tr class="border-b border-border/50 hover:bg-muted/30">
              <td class="px-3 py-2">
                <div class="flex items-center gap-2">
                  <Icon name="user" size={12} class="text-muted-foreground" />
                  <span class="font-medium">{user.name}</span>
                </div>
              </td>
              <td class="px-3 py-2">{user.terminal}</td>
              <td class="px-3 py-2 truncate max-w-[150px]" title={user.from}>
                {user.from || '—'}
              </td>
              <td class="px-3 py-2">{formatLoginTime(user.login_time)}</td>
              <td class="px-3 py-2 tnum">{formatDuration(user.duration_sec)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
