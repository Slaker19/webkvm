<script>
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Chart from '$lib/components/Chart.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';

  let { vmId, vmName = '' } = $props();

  let historyWindow = $state('24h');
  let history = $state(null);
  let historyLoading = $state(false);

  const historyCpu = $derived(history?.cpu?.points || []);
  const historyRam = $derived(history?.ram?.points || []);
  const historyDiskR = $derived(history?.disk_read?.points || []);
  const historyDiskW = $derived(history?.disk_write?.points || []);
  const historyNetRx = $derived(history?.net_rx?.points || []);
  const historyNetTx = $derived(history?.net_tx?.points || []);

  let vmLogs = $state('');
  let vmLogsLoading = $state(false);
  let vmLogsLines = $state(200);

  export async function loadHistory() {
    if (!vmId) return;
    historyLoading = true;
    try {
      history = await api.getVMMetricsHistory(vmId, historyWindow);
    } catch (err) {
      toast.error(err.message);
    } finally {
      historyLoading = false;
    }
  }

  export async function loadVMLogs() {
    if (!vmId) return;
    vmLogsLoading = true;
    try {
      const res = await api.getVMLogs(vmId, vmLogsLines);
      const text = res?.logs || '';
      vmLogs = /^\(No hypervisor log file found/.test(text) ? '' : text;
    } catch (e) {
      vmLogs = `(${t('common.error')}: ${e.message})`;
    } finally {
      vmLogsLoading = false;
    }
  }

  function downloadVMLogs() {
    if (!vmLogs) return;
    const blob = new Blob([vmLogs], { type: 'text/plain;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${vmName || vmId}-qemu.log`;
    a.click();
    URL.revokeObjectURL(url);
  }

  onMount(() => {
    loadHistory();
    loadVMLogs();
  });
</script>

<BlockCard bid="history" title={t('vmDetail.historyTitle')} anchor="history">
  <div class="flex items-center justify-between mb-3 gap-3 flex-wrap">
    <span class="text-xs text-muted-foreground">{t('vmDetail.historyDesc')}</span>
    <div class="flex items-center gap-1">
      {#each ['24h', '168h', '720h'] as w (w)}
        <Button
          size="xs"
          variant={historyWindow === w ? 'default' : 'outline'}
          onclick={() => {
            historyWindow = w;
            loadHistory();
          }}
          >{w === '24h'
            ? t('vmDetail.hist24h')
            : w === '168h'
              ? t('vmDetail.hist7d')
              : t('vmDetail.hist30d')}</Button
        >
      {/each}
    </div>
  </div>
  {#if historyLoading}
    <div class="flex justify-center py-10"><Spinner size="lg" /></div>
  {:else if historyCpu.length === 0 && historyRam.length === 0}
    <p class="text-sm text-muted-foreground">{t('vmDetail.historyEmpty')}</p>
  {:else}
    <div class="grid grid-cols-1 md:grid-cols-2 gap-5">
      <div>
        <div class="flex items-baseline justify-between mb-1.5">
          <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
            >{t('vms.cpu')}</span
          >
          <span class="text-sm tnum"
            >{historyCpu.length
              ? historyCpu[historyCpu.length - 1].v.toFixed(1)
              : '0.0'}%</span
          >
        </div>
        <Chart points={historyCpu} yMax={100} height={80} />
      </div>
      <div>
        <div class="flex items-baseline justify-between mb-1.5">
          <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
            >{t('common.ram')}</span
          >
          <span class="text-sm tnum"
            >{historyRam.length
              ? historyRam[historyRam.length - 1].v.toFixed(1)
              : '0.0'}%</span
          >
        </div>
        <Chart
          points={historyRam}
          yMax={100}
          height={80}
          color="var(--info, var(--accent))"
        />
      </div>
      <div>
        <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
          >{t('vmDetail.diskIo')}</span
        >
        <Chart
          points={[...historyDiskR, ...historyDiskW]}
          height={80}
          color="var(--success)"
        />
      </div>
      <div>
        <span class="text-xs font-medium text-muted-foreground uppercase tracking-wider"
          >{t('vmDetail.netIo')}</span
        >
        <Chart
          points={[...historyNetRx, ...historyNetTx]}
          height={80}
          color="var(--info, var(--accent))"
        />
      </div>
    </div>
  {/if}
</BlockCard>

<BlockCard bid="hypervisor-logs" title={t('vmDetail.hypervisorLogs')}>
  {#snippet headerActions()}
    <div class="flex items-center gap-1.5">
      <select
        bind:value={vmLogsLines}
        onchange={loadVMLogs}
        class="input !h-7 !text-xs !py-0 w-24"
      >
        <option value={50}>50 líneas</option>
        <option value={100}>100 líneas</option>
        <option value={200}>200 líneas</option>
        <option value={500}>500 líneas</option>
        <option value={1000}>1000 líneas</option>
      </select>
      <Button
        variant="outline"
        size="sm"
        class="!h-7 !text-xs"
        onclick={loadVMLogs}
        disabled={vmLogsLoading}
      >
        <Icon
          name="refresh"
          size={12}
          class={vmLogsLoading ? 'animate-spin mr-1' : 'mr-1'}
        />
        {t('common.refresh')}
      </Button>
      <Button
        variant="outline"
        size="sm"
        class="!h-7 !text-xs"
        onclick={() => {
          navigator.clipboard.writeText(vmLogs);
          toast.success(t('common.copied'));
        }}
        disabled={!vmLogs || vmLogsLoading}
      >
        <Icon name="copy" size={12} class="mr-1" />
        {t('common.copy')}
      </Button>
      <Button
        variant="outline"
        size="sm"
        class="!h-7 !text-xs"
        onclick={downloadVMLogs}
        disabled={!vmLogs || vmLogsLoading}
      >
        <Icon name="download" size={12} class="mr-1" />
        {t('common.download')}
      </Button>
    </div>
  {/snippet}

  {#if vmLogsLoading}
    <div class="flex justify-center py-10"><Spinner size="lg" /></div>
  {:else if !vmLogs}
    <p class="text-sm text-muted-foreground">
      {t('vmDetail.noLogs')}
    </p>
  {:else}
    <div
      class="rounded-lg border border-border bg-slate-950 p-3.5 font-mono text-xs text-slate-200 overflow-x-auto max-h-96 overflow-y-auto select-text"
    >
      <pre class="whitespace-pre leading-relaxed">{vmLogs}</pre>
    </div>
  {/if}
</BlockCard>
