<script>
  import { t } from '$lib/i18n.svelte.js';
  import Icon from '$lib/components/Icon.svelte';

  let { system } = $props();

  function formatUptime(seconds) {
    if (!seconds) return '—';
    const d = Math.floor(seconds / 86400);
    const h = Math.floor((seconds % 86400) / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    if (d > 0) return `${d}d ${h}h ${m}m`;
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
  }
</script>

<div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
  <div class="rounded-lg border border-border bg-card p-3">
    <div class="flex items-center gap-2 mb-1">
      <Icon name="server" size={14} class="text-muted-foreground" />
      <span class="text-xs text-muted-foreground font-medium uppercase tracking-wider">
        {t('host.hostname')}
      </span>
    </div>
    <div class="text-sm font-mono font-medium truncate">{system?.hostname || '—'}</div>
  </div>

  <div class="rounded-lg border border-border bg-card p-3">
    <div class="flex items-center gap-2 mb-1">
      <Icon name="monitor" size={14} class="text-muted-foreground" />
      <span class="text-xs text-muted-foreground font-medium uppercase tracking-wider">
        {t('host.os')}
      </span>
    </div>
    <div class="text-sm font-mono truncate">{system?.os || '—'}</div>
    <div class="text-xs text-muted-foreground">{system?.arch || ''}</div>
  </div>

  <div class="rounded-lg border border-border bg-card p-3">
    <div class="flex items-center gap-2 mb-1">
      <Icon name="cpu" size={14} class="text-muted-foreground" />
      <span class="text-xs text-muted-foreground font-medium uppercase tracking-wider">
        {t('host.kernel')}
      </span>
    </div>
    <div class="text-sm font-mono truncate">{system?.kernel || '—'}</div>
  </div>

  <div class="rounded-lg border border-border bg-card p-3">
    <div class="flex items-center gap-2 mb-1">
      <Icon name="clock" size={14} class="text-muted-foreground" />
      <span class="text-xs text-muted-foreground font-medium uppercase tracking-wider">
        {t('host.uptime')}
      </span>
    </div>
    <div class="text-sm font-mono">{formatUptime(system?.uptime_sec)}</div>
  </div>
</div>
