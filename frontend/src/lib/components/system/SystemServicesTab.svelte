<script>
  import { t } from '$lib/i18n.svelte.js';
  import { Card } from '$lib/components/ui/card';
  import StatCard from '$lib/components/StatCard.svelte';

  /**
   * @typedef {Object} Props
   * @property {any} status - System status from /api/system/status
   * @property {any} capabilities - Capabilities store from stores/capabilities.svelte.js
   */
  let { status, capabilities } = $props();

  function fmtBytes(n) {
    if (!n && n !== 0) return '—';
    const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    let i = 0;
    let v = n;
    while (v >= 1024 && i < u.length - 1) {
      v /= 1024;
      i++;
    }
    return `${v.toFixed(1)} ${u[i]}`;
  }

  function fmtUptime(sec) {
    if (!sec) return '—';
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    const m = Math.floor((sec % 3600) / 60);
    if (d > 0) return `${d}d ${h}h ${m}m`;
    if (h > 0) return `${h}h ${m}m`;
    return `${m}m`;
  }
</script>

{#if status?.backend && status?.host && status?.disk && status?.load}
  <!-- System status stat cards (from original Status page) -->
  <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3 mb-4">
    <StatCard
      label={t('status.backend')}
      status={status.backend?.version ? 'running' : 'crashed'}
      value={status.backend?.version
        ? status.backend.version.startsWith('v')
          ? status.backend.version
          : `v${status.backend.version}`
        : '—'}
      hint={t('status.uptimeHint', { uptime: fmtUptime(status.uptime_sec) })}
    />
    <StatCard
      label={t('status.libvirt')}
      status={status.libvirt?.connected ? 'running' : 'crashed'}
      value={status.libvirt?.uri || '—'}
      hint={status.libvirt?.connected ? t('status.connected') : t('status.disconnected')}
    />
    <StatCard
      label={t('status.host')}
      status="running"
      value={status.host?.hostname || '—'}
      hint={status.host?.os || ''}
    />
    <StatCard
      label={t('status.update')}
      status={status.update_available ? 'paused' : 'running'}
      value={status.update_available
        ? t('status.updateAvailable', { version: status.latest_version })
        : t('status.upToDate')}
      hint={t('status.currentVersion', {
        version: status.backend?.version ? status.backend.version.replace(/^v/, '') : '',
      })}
    />
  </div>

  <!-- Aggregate disk + load/uptime -->
  <div class="grid grid-cols-1 md:grid-cols-2 gap-3 mb-4">
    <StatCard
      label={t('status.diskTitle')}
      status={status.disk?.used_pct > 90
        ? 'crashed'
        : status.disk?.used_pct > 75
          ? 'paused'
          : 'running'}
      value={`${fmtBytes(status.disk?.used_bytes || 0)} / ${fmtBytes(status.disk?.total_bytes || 0)}`}
      hint={`${(status.disk?.used_pct || 0).toFixed(1)}%`}
    />
    <StatCard
      label={t('status.loadTitle')}
      status="running"
      value={`${(status.load?.load1 || 0).toFixed(2)} ${(status.load?.load5 || 0).toFixed(2)} ${(status.load?.load15 || 0).toFixed(2)}`}
      hint={t('status.hostUptime', { uptime: fmtUptime(status.host_uptime_sec) })}
    />
  </div>

  <!-- System services -->
  <Card class="p-5 mb-4">
    <h2 class="text-sm font-semibold mb-3">{t('status.servicesTitle')}</h2>
    <div class="space-y-2">
      {#each status.services as svc (svc)}
        <div
          class="flex items-center justify-between text-sm border-b border-border/50 pb-2 last:border-0 last:pb-0"
        >
          <div class="min-w-0">
            <div class="font-medium truncate">
              {svc.key === 'libvirt'
                ? t('status.serviceLibvirt')
                : svc.key === 'incus'
                  ? t('status.serviceIncus')
                  : svc.key.startsWith('dnsmasq:')
                    ? t('status.servicesDnsmasqBridge', { bridge: svc.key.slice(8) })
                    : svc.unit}
            </div>
            <div class="text-xs text-muted-foreground truncate">
              {svc.description || svc.unit}
            </div>
          </div>
          <span
            class="shrink-0 inline-flex items-center gap-1 text-[10px] px-1.5 py-0.5 rounded-full font-medium {!svc.found
              ? 'bg-muted text-muted-foreground'
              : svc.active
                ? 'bg-success/10 text-success'
                : 'bg-destructive/10 text-destructive'}"
          >
            <span
              class="w-1.5 h-1.5 rounded-full {!svc.found
                ? 'bg-muted-foreground'
                : svc.active
                  ? 'bg-success'
                  : 'bg-destructive'}"
            ></span>
            {!svc.found
              ? t('status.serviceNotFound')
              : svc.active
                ? t('status.serviceRunning')
                : t('status.serviceStopped')}
          </span>
        </div>
      {:else}
        <p class="text-sm text-muted-foreground">{t('status.noServices')}</p>
      {/each}
    </div>
  </Card>

  <!-- Hypervisor platform -->
  <Card class="p-5 mb-4">

    <h2 class="text-sm font-semibold mb-3">{t('status.platformTitle')}</h2>
    <dl class="text-sm space-y-1.5">
      <div class="flex justify-between gap-2">
        <dt class="text-muted-foreground shrink-0">{t('status.platformKernel')}</dt>
        <dd class="font-mono truncate text-right">{status.platform.kernel || '—'}</dd>
      </div>
      <div class="flex justify-between gap-2">
        <dt class="text-muted-foreground shrink-0">{t('status.platformQemu')}</dt>
        <dd class="font-mono truncate text-right">{status.platform.qemu_version || '—'}</dd>
      </div>
      <div class="flex justify-between gap-2">
        <dt class="text-muted-foreground shrink-0">{t('status.platformLibvirt')}</dt>
        <dd class="font-mono truncate text-right">{status.platform.libvirt_version || '—'}</dd>
      </div>
      {#if status.platform.incus_enabled}
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.platformIncus')}</dt>
          <dd class="font-mono truncate text-right">{status.platform.incus_version || '—'}</dd>
        </div>
      {/if}
      <div class="flex justify-between gap-2">
        <dt class="text-muted-foreground shrink-0">{t('status.platformNested')}</dt>
        <dd class="text-right">
          {status.platform.nested_virt.supported
            ? t('status.platformEnabled')
            : t('status.platformDisabled')}
          {#if status.platform.nested_virt.vendor && status.platform.nested_virt.vendor !== 'unknown'}
            <span class="text-muted-foreground">({status.platform.nested_virt.vendor})</span>
          {/if}
        </dd>
      </div>
      <div class="flex justify-between gap-2">
        <dt class="text-muted-foreground shrink-0">{t('status.platformIommu')}</dt>
        <dd class="text-right">
          {#if status.platform.iommu.enabled}
            {t('status.platformGroups', { n: status.platform.iommu.groups })}
            {#if status.platform.iommu.assignable !== undefined}
              <span class="text-muted-foreground text-xs ml-1">
                ({status.platform.iommu.assignable}
                {t('vmDetail.pciAvailable').toLowerCase()})
              </span>
            {/if}
          {:else}
            {t('status.platformDisabled')}
          {/if}
        </dd>
      </div>
    </dl>
  </Card>

  <!-- Device capabilities -->
  {#if capabilities?.loaded}
    <Card class="p-5 mb-4">
      <div class="flex items-center justify-between mb-3">
        <h2 class="text-sm font-semibold">{t('status.deviceCaps')}</h2>
        {#if !capabilities.parsed}
          <span class="text-xs text-warning">{t('status.deviceCapsUnprobed')}</span>
        {/if}
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-2 text-sm">
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.deviceCapsVideo')}</dt>
          <dd class="font-mono text-right text-xs">
            {capabilities.videoModels.join(', ') || '—'}
          </dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.deviceCapsNetwork')}</dt>
          <dd class="font-mono text-right text-xs">
            {capabilities.networkModels.join(', ') || '—'}
          </dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.deviceCapsSound')}</dt>
          <dd class="font-mono text-right text-xs">
            {capabilities.soundModels.join(', ') || '—'}
          </dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">{t('status.deviceCapsDiskBus')}</dt>
          <dd class="font-mono text-right text-xs">
            {capabilities.diskBuses.join(', ') || '—'}
          </dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">SPICE</dt>
          <dd
            class="text-right text-xs {capabilities.spiceSupported
              ? 'text-success'
              : 'text-muted-foreground'}"
          >
            {capabilities.spiceSupported
              ? t('status.deviceCapsSpiceOn')
              : t('status.deviceCapsSpiceOff')}
          </dd>
        </div>
        <div class="flex justify-between gap-2">
          <dt class="text-muted-foreground shrink-0">QXL</dt>
          <dd
            class="text-right text-xs {capabilities.videoModels.includes('qxl')
              ? 'text-success'
              : 'text-muted-foreground'}"
          >
            {capabilities.videoModels.includes('qxl')
              ? t('status.deviceCapsQxlOn')
              : t('status.deviceCapsQxlOff')}
          </dd>
        </div>
      </div>
      {#if !capabilities.spiceSupported || !capabilities.videoModels.includes('qxl')}
        <p class="text-xs text-muted-foreground mt-3 pt-3 border-t border-border">
          {t('status.deviceCapsHint')}
        </p>
      {/if}
    </Card>
  {/if}

  <!-- Storage pools -->
  <Card class="p-5 mb-4">
    <h2 class="text-sm font-semibold mb-3">{t('status.storagePools')}</h2>
    <div class="space-y-3">
      {#each status.pools as p (p)}
        <div>
          <div class="flex items-center justify-between text-sm mb-1">
            <span class="font-medium">{p.name}</span>
            <span class="text-muted-foreground text-xs tnum"
              >{fmtBytes(p.used_bytes)} / {fmtBytes(p.total_bytes)} ({p.used_pct.toFixed(
                1
              )}%)</span
            >
          </div>
          <div class="h-2 bg-muted rounded-full overflow-hidden">
            <div
              class="h-full transition-all {p.used_pct > 90
                ? 'bg-destructive'
                : p.used_pct > 75
                  ? 'bg-warning'
                  : 'bg-success'}"
              style="width: {Math.min(100, p.used_pct)}%"
            ></div>
          </div>
          <div class="text-xs text-muted-foreground mt-1 font-mono">{p.path}</div>
        </div>
      {/each}
    </div>
  </Card>
{/if}
