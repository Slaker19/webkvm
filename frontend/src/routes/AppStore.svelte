<script>
  import { onMount } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { upsertTask } from '$lib/stores/tasks.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import * as Dialog from '$lib/components/ui/dialog';
  import * as Sheet from '$lib/components/ui/sheet';
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import CardGridSkeleton from '$lib/components/CardGridSkeleton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import HelperScriptsPanel from '$lib/components/HelperScriptsPanel.svelte';
  import { t } from '$lib/i18n.svelte.js';
  import { navigate } from '$lib/router.svelte.js';
  import { networkLabel } from '$lib/utils/networkLabel.js';
  import { deployablePools } from '$lib/purpose.js';

  let loading = $state(true);
  let appliances = $state([]);
  let networks = $state([]);
  let pools = $state([]);
  let incusProfiles = $state([]);
  let hostHasGPU = $state(false);
  let gpuRenderNodes = $state([]);
  let myAllowedPools = $state([]);
  let confirmState = $state({
    open: false,
    title: '',
    description: '',
    confirmLabel: t('common.delete'),
    variant: 'destructive',
    onConfirm: () => {},
    loading: false,
  });

  function askConfirm(opts) {
    confirmState = { ...opts, open: true, loading: false };
  }
  // Which catalog is on screen: WebKVM's curated appliances, or the
  // imported community-scripts list.
  let sourceView = $state('curated'); // 'curated' | 'community'
  let activeTab = $state('all'); // 'all', 'networking', 'devops', 'monitoring', 'security', 'productivity', 'automation', 'media'
  let typeFilter = $state('all'); // 'all', 'container', 'vm'
  let searchQuery = $state('');

  // Deploy Modal State
  let deployModalApp = $state(null);
  let deployName = $state('');
  let deployNetwork = $state('');
  let deployType = $state('container'); // 'container' | 'vm'
  let deployVcpus = $state(2);
  let deployRamMB = $state(2048);
  let deployDiskGB = $state(10);
  let deployUser = $state('ubuntu');
  let deployPassword = $state('');
  let deploySSHKey = $state('');
  let deployPool = $state('');
  let deployGPU = $state(false);
  let deployProfiles = $state([]);
  let deployNesting = $state(true);
  let deployPrivileged = $state(false);
  let deployAutostart = $state(true);
  let showAdvanced = $state(false);
  let showDeployPassword = $state(false);
  let deploying = $state(false);
  let deployProgress = $state(0);
  let deploySuccess = $state(null);
  let deployJobId = $state('');

  function generateNewPassword() {
    deployPassword = Math.random().toString(36).slice(-10) + 'A1!';
  }

  // Script Studio (Admin Drawer) State
  let showScriptStudio = $state(false);
  let editingApp = $state(null);
  let scriptForm = $state({
    id: '',
    name: '',
    description: '',
    category: 'devops',
    default_type: 'container',
    base_image: 'images:ubuntu/26.04',
    port: 8080,
    web_path: '/',
    vcpus: 2,
    ram_mb: 2048,
    disk_gb: 10,
    documentation_url: '',
    provision_script:
      '#!/bin/bash\nset -euo pipefail\nexport DEBIAN_FRONTEND=noninteractive\n\napt-get update -y\n',
  });
  let savingScript = $state(false);

  const CATEGORIES = [
    { id: 'all', labelKey: 'appstore.catAll', icon: 'layers' },
    { id: 'networking', labelKey: 'appstore.catNetworking', icon: 'globe' },
    { id: 'devops', labelKey: 'appstore.catDevops', icon: 'box' },
    { id: 'monitoring', labelKey: 'appstore.catMonitoring', icon: 'activity' },
    { id: 'security', labelKey: 'appstore.catSecurity', icon: 'shield' },
    { id: 'productivity', labelKey: 'appstore.catProductivity', icon: 'fileText' },
    { id: 'automation', labelKey: 'appstore.catAutomation', icon: 'zap' },
    { id: 'media', labelKey: 'appstore.catMedia', icon: 'play' },
  ];

  async function loadData() {
    loading = true;
    try {
      // Everything after the catalog is optional context for the deploy
      // dialog: a host with no capabilities endpoint, no Incus profiles
      // or a restricted pool list should still be able to browse and
      // deploy, just with fewer options offered.
      const [appRes, netRes, poolRes, capRes, profRes, meRes] = await Promise.all([
        api.listAppliances(),
        api.listNetworks().catch(() => []),
        api.listPools().catch(() => []),
        api.getCapabilities().catch(() => null),
        api.listIncusProfiles().catch(() => null),
        api.me().catch(() => null),
      ]);
      appliances = appRes?.appliances || [];
      networks = (netRes || []).filter((n) => n.active !== false);
      pools = poolRes || [];
      hostHasGPU = !!capRes?.has_gpu;
      gpuRenderNodes = capRes?.gpu_render_nodes || [];
      incusProfiles = profRes?.profiles || [];
      myAllowedPools = meRes?.allowed_pools || [];

      if (networks.length > 0 && !deployNetwork) {
        deployNetwork = networks[0].name;
      }
    } catch (err) {
      toast.error(err.message || t('appstore.loadError'));
    } finally {
      loading = false;
    }
  }

  onMount(loadData);

  const filteredAppliances = $derived.by(() => {
    let list = appliances;

    // Filter category
    if (activeTab !== 'all') {
      list = list.filter(
        (a) => a.category === activeTab || (activeTab === 'devops' && a.category === 'app')
      );
    }

    // Filter type (container vs vm)
    if (typeFilter === 'container') {
      list = list.filter(
        (a) => a.default_type === 'container' || a.is_helper_script || a.category === 'app'
      );
    } else if (typeFilter === 'vm') {
      list = list.filter(
        (a) =>
          a.default_type === 'vm' ||
          a.category === 'cloud' ||
          a.category === 'nas' ||
          a.category === 'router'
      );
    }

    // Search query
    const q = searchQuery.trim().toLowerCase();
    if (q) {
      list = list.filter(
        (a) =>
          a.name?.toLowerCase().includes(q) ||
          a.description?.toLowerCase().includes(q) ||
          a.id?.toLowerCase().includes(q) ||
          a.category?.toLowerCase().includes(q)
      );
    }

    return list;
  });

  // Scoping by target type and entitlement is shared with the other
  // deploy entry point (VmList), so it lives in one place: the two used
  // to disagree about which pools were even offerable.
  const deployPools = $derived(deployablePools(pools, deployType, myAllowedPools));

  // GPU sharing is an Incus container device. On a VM the equivalent is
  // exclusive PCI passthrough — a different, admin-only operation this
  // dialog does not perform — so the control is hidden rather than shown
  // as something that would quietly do nothing.
  const canUseGPU = $derived(hostHasGPU && deployType === 'container');

  // A single profile is not a choice. Showing a one-item selector adds a
  // decision the operator cannot get wrong or right.
  const canPickProfiles = $derived(deployType === 'container' && incusProfiles.length > 1);

  const isContainerDeploy = $derived(deployType === 'container');

  // Keep dependent state honest when the target type changes: options
  // that do not apply to the new type must not survive in the payload.
  $effect(() => {
    if (!canUseGPU && deployGPU) deployGPU = false;
    if (!isContainerDeploy && deployProfiles.length) deployProfiles = [];
  });

  // The selected pool must always belong to the current target type.
  // Without this, switching container→VM kept an Incus pool selected and
  // the deploy failed on a pool the UI was still showing as chosen.
  $effect(() => {
    const names = deployPools.map((p) => p.name);
    if (deployPool && !names.includes(deployPool)) {
      deployPool = '';
    }
  });

  function openDeployModal(app) {
    deployModalApp = app;
    // The instance name must match ^[A-Za-z0-9_.-]{1,64}$ server-side.
    // Catalog ids are not held to that charset — an imported script's id
    // is "cs:<slug>" — so the suggestion is sanitised instead of handing
    // the operator a pre-filled name the server will reject.
    const base =
      String(app.id || 'app')
        .replace(/[^A-Za-z0-9_.-]+/g, '-')
        .replace(/^-+|-+$/g, '')
        .slice(0, 48) || 'app';
    deployName = base + '-' + Math.floor(100 + Math.random() * 900);
    deployType = app.default_type || (app.is_helper_script ? 'container' : 'container');
    deployVcpus = app.vcpus || 2;
    deployRamMB = app.ram_mb || 2048;
    deployDiskGB = app.disk_gb || 10;
    deployUser = 'ubuntu';
    deployPassword = Math.random().toString(36).slice(-8) + 'A1!';
    // An imported script declares whether it needs hardware
    // acceleration (Jellyfin, Plex, Frigate…). Pre-ticking the box for
    // those turns a detail the operator would have to know into one the
    // catalog already knows — and it stays a checkbox, so it can be
    // turned off.
    deployGPU = hostHasGPU && deployType === 'container' && !!app.needs_gpu;
    deployProfiles = [];
    deployNesting = true;
    deployPrivileged = false;
    deployAutostart = true;
    deployPool = '';
    showAdvanced = false;
    deployProgress = 0;
    deploySuccess = null;
    deployJobId = '';
    deploying = false;
  }

  function closeDeployModal() {
    if (deploying) return;
    deployModalApp = null;
    deploySuccess = null;
  }

  async function executeDeploy() {
    if (!deployName.trim()) {
      toast.error(t('appstore.nameRequired'));
      return;
    }
    // The backend validates the instance name against
    // ^[A-Za-z0-9_.-]{1,64}$ (api/vms.go). The pre-filled name is built
    // from the catalogue id, which is NOT constrained to that charset,
    // so a catalogue entry with a slash or a space produced a name the
    // server rejects — and the operator only saw a generic error.
    if (!/^[A-Za-z0-9_.-]{1,64}$/.test(deployName.trim())) {
      toast.error(t('appstore.nameCharset'));
      return;
    }
    deploying = true;
    deployProgress = 15;

    try {
      const payload = {
        name: deployName.trim(),
        type: deployType,
        network: deployNetwork || networks[0]?.name || 'default',
        pool: deployPool || undefined,
        vcpus: Number(deployVcpus) || 2,
        ram_mb: Number(deployRamMB) || 2048,
        disk_gb: Number(deployDiskGB) || 10,
        cloud_init: {
          user: deployUser.trim() || 'ubuntu',
          password: deployPassword.trim(),
          ssh_key: deploySSHKey.trim() || undefined,
          hostname: deployName.trim(),
        },
      };
      // Container-only options are sent ONLY for a container. The server
      // ignores them on a VM anyway, but sending them would imply they
      // had an effect and make the request a poor record of what was
      // actually asked for.
      if (isContainerDeploy) {
        payload.nesting = deployNesting;
        payload.privileged = deployPrivileged;
        payload.autostart = deployAutostart;
        if (canUseGPU && deployGPU) payload.gpu = true;
        if (deployProfiles.length) payload.profiles = deployProfiles;
      }

      deployProgress = 45;
      const res = await api.deployAppliance(deployModalApp.id, payload);
      deployProgress = 100;

      // The endpoint answers 202 Accepted with {job_id, status}: the
      // deployment runs in a BACKGROUND goroutine
      // (api.deployApplianceJob), so there is no instance in the
      // response. The previous code read res.id/res.name/res.type,
      // every one of which was undefined — the success card rendered a
      // blank instance and its "Ver Instancia" button linked to
      // /vms/undefined — while claiming a success that had not
      // happened yet.
      //
      // It is also why `deploying` was never cleared on the success
      // path: closeDeployModal() bails out while deploying is true, so
      // the modal could only be dismissed by reloading the page.
      deployJobId = res?.job_id || '';
      if (deployJobId) {
        upsertTask({
          id: 'async:' + deployJobId,
          kind: 'general',
          title: `Desplegando App: ${deployModalApp.name} (${deployName})`,
          pct: 10,
          message: 'Descargando y preparando plantilla...',
          status: 'running',
        });
      }
      deploySuccess = {
        job_id: deployJobId,
        id: deployJobId,
        name: deployName,
        type: deployType,
        port: deployModalApp.port || 80,
        web_path: deployModalApp.web_path || '/',
        app_info: deployModalApp,
        user: deployUser,
        password: deployPassword,
        queued: true,
      };
      deploying = false;
      toast.success(
        `${deployModalApp.name} en cola de despliegue (trabajo ${deployJobId || 'en curso'})`
      );
    } catch (err) {
      toast.error(err.message || t('appstore.deployError'));
      deploying = false;
    }
  }

  // Script Studio (Admin) Functions
  function openScriptStudio(app = null) {
    if (app) {
      editingApp = app;
      scriptForm = {
        id: app.id,
        name: app.name,
        description: app.description || '',
        category: app.category || 'devops',
        default_type: app.default_type || 'container',
        base_image: app.base_image || 'images:ubuntu/26.04',
        port: app.port || 8080,
        web_path: app.web_path || '/',
        vcpus: app.vcpus || 2,
        ram_mb: app.ram_mb || 2048,
        disk_gb: app.disk_gb || 10,
        documentation_url: app.documentation_url || '',
        provision_script: app.provision_script || '',
      };
      // Fetch provision script if not loaded
      api
        .getApplianceProvision(app.id)
        .then((p) => {
          if (p?.script) scriptForm.provision_script = p.script;
        })
        .catch(() => {});
    } else {
      editingApp = null;
      scriptForm = {
        id: 'custom-' + Math.random().toString(36).slice(2, 7),
        name: '',
        description: '',
        category: 'devops',
        default_type: 'container',
        base_image: 'images:ubuntu/26.04',
        port: 8080,
        web_path: '/',
        vcpus: 2,
        ram_mb: 2048,
        disk_gb: 10,
        documentation_url: '',
        provision_script: `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# Actualización del sistema
apt-get update -y
apt-get install -y ca-certificates curl

# Instalación de tu software aquí:
echo "Instalando servicio..."

# Publicación de credenciales para WebKVM
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : Mi Aplicación
 URL        : http://$VMIP:8080/
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
`,
      };
    }
    showScriptStudio = true;
  }

  function insertScriptSnippet(snippetType) {
    let block = '';
    switch (snippetType) {
      case 'docker':
        block = `\n# Instalar Docker CE & Compose Plugin
if ! command -v docker &>/dev/null; then
  apt-get update -y && apt-get install -y ca-certificates curl gnupg
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi\n`;
        break;
      case 'nodejs':
        block = `\n# Instalar Node.js LTS
curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
apt-get install -y nodejs\n`;
        break;
      case 'python':
        block = `\n# Instalar Python3 y venv
apt-get update -y && apt-get install -y python3 python3-pip python3-venv\n`;
        break;
      case 'systemd':
        block = `\n# Crear servicio Systemd
cat > /etc/systemd/system/myapp.service <<'EOF'
[Unit]
Description=My Custom Application
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/myapp
ExecStart=/usr/bin/node /opt/myapp/server.js
Restart=always

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now myapp\n`;
        break;
      case 'webkvm-creds':
        block = `\n# Bloque de Conexión WebKVM
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : ${scriptForm.name || 'Custom App'}
 URL        : http://$VMIP:${scriptForm.port}${scriptForm.web_path}
 Log        : /var/log/webkvm-provision.log
==========================================
EOF\n`;
        break;
    }
    scriptForm.provision_script += block;
  }

  async function saveCustomScript() {
    if (!scriptForm.id.trim() || !scriptForm.name.trim()) {
      toast.error(t('appstore.idAndNameRequired'));
      return;
    }
    savingScript = true;
    try {
      const payload = {
        id: scriptForm.id.trim(),
        name: scriptForm.name.trim(),
        description: scriptForm.description.trim(),
        category: scriptForm.category,
        default_type: scriptForm.default_type,
        base_image: scriptForm.base_image,
        port: Number(scriptForm.port) || 80,
        web_path: scriptForm.web_path.trim() || '/',
        vcpus: Number(scriptForm.vcpus) || 2,
        ram_mb: Number(scriptForm.ram_mb) || 2048,
        disk_gb: Number(scriptForm.disk_gb) || 10,
        documentation_url: scriptForm.documentation_url.trim(),
        is_helper_script: true,
        cloud_init_supported: true,
        provision_script: scriptForm.provision_script,
        format: 'qcow2',
        compression: 'none',
        url: 'https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img',
        base_image_id: 'ubuntu-26.04',
      };

      if (editingApp) {
        await api.updateAppliance(scriptForm.id, payload);
        toast.success(t('appstore.scriptUpdated'));
      } else {
        await api.createAppliance(payload);
        toast.success(t('appstore.scriptCreated'));
      }
      showScriptStudio = false;
      await loadData();
    } catch (err) {
      toast.error(err.message || t('appstore.scriptSaveError'));
    } finally {
      savingScript = false;
    }
  }

  async function deleteApp(app) {
    askConfirm({
      title: t('appstore.deleteAppTitle'),
      description: t('appstore.deleteAppConfirm', { name: app.name }),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        try {
          confirmState.loading = true;
          await api.deleteAppliance(app.id);
          confirmState.open = false;
          toast.success(t('appstore.appDeleted'));
          await loadData();
        } catch (err) {
          confirmState.loading = false;
          toast.error(err.message || t('appstore.appDeleteError'));
        }
      },
    });
  }
</script>

<div class="flex-1 flex flex-col min-h-0 bg-background overflow-y-auto">
  <!-- Top Banner & Header -->
  <div class="p-4 sm:p-6 pb-2 sm:pb-3 border-b border-border bg-card/60 backdrop-blur-md">
    <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
      <div>
        <div class="flex items-center gap-2.5 mb-1">
          <div class="w-8 h-8 rounded-xl bg-accent/15 flex items-center justify-center text-accent">
            <Icon name="package" size={20} />
          </div>
          <h1 class="text-xl font-bold tracking-tight text-foreground">
            {t('appstore.title')}
          </h1>
          <span
            class="text-xs px-2.5 py-0.5 rounded-full bg-success/10 text-success font-medium border border-success/20 flex items-center gap-1"
          >
            <Icon name="box" size={12} /> Incus LXC 1-Clic
          </span>
        </div>
        <p class="text-xs text-muted-foreground max-w-2xl">
          {t('appstore.subtitle')}
        </p>
      </div>

      <!-- Action Buttons -->
      <div class="flex items-center gap-2">
        {#if auth.role === 'admin'}
          <Button size="sm" onclick={() => openScriptStudio(null)} class="font-semibold shadow-sm">
            <Icon name="plus" size={14} class="mr-1.5" />
            <span>{t('appstore.createScript')}</span>
          </Button>
        {/if}
        <Button variant="outline" size="sm" onclick={loadData} title={t('appstore.reloadCatalog')}>
          <Icon name="refresh" size={14} />
        </Button>
      </div>
    </div>

    <!--
      Source switch. The curated catalog and the community scripts are
      kept as separate views rather than merged into one list: they carry
      very different trust levels (WebKVM-verified images vs third-party
      shell run as root), and blending them would hide that distinction
      exactly where the user is choosing what to install.
    -->
    <div class="mt-4 inline-flex rounded-xl bg-muted/60 p-1 border border-border text-xs">
      <button
        onclick={() => (sourceView = 'curated')}
        class="px-3 py-1 rounded-lg font-medium transition-colors {sourceView === 'curated'
          ? 'bg-background text-foreground shadow-sm font-semibold'
          : 'text-muted-foreground hover:text-foreground'}"
      >
        {t('appstore.filterAll')}
      </button>
      <button
        onclick={() => (sourceView = 'community')}
        class="px-3 py-1 rounded-lg font-medium transition-colors {sourceView === 'community'
          ? 'bg-background text-foreground shadow-sm font-semibold'
          : 'text-muted-foreground hover:text-foreground'}"
      >
        {t('helperScripts.tab')}
      </button>
    </div>

    {#if sourceView === 'curated'}
      <!-- Search & Filters Toolbar -->
      <div class="mt-5 flex flex-col lg:flex-row lg:items-center justify-between gap-3">
        <!-- Search Input -->
        <div class="relative flex-1 max-w-md">
          <Icon
            name="search"
            size={15}
            class="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
          />
          <input
            type="text"
            placeholder={t('appstore.searchPlaceholder')}
            bind:value={searchQuery}
            class="w-full pl-9 pr-3 py-1.5 rounded-xl bg-background border border-border text-xs text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-accent/50 focus:border-accent"
          />
          {#if searchQuery}
            <button
              onclick={() => (searchQuery = '')}
              class="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
            >
              <Icon name="x" size={13} />
            </button>
          {/if}
        </div>

        <!-- Type Filter Buttons -->
        <div class="inline-flex rounded-xl bg-muted/60 p-1 border border-border text-xs">
          <button
            onclick={() => (typeFilter = 'all')}
            class="px-2.5 py-1 rounded-lg font-medium transition-colors {typeFilter === 'all'
              ? 'bg-background text-foreground shadow-sm font-semibold'
              : 'text-muted-foreground hover:text-foreground'}"
          >
            {t('appstore.filterAll')}
          </button>
          <button
            onclick={() => (typeFilter = 'container')}
            class="px-2.5 py-1 rounded-lg font-medium transition-colors flex items-center gap-1.5 {typeFilter ===
            'container'
              ? 'bg-background text-foreground shadow-sm font-semibold'
              : 'text-muted-foreground hover:text-foreground'}"
          >
            <Icon name="box" size={12} class="text-success" />
            <span>{t('appstore.filterLXC')}</span>
          </button>
          <button
            onclick={() => (typeFilter = 'vm')}
            class="px-2.5 py-1 rounded-lg font-medium transition-colors {typeFilter === 'vm'
              ? 'bg-background text-foreground shadow-sm font-semibold'
              : 'text-muted-foreground hover:text-foreground'}"
          >
            {t('appstore.filterKVM')}
          </button>
        </div>
      </div>

      <!-- Category Filter Tabs -->
      <div class="mt-4 flex items-center gap-1.5 overflow-x-auto pb-1 scrollbar-none text-xs">
        {#each CATEGORIES as cat (cat.id)}
          <button
            onclick={() => (activeTab = cat.id)}
            class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-xl transition-all shrink-0 font-medium {activeTab ===
            cat.id
              ? 'bg-accent text-accent-foreground font-semibold shadow-sm'
              : 'bg-card text-muted-foreground hover:text-foreground hover:bg-muted border border-border'}"
          >
            <Icon name={cat.icon} size={13} />
            <span>{t(cat.labelKey) || cat.id}</span>
          </button>
        {/each}
      </div>
    {/if}
  </div>

  <!-- Main Content Grid -->
  <div class="p-4 sm:p-6">
    {#if sourceView === 'community'}
      <HelperScriptsPanel onDeploy={openDeployModal} />
    {:else if loading}
      <CardGridSkeleton count={8} cols="grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4" />
    {:else if filteredAppliances.length === 0}
      <div class="border border-dashed border-border rounded-2xl bg-card/40">
        <EmptyState
          icon="package"
          title={t('appstore.noAppsFound')}
          description={t('appstore.noAppsDesc')}
        >
          {#snippet action()}
            <Button
              variant="outline"
              size="sm"
              onclick={() => {
                activeTab = 'all';
                typeFilter = 'all';
                searchQuery = '';
              }}
            >
              {t('appstore.clearFilters')}
            </Button>
          {/snippet}
        </EmptyState>
      </div>
    {:else}
      <!-- Cards Grid -->
      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
        {#each filteredAppliances as app (app.id)}
          <div
            class="group relative flex flex-col rounded-2xl border border-border bg-card hover:border-accent/40 hover:shadow-lg transition-all duration-200 overflow-hidden"
          >
            <!-- Card Header -->
            <div class="p-4 pb-3 flex items-start gap-3">
              <!-- App Icon Badge -->
              <div
                class="w-11 h-11 rounded-xl bg-accent/10 border border-accent/20 flex items-center justify-center text-accent shrink-0 shadow-sm group-hover:scale-105 transition-transform"
              >
                {#if app.id.includes('docker') || app.id.includes('portainer')}
                  <Icon name="box" size={22} />
                {:else if app.id.includes('pihole') || app.id.includes('adguard')}
                  <Icon name="shield" size={22} />
                {:else if app.id.includes('uptime') || app.id.includes('beszel') || app.id.includes('grafana')}
                  <Icon name="activity" size={22} />
                {:else if app.id.includes('wireguard') || app.id.includes('openvpn')}
                  <Icon name="lock" size={22} />
                {:else if app.id.includes('n8n')}
                  <Icon name="zap" size={22} />
                {:else if app.id.includes('jellyfin')}
                  <Icon name="play" size={22} />
                {:else}
                  <Icon name="package" size={22} />
                {/if}
              </div>

              <!-- Title & Category -->
              <div class="flex-1 min-w-0">
                <div class="flex items-center justify-between gap-1">
                  <h3 class="font-bold text-sm text-foreground truncate" title={app.name}>
                    {app.name}
                  </h3>
                  {#if app.default_type === 'container' || app.is_helper_script}
                    <span
                      class="px-1.5 py-0.5 rounded-md bg-success/10 text-success font-mono text-[10px] font-medium border border-success/20 shrink-0 flex items-center gap-1"
                    >
                      <Icon name="box" size={10} /> LXC
                    </span>
                  {:else}
                    <span
                      class="px-1.5 py-0.5 rounded-md bg-accent/10 text-accent font-mono text-[10px] font-medium border border-accent/20 shrink-0 flex items-center gap-1"
                    >
                      <Icon name="cpu" size={10} /> KVM
                    </span>
                  {/if}
                </div>
                <div class="flex items-center gap-1.5 text-[11px] text-muted-foreground mt-0.5">
                  <span class="capitalize">{app.category}</span>
                  {#if app.port}
                    <span>•</span>
                    <span class="font-mono text-accent font-semibold">:{app.port}</span>
                  {/if}
                </div>
              </div>
            </div>

            <!-- Card Description -->
            <div class="px-4 flex-1">
              <p class="text-xs text-muted-foreground line-clamp-2 leading-relaxed">
                {app.description || app.notes || t('appstore.defaultDescription')}
              </p>
            </div>

            <!-- Card Metadata Badges -->
            <div
              class="px-4 py-2.5 mt-2 flex items-center gap-2 border-t border-border/50 text-[11px] font-mono text-muted-foreground bg-muted/20"
            >
              <span class="flex items-center gap-1">
                <Icon name="cpu" size={12} class="text-muted-foreground" />
                {app.vcpus}C
              </span>
              <span>•</span>
              <span>{Math.round((app.ram_mb / 1024) * 10) / 10} GB</span>
              <span>•</span>
              <span>{app.disk_gb} GB</span>
              {#if app.base_image}
                <span class="ml-auto text-[10px] text-muted-foreground/80 truncate max-w-[90px]"
                  >{app.base_image}</span
                >
              {/if}
            </div>

            <!-- Card Actions -->
            <div
              class="p-3 pt-2 bg-card flex items-center justify-between gap-2 border-t border-border"
            >
              {#if auth.role === 'admin'}
                <button
                  onclick={() => openScriptStudio(app)}
                  class="p-1.5 rounded-lg text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
                  title="Editar script / configuración"
                >
                  <Icon name="pencil" size={13} />
                </button>
                {#if !app.builtin}
                  <button
                    onclick={() => deleteApp(app)}
                    class="p-1.5 rounded-lg text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors"
                    aria-label="Eliminar app"
                    title="Eliminar app"
                  >
                    <Icon name="trash" size={13} />
                  </button>
                {/if}
              {/if}

              {#if app.documentation_url}
                <a
                  href={app.documentation_url}
                  target="_blank"
                  rel="noopener noreferrer"
                  class="p-1.5 rounded-lg text-muted-foreground hover:text-accent hover:bg-muted transition-colors inline-flex items-center"
                  title="Documentación oficial"
                >
                  <Icon name="externalLink" size={13} />
                </a>
              {/if}

              <Button
                size="xs"
                variant="primary"
                onclick={() => openDeployModal(app)}
                class="ml-auto font-semibold flex items-center gap-1 shadow-sm"
              >
                <span>Desplegar 1-Clic</span>
                <Icon name="arrowRight" size={12} />
              </Button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </div>
</div>

<!-- Deploy Modal -->
<Dialog.Root
  open={!!deployModalApp}
  onOpenChange={(v) => {
    if (!v) closeDeployModal();
  }}
>
  <Dialog.Content class="sm:max-w-lg max-h-[90vh] flex flex-col">
    {#if deployModalApp}
      <Dialog.Header>
        <div class="flex items-center gap-3">
          <div
            class="w-10 h-10 rounded-xl bg-accent/15 flex items-center justify-center text-accent"
          >
            <Icon name="package" size={20} />
          </div>
          <div>
            <Dialog.Title>Desplegar {deployModalApp.name}</Dialog.Title>
            <Dialog.Description>Configuración rápida en 1-clic</Dialog.Description>
          </div>
        </div>
      </Dialog.Header>

      {#if deploySuccess}
        <!-- Success Screen -->
        <div class="py-6 flex flex-col items-center text-center gap-4">
          <div
            class="w-14 h-14 rounded-full bg-accent/15 border border-accent/30 flex items-center justify-center text-accent shadow-sm"
          >
            <Icon name="clock" size={28} />
          </div>
          <div>
            <h4 class="text-base font-bold text-foreground">
              {deploySuccess.name} está en cola de despliegue
            </h4>
            <p class="text-xs text-muted-foreground mt-1 max-w-sm">
              La instalación se ejecuta en segundo plano. La instancia aparecerá en tu lista cuando
              termine; puedes seguirla en Detalles → Tareas.
            </p>
          </div>

          <!-- Connection Card -->
          <div
            class="w-full rounded-xl bg-muted/40 border border-border p-4 text-left font-mono text-xs space-y-2"
          >
            <div class="flex items-center justify-between">
              <span class="text-muted-foreground">Instancia:</span>
              <span class="text-foreground font-bold">{deploySuccess.name}</span>
            </div>
            <div class="flex items-center justify-between">
              <span class="text-muted-foreground">Trabajo:</span>
              <span class="text-foreground font-bold">{deploySuccess.job_id || '—'}</span>
            </div>
            <div class="flex items-center justify-between gap-2 min-w-0">
              <span class="text-muted-foreground shrink-0">Puerto Servicio:</span>
              <span class="text-accent font-bold truncate"
                >:{deploySuccess.port}{deploySuccess.web_path}</span
              >
            </div>
            {#if deploySuccess.user}
              <div class="flex items-center justify-between gap-2 min-w-0">
                <span class="text-muted-foreground shrink-0">Usuario Inicial:</span>
                <span class="text-foreground font-bold truncate">{deploySuccess.user}</span>
              </div>
            {/if}
            {#if deploySuccess.password}
              <div class="flex items-center justify-between gap-2 min-w-0">
                <span class="text-muted-foreground shrink-0">Password Inicial:</span>
                <span class="text-warning font-bold break-all">{deploySuccess.password}</span>
              </div>
            {/if}
          </div>

          <div class="flex items-center gap-3 w-full mt-2">
            <!-- No "Ver Instancia" link: the deployment is still queued,
                 so the instance does not exist yet and its id is not
                 known. Navigating to /vms/<job_id> just opened a
                 not-found page. -->
            <Button
              variant="primary"
              class="w-full font-semibold"
              onclick={() => {
                closeDeployModal();
                navigate('/vms');
              }}
            >
              <Icon name="check" size={14} class="mr-1.5" />
              <span>Finalizar</span>
            </Button>
          </div>
        </div>
      {:else}
        <!-- Form Content -->
        <div class="py-4 space-y-4 overflow-y-auto flex-1 min-w-0">
          <!-- Target Environment (LXC vs VM) -->
          <div>
            <span class="text-xs font-semibold text-foreground block mb-1.5"
              >Entorno de Ejecución</span
            >
            <div class="grid grid-cols-2 gap-2 text-xs">
              <button
                type="button"
                onclick={() => (deployType = 'container')}
                class="flex flex-col items-start p-3 rounded-xl border text-left transition-all {deployType ===
                'container'
                  ? 'border-success bg-success/10 text-foreground font-semibold shadow-sm'
                  : 'border-border bg-card text-muted-foreground hover:bg-muted'}"
              >
                <div class="flex items-center gap-1.5 text-success font-semibold mb-0.5">
                  <Icon name="box" size={14} />
                  <span>Incus LXC</span>
                  <span class="text-[10px] px-1 py-0.2 rounded bg-success/20">Recomendado</span>
                </div>
                <span class="text-[11px] text-muted-foreground font-normal"
                  >Arranque en ~3s, bajo consumo de RAM.</span
                >
              </button>
              <button
                type="button"
                onclick={() => (deployType = 'vm')}
                class="flex flex-col items-start p-3 rounded-xl border text-left transition-all {deployType ===
                'vm'
                  ? 'border-accent bg-accent/10 text-foreground font-semibold shadow-sm'
                  : 'border-border bg-card text-muted-foreground hover:bg-muted'}"
              >
                <div class="flex items-center gap-1.5 text-accent font-semibold mb-0.5">
                  <Icon name="cpu" size={14} />
                  <span>KVM Virtual Machine</span>
                </div>
                <span class="text-[11px] text-muted-foreground font-normal"
                  >Aislamiento de kernel completo.</span
                >
              </button>
            </div>
          </div>

          <!-- Name & Network -->
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label
                for="deploy-instance-name"
                class="text-xs font-semibold text-foreground block mb-1"
                >Nombre de la Instancia *</label
              >
              <Input
                id="deploy-instance-name"
                bind:value={deployName}
                placeholder="mi-app"
                class="w-full"
              />
            </div>
            <div>
              <label
                for="deploy-network-select"
                class="text-xs font-semibold text-foreground block mb-1">Red / Bridge *</label
              >
              <select
                id="deploy-network-select"
                bind:value={deployNetwork}
                class="input w-full text-xs"
              >
                {#if networks.length === 0}
                  <option value="default">default (automático)</option>
                {:else}
                  {#each networks as n (n.name)}
                    <option value={n.name}>{networkLabel(n)}</option>
                  {/each}
                {/if}
              </select>
            </div>
          </div>

          <!-- Storage pool. The list is already scoped to the target
               type and the caller's entitlements, so an empty list means
               "nothing eligible here", which is worth saying plainly. -->
          <div>
            <label
              for="deploy-pool-select"
              class="text-xs font-semibold text-foreground block mb-1"
            >
              {t('appstore.poolLabel')}
            </label>
            <select id="deploy-pool-select" bind:value={deployPool} class="input w-full text-xs">
              <option value="">{t('appstore.poolAuto')}</option>
              {#each deployPools as p (p.name)}
                <option value={p.name}>{p.name}{p.type ? ` · ${p.type}` : ''}</option>
              {/each}
            </select>
            <p class="text-[11px] text-muted-foreground mt-1">
              {#if deployPools.length === 0}
                {t('appstore.poolNoneEligible')}
              {:else if isContainerDeploy}
                {t('appstore.poolHelperContainer')}
              {:else}
                {t('appstore.poolHelperVM')}
              {/if}
            </p>
          </div>

          {#if canUseGPU}
            <!-- Shown only when the host really has a render node and the
                 target is a container. A VM would need exclusive PCI
                 passthrough, which is a separate admin operation. -->
            <label
              class="flex items-start gap-2.5 rounded-xl border border-border bg-muted/20 p-3 cursor-pointer hover:bg-muted/30 transition-colors"
            >
              <input type="checkbox" bind:checked={deployGPU} class="mt-0.5 accent-accent" />
              <span class="min-w-0">
                <span class="text-xs font-semibold text-foreground flex items-center gap-1.5">
                  <Icon name="cpu" size={13} class="text-accent" />
                  <span>{t('appstore.gpuLabel')}</span>
                  {#if deployModalApp.needs_gpu}
                    <span class="text-[10px] px-1.5 py-0.5 rounded bg-accent/20 text-accent"
                      >{t('appstore.gpuRecommended')}</span
                    >
                  {/if}
                </span>
                <span class="text-[11px] text-muted-foreground block mt-0.5">
                  {t('appstore.gpuHelper', { device: gpuRenderNodes[0] || '/dev/dri' })}
                </span>
              </span>
            </label>
          {/if}

          <!-- Credentials (User, Root/Sudo Password, SSH Key) -->
          <div class="rounded-xl border border-border bg-muted/20 p-3.5 space-y-3">
            <div class="flex items-center justify-between">
              <span class="text-xs font-semibold text-foreground flex items-center gap-1.5">
                <Icon name="lock" size={13} class="text-accent" />
                <span>Credenciales de Acceso (Usuario & Root)</span>
              </span>
              <button
                type="button"
                onclick={generateNewPassword}
                class="text-[11px] text-accent hover:underline flex items-center gap-1"
                title="Generar nueva contraseña segura"
              >
                <Icon name="refresh" size={11} />
                <span>Generar nueva</span>
              </button>
            </div>

            <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <label for="deploy-user-input" class="text-[11px] text-muted-foreground block mb-1">
                  Usuario del Sistema
                </label>
                <Input
                  id="deploy-user-input"
                  bind:value={deployUser}
                  placeholder="ubuntu"
                  class="w-full text-xs font-mono"
                />
              </div>
              <div>
                <label for="deploy-pass-input" class="text-[11px] text-muted-foreground block mb-1">
                  Contraseña (Root / Sudo) *
                </label>
                <div class="relative">
                  <input
                    id="deploy-pass-input"
                    type={showDeployPassword ? 'text' : 'password'}
                    bind:value={deployPassword}
                    class="w-full pl-2.5 pr-8 py-1.5 rounded-xl bg-background border border-border text-xs font-mono text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
                    placeholder="Mínimo 6 caracteres"
                  />
                  <button
                    type="button"
                    onclick={() => (showDeployPassword = !showDeployPassword)}
                    class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                    title={showDeployPassword ? 'Ocultar' : 'Mostrar'}
                  >
                    <Icon name={showDeployPassword ? 'eyeOff' : 'eye'} size={14} />
                  </button>
                </div>
              </div>
            </div>

            <details class="text-xs pt-1">
              <summary
                class="cursor-pointer text-muted-foreground hover:text-foreground select-none font-medium text-[11px]"
              >
                + Clave SSH Pública (Opcional)
              </summary>
              <div class="mt-2">
                <textarea
                  id="deploy-ssh-key-input"
                  bind:value={deploySSHKey}
                  rows="2"
                  placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5..."
                  class="w-full p-2 rounded-xl bg-background border border-border text-xs font-mono text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
                ></textarea>
              </div>
            </details>
          </div>

          <!-- Resources -->
          <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
            <div>
              <label for="deploy-vcpus" class="text-xs text-muted-foreground block mb-1"
                >vCPU Cores</label
              >
              <Input
                id="deploy-vcpus"
                type="number"
                min="1"
                max="16"
                bind:value={deployVcpus}
                class="w-full"
              />
            </div>
            <div>
              <label for="deploy-ram" class="text-xs text-muted-foreground block mb-1"
                >RAM (MB)</label
              >
              <Input
                id="deploy-ram"
                type="number"
                min="256"
                step="256"
                bind:value={deployRamMB}
                class="w-full"
              />
            </div>
            <div>
              <label for="deploy-disk" class="text-xs text-muted-foreground block mb-1"
                >Disco (GB)</label
              >
              <Input
                id="deploy-disk"
                type="number"
                min="2"
                max="500"
                bind:value={deployDiskGB}
                class="w-full"
              />
            </div>
          </div>

          {#if isContainerDeploy}
            <!-- Container-only switches. Collapsed by default: the
                 defaults are right for almost every appliance, and an
                 always-open block of toggles invites changes nobody
                 needed to make. -->
            <div class="rounded-xl border border-border bg-muted/20">
              <button
                type="button"
                onclick={() => (showAdvanced = !showAdvanced)}
                class="w-full flex items-center justify-between p-3 text-xs font-semibold text-foreground hover:bg-muted/30 rounded-xl transition-colors"
                aria-expanded={showAdvanced}
              >
                <span class="flex items-center gap-1.5">
                  <Icon name="settings" size={13} class="text-muted-foreground" />
                  <span>{t('appstore.advancedTitle')}</span>
                </span>
                <Icon name={showAdvanced ? 'chevronUp' : 'chevronDown'} size={14} />
              </button>

              {#if showAdvanced}
                <div class="px-3 pb-3 space-y-2.5">
                  <label class="flex items-start gap-2.5 cursor-pointer">
                    <input
                      type="checkbox"
                      bind:checked={deployNesting}
                      class="mt-0.5 accent-accent"
                    />
                    <span class="min-w-0">
                      <span class="text-xs font-medium text-foreground"
                        >{t('appstore.nestingLabel')}</span
                      >
                      <span class="text-[11px] text-muted-foreground block"
                        >{t('appstore.nestingHelper')}</span
                      >
                    </span>
                  </label>

                  <label class="flex items-start gap-2.5 cursor-pointer">
                    <input
                      type="checkbox"
                      bind:checked={deployAutostart}
                      class="mt-0.5 accent-accent"
                    />
                    <span class="min-w-0">
                      <span class="text-xs font-medium text-foreground"
                        >{t('appstore.autostartLabel')}</span
                      >
                      <span class="text-[11px] text-muted-foreground block"
                        >{t('appstore.autostartHelper')}</span
                      >
                    </span>
                  </label>

                  <label class="flex items-start gap-2.5 cursor-pointer">
                    <input
                      type="checkbox"
                      bind:checked={deployPrivileged}
                      class="mt-0.5 accent-warning"
                    />
                    <span class="min-w-0">
                      <span class="text-xs font-medium text-foreground flex items-center gap-1.5">
                        <span>{t('appstore.privilegedLabel')}</span>
                        {#if deployPrivileged}
                          <Icon name="alertTriangle" size={12} class="text-warning" />
                        {/if}
                      </span>
                      <span class="text-[11px] text-muted-foreground block"
                        >{t('appstore.privilegedHelper')}</span
                      >
                    </span>
                  </label>

                  {#if canPickProfiles}
                    <div class="pt-1">
                      <span class="text-xs font-medium text-foreground block mb-1"
                        >{t('appstore.profilesLabel')}</span
                      >
                      <div class="flex flex-wrap gap-1.5">
                        {#each incusProfiles as prof (prof.name || prof)}
                          {@const pname = prof.name || prof}
                          <button
                            type="button"
                            onclick={() =>
                              (deployProfiles = deployProfiles.includes(pname)
                                ? deployProfiles.filter((x) => x !== pname)
                                : [...deployProfiles, pname])}
                            class="text-[11px] px-2 py-1 rounded-lg border transition-colors {deployProfiles.includes(
                              pname
                            )
                              ? 'border-accent bg-accent/15 text-accent font-semibold'
                              : 'border-border bg-card text-muted-foreground hover:bg-muted'}"
                          >
                            {pname}
                          </button>
                        {/each}
                      </div>
                      <p class="text-[11px] text-muted-foreground mt-1">
                        {t('appstore.profilesHelper')}
                      </p>
                    </div>
                  {/if}
                </div>
              {/if}
            </div>
          {/if}

          {#if deploying}
            <div class="pt-2 space-y-2">
              <div class="flex items-center justify-between text-xs font-semibold text-accent">
                <span>Desplegando contenedor y ejecutando aprovisionamiento...</span>
                <span>{deployProgress}%</span>
              </div>
              <div class="w-full h-2 rounded-full bg-muted overflow-hidden">
                <div
                  class="h-full bg-accent transition-all duration-300"
                  style="width: {deployProgress}%"
                ></div>
              </div>
            </div>
          {/if}
        </div>

        <!-- Footer Buttons -->
        <Dialog.Footer>
          <Button variant="outline" size="sm" onclick={closeDeployModal} disabled={deploying}>
            Cancelar
          </Button>
          <Button
            variant="primary"
            size="sm"
            onclick={executeDeploy}
            disabled={deploying}
            class="font-semibold"
          >
            {#if deploying}
              <Spinner size="sm" class="mr-1.5" />
              <span>Desplegando...</span>
            {:else}
              <Icon name="zap" size={14} class="mr-1.5 text-warning" />
              <span>Lanzar en 1-Clic</span>
            {/if}
          </Button>
        </Dialog.Footer>
      {/if}
    {/if}
  </Dialog.Content>
</Dialog.Root>

<!-- Script Studio (Admin Slide-over Drawer) -->
<Sheet.Root bind:open={showScriptStudio}>
  <Sheet.Content side="right" class="w-full sm:max-w-2xl overflow-y-auto" showCloseButton={false}>
    <div class="flex flex-col h-full">
      <!-- Drawer Header -->
      <div class="flex items-center justify-between pb-4 border-b border-border">
        <div class="flex items-center gap-2.5">
          <div class="w-9 h-9 rounded-xl bg-accent/15 flex items-center justify-center text-accent">
            <Icon name="code" size={18} />
          </div>
          <div>
            <h3 class="text-base font-bold text-foreground">
              {editingApp
                ? `Editar Helper Script: ${editingApp.name}`
                : 'Script Studio (Crear Helper Script Propio)'}
            </h3>
            <p class="text-xs text-muted-foreground">Exclusivo para administradores de WebKVM</p>
          </div>
        </div>
        <button
          onclick={() => (showScriptStudio = false)}
          class="text-muted-foreground hover:text-foreground p-1 rounded-lg"
        >
          <Icon name="x" size={18} />
        </button>
      </div>

      <!-- Drawer Body Form -->
      <div class="py-5 space-y-4 flex-1">
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label for="script-id-input" class="text-xs font-semibold text-foreground block mb-1"
              >ID Único *</label
            >
            <Input
              id="script-id-input"
              bind:value={scriptForm.id}
              disabled={!!editingApp}
              placeholder="mi-app"
              class="w-full"
            />
          </div>
          <div>
            <label for="script-name-input" class="text-xs font-semibold text-foreground block mb-1"
              >Nombre de la Aplicación *</label
            >
            <Input
              id="script-name-input"
              bind:value={scriptForm.name}
              placeholder="Mi Aplicación"
              class="w-full"
            />
          </div>
        </div>

        <div>
          <label for="script-desc-input" class="text-xs font-semibold text-foreground block mb-1"
            >Descripción</label
          >
          <Input
            id="script-desc-input"
            bind:value={scriptForm.description}
            placeholder="Descripción de la app..."
            class="w-full"
          />
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
          <div>
            <label for="script-cat-select" class="text-xs font-semibold text-foreground block mb-1"
              >Categoría</label
            >
            <select
              id="script-cat-select"
              bind:value={scriptForm.category}
              class="input w-full text-xs"
            >
              <option value="devops">DevOps & Docker</option>
              <option value="networking">Redes & DNS</option>
              <option value="monitoring">Monitorización</option>
              <option value="security">Seguridad & VPN</option>
              <option value="productivity">Productividad</option>
              <option value="automation">Automatización</option>
              <option value="media">Media & Home</option>
            </select>
          </div>
          <div>
            <label for="script-type-select" class="text-xs font-semibold text-foreground block mb-1"
              >Tipo Predeterminado</label
            >
            <select
              id="script-type-select"
              bind:value={scriptForm.default_type}
              class="input w-full text-xs"
            >
              <option value="container">Contenedor LXC (Incus)</option>
              <option value="vm">Máquina KVM</option>
            </select>
          </div>
          <div>
            <label for="script-base-select" class="text-xs font-semibold text-foreground block mb-1"
              >Imagen Base</label
            >
            <select
              id="script-base-select"
              bind:value={scriptForm.base_image}
              class="input w-full text-xs font-mono"
            >
              <option value="images:ubuntu/26.04">images:ubuntu/26.04</option>
              <option value="images:ubuntu/24.04">images:ubuntu/24.04</option>
              <option value="images:debian/12">images:debian/12</option>
              <option value="images:alpine/3.20">images:alpine/3.20</option>
            </select>
          </div>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <div>
            <label for="script-port-input" class="text-xs font-semibold text-foreground block mb-1"
              >Puerto de Acceso</label
            >
            <Input
              id="script-port-input"
              type="number"
              bind:value={scriptForm.port}
              placeholder="8080"
              class="w-full"
            />
          </div>
          <div>
            <label
              for="script-webpath-input"
              class="text-xs font-semibold text-foreground block mb-1">Ruta Web</label
            >
            <Input
              id="script-webpath-input"
              bind:value={scriptForm.web_path}
              placeholder="/"
              class="w-full"
            />
          </div>
        </div>

        <!-- Script Editor Header & Quick Snippets -->
        <div class="pt-2">
          <div class="flex items-center justify-between mb-1.5">
            <label for="script-textarea" class="text-xs font-semibold text-foreground"
              >Script de Aprovisionamiento Bash (Root)</label
            >
            <span class="text-[10px] text-muted-foreground font-mono"
              >Ejecutado al primer arranque</span
            >
          </div>

          <!-- Snippet Insertion Pills -->
          <div class="flex flex-wrap items-center gap-1.5 mb-2">
            <span class="text-[11px] text-muted-foreground">Insertar:</span>
            <button
              type="button"
              onclick={() => insertScriptSnippet('docker')}
              class="px-2 py-0.5 rounded-lg bg-muted hover:bg-accent/20 hover:text-accent text-[11px] font-mono border border-border"
            >
              + Docker CE
            </button>
            <button
              type="button"
              onclick={() => insertScriptSnippet('nodejs')}
              class="px-2 py-0.5 rounded-lg bg-muted hover:bg-accent/20 hover:text-accent text-[11px] font-mono border border-border"
            >
              + Node.js LTS
            </button>
            <button
              type="button"
              onclick={() => insertScriptSnippet('python')}
              class="px-2 py-0.5 rounded-lg bg-muted hover:bg-accent/20 hover:text-accent text-[11px] font-mono border border-border"
            >
              + Python venv
            </button>
            <button
              type="button"
              onclick={() => insertScriptSnippet('systemd')}
              class="px-2 py-0.5 rounded-lg bg-muted hover:bg-accent/20 hover:text-accent text-[11px] font-mono border border-border"
            >
              + Systemd Service
            </button>
            <button
              type="button"
              onclick={() => insertScriptSnippet('webkvm-creds')}
              class="px-2 py-0.5 rounded-lg bg-muted hover:bg-accent/20 hover:text-accent text-[11px] font-mono border border-border"
            >
              + Info WebKVM
            </button>
          </div>

          <textarea
            id="script-textarea"
            bind:value={scriptForm.provision_script}
            rows="12"
            class="w-full p-3 font-mono text-xs rounded-xl bg-background border border-border text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40 leading-relaxed resize-y"
            spellcheck="false"
          ></textarea>
        </div>
      </div>

      <!-- Drawer Footer -->
      <div class="pt-4 border-t border-border flex items-center justify-end gap-2">
        <Button variant="outline" size="sm" onclick={() => (showScriptStudio = false)}>
          Cancelar
        </Button>
        <Button
          variant="primary"
          size="sm"
          onclick={saveCustomScript}
          disabled={savingScript}
          class="font-semibold"
        >
          {#if savingScript}
            <Spinner size="sm" class="mr-1.5" />
            <span>Guardando...</span>
          {:else}
            <Icon name="save" size={14} class="mr-1.5" />
            <span>Guardar en Catálogo</span>
          {/if}
        </Button>
      </div>
    </div>
  </Sheet.Content>
</Sheet.Root>

<ConfirmDialog
  bind:open={confirmState.open}
  title={confirmState.title}
  description={confirmState.description}
  confirmLabel={confirmState.confirmLabel}
  variant={confirmState.variant}
  loading={confirmState.loading}
  onConfirm={confirmState.onConfirm}
/>
