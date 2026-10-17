<script>
  import * as Dialog from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import LocalFolderBrowser from '$lib/components/LocalFolderBrowser.svelte';
  import RemoteFolderBrowser from '$lib/components/RemoteFolderBrowser.svelte';
  import { toast } from '$lib/components/ui/toast';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';

  let {
    open = $bindable(false),
    hostPoolsDir = '/opt/webkvm/pools',
    onCreated = () => {},
  } = $props();

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
  let poolLocation = $state('system');

  function resetForm() {
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

  function joinPath(base, sub) {
    return sub ? base.replace(/\/+$/, '') + '/' + sub : base;
  }

  async function createPool() {
    if (poolCreating) return;
    if (!auth.isAdmin()) {
      toast.error(t('storage.poolAdminOnly'));
      return;
    }
    if (!poolName) return;
    const isContainer = poolPurpose === 'container';
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
      const createdPool = { name: createdName, purpose: body.purpose, type: body.type };
      resetForm();
      open = false;
      toast.success(t('storage.poolCreated', { name: createdName }));
      onCreated(createdPool);
    } catch (e) {
      if (e && e.status === 403) {
        toast.error(t('storage.poolAdminOnly'));
        open = false;
        return;
      }
      toast.error(e.message);
    } finally {
      poolCreating = false;
    }
  }
</script>

<Dialog.Root bind:open>
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
              <LocalFolderBrowser onSelect={(p) => (poolPath = p)} />
              <p class="text-[11px] text-muted-foreground mt-1">
                {@html t('storage.absolutePathHint', {
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
      <Button variant="outline" onclick={() => (open = false)}>{t('common.cancel')}</Button>
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
