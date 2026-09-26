<script>
  /**
   * AddScheduleDialog — "create a backup schedule" form, extracted from
   * Backup.svelte. `targets` is owned by the parent (the whole Backup
   * page's data), passed in read-only for the target picker. On success
   * calls `onCreated` so the parent reloads its full state (targets,
   * schedules, jobs) the same way it did before extraction.
   */
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { Input } from './ui/input';
  import CronPicker from './CronPicker.svelte';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from './ui/toast';
  import { api } from '$lib/stores/auth.svelte.js';

  let { open = $bindable(false), targets = [], onCreated = () => {} } = $props();

  let name = $state('');
  let cron = $state('0 2 * * *');
  let targetId = $state('');
  let mode = $state('full');

  function reset() {
    name = '';
    cron = '0 2 * * *';
    targetId = '';
    mode = 'full';
  }

  async function addSchedule() {
    if (!name.trim() || !cron.trim() || !targetId) {
      toast.error(t('backup.nameCronTargetRequired'));
      return;
    }
    try {
      await api.createBackupSchedule({
        name: name.trim(),
        cron: cron.trim(),
        target_id: targetId,
        mode,
      });
      open = false;
      reset();
      await onCreated();
      toast.success(t('backup.scheduleAdded'));
    } catch (err) {
      toast.error(err.message);
    }
  }
</script>

<ConfirmDialog
  {open}
  title={t('backup.addScheduleTitle')}
  message={t('backup.addScheduleMsg')}
  confirmLabel={t('backup.addSchedule')}
  onConfirm={addSchedule}
  onCancel={() => (open = false)}
>
  <div class="space-y-3 min-w-0">
    <div>
      <label class="text-sm font-medium block mb-1" for="add-sched-name">{t('backup.name')}</label>
      <Input
        id="add-sched-name"
        bind:value={name}
        placeholder="e.g. nightly"
        class="w-full min-w-0"
      />
    </div>
    <div>
      <label class="text-sm font-medium block mb-1" for="add-sched-cron"
        >{t('backup.scheduleLabel')}</label
      >
      <CronPicker bind:expression={cron} />
    </div>
    <div>
      <label class="text-sm font-medium block mb-1" for="add-sched-target"
        >{t('backup.target')}</label
      >
      <select
        id="add-sched-target"
        bind:value={targetId}
        class="w-full min-w-0 h-8 rounded-lg border border-border bg-background px-2 text-sm"
      >
        <option value="">{t('backup.pickTarget')}</option>
        {#each targets as target (target)}
          <option value={target.id}>{target.name} ({target.type})</option>
        {/each}
      </select>
    </div>
    <div>
      <label class="text-sm font-medium block mb-1" for="add-sched-mode"
        >{t('backup.modeLabel')}</label
      >
      <select
        id="add-sched-mode"
        bind:value={mode}
        class="w-full min-w-0 h-8 rounded-lg border border-border bg-background px-2 text-sm"
      >
        <option value="full">{t('backup.modeFull')}</option>
        <option value="incremental">{t('backup.modeIncremental')}</option>
      </select>
    </div>
  </div>
</ConfirmDialog>
