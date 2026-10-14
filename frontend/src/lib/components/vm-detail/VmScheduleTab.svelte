<script>
  import { t } from '$lib/i18n.svelte.js';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import CronPicker from '$lib/components/CronPicker.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';

  let {
    vmId,
    schedStart = $bindable(''),
    schedStop = $bindable(''),
    schedSnap = $bindable(''),
    schedSnapMax = $bindable(3),
    schedSaving = $bindable(false),
    saveSchedule,
  } = $props();
</script>

<BlockCard bid="schedule" title={t('vmDetail.scheduleTitle')} anchor="schedule">
  <div class="space-y-4 mb-4">
    <!-- Encendido Programado -->
    <div class="p-4 rounded-xl border border-border bg-card/40 space-y-3">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <Icon name="play" size={14} class="text-success" />
          <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
            {t('vmDetail.scheduleStart')}
          </span>
        </div>
        <Switch
          size="sm"
          checked={schedStart !== ''}
          onchange={(v) => (schedStart = v ? '0 8 * * 1-5' : '')}
        />
      </div>
      {#if !schedStart}
        <p class="text-xs text-muted-foreground py-1">
          {t('vmDetail.noActiveSchedule')}
        </p>
      {:else}
        <CronPicker bind:expression={schedStart} />
      {/if}
    </div>

    <!-- Apagado Programado -->
    <div class="p-4 rounded-xl border border-border bg-card/40 space-y-3">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <Icon name="power" size={14} class="text-destructive" />
          <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
            {t('vmDetail.scheduleStop')}
          </span>
        </div>
        <Switch
          size="sm"
          checked={schedStop !== ''}
          onchange={(v) => (schedStop = v ? '0 20 * * 1-5' : '')}
        />
      </div>
      {#if !schedStop}
        <p class="text-xs text-muted-foreground py-1">
          {t('vmDetail.noActiveSchedule')}
        </p>
      {:else}
        <CronPicker bind:expression={schedStop} />
      {/if}
    </div>

    <!-- Instantáneas Automáticas -->
    <div class="p-4 rounded-xl border border-border bg-card/40 space-y-3">
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-2">
          <Icon name="camera" size={14} class="text-accent" />
          <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
            {t('vmDetail.snapScheduleTitle')}
          </span>
        </div>
        <Switch
          size="sm"
          checked={schedSnap !== ''}
          onchange={(v) => (schedSnap = v ? '0 2 * * *' : '')}
        />
      </div>
      {#if !schedSnap}
        <p class="text-xs text-muted-foreground py-1">
          {t('vmDetail.noSnapshotsScheduled')}
        </p>
      {:else}
        <CronPicker bind:expression={schedSnap} />
        <div class="pt-3 border-t border-border/50 space-y-2">
          <label for="sched-snap-max-tab" class="text-xs text-muted-foreground block">
            {t('vmDetail.snapMaxKeep')}
          </label>
          <div class="flex flex-wrap items-center gap-2">
            <select
              value={schedSnapMax}
              onchange={(e) => (schedSnapMax = Number(e.target.value) || 3)}
              class="input !h-8 text-xs w-auto"
            >
              <option value={1}>1</option>
              <option value={3}>3</option>
              <option value={5}>5</option>
              <option value={7}>7</option>
              <option value={14}>14</option>
              <option value={30}>30</option>
            </select>
            <span class="text-xs text-muted-foreground">·</span>
            <Input
              id="sched-snap-max-tab"
              type="number"
              min="1"
              max="999"
              bind:value={schedSnapMax}
              placeholder={t('vmDetail.retentionCustom')}
              class="!h-8 w-24 tnum text-xs"
            />
            <span class="text-xs text-muted-foreground">
              {t('vmDetail.retentionCount', { n: schedSnapMax }) ||
                `${schedSnapMax} instantáneas`}
            </span>
          </div>
        </div>
      {/if}
    </div>
  </div>

  <p class="text-xs text-muted-foreground mb-3">{t('vmDetail.scheduleHint')}</p>
  <Button size="sm" onclick={saveSchedule} disabled={schedSaving}>
    {#if schedSaving}<Spinner size="xs" color="text-white" />{:else}{t(
        'common.save'
      )}{/if}
  </Button>
</BlockCard>
