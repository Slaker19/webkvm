<script>
  import { t } from '$lib/i18n.svelte.js';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import CronPicker from '$lib/components/CronPicker.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';

  let {
    vmId,
    snapshots = [],
    snapName = $bindable(''),
    snapDesc = $bindable(''),
    snapMemory = $bindable(false),
    actionLoading = '',
    createSnapshot,
    revertSnapshot,
    deleteSnapshot,
    schedSnap = $bindable(''),
    schedSnapMax = $bindable(3),
    schedSaving = false,
    saveSchedule,
  } = $props();

  function bytesToStr(b) {
    if (!b) return '0 B';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    let n = b;
    while (n >= 1024 && i < u.length - 1) {
      n /= 1024;
      i++;
    }
    return n.toFixed(i > 0 ? 1 : 0) + ' ' + u[i];
  }

  function formatSnapshotDate(epoch) {
    if (!epoch) return '—';
    const d = new Date(epoch * 1000);
    const y = d.getFullYear();
    const m = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    const hh = String(d.getHours()).padStart(2, '0');
    const mm = String(d.getMinutes()).padStart(2, '0');
    return `${y}-${m}-${day} ${hh}:${mm}`;
  }

  const snapshotTree = $derived.by(() => {
    if (!Array.isArray(snapshots) || snapshots.length === 0) return { roots: [], byId: {} };
    const byId = {};
    for (const s of snapshots) byId[s.name] = { ...s, children: [] };
    const roots = [];
    for (const k of Object.keys(byId)) {
      const node = byId[k];
      if (node.parent_name && byId[node.parent_name]) {
        byId[node.parent_name].children.push(node);
      } else {
        roots.push(node);
      }
    }
    const sortRec = (nodes) => {
      nodes.sort((a, b) => (a.creation_time || 0) - (b.creation_time || 0));
      nodes.forEach((n) => sortRec(n.children));
    };
    sortRec(roots);
    return { roots, byId };
  });
</script>

{#snippet snapshotNode(node, depth)}
  <div
    class="flex items-center justify-between py-1.5 border-b border-border last:border-0"
    style="padding-left: {depth * 20}px"
  >
    <div class="flex items-center gap-2 min-w-0">
      {#if depth > 0}
        <svg
          class="w-3 h-3 text-muted-foreground/40 shrink-0"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          viewBox="0 0 24 24"><polyline points="9 6 15 12 9 18" /></svg
        >
      {/if}
      <div class="min-w-0">
        <p class="text-sm font-medium truncate">
          {node.name}
          {#if node.current}
            <span
              class="text-[10px] text-success ml-1.5 px-1.5 py-0.5 rounded border border-success/30 bg-success/10 uppercase tracking-wider"
              >{t('vmDetail.current')}</span
            >
          {/if}
        </p>
        <p class="text-xs text-muted-foreground tnum">
          {formatSnapshotDate(node.creation_time)}
          {#if node.size_at_snap_bytes}
            <span class="mx-1.5 text-border">·</span>
            <span class="text-accent">{bytesToStr(node.size_at_snap_bytes)}</span>
            <span class="text-muted-foreground/70">{t('vmDetail.atCreation')}</span>
          {/if}
        </p>
      </div>
    </div>
    <div class="flex gap-1 shrink-0">
      {#if !node.current}
        <button
          onclick={() => revertSnapshot(node.id)}
          class="text-xs text-warning hover:text-warning px-2 py-1 rounded hover:bg-warning/10"
          >{t('vmDetail.revert')}</button
        >
      {/if}
      <button
        onclick={() => deleteSnapshot(node.id)}
        class="text-xs text-muted-foreground hover:text-destructive px-2 py-1 rounded hover:bg-destructive/10"
        >{t('common.delete')}</button
      >
    </div>
  </div>
  {#each node.children as child (child.name)}
    {@render snapshotNode(child, depth + 1)}
  {/each}
{/snippet}

<BlockCard bid="snaps" title={t('vmDetail.snapshots')} anchor="snapshots">
  <div class="flex gap-2 mb-3">
    <Input bind:value={snapName} placeholder="Snapshot name" class="flex-1" />
    <Button onclick={createSnapshot} disabled={!snapName || actionLoading === 'snapshot'}>
      {#if actionLoading === 'snapshot'}<Spinner size="xs" color="text-white" />{:else}{t(
          'vmDetail.createSnapshot'
        )}{/if}
    </Button>
  </div>
  <label
    class="flex items-center gap-2 text-sm text-muted-foreground mb-3 cursor-pointer select-none"
  >
    <input
      type="checkbox"
      bind:checked={snapMemory}
      class="w-4 h-4 rounded border-border"
    />
    {t('vmDetail.snapshotWithMemory')}
  </label>
  {#if snapshots.length === 0}
    <EmptyState compact icon="camera" title={t('vmDetail.noSnapshots')} />
  {:else}
    <div class="space-y-0.5">
      {#each snapshotTree.roots as root (root.name)}
        {@render snapshotNode(root, 0)}
      {/each}
    </div>
  {/if}

  <!-- Automated Snapshot Policy -->
  <div class="mt-4 pt-3 border-t border-border">
    <div class="flex items-center justify-between mb-2">
      <div class="flex items-center gap-2">
        <Icon name="clock" size={13} class="text-accent" />
        <span class="text-xs font-semibold text-foreground">
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
        <label for="sched-snap-max-snap" class="text-xs text-muted-foreground block">
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
            id="sched-snap-max-snap"
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
    <div class="mt-3 flex justify-end">
      <Button size="xs" onclick={saveSchedule} disabled={schedSaving}>
        {#if schedSaving}<Spinner size="xs" color="text-white" />{:else}{t(
            'common.save'
          )}{/if}
      </Button>
    </div>
  </div>
</BlockCard>
