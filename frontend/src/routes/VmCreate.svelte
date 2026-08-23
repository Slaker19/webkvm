<script>
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import PasswordModal from '$lib/components/PasswordModal.svelte';
  import ErrorModal from '$lib/components/ErrorModal.svelte';
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import SettingRow from '$lib/components/SettingRow.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { t } from '../lib/i18n.svelte.js';

  let name = $state('');
  let showCiPass = $state(false);
  let vcpus = $state(2);
  let ramMB = $state(2048);
  let storagePool = $state('');
  let cpuMode = $state('host-passthrough');
  let cpuModel = $state('');
  let videoModel = $state('virtio');
  let network = $state('default');
  let iso = $state('');
  let loading = $state(false);
  let error = $state('');
  let pools = $state([]);
  let networks = $state([]);
  let isos = $state([]);
  let loadingData = $state(true);

  let osType = $state('linux');
  let osVersion = $state('arch');
  let chipset = $state('q35');
  let firmware = $state('uefi');
  let secureBoot = $state(false);
  let tpmEnabled = $state(false);
  let networkModel = $state('virtio');

  // Disk options
  let diskSize = $state(30);
  let diskBus = $state('virtio');
  let diskFormat = $state('qcow2');
  let virtioISO = $state('');
  let useExistingDisk = $state(false);
  // Optional cloud-init provisioning.
  let ciEnabled = $state(false);
  let ciUser = $state('');
  let ciPassword = $state('');
  let ciSSHKey = $state('');
  let ciHostname = $state('');
  // Password modal state.
  let showPasswordModal = $state(false);
  let createdPassword = $state('');
  let createdUsername = $state('');
  let showErrorModal = $state(false);
  let errorTitle = $state('');
  let errorMessage = $state('');
  let existingDiskPool = $state('');
  let existingDiskName = $state('');
  let existingVolumes = $state([]);
  let loadingVolumes = $state(false);

  // Validation
  let touched = $state({ name: false, vcpus: false, ramMB: false, diskSize: false });
  const nameError = $derived(
    !name.trim() ? t('vmCreate.nameRequired') : name.length > 64 ? t('vmCreate.nameTooLong') : ''
  );
  const vcpusError = $derived(vcpus < 1 || vcpus > 64 ? t('vmCreate.vcpusRange') : '');
  const ramError = $derived(ramMB < 512 ? t('vmCreate.ramMin') : '');
  const diskSizeError = $derived(
    !useExistingDisk && (diskSize < 1 || diskSize > 1024) ? t('vmCreate.diskSizeRange') : ''
  );
  const ciError = $derived(
    !ciEnabled
      ? ''
      : !ciUser.trim()
        ? 'Username is required for cloud-init provisioning'
        : /^(root|daemon|bin|sys|sync|games|man|lp|mail|news|uucp|proxy|www-data|backup|list|irc|_apt|nobody|systemd-network|systemd-timesync|dhcpcd|messagebus|syslog|systemd-resolve|uuidd|tss|sshd|pollinate|tcpdump|landscape|fwupd-refresh|polkitd|sudo|adm|admin)$/i.test(
              ciUser
            )
          ? 'That name is a system group and would fail to provision; choose a different user name'
          : !ciPassword
            ? 'Password is required for cloud-init provisioning'
            : ciPassword.length < 6
              ? 'Password must be at least 6 characters'
              : ciPassword.length > 12
                ? 'Password must be at most 12 characters'
                : ''
  );
  const isValid = $derived(!nameError && !vcpusError && !ramError && !diskSizeError && !ciError);

  const cpuModes = [
    { value: 'host-passthrough', label: 'host-passthrough (recommended)' },
    { value: 'host-model', label: 'host-model' },
    { value: 'max', label: 'max' },
    { value: 'custom', label: 'custom' },
  ];

  const videoModels = [
    { value: 'virtio', label: 'virtio' },
    { value: 'qxl', label: 'qxl' },
    { value: 'vga', label: 'VGA' },
    { value: 'cirrus', label: 'cirrus' },
    { value: 'vmvga', label: 'vmvga (VMware)' },
    { value: 'bochs', label: 'bochs' },
    { value: 'none', label: 'none' },
  ];

  const networkModels = [
    { value: 'virtio', label: 'virtio (recommended)' },
    { value: 'e1000e', label: 'e1000e (Intel, ideal for Windows)' },
    { value: 'e1000', label: 'e1000 (legacy Intel)' },
    { value: 'rtl8139', label: 'rtl8139 (Realtek, very compatible)' },
    { value: 'pcnet', label: 'pcnet (AMD, legacy)' },
  ];

  const windowsVersions = [
    { value: 'win11', label: 'Windows 11' },
    { value: 'win10', label: 'Windows 10' },
    { value: 'win2k22', label: 'Windows Server 2022' },
    { value: 'win2k19', label: 'Windows Server 2019' },
    { value: 'win2k16', label: 'Windows Server 2016' },
  ];

  const linuxVersions = [
    { value: 'arch', label: 'Arch Linux' },
    { value: 'ubuntu24', label: 'Ubuntu 24.04' },
    { value: 'ubuntu22', label: 'Ubuntu 22.04' },
    { value: 'debian12', label: 'Debian 12' },
    { value: 'debian11', label: 'Debian 11' },
    { value: 'fedora40', label: 'Fedora 40' },
    { value: 'centos9', label: 'CentOS Stream 9' },
    { value: 'rhel9', label: 'RHEL 9' },
    { value: 'rocky9', label: 'Rocky Linux 9' },
    { value: 'opensuse', label: 'openSUSE Leap' },
    { value: 'alpine', label: 'Alpine Linux' },
    { value: 'gentoo', label: 'Gentoo' },
    { value: 'void', label: 'Void Linux' },
    { value: 'other', label: 'Other Linux' },
  ];

  let osVersions = $derived(osType === 'windows' ? windowsVersions : linuxVersions);

  const diskBusOptions = [
    { value: 'virtio', label: 'virtio (recommended)' },
    { value: 'sata', label: 'SATA' },
    { value: 'scsi', label: 'SCSI' },
    { value: 'ide', label: 'IDE' },
  ];

  const diskFormatOptions = [
    { value: 'qcow2', label: 'qcow2 (recommended)' },
    { value: 'raw', label: 'raw' },
  ];

  $effect(() => {
    if (osType === 'windows') osVersion = 'win11';
    else osVersion = 'arch';
  });

  onMount(async () => {
    try {
      const [p, n, i] = await Promise.all([api.listPools(), api.listNetworks(), api.listISOs()]);
      pools = p;
      networks = n;
      isos = i;
      const diskPools = pools.filter((p) => p.purpose !== 'iso');
      const preferredDiskPool = diskPools.find((p) => p.name === 'webkvm-disks');
      storagePool = preferredDiskPool ? preferredDiskPool.name : diskPools[0]?.name || '';
      existingDiskPool = storagePool;
      loadExistingVolumes(existingDiskPool);
    } catch (e) {
      error = t('vmCreate.errorLoadingData', { error: e.message });
    } finally {
      loadingData = false;
    }
  });

  async function loadExistingVolumes(pool) {
    if (!pool) {
      existingVolumes = [];
      return;
    }
    loadingVolumes = true;
    try {
      const all = (await api.listVolumes(pool)) || [];
      // Internal qcow2 snapshots (e.g. "ubuntu-1.gnome") are
      // valid StorageVolume entries but must never be selected
      // as a primary disk for a new VM — they share the
      // overlay chain of their parent and would corrupt the
      // backing file. Filter them out by the is_snapshot flag
      // set by the backend's H3a classifier.
      existingVolumes = all.filter((v) => !v.is_snapshot);
      if (!existingVolumes.find((v) => v.name === existingDiskName)) {
        existingDiskName = existingVolumes[0]?.name || '';
      }
    } catch {
      existingVolumes = [];
    } finally {
      loadingVolumes = false;
    }
  }

  async function create() {
    touched = { name: true, vcpus: true, ramMB: true, diskSize: true };
    if (!isValid) {
      error = t('vmCreate.fixErrors');
      return;
    }
    loading = true;
    error = '';
    try {
      const payload = {
        name,
        vcpus,
        ram_mb: ramMB,
        storage_pool: useExistingDisk ? undefined : storagePool,
        cpu_mode: cpuMode === 'custom' ? 'custom' : cpuMode,
        cpu_model: cpuMode === 'custom' ? cpuModel : undefined,
        video_model: videoModel,
        network,
        network_model: networkModel,
        iso: iso || undefined,
        os_type: osType,
        os_version: osVersion,
        chipset,
        firmware,
        secure_boot: chipset === 'q35' ? secureBoot : false,
        tpm_enabled: chipset === 'q35' ? tpmEnabled : false,
        disk_gb: useExistingDisk ? undefined : diskSize,
        disk_bus: diskBus,
        disk_format: useExistingDisk ? undefined : diskFormat,
        virtio_iso: virtioISO || undefined,
      };
      if (useExistingDisk) {
        payload.existing_disk_pool = existingDiskPool;
        payload.existing_disk_name = existingDiskName;
      }
      if (ciEnabled) {
        payload.cloud_init = {
          user: ciUser || undefined,
          password: ciPassword || undefined,
          ssh_key: ciSSHKey || undefined,
          hostname: ciHostname || undefined,
        };
      }
      const result = await api.createVM(payload);
      if (result.password) {
        createdUsername = ciUser;
        createdPassword = result.password;
        showPasswordModal = true;
      } else {
        toast.success(t('vmCreate.vmCreated', { name }));
        navigate('/vms');
      }
    } catch (e) {
      const msg = e.message || '';
      error = msg;
      toast.error(msg);
      if (/collides with a system group/i.test(msg)) {
        errorTitle = 'User name not available';
        errorMessage = msg;
        showErrorModal = true;
      }
    } finally {
      loading = false;
    }
  }

  function onBack() {
    navigate('/vms');
  }
</script>

<div class="p-6 max-w-3xl">
  <div class="flex items-center gap-3 mb-6">
    <button
      onclick={onBack}
      class="p-1.5 rounded-md hover:bg-muted text-muted-foreground hover:text-foreground transition-colors"
      aria-label={t('vmCreate.back')}
    >
      <svg class="w-4 h-4" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24">
        <polyline points="15 18 9 12 15 6" />
      </svg>
    </button>
    <div>
      <h1 class="text-xl font-semibold tracking-tight">{t('vmCreate.title')}</h1>
      <p class="text-sm text-muted-foreground mt-0.5">{t('vmCreate.subtitle')}</p>
    </div>
  </div>

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if loadingData}
    <div class="flex items-center justify-center py-20"><Spinner size="lg" /></div>
  {:else}
    <form
      onsubmit={(e) => {
        e.preventDefault();
        create();
      }}
      class="space-y-5"
    >
      <!-- General + OS -->
      <div class="border border-border rounded-lg bg-card p-5">
        <div class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">
          {t('vmCreate.general')}
        </div>
        <SettingRow
          label={t('common.name')}
          helper={t('vmCreate.nameHelper')}
          error={touched.name ? nameError : ''}
        >
          <Input
            bind:value={name}
            type="text"
            placeholder="my-vm"
            class="max-w-sm tnum"
            aria-invalid={touched.name && nameError ? 'true' : undefined}
            onblur={() => (touched.name = true)}
          />
        </SettingRow>
        <SettingRow label={t('vmCreate.operatingSystem')} helper={t('vmCreate.osHelper')}>
          <div class="grid grid-cols-[8rem_minmax(0,1fr)] gap-3 w-full">
            <select bind:value={osType} class="input">
              <option value="linux">Linux</option>
              <option value="windows">Windows</option>
            </select>
            <select bind:value={osVersion} class="input">
              {#each osVersions as v}
                <option value={v.value}>{v.label}</option>
              {/each}
            </select>
          </div>
        </SettingRow>
        <SettingRow label={t('vmCreate.isoOptional')} helper={t('vmCreate.isoHelper')}>
          <select bind:value={iso} class="input max-w-xs">
            <option value="">{t('vmCreate.noneInstallLater')}</option>
            {#each isos as isoFile}
              <option value={isoFile.path}>{isoFile.name}</option>
            {/each}
          </select>
        </SettingRow>
      </div>

      <!-- System -->
      <div class="border border-border rounded-lg bg-card p-5">
        <div class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">
          {t('vmCreate.system')}
        </div>
        <SettingRow label={t('vmCreate.chipset')} helper={t('vmCreate.chipsetHelper')}>
          <select
            bind:value={chipset}
            onchange={() => {
              if (chipset === 'i440fx') {
                firmware = 'seabios';
                secureBoot = false;
                tpmEnabled = false;
              }
            }}
            class="input w-40"
          >
            <option value="q35">{t('vmCreate.q35Modern')}</option>
            <option value="i440fx">{t('vmCreate.i440fxLegacy')}</option>
          </select>
        </SettingRow>
        <SettingRow label={t('vmDetail.firmwareLabel')} helper={t('vmCreate.biosHelper')}>
          <select
            bind:value={firmware}
            disabled={chipset === 'i440fx'}
            class="input w-40 {chipset === 'i440fx' ? 'opacity-50' : ''}"
          >
            <option value="seabios">{t('vmCreate.seabios')}</option>
            <option value="uefi">{t('vmCreate.uefi')}</option>
          </select>
        </SettingRow>
        {#if chipset === 'q35' && firmware === 'uefi'}
          <SettingRow label={t('vmCreate.secureBoot')} helper={t('vmCreate.secureBootHelper')}>
            <button
              type="button"
              onclick={() => (secureBoot = !secureBoot)}
              class="relative w-9 h-5 rounded-full transition-colors {secureBoot
                ? 'bg-accent'
                : 'bg-muted'}"
              aria-label={t('vmCreate.toggleSecureBoot')}
            >
              <span
                class="absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform {secureBoot
                  ? 'translate-x-4'
                  : ''}"
              ></span>
            </button>
          </SettingRow>
          <SettingRow label={t('vmCreate.tpm')} helper={t('vmCreate.tpmHelper')}>
            <button
              type="button"
              onclick={() => (tpmEnabled = !tpmEnabled)}
              class="relative w-9 h-5 rounded-full transition-colors {tpmEnabled
                ? 'bg-accent'
                : 'bg-muted'}"
              aria-label={t('vmCreate.toggleTpm')}
            >
              <span
                class="absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform {tpmEnabled
                  ? 'translate-x-4'
                  : ''}"
              ></span>
            </button>
          </SettingRow>
        {/if}
      </div>

      <!-- Hardware -->
      <div class="border border-border rounded-lg bg-card p-5">
        <div class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">
          {t('vmCreate.hardware')}
        </div>
        <SettingRow
          label={t('common.vcpu')}
          helper={t('vmCreate.vcpusHelper')}
          error={touched.vcpus ? vcpusError : ''}
        >
          <Input
            type="number"
            bind:value={vcpus}
            min="1"
            max="64"
            class="w-24 tnum"
            onblur={() => (touched.vcpus = true)}
          />
        </SettingRow>
        <SettingRow
          label={t('vmDetail.ramLabel')}
          helper={t('vmCreate.ramHelper')}
          error={touched.ramMB ? ramError : ''}
        >
          <Input
            type="number"
            bind:value={ramMB}
            min="512"
            step="512"
            class="w-28 tnum"
            onblur={() => (touched.ramMB = true)}
          />
        </SettingRow>
        <SettingRow
          label={t('vmCreate.useExistingDisk')}
          helper={t('vmCreate.useExistingDiskHelper')}
        >
          <button
            type="button"
            onclick={() => (useExistingDisk = !useExistingDisk)}
            class="relative w-9 h-5 rounded-full transition-colors {useExistingDisk
              ? 'bg-accent'
              : 'bg-muted'}"
            aria-label={t('vmCreate.toggleExistingDisk')}
          >
            <span
              class="absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full transition-transform {useExistingDisk
                ? 'translate-x-4'
                : ''}"
            ></span>
          </button>
        </SettingRow>
        {#if useExistingDisk}
          <SettingRow label={t('vmCreate.diskPool')} helper={t('vmCreate.diskPoolHelper')}>
            <select
              bind:value={existingDiskPool}
              onchange={() => loadExistingVolumes(existingDiskPool)}
              class="input max-w-xs"
            >
              {#each pools.filter((p) => p.purpose !== 'iso') as p}
                <option value={p.name}>{p.name}</option>
              {/each}
            </select>
          </SettingRow>
          <SettingRow
            label={t('vmCreate.existingVolume')}
            helper={t('vmCreate.existingVolumeHelper')}
          >
            <select
              bind:value={existingDiskName}
              class="input max-w-xs"
              disabled={loadingVolumes || existingVolumes.length === 0}
            >
              {#if loadingVolumes}
                <option value="">{t('vmCreate.loadingVolumes')}</option>
              {:else if existingVolumes.length === 0}
                <option value="">{t('vmCreate.noVolumes')}</option>
              {:else}
                {#each existingVolumes as v}
                  <option value={v.name}
                    >{v.name} ({(v.capacity / 1024 / 1024 / 1024).toFixed(1)} GB)</option
                  >
                {/each}
              {/if}
            </select>
          </SettingRow>
        {:else}
          <SettingRow label={t('common.pool')} helper={t('vmCreate.storagePoolHelper')}>
            <select bind:value={storagePool} class="input max-w-xs">
              {#each pools.filter((p) => p.purpose !== 'iso') as p}
                <option value={p.name}>{p.name}</option>
              {/each}
            </select>
          </SettingRow>
          <SettingRow
            label={t('vmCreate.diskSizeGb')}
            helper={t('vmCreate.diskSizeHelper')}
            error={touched.diskSize ? diskSizeError : ''}
          >
            <Input
              type="number"
              bind:value={diskSize}
              min="1"
              max="1024"
              class="w-24 tnum"
              onblur={() => (touched.diskSize = true)}
            />
          </SettingRow>
          <SettingRow label={t('vmCreate.diskFormat')} helper={t('vmCreate.diskFormatHelper')}>
            <select bind:value={diskFormat} class="input max-w-xs">
              {#each diskFormatOptions as o}
                <option value={o.value}>{o.label}</option>
              {/each}
            </select>
          </SettingRow>
        {/if}
        <SettingRow label={t('vmDetail.busLabel')} helper={t('vmCreate.diskBusHelper')}>
          <select bind:value={diskBus} class="input max-w-xs">
            {#each diskBusOptions as o}
              <option value={o.value}>{o.label}</option>
            {/each}
          </select>
        </SettingRow>
        {#if osType === 'windows'}
          <SettingRow
            label={t('vmCreate.virtioDriversIso')}
            helper={t('vmCreate.virtioDriversHelper')}
          >
            <select bind:value={virtioISO} class="input max-w-xs">
              <option value="">{t('common.none')}</option>
              {#each isos as isoFile}
                <option value={isoFile.path}>{isoFile.name}</option>
              {/each}
            </select>
          </SettingRow>
        {/if}
      </div>

      <!-- CPU + Video + Network -->
      <div class="border border-border rounded-lg bg-card p-5">
        <div class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">
          {t('vmCreate.advanced')}
        </div>
        <SettingRow label={t('vmDetail.cpuModeLabel')} helper={t('vmCreate.cpuModeHelper')}>
          <select bind:value={cpuMode} class="input max-w-xs">
            {#each cpuModes as m}
              <option value={m.value}>{m.label}</option>
            {/each}
          </select>
        </SettingRow>
        {#if cpuMode === 'custom'}
          <SettingRow label={t('vmCreate.cpuModel')} helper={t('vmCreate.cpuModelHelper')}>
            <Input bind:value={cpuModel} type="text" placeholder="EPYC" class="max-w-xs" />
          </SettingRow>
        {/if}
        <SettingRow label={t('vmDetail.videoModelLabel')} helper={t('vmCreate.videoModelHelper')}>
          <select bind:value={videoModel} class="input max-w-xs">
            {#each videoModels as m}
              <option value={m.value}>{m.label}</option>
            {/each}
          </select>
        </SettingRow>
        <SettingRow label={t('vmDetail.networkLabel')} helper={t('vmCreate.networkHelper')}>
          <select bind:value={network} class="input max-w-xs">
            {#each networks as net}
              <option value={net.name}>{net.name} ({net.forward || 'isolated'})</option>
            {/each}
          </select>
        </SettingRow>
        <SettingRow label={t('vmDetail.adapter')} helper={t('vmCreate.adapterHelper')}>
          <select bind:value={networkModel} class="input max-w-xs">
            {#each networkModels as m}
              <option value={m.value}>{m.label}</option>
            {/each}
          </select>
        </SettingRow>

        <!-- Cloud-init (optional provisioning) -->
        <SettingRow label={t('vmCreate.cloudInitLabel')} helper={t('vmCreate.cloudInitHelper')}>
          <div class="w-full max-w-md space-y-3">
            <label class="flex items-center gap-2 text-sm cursor-pointer select-none">
              <input
                type="checkbox"
                bind:checked={ciEnabled}
                class="w-4 h-4 rounded border-border"
              />
              {t('vmCreate.cloudInitEnable')}
            </label>
            {#if ciEnabled}
              <div class="grid grid-cols-2 gap-3">
                <div>
                  <label class="text-xs text-muted-foreground">{t('vmCreate.cloudInitUser')}</label>
                  <input
                    bind:value={ciUser}
                    class="input w-full"
                    placeholder="webkvm"
                    oninput={() => (touched.name = true)}
                  />
                  <p class="text-[11px] text-muted-foreground mt-1">
                    Cannot be a system group name (e.g. admin, sudo, adm, root)
                  </p>
                </div>
                <div>
                  <label class="text-xs text-muted-foreground">Password *</label>
                  <div class="relative">
                    <input
                      bind:value={ciPassword}
                      class="input w-full pr-10"
                      placeholder="6-12 characters"
                      type={showCiPass ? 'text' : 'password'}
                      minlength="6"
                      maxlength="12"
                    />
                    <button
                      type="button"
                      onclick={() => (showCiPass = !showCiPass)}
                      class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground p-1 rounded"
                      aria-label={showCiPass ? t('login.hidePassword') : t('login.showPassword')}
                      aria-pressed={showCiPass}
                    >
                      <Icon name={showCiPass ? 'eyeOff' : 'eye'} size={16} />
                    </button>
                  </div>
                </div>
                <div>
                  <label class="text-xs text-muted-foreground"
                    >{t('vmCreate.cloudInitHostname')}</label
                  >
                  <input bind:value={ciHostname} class="input w-full" placeholder="my-vm" />
                </div>
                <div></div>
                <div class="col-span-2">
                  <label class="text-xs text-muted-foreground"
                    >{t('vmCreate.cloudInitSSHKey')}</label
                  >
                  <textarea
                    bind:value={ciSSHKey}
                    class="input w-full font-mono text-xs"
                    rows="3"
                    placeholder="ssh-ed25519 AAAA... (optional)"
                  ></textarea>
                </div>
                <div class="col-span-2">
                  <p class="text-xs text-muted-foreground">
                    This is the guest OS login (used on the serial console). QEMU guest agent is
                    installed automatically so the password can be reset from WebKVM.
                  </p>
                  {#if ciError}
                    <p class="text-xs text-destructive mt-1">{ciError}</p>
                  {/if}
                </div>
              </div>
            {/if}
          </div>
        </SettingRow>
      </div>

      <div class="flex items-center gap-2 pt-2">
        <Button type="submit" disabled={loading || !isValid}>
          {#if loading}
            <Spinner size="sm" color="text-white" />
            {t('vmCreate.creating')}
          {:else}
            {t('vms.create')}
          {/if}
        </Button>
        <Button type="button" variant="outline" onclick={onBack}>{t('common.cancel')}</Button>
      </div>
    </form>
  {/if}
</div>

<PasswordModal
  bind:open={showPasswordModal}
  username={createdUsername}
  password={createdPassword}
  onClose={() => {
    toast.success(t('vmCreate.vmCreated', { name }));
    navigate('/vms');
  }}
/>

<ErrorModal bind:open={showErrorModal} title={errorTitle} message={errorMessage} />
