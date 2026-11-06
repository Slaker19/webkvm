<script>
  import { t } from '$lib/i18n.svelte.js';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Chart from '$lib/components/Chart.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { formatBytes, formatRate } from '$lib/utils/format.js';

  let { networks, summary, history } = $props();

  // Separate physical and virtual interfaces
  const physicalNetworks = $derived((networks || []).filter(n => !n.virtual));
  const virtualNetworks = $derived((networks || []).filter(n => n.virtual));

  // Sort: active first, then alphabetically
  function sortNetworks(nets) {
    return [...nets].sort((a, b) => {
      if (a.state === 'up' && b.state !== 'up') return -1;
      if (a.state !== 'up' && b.state === 'up') return 1;
      return a.name.localeCompare(b.name);
    });
  }

  const sortedPhysical = $derived(sortNetworks(physicalNetworks));
  const sortedVirtual = $derived(sortNetworks(virtualNetworks));

  // Selection state - use localStorage for persistence
  const STORAGE_KEY_PHYSICAL = 'webkvm.network.physical.visible';
  const STORAGE_KEY_VIRTUAL = 'webkvm.network.virtual.visible';

  let showAllPhysical = $state(true);
  let showAllVirtual = $state(true);
  let selectedPhysical = $state(new Set());
  let selectedVirtual = $state(new Set());
  let physicalDropdownOpen = $state(false);
  let virtualDropdownOpen = $state(false);

  // Load from localStorage on mount
  $effect(() => {
    try {
      const savedPhysical = localStorage.getItem(STORAGE_KEY_PHYSICAL);
      if (savedPhysical) {
        const data = JSON.parse(savedPhysical);
        showAllPhysical = data.showAll ?? true;
        selectedPhysical = new Set(data.selected ?? []);
      }
      const savedVirtual = localStorage.getItem(STORAGE_KEY_VIRTUAL);
      if (savedVirtual) {
        const data = JSON.parse(savedVirtual);
        showAllVirtual = data.showAll ?? true;
        selectedVirtual = new Set(data.selected ?? []);
      }
    } catch (e) {
      // Ignore localStorage errors
    }
  });

  // Save to localStorage when selection changes
  function savePhysicalSelection() {
    try {
      localStorage.setItem(STORAGE_KEY_PHYSICAL, JSON.stringify({
        showAll: showAllPhysical,
        selected: [...selectedPhysical]
      }));
    } catch (e) {}
  }

  function saveVirtualSelection() {
    try {
      localStorage.setItem(STORAGE_KEY_VIRTUAL, JSON.stringify({
        showAll: showAllVirtual,
        selected: [...selectedVirtual]
      }));
    } catch (e) {}
  }

  // Filter networks based on selection
  const visiblePhysical = $derived(
    showAllPhysical
      ? sortedPhysical
      : sortedPhysical.filter(n => selectedPhysical.has(n.name))
  );

  const visibleVirtual = $derived(
    showAllVirtual
      ? sortedVirtual
      : sortedVirtual.filter(n => selectedVirtual.has(n.name))
  );

  function togglePhysicalAll() {
    showAllPhysical = !showAllPhysical;
    savePhysicalSelection();
  }

  function toggleVirtualAll() {
    showAllVirtual = !showAllVirtual;
    saveVirtualSelection();
  }

  function togglePhysicalInterface(name) {
    if (selectedPhysical.has(name)) {
      selectedPhysical.delete(name);
    } else {
      selectedPhysical.add(name);
    }
    selectedPhysical = new Set(selectedPhysical);
    showAllPhysical = false;
    savePhysicalSelection();
  }

  function toggleVirtualInterface(name) {
    if (selectedVirtual.has(name)) {
      selectedVirtual.delete(name);
    } else {
      selectedVirtual.add(name);
    }
    selectedVirtual = new Set(selectedVirtual);
    showAllVirtual = false;
    saveVirtualSelection();
  }

  // Close dropdowns when clicking outside
  function handleClickOutside(e) {
    const target = e.target;
    if (!target.closest('.physical-dropdown')) {
      physicalDropdownOpen = false;
    }
    if (!target.closest('.virtual-dropdown')) {
      virtualDropdownOpen = false;
    }
  }

  $effect(() => {
    document.addEventListener('click', handleClickOutside);
    return () => document.removeEventListener('click', handleClickOutside);
  });
</script>

<BlockCard bid="network" title={t('host.networkTraffic')}>
  <!-- Summary Bar -->
  <div class="flex items-center justify-between mb-4 px-3 py-2 bg-muted/30 rounded-lg">
    <div class="flex items-center gap-4">
      <span class="text-xs text-muted-foreground uppercase tracking-wider">
        {t('host.totalTraffic')}
      </span>
      <span class="text-sm font-mono tnum">
        <span class="text-success">↓ {formatRate(summary?.rx_bytes_per_sec)}</span>
        <span class="mx-2 text-muted-foreground">/</span>
        <span class="text-info">↑ {formatRate(summary?.tx_bytes_per_sec)}</span>
      </span>
    </div>
    <div class="flex items-center gap-3 text-xs">
      {#if physicalNetworks.length > 0}
        <span class="flex items-center gap-1 text-muted-foreground">
          <Icon name="ethernet" size={11} />
          {physicalNetworks.length}
        </span>
      {/if}
      {#if virtualNetworks.length > 0}
        <span class="flex items-center gap-1 text-muted-foreground">
          <Icon name="network" size={11} />
          {virtualNetworks.length}
        </span>
      {/if}
    </div>
  </div>

  <!-- Two Column Layout -->
  <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
    <!-- Physical Interfaces Column -->
    {#if sortedPhysical.length > 0}
      <div>
        <!-- Column Header with Dropdown -->
        <div class="flex items-center justify-between mb-3 px-1">
          <div class="flex items-center gap-2">
            <Icon name="ethernet" size={14} class="text-muted-foreground" />
            <span class="text-sm font-medium">{t('host.physicalInterfaces')}</span>
            <span class="text-xs text-muted-foreground">({visiblePhysical.length}/{sortedPhysical.length})</span>
          </div>
          <div class="relative physical-dropdown">
            <button
              type="button"
              class="flex items-center gap-1 text-xs px-2 py-1 rounded border border-border hover:bg-muted/50 transition-colors"
              onclick={() => physicalDropdownOpen = !physicalDropdownOpen}
            >
              <Icon name="chevron-down" size={12} class={physicalDropdownOpen ? 'rotate-180' : ''} />
              {showAllPhysical ? t('common.all') : `${selectedPhysical.size} ${t('host.selected')}`}
            </button>
            {#if physicalDropdownOpen}
              <div class="absolute right-0 top-full mt-1 w-48 bg-background border border-border rounded-lg shadow-lg z-10 max-h-60 overflow-y-auto">
                <button
                  type="button"
                  class="w-full px-3 py-2 text-left text-xs hover:bg-muted/50 flex items-center gap-2 border-b border-border"
                  onclick={togglePhysicalAll}
                >
                  <span class="w-4 h-4 rounded border {showAllPhysical ? 'bg-primary border-primary' : 'border-muted-foreground'} flex items-center justify-center">
                    {#if showAllPhysical}
                      <Icon name="check" size={10} class="text-primary-foreground" />
                    {/if}
                  </span>
                  <span class="font-medium">{t('common.all')}</span>
                </button>
                {#each sortedPhysical as net}
                  <button
                    type="button"
                    class="w-full px-3 py-2 text-left text-xs hover:bg-muted/50 flex items-center gap-2"
                    onclick={() => togglePhysicalInterface(net.name)}
                  >
                    <span class="w-4 h-4 rounded border {selectedPhysical.has(net.name) || showAllPhysical ? 'bg-primary border-primary' : 'border-muted-foreground'} flex items-center justify-center">
                      {#if selectedPhysical.has(net.name) || showAllPhysical}
                        <Icon name="check" size={10} class="text-primary-foreground" />
                      {/if}
                    </span>
                    <span class="flex items-center gap-1.5">
                      <span class="w-1.5 h-1.5 rounded-full {net.state === 'up' ? 'bg-success' : 'bg-muted-foreground'}"></span>
                      <span class="font-mono">{net.name}</span>
                    </span>
                  </button>
                {/each}
              </div>
            {/if}
          </div>
        </div>

        <!-- Physical Interface Cards -->
        <div class="space-y-3">
          {#each visiblePhysical as net (net.name)}
            {@const rxHistory = (history?.[net.name] || []).map(h => ({ t: h.t, v: h.rx }))}
            {@const txHistory = (history?.[net.name] || []).map(h => ({ t: h.t, v: h.tx }))}

            <div class="border border-border rounded-lg p-3 hover:bg-muted/20 transition-colors">
              <!-- Interface Header -->
              <div class="mb-2">
                <div class="flex items-center justify-between mb-1">
                  <div class="flex items-center gap-2">
                    <span class="w-2 h-2 rounded-full {net.state === 'up' ? 'bg-success' : 'bg-muted-foreground'}"></span>
                    <Icon name="ethernet" size={13} class="text-muted-foreground" />
                    <span class="text-sm font-mono font-semibold">{net.name}</span>
                    <span class="text-[10px] px-1.5 py-0.5 rounded bg-muted text-muted-foreground uppercase font-medium">
                      {net.state}
                    </span>
                  </div>
                </div>
                
                <!-- IP and MAC Address -->
                <div class="flex flex-col gap-0.5 ml-9 text-xs">
                  {#if net.ip_address}
                    <div class="flex items-center gap-2 text-muted-foreground">
                      <Icon name="globe" size={10} class="shrink-0" />
                      <span class="font-mono">{net.ip_address}</span>
                    </div>
                  {/if}
                  {#if net.mac_address}
                    <div class="flex items-center gap-2 text-muted-foreground">
                      <Icon name="hash" size={10} class="shrink-0" />
                      <span class="font-mono text-[10px]">{net.mac_address}</span>
                    </div>
                  {/if}
                </div>
              </div>

              <!-- RX/TX Charts -->
              <div class="grid grid-cols-2 gap-2 mb-2">
                <div>
                  <div class="flex items-baseline justify-between mb-0.5">
                    <span class="text-[10px] text-muted-foreground">↓ RX</span>
                    <span class="text-[10px] font-mono tnum text-success">{formatRate(net.rx_bytes_per_sec)}</span>
                  </div>
                  <Chart points={rxHistory} height={30} color="var(--success)" />
                </div>
                <div>
                  <div class="flex items-baseline justify-between mb-0.5">
                    <span class="text-[10px] text-muted-foreground">↑ TX</span>
                    <span class="text-[10px] font-mono tnum text-info">{formatRate(net.tx_bytes_per_sec)}</span>
                  </div>
                  <Chart points={txHistory} height={30} color="var(--info, var(--accent))" />
                </div>
              </div>

              <!-- Total Counter -->
              <div class="flex items-center justify-between text-[10px] text-muted-foreground pt-1.5 border-t border-border">
                <span>↓ {formatBytes(net.rx_total_bytes)}</span>
                <span>↑ {formatBytes(net.tx_total_bytes)}</span>
              </div>
            </div>
          {/each}
        </div>
      </div>
    {/if}

    <!-- Virtual Interfaces Column -->
    {#if sortedVirtual.length > 0}
      <div>
        <!-- Column Header with Dropdown -->
        <div class="flex items-center justify-between mb-3 px-1">
          <div class="flex items-center gap-2">
            <Icon name="network" size={14} class="text-accent" />
            <span class="text-sm font-medium">{t('host.virtualInterfaces')}</span>
            <span class="text-xs text-muted-foreground">({visibleVirtual.length}/{sortedVirtual.length})</span>
          </div>
          <div class="relative virtual-dropdown">
            <button
              type="button"
              class="flex items-center gap-1 text-xs px-2 py-1 rounded border border-border hover:bg-muted/50 transition-colors"
              onclick={() => virtualDropdownOpen = !virtualDropdownOpen}
            >
              <Icon name="chevron-down" size={12} class={virtualDropdownOpen ? 'rotate-180' : ''} />
              {showAllVirtual ? t('common.all') : `${selectedVirtual.size} ${t('host.selected')}`}
            </button>
            {#if virtualDropdownOpen}
              <div class="absolute right-0 top-full mt-1 w-48 bg-background border border-border rounded-lg shadow-lg z-10 max-h-60 overflow-y-auto">
                <button
                  type="button"
                  class="w-full px-3 py-2 text-left text-xs hover:bg-muted/50 flex items-center gap-2 border-b border-border"
                  onclick={toggleVirtualAll}
                >
                  <span class="w-4 h-4 rounded border {showAllVirtual ? 'bg-primary border-primary' : 'border-muted-foreground'} flex items-center justify-center">
                    {#if showAllVirtual}
                      <Icon name="check" size={10} class="text-primary-foreground" />
                    {/if}
                  </span>
                  <span class="font-medium">{t('common.all')}</span>
                </button>
                {#each sortedVirtual as net}
                  <button
                    type="button"
                    class="w-full px-3 py-2 text-left text-xs hover:bg-muted/50 flex items-center gap-2"
                    onclick={() => toggleVirtualInterface(net.name)}
                  >
                    <span class="w-4 h-4 rounded border {selectedVirtual.has(net.name) || showAllVirtual ? 'bg-primary border-primary' : 'border-muted-foreground'} flex items-center justify-center">
                      {#if selectedVirtual.has(net.name) || showAllVirtual}
                        <Icon name="check" size={10} class="text-primary-foreground" />
                      {/if}
                    </span>
                    <span class="flex items-center gap-1.5">
                      <span class="w-1.5 h-1.5 rounded-full {net.state === 'up' ? 'bg-success' : 'bg-muted-foreground'}"></span>
                      <span class="font-mono">{net.name}</span>
                    </span>
                  </button>
                {/each}
              </div>
            {/if}
          </div>
        </div>

        <!-- Virtual Interface Cards -->
        <div class="space-y-3">
          {#each visibleVirtual as net (net.name)}
            {@const rxHistory = (history?.[net.name] || []).map(h => ({ t: h.t, v: h.rx }))}
            {@const txHistory = (history?.[net.name] || []).map(h => ({ t: h.t, v: h.tx }))}

            <div class="border border-border rounded-lg p-3 hover:bg-muted/20 transition-colors bg-accent/5 border-accent/20">
              <!-- Interface Header -->
              <div class="mb-2">
                <div class="flex items-center justify-between mb-1">
                  <div class="flex items-center gap-2">
                    <span class="w-2 h-2 rounded-full {net.state === 'up' ? 'bg-success' : 'bg-muted-foreground'}"></span>
                    <Icon name="network" size={13} class="text-accent" />
                    <span class="text-sm font-mono font-semibold">{net.name}</span>
                    <span class="text-[10px] px-1.5 py-0.5 rounded bg-muted text-muted-foreground uppercase font-medium">
                      {net.state}
                    </span>
                    <span class="text-[10px] px-1.5 py-0.5 rounded bg-accent/20 text-accent font-medium">
                      virtual
                    </span>
                  </div>
                </div>
                
                <!-- IP and MAC Address -->
                <div class="flex flex-col gap-0.5 ml-9 text-xs">
                  {#if net.ip_address}
                    <div class="flex items-center gap-2 text-muted-foreground">
                      <Icon name="globe" size={10} class="shrink-0" />
                      <span class="font-mono">{net.ip_address}</span>
                    </div>
                  {/if}
                  {#if net.mac_address}
                    <div class="flex items-center gap-2 text-muted-foreground">
                      <Icon name="hash" size={10} class="shrink-0" />
                      <span class="font-mono text-[10px]">{net.mac_address}</span>
                    </div>
                  {/if}
                </div>
              </div>

              <!-- RX/TX Charts -->
              <div class="grid grid-cols-2 gap-2 mb-2">
                <div>
                  <div class="flex items-baseline justify-between mb-0.5">
                    <span class="text-[10px] text-muted-foreground">↓ RX</span>
                    <span class="text-[10px] font-mono tnum text-success">{formatRate(net.rx_bytes_per_sec)}</span>
                  </div>
                  <Chart points={rxHistory} height={30} color="var(--success)" />
                </div>
                <div>
                  <div class="flex items-baseline justify-between mb-0.5">
                    <span class="text-[10px] text-muted-foreground">↑ TX</span>
                    <span class="text-[10px] font-mono tnum text-info">{formatRate(net.tx_bytes_per_sec)}</span>
                  </div>
                  <Chart points={txHistory} height={30} color="var(--info, var(--accent))" />
                </div>
              </div>

              <!-- Total Counter -->
              <div class="flex items-center justify-between text-[10px] text-muted-foreground pt-1.5 border-t border-border">
                <span>↓ {formatBytes(net.rx_total_bytes)}</span>
                <span>↑ {formatBytes(net.tx_total_bytes)}</span>
              </div>
            </div>
          {/each}
        </div>
      </div>
    {/if}
  </div>

  {#if !networks || networks.length === 0}
    <p class="text-sm text-muted-foreground text-center py-4">
      {t('host.noNetworks')}
    </p>
  {/if}
</BlockCard>
