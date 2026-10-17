<script>
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { capabilities, supports, unavailableReason } from '$lib/stores/capabilities.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import SettingRow from '$lib/components/SettingRow.svelte';
  import CpuPrioritySelector from '$lib/components/CpuPrioritySelector.svelte';
  import CpuFlagPicker from '$lib/components/CpuFlagPicker.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Pencil } from '@lucide/svelte';
  import { networkLabel } from '$lib/utils/networkLabel.js';

  let {
    vm = {},
    vmId = '',
    isContainerVm = false,
    vmNetworks = [],
    groupsList = [],
    hwEditing = $bindable(false),
    editTab = $bindable('general'),
    load,
  } = $props();

  let eName = $state('');
  let eVcpus = $state(2);
  let eRamMB = $state(2048);
  let eMinRamMB = $state(0);
  let eIOThreads = $state(0);
  let eCPUMode = $state('host-passthrough');
  let eCPUModel = $state('');
  let eCPUUnits = $state(1024);
  let eKVMHidden = $state(false);
  let eCPUFlags = $state([]);

  const COMMON_CPU_FLAGS = [
    { value: 'aes', label: 'aes', desc: 'AES-NI Hardware Encryption' },
    { value: 'avx2', label: 'avx2', desc: 'Advanced Vector Extensions 2' },
    { value: 'topoext', label: 'topoext', desc: 'AMD SMT / Core Topology' },
    { value: 'pdpe1gb', label: 'pdpe1gb', desc: '1 GB Hugepages Support' },
    { value: 'pcid', label: 'pcid', desc: 'Process-Context Identifiers (faster TLB)' },
    {
      value: 'hypervisor',
      label: 'hypervisor',
      desc: 'Hide Hypervisor Feature Bit (default: off)',
    },
  ];

  const cpuModelPresets = [
    'EPYC-Genoa',
    'EPYC-Milan',
    'EPYC-Rome',
    'EPYC',
    'SapphireRapids',
    'Icelake-Server',
    'Cascadelake-Server',
    'Skylake-Server',
    'Skylake-Client',
    'Broadwell-noTSX',
    'Broadwell',
    'Haswell-noTSX',
    'Haswell',
    'IvyBridge',
    'SandyBridge',
    'Nehalem',
    'Westmere',
    'Penryn',
    'Conroe',
    'core2duo',
    'Opteron_G5',
    'athlon',
    'phenom',
    'kvm64',
    'qemu64',
  ];

  let eVideoModel = $state('virtio');
  let eAudioModel = $state('none');
  let eSerialPort = $state(true);
  let eGraphicsType = $state('both');
  let eAutostart = $state(false);
  let eSelectedGroups = $state([]);
  let eNetwork = $state('default');
  let eNetworkModel = $state('virtio');
  let eChipset = $state('q35');
  let eSecureBoot = $state(false);
  let eTPM = $state(false);
  let eTPMVersion = $state('2.0');
  let eWatchdog = $state(false);
  let eFirmware = $state('uefi');
  let eOSType = $state('');
  let eOSVersion = $state('');
  let eBootOrder = $state('disk');
  let ePrivileged = $state(false);
  let eNesting = $state(false);
  let eProfiles = $state(['default']);
  let incusProfiles = $state([]);
  let editSaving = $state(false);

  const networkModels = [
    { value: 'virtio', label: 'virtio (recommended)' },
    { value: 'e1000e', label: 'e1000e (Intel, ideal for Windows)' },
    { value: 'e1000', label: 'e1000 (legacy Intel)' },
    { value: 'rtl8139', label: 'rtl8139 (Realtek, very compatible)' },
    { value: 'pcnet', label: 'pcnet (AMD, legacy)' },
  ];

  const audioModels = [
    { value: 'none', label: 'Sin audio (Desactivado)' },
    { value: 'ich9', label: 'Intel ICH9 HD Audio (Recomendado)' },
    { value: 'ac97', label: 'AC97 Audio (Legacy)' },
    { value: 'es1370', label: 'Ensoniq ES1370' },
  ];

  const bootOrderOptions = $derived([
    { value: 'disk', label: t('vmCreate.bootOrderDisk') },
    { value: 'cdrom', label: t('vmCreate.bootOrderCdrom') },
    { value: 'network', label: t('vmCreate.bootOrderNetwork') },
  ]);

  const quickRamPresets = [1024, 2048, 4096, 8192, 16384, 32768, 65536];

  export function openEdit(tab = 'general') {
    editTab = tab;
    eName = vm.name || '';
    eVcpus = vm.vcpus || 2;
    eRamMB = vm.ram_mb || 2048;
    eMinRamMB = vm.min_ram_mb || 0;
    eIOThreads = vm.iothreads || 0;
    eCPUMode = vm.cpu_mode || 'host-passthrough';
    eCPUModel = vm.cpu_model || '';
    eCPUUnits = vm.cpu_units || 1024;
    eKVMHidden = !!vm.kvm_hidden;
    eCPUFlags = Array.isArray(vm.cpu_flags) ? [...vm.cpu_flags] : [];
    eVideoModel = vm.video_model || 'virtio';
    eAudioModel = vm.audio_model || 'none';
    eSerialPort = vm.serial_port !== false;
    eGraphicsType = vm.graphics_type || 'both';
    eNetwork = vm.networks?.[0]?.network || vmNetworks[0]?.name || 'default';
    eNetworkModel = vm.networks?.[0]?.model || 'virtio';
    eChipset = vm.chipset || 'q35';
    eSecureBoot = vm.secure_boot;
    eTPM = vm.tpm_enabled;
    eTPMVersion = vm.tpm_version || '2.0';
    eWatchdog = vm.watchdog_enabled || false;
    eFirmware = vm.chipset === 'i440fx' ? 'seabios' : vm.firmware || 'seabios';
    if (eChipset === 'i440fx') {
      eSecureBoot = false;
      eTPM = false;
    }
    eOSType = vm.os_type || '';
    eOSVersion = vm.os_version || '';
    eBootOrder = vm.boot_order || 'disk';
    eAutostart = !!vm.autostart;
    eSelectedGroups = Array.isArray(vm.groups) ? [...vm.groups] : [];
    ePrivileged = vm.privileged;
    eNesting = vm.nesting;
    eProfiles = vm.profiles?.length ? vm.profiles : ['default'];
    if (isContainerVm && !incusProfiles.length) {
      api
        .listIncusProfiles()
        .then((pr) => {
          if (pr?.profiles?.length) incusProfiles = pr.profiles;
        })
        .catch(() => {});
    }
    hwEditing = true;
  }

  export function openEditTab(tab = 'general') {
    openEdit(tab);
  }

  async function saveEdit() {
    editSaving = true;
    try {
      const data = {};
      if (eName !== vm.name) data.name = eName;
      if (eVcpus !== vm.vcpus) data.vcpus = eVcpus;
      if (eRamMB !== vm.ram_mb) data.ram_mb = eRamMB;
      if (!isContainerVm) {
        if (eMinRamMB !== (vm.min_ram_mb || 0)) data.min_ram_mb = eMinRamMB;
        if (eIOThreads !== (vm.iothreads || 0)) data.iothreads = eIOThreads;
      }
      if (eCPUMode !== (vm.cpu_mode || 'host-passthrough')) data.cpu_mode = eCPUMode;
      if (eCPUMode === 'custom' && eCPUModel !== (vm.cpu_model || '')) data.cpu_model = eCPUModel;
      if (eCPUUnits !== (vm.cpu_units || 1024)) data.cpu_units = eCPUUnits;
      if (eKVMHidden !== !!vm.kvm_hidden) data.kvm_hidden = eKVMHidden;
      if (JSON.stringify(eCPUFlags) !== JSON.stringify(vm.cpu_flags || []))
        data.cpu_flags = eCPUFlags;
      if (eVideoModel !== (vm.video_model || 'virtio')) data.video_model = eVideoModel;
      if (eAudioModel !== (vm.audio_model || 'none')) data.audio_model = eAudioModel;
      if (eSerialPort !== (vm.serial_port !== false)) data.serial_port = eSerialPort;
      if (eGraphicsType !== (vm.graphics_type || 'both')) data.graphics_type = eGraphicsType;
      if (eNetwork !== (vm.networks?.[0]?.network || vmNetworks[0]?.name || 'default'))
        data.network = eNetwork;
      if (eNetworkModel !== (vm.networks?.[0]?.model || 'virtio'))
        data.network_model = eNetworkModel;
      if (eOSType !== (vm.os_type || '')) data.os_type = eOSType;
      if (eOSVersion !== (vm.os_version || '')) data.os_version = eOSVersion;
      const effSecureBoot = eFirmware === 'uefi' ? eSecureBoot : false;
      const effTPM = eFirmware === 'uefi' ? eTPM : false;
      if (effSecureBoot !== vm.secure_boot) data.secure_boot = effSecureBoot;
      if (effTPM !== vm.tpm_enabled) data.tpm_enabled = effTPM;
      if (effTPM && eTPMVersion !== (vm.tpm_version || '2.0')) data.tpm_version = eTPMVersion;
      if (eWatchdog !== (vm.watchdog_enabled || false)) data.watchdog_enabled = eWatchdog;
      if (eFirmware !== (vm.firmware || 'uefi')) data.firmware = eFirmware;
      if (eBootOrder !== (vm.boot_order || 'disk')) data.boot_order = eBootOrder;
      if (eAutostart !== !!vm.autostart) data.autostart = eAutostart;
      if (ePrivileged !== vm.privileged) data.privileged = ePrivileged;
      if (eNesting !== vm.nesting) data.nesting = eNesting;
      const curProfiles = JSON.stringify(vm.profiles || ['default']);
      if (JSON.stringify(eProfiles || []) !== curProfiles) data.profiles = eProfiles;

      await api.updateVM(vmId, data);

      const oldGroups = Array.isArray(vm.groups) ? vm.groups : [];
      if (JSON.stringify(oldGroups.sort()) !== JSON.stringify([...eSelectedGroups].sort())) {
        await api.updateVMMeta(vmId, { groups: eSelectedGroups }).catch(() => {});
      }

      hwEditing = false;
      toast.success(t('vmDetail.settingsUpdated'));
      await load?.();
    } catch (e) {
      toast.error(e.message);
    } finally {
      editSaving = false;
    }
  }
</script>

{#snippet hwRow(icon, label, value, _tab, badge = '', accent = false, badgeVariant = '')}
  <div class="flex items-center justify-between gap-3 py-2.5 px-3.5">
    <div class="flex items-center gap-3 min-w-0">
      <div
        class="w-7 h-7 rounded-md flex items-center justify-center shrink-0 {accent
          ? 'bg-accent/15 text-accent'
          : 'bg-muted text-muted-foreground'}"
      >
        <Icon name={icon} size={14} />
      </div>
      <div class="min-w-0">
        <p
          class="text-[11px] font-medium text-muted-foreground uppercase tracking-wide leading-tight"
        >
          {label}
        </p>
        <div class="flex items-center gap-2 mt-0.5">
          <p class="text-sm font-semibold text-foreground truncate">{value}</p>
          {#if badge}
            {@const variant =
              badgeVariant ||
              (['active', 'on', 'yes', 'ich9', 'virtio', 'uefi', 'true'].includes(
                String(badge).toLowerCase()
              )
                ? 'success'
                : ['disabled', 'off', 'no', 'false'].includes(String(badge).toLowerCase())
                  ? 'muted'
                  : 'accent')}
            <span
              class="text-[10px] font-mono px-1.5 py-0.5 rounded border uppercase font-medium {variant ===
              'success'
                ? 'bg-success/10 text-success border-success/20'
                : variant === 'muted'
                  ? 'bg-muted text-muted-foreground border-border'
                  : 'bg-accent/10 text-accent border-accent/20'}"
            >
              {badge}
            </span>
          {/if}
        </div>
      </div>
    </div>
  </div>
{/snippet}

<BlockCard bid="hardware" title={t('vmDetail.hardware')}>
  {#snippet headerActions()}
    {#if auth.canMutate()}
      {#if hwEditing}
        <Button
          size="sm"
          variant="outline"
          class="!h-7 !text-xs"
          onclick={() => (hwEditing = false)}
          disabled={editSaving}
        >
          {t('common.cancel')}
        </Button>
        <Button
          size="sm"
          class="!h-7 !text-xs min-w-[100px]"
          onclick={saveEdit}
          disabled={editSaving}
        >
          {#if editSaving}
            <Spinner size="sm" color="text-white" />
          {:else}
            <Icon name="check" size={14} class="mr-1.5" />
            {t('common.save')}
          {/if}
        </Button>
      {:else}
        <Button size="sm" class="!h-7 !text-xs" onclick={() => openEditTab('general')}>
          <Pencil class="w-3 h-3 mr-1.5" />
          {t('vmDetail.editAllSettings')}
        </Button>
      {/if}
    {/if}
  {/snippet}

  {#if hwEditing}
    {#if vm?.state === 'running'}
      <div
        class="mb-3 p-2.5 rounded-lg border border-warning/30 bg-warning-subtle text-warning text-xs flex items-center gap-2"
      >
        <Icon name="alert-triangle" size={14} class="shrink-0" />
        <span>{t('vmDetail.someChangesRequireRestart')}</span>
      </div>
    {/if}

    <!-- Horizontal Tab Selector -->
    <div
      class="flex items-center gap-1 overflow-x-auto pt-2 border-b border-border mb-3 no-scrollbar"
    >
      <button
        type="button"
        onclick={() => (editTab = 'general')}
        class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-all cursor-pointer whitespace-nowrap {editTab ===
        'general'
          ? 'border-accent text-accent'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="tag" size={13} />
        {t('vmCreate.general')}
      </button>

      <button
        type="button"
        onclick={() => (editTab = 'cpu')}
        class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-all cursor-pointer whitespace-nowrap {editTab ===
        'cpu'
          ? 'border-accent text-accent'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="cpu" size={13} />
        CPU & RAM
      </button>

      <button
        type="button"
        onclick={() => (editTab = 'display')}
        class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-all cursor-pointer whitespace-nowrap {editTab ===
        'display'
          ? 'border-accent text-accent'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="monitor" size={13} />
        {t('vmCreate.graphicsAudio')}
      </button>

      {#if !isContainerVm}
        <button
          type="button"
          onclick={() => (editTab = 'boot')}
          class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-all cursor-pointer whitespace-nowrap {editTab ===
          'boot'
            ? 'border-accent text-accent'
            : 'border-transparent text-muted-foreground hover:text-foreground'}"
        >
          <Icon name="lock" size={13} />
          {t('vmCreate.bootSecurity')}
        </button>
      {:else}
        <button
          type="button"
          onclick={() => (editTab = 'container')}
          class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-all cursor-pointer whitespace-nowrap {editTab ===
          'container'
            ? 'border-accent text-accent'
            : 'border-transparent text-muted-foreground hover:text-foreground'}"
        >
          <Icon name="box" size={13} />
          Incus / LXC
        </button>
      {/if}

      <button
        type="button"
        onclick={() => (editTab = 'os')}
        class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 -mb-px transition-all cursor-pointer whitespace-nowrap {editTab ===
        'os'
          ? 'border-accent text-accent'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="layers" size={13} />
        {t('vmDetail.os')}
      </button>
    </div>

    <!-- Tab Content Body -->
    <div class="space-y-4 divide-y divide-border/40">
      {#if editTab === 'general'}
        <div class="space-y-3 pt-1">
          <SettingRow label={t('common.name')} helper={t('vmCreate.nameHelper')}>
            <Input id="edit-name" bind:value={eName} type="text" class="max-w-xs font-medium" />
          </SettingRow>

          <SettingRow
            label={t('vmDetail.groupsLabel')}
            helper="Organiza esta instancia asociándola a uno o varios grupos de trabajo."
          >
            {#if groupsList.length > 0}
              <div class="flex flex-wrap gap-1.5 max-w-sm justify-end pt-1">
                {#each groupsList as g (g.name)}
                  {@const active = eSelectedGroups.includes(g.name)}
                  <button
                    type="button"
                    onclick={() => {
                      if (active) {
                        eSelectedGroups = eSelectedGroups.filter((x) => x !== g.name);
                      } else {
                        eSelectedGroups = [...eSelectedGroups, g.name];
                      }
                    }}
                    class="text-xs px-2.5 py-1 rounded-full border transition-all cursor-pointer font-medium select-none {active
                      ? 'border-transparent text-white shadow-sm ring-1 ring-white/20'
                      : 'hover:bg-muted/60 opacity-80 hover:opacity-100'}"
                    style={active
                      ? `background-color: ${g.color}`
                      : `border-color: ${g.color}50; color: ${g.color}; background-color: ${g.color}15`}
                  >
                    {g.name}
                  </button>
                {/each}
              </div>
            {:else}
              <span class="text-xs text-muted-foreground">{t('vmDetail.noGroups')}</span>
            {/if}
          </SettingRow>

          <SettingRow label={t('vmCreate.startAtBoot')} helper={t('vmCreate.startAtBootHelper')}>
            <Switch bind:checked={eAutostart} ariaLabel={t('vmCreate.startAtBoot')} />
          </SettingRow>

          <SettingRow label={t('vmDetail.networkLabel')} helper={t('vmCreate.networkHelper')}>
            <select id="edit-net" bind:value={eNetwork} class="input max-w-xs">
              {#each vmNetworks as net (net.name)}
                <option value={net.name}>{networkLabel(net)}</option>
              {/each}
            </select>
          </SettingRow>

          {#if !isContainerVm}
            <SettingRow label={t('vmDetail.adapter')} helper={t('vmCreate.adapterHelper')}>
              <select id="edit-netmodel" bind:value={eNetworkModel} class="input max-w-xs">
                {#each networkModels as m (m.value)}
                  <option value={m.value} disabled={!supports('network', m.value)}>
                    {m.label}{!supports('network', m.value)
                      ? ' — ' + unavailableReason('network', m.value)
                      : ''}
                  </option>
                {/each}
              </select>
            </SettingRow>
          {/if}
        </div>
      {:else if editTab === 'cpu'}
        <div class="space-y-3 pt-1">
          <SettingRow label={t('common.vcpu')} helper={t('vmCreate.vcpusHelper')}>
            <div class="flex items-center gap-2">
              <Input
                id="edit-vcpus"
                type="number"
                min="1"
                max="64"
                bind:value={eVcpus}
                class="tnum w-24 text-center font-medium"
              />
              <span class="text-xs text-muted-foreground">vCPU</span>
            </div>
          </SettingRow>

          {#if !isContainerVm}
            <SettingRow label={t('vmDetail.cpuModeLabel')} helper={t('vmCreate.cpuModeHelper')}>
              <select id="edit-cpu" bind:value={eCPUMode} class="input max-w-xs">
                <option value="host-passthrough">host-passthrough (Rendimiento nativo)</option>
                <option value="host-model">host-model (Compatible con migración)</option>
                <option value="max">max (Todas las características QEMU)</option>
                <option value="custom">custom</option>
              </select>
            </SettingRow>

            {#if eCPUMode === 'custom'}
              <SettingRow label={t('vmCreate.cpuModel')} helper={t('vmCreate.cpuModelHelper')}>
                <select bind:value={eCPUModel} class="input max-w-xs">
                  {#each cpuModelPresets as m (m)}
                    <option value={m} disabled={!supports('cpu', m)}
                      >{m}{!supports('cpu', m) ? ' — ' + unavailableReason('cpu', m) : ''}</option
                    >
                  {/each}
                </select>
              </SettingRow>
            {/if}

            <SettingRow
              label={t('vmDetail.cpuPriorityLabel')}
              helper={t('vmDetail.cpuPriorityHelper')}
              stacked={true}
            >
              <CpuPrioritySelector bind:value={eCPUUnits} />
            </SettingRow>

            <SettingRow label={t('vmDetail.kvmHidden')} helper={t('vmDetail.kvmHiddenHelper')}>
              <Switch bind:checked={eKVMHidden} ariaLabel={t('vmDetail.kvmHidden')} />
            </SettingRow>

            <SettingRow
              label={t('vmDetail.cpuFlags')}
              helper={t('vmDetail.cpuFlagsHelper')}
              stacked={true}
            >
              <CpuFlagPicker
                bind:flags={eCPUFlags}
                available={capabilities.cpuFlags}
                common={COMMON_CPU_FLAGS}
              />
            </SettingRow>
          {/if}

          <SettingRow
            label={t('vmDetail.ramLabel')}
            helper={vm?.status === 'running'
              ? `${t('vmCreate.ramHelper')} • ${t('vmDetail.liveBallooningActive')}`
              : t('vmCreate.ramHelper')}
          >
            <div class="space-y-2 max-w-md flex flex-col items-end">
              <div class="flex flex-wrap items-center justify-end gap-2">
                <Input
                  id="edit-ram"
                  type="number"
                  min="256"
                  step="256"
                  bind:value={eRamMB}
                  class="tnum w-32 text-center font-medium"
                />
                <span class="text-xs text-muted-foreground"
                  >MB ({(eRamMB / 1024).toFixed(1)} GB)</span
                >
              </div>
              <!-- Quick RAM pills -->
              <div class="flex flex-wrap gap-1 justify-end">
                {#each quickRamPresets as p (p)}
                  <button
                    type="button"
                    onclick={() => (eRamMB = p)}
                    class="text-[11px] px-2 py-0.5 rounded border transition-colors {eRamMB === p
                      ? 'border-accent bg-accent/15 text-accent font-semibold'
                      : 'border-border bg-background text-muted-foreground hover:bg-muted/60'}"
                  >
                    {p >= 1024 ? `${p / 1024} GB` : `${p} MB`}
                  </button>
                {/each}
              </div>
            </div>
          </SettingRow>

          {#if !isContainerVm}
            <SettingRow label={t('vmDetail.minRamLabel')} helper={t('vmDetail.minRamHelper')}>
              <div class="flex items-center gap-2">
                <Input
                  id="edit-min-ram"
                  type="number"
                  min="0"
                  max={eRamMB}
                  step="256"
                  bind:value={eMinRamMB}
                  class="tnum w-32 text-center font-medium"
                />
                <span class="text-xs text-muted-foreground">
                  {eMinRamMB > 0
                    ? `MB (${(eMinRamMB / 1024).toFixed(1)} GB)`
                    : t('vmDetail.minRamDynamic')}
                </span>
              </div>
            </SettingRow>

            <SettingRow label={t('vmDetail.iothreadsLabel')} helper={t('vmDetail.iothreadsHelper')}>
              <div class="flex items-center gap-2">
                <Input
                  id="edit-iothreads"
                  type="number"
                  min="0"
                  max="16"
                  bind:value={eIOThreads}
                  class="tnum w-24 text-center font-medium"
                />
                <span class="text-xs text-muted-foreground"
                  >{eIOThreads > 0
                    ? t('vmDetail.iothreadsDedicated')
                    : t('vmDetail.iothreadsDisabled')}</span
                >
              </div>
            </SettingRow>
          {/if}
        </div>
      {:else if editTab === 'display'}
        <div class="space-y-3 pt-1">
          {#if !isContainerVm}
            <SettingRow
              label={t('vmDetail.videoModelLabel')}
              helper={t('vmCreate.videoModelHelper')}
            >
              <select id="edit-video" bind:value={eVideoModel} class="input max-w-xs">
                <option value="virtio" disabled={!supports('video', 'virtio')}
                  >VirtIO GPU (Moderno y rápido)</option
                >
                <option value="qxl" disabled={!supports('video', 'qxl')}
                  >QXL (Excelente soporte SPICE/Windows){!supports('video', 'qxl')
                    ? ' — ' + unavailableReason('video', 'qxl')
                    : ''}</option
                >
                <option value="vga" disabled={!supports('video', 'vga')}
                  >VGA Estándar (Máxima compatibilidad)</option
                >
                <option value="cirrus" disabled={!supports('video', 'cirrus')}
                  >Cirrus (Legado / SO antiguos)</option
                >
                <option value="vmvga" disabled={!supports('video', 'vmvga')}>VMware SVGA II</option>
                <option value="bochs" disabled={!supports('video', 'bochs')}>Bochs Display</option>
                <option value="none">{t('vmCreate.serialOnly')}</option>
              </select>
            </SettingRow>

            <SettingRow label={t('vmCreate.audioDevice')} helper={t('vmCreate.audioDeviceHelper')}>
              <select bind:value={eAudioModel} class="input max-w-xs">
                {#each audioModels as m (m.value)}
                  <option value={m.value} disabled={!supports('sound', m.value)}>
                    {m.label}{!supports('sound', m.value)
                      ? ' — ' + unavailableReason('sound', m.value)
                      : ''}
                  </option>
                {/each}
              </select>
            </SettingRow>

            <SettingRow
              label="Servidores Gráficos de Consola"
              helper="Activa o desactiva las consolas gráficas (VNC, SPICE o ambas simultáneamente)"
            >
              <select bind:value={eGraphicsType} class="input max-w-xs">
                <option value="both">Ambas activas (noVNC + SPICE)</option>
                <option value="vnc">Solo noVNC (RFB estándar)</option>
                <option value="spice">Solo SPICE (Alto rendimiento / Web SPICE)</option>
                <option value="none">Desactivadas (Sin consola gráfica / Headless)</option>
              </select>
            </SettingRow>

            <SettingRow label={t('vmCreate.serialPort')} helper={t('vmCreate.serialPortHelper')}>
              <Switch bind:checked={eSerialPort} ariaLabel={t('vmCreate.serialPort')} />
            </SettingRow>

            <SettingRow label={t('vmCreate.watchdog')} helper={t('vmCreate.watchdogHelper')}>
              <Switch bind:checked={eWatchdog} ariaLabel={t('vmCreate.watchdog')} />
            </SettingRow>
          {:else}
            <div class="p-4 rounded-lg bg-muted/20 text-center text-xs text-muted-foreground">
              {t('vmDetail.containerConsoleDesc')}
            </div>
          {/if}
        </div>
      {:else if editTab === 'boot' && !isContainerVm}
        <div class="space-y-3 pt-1">
          <SettingRow label={t('vmCreate.bootOrder')} helper={t('vmCreate.bootOrderHelper')}>
            <select id="edit-boot" bind:value={eBootOrder} class="input max-w-xs">
              {#each bootOrderOptions as opt (opt.value)}
                <option value={opt.value}>{opt.label}</option>
              {/each}
            </select>
          </SettingRow>

          <SettingRow label={t('vmDetail.chipset')} helper={t('vmDetail.chipsetLocked')}>
            <select
              id="edit-chipset"
              bind:value={eChipset}
              disabled
              class="input max-w-xs opacity-60"
            >
              <option value="q35">Q35 (PCIe / Moderno)</option>
              <option value="i440fx">i440fx (PCI / Legacy)</option>
            </select>
          </SettingRow>

          <SettingRow
            label={t('vmDetail.firmwareLabel')}
            helper={eChipset === 'i440fx'
              ? t('vmDetail.i440fxRequiresSeabios')
              : t('vmDetail.firmwareHelperDesc')}
          >
            <select
              id="edit-firmware"
              bind:value={eFirmware}
              disabled={eChipset === 'i440fx'}
              class="input max-w-xs {eChipset === 'i440fx' ? 'opacity-60' : ''}"
            >
              <option value="uefi">{t('vmDetail.uefiRecommended')}</option>
              <option value="seabios">{t('vmDetail.biosTraditional')}</option>
            </select>
          </SettingRow>

          {#if eFirmware === 'uefi'}
            <SettingRow
              label={t('vmDetail.secureBootLabel')}
              helper={t('vmCreate.secureBootHelper')}
            >
              <Switch bind:checked={eSecureBoot} ariaLabel={t('vmDetail.secureBootLabel')} />
            </SettingRow>

            <SettingRow label={t('vmDetail.tpm2')} helper={t('vmCreate.tpmHelper')}>
              <div class="flex items-center gap-3">
                <Switch bind:checked={eTPM} ariaLabel={t('vmDetail.tpm2')} />
                <select
                  bind:value={eTPMVersion}
                  disabled={!eTPM}
                  class="input !py-1 !text-xs w-28 {!eTPM ? 'opacity-50' : ''}"
                >
                  <option value="2.0">v2.0 (Win11)</option>
                  <option value="1.2">v1.2 (Legacy)</option>
                </select>
              </div>
            </SettingRow>
          {/if}
        </div>
      {:else if editTab === 'container' && isContainerVm}
        <div class="space-y-3 pt-1">
          <SettingRow label={t('vmCreate.privileged')} helper={t('vmCreate.privilegedHelper')}>
            <Switch
              bind:checked={ePrivileged}
              ariaLabel={t('vmCreate.privileged')}
              disabled={!auth.isAdmin()}
            />
          </SettingRow>

          <SettingRow label={t('vmCreate.nesting')} helper={t('vmCreate.nestingHelper')}>
            <Switch bind:checked={eNesting} ariaLabel={t('vmCreate.nesting')} />
          </SettingRow>

          <SettingRow label={t('vmCreate.profiles')} helper={t('vmCreate.profilesHelper')}>
            <select id="edit-profiles" bind:value={eProfiles} multiple class="input h-28 max-w-xs">
              {#each incusProfiles as p (p)}
                <option value={p}>{p}</option>
              {/each}
            </select>
          </SettingRow>
        </div>
      {:else if editTab === 'os'}
        <div class="space-y-3 pt-1">
          <SettingRow
            label={t('vmDetail.osTypeLabel')}
            helper="Familia del sistema operativo invitado para optimizar drivers e iconografía."
          >
            <select id="edit-ostype" bind:value={eOSType} class="input max-w-xs">
              <option value="">{t('vmDetail.auto')}</option>
              <option value="linux">{t('vmDetail.linux')}</option>
              <option value="windows">{t('vmDetail.windows')}</option>
              <option value="freebsd">{t('vmDetail.freebsd')}</option>
              <option value="other">{t('vmDetail.other')}</option>
            </select>
          </SettingRow>

          <SettingRow
            label={t('vmDetail.osVersionLabel')}
            helper="Versión específica o variante de la distribución (ej. ubuntu24.04, win11, arch)."
          >
            <Input
              id="edit-osver"
              bind:value={eOSVersion}
              placeholder="ej. ubuntu24.04"
              class="max-w-xs"
            />
          </SettingRow>
        </div>
      {/if}
    </div>

    <div class="flex items-center justify-end gap-2 pt-4 mt-2 border-t border-border">
      <Button variant="outline" onclick={() => (hwEditing = false)} disabled={editSaving}>
        {t('common.cancel')}
      </Button>
      <Button onclick={saveEdit} disabled={editSaving} class="min-w-[130px]">
        {#if editSaving}
          <Spinner size="sm" color="text-white" /> {t('vmDetail.saving')}
        {:else}
          <Icon name="check" size={14} class="mr-1.5" />
          {t('common.save')}
        {/if}
      </Button>
    </div>
  {:else}
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <!-- Cómputo -->
      <div class="rounded-xl border border-border bg-card/60 overflow-hidden flex flex-col">
        <div class="px-4 py-2.5 bg-muted/30 border-b border-border flex items-center gap-2">
          <Icon name="cpu" size={14} class="text-accent" />
          <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
            {t('vmCreate.compute')}
          </span>
        </div>
        <div class="divide-y divide-border/50 flex-1">
          {@render hwRow(
            'cpu',
            t('common.vcpu'),
            `${vm.vcpus} vCPU`,
            'cpu',
            `${vm.vcpus} cores`,
            true
          )}
          {@render hwRow(
            'activity',
            t('vmDetail.ramLabel'),
            `${vm.ram_mb} MB (${(vm.ram_mb / 1024).toFixed(1)} GB)`,
            'cpu',
            `${(vm.ram_mb / 1024).toFixed(1)} GB`,
            true
          )}
          {#if !isContainerVm}
            {@render hwRow(
              'settings',
              t('vmDetail.cpuMode'),
              vm.cpu_mode === 'custom' && vm.cpu_model
                ? `custom (${vm.cpu_model})`
                : vm.cpu_mode || 'host-passthrough',
              'cpu',
              vm.cpu_mode === 'host-passthrough' ? 'passthrough' : ''
            )}
            {#if vm.cpu_units && vm.cpu_units !== 1024}
              {@render hwRow(
                'gauge',
                t('vmDetail.cpuPriorityLabel'),
                vm.cpu_units === 512
                  ? t('vmDetail.cpuPriorityLow')
                  : vm.cpu_units === 2048
                    ? t('vmDetail.cpuPriorityHigh')
                    : `${vm.cpu_units} shares`,
                'cpu'
              )}
            {/if}
            {#if vm.kvm_hidden}
              {@render hwRow(
                'shield',
                t('vmDetail.kvmHidden'),
                t('vmDetail.kvmHiddenActive'),
                'cpu',
                'hidden'
              )}
            {/if}
            {#if vm.cpu_flags && vm.cpu_flags.length > 0}
              {@render hwRow('zap', t('vmDetail.cpuFlags'), vm.cpu_flags.join(', '), 'cpu')}
            {/if}
          {/if}
        </div>
      </div>

      <!-- Pantalla & Audio / Periféricos -->
      {#if !isContainerVm}
        <div class="rounded-xl border border-border bg-card/60 overflow-hidden flex flex-col">
          <div class="px-4 py-2.5 bg-muted/30 border-b border-border flex items-center gap-2">
            <Icon name="monitor" size={14} class="text-accent" />
            <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
              {t('vmCreate.graphicsAudio')}
            </span>
          </div>
          <div class="divide-y divide-border/50 flex-1">
            {@render hwRow(
              'monitor',
              t('vmDetail.video'),
              (vm.video_model || 'virtio').toUpperCase(),
              'display',
              vm.video_model || 'virtio',
              true
            )}
            {@render hwRow(
              'tv',
              'Consola Gráfica',
              vm.graphics_type === 'vnc'
                ? 'Solo noVNC'
                : vm.graphics_type === 'spice'
                  ? 'Solo SPICE'
                  : vm.graphics_type === 'none'
                    ? t('common.disabled')
                    : 'noVNC + SPICE',
              'display',
              vm.graphics_type === 'none' ? t('common.off') : vm.graphics_type || 'both',
              false,
              vm.graphics_type === 'none' ? 'muted' : 'accent'
            )}
            {@render hwRow(
              'volume-2',
              t('vmCreate.audioDevice'),
              !vm.audio_model || vm.audio_model === 'none'
                ? t('common.disabled')
                : vm.audio_model.toUpperCase(),
              'display',
              !vm.audio_model || vm.audio_model === 'none' ? t('common.off') : vm.audio_model,
              false,
              !vm.audio_model || vm.audio_model === 'none' ? 'muted' : 'accent'
            )}
            {@render hwRow(
              'terminal',
              t('vmCreate.serialPort'),
              vm.serial_port !== false
                ? `${t('common.enabled')} (ttyS0 PTY)`
                : t('common.disabled'),
              'display',
              vm.serial_port !== false ? t('common.on') : t('common.off'),
              vm.serial_port !== false,
              vm.serial_port !== false ? 'success' : 'muted'
            )}
            {@render hwRow(
              'shield',
              t('vmCreate.watchdog'),
              vm.watchdog_enabled ? t('vmDetail.watchdogModel') : t('common.disabled'),
              'display',
              vm.watchdog_enabled ? t('common.on') : t('common.off'),
              false,
              vm.watchdog_enabled ? 'success' : 'muted'
            )}
          </div>
        </div>

        <!-- Arranque & Seguridad -->
        <div class="rounded-xl border border-border bg-card/60 overflow-hidden flex flex-col">
          <div class="px-4 py-2.5 bg-muted/30 border-b border-border flex items-center gap-2">
            <Icon name="lock" size={14} class="text-accent" />
            <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
              {t('vmCreate.bootSecurity')}
            </span>
          </div>
          <div class="divide-y divide-border/50 flex-1">
            {@render hwRow(
              'play',
              t('vmCreate.bootOrder'),
              vm.boot_order === 'cdrom'
                ? t('vmCreate.bootOrderCdrom')
                : vm.boot_order === 'network'
                  ? t('vmCreate.bootOrderNetwork')
                  : t('vmCreate.bootOrderDisk'),
              'boot',
              vm.boot_order || 'disk',
              true
            )}
            {@render hwRow(
              'server',
              t('vmDetail.chipset'),
              vm.chipset || 'q35',
              'boot',
              vm.chipset || 'q35'
            )}
            {@render hwRow(
              'zap',
              t('vmDetail.bios'),
              vm.firmware === 'uefi' ? 'UEFI OVMF (x86_64)' : 'SeaBIOS (Legacy)',
              'boot',
              (vm.firmware || 'seabios').toUpperCase()
            )}
            {@render hwRow(
              'lock',
              t('vmDetail.secureBoot'),
              vm.secure_boot ? t('common.yes') : t('common.no'),
              'boot',
              vm.secure_boot ? t('common.yes') : t('common.no'),
              vm.secure_boot,
              vm.secure_boot ? 'success' : 'muted'
            )}
            {@render hwRow(
              'key',
              t('vmDetail.tpm'),
              vm.tpm_enabled ? `TPM ${vm.tpm_version || '2.0'} (swtpm)` : t('common.disabled'),
              'boot',
              vm.tpm_enabled ? t('common.on') : t('common.off'),
              vm.tpm_enabled,
              vm.tpm_enabled ? 'success' : 'muted'
            )}
          </div>
        </div>
      {:else}
        <!-- Contenedor Incus / LXC -->
        <div class="rounded-xl border border-border bg-card/60 overflow-hidden flex flex-col">
          <div class="px-4 py-2.5 bg-muted/30 border-b border-border flex items-center gap-2">
            <Icon name="box" size={14} class="text-accent" />
            <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
              Incus / LXC
            </span>
          </div>
          <div class="divide-y divide-border/50 flex-1">
            {@render hwRow(
              'shield',
              t('vmCreate.privileged'),
              vm.privileged ? t('vmDetail.privilegedRoot') : t('vmDetail.privilegedIsolated'),
              'container',
              vm.privileged ? t('common.yes') : t('common.no'),
              vm.privileged,
              vm.privileged ? 'success' : 'muted'
            )}
            {@render hwRow(
              'box',
              t('vmCreate.nesting'),
              vm.nesting ? t('vmDetail.nestingEnabled') : t('common.disabled'),
              'container',
              vm.nesting ? t('common.yes') : t('common.no'),
              vm.nesting,
              vm.nesting ? 'success' : 'muted'
            )}
            {@render hwRow(
              'layers',
              t('vmCreate.profiles'),
              (vm.profiles || ['default']).join(', '),
              'container',
              `${(vm.profiles || ['default']).length} profiles`
            )}
          </div>
        </div>
      {/if}

      <!-- Sistema & Identidad -->
      <div class="rounded-xl border border-border bg-card/60 overflow-hidden flex flex-col">
        <div class="px-4 py-2.5 bg-muted/30 border-b border-border flex items-center gap-2">
          <Icon name="tag" size={14} class="text-accent" />
          <span class="text-xs font-semibold uppercase tracking-wider text-foreground">
            {t('vmCreate.general')}
          </span>
        </div>
        <div class="divide-y divide-border/50 flex-1">
          {@render hwRow('tag', t('common.name'), vm.name, 'general', '', true)}
          {@render hwRow(
            'layers',
            t('vmDetail.os'),
            vm.os_type
              ? `${vm.os_type}${vm.os_version ? ' (' + vm.os_version + ')' : ''}`
              : t('vmDetail.auto'),
            'os',
            vm.os_type || 'auto'
          )}
          {@render hwRow(
            'network',
            t('vmDetail.networkLabel'),
            vm.networks?.[0]?.network || vm.networks?.[0]?.bridge || 'default',
            'general',
            vm.networks?.[0]?.model || 'virtio'
          )}
          {@render hwRow(
            'power',
            t('vmCreate.startAtBoot'),
            vm.autostart ? 'Inicio Automático con el Host' : 'Manual',
            'general',
            vm.autostart ? 'yes' : 'no',
            vm.autostart
          )}
          <!-- Groups row with actual colored chips -->
          <div class="flex items-center justify-between gap-3 py-2.5 px-3.5">
            <div class="flex items-center gap-3 min-w-0">
              <div
                class="w-7 h-7 rounded-md flex items-center justify-center shrink-0 bg-muted text-muted-foreground"
              >
                <Icon name="tag" size={14} />
              </div>
              <div class="min-w-0">
                <p
                  class="text-[11px] font-medium text-muted-foreground uppercase tracking-wide leading-tight"
                >
                  {t('vmDetail.groupsLabel')}
                </p>
                <div class="flex flex-wrap items-center gap-1.5 mt-1">
                  {#if Array.isArray(vm.groups) && vm.groups.length > 0}
                    {#each vm.groups as gName (gName)}
                      {@const groupObj = groupsList.find((x) => x.name === gName)}
                      <span
                        class="text-xs px-2 py-0.5 rounded-full border font-medium"
                        style={groupObj?.color
                          ? `border-color: ${groupObj.color}50; color: ${groupObj.color}; background-color: ${groupObj.color}15`
                          : 'border-border bg-muted text-muted-foreground'}
                      >
                        {gName}
                      </span>
                    {/each}
                  {:else}
                    <span class="text-xs text-muted-foreground">{t('vmDetail.noGroups')}</span>
                  {/if}
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  {/if}
</BlockCard>
