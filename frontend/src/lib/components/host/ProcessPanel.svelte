<script>
  import { t } from '$lib/i18n.svelte.js';
  import Icon from '$lib/components/Icon.svelte';
  import { formatBytes } from '$lib/utils/format.js';

  let { processes = [] } = $props();

  let searchQuery = $state('');
  let sortColumn = $state('cpu_percent');
  let sortDescending = $state(true);

  const filteredProcesses = $derived.by(() => {
    let filtered = processes;

    if (searchQuery) {
      const q = searchQuery.toLowerCase();
      filtered = filtered.filter(
        (p) =>
          p.command?.toLowerCase().includes(q) ||
          p.user?.toLowerCase().includes(q) ||
          String(p.pid).includes(q)
      );
    }

    return [...filtered].sort((a, b) => {
      const aVal = a[sortColumn] ?? 0;
      const bVal = b[sortColumn] ?? 0;
      const cmp = typeof aVal === 'string' ? aVal.localeCompare(bVal) : aVal - bVal;
      return sortDescending ? -cmp : cmp;
    });
  });

  function toggleSort(column) {
    if (sortColumn === column) {
      sortDescending = !sortDescending;
    } else {
      sortColumn = column;
      sortDescending = true;
    }
  }

  function getSortIcon(column) {
    if (sortColumn !== column) return 'chevron-up';
    return sortDescending ? 'chevron-down' : 'chevron-up';
  }
</script>

<div class="space-y-3">
  <!-- Search and Controls -->
  <div class="flex items-center gap-3">
    <div class="flex-1 relative">
      <Icon name="search" size={14} class="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground" />
      <input
        type="text"
        placeholder={t('host.searchProcesses')}
        class="w-full pl-9 pr-3 py-2 text-sm border border-border rounded-lg bg-background focus:outline-none focus:ring-2 focus:ring-accent"
        bind:value={searchQuery}
      />
    </div>
    <span class="text-xs text-muted-foreground">
      {filteredProcesses.length} / {processes.length} {t('host.processes')}
    </span>
  </div>

  <!-- Process Table -->
  <div class="overflow-x-auto border border-border rounded-lg">
    <table class="w-full text-xs">
      <thead class="bg-muted/50">
        <tr class="text-left text-muted-foreground border-b border-border">
          <th class="px-3 py-2 font-medium cursor-pointer hover:text-foreground" onclick={() => toggleSort('pid')}>
            <div class="flex items-center gap-1">
              PID
              <Icon name={getSortIcon('pid')} size={10} />
            </div>
          </th>
          <th class="px-3 py-2 font-medium">{t('host.user')}</th>
          <th class="px-3 py-2 font-medium cursor-pointer hover:text-foreground" onclick={() => toggleSort('cpu_percent')}>
            <div class="flex items-center gap-1">
              CPU%
              <Icon name={getSortIcon('cpu_percent')} size={10} />
            </div>
          </th>
          <th class="px-3 py-2 font-medium cursor-pointer hover:text-foreground" onclick={() => toggleSort('memory_percent')}>
            <div class="flex items-center gap-1">
              MEM%
              <Icon name={getSortIcon('memory_percent')} size={10} />
            </div>
          </th>
          <th class="px-3 py-2 font-medium cursor-pointer hover:text-foreground" onclick={() => toggleSort('rss_bytes')}>
            <div class="flex items-center gap-1">
              RSS
              <Icon name={getSortIcon('rss_bytes')} size={10} />
            </div>
          </th>
          <th class="px-3 py-2 font-medium">{t('host.command')}</th>
        </tr>
      </thead>
      <tbody class="font-mono">
        {#each filteredProcesses.slice(0, 50) as proc (proc.pid)}
          <tr class="border-b border-border/50 hover:bg-muted/30">
            <td class="px-3 py-1.5">{proc.pid}</td>
            <td class="px-3 py-1.5">{proc.user}</td>
            <td class="px-3 py-1.5 tnum">
              <span class={proc.cpu_percent > 50 ? 'text-destructive' : proc.cpu_percent > 20 ? 'text-warning' : ''}>
                {proc.cpu_percent.toFixed(1)}
              </span>
            </td>
            <td class="px-3 py-1.5 tnum">
              <span class={proc.memory_percent > 50 ? 'text-destructive' : proc.memory_percent > 20 ? 'text-warning' : ''}>
                {proc.memory_percent.toFixed(1)}
              </span>
            </td>
            <td class="px-3 py-1.5 tnum">{formatBytes(proc.rss_bytes)}</td>
            <td class="px-3 py-1.5 truncate max-w-[200px]" title={proc.command}>{proc.command}</td>
          </tr>
        {:else}
          <tr>
            <td colspan="6" class="px-3 py-4 text-center text-muted-foreground">
              {t('host.noProcesses')}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>

  {#if filteredProcesses.length > 50}
    <p class="text-xs text-muted-foreground text-center">
      {t('host.showingTop', { n: 50 })}
    </p>
  {/if}
</div>
