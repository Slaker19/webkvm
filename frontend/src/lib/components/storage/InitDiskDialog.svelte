<script>
  import * as Dialog from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { toast } from '$lib/components/ui/toast';
  import { upsertTask, updateTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { poolForFolder, poolsForFolders } from '$lib/purpose.js';

  let { open = $bindable(false), disk = null, hostDisks = [], onInitialized = () => {} } = $props();

  let initDiskPath = $state('');
  let initDiskName = $state('');
  let initDiskMount = $state('');
  let initDiskFs = $state('ext4');
  let initDiskCreatePool = $state(true);
  let initDiskBusy = $state(false);
  let initDiskRegisterBackup = $state(true);
  let initDiskMode = $state('format');
  let initDiskDevice = $state('');
  let initDiskNoFail = $state(true);
  let initDiskAutomount = $state(true);
  let initDiskReadOnly = $state(false);
  let initDiskExtraOptions = $state('');
  let availableFilesystems = $state([]);

  const DEFAULT_INIT_SUBFOLDERS = ['isos', 'discos', 'contenedores', 'backups', 'plantillas'];
  let initDiskSelectedSubfolders = $state([...DEFAULT_INIT_SUBFOLDERS]);

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

  const poolsToCreate = $derived(poolsForFolders(initDiskSelectedSubfolders, initDiskName));

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
      if (availableFilesystems.length && !availableFilesystems.some((fs) => fs.id === initDiskFs)) {
        initDiskFs = availableFilesystems[0].id;
      }
    } catch {
      availableFilesystems = [
        { id: 'ext4', label: 'ext4', available: true },
        { id: 'xfs', label: 'XFS', available: true },
      ];
    }
  }

  $effect(() => {
    if (open) {
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
      loadFilesystems();
    }
  });

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

  const initDiskExistingFs = $derived.by(() => {
    const d = hostDisks.find((x) => x.path === initDiskPath);
    if (!d) return [];
    const out = [];
    if (d.fstype) out.push({ path: d.path, fstype: d.fstype, label: d.path });
    for (const c of d.children || []) {
      if (c.fstype) out.push({ path: c.path, fstype: c.fstype, label: `${c.path} (${c.fstype})` });
    }
    return out;
  });

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

  function togglePreset(opt) {
    const opts = currentExtraOpts();
    const idx = opts.indexOf(opt);
    if (idx >= 0) {
      opts.splice(idx, 1);
    } else {
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

  function toggleInitSubfolder(id) {
    if (initDiskSelectedSubfolders.includes(id)) {
      initDiskSelectedSubfolders = initDiskSelectedSubfolders.filter((x) => x !== id);
    } else {
      initDiskSelectedSubfolders = [...initDiskSelectedSubfolders, id];
    }
  }

  async function performInitDisk() {
    if (!initDiskPath || !initDiskName.trim()) return;
    initDiskBusy = true;
    const taskId = 'disk-init:' + initDiskPath;
    upsertTask({
      id: taskId,
      kind: 'storage',
      title: `Inicializando disco: ${initDiskPath} (${initDiskName.trim()})`,
      pct: 25,
      message:
        initDiskMode === 'format'
          ? 'Formateando y preparando punto de montaje...'
          : 'Montando y preparando punto de montaje...',
      status: 'running',
    });
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
      updateTask(taskId, {
        pct: 85,
        message: 'Registrando pools de almacenamiento y persistencia...',
      });
      const createdPools = res?.pools || [];
      const failed = createdPools.filter((p) => p.status === 'error');
      const mismatched = createdPools.filter((p) => p.mismatch);
      if (failed.length > 0) {
        toast.error(
          `${t('storage.poolsFailed', { names: failed.map((p) => p.name).join(', ') })} — ${failed[0].error}`,
          { duration: 12000 }
        );
      } else if (mismatched.length > 0) {
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
      finishTask(taskId, 'success', 'Disco inicializado y montado con éxito', 100);
      open = false;
      onInitialized();
    } catch (e) {
      finishTask(taskId, 'error', e.message || 'Error al inicializar disco', 25);
      toast.error(e.message);
    } finally {
      initDiskBusy = false;
    }
  }
</script>

<Dialog.Root bind:open>
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
      <Button variant="outline" onclick={() => (open = false)} disabled={initDiskBusy}>
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
