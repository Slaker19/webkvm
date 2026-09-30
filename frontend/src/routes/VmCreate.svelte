<script>
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import PasswordModal from '$lib/components/PasswordModal.svelte';
  import ErrorModal from '$lib/components/ErrorModal.svelte';
  import CloudInitPreviewDialog from '$lib/components/CloudInitPreviewDialog.svelte';
  import { onMount, untrack } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import {
    loadCapabilities,
    supports,
    unavailableReason,
    pick,
    capabilities,
  } from '$lib/stores/capabilities.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Card } from '$lib/components/ui/card';
  import SettingRow from '$lib/components/SettingRow.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import QuickPresetPicker from '$lib/components/QuickPresetPicker.svelte';
  import ImageCatalog from '$lib/components/ImageCatalog.svelte';
  import CloudInitFields from '$lib/components/CloudInitFields.svelte';
  import WizardStepRail from '$lib/components/WizardStepRail.svelte';
  import WizardNav from '$lib/components/WizardNav.svelte';
  import VmReviewStep from '$lib/components/VmReviewStep.svelte';
  import InstanceTypePicker from '$lib/components/InstanceTypePicker.svelte';
  import OsPresetPicker from '$lib/components/OsPresetPicker.svelte';

  import CpuFlagPicker from '$lib/components/CpuFlagPicker.svelte';
  import CpuPrioritySelector from '$lib/components/CpuPrioritySelector.svelte';
  import { t } from '$lib/i18n.svelte.js';
  import { vmDiskPools, containerPools } from '$lib/purpose.js';
  import { getCloudInitErrorKey } from '$lib/utils/cloudInitValidation.js';
  import { isValidVmName } from '$lib/utils/vmName.js';
  import {
    INCUS_IMAGE_PRESETS,
    INCUS_IMAGE_CATEGORIES,
    CUSTOM_IMAGE,
    labelForImage,
    getRecommendedResources,
    filterIncusImages,
    isValidImageRef,
  } from '$lib/utils/incusImages.js';
  import { networkLabel, networkLabelFor, preselectNetwork } from '$lib/utils/networkLabel.js';

  let name = $state('');
  // v1.4 Fase 4: instance kind selector ("vm" = KVM, "container" = LXC).
  // When "container" the form collapses to the LXD fields (image, root
  // disk, cloud-init) and hides every KVM-only section.
  let instanceType = $state('vm');
  const isContainer = $derived(instanceType === 'container');
  const isCloudInit = $derived(instanceType === 'cloudinit');
  const isKvm = $derived(instanceType === 'vm');

  const APPLIANCE_CATEGORIES = $derived([
    { id: 'all', label: t('vmCreate.applianceCatAll') },
    { id: 'cloud', label: t('vmCreate.applianceCatCloud') },
    { id: 'app', label: t('vmCreate.applianceCatApp') },
    { id: 'nas', label: t('vmCreate.applianceCatNas') },
  ]);

  // Cloud-Init / Appliances catalog
  let appliances = $state([]);
  let selectedApplianceId = $state('');
  let selectedApplianceCat = $state('all');
  let searchApplianceQuery = $state('');
  const filteredAppliances = $derived.by(() => {
    let list = appliances;
    if (selectedApplianceCat !== 'all') {
      list = list.filter((a) => a.category === selectedApplianceCat);
    }
    const q = searchApplianceQuery.trim().toLowerCase();
    if (q) {
      list = list.filter(
        (a) =>
          (a.name || '').toLowerCase().includes(q) ||
          (a.description || '').toLowerCase().includes(q) ||
          (a.id || '').toLowerCase().includes(q)
      );
    }
    return list;
  });
  const selectedAppliance = $derived(appliances.find((a) => a.id === selectedApplianceId) || null);

  function selectAppliance(app) {
    selectedApplianceId = app.id;
    touched.image = true;
    if (!name) name = app.id;
    if (app.vcpus) vcpus = app.vcpus;
    if (app.ram_mb) ramMB = app.ram_mb;
    if (app.disk_gb) diskSize = app.disk_gb;
    ciEnabled = true;
    if (!ciUser) ciUser = 'webkvm';
  }

  // v1.4 Fase 4.1: image picker = dropdown of presets + "Custom/Other".
  // containerImage is derived: a preset ref, or the manual custom ref.
  let imageChoice = $state('images:ubuntu/24.04');
  let customImage = $state('');
  let selectedIncusCat = $state('all');
  let searchIncusQuery = $state('');
  let rawIncusImages = $state([]);
  let incusHostArch = $state('');
  let incusEnabled = $state(true);
  const combinedIncusImages = $derived(
    rawIncusImages.length > 0 ? rawIncusImages : INCUS_IMAGE_PRESETS
  );
  const filteredIncusPresets = $derived(
    filterIncusImages(combinedIncusImages, {
      category: selectedIncusCat,
      search: searchIncusQuery,
    })
  );
  const localImagesCount = $derived(combinedIncusImages.filter((img) => img.is_local).length);
  const incusCategoriesWithCount = $derived(
    INCUS_IMAGE_CATEGORIES.filter((c) => c.id !== 'local' || localImagesCount > 0).map((c) => ({
      ...c,
      badgeCount: c.id === 'local' ? localImagesCount : 0,
    }))
  );
  const containerImage = $derived(
    imageChoice === CUSTOM_IMAGE.ref ? customImage.trim() : imageChoice
  );

  function selectContainerImage(p) {
    imageChoice = p.ref;
    touched.image = true;
    const rec = getRecommendedResources(p.ref, combinedIncusImages);
    if (rec) {
      vcpus = rec.vcpus;
      ramMB = rec.ramMB;
      diskSize = rec.diskGB;
    }
  }
  let vcpus = $state(2);
  let ramMB = $state(2048);
  let storagePool = $state('');
  let containerStoragePool = $state('');
  let cpuMode = $state('host-passthrough');
  let cpuModel = $state('');
  let videoModel = $state('virtio');
  let network = $state('');
  let iso = $state('');
  let loading = $state(false);
  let error = $state('');
  let pools = $state([]);
  let availableGroups = $state([]);
  let selectedGroups = $state([]);
  // Current user's pool allowlist (null until loaded). Used to hide
  // pools this user may not use (per-user ACL). Admins see all pools.
  let myAllowedPools = $state(null);
  // Current user's network allowlist (null until loaded). Same shape and
  // semantics as myAllowedPools, for the NIC's network selector.
  let myAllowedNetworks = $state(null);
  // Current user's quota (0/absent dimensions = unlimited). Drives the
  // vCPU/RAM capacity bars below; admins are quota-exempt so those come
  // back empty and the bars simply don't render.
  let myQuota = $state(null);
  // Pools visible to the current user for VM disk placement. Only pools
  // that can actually hold a KVM disk image: an ISO pool holds read-only
  // install media, and a container pool belongs to Incus, which shares
  // no namespace with libvirt. This used to filter with usableForDisks,
  // which strips ISO/backup/template pools but lets container pools
  // through — so the form offered an Incus pool for a KVM disk.
  const vmPools = $derived.by(() => {
    const allow = auth.role === 'admin' ? [] : myAllowedPools || [];
    return vmDiskPools(pools, allow);
  });
  // Pools visible to the current user for container root disk placement.
  // Only pools carrying purpose "container" (Incus storage pools).
  const lxcPools = $derived.by(() => {
    const allow = auth.role === 'admin' ? [] : myAllowedPools || [];
    return containerPools(pools, allow);
  });
  let networks = $state([]);
  // Networks visible to the current user for the VM's NIC. Mirrors vmPools
  // exactly (empty/absent allowlist or admin = every network).
  const vmNetworks = $derived.by(() => {
    if (auth.role === 'admin') return networks;
    if (myAllowedNetworks && myAllowedNetworks.length) {
      return networks.filter((n) => myAllowedNetworks.includes(n.name));
    }
    return networks;
  });
  let isos = $state([]);
  let loadingData = $state(true);

  let osType = $state('linux');
  // '' = generic Linux (no specific distro tag). The "modern Linux"
  // preset used to pick the first list entry ("Arch Linux") silently.
  let osVersion = $state('');
  let chipset = $state('q35');
  let firmware = $state('uefi');
  let secureBoot = $state(false);
  let tpmEnabled = $state(false);
  let tpmVersion = $state('2.0');
  let watchdogEnabled = $state(false);
  let serialPort = $state(true);
  let networkModel = $state('virtio');
  let audioModel = $state('none');
  let selectedOsPreset = $state('linux');

  // Preset preferred models, with the fallback chain used when this
  // host's QEMU cannot provide them. Windows presets historically
  // defaulted to "qxl", but QEMU 10 dropped QXL — on such hosts qxl is
  // disabled in the picker, so binding to it would leave the form on an
  // invalid value and the create call would fail. Picking the first
  // supported alternative keeps one-click presets working everywhere.
  // `pick()` comes from the capabilities store and is a no-op when the
  // host could not be probed (it returns the preferred model).
  const VIDEO_FALLBACKS = ['qxl', 'virtio', 'vmvga', 'vga'];
  const NETWORK_FALLBACKS = ['e1000e', 'virtio', 'rtl8139', 'e1000'];
  const AUDIO_FALLBACKS = ['ich9', 'ac97', 'es1370', 'none'];
  const DISKBUS_FALLBACKS = ['sata', 'virtio', 'scsi', 'ide'];

  function applyOsPreset(presetId) {
    selectedOsPreset = presetId;
    if (presetId === 'linux') {
      chipset = 'q35';
      firmware = 'uefi';
      secureBoot = false;
      tpmEnabled = false;
      diskBus = pick('diskbus', 'virtio', DISKBUS_FALLBACKS);
      diskDiscard = true;
      networkModel = pick('network', 'virtio', NETWORK_FALLBACKS);
      videoModel = pick('video', 'virtio', VIDEO_FALLBACKS);
      audioModel = 'none';
      bootOrder = 'cdrom';
      osType = 'linux';
      osVersion = '';
    } else if (presetId === 'win11') {
      chipset = 'q35';
      firmware = 'uefi';
      secureBoot = true;
      tpmEnabled = true;
      tpmVersion = '2.0';
      diskBus = pick('diskbus', 'sata', DISKBUS_FALLBACKS);
      networkModel = pick('network', 'e1000e', NETWORK_FALLBACKS);
      videoModel = pick('video', 'qxl', VIDEO_FALLBACKS);
      audioModel = pick('sound', 'ich9', AUDIO_FALLBACKS);
      bootOrder = 'cdrom';
      osType = 'windows';
      osVersion = 'win11';
    } else if (presetId === 'win10') {
      chipset = 'q35';
      firmware = 'uefi';
      secureBoot = false;
      tpmEnabled = false;
      diskBus = pick('diskbus', 'sata', DISKBUS_FALLBACKS);
      networkModel = pick('network', 'e1000e', NETWORK_FALLBACKS);
      videoModel = pick('video', 'qxl', VIDEO_FALLBACKS);
      audioModel = pick('sound', 'ich9', AUDIO_FALLBACKS);
      bootOrder = 'cdrom';
      osType = 'windows';
      osVersion = 'win10';
    } else if (presetId === 'bsd') {
      chipset = 'q35';
      firmware = 'uefi';
      secureBoot = false;
      tpmEnabled = false;
      diskBus = pick('diskbus', 'virtio', DISKBUS_FALLBACKS);
      networkModel = pick('network', 'virtio', NETWORK_FALLBACKS);
      videoModel = pick('video', 'virtio', VIDEO_FALLBACKS);
      audioModel = 'none';
      bootOrder = 'cdrom';
      osType = 'freebsd';
    } else if (presetId === 'legacy') {
      chipset = 'i440fx';
      firmware = 'seabios';
      secureBoot = false;
      tpmEnabled = false;
      diskBus = pick('diskbus', 'ide', DISKBUS_FALLBACKS);
      networkModel = pick('network', 'e1000e', NETWORK_FALLBACKS);
      videoModel = pick('video', 'vga', VIDEO_FALLBACKS);
      audioModel = 'none';
      bootOrder = 'cdrom';
      osType = 'other';
    }
  }

  // v1.4 Fase 5: advanced options (boot order, Incus security/profiles,
  // autostart). Unprivileged containers are the safe default.
  let bootOrder = $state('cdrom');
  let privileged = $state(false);
  let nesting = $state(true);
  let profiles = $state(['default']);
  // Off by default: a new VM should not silently start with the host.
  // The switch is visible in both easy and advanced mode.
  let startAtBoot = $state(false);
  let incusProfiles = $state([]);

  const audioModels = $derived([
    { value: 'none', label: t('vmCreate.audioModelNone') },
    { value: 'ich9', label: t('vmCreate.audioModelIch9') },
    { value: 'ac97', label: t('vmCreate.audioModelAc97') },
  ]);

  const bootOrderOptions = $derived([
    { value: 'cdrom', label: t('vmCreate.bootOrderCdrom') },
    { value: 'disk', label: t('vmCreate.bootOrderDisk') },
    { value: 'network', label: t('vmCreate.bootOrderNetwork') },
  ]);

  // Disk options
  let diskSize = $state(30);
  let diskBus = $state('virtio');
  let diskFormat = $state('qcow2');
  let virtioISO = $state('');
  let diskCacheIO = $state(false);
  let diskDiscard = $state(false);

  // Secondary disks: created via POST /vms/{id}/disks right after the
  // VM itself is created (KVM only — reuses the same AttachDisk path
  // as "Add Disk" in VmDetail, so pool quotas/ACLs are enforced
  // identically). Each row is { sizeGB, pool, bus, format }.
  let extraDisks = $state([]);
  let nextExtraDiskId = 1;
  function addExtraDisk() {
    extraDisks = [
      ...extraDisks,
      {
        _id: nextExtraDiskId++,
        sizeGB: 20,
        pool: storagePool || '',
        bus: 'virtio',
        format: 'qcow2',
      },
    ];
  }
  function removeExtraDisk(rowId) {
    extraDisks = extraDisks.filter((d) => d._id !== rowId);
  }

  // Secondary network interfaces: attached via createNetIface after VM creation.
  let extraNics = $state([]);
  let nextExtraNicId = 1;
  function addExtraNic() {
    extraNics = [
      ...extraNics,
      {
        _id: nextExtraNicId++,
        network: vmNetworks[0]?.name || 'default',
        model: 'virtio',
        vlanTag: '',
      },
    ];
  }
  function removeExtraNic(rowId) {
    extraNics = extraNics.filter((n) => n._id !== rowId);
  }

  // CPU topology (optional; when enabled, sockets*cores*threads must
  // equal vcpus).
  let cpuTopologyEnabled = $state(false);
  let cpuSockets = $state(1);
  let cpuCores = $state(2);
  let cpuThreads = $state(1);
  const cpuTopologyError = $derived(
    cpuTopologyEnabled && cpuSockets * cpuCores * cpuThreads !== vcpus
      ? t('vmCreate.cpuTopologyMismatch', { product: cpuSockets * cpuCores * cpuThreads, vcpus })
      : ''
  );
  let useExistingDisk = $state(false);
  // Optional cloud-init provisioning.
  let ciEnabled = $state(false);
  let ciUser = $state('');
  let ciPassword = $state('');
  let ciSSHKey = $state('');
  let ciHostname = $state('');
  let ciIpMode = $state('dhcp');
  let ciStaticIP = $state('');
  let ciGateway = $state('');
  let ciDNS = $state('');
  let minRamMB = $state(0);
  let iothreads = $state(0);
  let ciSnippetIds = $state([]);
  const ciSnippetId = $derived(ciSnippetIds[0] || '');
  let ciCustomUserData = $state('');
  let availableSnippets = $state([]);
  let showPreviewModal = $state(false);
  let previewContent = $state('');
  let previewLoading = $state(false);

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
  let touched = $state({ name: false, vcpus: false, ramMB: false, diskSize: false, image: false });
  const nameError = $derived(
    !name.trim()
      ? t('vmCreate.nameRequired')
      : name.length > 64
        ? t('vmCreate.nameTooLong')
        : !isValidVmName(name)
          ? t('vmCreate.nameInvalidChars')
          : ''
  );
  const vcpusError = $derived(vcpus < 1 || vcpus > 64 ? t('vmCreate.vcpusRange') : '');
  const ramError = $derived(ramMB < 512 ? t('vmCreate.ramMin') : '');
  const diskSizeError = $derived(
    !useExistingDisk && (diskSize < 1 || diskSize > 1024) ? t('vmCreate.diskSizeRange') : ''
  );
  // LXC image reference: required for containers.
  const imageError = $derived(
    !isContainer
      ? ''
      : !containerImage.trim()
        ? t('vmCreate.containerImageRequired')
        : !isValidImageRef(containerImage)
          ? t('vmCreate.imageFormatError')
          : ''
  );
  const applianceError = $derived(
    isCloudInit && !selectedAppliance ? t('vmCreate.cloudApplianceRequired') : ''
  );
  // ciEnabled is sticky (auto-enabled by LXC / Cloud-Init and kept when the
  // operator switches back), but plain KVM renders no cloud-init fields and
  // sends no cloud_init payload — so only validate where it applies,
  // otherwise LXC → KVM leaves Identity blocked by an invisible error.
  const ciApplicable = $derived(ciEnabled && !isKvm);
  const ciErrorKey = $derived(
    getCloudInitErrorKey({
      enabled: ciApplicable,
      instanceType,
      user: ciUser,
      password: ciPassword,
    })
  );
  const ciError = $derived(ciErrorKey ? t(ciErrorKey) : '');

  const isValid = $derived(
    !nameError &&
      !vcpusError &&
      !ramError &&
      !diskSizeError &&
      !imageError &&
      !applianceError &&
      !ciError &&
      !cpuTopologyError
  );

  // Per-step error flags for the rail. Gated on `touched` so a step the
  // operator hasn't filled in yet isn't flagged red on arrival.
  const stepImageHasError = $derived(Boolean(touched.image && (imageError || applianceError)));
  const stepIdentityHasError = $derived(Boolean((touched.name && nameError) || ciError));
  const stepResourcesHasError = $derived(
    Boolean(
      (touched.vcpus && vcpusError) ||
      (touched.ramMB && ramError) ||
      (touched.diskSize && diskSizeError) ||
      cpuTopologyError
    )
  );

  const primaryError = $derived.by(() => {
    if (touched.name && nameError) return nameError;
    if (touched.image && imageError) return imageError;
    if (touched.image && applianceError) return applianceError;
    if (touched.vcpus && vcpusError) return vcpusError;
    if (touched.ramMB && ramError) return ramError;
    if (touched.diskSize && diskSizeError) return diskSizeError;
    if (cpuTopologyError) return cpuTopologyError;
    if (ciError) return ciError;
    return '';
  });

  // Capacity bars against the current user's quota (null/0 = unlimited,
  // in which case no bar is shown for that dimension).
  const vcpuCapacityPct = $derived(
    myQuota?.max_vcpus > 0 ? (vcpus / myQuota.max_vcpus) * 100 : null
  );
  const ramCapacityPct = $derived(
    myQuota?.max_ram_mb > 0 ? (ramMB / myQuota.max_ram_mb) * 100 : null
  );

  let wizardMode = $state('easy');
  const isAdvanced = $derived(wizardMode === 'advanced');

  function setWizardMode(mode) {
    wizardMode = mode;
    try {
      localStorage.setItem('webkvm.createWizardMode', mode);
    } catch {
      /* ignore */
    }
  }

  // ---- Wizard steps -------------------------------------------------
  //
  // Creating an instance is a decision tree, not a settings screen: the
  // instance type changes which of the later questions even apply. So
  // the form is split into real steps with per-step validation, and
  // only the current one is mounted. `when` keeps the step list and the
  // rendered panels in sync; numbering is assigned after filtering.
  //
  //   1 Type + image   the decision everything else depends on
  //   2 Identity       name, groups, hostname/credentials
  //   3 Resources      vCPU, RAM, disk (+ firmware/CPU in advanced)
  //   4 Connectivity   network, extra NICs/disks
  //   5 Review         read-only recap, the only place that creates
  const STEP_IMAGE = 'step-image';
  const STEP_IDENTITY = 'step-identity';
  const STEP_RESOURCES = 'step-resources';
  const STEP_NETWORK = 'step-network';
  const STEP_REVIEW = 'step-review';

  // LXC delegates networking to its profiles, so that step is dropped
  // entirely rather than shown empty.
  const steps = $derived(
    [
      {
        id: STEP_IMAGE,
        label: t('vmCreate.stepImage'),
        when: true,
        hasError: stepImageHasError,
      },
      {
        id: STEP_IDENTITY,
        label: t('vmCreate.stepIdentity'),
        when: true,
        hasError: stepIdentityHasError,
      },
      {
        id: STEP_RESOURCES,
        label: t('vmCreate.stepResources'),
        when: true,
        hasError: stepResourcesHasError,
      },
      {
        id: STEP_NETWORK,
        label: t('vmCreate.stepNetwork'),
        when: !isContainer,
        hasError: false,
      },
      { id: STEP_REVIEW, label: t('vmCreate.stepReview'), when: true, hasError: false },
    ]
      .filter((s) => s.when)
      .map((s, i) => ({ id: s.id, label: s.label, hasError: s.hasError, num: i + 1 }))
  );

  let activeStepId = $state(STEP_IMAGE);
  const activeIndex = $derived(
    Math.max(
      0,
      steps.findIndex((s) => s.id === activeStepId)
    )
  );
  const isFirstStep = $derived(activeIndex === 0);
  const isLastStep = $derived(activeIndex === steps.length - 1);

  // Toggling easy/advanced or the instance type can retire the step the
  // operator is standing on (LXC has no network step). Fall back to the
  // last surviving step instead of rendering nothing.
  $effect(() => {
    if (steps.length > 0 && !steps.some((s) => s.id === activeStepId)) {
      activeStepId = steps[steps.length - 1].id;
    }
  });

  // Default container storage pool: prefer 'webkvm-incus' if present, otherwise first available
  $effect(() => {
    if (
      lxcPools.length > 0 &&
      (!containerStoragePool || !lxcPools.some((p) => p.name === containerStoragePool))
    ) {
      const preferred = lxcPools.find((p) => p.name === 'webkvm-incus');
      containerStoragePool = preferred ? preferred.name : lxcPools[0].name;
    }
  });

  // Blocking errors for the current step only. Advancing is gated on
  // these; the create button is still gated on the whole form.
  const currentStepBlocked = $derived.by(() => {
    if (activeStepId === STEP_IMAGE) return Boolean(imageError || applianceError);
    if (activeStepId === STEP_IDENTITY) return Boolean(nameError || ciError);
    if (activeStepId === STEP_RESOURCES) {
      return Boolean(vcpusError || ramError || diskSizeError || cpuTopologyError);
    }
    return false;
  });

  /** Mark the current step's fields as touched so its errors surface. */
  function touchCurrentStep() {
    if (activeStepId === STEP_IMAGE) touched.image = true;
    else if (activeStepId === STEP_IDENTITY) touched.name = true;
    else if (activeStepId === STEP_RESOURCES) {
      touched.vcpus = true;
      touched.ramMB = true;
      touched.diskSize = true;
    }
  }

  function goToStep(id) {
    if (!steps.some((s) => s.id === id)) return;
    activeStepId = id;
    // Panels swap in place, so without this the operator lands
    // mid-page on whatever the previous step's scroll position was.
    document.getElementById('wizard-panel')?.scrollIntoView({ block: 'start' });
  }

  function nextStep() {
    touchCurrentStep();
    if (currentStepBlocked) return;
    const next = steps[activeIndex + 1];
    if (next) goToStep(next.id);
  }

  function prevStep() {
    const prev = steps[activeIndex - 1];
    if (prev) goToStep(prev.id);
  }

  // Heading for the panel. Copy changes with the instance type so each
  // step explains what it is asking for in that particular context.
  const currentStep = $derived.by(() => {
    if (activeStepId === STEP_IMAGE) {
      return {
        title: t('vmCreate.stepImageTitle'),
        description: isContainer
          ? t('vmCreate.stepImageDescLxc')
          : isCloudInit
            ? t('vmCreate.stepImageDescCloud')
            : t('vmCreate.stepImageDescKvm'),
      };
    }
    if (activeStepId === STEP_IDENTITY) {
      return {
        title: t('vmCreate.stepIdentityTitle'),
        description: t('vmCreate.stepIdentityDesc'),
      };
    }
    if (activeStepId === STEP_RESOURCES) {
      return {
        title: t('vmCreate.stepResourcesTitle'),
        description: t('vmCreate.stepResourcesDesc'),
      };
    }
    if (activeStepId === STEP_NETWORK) {
      return {
        title: t('vmCreate.stepNetworkTitle'),
        description: t('vmCreate.stepNetworkDesc'),
      };
    }
    return { title: t('vmCreate.stepReviewTitle'), description: t('vmCreate.stepReviewDesc') };
  });

  /** Rail navigation: jumping back is always allowed, jumping forward
   * only out of a valid step, so errors can't be skipped past. */
  function onRailSelect(id) {
    const target = steps.findIndex((s) => s.id === id);
    if (target < 0 || target === activeIndex) return;
    if (target < activeIndex) {
      goToStep(id);
      return;
    }
    touchCurrentStep();
    if (currentStepBlocked) return;
    goToStep(id);
  }

  // "Will define" summary — purely derived from existing form state,
  // shown in a sticky sidebar so the operator can sanity-check the
  // whole VM without scrolling back up through every section.
  const summaryOs = $derived(
    (osType === 'windows' ? windowsVersions : linuxVersions).find((v) => v.value === osVersion)
      ?.label ||
      osVersion ||
      osType
  );
  const summaryDisk = $derived(
    useExistingDisk
      ? existingDiskName
        ? `${existingDiskName} (${existingDiskPool})`
        : t('vmCreate.noVolumes')
      : `${diskSize} GB · ${diskFormat}`
  );

  // The instance kind was being re-derived inline in the summary badge,
  // the action bar and the sticky panel, each with its own nested
  // ternary. One source of truth instead.
  const kindLabel = $derived(isContainer ? 'LXC' : isCloudInit ? 'Cloud-Init' : 'KVM');
  const kindTheme = $derived(isContainer ? 'warning' : isCloudInit ? 'success' : 'accent');

  const summaryRows = $derived.by(() => {
    const rows = [
      { key: 'name', label: t('common.name'), value: name || '—' },
      {
        key: 'os',
        label: t('vmCreate.operatingSystem'),
        value: isContainer
          ? labelForImage(containerImage, combinedIncusImages) || '—'
          : isCloudInit
            ? selectedAppliance?.name || '—'
            : summaryOs,
      },
      { key: 'vcpu', label: t('common.vcpu'), value: `${vcpus} vCPU` },
      {
        key: 'ram',
        label: t('vmDetail.ramLabel'),
        value: ramMB >= 1024 ? `${(ramMB / 1024).toFixed(0)} GB` : `${ramMB} MB`,
      },
      {
        key: 'disk',
        label: isContainer ? t('vmCreate.lxcRootDisk') : t('vmCreate.diskSizeGb'),
        value: isContainer
          ? `${diskSize} GB (${containerStoragePool || 'default'})`
          : isCloudInit
            ? `${diskSize} GB (${storagePool || 'default'})`
            : summaryDisk,
      },
    ];
    if (!isContainer) {
      rows.push({
        key: 'network',
        label: t('vmDetail.networkLabel'),
        value: networkLabelFor(network, networks),
      });
    }
    if (ciApplicable || isCloudInit) {
      rows.push({
        key: 'cloudinit',
        label: t('vmCreate.cloudInitLabel'),
        value: ciUser || (isContainer ? 'root' : '—'),
      });
    }
    return rows;
  });

  const cpuModes = $derived([
    { value: 'host-passthrough', label: t('vmCreate.cpuModeHostPassthrough') },
    { value: 'host-model', label: 'host-model' },
    { value: 'max', label: 'max' },
    { value: 'custom', label: 'custom' },
  ]);

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

  let cpuUnits = $state(1024);
  let kvmHidden = $state(false);
  let cpuFlags = $state([]);

  // Pinned quick-access flags shown as three-way (auto/on/off) pills in
  // CpuFlagPicker; `value` is the bare CPUID name (no +/- prefix — that
  // encodes state, not identity, in the on/auto/off model). The full
  // host-detected flag list (hundreds of names) is reachable via search
  // in the same picker.
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
  // 'other' reveals the manual text input below, for a model not in
  // the preset list. cpuModel (sent to the backend as-is) starts
  // synced to the first preset rather than empty, so switching into
  // "custom" CPU mode doesn't silently send an empty cpu_model.
  let cpuModelChoice = $state(cpuModelPresets[0]);
  $effect(() => {
    if (cpuModelChoice !== 'other') cpuModel = cpuModelChoice;
  });

  const videoModels = [
    { value: 'virtio', label: 'virtio' },
    { value: 'qxl', label: 'qxl' },
    { value: 'vga', label: 'VGA' },
    { value: 'cirrus', label: 'cirrus' },
    { value: 'vmvga', label: 'vmvga (VMware)' },
    { value: 'bochs', label: 'bochs' },
    { value: 'none', label: 'none' },
  ];

  const networkModels = $derived([
    { value: 'virtio', label: t('vmCreate.networkVirtioRec') },
    { value: 'e1000e', label: 'e1000e (Intel, ideal for Windows)' },
    { value: 'e1000', label: 'e1000 (legacy Intel)' },
    { value: 'rtl8139', label: 'rtl8139 (Realtek, very compatible)' },
    { value: 'pcnet', label: 'pcnet (AMD, legacy)' },
  ]);

  const windowsVersions = [
    { value: 'win11', label: 'Windows 11' },
    { value: 'win10', label: 'Windows 10' },
    { value: 'win2k22', label: 'Windows Server 2022' },
    { value: 'win2k19', label: 'Windows Server 2019' },
    { value: 'win2k16', label: 'Windows Server 2016' },
  ];

  const linuxVersions = $derived([
    { value: '', label: t('vmCreate.osVersionGenericLinux') },
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
    { value: 'other', label: t('vmCreate.osVersionOtherLinux') },
  ]);

  let osVersions = $derived(osType === 'windows' ? windowsVersions : linuxVersions);

  const diskBusOptions = $derived([
    { value: 'virtio', label: t('vmCreate.diskBusVirtioRec') },
    { value: 'sata', label: 'SATA' },
    { value: 'scsi', label: 'SCSI' },
    { value: 'ide', label: 'IDE' },
  ]);

  const diskFormatOptions = $derived([
    { value: 'qcow2', label: t('vmCreate.diskFormatQcow2Rec') },
    { value: 'raw', label: 'raw' },
  ]);

  // Keep the OS version in step with the OS family — but only when the
  // family actually changes, and only when the current version does not
  // belong to the new family: applyOsPreset() sets osType AND osVersion
  // together (win10), and this effect runs afterwards, so resetting
  // unconditionally turned the Windows 10 preset into win11. Families
  // without a version list (freebsd, other) get no version rather than a
  // stale Linux one ("arch" on BSD).
  //
  // This effect used to write osVersion unconditionally, and because it
  // both reads and writes $state, ANY other dependency change that
  // re-triggered it wiped the operator's selection: pick Linux →
  // "Ubuntu 24.04", toggle any control, and the dropdown snapped back to
  // "arch" — silently provisioning the wrong image. prevOsType is
  // deliberately NOT $state so reading it does not re-arm the effect
  // (the same pattern CronPicker.svelte uses for prevExpression).
  let prevOsType = null;
  $effect(() => {
    const t = osType;
    if (t === prevOsType) return;
    prevOsType = t;
    const list =
      t === 'windows' ? windowsVersions : t === 'linux' ? untrack(() => linuxVersions) : null;
    if (!list) osVersion = '';
    else if (!list.some((v) => v.value === untrack(() => osVersion))) osVersion = list[0].value;
  });

  // Containers are unusable without a first-boot login, so switching to
  // LXC enables cloud-init automatically — but only on the transition
  // into LXC, so the operator can still uncheck it afterwards (the old
  // effect re-enabled it on every run, making the checkbox stuck on).
  // prevIsContainer is deliberately NOT $state (same pattern as
  // prevOsType above). Prefill the hostname from the instance name.
  let prevIsContainer = false;
  $effect(() => {
    const c = isContainer;
    if (c && !prevIsContainer) untrack(() => (ciEnabled = true));
    prevIsContainer = c;
  });
  $effect(() => {
    if (isContainer && !ciHostname && name) ciHostname = name;
  });

  async function openCiPreview() {
    previewLoading = true;
    showPreviewModal = true;
    try {
      const res = await api.previewCloudInit({
        snippet_id: ciSnippetId || undefined,
        snippet_ids: ciSnippetIds.length > 0 ? ciSnippetIds : undefined,
        custom_user_data: ciCustomUserData || undefined,
        user: ciUser || (isContainer ? 'root' : 'webkvm'),
        password: ciPassword || undefined,
        ssh_key: ciSSHKey || undefined,
        hostname: ciHostname || name || 'my-instance',
      });
      previewContent = res.user_data;
    } catch (err) {
      toast.error(err.message);
      previewContent = '# Error generating preview: ' + err.message;
    } finally {
      previewLoading = false;
    }
  }

  onMount(async () => {
    try {
      const savedMode = localStorage.getItem('webkvm.createWizardMode');
      if (savedMode === 'easy' || savedMode === 'advanced') {
        wizardMode = savedMode;
      }
    } catch {
      /* ignore */
    }

    // Host capabilities gate the device-model pickers. Fire-and-forget
    // so a slow or failing probe never delays the form itself.
    loadCapabilities();
    try {
      // Pools, networks and ISOs are the three inputs the form cannot
      // work without, so they must NOT be grouped with the optional
      // lookups. One failing optional call used to abort the whole
      // sequence and leave the create form with no storage pool and no
      // network to choose from, and no message saying why — a dead-end
      // screen. allSettled keeps every panel independent.
      const [p, n, i, g, appRes, snRes] = await Promise.allSettled([
        api.listPools(),
        api.listNetworks(),
        api.listISOs(),
        api.listGroups(),
        api.listAppliances(),
        api.listCloudInitSnippets(),
      ]);
      if (p.status === 'fulfilled') {
        pools = Array.isArray(p.value) ? p.value : p.value?.pools || [];
      } else {
        toast.error(t('vmCreate.loadError'));
      }
      if (n.status === 'fulfilled') {
        networks = Array.isArray(n.value) ? n.value : n.value?.networks || [];
      } else {
        toast.error(t('vmCreate.loadError'));
      }
      if (i.status === 'fulfilled') {
        isos = Array.isArray(i.value) ? i.value : i.value?.isos || [];
      } else {
        isos = [];
      }
      if (g.status === 'fulfilled') availableGroups = g.value?.groups || [];
      if (appRes.status === 'fulfilled') appliances = appRes.value?.appliances || [];
      if (snRes.status === 'fulfilled') {
        availableSnippets = Array.isArray(snRes.value) ? snRes.value : [];
      }
      if (appliances.length > 0 && !selectedApplianceId) {
        selectedApplianceId = appliances[0].id;
      }
      // Default the network selector to the physical bridge (vmbr0/br0)
      // or the WebKVM network wired to it — never NAT (Proxmox-style).
      //
      // Pass `networks`, not `n`: with allSettled `n` is the
      // {status, value} wrapper, and calling .find() on it throws,
      // aborting onMount and blanking the whole form.
      network = preselectNetwork(networks);
      // Fase 5: available Incus profiles and images for the container form
      try {
        const [pr, imgRes] = await Promise.all([
          api.listIncusProfiles().catch(() => ({ profiles: [] })),
          api.listIncusImages().catch(() => ({ images: [] })),
        ]);
        if (pr?.profiles?.length) incusProfiles = pr.profiles;
        if (imgRes && Array.isArray(imgRes.images) && imgRes.images.length > 0) {
          rawIncusImages = imgRes.images;
          incusHostArch = imgRes.host_arch || '';
          incusEnabled = imgRes.incus_enabled ?? true;
        }
      } catch {
        incusProfiles = [];
        rawIncusImages = [];
      }
      try {
        const me = await api.me();
        myAllowedPools = me?.allowed_pools || [];
        myAllowedNetworks = me?.allowed_networks || [];
        myQuota = me?.quota || null;
      } catch {
        myAllowedPools = [];
        myAllowedNetworks = [];
        myQuota = null;
      }
      // Re-run preselection scoped to what this user may actually use, so a
      // restricted user isn't left defaulted onto a disallowed bridge.
      if (myAllowedNetworks && myAllowedNetworks.length && !myAllowedNetworks.includes(network)) {
        network = preselectNetwork(vmNetworks);
      }
      const diskPools = vmPools;
      const preferredDiskPool = diskPools.find((p) => p.name === 'webkvm-disks');
      storagePool = preferredDiskPool ? preferredDiskPool.name : diskPools[0]?.name || '';
      existingDiskPool = storagePool;
      loadExistingVolumes(existingDiskPool);

      const cPools = lxcPools;
      const preferredContainerPool = cPools.find((p) => p.name === 'webkvm-incus');
      containerStoragePool = preferredContainerPool
        ? preferredContainerPool.name
        : cPools[0]?.name || '';
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
    touched = { name: true, vcpus: true, ramMB: true, diskSize: true, image: true };
    if (!isValid) {
      error = t('vmCreate.fixErrors');
      return;
    }
    loading = true;
    error = '';
    try {
      // 1. VM Cloud-Init Deployment
      if (isCloudInit) {
        if (!selectedAppliance) {
          error = t('vmCreate.cloudApplianceRequired');
          loading = false;
          return;
        }
        if (!ciPassword) {
          error = 'Password is required for cloud-init provisioning';
          loading = false;
          return;
        }
        const deployPayload = {
          name,
          pool: storagePool || undefined,
          network: network || undefined,
          vcpus,
          ram_mb: ramMB,
          disk_gb: diskSize,
          autostart: startAtBoot,
          cloud_init: {
            user: ciUser || 'webkvm',
            password: ciPassword,
            ssh_key: ciSSHKey || undefined,
            hostname: ciHostname || name || selectedAppliance.id,
            snippet_id: ciSnippetId || undefined,
            snippet_ids: ciSnippetIds.length > 0 ? ciSnippetIds : undefined,
            custom_user_data: ciCustomUserData || undefined,
          },
        };
        // Deploy is asynchronous: the backend returns 202 + a job id and
        // finishes the download/create in the background. Wait for the
        // job before reporting success — declaring "deployed" and
        // navigating away immediately shows success even when the deploy
        // later fails.
        const res = await api.deployAppliance(selectedAppliance.id, deployPayload);
        if (res?.job_id) {
          await api.waitJob(res.job_id);
        }
        toast.success(t('vms.applianceDeployed', { name: name || selectedAppliance.name }));
        navigate('/vms');
        return;
      }

      // 2. Containers (LXC) / KVM creation
      const payload = isContainer
        ? {
            name,
            type: 'container',
            image: containerImage,
            vcpus,
            ram_mb: ramMB,
            disk_gb: diskSize,
            storage_pool: containerStoragePool || undefined,
            network,
            privileged,
            nesting,
            profiles,
            autostart: startAtBoot,
          }
        : {
            name,
            vcpus,
            ram_mb: ramMB,
            storage_pool: useExistingDisk ? undefined : storagePool,
            cpu_mode: cpuMode === 'custom' ? 'custom' : cpuMode,
            cpu_model: cpuMode === 'custom' ? cpuModel : undefined,
            cpu_flags: cpuFlags.length > 0 ? cpuFlags : undefined,
            cpu_units: cpuUnits !== 1024 ? cpuUnits : undefined,
            kvm_hidden: kvmHidden ? true : undefined,
            video_model: videoModel,
            audio_model: audioModel !== 'none' ? audioModel : undefined,
            network,
            network_model: networkModel,
            iso: iso || undefined,
            os_type: osType,
            os_version: osVersion,
            chipset,
            firmware,
            secure_boot: chipset === 'q35' ? secureBoot : false,
            tpm_enabled: chipset === 'q35' ? tpmEnabled : false,
            tpm_version: chipset === 'q35' && tpmEnabled ? tpmVersion : undefined,
            watchdog_enabled: watchdogEnabled,
            serial_port: serialPort,
            disk_gb: useExistingDisk ? undefined : diskSize,
            disk_bus: diskBus,
            disk_format: useExistingDisk ? undefined : diskFormat,
            virtio_iso: virtioISO || undefined,
            disk_cache_io: diskCacheIO,
            disk_discard: diskDiscard,
            boot_order: bootOrder,
            autostart: startAtBoot,
          };
      if (!isContainer) {
        if (minRamMB > 0) payload.min_ram_mb = minRamMB;
        if (iothreads > 0) payload.iothreads = iothreads;
      }
      if (!isContainer && cpuTopologyEnabled) {
        payload.cpu_sockets = cpuSockets;
        payload.cpu_cores = cpuCores;
        payload.cpu_threads = cpuThreads;
      }
      if (useExistingDisk) {
        payload.existing_disk_pool = existingDiskPool;
        payload.existing_disk_name = existingDiskName;
      }
      if (ciEnabled || isCloudInit) {
        let networks = undefined;
        if (ciIpMode === 'static' && ciStaticIP) {
          networks = [
            {
              interface: 'eth0',
              ipv4: ciStaticIP,
              gateway4: ciGateway || undefined,
              dns: ciDNS
                ? ciDNS
                    .split(',')
                    .map((s) => s.trim())
                    .filter(Boolean)
                : undefined,
            },
          ];
        }
        payload.cloud_init = {
          user: ciUser || (isContainer ? 'root' : 'webkvm'),
          password: ciPassword || undefined,
          ssh_key: ciSSHKey || undefined,
          hostname: ciHostname || undefined,
          networks,
          snippet_id: ciSnippetId || undefined,
          snippet_ids: ciSnippetIds.length > 0 ? ciSnippetIds : undefined,
          custom_user_data: ciCustomUserData || undefined,
        };
      }
      const result = await api.createVM(payload);
      if (result && result.id && selectedGroups.length > 0) {
        try {
          await api.updateVMMeta(result.id, { groups: selectedGroups });
        } catch (err) {
          console.warn('Failed to assign initial groups:', err);
        }
      }
      // Secondary disks: attached sequentially after the VM exists,
      // reusing the same AttachDisk endpoint "Add Disk" uses in
      // VmDetail. A failure here doesn't roll back the VM — it's
      // surfaced as a warning toast so the operator can retry from
      // the VM's Disks tab instead of losing the whole VM.
      if (!isContainer && result && result.id && extraDisks.length > 0) {
        let diskFailures = 0;
        for (const d of extraDisks) {
          try {
            await api.createDisk(result.id, {
              device: 'disk',
              bus: d.bus,
              size_gb: d.sizeGB,
              pool: d.pool || undefined,
              format: d.format,
            });
          } catch (err) {
            diskFailures++;
            console.warn('Failed to attach extra disk:', err);
          }
        }
        if (diskFailures > 0) {
          toast.warning(
            t('vmCreate.extraDisksFailed', { n: diskFailures }) ||
              `${diskFailures} secondary disk(s) failed to attach — add them from the VM's Disks tab.`
          );
        }
      }
      // Secondary network interfaces: attached sequentially via createNetIface.
      if (!isContainer && result && result.id && extraNics.length > 0) {
        let nicFailures = 0;
        for (const n of extraNics) {
          try {
            await api.createNetIface(result.id, {
              network: n.network,
              model: n.model,
              vlan_tag: n.vlanTag ? parseInt(n.vlanTag, 10) : undefined,
            });
          } catch (err) {
            nicFailures++;
            console.warn('Failed to attach extra NIC:', err);
          }
        }
        if (nicFailures > 0) {
          toast.warning(
            t('vmCreate.extraNicsFailed', { n: nicFailures }) ||
              `${nicFailures} secondary NIC(s) failed to attach — add them from the VM's Network tab.`
          );
        }
      }
      if (result.password) {
        // LXC provisions root when no user is given (same default the
        // cloud-init preview uses), so never show an empty "User" field.
        createdUsername = ciUser || (isContainer ? 'root' : '');
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
        errorTitle = t('vmCreate.errorUserNameNotAvailable');
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

<div class="p-4 sm:p-6 w-full max-w-6xl mx-auto">
  <PageHeader title={t('vmCreate.title')} subtitle={t('vmCreate.subtitle')}>
    {#snippet actions()}
      <div class="flex flex-wrap items-center gap-2">
        <!-- Selector Modo Fácil / Modo Avanzado -->
        <div class="inline-flex items-center p-1 bg-muted/60 rounded-xl border border-border">
          <button
            type="button"
            onclick={() => setWizardMode('easy')}
            class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg transition-all cursor-pointer {wizardMode ===
            'easy'
              ? 'bg-background text-foreground shadow-xs'
              : 'text-muted-foreground hover:text-foreground'}"
            title={t('vmCreate.modeEasyDesc')}
          >
            <Icon name="zap" size={13} class={wizardMode === 'easy' ? 'text-accent' : ''} />
            <span>{t('vmCreate.modeEasy')}</span>
          </button>
          <button
            type="button"
            onclick={() => setWizardMode('advanced')}
            class="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg transition-all cursor-pointer {wizardMode ===
            'advanced'
              ? 'bg-background text-foreground shadow-xs'
              : 'text-muted-foreground hover:text-foreground'}"
            title={t('vmCreate.modeAdvancedDesc')}
          >
            <Icon
              name="settings"
              size={13}
              class={wizardMode === 'advanced' ? 'text-accent' : ''}
            />
            <span>{t('vmCreate.modeAdvanced')}</span>
          </button>
        </div>

        <Button variant="outline" size="sm" onclick={onBack} class="cursor-pointer">
          <Icon name="chevronLeft" size={13} class="mr-1" />
          {t('common.back')}
        </Button>
      </div>
    {/snippet}
  </PageHeader>

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  {#if loadingData}
    <div class="flex items-center justify-center py-20"><Spinner size="lg" /></div>
  {:else}
    <div class="grid grid-cols-1 lg:grid-cols-[200px_minmax(0,1fr)] gap-4 lg:gap-8 items-start">
      <!-- Step rail: doubles as progress indicator and navigation. On
           mobile the grid collapses to one column, so it lands above
           the panel as a horizontal strip. -->
      <WizardStepRail {steps} activeId={activeStepId} onselect={onRailSelect} />

      <form
        onsubmit={(e) => {
          e.preventDefault();
          if (!isLastStep) {
            nextStep();
            return;
          }
          create();
        }}
        class="min-w-0 space-y-4"
        id="wizard-panel"
      >
        <Card class="p-5 scroll-mt-4">
          <header class="mb-4 pb-3 border-b border-border/60">
            <p class="text-[11px] font-semibold text-accent uppercase tracking-wider">
              {t('vmCreate.stepCounter', { current: activeIndex + 1, total: steps.length })}
            </p>
            <h2 class="text-base font-semibold text-foreground mt-0.5">{currentStep.title}</h2>
            <p class="text-xs text-muted-foreground mt-0.5">{currentStep.description}</p>
          </header>

          {#if activeStepId === STEP_IMAGE}
            <InstanceTypePicker
              bind:value={instanceType}
              onselect={(kind) => {
                if (kind !== 'cloudinit') return;
                if (!ciEnabled) ciEnabled = true;
                if (!ciUser) ciUser = 'webkvm';
              }}
            />
            {#if isKvm}
              <!-- OS Presets for KVM -->
              <OsPresetPicker bind:value={selectedOsPreset} onselect={applyOsPreset} />
            {/if}
            {#if isContainer}
              {#if !incusEnabled}
                <Alert variant="warning" class="mt-5">
                  {t('vmCreate.incusDisabledWarning')}
                </Alert>
              {/if}
              <!-- LXC Container Image Visual Catalog -->
              <div class="pt-5 mt-2 border-t border-border/60 space-y-4">
                <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-2">
                  <div>
                    <div class="flex items-center gap-2">
                      <h3 class="text-sm font-semibold text-foreground">
                        {t('vmCreate.containerImage')}
                      </h3>
                      {#if incusHostArch}
                        <span
                          class="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-muted/80 text-[11px] font-mono text-muted-foreground border border-border/70"
                        >
                          <span>{t('vmCreate.hostArchLabel')}:</span>
                          <strong class="text-foreground">{incusHostArch}</strong>
                        </span>
                      {/if}
                    </div>
                    <p class="text-xs text-muted-foreground mt-0.5">
                      {t('vmCreate.containerImageHelper')}
                    </p>
                  </div>

                  <!-- Custom / Visual Switcher Button -->
                  <button
                    type="button"
                    onclick={() => {
                      imageChoice =
                        imageChoice === CUSTOM_IMAGE.ref ? 'images:ubuntu/24.04' : CUSTOM_IMAGE.ref;
                      touched.image = true;
                    }}
                    class="text-xs text-accent hover:text-accent-hover font-medium underline-offset-4 hover:underline self-start sm:self-auto shrink-0 cursor-pointer"
                  >
                    {imageChoice === CUSTOM_IMAGE.ref
                      ? t('vmCreate.customImageReturn')
                      : t('vmCreate.customImageManual')}
                  </button>
                </div>

                {#if imageChoice === CUSTOM_IMAGE.ref}
                  <div class="p-4 bg-muted/30 border border-border rounded-xl space-y-2">
                    <label
                      for="custom-incus-ref"
                      class="block text-xs font-semibold text-foreground"
                    >
                      {t('vmCreate.customImageRef')}
                    </label>
                    <Input
                      id="custom-incus-ref"
                      bind:value={customImage}
                      type="text"
                      placeholder="e.g. images:gentoo, rockylinux:9/cloud, local:my-custom-image"
                      class="w-full font-mono text-xs"
                      aria-invalid={touched.image && imageError ? 'true' : undefined}
                      onblur={() => (touched.image = true)}
                    />
                    <p class="text-[11px] text-muted-foreground">
                      {t('vmCreate.customImageHelper')}
                    </p>
                    {#if touched.image && imageError}
                      <p class="text-xs text-destructive">{imageError}</p>
                    {/if}
                  </div>
                {:else}
                  <ImageCatalog
                    items={filteredIncusPresets}
                    bind:selectedId={imageChoice}
                    categories={incusCategoriesWithCount}
                    bind:selectedCategory={selectedIncusCat}
                    bind:searchQuery={searchIncusQuery}
                    searchPlaceholder={t('vmCreate.searchCloudImages')}
                    theme="accent"
                    onselect={selectContainerImage}
                  />
                {/if}
              </div>
            {:else if isCloudInit}
              <!-- Cloud-Init Template Catalog -->
              <div class="pt-5 mt-2 border-t border-border/60 space-y-4">
                <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-2">
                  <div>
                    <div class="flex items-center gap-2">
                      <h3 class="text-sm font-semibold text-foreground">
                        {t('vmCreate.cloudAppliance')}
                      </h3>
                      <span
                        class="px-2 py-0.5 rounded bg-success/20 text-success text-[11px] font-mono border border-success/30"
                      >
                        {t('vmCreate.availableAppliancesCount', { count: appliances.length })}
                      </span>
                    </div>
                    <p class="text-xs text-muted-foreground mt-0.5">
                      {t('vmCreate.cloudApplianceHelper')}
                    </p>
                  </div>
                </div>

                <ImageCatalog
                  items={filteredAppliances}
                  bind:selectedId={selectedApplianceId}
                  categories={APPLIANCE_CATEGORIES}
                  bind:selectedCategory={selectedApplianceCat}
                  bind:searchQuery={searchApplianceQuery}
                  searchPlaceholder={t('vmCreate.searchCloudImages')}
                  theme="success"
                  onselect={selectAppliance}
                />

                {#if touched.image && applianceError}
                  <p class="text-xs text-destructive">{applianceError}</p>
                {/if}
              </div>
            {:else}
              {#if isAdvanced}
                <SettingRow label={t('vmCreate.operatingSystem')} helper={t('vmCreate.osHelper')}>
                  <div class="grid grid-cols-[8rem_minmax(0,1fr)] gap-3 w-full">
                    <select bind:value={osType} class="input">
                      <option value="linux">Linux</option>
                      <option value="windows">Windows</option>
                    </select>
                    <select bind:value={osVersion} class="input">
                      {#each osVersions as v (v.value)}
                        <option value={v.value}>{v.label}</option>
                      {/each}
                    </select>
                  </div>
                </SettingRow>
              {/if}
              <SettingRow label={t('vmCreate.isoOptional')} helper={t('vmCreate.isoHelper')}>
                <select bind:value={iso} class="input max-w-xs">
                  <option value="">{t('vmCreate.noneInstallLater')}</option>
                  {#each isos as isoFile (isoFile.path)}
                    <option value={isoFile.path}>{isoFile.name}</option>
                  {/each}
                </select>
              </SettingRow>
            {/if}
          {:else if activeStepId === STEP_IDENTITY}
            <SettingRow
              label={t('common.name')}
              helper={t('vmCreate.nameHelper')}
              error={touched.name || (name && nameError) ? nameError : ''}
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
            {#if availableGroups.length > 0}
              <SettingRow
                label={t('vmCreate.groupsOptional')}
                helper={t('vmCreate.groupAssignmentHelper')}
              >
                <div class="flex flex-wrap gap-1.5 max-w-sm pt-1">
                  {#each availableGroups as g (g.name)}
                    {@const active = selectedGroups.includes(g.name)}
                    <button
                      type="button"
                      aria-pressed={active}
                      onclick={() => {
                        if (active) {
                          selectedGroups = selectedGroups.filter((x) => x !== g.name);
                        } else {
                          selectedGroups = [...selectedGroups, g.name];
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
              </SettingRow>
            {/if}
            {#if isCloudInit}
              <!-- Prominent Cloud-Init Config Section -->
              <div class="space-y-4 pt-2">
                <CloudInitFields
                  bind:user={ciUser}
                  bind:password={ciPassword}
                  bind:hostname={ciHostname}
                  bind:sshKey={ciSSHKey}
                  bind:ipMode={ciIpMode}
                  bind:staticIP={ciStaticIP}
                  bind:gateway={ciGateway}
                  bind:dns={ciDNS}
                  bind:selectedSnippetIds={ciSnippetIds}
                  bind:customUserData={ciCustomUserData}
                  {availableSnippets}
                  error={ciError}
                  isContainer={false}
                  hostnamePlaceholder={name || selectedAppliance?.id || 'my-vm'}
                  idPrefix="ci"
                  onpreview={openCiPreview}
                  oninput={() => (touched.name = true)}
                />
              </div>
            {/if}
            {#if isContainer}
              <!-- LXC credentials (optional provisioning) -->
              <SettingRow
                label={t('vmCreate.lxcCredentialsLabel')}
                helper={t('vmCreate.lxcCredentialsHelper')}
              >
                <div class="w-full max-w-md space-y-3">
                  <label class="flex items-center gap-2 text-sm cursor-pointer select-none">
                    <input
                      type="checkbox"
                      bind:checked={ciEnabled}
                      class="w-4 h-4 rounded border-border cursor-pointer"
                    />
                    {t('vmCreate.cloudInitEnable')}
                  </label>
                  {#if ciEnabled}
                    <CloudInitFields
                      bind:user={ciUser}
                      bind:password={ciPassword}
                      bind:hostname={ciHostname}
                      bind:sshKey={ciSSHKey}
                      bind:ipMode={ciIpMode}
                      bind:staticIP={ciStaticIP}
                      bind:gateway={ciGateway}
                      bind:dns={ciDNS}
                      bind:selectedSnippetIds={ciSnippetIds}
                      bind:customUserData={ciCustomUserData}
                      {availableSnippets}
                      error={ciError}
                      isContainer={true}
                      hostnamePlaceholder="my-vm"
                      idPrefix="lxc-ci"
                      onpreview={openCiPreview}
                      oninput={() => (touched.name = true)}
                    />
                  {/if}
                </div>
              </SettingRow>
            {/if}
            <!-- Start at boot: visible in easy AND advanced mode for every
                 instance type (it used to hide in a collapsed section while
                 defaulting to on). -->
            <div class="pt-2 border-t border-border/60">
              <SettingRow
                label={t('vmCreate.startAtBoot')}
                helper={t('vmCreate.startAtBootHelper')}
              >
                <Switch bind:checked={startAtBoot} ariaLabel={t('vmCreate.startAtBoot')} />
              </SettingRow>
            </div>
          {:else if activeStepId === STEP_RESOURCES}
            <SettingRow
              label={t('common.vcpu')}
              helper={t('vmCreate.vcpusHelper')}
              error={touched.vcpus ? vcpusError : ''}
            >
              <div class="flex flex-col items-end w-full max-w-xs gap-1.5">
                <div class="flex flex-wrap items-center justify-end gap-2 w-full">
                  <QuickPresetPicker
                    bind:value={vcpus}
                    options={[1, 2, 4, 8]}
                    ariaLabel={t('vmCreate.vcpuPresetsLabel')}
                    onselect={() => (touched.vcpus = true)}
                  />
                  <Input
                    type="number"
                    bind:value={vcpus}
                    min="1"
                    max="64"
                    class="w-20 tnum"
                    onblur={() => (touched.vcpus = true)}
                  />
                </div>
                {#if vcpuCapacityPct != null}
                  <ProgressBar
                    value={vcpuCapacityPct}
                    variant={vcpuCapacityPct > 100 ? 'destructive' : 'accent'}
                    label={`${vcpus} / ${myQuota.max_vcpus} vCPU`}
                    class="mt-1 w-full"
                  />
                {/if}
              </div>
            </SettingRow>
            <SettingRow
              label={t('vmDetail.ramLabel')}
              helper={t('vmCreate.ramHelper')}
              error={touched.ramMB ? ramError : ''}
            >
              <div class="flex flex-col items-end w-full max-w-xs gap-1.5">
                <div class="flex flex-wrap items-center justify-end gap-2 w-full">
                  <QuickPresetPicker
                    bind:value={ramMB}
                    options={[2048, 4096, 8192, 16384]}
                    formatLabel={(mb) => `${mb / 1024}G`}
                    ariaLabel={t('vmCreate.ramPresetsLabel')}
                    onselect={() => (touched.ramMB = true)}
                  />
                  <Input
                    type="number"
                    bind:value={ramMB}
                    min="512"
                    step="512"
                    class="w-24 tnum"
                    onblur={() => (touched.ramMB = true)}
                  />
                </div>
                {#if ramCapacityPct != null}
                  <ProgressBar
                    value={ramCapacityPct}
                    variant={ramCapacityPct > 100 ? 'destructive' : 'accent'}
                    label={`${ramMB} / ${myQuota.max_ram_mb} MB`}
                    class="mt-1 w-full"
                  />
                {/if}
              </div>
            </SettingRow>
            {#if !isContainer}
              <SettingRow label={t('vmDetail.minRamLabel')} helper={t('vmDetail.minRamHelper')}>
                <div class="flex items-center gap-2">
                  <Input
                    type="number"
                    min="0"
                    max={ramMB}
                    step="256"
                    bind:value={minRamMB}
                    class="w-28 tnum"
                  />
                  <span class="text-xs text-muted-foreground">
                    {minRamMB > 0 ? `MB (${(minRamMB / 1024).toFixed(1)} GB)` : t('vmDetail.minRamDynamic')}
                  </span>
                </div>
              </SettingRow>

              <SettingRow label={t('vmDetail.iothreadsLabel')} helper={t('vmDetail.iothreadsHelper')}>
                <div class="flex items-center gap-2">
                  <Input
                    type="number"
                    min="0"
                    max="16"
                    bind:value={iothreads}
                    class="w-24 tnum"
                  />
                  <span class="text-xs text-muted-foreground">{iothreads > 0 ? t('vmDetail.iothreadsDedicated') : t('vmDetail.iothreadsDisabled')}</span>
                </div>
              </SettingRow>
            {/if}
            {#if isContainer}
              <!-- LXC: storage pool and root disk size -->
              <SettingRow label={t('common.pool')} helper={t('vmCreate.containerPoolHelper')}>
                <select bind:value={containerStoragePool} class="input max-w-xs">
                  {#if lxcPools.length === 0}
                    <option value="">{t('vmCreate.noContainerPools')}</option>
                  {:else}
                    {#each lxcPools as p (p.name)}
                      <option value={p.name}>{p.name}</option>
                    {/each}
                  {/if}
                </select>
              </SettingRow>
              <SettingRow
                label={t('vmCreate.lxcRootDisk')}
                helper={t('vmCreate.lxcRootDiskHelper')}
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
            {:else if isCloudInit}
              <!-- Cloud-Init Storage Pool & Disk Size -->
              <SettingRow label={t('common.pool')} helper={t('vmCreate.storagePoolHelper')}>
                <select bind:value={storagePool} class="input max-w-xs">
                  {#each vmPools as p (p.name)}
                    <option value={p.name}>{p.name}</option>
                  {/each}
                </select>
              </SettingRow>
              <SettingRow
                label={t('vmCreate.diskSizeGb')}
                helper={t('vmCreate.cloudInitDiskSizeHelper')}
                error={touched.diskSize ? diskSizeError : ''}
              >
                <Input
                  type="number"
                  bind:value={diskSize}
                  min={selectedAppliance?.min_disk_gb || 5}
                  max="1024"
                  class="w-24 tnum"
                  onblur={() => (touched.diskSize = true)}
                />
              </SettingRow>
            {:else}
              <SettingRow
                label={t('vmCreate.useExistingDisk')}
                helper={t('vmCreate.useExistingDiskHelper')}
              >
                <Switch
                  bind:checked={useExistingDisk}
                  ariaLabel={t('vmCreate.toggleExistingDisk')}
                />
              </SettingRow>
              {#if useExistingDisk}
                <SettingRow label={t('vmCreate.diskPool')} helper={t('vmCreate.diskPoolHelper')}>
                  <select
                    bind:value={existingDiskPool}
                    onchange={() => loadExistingVolumes(existingDiskPool)}
                    class="input max-w-xs"
                  >
                    {#each vmPools as p (p.name)}
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
                      {#each existingVolumes as v (v.name)}
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
                    {#each vmPools as p (p.name)}
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
              {/if}
            {/if}
            {#if isKvm && isAdvanced}
              <div class="pt-4 mt-3 border-t border-border/60">
                <p
                  class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2"
                >
                  {t('vmCreate.system')}
                </p>
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
                  <SettingRow
                    label={t('vmCreate.secureBoot')}
                    helper={t('vmCreate.secureBootHelper')}
                  >
                    <Switch bind:checked={secureBoot} ariaLabel={t('vmCreate.toggleSecureBoot')} />
                  </SettingRow>
                  <SettingRow label={t('vmCreate.tpm')} helper={t('vmCreate.tpmHelper')}>
                    <div class="flex items-center gap-3">
                      <Switch bind:checked={tpmEnabled} ariaLabel={t('vmCreate.toggleTpm')} />
                      <select
                        bind:value={tpmVersion}
                        disabled={!tpmEnabled}
                        class="input !py-1 !text-xs w-28 {!tpmEnabled ? 'opacity-50' : ''}"
                      >
                        <option value="2.0">v2.0 (Win11)</option>
                        <option value="1.2">v1.2 (Legacy)</option>
                      </select>
                    </div>
                  </SettingRow>
                {/if}
                <SettingRow label={t('vmCreate.watchdog')} helper={t('vmCreate.watchdogHelper')}>
                  <Switch bind:checked={watchdogEnabled} ariaLabel={t('vmCreate.toggleWatchdog')} />
                </SettingRow>
              </div>
              <div class="pt-4 mt-3 border-t border-border/60">
                <p
                  class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2"
                >
                  {t('vmCreate.cpuAndVideo')}
                </p>
                <SettingRow label={t('vmDetail.cpuModeLabel')} helper={t('vmCreate.cpuModeHelper')}>
                  <select bind:value={cpuMode} class="input max-w-xs">
                    {#each cpuModes as m (m.value)}
                      <option value={m.value}>{m.label}</option>
                    {/each}
                  </select>
                </SettingRow>
                {#if cpuMode === 'custom'}
                  <SettingRow label={t('vmCreate.cpuModel')} helper={t('vmCreate.cpuModelHelper')}>
                    <select bind:value={cpuModelChoice} class="input max-w-xs">
                      {#each cpuModelPresets as m (m)}
                        <option value={m} disabled={!supports('cpu', m)}
                          >{m}{!supports('cpu', m)
                            ? ' — ' + unavailableReason('cpu', m)
                            : ''}</option
                        >
                      {/each}
                      <option value="other">{t('vmCreate.cpuModelOther')}</option>
                    </select>
                  </SettingRow>
                  {#if cpuModelChoice === 'other'}
                    <SettingRow
                      label={t('vmCreate.cpuModelManual')}
                      helper={t('vmCreate.cpuModelHelper')}
                    >
                      <Input
                        bind:value={cpuModel}
                        type="text"
                        placeholder="EPYC"
                        class="max-w-xs"
                      />
                    </SettingRow>
                  {/if}
                {/if}
                <SettingRow
                  label={t('vmCreate.cpuTopology')}
                  helper={t('vmCreate.cpuTopologyHelper')}
                >
                  <Switch bind:checked={cpuTopologyEnabled} ariaLabel={t('vmCreate.cpuTopology')} />
                </SettingRow>
                {#if cpuTopologyEnabled}
                  <SettingRow label={t('vmCreate.cpuTopologyFields')} error={cpuTopologyError}>
                    <div class="flex items-center gap-2">
                      <Input
                        type="number"
                        bind:value={cpuSockets}
                        min="1"
                        class="w-20 tnum"
                        aria-label={t('vmCreate.cpuSockets')}
                      />
                      <span class="text-xs text-muted-foreground">{t('vmCreate.cpuSockets')}</span>
                      <Input
                        type="number"
                        bind:value={cpuCores}
                        min="1"
                        class="w-20 tnum"
                        aria-label={t('vmCreate.cpuCores')}
                      />
                      <span class="text-xs text-muted-foreground">{t('vmCreate.cpuCores')}</span>
                      <Input
                        type="number"
                        bind:value={cpuThreads}
                        min="1"
                        class="w-20 tnum"
                        aria-label={t('vmCreate.cpuThreads')}
                      />
                      <span class="text-xs text-muted-foreground">{t('vmCreate.cpuThreads')}</span>
                    </div>
                  </SettingRow>
                {/if}

                <!-- CPU Priority & Flags -->
                <SettingRow
                  label={t('vmDetail.cpuPriorityLabel')}
                  helper={t('vmDetail.cpuPriorityHelper')}
                  stacked={true}
                >
                  <CpuPrioritySelector bind:value={cpuUnits} />
                </SettingRow>

                <SettingRow
                  label={t('vmDetail.kvmHidden')}
                  helper={t('vmCreate.kvmHiddenDetailedHelper')}
                >
                  <Switch bind:checked={kvmHidden} ariaLabel={t('vmDetail.kvmHidden')} />
                </SettingRow>

                <SettingRow
                  label={t('vmDetail.cpuFlags')}
                  helper={t('vmCreate.cpuFlagsDetailedHelper')}
                  stacked={true}
                >
                  <CpuFlagPicker
                    bind:flags={cpuFlags}
                    available={capabilities.cpuFlags}
                    common={COMMON_CPU_FLAGS}
                  />
                </SettingRow>
                <SettingRow
                  label={t('vmDetail.videoModelLabel')}
                  helper={t('vmCreate.videoModelHelper')}
                >
                  <select bind:value={videoModel} class="input max-w-xs">
                    {#each videoModels as m (m.value)}
                      <option value={m.value} disabled={!supports('video', m.value)}>
                        {m.value === 'none' ? t('vmCreate.serialOnly') : m.label}{!supports(
                          'video',
                          m.value
                        )
                          ? ' — ' + unavailableReason('video', m.value)
                          : ''}
                      </option>
                    {/each}
                  </select>
                </SettingRow>
              </div>
            {/if}
            <!-- Fase 5: advanced options (collapsible) -->
            <div class="mt-4 border border-border rounded-lg bg-muted/30 p-4">
              <details>
                <summary class="cursor-pointer text-sm font-semibold text-foreground select-none">
                  {t('vmCreate.advancedOptions')}
                </summary>
                <div class="mt-3 space-y-4">
                  {#if isContainer}
                    <SettingRow
                      label={t('vmCreate.privileged')}
                      helper={t('vmCreate.privilegedHelper')}
                    >
                      <Switch bind:checked={privileged} ariaLabel={t('vmCreate.privileged')} />
                    </SettingRow>
                    <SettingRow label={t('vmCreate.nesting')} helper={t('vmCreate.nestingHelper')}>
                      <Switch bind:checked={nesting} ariaLabel={t('vmCreate.nesting')} />
                    </SettingRow>
                    <SettingRow
                      label={t('vmCreate.profiles')}
                      helper={t('vmCreate.profilesHelper')}
                    >
                      <div class="flex flex-wrap gap-2 max-w-sm">
                        {#each incusProfiles as p (p)}
                          {@const isSelected = profiles.includes(p)}
                          <button
                            type="button"
                            aria-pressed={isSelected}
                            class="px-2.5 py-1 rounded-md text-xs font-mono transition-all flex items-center gap-1.5 border cursor-pointer {isSelected
                              ? 'bg-accent/15 border-accent text-accent font-medium shadow-sm'
                              : 'bg-surface-secondary/60 border-border/50 text-muted-foreground hover:border-border hover:text-foreground'}"
                            onclick={() => {
                              if (p === 'default') return;
                              if (isSelected) {
                                profiles = profiles.filter((x) => x !== p);
                              } else {
                                profiles = [...profiles, p];
                              }
                            }}
                          >
                            <span
                              class="w-1.5 h-1.5 rounded-full {isSelected
                                ? 'bg-accent'
                                : 'bg-muted-foreground/40'}"
                            ></span>
                            <span>{p}</span>
                            {#if p === 'default'}
                              <span
                                class="text-[9px] bg-accent/20 text-accent px-1 rounded uppercase tracking-wider font-semibold"
                                >{t('vmCreate.profileActiveBadge')}</span
                              >
                            {:else if isSelected}
                              <svg
                                class="w-3 h-3 text-accent"
                                fill="none"
                                viewBox="0 0 24 24"
                                stroke="currentColor"
                              >
                                <path
                                  stroke-linecap="round"
                                  stroke-linejoin="round"
                                  stroke-width="2"
                                  d="M5 13l4 4L19 7"
                                />
                              </svg>
                            {/if}
                          </button>
                        {/each}
                      </div>
                    </SettingRow>
                  {:else}
                    <SettingRow
                      label={t('vmCreate.bootOrder')}
                      helper={t('vmCreate.bootOrderHelper')}
                    >
                      <select bind:value={bootOrder} class="input max-w-xs">
                        {#each bootOrderOptions as opt (opt.value)}
                          <option value={opt.value}>{opt.label}</option>
                        {/each}
                      </select>
                    </SettingRow>
                  {/if}
                </div>
              </details>
            </div>
          {:else if activeStepId === STEP_NETWORK}
            <!-- Primary Network (KVM) -->
            <SettingRow label={t('vmDetail.networkLabel')} helper={t('vmCreate.networkHelper')}>
              <select bind:value={network} class="input max-w-xs">
                {#each vmNetworks as net (net.name)}
                  <option value={net.name}>{networkLabel(net)}</option>
                {/each}
              </select>
            </SettingRow>

            {#if isAdvanced}
              <SettingRow label={t('vmDetail.adapter')} helper={t('vmCreate.adapterHelper')}>
                <select bind:value={networkModel} class="input max-w-xs">
                  {#each networkModels as m (m.value)}
                    <option value={m.value} disabled={!supports('network', m.value)}>
                      {m.label}{!supports('network', m.value)
                        ? ' — ' + unavailableReason('network', m.value)
                        : ''}
                    </option>
                  {/each}
                </select>
              </SettingRow>

              <!-- Secondary Network Interfaces -->
              <div class="pt-3 mt-1 border-t border-border">
                <div class="flex items-center justify-between mb-2">
                  <div>
                    <p class="text-sm font-medium">
                      {t('vmCreate.extraNics')}
                    </p>
                    <p class="text-xs text-muted-foreground">
                      {t('vmCreate.extraNicsHelper')}
                    </p>
                  </div>
                  <Button size="xs" variant="outline" onclick={addExtraNic} class="cursor-pointer">
                    <Icon name="plus" size={12} class="mr-1" />
                    {t('vmCreate.addExtraNic')}
                  </Button>
                </div>
                {#if extraNics.length > 0}
                  <div class="space-y-2">
                    {#each extraNics as n (n._id)}
                      <div
                        class="flex flex-wrap items-center gap-2 p-2.5 rounded-lg border border-border bg-muted/20"
                      >
                        <select bind:value={n.network} class="input !h-8 !text-xs max-w-[12rem]">
                          {#each vmNetworks as net (net.name)}
                            <option value={net.name}>{networkLabel(net)}</option>
                          {/each}
                        </select>
                        {#if !isContainer}
                          <select bind:value={n.model} class="input !h-8 !text-xs max-w-[9rem]">
                            {#each networkModels as m (m.value)}
                              <option value={m.value} disabled={!supports('network', m.value)}
                                >{m.label}</option
                              >
                            {/each}
                          </select>
                        {/if}
                        <div class="flex items-center gap-1">
                          <span class="text-xs text-muted-foreground">VLAN:</span>
                          <Input
                            type="number"
                            min="1"
                            max="4094"
                            bind:value={n.vlanTag}
                            placeholder={t('common.optional')}
                            class="w-20 !h-8 !text-xs tnum"
                          />
                        </div>
                        <button
                          type="button"
                          onclick={() => removeExtraNic(n._id)}
                          class="ml-auto text-xs text-destructive hover:opacity-80 px-2 py-1 cursor-pointer"
                        >
                          {t('common.remove')}
                        </button>
                      </div>
                    {/each}
                  </div>
                {/if}
              </div>
              <SettingRow
                label={t('vmCreate.audioDevice')}
                helper={t('vmCreate.audioDeviceHelper')}
              >
                <select bind:value={audioModel} class="input max-w-xs">
                  {#each audioModels as m (m.value)}
                    <option value={m.value} disabled={!supports('sound', m.value)}>
                      {m.label}{!supports('sound', m.value)
                        ? ' — ' + unavailableReason('sound', m.value)
                        : ''}
                    </option>
                  {/each}
                </select>
              </SettingRow>
              <SettingRow label={t('vmCreate.serialPort')} helper={t('vmCreate.serialPortHelper')}>
                <Switch bind:checked={serialPort} ariaLabel={t('vmCreate.serialPort')} />
              </SettingRow>
              <!-- Advanced Disk Settings (KVM) -->
              {#if isAdvanced}
                {#if !useExistingDisk}
                  <SettingRow
                    label={t('vmCreate.diskFormat')}
                    helper={t('vmCreate.diskFormatHelper')}
                  >
                    <select bind:value={diskFormat} class="input max-w-xs">
                      {#each diskFormatOptions as o (o.value)}
                        <option value={o.value}>{o.label}</option>
                      {/each}
                    </select>
                  </SettingRow>
                {/if}
                <SettingRow label={t('vmDetail.busLabel')} helper={t('vmCreate.diskBusHelper')}>
                  <select bind:value={diskBus} class="input max-w-xs">
                    {#each diskBusOptions as o (o.value)}
                      <option value={o.value} disabled={!supports('diskbus', o.value)}>
                        {o.label}{!supports('diskbus', o.value)
                          ? ' — ' + unavailableReason('diskbus', o.value)
                          : ''}
                      </option>
                    {/each}
                  </select>
                </SettingRow>
                <SettingRow
                  label={t('vmCreate.diskCacheIO')}
                  helper={t('vmCreate.diskCacheIOHelper')}
                >
                  <Switch bind:checked={diskCacheIO} ariaLabel={t('vmCreate.diskCacheIO')} />
                </SettingRow>
                <SettingRow
                  label={t('vmCreate.diskDiscard')}
                  helper={t('vmCreate.diskDiscardHelper')}
                >
                  <Switch bind:checked={diskDiscard} ariaLabel={t('vmCreate.diskDiscard')} />
                </SettingRow>
                {#if osType === 'windows'}
                  <SettingRow
                    label={t('vmCreate.virtioDriversIso')}
                    helper={t('vmCreate.virtioDriversHelper')}
                  >
                    <select bind:value={virtioISO} class="input max-w-xs">
                      <option value="">{t('common.none')}</option>
                      {#each isos as isoFile (isoFile.path)}
                        <option value={isoFile.path}>{isoFile.name}</option>
                      {/each}
                    </select>
                  </SettingRow>
                {/if}

                <!-- Secondary disks -->
                <div class="pt-3 mt-1 border-t border-border">
                  <div class="flex items-center justify-between mb-2">
                    <div>
                      <p class="text-sm font-medium">{t('vmCreate.extraDisks')}</p>
                      <p class="text-xs text-muted-foreground">{t('vmCreate.extraDisksHelper')}</p>
                    </div>
                    <Button
                      size="xs"
                      variant="outline"
                      onclick={addExtraDisk}
                      class="cursor-pointer"
                    >
                      <Icon name="plus" size={12} class="mr-1" />
                      {t('vmCreate.addExtraDisk')}
                    </Button>
                  </div>
                  {#if extraDisks.length > 0}
                    <div class="space-y-2">
                      {#each extraDisks as d (d._id)}
                        <div
                          class="flex flex-wrap items-center gap-2 p-2.5 rounded-lg border border-border bg-muted/20"
                        >
                          <Input
                            type="number"
                            min="1"
                            bind:value={d.sizeGB}
                            class="w-20 tnum"
                            aria-label={t('vmCreate.diskSizeGb')}
                          />
                          <span class="text-xs text-muted-foreground">GB</span>
                          <select bind:value={d.pool} class="input !h-8 !text-xs max-w-[10rem]">
                            {#each vmPools as p (p.name)}
                              <option value={p.name}>{p.name}</option>
                            {/each}
                          </select>
                          <select bind:value={d.bus} class="input !h-8 !text-xs max-w-[8rem]">
                            {#each diskBusOptions as o (o.value)}
                              <option value={o.value} disabled={!supports('diskbus', o.value)}
                                >{o.label}</option
                              >
                            {/each}
                          </select>
                          <select bind:value={d.format} class="input !h-8 !text-xs max-w-[7rem]">
                            {#each diskFormatOptions as o (o.value)}
                              <option value={o.value}>{o.label}</option>
                            {/each}
                          </select>
                          <button
                            type="button"
                            onclick={() => removeExtraDisk(d._id)}
                            class="ml-auto text-xs text-destructive hover:opacity-80 px-2 py-1 cursor-pointer"
                          >
                            {t('common.remove')}
                          </button>
                        </div>
                      {/each}
                    </div>
                  {/if}
                </div>
              {/if}
            {/if}
          {:else}
            <VmReviewStep rows={summaryRows} {kindLabel} {kindTheme} {steps} onedit={goToStep} />
          {/if}
        </Card>

        {#if !isAdvanced}
          <div
            class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 p-3.5 rounded-xl border border-border/80 bg-muted/20 text-xs text-muted-foreground"
          >
            <div class="flex items-center gap-2">
              <Icon name="settings" size={15} class="text-accent shrink-0" />
              <span>{t('vmCreate.switchToAdvancedNotice')}</span>
            </div>
            <button
              type="button"
              onclick={() => setWizardMode('advanced')}
              class="text-accent hover:underline font-semibold shrink-0 cursor-pointer self-start sm:self-auto"
            >
              {t('vmCreate.switchToAdvancedAction')} &rarr;
            </button>
          </div>
        {/if}

        <WizardNav
          {isFirstStep}
          {isLastStep}
          {loading}
          blocked={currentStepBlocked}
          canSubmit={isValid}
          errorText={primaryError}
          onprev={prevStep}
          oncancel={onBack}
          submitLabel={isContainer ? t('vmCreate.createContainer') : ''}
        />
      </form>
    </div>
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

<!-- Cloud-Init #cloud-config Preview Modal -->
<CloudInitPreviewDialog
  bind:open={showPreviewModal}
  content={previewContent}
  loading={previewLoading}
/>
