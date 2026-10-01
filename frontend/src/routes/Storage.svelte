<script>
  import { SvelteSet } from 'svelte/reactivity';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Alert from '$lib/components/Alert.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import ProgressBar from '$lib/components/ProgressBar.svelte';
  import StorageBreakdownBar from '$lib/components/StorageBreakdownBar.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import StatCard from '$lib/components/StatCard.svelte';
  import { formatRate, formatBytes, formatETA } from '$lib/utils/format.js';
  import { upsertTask, updateTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { trackImageJob, onImageJobDone } from '$lib/stores/imageJobs.svelte.js';
  import { onMount, onDestroy } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button, buttonVariants } from '$lib/components/ui/button';
  import { cn } from '$lib/utils.js';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import RemoteFolderBrowser from '$lib/components/RemoteFolderBrowser.svelte';
  import LocalFolderBrowser from '$lib/components/LocalFolderBrowser.svelte';
  import ErrorModal from '$lib/components/ErrorModal.svelte';
  import CreateVolumeInlineForm from '$lib/components/CreateVolumeInlineForm.svelte';
  import SmartModal from '$lib/components/SmartModal.svelte';
  import * as Dialog from '$lib/components/ui/dialog';
  import { navigate } from '$lib/router.svelte.js';
  import { t, htmlVar } from '../lib/i18n.svelte.js';
  import {
    hasPurpose,
    isIsoOnly,
    purposeLabel,
    purposeList,
    poolForFolder,
    poolsForFolders,
    movablePools,
    isBuiltinPool,
  } from '$lib/purpose.js';

  // Active Tab: 'pools' | 'vm-disks' | 'lxc-disks' | 'isos' | 'lxc-templates' | 'cloud-init'
  let activeTab = $state('pools');

  let pools = $state([]);
  let volumes = $state([]);
  // Per-pool storage split (VM disks / ISOs / backups / free) powering
  // the segmented usage bars. Keyed by pool name for O(1) lookup while
  // rendering the grid. Non-fatal: if the endpoint fails the cards fall
  // back to the plain allocated/capacity progress bar.
  let breakdownByPool = $state({});

  async function loadBreakdown() {
    try {
      const list = (await api.getStorageBreakdown()) || [];
      const map = {};
      for (const b of list) map[b.pool] = b;
      breakdownByPool = map;
    } catch {
      breakdownByPool = {};
    }
  }
  let selectedDiskPool = $state('');
  let selectedLxcPool = $state('');
  let selectedISOPool = $state('__all__');

  let loading = $state(true);
  let error = $state('');
  let isos = $state([]);
  let lxcImages = $state([]);
  let cloudInitSnippets = $state([]);

  // Search & Filter state
  let diskSearch = $state('');
  let lxcDiskSearch = $state('');
  let isoSearch = $state('');
  let lxcSearch = $state('');
  let lxcFilter = $state('all'); // 'all', 'official', 'local'
  let snippetSearch = $state('');

  const isoPools = $derived(pools.filter((p) => hasPurpose(p, 'iso')));
  const lxcPools = $derived(
    pools.filter(
      (p) =>
        hasPurpose(p, 'container') || (p.type && p.type.includes('incus')) || p.name === 'default'
    )
  );
  const kvmPools = $derived(pools.filter((p) => hasPurpose(p, 'disk') && p.name !== 'default'));

  // Upload & Download state
  let uploading = $state(false);
  let uploadProgress = $state(0);
  let uploadingDisk = $state(false);
  let uploadDiskProgress = $state(0);
  let downloadProgress = $state(0);
  let downloading = $state(false);
  let downloadMessage = $state('');
  let downloadInterval = $state(null);
  let downloadingRef = $state('');

  // Pool Creation Dialog
  let showCreatePool = $state(false);
  let poolName = $state('');
  let poolPath = $state('');
  let poolPurpose = $state('disk');
  let poolKind = $state('dir');
  let poolSourceFormat = $state('nfs');
  let poolSourceHost = $state('');
  let poolSourcePort = $state('');
  let poolSourceDevice = $state('');
  let poolSourceDir = $state('');
  let poolSourceUsername = $state('');
  let poolSourcePassword = $state('');
  let poolChapUsername = $state('');
  let poolChapPassword = $state('');
  let poolCreating = $state(false);
  // Where a directory pool lives: 'system' roots it under the pools
  // directory on the system disk (the server derives the path from the
  // pool name), 'custom' is an explicit path on another disk.
  //
  // 'system' is the default because it is the answer for an operator
  // who has not mounted a second disk, and typing the path by hand is
  // the step where a typo quietly creates a pool rooted somewhere it
  // should not be.
  let poolLocation = $state('system');
  // Reported by GET /api/host so the preview shows the real directory
  // instead of a hardcoded /opt/webkvm an operator may have moved.
  let hostPoolsDir = $state('/opt/webkvm/pools');

  // Pool Retag Dialog. Retagging writes pool-purposes.json only — no
  // data moves and the pool keeps running — so an operator who picked
  // the wrong purpose at creation time fixes it here instead of
  // deleting and recreating the pool.
  let showRetagPool = $state(false);
  let retagPoolName = $state('');
  let retagPurpose = $state('disk');
  let retagCurrent = $state('');
  let retagSaving = $state(false);

  // Volume creation form lives in CreateVolumeInlineForm.svelte; Storage
  // only keeps the trigger flag.
  let showCreateVol = $state(false);

  // Volume Resize Dialog
  let showResizeVol = $state(false);
  let resizeVolName = $state('');
  let resizeVolSize = $state(20);
  let resizeVolCurrent = $state(0);

  // Host Physical Disks state
  let hostDisks = $state([]);
  let hostDisksLoading = $state(false);
  let selectedSmartDisk = $state(null);
  let showSmartModal = $state(false);
  let scrubbingPool = $state({});
  // Automount units with no .mount sibling: they hide the real contents
  // of their mountpoint behind a failing autofs.
  let orphanMounts = $state([]);
  let showInitDisk = $state(false);
  let initDiskPath = $state('');
  let initDiskName = $state('');
  let initDiskMount = $state('');
  let initDiskFs = $state('ext4');
  let initDiskCreatePool = $state(true);
  let initDiskBusy = $state(false);

  // --- ZFS State ---
  let zpools = $state([]);
  let zvols = $state([]);
  let zfsLoading = $state(false);

  // Create ZPool Modal State
  let showCreateZPool = $state(false);
  let createZPoolName = $state('');
  let createZPoolTopology = $state('stripe');
  let createZPoolDisks = $state([]);
  let creatingZPool = $state(false);

  // Create ZVol Modal State
  let showCreateZVol = $state(false);
  let createZVolPool = $state('');
  let createZVolName = $state('');
  let createZVolSizeGB = $state(20);
  let createZVolSparse = $state(true);
  let creatingZVol = $state(false);

  // --- Software RAID State ---
  let showCreateRaid = $state(false);
  let createRaidLevel = $state('1');
  let createRaidDevice = $state('');
  let createRaidDisks = $state([]);
  let creatingRaid = $state(false);
  // Cada carpeta marcada con naturaleza crea su propio pool
  // independiente en su subcarpeta (discos -> libvirt, isos ->
  // libvirt, contenedores -> Incus, backups -> libvirt, plantillas
  // -> libvirt). backups y plantillas ya NO son neutrales: también
  // tienen pool. El diálogo no ofrece el desplegable
  // "Propósito del Pool" porque los pools se DERIVAN de las
  // carpetas marcadas: el nombre de cada pool se muestra en la
  // propia fila de su carpeta.
  //
  // Los mapas carpeta->naturaleza->sufijo viven en $lib/purpose.js
  // (poolForFolder/poolsForFolders), que replica el backend y tiene
  // tests que fijan la paridad.
  //
  // $derived (not a plain const) so the labels/descriptions re-render
  // when the user switches language while this route is mounted.
  const initDiskSubfolders = $derived([
    { id: 'isos', path: '/isos', label: 'ISOs', desc: t('storage.subfolderIsosDesc') },
    {
      id: 'discos',
      path: '/discos',
      label: t('storage.subfolderDiscosLabel'),
      desc: t('storage.subfolderDiscosDesc'),
    },
    {
      id: 'contenedores',
      path: '/contenedores',
      label: t('storage.subfolderContenedoresLabel'),
      desc: t('storage.subfolderContenedoresDesc'),
    },
    {
      id: 'backups',
      path: '/backups',
      label: t('storage.subfolderBackupsLabel'),
      desc: t('storage.subfolderBackupsDesc'),
    },
    {
      id: 'plantillas',
      path: '/plantillas',
      label: t('storage.subfolderPlantillasLabel'),
      desc: t('storage.subfolderPlantillasDesc'),
    },
  ]);
  // Multi-propósito por defecto: las cinco carpetas marcadas (KVM +
  // LXC + ISO + backups + plantillas); el usuario desmarca lo que no
  // quiera en este disco. Compartido con openInitDisk() para que
  // abrir el diálogo nunca restaure una lista anticuada.
  const DEFAULT_INIT_SUBFOLDERS = ['isos', 'discos', 'contenedores', 'backups', 'plantillas'];
  let initDiskSelectedSubfolders = $state([...DEFAULT_INIT_SUBFOLDERS]);
  // Pools que se van a crear: uno independiente por cada carpeta
  // marcada con naturaleza, enraizado en su subcarpeta. Sin carpetas
  // con naturaleza no se crea NINGÚN pool (el backend solo aplica el
  // pool único en la raíz para clientes antiguos que ni siquiera
  // envían `subfolders`).
  const poolsToCreate = $derived(poolsForFolders(initDiskSelectedSubfolders, initDiskName));
  let initDiskRegisterBackup = $state(true);
  // "mount" = non-destructive: mount an already-formatted filesystem.
  // "format" = destructive: partition + format the whole disk.
  let initDiskMode = $state('format');
  let initDiskDevice = $state('');
  // Boot-resilience mount options.
  let initDiskNoFail = $state(true);
  let initDiskAutomount = $state(true);
  let initDiskReadOnly = $state(false);
  let initDiskExtraOptions = $state('');
  // Filesystems the host can actually format with (mkfs binary present).
  // Fetched lazily when the init-disk dialog opens so the choices
  // offered always match what this specific host supports — see
  // supportedFilesystems in backend/internal/api/host_disks.go.
  let availableFilesystems = $state([]);

  // Filesystem IDs that have a curated description key in i18n
  // (storage.fs<Id>Desc). t() never returns falsy for a missing key (it
  // returns the raw key), so `t(key) || fs.label` never actually fell
  // back — this explicit allowlist is what makes the fallback real for
  // any future filesystem the backend might report that isn't curated.
  const fsDescKeys = new Set([
    'storage.fsExt4Desc',
    'storage.fsXfsDesc',
    'storage.fsBtrfsDesc',
    'storage.fsF2fsDesc',
  ]);

  async function loadFilesystems() {
    try {
      const list = (await api.listFilesystems()) || [];
      availableFilesystems = list.filter((fs) => fs.available);
      // If the currently-selected FS isn't available on this host,
      // fall back to the first one that is (ext4 first in the catalog).
      if (availableFilesystems.length && !availableFilesystems.some((fs) => fs.id === initDiskFs)) {
        initDiskFs = availableFilesystems[0].id;
      }
    } catch {
      // Fall back to the historical hardcoded choices if the endpoint
      // is unreachable (e.g. older backend during a rolling deploy).
      availableFilesystems = [
        { id: 'ext4', label: 'ext4', available: true },
        { id: 'xfs', label: 'XFS', available: true },
      ];
    }
  }

  async function loadHostDisks() {
    hostDisksLoading = true;
    try {
      hostDisks = (await api.listHostDisks()) || [];
    } catch {
      hostDisks = [];
    } finally {
      hostDisksLoading = false;
    }
    // Independent of the disk list: a broken automount declares no
    // device, so it never shows up as a disk row. Failing this call
    // must not blank the table, hence the separate try.
    try {
      orphanMounts = (await api.listOrphanMounts()) || [];
    } catch {
      orphanMounts = [];
    }
    if (auth.isAdmin()) {
      loadZfsData();
    }
  }

  async function loadZfsData() {
    zfsLoading = true;
    try {
      const [poolsRes, volsRes] = await Promise.all([
        api.listHostZpools().catch(() => []),
        api.listHostZVols().catch(() => []),
      ]);
      zpools = poolsRes || [];
      zvols = volsRes || [];
    } catch {
      zpools = [];
      zvols = [];
    } finally {
      zfsLoading = false;
    }
  }

  async function handleScrub(poolName, action = 'start') {
    scrubbingPool[poolName] = true;
    try {
      await api.scrubZpool(poolName, action);
      toast.success(
        action === 'stop' ? `Scrub cancelado para ${poolName}` : `Scrub iniciado para ${poolName}`
      );
      await loadZfsData();
    } catch (e) {
      toast.error('Error en ZFS scrub: ' + e.message);
    } finally {
      scrubbingPool[poolName] = false;
    }
  }

  function openCreateZPoolDialog() {
    createZPoolName = '';
    createZPoolTopology = 'stripe';
    createZPoolDisks = [];
    showCreateZPool = true;
  }

  function minDisksForTopology(topo) {
    switch (topo) {
      case 'mirror':
        return 2;
      case 'raidz1':
        return 3;
      case 'raidz2':
        return 4;
      default:
        return 1;
    }
  }

  async function doCreateZPool() {
    const min = minDisksForTopology(createZPoolTopology);
    if (!createZPoolName || createZPoolDisks.length < min) return;
    creatingZPool = true;
    try {
      await api.createHostZpool({
        name: createZPoolName.trim(),
        topology: createZPoolTopology,
        devices: createZPoolDisks,
      });
      showCreateZPool = false;
      await loadZfsData();
      await loadHostDisks();
    } catch (e) {
      showStorageErr('Create ZFS Pool Error', e);
    } finally {
      creatingZPool = false;
    }
  }

  function openCreateZVolDialog(defaultPool = '') {
    createZVolPool = defaultPool || zpools[0]?.name || '';
    createZVolName = '';
    createZVolSizeGB = 20;
    createZVolSparse = true;
    showCreateZVol = true;
  }

  async function doCreateZVol() {
    if (!createZVolPool || !createZVolName || createZVolSizeGB <= 0) return;
    creatingZVol = true;
    try {
      await api.createHostZvol({
        pool: createZVolPool,
        name: createZVolName.trim(),
        size_gb: Number(createZVolSizeGB),
        sparse: createZVolSparse,
      });
      showCreateZVol = false;
      await loadZfsData();
    } catch (e) {
      showStorageErr('Create ZVol Error', e);
    } finally {
      creatingZVol = false;
    }
  }

  function openCreateRaidDialog() {
    createRaidLevel = '1';
    createRaidDevice = '';
    createRaidDisks = [];
    showCreateRaid = true;
  }

  function minDisksForRaid(level) {
    switch (level) {
      case '5':
        return 3;
      case '6':
      case '10':
        return 4;
      default:
        return 2;
    }
  }

  async function doCreateRaid() {
    const min = minDisksForRaid(createRaidLevel);
    if (createRaidDisks.length < min) return;
    creatingRaid = true;
    try {
      const res = await api.createHostRaid({
        name: createRaidDevice.trim() || undefined,
        level: createRaidLevel,
        devices: createRaidDisks,
      });
      showCreateRaid = false;
      await loadHostDisks();
      alert(t('storage.raidCreatedSuccess', { device: res.device }));
    } catch (e) {
      showStorageErr('Create RAID Error', e);
    } finally {
      creatingRaid = false;
    }
  }

  function openInitDiskDialog(disk) {
    initDiskPath = disk ? disk.path : hostDisks.find((d) => !d.is_system)?.path || '';
    const cleanName = initDiskPath.replace('/dev/', '').replace(/[^a-zA-Z0-9]/g, '');
    initDiskName = `storage-${cleanName || '1'}`;
    initDiskMount = `/mnt/storage-${cleanName || '1'}`;
    initDiskFs = 'ext4';
    initDiskCreatePool = true;
    initDiskSelectedSubfolders = [...DEFAULT_INIT_SUBFOLDERS];
    initDiskRegisterBackup = true;
    initDiskMode =
      disk && (disk.fstype || disk.children?.some((c) => c.fstype)) ? 'mount' : 'format';
    initDiskDevice = '';
    initDiskNoFail = true;
    initDiskAutomount = true;
    initDiskReadOnly = false;
    initDiskExtraOptions = '';
    showInitDisk = true;
    loadFilesystems();
  }

  // Preview of the mount options that will land in the systemd unit.
  const initDiskOptionsPreview = $derived.by(() => {
    const opts = ['defaults', 'noatime'];
    if (initDiskNoFail) opts.push('nofail', 'x-systemd.device-timeout=10');
    if (initDiskAutomount) opts.push('x-systemd.automount');
    if (initDiskReadOnly) opts.push('ro');
    const extra = initDiskExtraOptions
      .split(',')
      .map((o) => o.trim())
      .filter((o) => /^[a-zA-Z0-9][a-zA-Z0-9_\-./:=@+]*$/.test(o));
    return [...opts, ...extra].join(',');
  });

  // Per-filesystem mount-option presets. These are the options worth
  // setting for a WebKVM storage pool specifically (VM disk images and
  // container rootfs), not a general mount(8) reference — e.g. Btrfs
  // compression pays off on sparse qcow2 files, and F2FS discard
  // matters because the pool usually lives on the NVMe this host
  // boots from.
  //
  // `opt` is the literal fstab option appended to the unit's Options=
  // line; the UI toggles it in/out of initDiskExtraOptions so the live
  // preview and the request body stay in sync with what's shown.
  const FS_OPTION_PRESETS = {
    btrfs: [
      { opt: 'compress=zstd', descKey: 'storage.presetBtrfsZstd' },
      { opt: 'compress=lzo', descKey: 'storage.presetBtrfsLzo' },
      { opt: 'autodefrag', descKey: 'storage.presetBtrfsAutodefrag' },
      { opt: 'space_cache=v2', descKey: 'storage.presetBtrfsSpaceCache' },
    ],
    f2fs: [
      { opt: 'compress_algorithm=zstd', descKey: 'storage.presetF2fsZstd' },
      { opt: 'compress_chksum', descKey: 'storage.presetF2fsChksum' },
      { opt: 'discard', descKey: 'storage.presetF2fsDiscard' },
    ],
    ext4: [
      { opt: 'commit=60', descKey: 'storage.presetExt4Commit' },
      { opt: 'data=writeback', descKey: 'storage.presetExt4Writeback' },
    ],
    xfs: [
      { opt: 'logbufs=8', descKey: 'storage.presetXfsLogbufs' },
      { opt: 'allocsize=64m', descKey: 'storage.presetXfsAllocsize' },
    ],
  };

  // Presets for the filesystem actually being applied: the chosen one
  // in "format" mode, or the detected one in "mount" mode (mounting an
  // existing Btrfs disk should still offer compression).
  const activeFsPresets = $derived.by(() => {
    let fs = initDiskFs;
    if (initDiskMode === 'mount') {
      const match = initDiskExistingFs.find((f) => f.path === initDiskDevice);
      fs = match?.fstype || initDiskExistingFs[0]?.fstype || '';
    }
    return FS_OPTION_PRESETS[fs] || [];
  });

  function currentExtraOpts() {
    return initDiskExtraOptions
      .split(',')
      .map((o) => o.trim())
      .filter(Boolean);
  }

  function isPresetActive(opt) {
    return currentExtraOpts().includes(opt);
  }

  // Toggling a preset rewrites initDiskExtraOptions, so the text field
  // stays the single source of truth (a user can still type options by
  // hand and the chips reflect it).
  function togglePreset(opt) {
    const opts = currentExtraOpts();
    const idx = opts.indexOf(opt);
    if (idx >= 0) {
      opts.splice(idx, 1);
    } else {
      // Mutually-exclusive families: picking compress=zstd must drop
      // compress=lzo, otherwise the mount fails with conflicting opts.
      const family = opt.split('=')[0];
      if (opt.includes('=')) {
        for (let i = opts.length - 1; i >= 0; i--) {
          if (opts[i].split('=')[0] === family) opts.splice(i, 1);
        }
      }
      opts.push(opt);
    }
    initDiskExtraOptions = opts.join(',');
  }

  // Existing filesystems discovered on the selected disk, offered as
  // explicit mount targets in "mount" mode.
  const initDiskExistingFs = $derived.by(() => {
    const disk = hostDisks.find((d) => d.path === initDiskPath);
    if (!disk) return [];
    const out = [];
    if (disk.fstype) out.push({ path: disk.path, fstype: disk.fstype, label: disk.path });
    for (const c of disk.children || []) {
      if (c.fstype) out.push({ path: c.path, fstype: c.fstype, label: `${c.path} (${c.fstype})` });
    }
    return out;
  });

  // Consolidated status for a physical disk row. The table already
  // shows fstype and mountpoints separately, but at a glance an
  // operator mostly needs to know one thing before clicking "Wipe":
  // is this safe to touch? Mounted disks (or the OS disk) are refused
  // by the backend anyway — surfacing it here avoids the round-trip.
  //
  // `mount_state` comes from the backend's intent-aware resolver and is
  // authoritative: a disk can have zero mountpoints in the kernel's
  // view and still be claimed by an idle systemd automount, an fstab
  // entry or a libvirt pool. Trusting the (empty) mountpoint list alone
  // is what used to present such a disk as free to format.
  function hostDiskStatus(disk) {
    if (disk.is_system) {
      return { key: 'system', label: t('storage.diskProtected'), icon: 'shield' };
    }
    const mounts = [
      ...(disk.mountpoints || []),
      ...(disk.children || []).flatMap((c) => c.mountpoints || []),
    ].filter(Boolean);

    if (disk.mount_state === 'configured') {
      return {
        key: 'reserved',
        label: mounts.length
          ? t('storage.diskReserved', { path: mounts[0] })
          : t('storage.diskReservedNoPath'),
        icon: 'clock',
        title: disk.mount_detail || mounts.join(', '),
      };
    }

    if (disk.mount_state === 'active' || mounts.length > 0) {
      return {
        key: 'inuse',
        label: t('storage.diskInUse', { path: mounts[0] || '' }),
        icon: 'check',
        title: mounts.join(', '),
      };
    }
    return { key: 'free', label: t('storage.diskAvailable'), icon: 'hardDrive' };
  }

  function toggleInitSubfolder(id) {
    if (initDiskSelectedSubfolders.includes(id)) {
      initDiskSelectedSubfolders = initDiskSelectedSubfolders.filter((x) => x !== id);
    } else {
      // Un pool independiente por carpeta marcada — nunca un pool
      // unificado: el backend deriva los pools de `subfolders`.
      initDiskSelectedSubfolders = [...initDiskSelectedSubfolders, id];
    }
  }

  async function performInitDisk() {
    if (!initDiskPath || !initDiskName.trim()) return;
    initDiskBusy = true;
    try {
      const res = await api.initHostDiskDirectory({
        disk_path: initDiskPath,
        volume_name: initDiskName.trim(),
        mount_point: initDiskMount.trim() || `/mnt/${initDiskName.trim()}`,
        filesystem: initDiskMode === 'format' ? initDiskFs : undefined,
        create_pool: initDiskCreatePool,
        mode: initDiskMode,
        device: initDiskMode === 'mount' && initDiskDevice ? initDiskDevice : undefined,
        nofail: initDiskNoFail,
        automount: initDiskAutomount,
        read_only: initDiskReadOnly,
        mount_options: initDiskExtraOptions
          .split(',')
          .map((o) => o.trim())
          .filter((o) => /^[a-zA-Z0-9][a-zA-Z0-9_\-./:=@+]*$/.test(o)),
        subfolders: initDiskSelectedSubfolders,
        register_backup_target:
          initDiskRegisterBackup && initDiskSelectedSubfolders.includes('backups'),
      });
      // El registro de pools es best-effort: el disco ya está
      // formateado y montado, así que un pool que falla NO aborta la
      // inicialización. Sin mirar el informe `pools`, un fallo se
      // vería como un éxito verde y el operador descubriría el pool
      // ausente mucho después.
      // `createdPools`, no `pools`: el módulo ya tiene un $state
      // llamado `pools` con los pools del sistema, y sombrearlo aquí
      // haría que un futuro lector creyera estar leyendo aquel.
      const createdPools = res?.pools || [];
      const failed = createdPools.filter((p) => p.status === 'error');
      const mismatched = createdPools.filter((p) => p.mismatch);
      if (failed.length > 0) {
        toast.error(
          `${t('storage.poolsFailed', { names: failed.map((p) => p.name).join(', ') })} — ${failed[0].error}`,
          { duration: 12000 }
        );
      } else if (mismatched.length > 0) {
        // Mismo nombre, otra carpeta: los discos aterrizarían en otro
        // sitio sin avisar.
        toast.warning(
          t('storage.poolsPathMismatch', {
            name: mismatched[0].name,
            path: mismatched[0].path,
          }),
          { duration: 12000 }
        );
      } else {
        toast.success(t('storage.diskInitialized'));
      }
      showInitDisk = false;
      await loadHostDisks();
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      initDiskBusy = false;
    }
  }

  function wipeDiskConfirm(disk) {
    askConfirm({
      title: `${t('storage.wipeDiskTitle')}: ${disk.path}`,
      description: t('storage.wipeDiskDesc'),
      confirmLabel: t('storage.wipe'),
      variant: 'destructive',
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.wipeHostDisk(disk.path);
          confirmState.open = false;
          toast.success(t('storage.diskWiped'));
          await loadHostDisks();
        } catch (e) {
          toast.error(e.message);
        } finally {
          confirmState.loading = false;
        }
      },
    });
  }
  let resizeVolPool = $state('');
  let volResizing = $state(false);

  // ISO Download Dialog
  let showDownloadISO = $state(false);
  let downloadURL = $state('');
  let downloadName = $state('');
  let isoTargetPool = $state('');

  // Move a volume or ISO to another pool of the same kind. One dialog
  // serves both: the difference is only which pools are offered and
  // which "kind" the backend is told, so two near-identical dialogs
  // would be duplication, not clarity.
  let showMoveVol = $state(false);
  let moveVolName = $state('');
  let moveVolSrcPool = $state('');
  let moveVolDestPool = $state('');
  let moveVolKind = $state('disk'); // 'disk' | 'iso'
  let moveVolCopy = $state(false);
  let moveVolBusy = $state(false);
  let moveVolProgress = $state(null);
  const moveVolOptions = $derived(movablePools(pools, moveVolKind, moveVolSrcPool));

  // Every pool of each kind, so a card can ask "is there anywhere to go
  // from here?" without recomputing the whole filter for every row.
  const poolsByKind = $derived({
    iso: movablePools(pools, 'iso'),
    disk: movablePools(pools, 'disk'),
  });

  /**
   * True when an item of `kind` sitting in `currentPool` has at least
   * one other pool of its own kind to move into.
   *
   * The move button is only rendered when this holds. A single pool of
   * a kind — which is the normal shape for ISOs — has nowhere to move
   * anything to, and a button that opens a dialog whose only possible
   * content is "there is no destination" reads as a broken control
   * rather than as an unavailable one.
   */
  function canMoveTo(kind, currentPool) {
    return (poolsByKind[kind] || []).some((p) => p.name !== currentPool);
  }

  // ISO Rename Dialog
  let showRenameISO = $state(false);
  let renameOldName = $state('');
  let renameNewName = $state('');
  let renamePool = $state('');
  let renaming = $state(false);

  // Cloud-Init Modal & State
  let showSnippetModal = $state(false);
  let editingSnippet = $state(null);
  let snippetSaving = $state(false);
  let snippetCategoryFilter = $state('all');
  let previewingRender = $state(false);
  let previewRenderResult = $state('');
  let previewLoading = $state(false);
  let snippetForm = $state({
    name: '',
    description: '',
    category: 'custom',
    type: 'user-data',
    content: '',
  });

  // Storage Error Modal
  let showStorageError = $state(false);
  let storageErrorTitle = $state('');
  let storageErrorMessage = $state('');

  function showStorageErr(title, e) {
    storageErrorTitle = title;
    storageErrorMessage = e?.message || String(e);
    showStorageError = true;
  }

  let confirmState = $state({
    open: false,
    title: '',
    description: '',
    confirmLabel: t('common.delete'),
    variant: 'destructive',
    onConfirm: () => {},
    loading: false,
  });

  function stopDownloadInterval() {
    if (downloadInterval) clearInterval(downloadInterval);
    downloadInterval = null;
  }

  let unsubscribeDone;
  onDestroy(() => {
    stopDownloadInterval();
    if (unsubscribeDone) unsubscribeDone();
  });

  onMount(() => {
    load();
    unsubscribeDone = onImageJobDone(() => {
      loadQuiet();
    });
  });

  async function loadQuiet() {
    try {
      const [pList, lxcImgs, snips] = await Promise.all([
        api.listPools().catch(() => []),
        api.listContainerImages().catch(() => []),
        api.listCloudInitSnippets().catch(() => []),
      ]);
      pools = pList || [];
      lxcImages = lxcImgs || [];
      cloudInitSnippets = snips || [];
      loadBreakdown();
      if (activeTab === 'vm-disks' && selectedDiskPool) {
        volumes = (await api.listVolumes(selectedDiskPool).catch(() => [])) || [];
      } else if (activeTab === 'lxc-disks' && selectedLxcPool) {
        volumes = (await api.listVolumes(selectedLxcPool).catch(() => [])) || [];
      }
      isos =
        (await api
          .listISOs(selectedISOPool === '__all__' ? undefined : selectedISOPool)
          .catch(() => [])) || [];
    } catch {
      // quiet refresh
    }
  }

  async function load() {
    loading = true;
    error = '';
    try {
      const [pList, lxcImgs, snips, hostInfo] = await Promise.all([
        api.listPools().catch(() => []),
        api.listContainerImages().catch(() => []),
        api.listCloudInitSnippets().catch(() => []),
        // Only for the create-pool path preview. A failure here must
        // not take the page down, so the default stands in.
        api.getHostInfo().catch(() => null),
      ]);
      pools = pList || [];
      if (hostInfo?.pools_dir) hostPoolsDir = hostInfo.pools_dir;
      lxcImages = lxcImgs || [];
      cloudInitSnippets = snips || [];
      loadBreakdown();

      const diskPools = pools.filter((p) => hasPurpose(p, 'disk') && p.name !== 'default');
      if (!selectedDiskPool || !diskPools.find((p) => p.name === selectedDiskPool)) {
        selectedDiskPool = diskPools[0]?.name || pools[0]?.name || '';
      }

      const lxcList = pools.filter(
        (p) =>
          hasPurpose(p, 'container') || p.name === 'default' || (p.type && p.type.includes('incus'))
      );
      if (!selectedLxcPool || !lxcList.find((p) => p.name === selectedLxcPool)) {
        selectedLxcPool = lxcList[0]?.name || 'default';
      }

      if (!isoTargetPool || !isoPools.find((p) => p.name === isoTargetPool)) {
        isoTargetPool = isoPools[0]?.name || pools[0]?.name || '';
      }

      if (activeTab === 'vm-disks' && selectedDiskPool) {
        volumes = (await api.listVolumes(selectedDiskPool).catch(() => [])) || [];
      } else if (activeTab === 'lxc-disks' && selectedLxcPool) {
        volumes = (await api.listVolumes(selectedLxcPool).catch(() => [])) || [];
      }

      isos =
        (await api
          .listISOs(selectedISOPool === '__all__' ? undefined : selectedISOPool)
          .catch(() => [])) || [];
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  async function switchTab(tab) {
    activeTab = tab;
    if (tab === 'vm-disks' && selectedDiskPool) {
      loading = true;
      try {
        volumes = (await api.listVolumes(selectedDiskPool).catch(() => [])) || [];
      } finally {
        loading = false;
      }
    } else if (tab === 'lxc-disks' && selectedLxcPool) {
      loading = true;
      try {
        volumes = (await api.listVolumes(selectedLxcPool).catch(() => [])) || [];
      } finally {
        loading = false;
      }
    } else if (tab === 'host-disks') {
      loadHostDisks();
    } else if (tab === 'zfs') {
      loadZfsData();
      loadHostDisks();
    } else if (tab === 'isos') {
      loading = true;
      try {
        isos =
          (await api
            .listISOs(selectedISOPool === '__all__' ? undefined : selectedISOPool)
            .catch(() => [])) || [];
      } finally {
        loading = false;
      }
    }
  }

  function openPoolInTab(p) {
    // Pure-ISO pools open the ISO tab; multi-purpose pools follow the
    // historical priority (LXC → VM disks) so single-purpose behavior
    // stays unchanged.
    if (isIsoOnly(p)) {
      selectedISOPool = p.name;
      switchTab('isos');
    } else if (
      hasPurpose(p, 'container') ||
      p.name === 'default' ||
      (p.type && p.type.includes('incus'))
    ) {
      selectedLxcPool = p.name;
      switchTab('lxc-disks');
    } else {
      selectedDiskPool = p.name;
      switchTab('vm-disks');
    }
  }

  function askConfirm(opts) {
    confirmState = { ...opts, open: true, loading: false };
  }

  function joinPath(base, sub) {
    return sub ? base.replace(/\/+$/, '') + '/' + sub : base;
  }

  function resetPoolForm() {
    poolName = '';
    poolPath = '';
    poolPurpose = 'disk';
    poolKind = 'dir';
    poolSourceFormat = 'nfs';
    poolSourceHost = '';
    poolSourcePort = '';
    poolSourceDevice = '';
    poolSourceDir = '';
    poolSourceUsername = '';
    poolSourcePassword = '';
    poolChapUsername = '';
    poolChapPassword = '';
    poolLocation = 'system';
  }

  async function createPool() {
    if (poolCreating) return;
    if (!auth.isAdmin()) {
      toast.error(t('storage.poolAdminOnly'));
      return;
    }
    if (!poolName) return;
    const isContainer = poolPurpose === 'container';
    // A directory pool on the system disk sends no path at all: the
    // server derives it from the pool name, so the client never has to
    // guess where the pools directory is.
    const onSystemDisk = !isContainer && poolKind === 'dir' && poolLocation === 'system';
    if (!isContainer && !onSystemDisk && poolKind !== 'iscsi' && !poolPath) {
      toast.error(t('storage.poolPathRequired'));
      return;
    }
    const body = {
      name: poolName,
      path: onSystemDisk ? '' : poolPath,
      purpose: poolPurpose,
      type: poolKind,
    };
    if (poolKind === 'iscsi') {
      if (!poolSourceHost || !poolSourceDevice) {
        toast.error(t('storage.iscsiRequireSource'));
        return;
      }
      if ((poolChapUsername !== '') !== (poolChapPassword !== '')) {
        toast.error(t('storage.iscsiChapRequired'));
        return;
      }
      body.source_host = poolSourceHost;
      body.source_device = poolSourceDevice;
      if (poolSourcePort) {
        body.source_port = parseInt(poolSourcePort, 10) || 3260;
      }
      if (poolChapUsername) {
        body.source_username = poolChapUsername;
        body.source_password = poolChapPassword;
      }
      body.path = poolPath || '/dev/disk/by-path';
    } else if (poolKind === 'netfs') {
      if (!poolSourceHost || !poolSourceDir) {
        toast.error(t('storage.netfsRequireSource'));
        return;
      }
      if ((poolSourceUsername !== '') !== (poolSourcePassword !== '')) {
        toast.error(t('storage.cifsAuthRequired'));
        return;
      }
      body.source_host = poolSourceHost;
      body.source_dir = poolSourceDir;
      body.source_format = poolSourceFormat;
      if (poolSourceFormat === 'cifs' && poolSourceUsername) {
        body.source_username = poolSourceUsername;
        body.source_password = poolSourcePassword;
      }
    }
    poolCreating = true;
    try {
      await api.createPool(body);
      const createdName = poolName;
      resetPoolForm();
      showCreatePool = false;
      toast.success(t('storage.poolCreated', { name: createdName }));
      await load();
      openPoolInTab({ name: createdName, purpose: body.purpose, type: body.type });
    } catch (e) {
      if (e && e.status === 403) {
        toast.error(t('storage.poolAdminOnly'));
        showCreatePool = false;
        return;
      }
      toast.error(e.message);
    } finally {
      poolCreating = false;
    }
  }

  async function deletePool(name) {
    askConfirm({
      title: t('storage.deletePoolTitle', { name }),
      description: t('storage.deletePoolDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.deletePool(name);
          confirmState.open = false;
          toast.success(t('storage.poolDeleted', { name }));
          await load();
        } catch (e) {
          confirmState.loading = false;
          showStorageErr(t('storage.deletePoolTitle', { name }), e);
        }
      },
    });
  }

  function openRetagPool(p) {
    retagPoolName = p.name;
    retagCurrent = purposeList(p)[0] || 'disk';
    retagPurpose = retagCurrent;
    showRetagPool = true;
  }

  async function retagPool() {
    if (retagSaving || retagPurpose === retagCurrent) return;
    retagSaving = true;
    try {
      await api.updatePool(retagPoolName, { purpose: retagPurpose });
      showRetagPool = false;
      toast.success(t('storage.poolRetagged', { name: retagPoolName, purpose: retagPurpose }));
      await load();
    } catch (e) {
      showStorageErr(t('storage.retagPoolTitle', { name: retagPoolName }), e);
    } finally {
      retagSaving = false;
    }
  }

  async function reloadDiskVolumes() {
    volumes = (await api.listVolumes(selectedDiskPool).catch(() => [])) || [];
  }

  async function deleteVolume(pool, name) {
    askConfirm({
      title: t('storage.deleteVolumeTitle', { name }),
      description: t('storage.deleteVolumeDesc', { pool }),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.deleteVolume(pool, name);
          confirmState.open = false;
          toast.success(t('storage.volumeDeleted', { name }));
          volumes = (await api.listVolumes(pool).catch(() => [])) || [];
        } catch (e) {
          confirmState.loading = false;
          showStorageErr(t('storage.deleteVolumeTitle', { name }), e);
        }
      },
    });
  }

  async function resizeVolume() {
    if (volResizing) return;
    if (!resizeVolName) return;
    volResizing = true;
    try {
      await api.resizeVolume(resizeVolPool, resizeVolName, resizeVolSize);
      showResizeVol = false;
      resizeVolName = '';
      toast.success(t('storage.volumeResized'));
      if (selectedDiskPool) {
        volumes = (await api.listVolumes(selectedDiskPool).catch(() => [])) || [];
      }
    } catch (e) {
      toast.error(e.message);
    } finally {
      volResizing = false;
    }
  }

  async function deleteISO(iso) {
    const name = iso.name;
    askConfirm({
      title: t('storage.deleteIsoTitle', { name }),
      description: t('storage.deleteIsoDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.deleteISO(name, iso.pool || isoTargetPool);
          confirmState.open = false;
          toast.success(t('storage.isoDeleted', { name }));
          isos =
            (await api
              .listISOs(selectedISOPool === '__all__' ? undefined : selectedISOPool)
              .catch(() => [])) || [];
        } catch (e) {
          confirmState.loading = false;
          showStorageErr(t('storage.deleteIsoTitle', { name }), e);
        }
      },
    });
  }

  // Backend stage identifiers mapped to their keys explicitly, so
  // check-i18n can still see each one as used.
  const MOVE_STAGES = {
    move_preparing: 'vmDetail.movePreparing',
    move_copying: 'vmDetail.moveCopying',
    move_cleanup: 'vmDetail.moveCleanup',
    move_done: 'vmDetail.moveDone',
  };
  function moveStageLabel(stage) {
    const key = MOVE_STAGES[stage];
    return key ? t(key) : '';
  }

  function openMoveVolume(pool, name, kind) {
    moveVolName = name;
    moveVolSrcPool = pool;
    moveVolKind = kind;
    moveVolCopy = false;
    moveVolProgress = null;
    // The options list is derived, so the first valid destination is
    // only known after the fields above are set.
    moveVolDestPool = movablePools(pools, kind, pool)[0]?.name || '';
    showMoveVol = true;
  }

  async function doMoveVolume() {
    if (moveVolBusy || !moveVolDestPool) return;
    moveVolBusy = true;
    moveVolProgress = { pct: 0, stage: 'move_preparing' };
    try {
      const res = await api.moveVolume(moveVolSrcPool, moveVolName, {
        pool: moveVolDestPool,
        kind: moveVolKind,
        copy: moveVolCopy,
      });
      await api.waitJob(res.id, {
        // Copying a large disk across devices can run for hours; the
        // default ceiling would report a failure on a healthy job.
        timeout: 4 * 60 * 60 * 1000,
        onPoll: (job) => {
          moveVolProgress = { pct: job.progress || 0, stage: job.stage || '' };
        },
      });
      showMoveVol = false;
      toast.success(
        t(moveVolCopy ? 'storage.volumeCopied' : 'storage.volumeMoved', {
          name: moveVolName,
          pool: moveVolDestPool,
        })
      );
      await refreshAfterMove();
    } catch (e) {
      showStorageErr(t('storage.moveVolumeTitle'), e);
    } finally {
      moveVolBusy = false;
      moveVolProgress = null;
    }
  }

  // Both ends of a move changed, so the source list alone is stale.
  async function refreshAfterMove() {
    if (moveVolKind === 'iso') {
      isos =
        (await api
          .listISOs(selectedISOPool === '__all__' ? undefined : selectedISOPool)
          .catch(() => [])) || [];
      return;
    }
    if (selectedDiskPool) {
      volumes = (await api.listVolumes(selectedDiskPool).catch(() => [])) || [];
    }
  }

  function openRenameISO(iso) {
    renameOldName = iso.name;
    renameNewName = iso.name;
    renamePool = iso.pool || isoTargetPool;
    showRenameISO = true;
  }

  async function doRenameISO() {
    if (!renameOldName || !renameNewName || renameNewName === renameOldName) {
      showRenameISO = false;
      return;
    }
    renaming = true;
    try {
      await api.renameISO(renameOldName, renameNewName, renamePool);
      showRenameISO = false;
      renameOldName = '';
      renameNewName = '';
      toast.success(t('storage.isoRenamed'));
      isos =
        (await api
          .listISOs(selectedISOPool === '__all__' ? undefined : selectedISOPool)
          .catch(() => [])) || [];
    } catch (e) {
      toast.error(e.message);
    } finally {
      renaming = false;
    }
  }

  async function handleDownloadISO() {
    if (!downloadURL) return;
    if (!isoTargetPool) {
      toast.error(t('storage.noIsoPool'));
      return;
    }
    downloading = true;
    downloadProgress = 0;
    downloadMessage = t('storage.startingDownload');
    let taskId = null;
    try {
      const data = await api.downloadISO(downloadURL, downloadName || undefined, isoTargetPool);
      const jobId = data.job_id;
      if (!jobId) throw new Error(t('storage.noJobId'));
      taskId = 'download:' + jobId;
      upsertTask({
        id: taskId,
        kind: 'download',
        title: downloadName || t('storage.isoSectionTitle'),
        pct: 0,
        message: t('storage.startingDownload'),
        status: 'running',
      });

      await new Promise((resolve) => {
        downloadInterval = setInterval(async () => {
          try {
            const job = await api.getDownloadJob(jobId);
            if (!job) return;
            if (job.status === 'queued') {
              downloadMessage = t('storage.waitingInQueue');
              updateTask(taskId, { message: downloadMessage });
            } else if (job.status === 'downloading') {
              const pct =
                job.progress > 0 && job.progress < 0.01 ? 0 : Math.round(job.progress || 0);
              downloadProgress = pct;
              let msg = t('storage.downloadingName', { name: job.name, pct });
              if (job.speed_bps > 0 || job.bytes_done > 0) {
                const parts = [];
                if (job.speed_bps > 0) {
                  parts.push(formatRate(job.speed_bps));
                }
                if (job.bytes_done > 0 && job.bytes_total > 0) {
                  parts.push(`${formatBytes(job.bytes_done)} / ${formatBytes(job.bytes_total)}`);
                } else if (job.bytes_done > 0) {
                  parts.push(formatBytes(job.bytes_done));
                }
                if (job.eta_seconds > 0) {
                  parts.push(`ETA ${formatETA(job.eta_seconds)}`);
                }
                if (parts.length > 0) {
                  msg = `${job.name ? job.name + ': ' : ''}${parts.join(' · ')} (${pct}%)`;
                }
              }
              downloadMessage = msg;
              updateTask(taskId, {
                pct,
                message: downloadMessage,
                speed_bps: job.speed_bps,
                bytes_done: job.bytes_done,
                bytes_total: job.bytes_total,
                eta_seconds: job.eta_seconds,
              });
            } else if (job.status === 'completed') {
              downloadProgress = 100;
              downloadMessage = t('storage.downloadComplete');
              stopDownloadInterval();
              downloadURL = '';
              downloadName = '';
              showDownloadISO = false;
              finishTask(taskId, 'success', downloadMessage, 100);
              toast.success(t('storage.downloadComplete'));
              resolve();
            } else if (job.status === 'error') {
              toast.error(job.error || t('storage.downloadFailed'));
              stopDownloadInterval();
              downloadMessage = '';
              downloadProgress = 0;
              finishTask(taskId, 'error', job.error || t('storage.downloadFailed'), 0);
              resolve();
            }
          } catch {
            /* ignore poll errors */
          }
        }, 500);
      });
      isos =
        (await api
          .listISOs(selectedISOPool === '__all__' ? undefined : selectedISOPool)
          .catch(() => [])) || [];
    } catch (e) {
      toast.error(e.message);
    } finally {
      stopDownloadInterval();
      downloading = false;
    }
  }

  let uploadFiles = $state(null);

  async function handleUpload() {
    const file = uploadFiles?.[0];
    if (!file) return;
    if (!isoTargetPool) {
      toast.error(t('storage.noIsoPool'));
      return;
    }
    uploading = true;
    uploadProgress = 0;
    const taskId = 'upload:' + file.name;
    upsertTask({
      id: taskId,
      kind: 'upload',
      title: file.name,
      pct: 0,
      message: t('storage.uploadProgress'),
      status: 'running',
    });
    try {
      await api.uploadISO(
        file,
        (pct) => {
          uploadProgress = pct;
          updateTask(taskId, { pct });
        },
        isoTargetPool
      );
      uploadFiles = null;
      finishTask(taskId, 'success', t('storage.isoUploaded'), 100);
      toast.success(t('storage.isoUploaded'));
      isos =
        (await api
          .listISOs(selectedISOPool === '__all__' ? undefined : selectedISOPool)
          .catch(() => [])) || [];
    } catch (e) {
      finishTask(taskId, 'error', e.message, uploadProgress || 0);
      toast.error(e.message);
    } finally {
      uploading = false;
    }
  }

  let uploadDiskFiles = $state(null);

  async function handleUploadDisk() {
    const file = uploadDiskFiles?.[0];
    if (!file || !selectedDiskPool) return;
    uploadingDisk = true;
    uploadDiskProgress = 0;
    const taskId = 'upload-disk:' + file.name;
    upsertTask({
      id: taskId,
      kind: 'upload',
      title: file.name,
      pct: 0,
      message: t('storage.uploadProgress'),
      status: 'running',
    });
    try {
      await api.uploadDisk(
        file,
        (pct) => {
          uploadDiskProgress = pct;
          updateTask(taskId, { pct });
        },
        selectedDiskPool
      );
      uploadDiskFiles = null;
      finishTask(taskId, 'success', t('storage.diskUploaded'), 100);
      toast.success(t('storage.diskUploaded'));
      volumes = (await api.listVolumes(selectedDiskPool).catch(() => [])) || [];
    } catch (e) {
      finishTask(taskId, 'error', e.message, uploadDiskProgress || 0);
      toast.error(e.message);
    } finally {
      uploadingDisk = false;
    }
  }

  // --- LXC Images Actions ---
  async function downloadLxcImage(img) {
    const ref = img.ref;
    downloadingRef = ref;
    try {
      const res = await api.pullContainerImage(ref);
      if (res?.job_id) {
        trackImageJob({
          jobId: res.job_id,
          taskId: 'container_pull_' + ref,
          title: 'Plantilla ' + (img.name || ref),
          kind: 'download',
        });
      }
      toast.success(t('storage.startingDownload') + ': ' + (img.name || ref));
    } catch (e) {
      toast.error(e.message || t('storage.requestDownloadError'));
    } finally {
      downloadingRef = '';
    }
  }

  async function deleteLxcImage(img) {
    const fp = img.fingerprint || img.ref;
    askConfirm({
      title: t('storage.deleteImageConfirm', { name: img.name || img.ref }),
      description: t('storage.deleteIsoDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.deleteContainerImage(fp);
          confirmState.open = false;
          toast.success(t('common.deleted'));
          lxcImages = (await api.listContainerImages().catch(() => [])) || [];
        } catch (e) {
          confirmState.loading = false;
          showStorageErr(t('storage.deleteImageError'), e);
        }
      },
    });
  }

  // --- Cloud-Init Snippets Actions ---
  function openNewSnippet() {
    editingSnippet = null;
    snippetForm = {
      name: '',
      description: '',
      category: 'custom',
      type: 'user-data',
      content:
        '#cloud-config\n# Tu configuración personalizada aquí\npackage_update: true\npackages:\n  - curl\n',
    };
    previewingRender = false;
    previewRenderResult = '';
    showSnippetModal = true;
  }

  function openEditSnippet(sn) {
    editingSnippet = sn;
    snippetForm = {
      name: sn.name,
      description: sn.description || '',
      category: sn.category || (sn.is_preset ? 'devops' : 'custom'),
      type: sn.type || 'user-data',
      content: sn.content || '',
    };
    previewingRender = false;
    previewRenderResult = '';
    showSnippetModal = true;
  }

  function clonePresetAsCustom(sn) {
    editingSnippet = null;
    snippetForm = {
      name: sn.name + ' (Personalizado)',
      description: sn.description || '',
      category: sn.category || 'custom',
      type: sn.type || 'user-data',
      content: sn.content || '',
    };
    previewingRender = false;
    previewRenderResult = '';
    showSnippetModal = true;
  }

  function copySnippetContent(content) {
    if (!content) return;
    navigator.clipboard
      .writeText(content)
      .then(() => {
        toast.success(t('storage.yamlCopied'));
      })
      .catch(() => {
        toast.error(t('storage.copyFailed'));
      });
  }

  function insertVariable(varText) {
    snippetForm.content = (snippetForm.content || '') + ' ' + varText + ' ';
  }

  async function testPreviewSnippet() {
    previewLoading = true;
    try {
      const res = await api.previewCloudInit({
        custom_user_data: snippetForm.content,
        user: 'admin',
        hostname: 'srv-web01',
        ip: '192.168.1.100',
      });
      previewRenderResult = res?.user_data || '';
      previewingRender = true;
    } catch (e) {
      toast.error(e.message || t('storage.previewRenderError'));
    } finally {
      previewLoading = false;
    }
  }

  async function saveSnippet() {
    if (!snippetForm.name.trim()) {
      toast.error(t('storage.snippetNameRequired'));
      return;
    }
    snippetSaving = true;
    try {
      if (editingSnippet && !editingSnippet.is_preset) {
        await api.updateCloudInitSnippet(editingSnippet.id, snippetForm);
        toast.success(t('storage.snippetUpdated'));
      } else {
        await api.createCloudInitSnippet(snippetForm);
        toast.success(t('storage.snippetCreated'));
      }
      showSnippetModal = false;
      cloudInitSnippets = (await api.listCloudInitSnippets().catch(() => [])) || [];
    } catch (e) {
      toast.error(e.message || t('storage.saveSnippetError'));
    } finally {
      snippetSaving = false;
    }
  }

  async function deleteSnippet(sn) {
    askConfirm({
      title: t('storage.deleteSnippetConfirm', { name: sn.name }),
      description: t('storage.deleteIsoDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        confirmState.loading = true;
        try {
          await api.deleteCloudInitSnippet(sn.id);
          confirmState.open = false;
          toast.success(t('storage.snippetDeleted'));
          cloudInitSnippets = (await api.listCloudInitSnippets().catch(() => [])) || [];
        } catch (e) {
          confirmState.loading = false;
          showStorageErr(t('storage.deleteSnippetError'), e);
        }
      },
    });
  }

  function bytesToStr(b) {
    if (!b) return '0 B';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (b >= 1024 && i < u.length - 1) {
      b /= 1024;
      i++;
    }
    return b.toFixed(i > 0 ? 1 : 0) + ' ' + u[i];
  }

  const volumeTree = $derived.by(() => {
    const list = volumes.filter(
      (v) => !diskSearch.trim() || v.name.toLowerCase().includes(diskSearch.toLowerCase())
    );
    return buildVolumeTree(list);
  });
  const vmNameById = $derived.by(() => _vmNameByIdCache);

  function buildVolumeTree(vols) {
    const byName = {};
    for (const v of vols) byName[v.name] = { ...v, children: [] };
    const roots = [];
    for (const k of Object.keys(byName)) {
      const node = byName[k];
      if (node.is_snapshot && node.parent_volume && byName[node.parent_volume]) {
        byName[node.parent_volume].children.push(node);
      } else {
        roots.push(node);
      }
    }
    const sortRec = (nodes) => {
      nodes.sort((a, b) => a.name.localeCompare(b.name));
      nodes.forEach((n) => sortRec(n.children));
    };
    sortRec(roots);
    return roots;
  }

  let _vmNameByIdCache = $state({});
  // Deliberately not $state: this only guards against duplicate
  // in-flight requests, it is never rendered.
  const pendingOwnerLookups = new SvelteSet();

  function sumDedupedByDevice(list, field) {
    const seenDevices = new SvelteSet();
    let sum = 0;
    for (const p of list) {
      const key = p.device_id || `pool:${p.name}`;
      if (seenDevices.has(key)) continue;
      seenDevices.add(key);
      sum += p[field] || 0;
    }
    return sum;
  }
  const totalCapacity = $derived(sumDedupedByDevice(pools, 'capacity'));
  const totalAllocated = $derived(sumDedupedByDevice(pools, 'allocated'));

  // Derived filtered lists
  const filteredTemplates = $derived.by(() => {
    let list = lxcImages || [];
    if (lxcFilter === 'local') {
      list = list.filter((img) => img.is_local);
    } else if (lxcFilter === 'official') {
      list = list.filter((img) => !img.is_local);
    }
    if (!lxcSearch.trim()) return list;
    const q = lxcSearch.toLowerCase();
    return list.filter(
      (img) =>
        (img.name && img.name.toLowerCase().includes(q)) ||
        (img.ref && img.ref.toLowerCase().includes(q)) ||
        (img.os && img.os.toLowerCase().includes(q)) ||
        (img.fingerprint && img.fingerprint.toLowerCase().includes(q))
    );
  });

  const filteredSnippets = $derived.by(() => {
    let list = cloudInitSnippets || [];
    if (snippetCategoryFilter === 'custom') {
      list = list.filter((sn) => !sn.is_preset);
    } else if (snippetCategoryFilter !== 'all') {
      list = list.filter((sn) => sn.category === snippetCategoryFilter);
    }
    if (!snippetSearch.trim()) return list;
    const q = snippetSearch.toLowerCase();
    return list.filter(
      (sn) =>
        (sn.name && sn.name.toLowerCase().includes(q)) ||
        (sn.description && sn.description.toLowerCase().includes(q)) ||
        (sn.type && sn.type.toLowerCase().includes(q)) ||
        (sn.category && sn.category.toLowerCase().includes(q)) ||
        (sn.content && sn.content.toLowerCase().includes(q))
    );
  });

  const filteredISOs = $derived.by(() => {
    let list = isos || [];
    if (!isoSearch.trim()) return list;
    const q = isoSearch.toLowerCase();
    return list.filter((iso) => iso.name && iso.name.toLowerCase().includes(q));
  });

  const filteredLxcVolumes = $derived.by(() => {
    let list = volumes || [];
    if (!lxcDiskSearch.trim()) return list;
    const q = lxcDiskSearch.toLowerCase();
    return list.filter((v) => v.name && v.name.toLowerCase().includes(q));
  });

  $effect(() => {
    const ids = new SvelteSet();
    for (const v of volumes) {
      if (v.is_snapshot && v.snapshot_of_vm_id) ids.add(v.snapshot_of_vm_id);
    }
    // The in-flight guard reads the NON-reactive `pendingOwnerLookups`,
    // not `_vmNameByIdCache`. Reading the $state map here meant every
    // resolved name re-triggered this effect (write → re-read → write),
    // so each owner lookup cost a second full pass over the volume
    // list, and a pool with many snapshots issued the N+1
    // getVM burst more than once.
    for (const id of ids) {
      if (vmNameById[id] || pendingOwnerLookups.has(id)) continue;
      pendingOwnerLookups.add(id);
      api
        .getVM(id)
        .then((vm) => {
          if (vm && vm.name) _vmNameByIdCache = { ..._vmNameByIdCache, [id]: vm.name };
        })
        .catch(() => {})
        .finally(() => pendingOwnerLookups.delete(id));
    }
  });
</script>

<div class="p-3 sm:p-5 w-full max-w-[1700px] mx-auto space-y-4">
  <!-- Top Header & Global Metrics -->
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
    <div>
      <PageHeader title={t('storage.title')} subtitle={t('storage.subtitle')} class="!mb-0 !pb-0" />
    </div>

    <div class="flex items-center gap-2">
      <Button
        variant="ghost"
        size="sm"
        disabled={loading}
        onclick={load}
        class="!h-8 !text-xs"
        aria-label={t('common.refresh')}
      >
        <Icon name="refresh" size={13} class={loading ? 'animate-spin mr-1' : 'mr-1'} />
        {t('common.refresh')}
      </Button>
      <!-- Pool creation is admin-only server-side. -->
      {#if auth.isAdmin()}
        <Button size="sm" onclick={() => (showCreatePool = true)} class="!h-8 !text-xs">
          <Icon name="plus" size={13} class="mr-1" />
          {t('storage.createPool')}
        </Button>
      {/if}
    </div>
  </div>

  {#if error}
    <Alert variant="error">{error}</Alert>
  {/if}

  <!-- 4 Clean Metric Cards Strip -->
  <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
    <StatCard label={t('storage.storagePools')} value={String(pools.length)} hint="KVM + Incus" />
    <StatCard label={t('storage.usedSpace')} value={formatBytes(totalAllocated)} />
    <StatCard
      label={t('storage.totalCapacity')}
      value={formatBytes(totalCapacity)}
      hint={totalCapacity > 0
        ? t('storage.occupiedPct', { pct: Math.round((totalAllocated / totalCapacity) * 100) })
        : undefined}
    />
    <StatCard
      label={t('storage.mediaAndTemplates')}
      value={t('storage.isosCount', { n: isos.length })}
      hint={t('storage.lxcCachedCount', {
        n: lxcImages.filter((i) => i.is_local).length,
      })}
    />
  </div>

  <!-- FLAGSHIP HORIZONTAL TAB NAVIGATION BAR -->
  <div class="border border-border rounded-xl bg-card overflow-hidden shadow-sm">
    <div
      class="flex items-center gap-1 sm:gap-2 px-3 sm:px-4 border-b border-border bg-muted/15 overflow-x-auto text-xs sm:text-sm font-medium"
    >
      <button
        onclick={() => switchTab('pools')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'pools'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="hardDrive" size={15} />
        <span>{t('storage.storagePools')}</span>
        <span class="px-1.5 py-0.2 rounded-full text-[10px] bg-muted font-mono">{pools.length}</span
        >
      </button>

      <button
        onclick={() => switchTab('host-disks')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'host-disks'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="server" size={15} />
        <span>{t('storage.tabHostDisks')}</span>
        {#if hostDisks.length > 0}
          <span class="px-1.5 py-0.2 rounded-full text-[10px] bg-accent/15 text-accent font-mono">
            {hostDisks.length}
          </span>
        {/if}
      </button>

      <button
        onclick={() => switchTab('zfs')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'zfs'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="layers" size={15} />
        <span>{t('storage.tabZfs')}</span>
        {#if zpools.length > 0}
          <span class="px-1.5 py-0.2 rounded-full text-[10px] bg-accent/15 text-accent font-mono">
            {zpools.length}
          </span>
        {/if}
      </button>

      <button
        onclick={() => switchTab('vm-disks')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'vm-disks'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="computer" size={15} />
        <span>{t('storage.tabVmDisks')}</span>
      </button>

      <button
        onclick={() => switchTab('lxc-disks')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'lxc-disks'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="cpu" size={15} />
        <span>{t('storage.tabLxcDisks')}</span>
      </button>

      <button
        onclick={() => switchTab('isos')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'isos'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="disc" size={15} />
        <span>{t('storage.tabIsos')}</span>
        <span class="px-1.5 py-0.2 rounded-full text-[10px] bg-warning/15 text-warning font-mono"
          >{isos.length}</span
        >
      </button>

      <button
        onclick={() => switchTab('lxc-templates')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'lxc-templates'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="boxes" size={15} />
        <span>{t('storage.tabLxcTemplates')}</span>
        <span class="px-1.5 py-0.2 rounded-full text-[10px] bg-success/15 text-success font-mono">
          {lxcImages.filter((i) => i.is_local).length}
        </span>
      </button>

      <button
        onclick={() => switchTab('cloud-init')}
        class="flex items-center gap-2 px-3.5 py-3 border-b-2 transition-all whitespace-nowrap {activeTab ===
        'cloud-init'
          ? 'border-accent text-accent font-bold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Icon name="fileText" size={15} />
        <span>{t('storage.tabCloudInit')}</span>
        <span class="px-1.5 py-0.2 rounded-full text-[10px] bg-muted font-mono"
          >{cloudInitSnippets.length}</span
        >
      </button>
    </div>

    <!-- TAB 1: STORAGE POOLS OVERVIEW GRID -->
    {#if activeTab === 'pools'}
      <div class="p-4 sm:p-5 space-y-4">
        <div class="flex items-center justify-between">
          <div>
            <h3 class="text-sm font-bold text-foreground">{t('storage.availablePoolsTitle')}</h3>
            <p class="text-xs text-muted-foreground">
              {t('storage.poolsHint')}
            </p>
          </div>
        </div>

        {#if pools.length === 0}
          <div class="py-12">
            <EmptyState
              icon="disk"
              title={t('storage.noPools')}
              description={t('storage.noPoolsHint')}
            >
              {#snippet action()}
                {#if auth.isAdmin()}
                  <Button size="sm" onclick={() => (showCreatePool = true)}>
                    {t('storage.createPool')}
                  </Button>
                {/if}
              {/snippet}
            </EmptyState>
          </div>
        {:else}
          <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3.5">
            {#each pools as p (p.name)}
              {@const isISO = isIsoOnly(p)}
              {@const isLXC =
                hasPurpose(p, 'container') ||
                p.name === 'default' ||
                (p.type && p.type.includes('incus'))}
              <div
                class="relative flex flex-col justify-between gap-3 p-4 rounded-xl border border-border bg-background hover:border-accent/40 hover:shadow-sm transition-all duration-150 cursor-pointer group"
                onclick={() => openPoolInTab(p)}
                onkeydown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    openPoolInTab(p);
                  }
                }}
                role="button"
                tabindex="0"
              >
                <div>
                  <div class="flex items-start justify-between gap-2 mb-2">
                    <div class="flex items-center gap-2.5 min-w-0">
                      <div
                        class="w-9 h-9 rounded-lg flex items-center justify-center shrink-0 {isISO
                          ? 'bg-warning/10 text-warning'
                          : isLXC
                            ? 'bg-success/10 text-success'
                            : 'bg-accent/10 text-accent'}"
                      >
                        <Icon name={isISO ? 'disc' : isLXC ? 'cpu' : 'hardDrive'} size={17} />
                      </div>
                      <div class="min-w-0">
                        <h4
                          class="text-sm font-bold text-foreground group-hover:text-accent transition-colors truncate"
                        >
                          {p.name}
                        </h4>
                        <span class="text-[10px] font-mono text-muted-foreground truncate block"
                          >{p.path || '-'}</span
                        >
                      </div>
                    </div>

                    <span
                      class="text-[9px] font-mono px-2 py-0.5 rounded-full font-bold uppercase shrink-0 {isISO
                        ? 'bg-warning/10 text-warning border border-warning/20'
                        : isLXC
                          ? 'bg-success/10 text-success border border-success/20'
                          : 'bg-accent/10 text-accent border border-accent/20'}"
                    >
                      {purposeLabel(p)}
                    </span>
                  </div>

                  {#if p.capacity > 0}
                    {@const bd = breakdownByPool[p.name]}
                    <div class="space-y-1 my-3">
                      {#if bd}
                        <StorageBreakdownBar breakdown={bd} />
                      {:else}
                        <ProgressBar
                          value={(p.allocated / p.capacity) * 100}
                          size="sm"
                          variant={isISO ? 'warning' : isLXC ? 'success' : 'default'}
                        />
                      {/if}
                      <div
                        class="flex items-center justify-between text-[11px] text-muted-foreground font-mono"
                      >
                        <span>{formatBytes(p.allocated)} {t('storage.usedSpace')}</span>
                        <span>{formatBytes(p.capacity)}</span>
                      </div>
                    </div>
                  {/if}
                </div>

                <div
                  class="flex items-center justify-between pt-2.5 border-t border-border text-xs text-muted-foreground"
                >
                  <span class="text-accent group-hover:underline font-medium"
                    >{t('storage.openDisks')}</span
                  >
                  {#if auth.role === 'admin' && p.name !== 'default' && !isBuiltinPool(p.name)}
                    <div class="flex items-center gap-0.5">
                      <button
                        onclick={(e) => {
                          e.stopPropagation();
                          openRetagPool(p);
                        }}
                        class="p-1 rounded text-muted-foreground hover:text-accent hover:bg-accent/10"
                        aria-label={t('storage.retagPoolAria')}
                        title={t('storage.retagPoolAria')}
                      >
                        <Icon name="tag" size={14} />
                      </button>
                      <button
                        onclick={(e) => {
                          e.stopPropagation();
                          deletePool(p.name);
                        }}
                        class="p-1 rounded text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                        aria-label={t('storage.deletePoolAria')}
                        title={t('storage.deletePoolAria')}
                      >
                        <Icon name="trash" size={14} />
                      </button>
                    </div>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}

    <!-- TAB: HOST PHYSICAL DISKS (PROXMOX STYLE) -->
    {#if activeTab === 'host-disks'}
      <div class="p-4 sm:p-5 space-y-4">
        <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
          <div>
            <h3 class="text-sm font-bold text-foreground">{t('storage.hostDisksTitle')}</h3>
            <p class="text-xs text-muted-foreground">
              {@html t('storage.hostDisksDesc', {
                code: htmlVar('<code class="text-foreground font-mono">/mnt/[nombre]</code>'),
              })}
            </p>
          </div>

          <div class="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              class="!h-8 !text-xs"
              onclick={loadHostDisks}
              disabled={hostDisksLoading}
            >
              <Icon
                name="refresh"
                size={13}
                class={hostDisksLoading ? 'animate-spin mr-1' : 'mr-1'}
              />
              {t('common.refresh')}
            </Button>
            {#if auth.isAdmin()}
              <Button
                variant="outline"
                size="sm"
                class="!h-8 !text-xs gap-1.5"
                onclick={openCreateRaidDialog}
              >
                <Icon name="hardDrive" size={13} />
                {t('storage.createRaidBtn')}
              </Button>
              <Button
                size="sm"
                class="!h-8 !text-xs gap-1.5"
                onclick={() => openInitDiskDialog(null)}
              >
                <Icon name="plus" size={13} />
                {t('storage.initDirButton')}
              </Button>
            {/if}
          </div>
        </div>

        <!-- A broken automount is not a disk, so it has no row of its own,
             yet it actively hides the contents of its mountpoint. It has to
             be called out above the table or it stays invisible. -->
        {#if orphanMounts.length > 0}
          <div
            class="border border-destructive/40 bg-destructive/5 rounded-xl p-3 flex items-start gap-2.5"
          >
            <Icon name="alertTriangle" size={16} class="text-destructive shrink-0 mt-0.5" />
            <div class="min-w-0 space-y-1.5">
              <p class="text-xs font-bold text-destructive">{t('storage.orphanMountTitle')}</p>
              <p class="text-xs text-muted-foreground">{t('storage.orphanMountDesc')}</p>
              <ul class="space-y-1 pt-0.5">
                {#each orphanMounts as o (o.unit)}
                  <li class="text-xs font-mono text-foreground break-all">
                    {o.unit}{#if o.mountpoint}<span class="text-muted-foreground">
                        → {o.mountpoint}</span
                      >{/if}{#if o.enabled}<span class="text-destructive">
                        · {t('storage.orphanMountEnabled')}</span
                      >{/if}
                  </li>
                {/each}
              </ul>
              <p class="text-xs text-muted-foreground pt-0.5">
                {t('storage.orphanMountFix')}
              </p>
            </div>
          </div>
        {/if}

        {#if hostDisksLoading}
          <div class="flex justify-center py-16"><Spinner size="lg" /></div>
        {:else if hostDisks.length === 0}
          <EmptyState compact icon="hardDrive" title={t('storage.noHostDisks')} />
        {:else}
          <div class="border border-border rounded-xl bg-background overflow-hidden shadow-xs">
            <div class="overflow-x-auto">
              <table class="w-full text-xs text-left border-collapse">
                <thead>
                  <tr
                    class="border-b border-border bg-muted/30 text-muted-foreground font-semibold"
                  >
                    <th class="p-3 pl-4">{t('storage.colDevice')}</th>
                    <th class="p-3">{t('storage.colModelSerial')}</th>
                    <th class="p-3">{t('storage.colTypeBus')}</th>
                    <th class="p-3 text-right">{t('storage.capacity')}</th>
                    <th class="p-3">{t('storage.filesystemTitle')}</th>
                    <th class="p-3">{t('storage.colMountPoint')}</th>
                    <th class="p-3 pr-4 text-right">{t('storage.colActions')}</th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-border/60">
                  {#each hostDisks as disk (disk.path)}
                    {@const status = hostDiskStatus(disk)}
                    <tr class="hover:bg-muted/20 transition-colors">
                      <td class="p-3 pl-4 font-mono font-medium flex items-center gap-2">
                        <Icon
                          name={disk.rotational ? 'hardDrive' : 'zap'}
                          size={15}
                          class={disk.rotational ? 'text-warning' : 'text-accent'}
                        />
                        <span class="font-bold text-foreground">{disk.path}</span>
                        <span
                          title={status.title || status.label}
                          class="px-1.5 py-0.5 rounded text-[10px] font-sans font-semibold border flex items-center gap-1 whitespace-nowrap {status.key ===
                          'system'
                            ? 'bg-success/15 text-success border-success/30'
                            : status.key === 'inuse'
                              ? 'bg-accent/15 text-accent border-accent/30'
                              : 'bg-muted text-muted-foreground border-border'}"
                        >
                          <Icon name={status.icon} size={10} />
                          {status.label}
                        </span>
                      </td>
                      <td class="p-3">
                        <div class="font-medium text-foreground">
                          {disk.model || t('storage.genericModel')}
                        </div>
                        {#if disk.serial}
                          <div class="text-[10px] font-mono text-muted-foreground">
                            {disk.serial}
                          </div>
                        {/if}
                      </td>
                      <td class="p-3">
                        <span
                          class="px-2 py-0.5 rounded uppercase font-mono text-[10px] font-semibold border {disk.rotational
                            ? 'border-warning/30 bg-warning/10 text-warning'
                            : 'border-accent/30 bg-accent/10 text-accent'}"
                        >
                          {disk.transport || (disk.rotational ? 'HDD' : 'SSD')}
                        </span>
                        {#if disk.smart}
                          <div class="mt-1 flex items-center gap-1.5 flex-wrap">
                            <span
                              class="px-1.5 py-0.2 rounded text-[9px] font-mono font-bold uppercase border {disk
                                .smart.status === 'PASSED'
                                ? 'bg-success/15 text-success border-success/30'
                                : disk.smart.status === 'WARNING'
                                  ? 'bg-warning/15 text-warning border-warning/30'
                                  : disk.smart.status === 'FAILED'
                                    ? 'bg-destructive/15 text-destructive border-destructive/30'
                                    : 'bg-muted text-muted-foreground border-border'}"
                              title={disk.smart.healthy
                                ? 'S.M.A.R.T. Saludable'
                                : 'Atención / Fallo S.M.A.R.T.'}
                            >
                              SMART: {disk.smart.status}
                            </span>
                            {#if disk.smart.temperature_c > 0}
                              <span
                                class="text-[10px] font-mono {disk.smart.temperature_c >= 55
                                  ? 'text-destructive font-bold'
                                  : disk.smart.temperature_c >= 45
                                    ? 'text-warning font-semibold'
                                    : 'text-muted-foreground'}"
                                title="Temperatura"
                              >
                                {disk.smart.temperature_c}°C
                              </span>
                            {/if}
                            {#if disk.smart.wear_percentage >= 0}
                              <span
                                class="text-[10px] font-mono text-muted-foreground"
                                title="Desgaste acumulado SSD"
                              >
                                {disk.smart.wear_percentage}% uso
                              </span>
                            {/if}
                          </div>
                        {/if}
                      </td>
                      <td class="p-3 text-right font-mono font-bold text-foreground"
                        >{disk.size_human}</td
                      >
                      <td class="p-3 font-mono text-muted-foreground">
                        {disk.fstype ||
                          (disk.children?.length
                            ? t('storage.partitionsCount', { n: disk.children.length })
                            : t('storage.noFilesystem'))}
                      </td>
                      <td class="p-3 font-mono text-xs">
                        {#if disk.mountpoints?.length}
                          <span
                            class={disk.mount_state === 'configured'
                              ? 'text-warning font-semibold'
                              : 'text-accent font-semibold'}>{disk.mountpoints.join(', ')}</span
                          >
                        {:else if disk.children?.some((c) => c.mountpoints?.length)}
                          {@const mnts = disk.children
                            .flatMap((c) => c.mountpoints)
                            .filter(Boolean)}
                          <span class="text-foreground font-medium">{mnts.join(', ')}</span>
                        {:else}
                          <span class="text-muted-foreground/60 italic"
                            >{t('storage.notMounted')}</span
                          >
                        {/if}
                        <!-- A reserved disk reads as a contradiction otherwise:
                             "not mounted" next to a badge refusing to format it.
                             Naming the systemd unit turns it into an instruction. -->
                        {#if disk.mount_state === 'configured' && disk.mount_detail}
                          <span
                            class="mt-1 flex items-start gap-1 text-[11px] font-sans not-italic text-warning"
                          >
                            <Icon name="clock" size={11} class="shrink-0 mt-px" />
                            <span>{disk.mount_detail}</span>
                          </span>
                        {/if}
                      </td>
                      {#if hostDiskStatus(disk).key === 'free'}
                        <td class="p-3 pr-4 text-right">
                          <div class="flex items-center justify-end gap-1.5">
                            <Button
                              size="xs"
                              variant="outline"
                              class="!h-7 !text-xs gap-1"
                              onclick={() => {
                                selectedSmartDisk = disk;
                                showSmartModal = true;
                              }}
                            >
                              SMART
                            </Button>
                            {#if auth.isAdmin()}
                              <Button
                                size="xs"
                                variant="outline"
                                class="!h-7 !text-xs gap-1"
                                onclick={() => openInitDiskDialog(disk)}
                              >
                                <Icon name="plus" size={12} />
                                {t('storage.formatAndMount')}
                              </Button>
                              <Button
                                size="xs"
                                variant="destructive"
                                class="!h-7 !text-xs gap-1"
                                onclick={() => wipeDiskConfirm(disk)}
                              >
                                <Icon name="trash" size={12} />
                                {t('storage.wipeShort')}
                              </Button>
                            {:else}
                              <span class="text-[11px] text-muted-foreground/80 italic"
                                >{t('storage.diskAvailable')}</span
                              >
                            {/if}
                          </div>
                        </td>
                      {:else}
                        <td class="p-3 pr-4 text-right">
                          <div class="flex items-center justify-end gap-2">
                            <Button
                              size="xs"
                              variant="outline"
                              class="!h-7 !text-xs gap-1"
                              onclick={() => {
                                selectedSmartDisk = disk;
                                showSmartModal = true;
                              }}
                            >
                              SMART
                            </Button>
                            <span class="text-[11px] text-muted-foreground/80 italic">
                              {hostDiskStatus(disk).key === 'system'
                                ? t('storage.diskProtected')
                                : hostDiskStatus(disk).label}
                            </span>
                          </div>
                        </td>
                      {/if}
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          </div>
        {/if}
      </div>
    {/if}

    <!-- TAB: ZFS STORAGE (POOLS & ZVOLS) -->
    {#if activeTab === 'zfs'}
      <div class="p-4 sm:p-5 space-y-6">
        <!-- ZFS POOLS SECTION -->
        <div class="space-y-3">
          <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
            <div>
              <h3 class="text-sm font-bold text-foreground">{t('storage.zfsPoolsTitle')}</h3>
              <p class="text-xs text-muted-foreground">{t('storage.zfsPoolsDesc')}</p>
            </div>
            <div class="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                class="!h-8 !text-xs"
                onclick={loadZfsData}
                disabled={zfsLoading}
              >
                <Icon name="refresh" size={13} class={zfsLoading ? 'animate-spin mr-1' : 'mr-1'} />
                {t('common.refresh')}
              </Button>
              {#if auth.isAdmin()}
                <Button size="sm" class="!h-8 !text-xs gap-1.5" onclick={openCreateZPoolDialog}>
                  <Icon name="plus" size={13} />
                  {t('storage.createZPoolBtn')}
                </Button>
              {/if}
            </div>
          </div>

          {#if zfsLoading}
            <div class="flex items-center justify-center p-8 text-xs text-muted-foreground">
              <Icon name="refresh" size={16} class="animate-spin mr-2" />
              {t('common.loading')}
            </div>
          {:else if zpools.length === 0}
            <div
              class="p-8 text-center text-xs text-muted-foreground bg-muted/10 rounded-xl border border-dashed border-border"
            >
              <p>{t('storage.noZPools')}</p>
            </div>
          {:else}
            <div class="border border-border rounded-xl bg-card overflow-hidden">
              <div class="overflow-x-auto">
                <table class="w-full text-left text-xs border-collapse">
                  <thead>
                    <tr
                      class="border-b border-border bg-muted/30 text-muted-foreground font-semibold"
                    >
                      <th class="p-3">{t('common.name')}</th>
                      <th class="p-3">{t('storage.colHealth')}</th>
                      <th class="p-3">{t('common.size')}</th>
                      <th class="p-3">{t('storage.colAllocated')}</th>
                      <th class="p-3">{t('storage.colFree')}</th>
                      <th class="p-3">{t('storage.colDisks')}</th>
                      {#if auth.isAdmin()}
                        <th class="p-3 text-right">{t('storage.colActions')}</th>
                      {/if}
                    </tr>
                  </thead>
                  <tbody class="divide-y divide-border">
                    {#each zpools as pool (pool.name)}
                      <tr class="hover:bg-muted/10 transition-colors">
                        <td class="p-3 font-mono font-medium text-foreground">{pool.name}</td>
                        <td class="p-3">
                          <span
                            class="inline-flex items-center px-2 py-0.5 rounded text-[11px] font-medium {pool.health ===
                            'ONLINE'
                              ? 'bg-success/15 text-success'
                              : 'bg-warning/15 text-warning'}"
                          >
                            {pool.health}
                          </span>
                        </td>
                        <td class="p-3 font-mono">{pool.size_human}</td>
                        <td class="p-3 font-mono">{pool.alloc_human}</td>
                        <td class="p-3 font-mono">{pool.free_human}</td>
                        <td class="p-3 font-mono text-[11px] text-muted-foreground">
                          {pool.devices && pool.devices.length > 0 ? pool.devices.join(', ') : '-'}
                        </td>
                        {#if auth.isAdmin()}
                          <td class="p-3 text-right">
                            <div class="flex items-center justify-end gap-1.5">
                              <Button
                                variant="outline"
                                size="sm"
                                class="!h-7 !text-[11px] gap-1"
                                disabled={scrubbingPool[pool.name]}
                                onclick={() => handleScrub(pool.name, 'start')}
                              >
                                Scrub
                              </Button>
                              <Button
                                variant="outline"
                                size="sm"
                                class="!h-7 !text-[11px] gap-1"
                                onclick={() => openCreateZVolDialog(pool.name)}
                              >
                                <Icon name="plus" size={11} />
                                {t('storage.createZVolBtn')}
                              </Button>
                            </div>
                          </td>
                        {/if}
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          {/if}
        </div>

        <!-- ZFS VOLUMES (ZVOLS) SECTION -->
        <div class="space-y-3 pt-2 border-t border-border">
          <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
            <div>
              <h3 class="text-sm font-bold text-foreground">{t('storage.zfsVolsTitle')}</h3>
              <p class="text-xs text-muted-foreground">{t('storage.zfsVolsDesc')}</p>
            </div>
            {#if auth.isAdmin() && zpools.length > 0}
              <Button
                size="sm"
                class="!h-8 !text-xs gap-1.5"
                onclick={() => openCreateZVolDialog('')}
              >
                <Icon name="plus" size={13} />
                {t('storage.createZVolBtn')}
              </Button>
            {/if}
          </div>

          {#if zfsLoading}
            <div class="flex items-center justify-center p-8 text-xs text-muted-foreground">
              <Icon name="refresh" size={16} class="animate-spin mr-2" />
              {t('common.loading')}
            </div>
          {:else if zvols.length === 0}
            <div
              class="p-8 text-center text-xs text-muted-foreground bg-muted/10 rounded-xl border border-dashed border-border"
            >
              <p>{t('storage.noZVols')}</p>
            </div>
          {:else}
            <div class="border border-border rounded-xl bg-card overflow-hidden">
              <div class="overflow-x-auto">
                <table class="w-full text-left text-xs border-collapse">
                  <thead>
                    <tr
                      class="border-b border-border bg-muted/30 text-muted-foreground font-semibold"
                    >
                      <th class="p-3">{t('common.name')}</th>
                      <th class="p-3">{t('storage.targetPoolLabel')}</th>
                      <th class="p-3">{t('common.size')}</th>
                      <th class="p-3">{t('storage.colDevice')}</th>
                      <th class="p-3">{t('common.status')}</th>
                    </tr>
                  </thead>
                  <tbody class="divide-y divide-border">
                    {#each zvols as zvol (zvol.name)}
                      <tr class="hover:bg-muted/10 transition-colors">
                        <td class="p-3 font-mono font-medium text-foreground">{zvol.name}</td>
                        <td class="p-3 font-mono text-muted-foreground">{zvol.pool}</td>
                        <td class="p-3 font-mono">{formatBytes(zvol.volsize)}</td>
                        <td class="p-3 font-mono text-[11px] text-muted-foreground"
                          >{zvol.device}</td
                        >
                        <td class="p-3">
                          {#if zvol.used_by}
                            <span
                              class="inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] bg-warning/15 text-warning font-medium"
                            >
                              <Icon name="computer" size={11} />
                              {zvol.used_by.vm_name} ({zvol.used_by.target})
                            </span>
                          {:else}
                            <span
                              class="inline-flex items-center px-2 py-0.5 rounded text-[11px] bg-success/15 text-success font-medium"
                            >
                              Free
                            </span>
                          {/if}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          {/if}
        </div>
      </div>
    {/if}

    <!-- TAB 2: VM DISKS (KVM) -->
    {#if activeTab === 'vm-disks'}
      <div class="p-4 sm:p-5 space-y-4">
        <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
          <div class="flex flex-wrap items-center gap-3">
            <div class="flex items-center gap-2">
              <span class="text-xs font-semibold text-muted-foreground"
                >{t('storage.storagePoolLabel')}</span
              >
              <select
                bind:value={selectedDiskPool}
                onchange={async () => {
                  loading = true;
                  try {
                    volumes = (await api.listVolumes(selectedDiskPool).catch(() => [])) || [];
                  } finally {
                    loading = false;
                  }
                }}
                class="input !py-1 !text-xs !w-auto font-medium"
              >
                {#each kvmPools as p (p.name)}
                  <option value={p.name}>{p.name} ({p.type || 'dir'})</option>
                {/each}
              </select>
            </div>

            <div class="relative w-48 sm:w-60">
              <Icon
                name="search"
                size={13}
                class="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
              />
              <input
                type="text"
                placeholder={t('storage.searchDiskImages')}
                bind:value={diskSearch}
                class="w-full pl-7 pr-2.5 py-1 rounded-lg bg-background border border-border text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
              />
            </div>
          </div>

          <div class="flex items-center gap-2">
            <Input
              type="file"
              accept=".qcow2,.img,.raw,.qed,.vmdk,.ova,.vdi,.vhdx"
              bind:files={uploadDiskFiles}
              onchange={handleUploadDisk}
              class="hidden"
              id="disk-upload"
            />
            <label
              for="disk-upload"
              class={cn(buttonVariants({ size: 'sm' }), 'cursor-pointer shadow-sm')}
            >
              <Icon name="upload" size={13} class="mr-1" />
              {uploadingDisk ? t('storage.uploadProgress') : t('storage.uploadDiskOva')}
            </label>

            <Button
              size="sm"
              variant="outline"
              class="!h-8 !text-xs"
              onclick={() => (showCreateVol = true)}
            >
              <Icon name="plus" size={13} class="mr-1" />
              {t('storage.createVolume')}
            </Button>
          </div>
        </div>

        {#if uploadingDisk}
          <div class="bg-muted/30 rounded-lg p-3 border border-border">
            <ProgressBar
              value={uploadDiskProgress}
              label={t('storage.uploadProgress')}
              showValue
              size="sm"
            />
          </div>
        {/if}

        <CreateVolumeInlineForm
          bind:open={showCreateVol}
          pool={selectedDiskPool}
          onCreated={reloadDiskVolumes}
        />

        {#if volumeTree.length === 0}
          <EmptyState compact icon="disk" title={t('storage.noVolumesInPool')} />
        {:else}
          <div class="space-y-1.5">
            {#each volumeTree as vol (vol.name)}
              <div class="border border-border rounded-xl bg-background p-3 flex flex-col gap-2">
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-3 min-w-0">
                    <Icon name="hardDrive" size={16} class="text-accent shrink-0" />
                    <span class="text-sm font-semibold truncate">{vol.name}</span>
                    <span class="text-xs text-muted-foreground font-mono tnum"
                      >{bytesToStr(vol.capacity)}</span
                    >
                    {#if vol.allocation != null}
                      <span class="text-xs text-muted-foreground font-mono tnum">
                        ({t('storage.used', { size: bytesToStr(vol.allocation) })})
                      </span>
                    {/if}
                    {#if vol.children.length > 0}
                      <span
                        class="text-[10px] px-1.5 py-0.5 rounded bg-accent/15 text-accent font-mono"
                      >
                        {vol.children.length} snap{vol.children.length > 1 ? 's' : ''}
                      </span>
                    {/if}
                  </div>

                  <div class="flex items-center gap-1 shrink-0">
                    <button
                      onclick={() => {
                        resizeVolName = vol.name;
                        resizeVolSize = vol.capacity / (1024 * 1024 * 1024);
                        resizeVolCurrent = vol.capacity / (1024 * 1024 * 1024);
                        resizeVolPool = selectedDiskPool;
                        showResizeVol = true;
                      }}
                      class="text-xs text-accent hover:text-accent-hover px-2.5 py-1 rounded hover:bg-muted"
                    >
                      {t('storage.resize')}
                    </button>
                    {#if canMoveTo('disk', selectedDiskPool)}
                      <button
                        onclick={() => openMoveVolume(selectedDiskPool, vol.name, 'disk')}
                        class="p-1.5 rounded-md text-muted-foreground hover:text-accent hover:bg-muted"
                        aria-label={`${t('storage.moveVolume')} ${vol.name}`}
                        title={t('storage.moveVolume')}
                      >
                        <Icon name="arrowRightLeft" size={15} />
                      </button>
                    {/if}
                    <button
                      onclick={() => deleteVolume(selectedDiskPool, vol.name)}
                      class="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                      aria-label={`${t('common.delete')} ${vol.name}`}
                    >
                      <Icon name="trash" size={15} />
                    </button>
                  </div>
                </div>

                {#if vol.children.length > 0}
                  <div class="ml-4 pl-3 border-l-2 border-dashed border-border space-y-1 mt-1">
                    {#each vol.children as snap (snap.name)}
                      <div
                        class="flex items-center justify-between px-2.5 py-1.5 rounded bg-muted/30 text-xs"
                      >
                        <div class="flex items-center gap-2 min-w-0">
                          <Icon name="clock" size={12} class="text-muted-foreground shrink-0" />
                          <span class="font-mono truncate" title={snap.name}>{snap.name}</span>
                          <span
                            class="text-[10px] px-1.5 py-0.2 rounded bg-background border border-border text-muted-foreground uppercase font-mono"
                          >
                            {t('storage.internalSnapshot')}
                          </span>
                          <span class="text-muted-foreground font-mono"
                            >{bytesToStr(snap.allocation)}</span
                          >
                        </div>
                        <button
                          onclick={() =>
                            navigate(`/vms/${snap.snapshot_of_vm_id}`, {
                              query: { tab: 'snapshots' },
                            })}
                          class="text-xs text-accent hover:text-accent-hover px-2 py-1 rounded hover:bg-muted shrink-0"
                        >
                          {t('storage.manageIn', {
                            vm: vmNameById[snap.snapshot_of_vm_id] || 'VM',
                          })} →
                        </button>
                      </div>
                    {/each}
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}

    <!-- TAB 3: LXC CONTAINER DISKS (INCUS) -->
    {#if activeTab === 'lxc-disks'}
      <div class="p-4 sm:p-5 space-y-4">
        <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
          <div class="flex flex-wrap items-center gap-3">
            <div class="flex items-center gap-2">
              <span class="text-xs font-semibold text-muted-foreground"
                >{t('storage.incusPoolLabel')}</span
              >
              <select
                bind:value={selectedLxcPool}
                onchange={async () => {
                  loading = true;
                  try {
                    volumes = (await api.listVolumes(selectedLxcPool).catch(() => [])) || [];
                  } finally {
                    loading = false;
                  }
                }}
                class="input !py-1 !text-xs !w-auto font-medium"
              >
                {#each lxcPools as p (p.name)}
                  <option value={p.name}>{p.name} ({p.type || 'dir'})</option>
                {/each}
              </select>
            </div>

            <div class="relative w-48 sm:w-60">
              <Icon
                name="search"
                size={13}
                class="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
              />
              <input
                type="text"
                placeholder={t('storage.searchLxcVolumes')}
                bind:value={lxcDiskSearch}
                class="w-full pl-7 pr-2.5 py-1 rounded-lg bg-background border border-border text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
              />
            </div>
          </div>
        </div>

        {#if filteredLxcVolumes.length === 0}
          <EmptyState compact icon="boxes" title={t('storage.noLxcDisks')} />
        {:else}
          <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
            {#each filteredLxcVolumes as vol (vol.name)}
              <div
                class="p-3.5 rounded-xl border border-border bg-background flex flex-col justify-between gap-3 shadow-xs"
              >
                <div>
                  <div class="flex items-start justify-between gap-2 mb-1">
                    <div class="flex items-center gap-2 min-w-0">
                      <div
                        class="w-8 h-8 rounded-lg bg-success/10 text-success flex items-center justify-center shrink-0"
                      >
                        <Icon name="cpu" size={16} />
                      </div>
                      <div class="min-w-0">
                        <h4 class="text-sm font-bold text-foreground truncate" title={vol.name}>
                          {vol.name}
                        </h4>
                        <span
                          class="text-[10px] px-1.5 py-0.2 rounded bg-muted font-mono uppercase text-muted-foreground"
                        >
                          {vol.format || 'filesystem'}
                        </span>
                      </div>
                    </div>
                    <span class="text-xs font-mono font-semibold text-success shrink-0">
                      {vol.allocated > 0 ? formatBytes(vol.allocated) : bytesToStr(vol.capacity)}
                    </span>
                  </div>
                  <p
                    class="text-[11px] text-muted-foreground font-mono truncate mt-2 bg-muted/20 p-1.5 rounded"
                  >
                    {vol.path}
                  </p>
                </div>

                <div
                  class="flex items-center justify-between pt-2 border-t border-border text-xs text-muted-foreground"
                >
                  <span class="flex items-center gap-1">
                    <Icon name="hardDrive" size={12} />
                    {t('storage.poolLabelShort')}
                    <strong class="text-foreground">{vol.pool || selectedLxcPool}</strong>
                  </span>
                  <button
                    onclick={() => deleteVolume(vol.pool || selectedLxcPool, vol.name)}
                    class="p-1 rounded text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                    aria-label={t('storage.deleteVolumeAria')}
                    title={t('storage.deleteVolumeAria')}
                  >
                    <Icon name="trash" size={14} />
                  </button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}

    <!-- TAB 4: ISO LIBRARY -->
    {#if activeTab === 'isos'}
      <div class="p-4 sm:p-5 space-y-4">
        <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
          <div class="flex flex-wrap items-center gap-3">
            <select
              bind:value={selectedISOPool}
              onchange={async () => {
                loading = true;
                try {
                  isos =
                    (await api
                      .listISOs(selectedISOPool === '__all__' ? undefined : selectedISOPool)
                      .catch(() => [])) || [];
                } finally {
                  loading = false;
                }
              }}
              class="input !py-1 !text-xs !w-auto"
            >
              <option value="__all__">{t('storage.allPoolsOption')}</option>
              {#each pools as p (p.name)}
                <option value={p.name}>{p.name} ({purposeLabel(p)})</option>
              {/each}
            </select>

            <div class="relative w-48 sm:w-60">
              <Icon
                name="search"
                size={13}
                class="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
              />
              <input
                type="text"
                placeholder={t('storage.searchIsoImages')}
                bind:value={isoSearch}
                class="w-full pl-7 pr-2.5 py-1 rounded-lg bg-background border border-border text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
              />
            </div>
          </div>

          <div class="flex flex-wrap items-center gap-2">
            <select
              bind:value={isoTargetPool}
              class="input !py-1 !text-xs !w-auto"
              title={t('storage.isoTargetPool')}
            >
              {#if isoPools.length === 0}
                <option value="">{t('storage.noIsoPool')}</option>
              {/if}
              {#each isoPools as p (p.name)}
                <option value={p.name}>{t('storage.isoTargetPool')}: {p.name}</option>
              {/each}
            </select>

            <Button
              size="sm"
              variant="outline"
              class="!h-8 !text-xs"
              onclick={() => (showDownloadISO = !showDownloadISO)}
              disabled={!isoTargetPool}
            >
              <Icon name="download" size={13} class="mr-1" />
              {t('storage.downloadUrl')}
            </Button>

            <Input
              type="file"
              accept=".iso,.img"
              bind:files={uploadFiles}
              onchange={handleUpload}
              class="hidden"
              id="iso-upload-global"
            />
            <label
              for="iso-upload-global"
              class={cn(
                buttonVariants({ size: 'sm' }),
                'shadow-sm',
                isoTargetPool ? 'cursor-pointer' : 'opacity-50 cursor-not-allowed'
              )}
            >
              <Icon name="upload" size={13} class="mr-1" />
              {uploading ? t('storage.uploadProgress') : t('storage.uploadIso')}
            </label>
          </div>
        </div>

        {#if showDownloadISO}
          <div class="bg-muted/30 rounded-lg p-3.5 border border-border space-y-2">
            <div class="text-xs text-muted-foreground">{t('storage.downloadIsoFromUrl')}</div>
            <div class="flex flex-wrap gap-2 items-end">
              <Input
                bind:value={downloadURL}
                placeholder="https://releases.ubuntu.com/24.04/ubuntu-24.04-desktop-amd64.iso"
                class="flex-1 min-w-[280px]"
              />
              <Input
                bind:value={downloadName}
                placeholder={t('storage.filenameOptional')}
                class="w-48"
              />
              <Button
                onclick={handleDownloadISO}
                disabled={!downloadURL || downloading || !isoTargetPool}
                class="!h-8 !text-xs"
              >
                {downloading ? t('storage.downloadingButton') : t('common.download')}
              </Button>
            </div>
          </div>
        {/if}

        {#if uploading || downloading}
          <div class="bg-muted/30 rounded-lg p-3 border border-border space-y-2">
            {#if uploading}
              <ProgressBar
                value={uploadProgress}
                label={t('storage.uploadProgress')}
                showValue
                size="sm"
              />
            {/if}
            {#if downloading}
              <ProgressBar
                value={downloadProgress}
                label={downloadMessage || t('storage.downloadingButton')}
                showValue
                size="sm"
                variant="success"
              />
            {/if}
          </div>
        {/if}

        {#if filteredISOs.length === 0}
          <EmptyState compact icon="disc" title={t('storage.noIsos')} />
        {:else}
          <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
            {#each filteredISOs as iso (iso.name + (iso.pool || ''))}
              <div
                class="flex items-center justify-between p-3.5 rounded-xl border border-border bg-background shadow-xs"
              >
                <div class="flex items-center gap-3 min-w-0">
                  <div
                    class="w-8 h-8 rounded-lg bg-warning/10 text-warning flex items-center justify-center shrink-0"
                  >
                    <Icon name="disc" size={16} />
                  </div>
                  <div class="min-w-0">
                    <span
                      class="text-sm font-semibold text-foreground truncate block"
                      title={iso.name}>{iso.name}</span
                    >
                    <div
                      class="flex items-center gap-2 text-[11px] text-muted-foreground font-mono"
                    >
                      <span>{bytesToStr(iso.size)}</span>
                      <span>•</span>
                      <span>{t('storage.poolLabelShort')} {iso.pool || '—'}</span>
                    </div>
                  </div>
                </div>
                <div class="flex items-center gap-1 shrink-0">
                  <button
                    onclick={() => openRenameISO(iso)}
                    class="p-1.5 rounded-md text-muted-foreground hover:text-accent hover:bg-muted"
                    aria-label={`${t('storage.rename')} ${iso.name}`}
                  >
                    <Icon name="pencil" size={15} />
                  </button>
                  {#if canMoveTo('iso', iso.pool || isoTargetPool)}
                    <button
                      onclick={() => openMoveVolume(iso.pool || isoTargetPool, iso.name, 'iso')}
                      class="p-1.5 rounded-md text-muted-foreground hover:text-accent hover:bg-muted"
                      aria-label={`${t('storage.moveVolume')} ${iso.name}`}
                      title={t('storage.moveVolume')}
                    >
                      <Icon name="arrowRightLeft" size={15} />
                    </button>
                  {/if}
                  <button
                    onclick={() => deleteISO(iso)}
                    class="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                    aria-label={`${t('common.delete')} ${iso.name}`}
                  >
                    <Icon name="trash" size={15} />
                  </button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}

    <!-- TAB 5: LXC TEMPLATES (INCUS IMAGES) -->
    {#if activeTab === 'lxc-templates'}
      <div class="p-4 sm:p-5 space-y-4">
        <div
          class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 bg-muted/20 border border-border p-3.5 rounded-xl"
        >
          <div class="flex items-center gap-3">
            <div
              class="w-8 h-8 rounded-lg bg-success/15 flex items-center justify-center text-success"
            >
              <Icon name="box" size={16} />
            </div>
            <div>
              <h3 class="text-sm font-bold text-foreground">
                {t('storage.lxcTemplatesTitle')}
              </h3>
              <p class="text-xs text-muted-foreground">
                {t('storage.lxcTemplatesDesc')}
              </p>
            </div>
          </div>

          <div class="flex items-center gap-2">
            <div
              class="flex items-center bg-background border border-border rounded-lg p-0.5 text-xs font-medium"
            >
              <button
                onclick={() => (lxcFilter = 'all')}
                class="px-2.5 py-1 rounded-md transition-all {lxcFilter === 'all'
                  ? 'bg-accent text-accent-foreground font-bold'
                  : 'text-muted-foreground'}"
              >
                {t('storage.filterAll')}
              </button>
              <button
                onclick={() => (lxcFilter = 'local')}
                class="px-2.5 py-1 rounded-md transition-all {lxcFilter === 'local'
                  ? 'bg-success text-success-foreground font-bold'
                  : 'text-muted-foreground'}"
              >
                {t('storage.filterLocal')}
              </button>
              <button
                onclick={() => (lxcFilter = 'official')}
                class="px-2.5 py-1 rounded-md transition-all {lxcFilter === 'official'
                  ? 'bg-accent text-accent-foreground font-bold'
                  : 'text-muted-foreground'}"
              >
                {t('storage.filterOfficial')}
              </button>
            </div>

            <div class="relative w-48 sm:w-56">
              <Icon
                name="search"
                size={13}
                class="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
              />
              <input
                type="text"
                placeholder={t('storage.searchTemplates')}
                bind:value={lxcSearch}
                class="w-full pl-7 pr-2.5 py-1 rounded-lg bg-background border border-border text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
              />
            </div>
          </div>
        </div>

        {#if filteredTemplates.length === 0}
          <EmptyState compact icon="boxes" title={t('storage.noLxcTemplates')} />
        {:else}
          <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3.5">
            {#each filteredTemplates as img (img.ref || img.fingerprint)}
              <div
                class="flex flex-col justify-between rounded-xl border bg-background p-3.5 transition-all duration-150 {img.is_local
                  ? 'border-success/30 bg-success/[0.02]'
                  : 'border-border'}"
              >
                <div>
                  <div class="flex items-start justify-between gap-2 mb-2">
                    <div class="flex items-center gap-2 min-w-0">
                      <div
                        class="w-8 h-8 rounded-lg flex items-center justify-center font-bold text-xs {img.is_local
                          ? 'bg-success/15 text-success'
                          : 'bg-muted text-muted-foreground'}"
                      >
                        {img.os ? img.os.slice(0, 3).toUpperCase() : 'LXC'}
                      </div>
                      <div class="min-w-0">
                        <h4 class="font-bold text-xs text-foreground truncate" title={img.name}>
                          {img.name || img.ref}
                        </h4>
                        <p class="text-[10px] text-muted-foreground font-mono truncate">
                          {img.ref}
                        </p>
                      </div>
                    </div>

                    {#if img.is_local}
                      <span
                        class="px-1.5 py-0.5 rounded-full bg-success/15 text-success font-mono text-[9px] font-bold border border-success/30 shrink-0"
                      >
                        ✓ {t('storage.cached')}
                      </span>
                    {/if}
                  </div>

                  <div
                    class="my-2 flex items-center gap-2 text-[10px] font-mono text-muted-foreground"
                  >
                    <span>{img.arch || 'x86_64'}</span>
                    <span>•</span>
                    <span>{img.size > 0 ? formatBytes(img.size) : '~130 MB'}</span>
                    {#if img.fingerprint}
                      <span>•</span>
                      <span class="truncate max-w-[65px]">fp:{img.fingerprint}</span>
                    {/if}
                  </div>
                </div>

                <div class="pt-2.5 border-t border-border flex items-center justify-between gap-2">
                  {#if img.is_local}
                    <Button
                      size="sm"
                      variant="outline"
                      class="!h-7 !text-xs text-destructive hover:bg-destructive/10 w-full"
                      onclick={() => deleteLxcImage(img)}
                    >
                      <Icon name="trash" size={13} class="mr-1" />
                      {t('storage.deleteFromCache')}
                    </Button>
                  {:else}
                    <Button
                      size="sm"
                      class="!h-7 !text-xs w-full bg-success hover:bg-success/85 text-success-foreground"
                      disabled={downloadingRef === img.ref}
                      onclick={() => downloadLxcImage(img)}
                    >
                      {#if downloadingRef === img.ref}
                        <Spinner size="xs" class="mr-1" />
                      {:else}
                        <Icon name="download" size={13} class="mr-1" />
                      {/if}
                      {t('storage.downloadToCache')}
                    </Button>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}

    <!-- TAB 6: CLOUD-INIT SNIPPETS & RECIPES STUDIO -->
    {#if activeTab === 'cloud-init'}
      <div class="p-4 sm:p-5 space-y-4">
        <!-- Header & Category Toolbar -->
        <div
          class="flex flex-col sm:flex-row sm:items-center justify-between gap-3 bg-muted/20 border border-border p-3.5 rounded-xl"
        >
          <div class="flex items-center gap-3">
            <div
              class="w-8 h-8 rounded-lg bg-accent/15 flex items-center justify-center text-accent font-bold"
            >
              <Icon name="fileText" size={17} />
            </div>
            <div>
              <h3 class="text-sm font-bold text-foreground">{t('storage.cloudInitStudioTitle')}</h3>
              <p class="text-xs text-muted-foreground">
                {t('storage.cloudInitStudioDesc')}
              </p>
            </div>
          </div>

          <div class="flex items-center gap-2">
            <div class="relative w-48 sm:w-56">
              <Icon
                name="search"
                size={13}
                class="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
              />
              <input
                type="text"
                placeholder={t('storage.searchSnippets')}
                bind:value={snippetSearch}
                class="w-full pl-7 pr-2.5 py-1 rounded-lg bg-background border border-border text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
              />
            </div>
            <Button size="sm" class="!h-8 !text-xs" onclick={openNewSnippet}>
              <Icon name="plus" size={13} class="mr-1" />
              {t('storage.newSnippet')}
            </Button>
          </div>
        </div>

        <!-- Quick Category Filter Pills -->
        <div class="flex items-center gap-1.5 overflow-x-auto pb-1 text-xs font-medium">
          <button
            onclick={() => (snippetCategoryFilter = 'all')}
            class="px-3 py-1 rounded-lg transition-all whitespace-nowrap {snippetCategoryFilter ===
            'all'
              ? 'bg-accent text-accent-foreground font-bold shadow-xs'
              : 'bg-muted/40 text-muted-foreground hover:bg-muted'}"
          >
            {t('storage.filterAll')} ({cloudInitSnippets.length})
          </button>
          <button
            onclick={() => (snippetCategoryFilter = 'containers')}
            class="px-3 py-1 rounded-lg transition-all whitespace-nowrap {snippetCategoryFilter ===
            'containers'
              ? 'bg-accent text-accent-foreground font-bold shadow-xs'
              : 'bg-muted/40 text-muted-foreground hover:bg-muted'}"
          >
            {t('storage.catContainers')}
          </button>
          <button
            onclick={() => (snippetCategoryFilter = 'kubernetes')}
            class="px-3 py-1 rounded-lg transition-all whitespace-nowrap {snippetCategoryFilter ===
            'kubernetes'
              ? 'bg-accent text-accent-foreground font-bold shadow-xs'
              : 'bg-muted/40 text-muted-foreground hover:bg-muted'}"
          >
            {t('storage.catKubernetes')}
          </button>
          <button
            onclick={() => (snippetCategoryFilter = 'security')}
            class="px-3 py-1 rounded-lg transition-all whitespace-nowrap {snippetCategoryFilter ===
            'security'
              ? 'bg-accent text-accent-foreground font-bold shadow-xs'
              : 'bg-muted/40 text-muted-foreground hover:bg-muted'}"
          >
            {t('storage.catSecurity')}
          </button>
          <button
            onclick={() => (snippetCategoryFilter = 'networking')}
            class="px-3 py-1 rounded-lg transition-all whitespace-nowrap {snippetCategoryFilter ===
            'networking'
              ? 'bg-accent text-accent-foreground font-bold shadow-xs'
              : 'bg-muted/40 text-muted-foreground hover:bg-muted'}"
          >
            {t('storage.catNetworking')}
          </button>
          <button
            onclick={() => (snippetCategoryFilter = 'monitoring')}
            class="px-3 py-1 rounded-lg transition-all whitespace-nowrap {snippetCategoryFilter ===
            'monitoring'
              ? 'bg-accent text-accent-foreground font-bold shadow-xs'
              : 'bg-muted/40 text-muted-foreground hover:bg-muted'}"
          >
            {t('storage.catMonitoring')}
          </button>
          <button
            onclick={() => (snippetCategoryFilter = 'web')}
            class="px-3 py-1 rounded-lg transition-all whitespace-nowrap {snippetCategoryFilter ===
            'web'
              ? 'bg-accent text-accent-foreground font-bold shadow-xs'
              : 'bg-muted/40 text-muted-foreground hover:bg-muted'}"
          >
            {t('storage.catWeb')}
          </button>
          <button
            onclick={() => (snippetCategoryFilter = 'devops')}
            class="px-3 py-1 rounded-lg transition-all whitespace-nowrap {snippetCategoryFilter ===
            'devops'
              ? 'bg-accent text-accent-foreground font-bold shadow-xs'
              : 'bg-muted/40 text-muted-foreground hover:bg-muted'}"
          >
            {t('storage.catDevops')}
          </button>
          <button
            onclick={() => (snippetCategoryFilter = 'custom')}
            class="px-3 py-1 rounded-lg transition-all whitespace-nowrap {snippetCategoryFilter ===
            'custom'
              ? 'bg-success text-success-foreground font-bold shadow-xs'
              : 'bg-muted/40 text-muted-foreground hover:bg-muted'}"
          >
            {t('storage.myRecipes')} ({cloudInitSnippets.filter((s) => !s.is_preset).length})
          </button>
        </div>

        {#if filteredSnippets.length === 0}
          <EmptyState compact icon="code" title={t('storage.noSnippets')} />
        {:else}
          <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3.5">
            {#each filteredSnippets as sn (sn.id)}
              {@const isCustom = !sn.is_preset}
              <div
                class="p-4 rounded-xl border border-border bg-background flex flex-col justify-between gap-3 shadow-xs hover:border-accent/40 transition-colors"
              >
                <div>
                  <div class="flex items-start justify-between gap-2 mb-1.5">
                    <div class="flex items-center gap-2 min-w-0">
                      <div
                        class="w-8 h-8 rounded-lg flex items-center justify-center shrink-0 {isCustom
                          ? 'bg-success/15 text-success'
                          : 'bg-accent/10 text-accent'}"
                      >
                        <Icon
                          name={sn.category === 'security'
                            ? 'shield'
                            : sn.category === 'networking'
                              ? 'network'
                              : sn.category === 'monitoring'
                                ? 'activity'
                                : sn.category === 'web'
                                  ? 'globe'
                                  : 'fileText'}
                          size={16}
                        />
                      </div>
                      <div class="min-w-0">
                        <h4 class="text-sm font-bold text-foreground truncate" title={sn.name}>
                          {sn.name}
                        </h4>
                        <div class="flex items-center gap-1.5 mt-0.5">
                          <span
                            class="text-[10px] px-1.5 py-0.2 rounded bg-muted font-mono uppercase text-muted-foreground"
                          >
                            {sn.type || 'user-data'}
                          </span>
                          {#if sn.category}
                            <span
                              class="text-[10px] px-1.5 py-0.2 rounded bg-muted/60 font-mono text-muted-foreground"
                            >
                              {sn.category}
                            </span>
                          {/if}
                        </div>
                      </div>
                    </div>
                    {#if sn.is_preset}
                      <span
                        class="px-1.5 py-0.5 rounded-full bg-accent/15 text-accent font-mono text-[9px] font-bold border border-accent/30 shrink-0"
                      >
                        {t('storage.presetBadge')}
                      </span>
                    {:else}
                      <span
                        class="px-1.5 py-0.5 rounded-full bg-success/15 text-success font-mono text-[9px] font-bold border border-success/30 shrink-0"
                      >
                        {t('storage.customBadge')}
                      </span>
                    {/if}
                  </div>

                  <p class="text-xs text-muted-foreground line-clamp-2 my-2">
                    {sn.description || t('storage.noDescription')}
                  </p>

                  <div class="relative group/code">
                    <pre
                      class="text-[10px] font-mono bg-muted/40 p-2.5 rounded-lg border border-border text-foreground/80 overflow-x-auto max-h-28 whitespace-pre">{sn.content?.slice(
                        0,
                        350
                      ) || '# empty'}</pre>
                    <button
                      onclick={() => copySnippetContent(sn.content)}
                      class="absolute top-1.5 right-1.5 p-1 rounded bg-background/80 border border-border text-muted-foreground hover:text-foreground opacity-0 group-hover/code:opacity-100 transition-opacity text-[10px] flex items-center gap-1"
                      title={t('storage.copyYamlTitle')}
                    >
                      <Icon name="copy" size={11} />
                      <span>{t('storage.copyButton')}</span>
                    </button>
                  </div>
                </div>

                <div class="pt-2.5 border-t border-border flex items-center justify-between gap-2">
                  {#if sn.is_preset}
                    <Button
                      size="sm"
                      variant="outline"
                      class="!h-7 !text-xs flex-1"
                      onclick={() => openEditSnippet(sn)}
                    >
                      <Icon name="eye" size={12} class="mr-1" />
                      {t('storage.viewCode')}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      class="!h-7 !text-xs flex-1 text-accent hover:bg-accent/10"
                      onclick={() => clonePresetAsCustom(sn)}
                    >
                      <Icon name="copy" size={12} class="mr-1" />
                      {t('storage.cloneAndEdit')}
                    </Button>
                  {:else}
                    <Button
                      size="sm"
                      variant="outline"
                      class="!h-7 !text-xs flex-1"
                      onclick={() => openEditSnippet(sn)}
                    >
                      <Icon name="pencil" size={12} class="mr-1" />
                      {t('storage.editSnippet')}
                    </Button>
                    <button
                      onclick={() => deleteSnippet(sn)}
                      class="p-1.5 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                      aria-label={t('storage.deleteSnippetAria')}
                      title={t('storage.deleteSnippetAria')}
                    >
                      <Icon name="trash" size={14} />
                    </button>
                  {/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
  </div>
</div>

<!-- Create Pool Dialog -->
<Dialog.Root bind:open={showCreatePool}>
  <Dialog.Content class="sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>{t('storage.newPool')}</Dialog.Title>
      <Dialog.Description>
        {t('storage.createPoolDesc')}
      </Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3 my-2">
      <div>
        <label for="create-pool-name" class="block text-xs font-semibold mb-1"
          >{t('storage.poolName')}</label
        >
        <Input
          id="create-pool-name"
          bind:value={poolName}
          placeholder={t('storage.poolNamePlaceholder')}
        />
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
        <div>
          <label for="create-pool-purpose" class="block text-xs font-semibold mb-1"
            >{t('storage.purpose')}</label
          >
          <select
            id="create-pool-purpose"
            bind:value={poolPurpose}
            onchange={() => {
              if (poolPurpose === 'container') {
                poolKind = 'dir';
              } else if (poolKind !== 'dir' && poolKind !== 'netfs' && poolKind !== 'iscsi') {
                poolKind = 'dir';
              }
            }}
            class="input w-full !text-xs font-medium"
          >
            <!-- Un pool = un propósito. La lista debe cubrir los cinco
                 que acepta el backend (api/storage.go CreatePool), o
                 backups/plantillas solo serían creables inicializando
                 un disco. -->
            <option value="disk">{t('storage.purposeKvmOption')}</option>
            <option value="container">{t('storage.purposeLxcOption')}</option>
            <option value="iso">{t('storage.purposeIsoOption')}</option>
            <option value="backup">{t('storage.purposeBackupOption')}</option>
            <option value="template">{t('storage.purposeTemplateOption')}</option>
          </select>
        </div>
        <div>
          <label for="create-pool-kind" class="block text-xs font-semibold mb-1"
            >{t('storage.driverTypeLabel')}</label
          >
          <select
            id="create-pool-kind"
            bind:value={poolKind}
            class="input w-full !text-xs font-medium"
          >
            {#if poolPurpose === 'container'}
              <option value="dir">dir (Directorio Local)</option>
              <option value="zfs">zfs (ZFS Pool Incus)</option>
              <option value="btrfs">btrfs (Btrfs Pool Incus)</option>
              <option value="lvm">lvm (LVM Pool Incus)</option>
            {:else}
              <option value="dir">{t('storage.localDir')}</option>
              <option value="netfs">{t('storage.netfs')}</option>
              <option value="iscsi">{t('storage.iscsi')}</option>
            {/if}
          </select>
        </div>
      </div>

      {#if poolPurpose === 'container'}
        <div>
          <label for="create-pool-path" class="block text-xs font-semibold mb-1"
            >{t('storage.pathOrDeviceOptionalLabel')}</label
          >
          <Input
            id="create-pool-path"
            bind:value={poolPath}
            placeholder={t('storage.incusPathPlaceholder')}
          />
          <p class="text-[11px] text-muted-foreground mt-1">
            {t('storage.incusPathHint', { kind: poolKind })}
          </p>
        </div>
      {:else if poolKind === 'dir'}
        <!-- Where the pool lives. The system disk is offered first and
             selected by default: it is the right answer for an operator
             who has not mounted a second disk, and it needs no typing
             at all — the server derives the folder from the pool name. -->
        <div class="space-y-2">
          <span class="block text-xs font-semibold">{t('storage.poolLocationLabel')}</span>
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
            <button
              type="button"
              onclick={() => (poolLocation = 'system')}
              class="text-left p-2.5 rounded-lg border transition-colors {poolLocation === 'system'
                ? 'border-accent bg-accent/10'
                : 'border-border hover:border-accent/50'}"
            >
              <span class="flex items-center gap-2 text-xs font-semibold">
                <Icon name="hardDrive" size={14} />
                {t('storage.poolLocationSystem')}
              </span>
              <span class="block text-[11px] text-muted-foreground mt-1"
                >{t('storage.poolLocationSystemHint')}</span
              >
            </button>
            <button
              type="button"
              onclick={() => (poolLocation = 'custom')}
              class="text-left p-2.5 rounded-lg border transition-colors {poolLocation === 'custom'
                ? 'border-accent bg-accent/10'
                : 'border-border hover:border-accent/50'}"
            >
              <span class="flex items-center gap-2 text-xs font-semibold">
                <Icon name="folder" size={14} />
                {t('storage.poolLocationCustom')}
              </span>
              <span class="block text-[11px] text-muted-foreground mt-1"
                >{t('storage.poolLocationCustomHint')}</span
              >
            </button>
          </div>

          {#if poolLocation === 'system'}
            <!-- Live preview of the folder the server will derive, so
                 the operator sees the result before committing. -->
            <p class="text-[11px] text-muted-foreground">
              {t('storage.poolLocationPreview')}
              <code class="text-foreground font-mono"
                >{hostPoolsDir}/{poolName || t('storage.poolNamePlaceholderShort')}</code
              >
            </p>
          {:else}
            <div>
              <div class="flex items-center justify-between mb-1">
                <label for="create-pool-path" class="block text-xs font-semibold"
                  >{t('storage.path')}</label
                >
                <button
                  type="button"
                  onclick={() => {
                    poolPath = `/mnt/${poolName || 'storage'}`;
                  }}
                  class="text-[11px] text-accent hover:underline font-medium cursor-pointer"
                >
                  {t('storage.useProxmoxStylePath', { name: poolName || 'nombre' })}
                </button>
              </div>
              <Input
                id="create-pool-path"
                bind:value={poolPath}
                placeholder={t('storage.dirPathPlaceholder')}
              />
              <!-- The text input above stays editable: the picker is an
                   aid for the common case, not a replacement for typing
                   a path that does not exist yet. -->
              <LocalFolderBrowser onSelect={(p) => (poolPath = p)} />
              <p class="text-[11px] text-muted-foreground mt-1">
                {@html t('storage.absolutePathHint', {
                  // Plain string, not htmlVar(): poolName is user-supplied
                  // and was reaching the DOM unescaped, so a pool named
                  // <img src=x onerror=…> executed script in the operator's
                  // session. t() escapes plain interpolations for us.
                  name: `<code class="text-foreground font-mono">/mnt/${poolName || 'mi-disco'}</code>`,
                })}
              </p>
            </div>
          {/if}
        </div>
      {:else if poolKind === 'iscsi'}
        <div class="space-y-2 border border-border p-3 rounded-lg bg-muted/20">
          <div class="grid grid-cols-1 sm:grid-cols-3 gap-2">
            <div class="sm:col-span-2">
              <label for="create-iscsi-host" class="block text-xs font-semibold mb-1"
                >{t('storage.serverHostLabel')}</label
              >
              <Input
                id="create-iscsi-host"
                bind:value={poolSourceHost}
                placeholder="192.168.1.50"
              />
            </div>
            <div>
              <label for="create-iscsi-port" class="block text-xs font-semibold mb-1"
                >{t('storage.iscsiPortLabel')}</label
              >
              <Input
                id="create-iscsi-port"
                bind:value={poolSourcePort}
                placeholder="3260"
                type="number"
              />
            </div>
          </div>

          <div>
            <label for="create-iscsi-device" class="block text-xs font-semibold mb-1"
              >{t('storage.iscsiIQNLabel')}</label
            >
            <Input
              id="create-iscsi-device"
              bind:value={poolSourceDevice}
              placeholder={t('storage.iscsiIQNPlaceholder')}
            />
            <p class="text-[11px] text-muted-foreground mt-1">
              {t('storage.iscsiHint')}
            </p>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-2 pt-2 border-t border-border">
            <div>
              <label for="create-iscsi-chap-user" class="block text-xs font-semibold mb-1"
                >{t('storage.iscsiChapUsernameLabel')}</label
              >
              <Input
                id="create-iscsi-chap-user"
                bind:value={poolChapUsername}
                placeholder="admin"
                autocomplete="off"
              />
            </div>
            <div>
              <label for="create-iscsi-chap-pass" class="block text-xs font-semibold mb-1"
                >{t('storage.iscsiChapPasswordLabel')}</label
              >
              <Input
                id="create-iscsi-chap-pass"
                bind:value={poolChapPassword}
                type="password"
                placeholder="••••••••"
                autocomplete="new-password"
              />
            </div>
          </div>
        </div>
      {:else}
        <div class="space-y-2 border border-border p-3 rounded-lg bg-muted/20">
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
            <div>
              <label for="create-source-format" class="block text-xs font-semibold mb-1"
                >{t('storage.protocolLabel')}</label
              >
              <select
                id="create-source-format"
                bind:value={poolSourceFormat}
                class="input w-full !text-xs"
              >
                <option value="nfs">NFS</option>
                <option value="cifs">SMB / CIFS</option>
              </select>
            </div>
            <div>
              <label for="create-source-host" class="block text-xs font-semibold mb-1"
                >{t('storage.serverHostLabel')}</label
              >
              <Input
                id="create-source-host"
                bind:value={poolSourceHost}
                placeholder="192.168.1.50"
              />
            </div>
          </div>

          <div>
            <label for="create-source-dir" class="block text-xs font-semibold mb-1"
              >{t('storage.exportedPathLabel')}</label
            >
            <Input id="create-source-dir" bind:value={poolSourceDir} placeholder="/mnt/storage" />
          </div>

          <div>
            <label for="create-mount-point" class="block text-xs font-semibold mb-1"
              >{t('storage.localMountPointLabel')}</label
            >
            <Input id="create-mount-point" bind:value={poolPath} placeholder="/mnt/netfs-pool" />
          </div>

          <RemoteFolderBrowser
            format={poolSourceFormat}
            host={poolSourceHost}
            sourceDir={poolSourceDir}
            username={poolSourceUsername}
            password={poolSourcePassword}
            onSelect={(sub) => (poolSourceDir = joinPath(poolSourceDir, sub))}
          />

          {#if poolSourceFormat === 'cifs'}
            <div class="grid grid-cols-1 sm:grid-cols-2 gap-2 pt-1 border-t border-border">
              <div>
                <label for="create-cifs-user" class="block text-xs font-semibold mb-1"
                  >{t('storage.cifsUsernameLabel')}</label
                >
                <Input
                  id="create-cifs-user"
                  bind:value={poolSourceUsername}
                  placeholder="alice"
                  autocomplete="off"
                />
              </div>
              <div>
                <label for="create-cifs-pass" class="block text-xs font-semibold mb-1"
                  >Contraseña</label
                >
                <Input
                  id="create-cifs-pass"
                  bind:value={poolSourcePassword}
                  type="password"
                  placeholder="••••••••"
                  autocomplete="new-password"
                />
              </div>
            </div>
          {/if}
        </div>
      {/if}
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showCreatePool = false)}
        >{t('common.cancel')}</Button
      >
      <Button
        disabled={poolCreating ||
          !poolName ||
          (poolKind === 'iscsi' && (!poolSourceHost || !poolSourceDevice)) ||
          (poolKind === 'netfs' && (!poolSourceHost || !poolSourceDir)) ||
          (poolPurpose !== 'container' &&
            poolKind === 'dir' &&
            poolLocation === 'custom' &&
            !poolPath)}
        onclick={createPool}
      >
        {#if poolCreating}<Spinner size="xs" class="mr-1" />{/if}
        {t('common.create')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Retag a pool's purpose. No data moves: the purpose lives in
     pool-purposes.json, not in the libvirt XML, so the pool keeps
     running and its volumes stay exactly where they are. The server
     refuses the change when the pool already holds files the new
     purpose would hide, which is why no client-side guard duplicates
     that rule here. -->
<Dialog.Root bind:open={showRetagPool}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('storage.retagPoolTitle', { name: retagPoolName })}</Dialog.Title>
      <Dialog.Description>{t('storage.retagPoolDesc')}</Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3 my-2">
      <div>
        <label for="retag-pool-purpose" class="block text-xs font-semibold mb-1"
          >{t('storage.purpose')}</label
        >
        <select
          id="retag-pool-purpose"
          bind:value={retagPurpose}
          class="input w-full !text-xs font-medium"
        >
          <!-- A container pool lives in Incus and the rest in libvirt;
               the server rejects crossing that line, so the option is
               only offered to a pool that is already on that side. -->
          {#if retagCurrent === 'container'}
            <option value="container">{t('storage.purposeLxcOption')}</option>
          {:else}
            <option value="disk">{t('storage.purposeKvmOption')}</option>
            <option value="iso">{t('storage.purposeIsoOption')}</option>
            <option value="backup">{t('storage.purposeBackupOption')}</option>
            <option value="template">{t('storage.purposeTemplateOption')}</option>
          {/if}
        </select>
      </div>
      <p class="text-[11px] text-muted-foreground">
        {t('storage.retagPoolHint')}
      </p>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showRetagPool = false)}>{t('common.cancel')}</Button
      >
      <Button disabled={retagSaving || retagPurpose === retagCurrent} onclick={retagPool}>
        {#if retagSaving}<Spinner size="xs" class="mr-1" />{/if}
        {t('common.save')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Move a volume or ISO between pools of the same kind. Only pools of
     the matching kind are ever offered: the backend refuses the rest,
     so listing them would just invite a guaranteed error. -->
<Dialog.Root bind:open={showMoveVol}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('storage.moveVolumeTitle')}</Dialog.Title>
      <Dialog.Description>
        {t('storage.moveVolumeDesc', { name: moveVolName, pool: moveVolSrcPool })}
      </Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3">
      {#if moveVolOptions.length === 0}
        <p class="text-sm text-muted-foreground">
          {t('storage.moveNoDestination')}
        </p>
      {:else}
        <div class="space-y-1.5">
          <Label for="move-vol-pool">{t('vmDetail.moveDestPool')}</Label>
          <select
            id="move-vol-pool"
            bind:value={moveVolDestPool}
            class="input w-full"
            disabled={moveVolBusy}
          >
            {#each moveVolOptions as p (p.name)}<option value={p.name}>{p.name}</option>{/each}
          </select>
        </div>

        <!-- Copy leaves the original in place. Useful for an ISO that
             several pools want; pointless for a disk, which would then
             exist twice with no link between the two. -->
        <div class="rounded-lg border border-border p-3 space-y-2">
          <div class="flex items-center justify-between gap-2">
            <span class="text-sm font-medium">{t('storage.moveKeepSource')}</span>
            <Switch
              bind:checked={moveVolCopy}
              disabled={moveVolBusy}
              ariaLabel={t('storage.moveKeepSource')}
            />
          </div>
          <p class="text-xs text-muted-foreground">
            {moveVolCopy ? t('storage.moveKeepSourceOn') : t('storage.moveKeepSourceOff')}
          </p>
        </div>

        {#if !moveVolCopy}
          <!-- Only a real move can strand a VM that references the
               file; a copy leaves the original untouched. -->
          <p class="text-xs text-muted-foreground flex items-start gap-1.5">
            <Icon name="alert-triangle" size={13} class="mt-0.5 shrink-0 text-warning" />
            <span>
              {moveVolKind === 'iso'
                ? t('storage.moveIsoInUseHint')
                : t('storage.moveDiskInUseHint')}
            </span>
          </p>
        {/if}

        {#if moveVolProgress}
          <div class="space-y-1.5">
            <div class="flex items-center justify-between text-xs">
              <span class="text-muted-foreground">{moveStageLabel(moveVolProgress.stage)}</span>
              <span class="tnum">{Math.round(moveVolProgress.pct)}%</span>
            </div>
            <div class="h-1.5 rounded-full bg-muted overflow-hidden">
              <div
                class="h-full bg-accent transition-[width] duration-300"
                style="width: {Math.max(2, Math.min(100, moveVolProgress.pct))}%"
              ></div>
            </div>
          </div>
        {/if}
      {/if}
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showMoveVol = false)} disabled={moveVolBusy}>
        {t('common.cancel')}
      </Button>
      <Button onclick={doMoveVolume} disabled={moveVolBusy || !moveVolDestPool}>
        {#if moveVolBusy}<Spinner size="xs" class="mr-1" />{/if}
        {moveVolCopy ? t('storage.moveCopyConfirm') : t('vmDetail.moveStorageConfirm')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Volume Resize Modal -->
<Dialog.Root bind:open={showResizeVol}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title
        >{t('storage.resizeVolume', {
          name: resizeVolName,
          current: resizeVolCurrent,
        })}</Dialog.Title
      >
      <Dialog.Description>
        {t('storage.resizeVolumeDesc')}
      </Dialog.Description>
    </Dialog.Header>
    <div class="space-y-3 my-2">
      <div>
        <label for="resize-vol-size" class="block text-xs font-semibold mb-1"
          >{t('storage.newSizeGbLabel')}</label
        >
        <Input
          id="resize-vol-size"
          type="number"
          min={resizeVolCurrent}
          bind:value={resizeVolSize}
          class="w-full tnum font-mono"
        />
      </div>
    </div>
    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showResizeVol = false)}>{t('common.cancel')}</Button
      >
      <Button disabled={volResizing || resizeVolSize <= resizeVolCurrent} onclick={resizeVolume}>
        {#if volResizing}<Spinner size="xs" class="mr-1" />{/if}
        {t('storage.resize')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Snippet Editor Modal -->
<Dialog.Root bind:open={showSnippetModal}>
  <Dialog.Content class="sm:max-w-2xl max-h-[92vh] flex flex-col">
    <Dialog.Header>
      <Dialog.Title>
        {editingSnippet
          ? editingSnippet.is_preset
            ? t('storage.presetSnippetDetailTitle')
            : t('storage.editSnippet')
          : snippetForm.name?.includes('(Personalizado)') || snippetForm.name?.includes('(Copia)')
            ? t('storage.cloneRecipeTitle')
            : t('storage.newSnippet')}
      </Dialog.Title>
      <Dialog.Description>
        {t('storage.snippetEditorDesc')}
      </Dialog.Description>
    </Dialog.Header>

    <div class="flex-1 min-w-0 space-y-3.5 my-2 overflow-y-auto pr-1">
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
        <div>
          <label for="sn-name" class="block text-xs font-semibold mb-1"
            >{t('storage.snippetName')}</label
          >
          <Input
            id="sn-name"
            bind:value={snippetForm.name}
            placeholder={t('storage.snippetNamePlaceholder')}
            disabled={editingSnippet?.is_preset}
          />
        </div>
        <div>
          <label for="sn-category" class="block text-xs font-semibold mb-1"
            >{t('storage.categoryLabel')}</label
          >
          <select
            id="sn-category"
            bind:value={snippetForm.category}
            class="input w-full !text-xs font-medium"
            disabled={editingSnippet?.is_preset}
          >
            <option value="containers">{t('storage.catContainers')}</option>
            <option value="kubernetes">{t('storage.catKubernetes')}</option>
            <option value="security">{t('storage.catSecurity')}</option>
            <option value="networking">{t('storage.catNetworking')}</option>
            <option value="monitoring">{t('storage.catMonitoring')}</option>
            <option value="web">{t('storage.catWeb')}</option>
            <option value="devops">{t('storage.catDevops')}</option>
            <option value="custom">{t('storage.catCustom')}</option>
          </select>
        </div>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
        <div>
          <label for="sn-type" class="block text-xs font-semibold mb-1"
            >{t('storage.snippetType')}</label
          >
          <select
            id="sn-type"
            bind:value={snippetForm.type}
            class="input w-full !text-xs font-medium"
            disabled={editingSnippet?.is_preset}
          >
            <option value="user-data">{t('storage.typeUserData')}</option>
            <option value="meta-data">{t('storage.typeMetaData')}</option>
            <option value="network-config">{t('storage.typeNetworkConfig')}</option>
          </select>
        </div>
        <div>
          <label for="sn-desc" class="block text-xs font-semibold mb-1"
            >{t('storage.snippetDescription')}</label
          >
          <Input
            id="sn-desc"
            bind:value={snippetForm.description}
            placeholder={t('storage.snippetDescPlaceholder')}
            disabled={editingSnippet?.is_preset}
          />
        </div>
      </div>

      <!-- Variable Insertion Toolbar -->
      {#if !editingSnippet?.is_preset}
        <div
          class="flex flex-wrap items-center gap-1.5 p-2 rounded-lg bg-muted/30 border border-border"
        >
          <span class="text-[10px] font-semibold text-muted-foreground uppercase mr-1"
            >{t('storage.insertVariableLabel')}</span
          >
          <button
            type="button"
            onclick={() => insertVariable('{{ .VM.Hostname }}')}
            class="px-2 py-0.5 rounded bg-background border border-border text-[10px] font-mono text-accent hover:border-accent transition-colors"
          >
            {t('storage.varHostname')}
          </button>
          <button
            type="button"
            onclick={() => insertVariable('{{ .VM.User }}')}
            class="px-2 py-0.5 rounded bg-background border border-border text-[10px] font-mono text-accent hover:border-accent transition-colors"
          >
            {t('storage.varUser')}
          </button>
          <button
            type="button"
            onclick={() => insertVariable('{{ .VM.IP }}')}
            class="px-2 py-0.5 rounded bg-background border border-border text-[10px] font-mono text-accent hover:border-accent transition-colors"
          >
            {t('storage.varIp')}
          </button>
          <button
            type="button"
            onclick={() => insertVariable('{{ .VM.SSHKey }}')}
            class="px-2 py-0.5 rounded bg-background border border-border text-[10px] font-mono text-accent hover:border-accent transition-colors"
          >
            {t('storage.varSshKey')}
          </button>
        </div>
      {/if}

      <!-- YAML Editor & Validator -->
      <div>
        <div class="flex items-center justify-between mb-1">
          <label
            for="sn-content"
            class="text-xs font-semibold text-foreground flex items-center gap-2"
          >
            <span>{t('storage.snippetContent')}</span>
            {#if snippetForm.content.trim().startsWith('#cloud-config')}
              <span
                class="text-[10px] font-mono font-bold text-success bg-success/10 px-1.5 py-0.2 rounded border border-success/20"
              >
                {t('storage.cloudConfigValid')}
              </span>
            {:else}
              <span
                class="text-[10px] font-mono text-warning bg-warning/10 px-1.5 py-0.2 rounded border border-warning/20 flex items-center gap-1"
              >
                <Icon name="warning" size={10} />
                {t('storage.cloudConfigRecommendation')}
              </span>
            {/if}
          </label>

          <div class="flex items-center gap-1.5">
            <Button
              type="button"
              size="sm"
              variant="outline"
              class="!h-6 !text-[11px] !px-2"
              onclick={() => copySnippetContent(snippetForm.content)}
            >
              <Icon name="copy" size={11} class="mr-1" />
              {t('storage.copyButton')}
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              class="!h-6 !text-[11px] !px-2 {previewingRender
                ? 'bg-accent text-accent-foreground font-bold'
                : ''}"
              onclick={testPreviewSnippet}
              disabled={previewLoading || !snippetForm.content.trim()}
            >
              {#if previewLoading}<Spinner size="xs" class="mr-1" />{:else}<Icon
                  name="play"
                  size={11}
                  class="mr-1"
                />{/if}
              {t('storage.testRenderButton')}
            </Button>
          </div>
        </div>

        {#if previewingRender}
          <div class="space-y-1 my-2">
            <div
              class="flex items-center justify-between text-[11px] text-muted-foreground font-mono"
            >
              <span>{t('storage.previewResultHeader')}</span>
              <button onclick={() => (previewingRender = false)} class="text-accent hover:underline"
                >{t('storage.hideButton')}</button
              >
            </div>
            <pre
              class="w-full font-mono text-xs p-3 rounded-lg bg-card border border-accent/40 max-h-48 overflow-y-auto text-success whitespace-pre">{previewRenderResult ||
                t('storage.noOutput')}</pre>
          </div>
        {/if}

        <textarea
          id="sn-content"
          bind:value={snippetForm.content}
          rows="12"
          class="w-full font-mono text-xs p-3 rounded-lg bg-background border border-border focus:outline-none focus:ring-2 focus:ring-accent/40 leading-relaxed"
          placeholder="#cloud-config"
          disabled={editingSnippet?.is_preset}
        ></textarea>
      </div>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showSnippetModal = false)}>
        {t('common.cancel')}
      </Button>
      {#if !editingSnippet?.is_preset}
        <Button
          onclick={saveSnippet}
          disabled={snippetSaving || !snippetForm.name.trim() || !snippetForm.content.trim()}
        >
          {#if snippetSaving}<Spinner size="xs" class="mr-1" />{/if}
          {t('common.save')}
        </Button>
      {/if}
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Rename ISO Dialog -->
<Dialog.Root bind:open={showRenameISO}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('storage.renameIso')}</Dialog.Title>
      <Dialog.Description>{t('storage.currentIso', { name: renameOldName })}</Dialog.Description>
    </Dialog.Header>
    <div class="space-y-2">
      <label for="rename-iso-input" class="block text-sm font-medium">{t('storage.newName')}</label>
      <Input id="rename-iso-input" bind:value={renameNewName} placeholder="new-name.iso" />
      <p class="text-xs text-muted-foreground">
        {@html t('storage.mustEndWithIso', {
          code: htmlVar('<code>.iso</code>'),
          code2: htmlVar('<code>.img</code>'),
        })}
      </p>
    </div>
    <Dialog.Footer class="gap-2">
      <Button
        variant="outline"
        onclick={() => {
          showRenameISO = false;
          renameOldName = '';
          renameNewName = '';
        }}
        disabled={renaming}>{t('common.cancel')}</Button
      >
      <Button
        onclick={doRenameISO}
        disabled={renaming || !renameNewName || renameNewName === renameOldName}
      >
        {renaming ? t('storage.renaming') : t('storage.rename')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Initialize Host Disk Directory Dialog (Proxmox-Style) -->
<Dialog.Root bind:open={showInitDisk}>
  <Dialog.Content class="sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>{t('storage.initDiskTitle')}</Dialog.Title>
      <Dialog.Description>
        {t('storage.initDiskDesc', { name: initDiskName || 'nombre' })}
      </Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3.5 my-2 text-xs">
      <div>
        <label for="init-disk-select" class="block font-semibold mb-1"
          >{t('storage.physicalDiskLabel')}</label
        >
        <select
          id="init-disk-select"
          bind:value={initDiskPath}
          class="input w-full font-mono text-xs"
        >
          {#each hostDisks.filter((d) => !d.is_system) as d (d.path)}
            <option value={d.path}>
              {d.path} ({d.size_human} · {d.model || t('storage.genericDiskFallback')})
            </option>
          {/each}
        </select>
      </div>

      <div>
        <label for="init-vol-name" class="block font-semibold mb-1"
          >{t('storage.volumeNameIdLabel')}</label
        >
        <Input
          id="init-vol-name"
          bind:value={initDiskName}
          oninput={() => {
            initDiskMount = `/mnt/${initDiskName.trim() || 'storage'}`;
          }}
          placeholder={t('storage.volumeNamePlaceholder')}
          class="w-full font-medium"
        />
      </div>

      <div>
        <label for="init-mount-point" class="block font-semibold mb-1"
          >{t('storage.hostMountPointLabel')}</label
        >
        <Input
          id="init-mount-point"
          bind:value={initDiskMount}
          placeholder={t('storage.mountPointPlaceholder')}
          class="w-full font-mono"
        />
        <p class="text-[11px] text-muted-foreground mt-0.5">
          {t('storage.systemdAutoHint')}
        </p>
      </div>

      <!-- Operation mode: format (destructive) or mount (preserves data) -->
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
        <button
          type="button"
          onclick={() => (initDiskMode = 'format')}
          class="flex flex-col items-start gap-0.5 p-2.5 rounded-lg border text-left transition-colors {initDiskMode ===
          'format'
            ? 'border-destructive bg-destructive/10'
            : 'border-border hover:bg-muted/30'}"
        >
          <span class="flex items-center gap-1.5 font-semibold text-xs">
            <Icon
              name="trash"
              size={13}
              class={initDiskMode === 'format' ? 'text-destructive' : 'text-muted-foreground'}
            />
            {t('storage.formatDiskOption')}
          </span>
          <span class="text-[10px] text-muted-foreground">{t('storage.formatDiskOptionDesc')}</span>
        </button>
        <button
          type="button"
          onclick={() => (initDiskMode = 'mount')}
          class="flex flex-col items-start gap-0.5 p-2.5 rounded-lg border text-left transition-colors {initDiskMode ===
          'mount'
            ? 'border-success bg-success/10'
            : 'border-border hover:bg-muted/30'}"
        >
          <span class="flex items-center gap-1.5 font-semibold text-xs">
            <Icon
              name="shield"
              size={13}
              class={initDiskMode === 'mount' ? 'text-success' : 'text-muted-foreground'}
            />
            {t('storage.mountExistingOption')}
          </span>
          <span class="text-[10px] text-muted-foreground"
            >{t('storage.mountExistingOptionDesc')}</span
          >
        </button>
      </div>

      {#if initDiskMode === 'format' && initDiskExistingFs.length > 0}
        <div class="rounded-lg border border-destructive/40 bg-destructive/5 p-2.5">
          <p class="text-[11px] text-destructive flex items-start gap-1.5">
            <Icon name="alertTriangle" size={12} class="mt-0.5 shrink-0" />
            <span
              >{t('storage.existingDataWarning', {
                fstypes: initDiskExistingFs.map((f) => f.fstype).join(', '),
              })}</span
            >
          </p>
        </div>
      {/if}

      {#if initDiskMode === 'mount'}
        <div class="rounded-lg border border-success/30 bg-success/5 p-2.5">
          <p class="text-[11px] text-success flex items-start gap-1.5">
            <Icon name="shield" size={12} class="mt-0.5 shrink-0" />
            <span>{t('storage.mountOnlyNotice')}</span>
          </p>
          {#if initDiskExistingFs.length > 0}
            <div class="mt-2">
              <label for="init-existing-fs" class="block font-semibold mb-1 text-[11px]"
                >{t('storage.deviceFsLabel')}</label
              >
              <select
                id="init-existing-fs"
                bind:value={initDiskDevice}
                class="input w-full text-xs font-mono"
              >
                {#each initDiskExistingFs as fs (fs.path)}
                  <option value={fs.path}>{fs.label}</option>
                {/each}
              </select>
            </div>
          {:else}
            <p class="text-[11px] text-warning mt-2">
              {t('storage.noFsDetectedWarning')}
            </p>
          {/if}
        </div>
      {/if}

      <div>
        <div>
          <label for="init-disk-fs" class="block font-semibold mb-1"
            >{t('storage.filesystemTitle')}</label
          >
          <select
            id="init-disk-fs"
            bind:value={initDiskFs}
            class="input w-full text-xs"
            disabled={initDiskMode === 'mount' || availableFilesystems.length === 0}
          >
            {#each availableFilesystems as fs (fs.id)}
              {@const descKey = `storage.fs${fs.id[0].toUpperCase()}${fs.id.slice(1)}Desc`}
              <option value={fs.id}>
                {fsDescKeys.has(descKey) ? t(descKey) : fs.label}
              </option>
            {/each}
          </select>
          {#if availableFilesystems.length === 0}
            <p class="text-[10px] text-warning mt-1">{t('storage.fsNoneAvailable')}</p>
          {/if}
        </div>
      </div>

      <!-- Mount / boot options -->
      <div class="pt-2 border-t border-border space-y-2">
        <p class="font-semibold flex items-center gap-1.5">
          <Icon name="power" size={13} class="text-accent" />
          {t('storage.mountOptionsTitle')}
        </p>

        <label class="flex items-start gap-2.5 cursor-pointer select-none">
          <input
            type="checkbox"
            bind:checked={initDiskNoFail}
            class="w-4 h-4 rounded border-border mt-0.5"
          />
          <div class="min-w-0">
            <span class="text-xs font-medium">{t('storage.optNoFail')}</span>
            <span class="text-[11px] text-muted-foreground block">{t('storage.optNoFailDesc')}</span
            >
          </div>
        </label>

        <label class="flex items-start gap-2.5 cursor-pointer select-none">
          <input
            type="checkbox"
            bind:checked={initDiskAutomount}
            class="w-4 h-4 rounded border-border mt-0.5"
          />
          <div class="min-w-0">
            <span class="text-xs font-medium">{t('storage.optAutomount')}</span>
            <span class="text-[11px] text-muted-foreground block"
              >{t('storage.optAutomountDesc')}</span
            >
          </div>
        </label>

        <label class="flex items-start gap-2.5 cursor-pointer select-none">
          <input
            type="checkbox"
            bind:checked={initDiskReadOnly}
            class="w-4 h-4 rounded border-border mt-0.5"
          />
          <div class="min-w-0">
            <span class="text-xs font-medium">{t('storage.optReadOnly')}</span>
            <span class="text-[11px] text-muted-foreground block"
              >{t('storage.optReadOnlyDesc')}</span
            >
          </div>
        </label>

        {#if activeFsPresets.length > 0}
          <div>
            <p class="font-semibold mb-1 text-[11px] flex items-center gap-1.5">
              <Icon name="gauge" size={12} class="text-accent" />
              {t('storage.presetsTitle')}
            </p>
            <div class="flex flex-wrap gap-1.5">
              {#each activeFsPresets as preset (preset.opt)}
                {@const active = isPresetActive(preset.opt)}
                <button
                  type="button"
                  onclick={() => togglePreset(preset.opt)}
                  title={t(preset.descKey)}
                  class="group inline-flex items-center gap-1 px-2 py-1 rounded-full border text-[11px] font-mono transition-all duration-150 active:scale-95 {active
                    ? 'border-accent bg-accent/15 text-accent font-semibold'
                    : 'border-border text-muted-foreground hover:text-foreground hover:border-foreground/30 hover:bg-muted/40'}"
                >
                  <Icon
                    name={active ? 'check' : 'plus'}
                    size={10}
                    class="shrink-0 transition-transform duration-150 {active
                      ? ''
                      : 'group-hover:rotate-90'}"
                  />
                  {preset.opt}
                </button>
              {/each}
            </div>
            <p class="text-[10px] text-muted-foreground mt-1">{t('storage.presetsHint')}</p>
          </div>
        {/if}

        <div>
          <label for="init-extra-opts" class="block font-semibold mb-1 text-[11px]"
            >{t('storage.extraMountOptions')}</label
          >
          <Input
            id="init-extra-opts"
            bind:value={initDiskExtraOptions}
            placeholder={t('storage.extraOptsPlaceholder')}
            class="w-full font-mono text-xs"
          />
        </div>

        <div class="rounded-lg border border-border bg-muted/30 p-2 flex items-start gap-1.5">
          <Icon name="fileText" size={12} class="text-muted-foreground mt-0.5 shrink-0" />
          <code class="text-[11px] font-mono break-all text-foreground"
            >Options={initDiskOptionsPreview}</code
          >
        </div>
      </div>

      <!-- Subcarpetas estándar del pool -->
      <div class="pt-2 border-t border-border">
        <p class="font-semibold mb-2 flex items-center gap-1.5">
          <Icon name="layers" size={13} class="text-accent" />
          {t('storage.subfoldersTitle')}
        </p>
        <p class="text-[11px] text-muted-foreground mb-2">
          {t('storage.natureMultiHint')}
        </p>
        <div class="space-y-1.5">
          {#each initDiskSubfolders as sub (sub.id)}
            {@const active = initDiskSelectedSubfolders.includes(sub.id)}
            {@const pool = poolForFolder(
              sub.id,
              initDiskName,
              t('storage.poolPreviewNamePlaceholder')
            )}
            <label
              class="flex items-start gap-2.5 p-2 rounded-lg border cursor-pointer transition-colors {active
                ? 'border-accent/50 bg-accent/5'
                : 'border-border hover:bg-muted/30'}"
            >
              <input
                type="checkbox"
                checked={active}
                onchange={() => toggleInitSubfolder(sub.id)}
                class="w-4 h-4 rounded border-border mt-0.5"
              />
              <div class="min-w-0 flex-1">
                <span class="font-mono text-xs font-semibold">{sub.path}</span>
                <span class="text-[11px] text-muted-foreground"> — {sub.desc}</span>
                <!-- El pool que genera esta carpeta, aquí mismo: marcar
                     la casilla y leer el nombre del pool no deben ser
                     dos sitios distintos de la pantalla. -->
                {#if pool}
                  <span
                    class="flex items-center gap-1.5 mt-1 text-[11px] {active
                      ? 'text-foreground'
                      : 'text-muted-foreground/60'}"
                  >
                    <Icon
                      name={pool.icon}
                      size={12}
                      class={active ? 'text-accent shrink-0' : 'shrink-0'}
                    />
                    <span class="shrink-0">{t('storage.poolArrow')}</span>
                    <code class="font-semibold truncate">{pool.name}</code>
                    <span class="shrink-0 text-muted-foreground">· {pool.backend}</span>
                    {#if !active}
                      <span class="shrink-0 text-muted-foreground/70"
                        >· {t('storage.poolNotCreated')}</span
                      >
                    {/if}
                  </span>
                {/if}
              </div>
            </label>
          {/each}
        </div>
        {#if initDiskSelectedSubfolders.length > 0}
          <p class="text-[11px] text-muted-foreground mt-2 font-mono break-all">
            {(initDiskMount.trim() || `/mnt/${initDiskName.trim() || 'storage'}`) +
              '/' +
              initDiskSelectedSubfolders.join('  ·  ')}
          </p>
        {/if}
      </div>

      <div class="pt-2 border-t border-border space-y-2">
        <label class="flex items-center gap-2 cursor-pointer font-medium select-none">
          <input
            type="checkbox"
            bind:checked={initDiskCreatePool}
            class="w-4 h-4 rounded border-border"
          />
          <span>{t('storage.autoCreatePoolLabel')}</span>
        </label>
        <!-- Recuento de lo que se va a registrar, justo bajo la casilla
             que lo activa; el detalle por pool está en cada carpeta. -->
        {#if initDiskCreatePool}
          {#if poolsToCreate.length > 0}
            <p class="text-[11px] text-muted-foreground flex items-center gap-1.5 flex-wrap">
              <Icon name="check" size={12} class="text-success shrink-0" />
              <span>{t('storage.poolsToCreateCount', { count: poolsToCreate.length })}</span>
              {#each poolsToCreate as pool (pool.name)}
                <code class="text-foreground">{pool.name}</code>
              {/each}
            </p>
          {:else}
            <p
              class="text-[11px] text-warning flex items-start gap-1.5 rounded-lg border border-warning/30 bg-warning/5 p-2"
            >
              <Icon name="alertTriangle" size={12} class="mt-0.5 shrink-0" />
              <span>{t('storage.poolsToCreateNone')}</span>
            </p>
          {/if}
        {/if}
        {#if initDiskSelectedSubfolders.includes('backups')}
          <label class="flex items-center gap-2 cursor-pointer font-medium select-none">
            <input
              type="checkbox"
              bind:checked={initDiskRegisterBackup}
              class="w-4 h-4 rounded border-border"
            />
            <span>{t('storage.registerBackupTarget')}</span>
          </label>
        {/if}
      </div>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showInitDisk = false)} disabled={initDiskBusy}>
        {t('common.cancel')}
      </Button>
      <Button
        onclick={performInitDisk}
        disabled={initDiskBusy || !initDiskPath || !initDiskName.trim()}
      >
        {#if initDiskBusy}
          <Spinner size="sm" color="text-white" /> {t('storage.initializing')}
        {:else}
          <Icon name="check" size={14} class="mr-1" />
          {t('storage.initAndMountButton')}
        {/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Create ZFS Pool Dialog -->
<Dialog.Root bind:open={showCreateZPool}>
  <Dialog.Content class="sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>{t('storage.createZPoolTitle')}</Dialog.Title>
      <Dialog.Description>{t('storage.createZPoolDesc')}</Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3.5 my-2 text-xs">
      <div>
        <label for="zpool-name" class="block font-semibold mb-1">{t('storage.poolNameLabel')}</label
        >
        <Input
          id="zpool-name"
          bind:value={createZPoolName}
          placeholder={t('storage.zpoolNamePlaceholder')}
          class="w-full font-mono"
        />
      </div>

      <div>
        <label for="zpool-topology" class="block font-semibold mb-1"
          >{t('storage.topologyLabel')}</label
        >
        <select
          id="zpool-topology"
          bind:value={createZPoolTopology}
          class="input w-full font-medium text-xs"
        >
          <option value="stripe">{t('storage.topologyStripe')}</option>
          <option value="mirror">{t('storage.topologyMirror')}</option>
          <option value="raidz1">{t('storage.topologyRaidz1')}</option>
          <option value="raidz2">{t('storage.topologyRaidz2')}</option>
        </select>
      </div>

      <div>
        <span class="block font-semibold mb-1">{t('storage.selectDisksLabel')}</span>
        <div
          class="max-h-48 overflow-y-auto space-y-1.5 p-2 rounded-lg border border-border bg-muted/10"
        >
          {#each hostDisks.filter((d) => !d.is_system) as disk (disk.path)}
            <label
              class="flex items-center gap-2.5 p-1.5 rounded hover:bg-muted/20 cursor-pointer select-none"
            >
              <input
                type="checkbox"
                value={disk.path}
                checked={createZPoolDisks.includes(disk.path)}
                onchange={(e) => {
                  if (e.target.checked) {
                    createZPoolDisks = [...createZPoolDisks, disk.path];
                  } else {
                    createZPoolDisks = createZPoolDisks.filter((p) => p !== disk.path);
                  }
                }}
                class="w-4 h-4 rounded border-border"
              />
              <span class="font-mono text-xs font-medium text-foreground">{disk.path}</span>
              <span class="text-[11px] text-muted-foreground"
                >({disk.size_human} · {disk.model || 'Disk'})</span
              >
            </label>
          {:else}
            <p class="text-xs text-muted-foreground p-2">{t('storage.noHostDisks')}</p>
          {/each}
        </div>
        {#if createZPoolDisks.length < minDisksForTopology(createZPoolTopology)}
          <p class="text-[11px] text-warning mt-1">
            {t('storage.minDisksRequired', {
              topology: createZPoolTopology,
              min: minDisksForTopology(createZPoolTopology),
            })}
          </p>
        {/if}
      </div>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showCreateZPool = false)} disabled={creatingZPool}>
        {t('common.cancel')}
      </Button>
      <Button
        onclick={doCreateZPool}
        disabled={creatingZPool ||
          !createZPoolName.trim() ||
          createZPoolDisks.length < minDisksForTopology(createZPoolTopology)}
      >
        {#if creatingZPool}
          <Spinner size="sm" color="text-white" /> {t('storage.creatingZPool')}
        {:else}
          <Icon name="check" size={14} class="mr-1" />
          {t('storage.createZPoolBtn')}
        {/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Create ZFS Volume (zvol) Dialog -->
<Dialog.Root bind:open={showCreateZVol}>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{t('storage.createZVolTitle')}</Dialog.Title>
      <Dialog.Description>{t('storage.createZVolDesc')}</Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3.5 my-2 text-xs">
      <div>
        <label for="zvol-pool" class="block font-semibold mb-1"
          >{t('storage.targetPoolLabel')}</label
        >
        <select id="zvol-pool" bind:value={createZVolPool} class="input w-full font-mono text-xs">
          {#each zpools as pool (pool.name)}
            <option value={pool.name}>{pool.name} ({pool.free_human} free)</option>
          {/each}
        </select>
      </div>

      <div>
        <label for="zvol-name" class="block font-semibold mb-1">{t('storage.zvolNameLabel')}</label>
        <Input
          id="zvol-name"
          bind:value={createZVolName}
          placeholder={t('storage.zvolNamePlaceholder')}
          class="w-full font-mono"
        />
      </div>

      <div>
        <label for="zvol-size" class="block font-semibold mb-1">{t('storage.sizeGbLabel')}</label>
        <Input
          id="zvol-size"
          type="number"
          min="1"
          bind:value={createZVolSizeGB}
          class="w-full font-mono"
        />
      </div>

      <div class="pt-2 border-t border-border">
        <label class="flex items-start gap-2.5 cursor-pointer select-none">
          <input
            type="checkbox"
            bind:checked={createZVolSparse}
            class="w-4 h-4 rounded border-border mt-0.5"
          />
          <div class="min-w-0">
            <span class="text-xs font-medium">{t('storage.sparseLabel')}</span>
            <span class="text-[11px] text-muted-foreground block">{t('storage.sparseHint')}</span>
          </div>
        </label>
      </div>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showCreateZVol = false)} disabled={creatingZVol}>
        {t('common.cancel')}
      </Button>
      <Button
        onclick={doCreateZVol}
        disabled={creatingZVol ||
          !createZVolPool ||
          !createZVolName.trim() ||
          createZVolSizeGB <= 0}
      >
        {#if creatingZVol}
          <Spinner size="sm" color="text-white" /> {t('storage.creatingZVol')}
        {:else}
          <Icon name="check" size={14} class="mr-1" />
          {t('storage.createZVolBtn')}
        {/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Create Linux Software RAID (mdadm) Dialog -->
<Dialog.Root bind:open={showCreateRaid}>
  <Dialog.Content class="sm:max-w-lg">
    <Dialog.Header>
      <Dialog.Title>{t('storage.createRaidTitle')}</Dialog.Title>
      <Dialog.Description>{t('storage.createRaidDesc')}</Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3.5 my-2 text-xs">
      <div>
        <label for="raid-level" class="block font-semibold mb-1"
          >{t('storage.raidLevelLabel')}</label
        >
        <select
          id="raid-level"
          bind:value={createRaidLevel}
          class="input w-full font-medium text-xs"
        >
          <option value="0">{t('storage.raidLevel0')}</option>
          <option value="1">{t('storage.raidLevel1')}</option>
          <option value="5">{t('storage.raidLevel5')}</option>
          <option value="6">{t('storage.raidLevel6')}</option>
          <option value="10">{t('storage.raidLevel10')}</option>
        </select>
      </div>

      <div>
        <label for="raid-device" class="block font-semibold mb-1"
          >{t('storage.raidDeviceLabel')}</label
        >
        <Input
          id="raid-device"
          bind:value={createRaidDevice}
          placeholder={t('storage.raidDevicePlaceholder')}
          class="w-full font-mono"
        />
      </div>

      <div>
        <span class="block font-semibold mb-1">{t('storage.selectDisksLabel')}</span>
        <div
          class="max-h-48 overflow-y-auto space-y-1.5 p-2 rounded-lg border border-border bg-muted/10"
        >
          {#each hostDisks.filter((d) => !d.is_system && d.type !== 'loop') as disk (disk.path)}
            <label
              class="flex items-center gap-2.5 p-1.5 rounded hover:bg-muted/20 cursor-pointer select-none"
            >
              <input
                type="checkbox"
                value={disk.path}
                checked={createRaidDisks.includes(disk.path)}
                onchange={(e) => {
                  if (e.target.checked) {
                    createRaidDisks = [...createRaidDisks, disk.path];
                  } else {
                    createRaidDisks = createRaidDisks.filter((p) => p !== disk.path);
                  }
                }}
                class="w-4 h-4 rounded border-border"
              />
              <span class="font-mono text-xs font-medium text-foreground">{disk.path}</span>
              <span class="text-[11px] text-muted-foreground"
                >({disk.size_human} · {disk.model || 'Disk'})</span
              >
            </label>
          {:else}
            <p class="text-xs text-muted-foreground p-2">{t('storage.noHostDisks')}</p>
          {/each}
        </div>
        {#if createRaidDisks.length < minDisksForRaid(createRaidLevel)}
          <p class="text-[11px] text-warning mt-1">
            {t('storage.minRaidDisksRequired', {
              level: createRaidLevel,
              min: minDisksForRaid(createRaidLevel),
            })}
          </p>
        {/if}
      </div>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (showCreateRaid = false)} disabled={creatingRaid}>
        {t('common.cancel')}
      </Button>
      <Button
        onclick={doCreateRaid}
        disabled={creatingRaid || createRaidDisks.length < minDisksForRaid(createRaidLevel)}
      >
        {#if creatingRaid}
          <Spinner size="sm" color="text-white" /> {t('storage.creatingRaid')}
        {:else}
          <Icon name="check" size={14} class="mr-1" />
          {t('storage.createRaidBtn')}
        {/if}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<!-- Confirm Dialog -->
<ConfirmDialog
  bind:open={confirmState.open}
  title={confirmState.title}
  description={confirmState.description}
  confirmLabel={confirmState.confirmLabel}
  variant={confirmState.variant}
  loading={confirmState.loading}
  onConfirm={confirmState.onConfirm}
/>

<ErrorModal bind:open={showStorageError} title={storageErrorTitle} message={storageErrorMessage} />

<SmartModal
  disk={selectedSmartDisk}
  open={showSmartModal}
  onClose={() => {
    showSmartModal = false;
    selectedSmartDisk = null;
  }}
/>
