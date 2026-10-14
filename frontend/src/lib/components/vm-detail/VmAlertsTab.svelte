<script>
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';

  let { vmId } = $props();

  let alertRules = $state([]);
  let alertStatuses = $state([]);
  let alertsLoading = $state(false);

  export async function loadAlerts() {
    if (!vmId) return;
    alertsLoading = true;
    try {
      const r = await api.getVMAlerterRules(vmId);
      alertRules = (r.rules || []).map((x) => ({ ...x }));
      alertStatuses = r.statuses || [];
    } catch (err) {
      toast.error(err.message);
    } finally {
      alertsLoading = false;
    }
  }

  function newAlertRule() {
    return {
      id: `al_${Date.now().toString(36)}`,
      name: '',
      metric: 'cpu',
      threshold: 90,
      above: true,
      duration_secs: 300,
      cooldown_secs: 3600,
      enabled: true,
    };
  }

  function ruleStatus(ruleId) {
    const s = alertStatuses.find((x) => x.rule?.id === ruleId);
    return s?.state || 'idle';
  }

  async function saveAlertRules() {
    try {
      await api.setVMAlerterRules(vmId, alertRules);
      toast.success(t('vmDetail.alertsSaved'));
      await loadAlerts();
    } catch (err) {
      toast.error(err.message);
    }
  }

  onMount(() => {
    loadAlerts();
  });
</script>

<BlockCard bid="alerts" title={t('vmDetail.alertsTitle')} anchor="alerts">
  <p class="text-xs text-muted-foreground mb-3">{t('vmDetail.alertsDesc')}</p>
  {#if alertsLoading}
    <div class="flex justify-center py-10"><Spinner size="lg" /></div>
  {:else}
    <div class="space-y-2 mb-4">
      {#each alertRules as rule (rule.id)}
        <div
          class="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-muted/20 p-2.5"
        >
          <select
            class="h-8 rounded-lg border border-border bg-background px-1 text-sm w-24"
            bind:value={rule.metric}
          >
            <option value="cpu">CPU</option>
            <option value="ram">RAM</option>
            <option value="disk_r">Disk R</option>
            <option value="disk_w">Disk W</option>
            <option value="net_rx">Net RX</option>
            <option value="net_tx">Net TX</option>
          </select>
          <select
            class="h-8 rounded-lg border border-border bg-background px-1 text-sm w-20"
            bind:value={rule.above}
          >
            <option value={true}>{t('vmDetail.alertAbove')}</option>
            <option value={false}>{t('vmDetail.alertBelow')}</option>
          </select>
          <Input
            type="number"
            class="h-8 w-24 tnum"
            bind:value={rule.threshold}
            min="0"
          />
          <span class="text-xs text-muted-foreground">{t('vmDetail.alertFor')}</span>
          <Input
            type="number"
            class="h-8 w-20 tnum"
            bind:value={rule.duration_secs}
            min="1"
          />
          <span class="text-xs text-muted-foreground">{t('vmDetail.alertSecs')}</span>
          {#if ruleStatus(rule.id)}
            <span
              class="text-xs px-2 py-0.5 rounded-md {ruleStatus(rule.id) === 'firing'
                ? 'bg-destructive/15 text-destructive'
                : ruleStatus(rule.id) === 'pending'
                  ? 'bg-warning/15 text-warning'
                  : 'bg-muted/50 text-muted-foreground'}"
              >{t('vmDetail.alertState' + ruleStatus(rule.id))}</span
            >
          {/if}
          <Button
            size="icon"
            variant="ghost"
            class="text-destructive"
            onclick={() => (alertRules = alertRules.filter((x) => x.id !== rule.id))}
            aria-label={t('common.delete')}>×</Button
          >
        </div>
      {/each}
      {#if alertRules.length === 0}
        <EmptyState compact icon="bellOff" title={t('vmDetail.alertsEmpty')} />
      {/if}
    </div>
    <div class="flex items-center gap-2">
      <Button
        size="sm"
        variant="outline"
        onclick={() => (alertRules = [...alertRules, newAlertRule()])}
      >
        {t('vmDetail.addAlertRule')}
      </Button>
      <Button size="sm" onclick={saveAlertRules} disabled={alertsLoading}>
        {t('common.save')}
      </Button>
    </div>
  {/if}
</BlockCard>
