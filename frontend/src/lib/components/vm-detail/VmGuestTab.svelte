<script>
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { formatBytes } from '$lib/utils/format.js';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Gauge from '$lib/components/Gauge.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';

  let { vmId, vm = null } = $props();

  let guestInfo = $state(null);
  let guestLoading = $state(false);
  let actionLoading = $state('');

  let guestCommand = $state('');
  let guestExecRunning = $state(false);
  let guestExecResult = $state(null);
  let guestFreezeStatus = $state('thawed');
  let guestFreezeLoading = $state(false);
  let guestSyncingClock = $state(false);

  export async function loadGuestInfo() {
    if (!vmId) return;
    guestLoading = true;
    try {
      guestInfo = await api.getGuestInfo(vmId);
    } catch (e) {
      guestInfo = { available: false, error: e.message };
    } finally {
      guestLoading = false;
    }
  }

  async function runGuestFSTrim() {
    if (!vm || vm.state !== 'running') return;
    actionLoading = 'fstrim';
    try {
      const res = await api.guestFSTrim(vm.id);
      const totalTrimmed = (res.paths || []).reduce((acc, p) => acc + (p.trimmed || 0), 0);
      toast.success(t('vmDetail.fstrimSuccess', { size: formatBytes(totalTrimmed) }));
      await loadGuestInfo();
    } catch (e) {
      toast.error(e.message || t('vmDetail.fstrimError'));
    } finally {
      actionLoading = '';
    }
  }

  async function runGuestExec(cmd) {
    const toRun = (cmd || guestCommand || '').trim();
    if (!toRun || !vm || vm.state !== 'running') return;
    guestExecRunning = true;
    try {
      const res = await api.guestExec(vm.id, { command: toRun });
      guestExecResult = res;
    } catch (e) {
      toast.error(e.message);
    } finally {
      guestExecRunning = false;
    }
  }

  async function handleGuestSyncTime() {
    if (!vm || vm.state !== 'running') return;
    guestSyncingClock = true;
    try {
      await api.guestSyncTime(vm.id);
      toast.success(t('vmDetail.guestClockSynced'));
    } catch (e) {
      toast.error(e.message);
    } finally {
      guestSyncingClock = false;
    }
  }

  async function handleGuestFSFreeze(freeze) {
    if (!vm || vm.state !== 'running') return;
    guestFreezeLoading = true;
    try {
      const res = await api.guestFSFreeze(vm.id, freeze);
      guestFreezeStatus = freeze ? 'frozen' : 'thawed';
      if (freeze) {
        toast.success(t('vmDetail.guestFreezeSuccess', { count: res.count }));
      } else {
        toast.success(t('vmDetail.guestThawSuccess', { count: res.count }));
      }
    } catch (e) {
      toast.error(e.message);
    } finally {
      guestFreezeLoading = false;
    }
  }

  onMount(() => {
    loadGuestInfo();
  });
</script>

<BlockCard bid="guest" title={t('vmDetail.guestTab')} anchor="guest">
  {#snippet headerActions()}
    <div class="flex items-center gap-1.5">
      {#if vm?.state === 'running' && guestInfo?.available}
        <Button
          size="xs"
          variant="outline"
          onclick={handleGuestSyncTime}
          disabled={guestSyncingClock}
          title={t('vmDetail.guestSyncClock')}
        >
          {#if guestSyncingClock}<Spinner size="xs" />{:else}<Icon
              name="clock"
              size={13}
              class="mr-1"
            />{t('vmDetail.guestSyncClock')}{/if}
        </Button>
        <Button
          size="xs"
          variant={guestFreezeStatus === 'frozen' ? 'destructive' : 'outline'}
          onclick={() => handleGuestFSFreeze(guestFreezeStatus !== 'frozen')}
          disabled={guestFreezeLoading}
          title={guestFreezeStatus === 'frozen'
            ? t('vmDetail.guestThawBtn')
            : t('vmDetail.guestFreezeBtn')}
        >
          {#if guestFreezeLoading}<Spinner size="xs" />{:else}<Icon
              name="shield"
              size={13}
              class="mr-1"
            />{guestFreezeStatus === 'frozen'
              ? t('vmDetail.guestThawBtn')
              : t('vmDetail.guestFreezeBtn')}{/if}
        </Button>
        <Button
          size="xs"
          variant="outline"
          onclick={runGuestFSTrim}
          disabled={actionLoading === 'fstrim'}
          title={t('vmDetail.fstrimDesc')}
        >
          {#if actionLoading === 'fstrim'}<Spinner size="xs" />{:else}<Icon
              name="sparkles"
              size={13}
              class="mr-1"
            />{t('vmDetail.fstrimBtn')}{/if}
        </Button>
      {/if}
      <Button size="xs" variant="outline" onclick={loadGuestInfo} disabled={guestLoading}>
        {#if guestLoading}<Spinner size="xs" />{:else}{t('common.refresh')}{/if}
      </Button>
    </div>
  {/snippet}

  {#if guestLoading && !guestInfo}
    <div class="flex justify-center py-10"><Spinner size="lg" /></div>
  {:else if !guestInfo?.available}
    <div class="rounded-lg border border-border bg-muted/20 p-4 text-center">
      <Icon name="info" size={20} class="mx-auto mb-2 text-muted-foreground" />
      <p class="text-sm text-muted-foreground mb-1">
        {guestInfo?.error || t('vmDetail.guestUnavailable')}
      </p>
      <p class="text-xs text-muted-foreground">{t('vmDetail.guestInstallHint')}</p>
      <code
        class="mt-2 inline-block text-left p-2 rounded bg-muted font-mono text-[11px] select-all"
      >
        sudo apt install qemu-guest-agent && sudo systemctl enable --now qemu-guest-agent
      </code>
    </div>
  {:else}
    <!-- OS identity -->
    {#if guestInfo.os || guestInfo.hostname || guestInfo.timezone}
      <div class="mb-4 grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-1.5 text-sm">
        {#if guestInfo.os?.pretty_name || guestInfo.os?.name}
          <div class="flex gap-2">
            <span class="text-muted-foreground shrink-0">{t('vmDetail.guestOs')}</span>
            <span class="truncate">{guestInfo.os.pretty_name || guestInfo.os.name}</span>
          </div>
        {/if}
        {#if guestInfo.hostname}
          <div class="flex gap-2">
            <span class="text-muted-foreground shrink-0">{t('vmDetail.guestHostname')}</span>
            <span class="font-mono truncate">{guestInfo.hostname}</span>
          </div>
        {/if}
        {#if guestInfo.os?.kernel_release}
          <div class="flex gap-2">
            <span class="text-muted-foreground shrink-0">{t('vmDetail.guestKernel')}</span>
            <span class="font-mono truncate">{guestInfo.os.kernel_release}</span>
          </div>
        {/if}
        {#if guestInfo.os?.machine}
          <div class="flex gap-2">
            <span class="text-muted-foreground shrink-0">{t('vmDetail.guestArch')}</span>
            <span class="font-mono">{guestInfo.os.machine}</span>
          </div>
        {/if}
        {#if guestInfo.timezone?.zone}
          <div class="flex gap-2">
            <span class="text-muted-foreground shrink-0">{t('vmDetail.guestTimezone')}</span>
            <span class="font-mono">{guestInfo.timezone.zone}</span>
          </div>
        {/if}
      </div>
    {/if}

    <!-- Active Guest User Sessions -->
    {#if guestInfo.users?.length}
      <h3 class="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2">
        {t('vmDetail.guestUsers')}
      </h3>
      <div class="flex flex-wrap gap-1.5 mb-4">
        {#each guestInfo.users as u (u.user)}
          <span
            class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded border border-border bg-background text-xs font-mono"
          >
            <Icon name="user" size={13} class="text-muted-foreground" />
            <span>{u.user}</span>
            {#if u.domain}<span class="text-[10px] text-muted-foreground">({u.domain})</span>{/if}
          </span>
        {/each}
      </div>
    {/if}

    <!-- Real in-guest filesystem usage -->
    {#if guestInfo.filesystems?.length}
      <h3 class="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2">
        {t('vmDetail.guestFilesystems')}
      </h3>
      <div class="space-y-2 mb-4">
        {#each guestInfo.filesystems as fs (fs.mountpoint + fs.name)}
          <div class="rounded-md border border-border bg-background p-2.5">
            <div class="flex items-center justify-between gap-2 mb-1.5">
              <div class="flex items-center gap-2 min-w-0">
                <span class="font-mono text-sm font-medium truncate">{fs.mountpoint}</span>
                <span
                  class="text-[10px] px-1.5 py-0.5 rounded border border-border bg-muted text-muted-foreground uppercase"
                  >{fs.type}</span
                >
              </div>
              {#if fs.total_bytes > 0}
                <span class="text-xs text-muted-foreground tnum shrink-0">
                  {formatBytes(fs.used_bytes)} / {formatBytes(fs.total_bytes)}
                </span>
              {/if}
            </div>
            {#if fs.total_bytes > 0}
              <Gauge value={fs.used_pct} variant="linear" showValue={true} label="" />
            {/if}
          </div>
        {/each}
      </div>
    {/if}

    <!-- Every address the guest actually holds -->
    {#if guestInfo.interfaces?.length}
      <h3 class="text-xs font-medium uppercase tracking-wider text-muted-foreground mb-2">
        {t('vmDetail.guestInterfaces')}
      </h3>
      <div class="space-y-1.5">
        {#each guestInfo.interfaces as iface (iface.name)}
          <div
            class="flex items-start justify-between gap-3 px-3 py-2 rounded-md border border-border bg-background"
          >
            <div class="min-w-0">
              <span class="font-mono text-sm font-medium">{iface.name}</span>
              {#if iface.mac}
                <span class="ml-2 text-[11px] font-mono text-muted-foreground">{iface.mac}</span>
              {/if}
            </div>
            <div class="flex flex-col items-end gap-0.5 min-w-0">
              {#each iface.ipv4 || [] as ip (ip)}
                <span class="font-mono text-xs text-accent truncate">{ip}</span>
              {/each}
              {#each iface.ipv6 || [] as ip (ip)}
                <span class="font-mono text-[10px] text-muted-foreground truncate">{ip}</span>
              {/each}
            </div>
          </div>
        {/each}
      </div>
    {/if}

    <!-- In-Guest Execution (guest-exec) Console -->
    <div class="mt-6 pt-4 border-t border-border space-y-3">
      <div class="flex items-center justify-between">
        <div>
          <h3 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            {t('vmDetail.guestConsole')}
          </h3>
          <p class="text-xs text-muted-foreground">
            {t('vmDetail.guestConsoleDesc')}
          </p>
        </div>
        <span class="text-[10px] px-1.5 py-0.5 rounded font-mono bg-accent/10 text-accent">
          guest-exec
        </span>
      </div>

      <!-- Presets -->
      <div class="flex flex-wrap items-center gap-1.5">
        <button
          type="button"
          class="text-[11px] px-2 py-0.5 rounded border border-border bg-muted/40 hover:bg-muted font-mono transition-colors"
          onclick={() => runGuestExec('uptime')}
          disabled={guestExecRunning}
        >
          {t('vmDetail.presetUptime')}
        </button>
        <button
          type="button"
          class="text-[11px] px-2 py-0.5 rounded border border-border bg-muted/40 hover:bg-muted font-mono transition-colors"
          onclick={() => runGuestExec('df -h')}
          disabled={guestExecRunning}
        >
          {t('vmDetail.presetDiskUsage')}
        </button>
        <button
          type="button"
          class="text-[11px] px-2 py-0.5 rounded border border-border bg-muted/40 hover:bg-muted font-mono transition-colors"
          onclick={() => runGuestExec('free -m')}
          disabled={guestExecRunning}
        >
          {t('vmDetail.presetMemUsage')}
        </button>
        <button
          type="button"
          class="text-[11px] px-2 py-0.5 rounded border border-border bg-muted/40 hover:bg-muted font-mono transition-colors"
          onclick={() => runGuestExec('ip a')}
          disabled={guestExecRunning}
        >
          {t('vmDetail.presetIp')}
        </button>
        <button
          type="button"
          class="text-[11px] px-2 py-0.5 rounded border border-border bg-muted/40 hover:bg-muted font-mono transition-colors"
          onclick={() => runGuestExec('cat /etc/os-release 2>/dev/null || systeminfo')}
          disabled={guestExecRunning}
        >
          {t('vmDetail.presetOs')}
        </button>
      </div>

      <!-- Command Input & Run -->
      <form
        onsubmit={(e) => {
          e.preventDefault();
          runGuestExec();
        }}
        class="flex items-center gap-2"
      >
        <Input
          bind:value={guestCommand}
          placeholder={t('vmDetail.guestCommandPlaceholder')}
          class="font-mono text-xs flex-1"
          disabled={guestExecRunning}
        />
        <Button
          type="submit"
          size="sm"
          disabled={guestExecRunning || !guestCommand.trim()}
          class="gap-1.5 shrink-0"
        >
          {#if guestExecRunning}
            <Spinner size="xs" color="text-white" />
            {t('vmDetail.guestRunning')}
          {:else}
            <Icon name="play" size={13} />
            {t('vmDetail.guestRun')}
          {/if}
        </Button>
      </form>

      <!-- Output Box -->
      {#if guestExecResult}
        <div class="rounded-md border border-border bg-black/90 p-3 font-mono text-xs space-y-2">
          <div class="flex items-center justify-between border-b border-border/40 pb-2">
            <div class="flex items-center gap-2">
              <span class="text-muted-foreground">{t('vmDetail.guestExitCode')}:</span>
              <span
                class="px-1.5 py-0.5 rounded text-[10px] font-bold {guestExecResult.exit_code === 0
                  ? 'bg-emerald-500/20 text-emerald-400'
                  : 'bg-destructive/20 text-destructive'}"
              >
                {guestExecResult.exit_code}
              </span>
              {#if !guestExecResult.exited}
                <span class="text-[10px] text-amber-400 font-semibold">(Timeout)</span>
              {/if}
            </div>
            <Button
              size="xs"
              variant="ghost"
              class="!h-6 !text-[10px] text-muted-foreground hover:text-foreground"
              onclick={() => (guestExecResult = null)}
            >
              {t('common.clear')}
            </Button>
          </div>

          {#if guestExecResult.stdout}
            <pre
              class="text-emerald-400 whitespace-pre-wrap overflow-x-auto max-h-60 leading-relaxed">{guestExecResult.stdout}</pre>
          {/if}
          {#if guestExecResult.stderr}
            <pre
              class="text-destructive whitespace-pre-wrap overflow-x-auto max-h-40 leading-relaxed">{guestExecResult.stderr}</pre>
          {/if}
          {#if !guestExecResult.stdout && !guestExecResult.stderr}
            <p class="text-muted-foreground italic text-[11px]">(No output returned)</p>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</BlockCard>
