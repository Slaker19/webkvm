<script>
  import * as Dialog from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { upsertTask, finishTask } from '$lib/stores/tasks.svelte.js';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';

  let {
    open = $bindable(false),
    hostDisks = [],
    onCreated = () => {},
    onError = () => {},
  } = $props();

  let createRaidLevel = $state('1');
  let createRaidDevice = $state('');
  let createRaidDisks = $state([]);
  let creatingRaid = $state(false);
  let createRaidFormat = $state(false);
  let createRaidFs = $state('ext4');
  let createRaidMount = $state('');
  let createRaidPool = $state(true);
  let createRaidSubfolders = $state(['discos', 'isos', 'backups']);
  let createRaidPoolMode = $state('multi');
  let createRaidSinglePurpose = $state('disk');

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

  $effect(() => {
    if (open) {
      createRaidLevel = '1';
      createRaidDevice = '';
      createRaidDisks = [];
      createRaidFormat = false;
      createRaidFs = 'ext4';
      createRaidMount = '';
      createRaidPool = true;
      createRaidSubfolders = ['discos', 'isos', 'backups'];
      createRaidPoolMode = 'multi';
      createRaidSinglePurpose = 'disk';
    }
  });

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
    const raidDevName = createRaidDevice.trim() || `md-${createRaidLevel}`;
    const taskId = 'raid-create:' + Date.now();
    upsertTask({
      id: taskId,
      kind: 'storage',
      title: `Creando arreglo RAID ${createRaidLevel}: ${raidDevName}`,
      pct: 30,
      message: 'Construyendo arreglo de discos en el host...',
      status: 'running',
    });
    try {
      const raidPayload = {
        name: createRaidDevice.trim() || undefined,
        level: createRaidLevel,
        devices: createRaidDisks,
      };
      if (createRaidFormat) {
        raidPayload.format = true;
        raidPayload.filesystem = createRaidFs;
        raidPayload.mount_point = createRaidMount.trim() || `/mnt/${raidDevName}`;
        raidPayload.volume_name = raidDevName;
        raidPayload.create_pool = createRaidPool;
        if (createRaidPool && createRaidPoolMode === 'multi') {
          raidPayload.subfolders = createRaidSubfolders;
        } else if (createRaidPool && createRaidPoolMode === 'single') {
          raidPayload.pool_purpose = createRaidSinglePurpose;
        }
      }
      const res = await api.createHostRaid(raidPayload);
      finishTask(taskId, 'success', `Arreglo RAID ${res.device} creado`, 100);
      open = false;
      onCreated(res);
      alert(t('storage.raidCreatedSuccess', { device: res.device }));
    } catch (e) {
      finishTask(taskId, 'error', e.message || 'Error al crear RAID', 30);
      onError('Create RAID Error', e);
    } finally {
      creatingRaid = false;
    }
  }
</script>

<Dialog.Root bind:open>
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

      <div class="pt-2 border-t border-border space-y-3">
        <label class="flex items-center gap-2 cursor-pointer select-none">
          <input
            type="checkbox"
            bind:checked={createRaidFormat}
            class="w-4 h-4 rounded border-border"
          />
          <span class="font-semibold text-xs">{t('storage.raidAutoFormatLabel')}</span>
        </label>
        <p class="text-[11px] text-muted-foreground ml-6">
          {t('storage.raidAutoFormatDesc')}
        </p>

        {#if createRaidFormat}
          <div class="space-y-3 pl-4 pt-1 border-l-2 border-primary/40 ml-2">
            <div>
              <label for="raid-fs" class="block font-semibold mb-1"
                >{t('storage.filesystemTitle')}</label
              >
              <select id="raid-fs" bind:value={createRaidFs} class="input w-full text-xs font-mono">
                <option value="ext4">ext4 (Linux estándar)</option>
                <option value="xfs">xfs (Alto rendimiento)</option>
                <option value="btrfs">btrfs (CoW con snapshots)</option>
              </select>
            </div>
            <div>
              <label for="raid-mount" class="block font-semibold mb-1"
                >{t('storage.colMountPoint')}</label
              >
              <Input
                id="raid-mount"
                bind:value={createRaidMount}
                placeholder={`/mnt/${createRaidDevice.trim() || 'md-' + createRaidLevel}`}
                class="w-full font-mono text-xs"
              />
            </div>
            <div>
              <label class="flex items-center gap-2 cursor-pointer select-none">
                <input
                  type="checkbox"
                  bind:checked={createRaidPool}
                  class="w-4 h-4 rounded border-border"
                />
                <span class="text-xs font-semibold">{t('storage.createPoolsCheck')}</span>
              </label>
            </div>

            {#if createRaidPool}
              <div class="space-y-2 pt-1 pl-3 border-l border-border/60">
                <div class="flex items-center gap-4 text-[11px]">
                  <label class="flex items-center gap-1.5 cursor-pointer select-none">
                    <input
                      type="radio"
                      name="raid-pool-mode"
                      value="multi"
                      bind:group={createRaidPoolMode}
                    />
                    <span>{t('storage.raidPoolModeMulti')}</span>
                  </label>
                  <label class="flex items-center gap-1.5 cursor-pointer select-none">
                    <input
                      type="radio"
                      name="raid-pool-mode"
                      value="single"
                      bind:group={createRaidPoolMode}
                    />
                    <span>{t('storage.raidPoolModeSingle')}</span>
                  </label>
                </div>

                {#if createRaidPoolMode === 'single'}
                  <div>
                    <label for="raid-single-purpose" class="block font-semibold mb-1 text-[11px]"
                      >{t('storage.raidSinglePurposeLabel')}</label
                    >
                    <select
                      id="raid-single-purpose"
                      bind:value={createRaidSinglePurpose}
                      class="input w-full text-xs font-mono"
                    >
                      <option value="disk">disk (Imágenes de VM / VDI)</option>
                      <option value="iso">iso (Biblioteca de imágenes ISO)</option>
                      <option value="backup">backup (Copias de seguridad)</option>
                      <option value="container">container (Contenedores Incus)</option>
                    </select>
                  </div>
                {:else}
                  <div class="grid grid-cols-2 gap-2 text-[11px] pt-1">
                    {#each initDiskSubfolders as sub (sub.id)}
                      <label class="flex items-center gap-1.5 cursor-pointer select-none">
                        <input
                          type="checkbox"
                          checked={createRaidSubfolders.includes(sub.id)}
                          onchange={(e) => {
                            if (e.target.checked) {
                              createRaidSubfolders = [...createRaidSubfolders, sub.id];
                            } else {
                              createRaidSubfolders = createRaidSubfolders.filter(
                                (x) => x !== sub.id
                              );
                            }
                          }}
                          class="w-3.5 h-3.5 rounded border-border"
                        />
                        <span>{sub.label} ({sub.path})</span>
                      </label>
                    {/each}
                  </div>
                {/if}
              </div>
            {/if}
          </div>
        {/if}
      </div>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={() => (open = false)} disabled={creatingRaid}>
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
