<script>
  import { onDestroy, onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import CardGridSkeleton from '$lib/components/CardGridSkeleton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { Badge } from '$lib/components/ui/badge';
  import * as Sheet from '$lib/components/ui/sheet';
  import { t } from '$lib/i18n.svelte.js';

  let loading = $state(true);
  let snippets = $state([]);
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
  let searchQuery = $state('');
  let editingSnippet = $state(null);
  let isNew = $state(false);
  let saving = $state(false);

  // Studio Form state
  let formName = $state('');
  let formDescription = $state('');
  let formType = $state('user-data');
  let formContent = $state('');

  // Live Preview Form state
  let previewUser = $state('ubuntu');
  let previewHostname = $state('srv-prod-01');
  let previewIP = $state('192.168.1.150');
  let previewData = $state('');
  let previewLoading = $state(false);
  let previewTimer = null;

  async function load() {
    loading = true;
    try {
      snippets = await api.listCloudInitSnippets();
    } catch (err) {
      toast.error(err.message || 'Error cargando snippets');
    } finally {
      loading = false;
    }
  }

  onMount(load);

  // The debounce timer is cleared on every re-trigger but was never
  // cleared on unmount, so closing the tab within 250 ms of typing still
  // fired a preview request against a component that no longer existed.
  onDestroy(() => {
    if (previewTimer) clearTimeout(previewTimer);
  });

  const filteredSnippets = $derived.by(() => {
    let list = snippets;
    const q = searchQuery.trim().toLowerCase();
    if (q) {
      list = list.filter(
        (s) =>
          s.name?.toLowerCase().includes(q) ||
          s.description?.toLowerCase().includes(q) ||
          s.id?.toLowerCase().includes(q)
      );
    }
    return list;
  });

  function openCreate() {
    isNew = true;
    editingSnippet = {
      id: '',
      name: '',
      description: '',
      type: 'user-data',
      content: `#cloud-config
# Receta personalizada
package_update: true
packages:
  - curl
  - htop
  - git

write_files:
  - path: /etc/motd
    content: |
      Servidor {{ .VM.Hostname }} gestionado con WebKVM.
    permissions: '0644'
`,
    };
    formName = '';
    formDescription = '';
    formType = 'user-data';
    formContent = editingSnippet.content;
    previewData = '';
    triggerLivePreview();
  }

  function openEdit(sn) {
    isNew = false;
    editingSnippet = sn;
    formName = sn.name;
    formDescription = sn.description || '';
    formType = sn.type || 'user-data';
    formContent = sn.content || '';
    previewData = '';
    triggerLivePreview();
  }

  function closeDrawer() {
    editingSnippet = null;
    previewData = '';
  }

  function insertVariable(varTag) {
    formContent += ` {{ ${varTag} }} `;
    triggerLivePreview();
  }

  function triggerLivePreview() {
    if (previewTimer) clearTimeout(previewTimer);
    previewTimer = setTimeout(runPreview, 250);
  }

  async function runPreview() {
    if (!formContent.trim()) {
      previewData = '';
      return;
    }
    previewLoading = true;
    try {
      const res = await api.previewCloudInit({
        custom_user_data: formContent,
        user: previewUser || 'ubuntu',
        hostname: previewHostname || 'srv-prod-01',
        ip: previewIP || '192.168.1.150',
      });
      previewData = res.user_data;
    } catch (err) {
      previewData = '# Error en la plantilla: ' + err.message;
    } finally {
      previewLoading = false;
    }
  }

  async function saveSnippet() {
    if (!formName.trim()) {
      toast.error(t('snippets.nameRequired'));
      return;
    }
    saving = true;
    try {
      const payload = {
        name: formName.trim(),
        description: formDescription.trim(),
        type: formType,
        content: formContent,
      };
      if (isNew) {
        await api.createCloudInitSnippet(payload);
        toast.success(t('snippets.created'));
      } else {
        await api.updateCloudInitSnippet(editingSnippet.id, payload);
        toast.success(t('snippets.updated'));
      }
      closeDrawer();
      await load();
    } catch (err) {
      toast.error(err.message || 'Error guardando snippet');
    } finally {
      saving = false;
    }
  }

  async function deleteSnippet(id) {
    askConfirm({
      title: t('snippets.confirmDeleteTitle'),
      description: t('snippets.confirmDelete'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        try {
          confirmState.loading = true;
          await api.deleteCloudInitSnippet(id);
          confirmState.open = false;
          toast.success(t('snippets.deleted'));
          await load();
        } catch (err) {
          confirmState.loading = false;
          toast.error(err.message || 'Error eliminando snippet');
        }
      },
    });
  }

  function copyContent(text) {
    navigator.clipboard.writeText(text);
    toast.success('Copiado al portapapeles');
  }
</script>

<div class="space-y-6">
  <!-- Top Bar & Search -->
  <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
    <div>
      <h2 class="text-base font-semibold text-foreground flex items-center gap-2">
        <Icon name="code" size={18} class="text-accent" />
        <span>{t('snippets.title')}</span>
      </h2>
      <p class="text-xs text-muted-foreground mt-0.5">
        {t('snippets.description')}
      </p>
    </div>

    <div class="flex items-center gap-2">
      <!-- Search -->
      <div class="relative w-48 sm:w-64">
        <Icon
          name="search"
          size={14}
          class="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground pointer-events-none"
        />
        <input
          type="text"
          placeholder="Buscar snippet..."
          bind:value={searchQuery}
          class="w-full pl-8 pr-3 py-1.5 rounded-xl bg-background border border-border text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40"
        />
      </div>

      <Button onclick={openCreate} size="sm" class="gap-1.5 font-semibold shadow-sm">
        <Icon name="plus" size={14} />
        <span>{t('snippets.newSnippet')}</span>
      </Button>
    </div>
  </div>

  {#if loading}
    <CardGridSkeleton
      count={6}
      cols="grid-cols-1 md:grid-cols-2 lg:grid-cols-3"
      thumbnail={false}
    />
  {:else if filteredSnippets.length === 0}
    <div class="border border-dashed border-border rounded-2xl bg-card/40">
      <EmptyState
        icon="code"
        title="No se encontraron snippets"
        description="Crea una nueva plantilla de aprovisionamiento o ajusta la búsqueda."
      >
        {#snippet action()}
          <Button size="sm" onclick={openCreate}>Crear primer Snippet</Button>
        {/snippet}
      </EmptyState>
    </div>
  {:else}
    <!-- Snippets Modern Grid -->
    <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
      {#each filteredSnippets as sn (sn.id)}
        <div
          class="group relative flex flex-col justify-between rounded-2xl border border-border bg-card p-4 hover:border-accent/40 hover:shadow-md transition-all"
        >
          <div>
            <div class="flex items-start justify-between gap-2 mb-2">
              <div class="flex items-center gap-2 flex-wrap min-w-0">
                <span class="font-bold text-sm text-foreground truncate" title={sn.name}
                  >{sn.name}</span
                >
                {#if sn.is_preset}
                  <span
                    class="px-1.5 py-0.5 rounded text-[10px] font-mono font-bold bg-accent/15 text-accent border border-accent/30"
                  >
                    PRESET
                  </span>
                {:else}
                  <span
                    class="px-1.5 py-0.5 rounded text-[10px] font-mono font-bold bg-muted text-muted-foreground"
                  >
                    CUSTOM
                  </span>
                {/if}
              </div>

              <!-- Actions -->
              <div class="flex items-center gap-1 shrink-0">
                <button
                  onclick={() => copyContent(sn.content)}
                  class="p-1 rounded-lg text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
                  title="Copiar código"
                >
                  <Icon name="copy" size={13} />
                </button>
                <button
                  onclick={() => openEdit(sn)}
                  class="p-1 rounded-lg text-muted-foreground hover:text-accent hover:bg-muted transition-colors"
                  title={sn.is_preset ? 'Ver receta' : 'Editar receta'}
                >
                  <Icon name={sn.is_preset ? 'eye' : 'pencil'} size={13} />
                </button>
                {#if !sn.is_preset}
                  <button
                    onclick={() => deleteSnippet(sn.id)}
                    class="p-1 rounded-lg text-muted-foreground hover:text-destructive hover:bg-destructive/10 transition-colors"
                    aria-label="Eliminar snippet"
                    title="Eliminar snippet"
                  >
                    <Icon name="trash" size={13} />
                  </button>
                {/if}
              </div>
            </div>

            <p class="text-xs text-muted-foreground line-clamp-2 mb-3">
              {sn.description || 'Plantilla de aprovisionamiento lista para producción.'}
            </p>
          </div>

          <div
            class="pt-2 border-t border-border/50 flex items-center justify-between text-[11px] font-mono text-muted-foreground"
          >
            <span class="truncate max-w-[150px]">id: {sn.id}</span>
            <span class="text-[10px] px-1.5 py-0.2 rounded bg-muted/60"
              >{sn.type || 'user-data'}</span
            >
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

<!-- Studio IDE Drawer / Modal (Split-View Editor + Live Inspector) -->
<Sheet.Root
  open={!!editingSnippet}
  onOpenChange={(v) => {
    if (!v) closeDrawer();
  }}
>
  <Sheet.Content
    side="right"
    class="w-full sm:max-w-5xl p-0 overflow-hidden"
    showCloseButton={false}
  >
    <div class="h-full flex flex-col">
      <!-- Top Studio Bar -->
      <div
        class="px-6 py-4 border-b border-border flex items-center justify-between bg-card/80 shrink-0"
      >
        <div class="flex items-center gap-2.5">
          <div class="w-8 h-8 rounded-xl bg-accent/15 flex items-center justify-center text-accent">
            <Icon name="code" size={18} />
          </div>
          <div>
            <h3 class="text-sm font-bold text-foreground">
              {isNew ? 'Cloud-Init Studio (Nuevo Snippet)' : `Editando: ${editingSnippet.name}`}
            </h3>
            <p class="text-[11px] text-muted-foreground">
              Editor interactivo #cloud-config con previsualización en vivo
            </p>
          </div>
        </div>

        <div class="flex items-center gap-2">
          {#if !editingSnippet.is_preset}
            <Button
              size="sm"
              variant="primary"
              onclick={saveSnippet}
              disabled={saving}
              class="font-semibold shadow-sm"
            >
              {#if saving}
                <Spinner size="sm" class="mr-1" />
                <span>Guardando...</span>
              {:else}
                <Icon name="save" size={14} class="mr-1" />
                <span>Guardar Snippet</span>
              {/if}
            </Button>
          {/if}
          <button
            onclick={closeDrawer}
            class="text-muted-foreground hover:text-foreground p-1.5 rounded-lg"
          >
            <Icon name="x" size={18} />
          </button>
        </div>
      </div>

      <!-- Split-View Main Body -->
      <div
        class="flex-1 grid grid-cols-1 lg:grid-cols-2 min-h-0 divide-y lg:divide-y-0 lg:divide-x divide-border"
      >
        <!-- Left Column: Metadata & Code Editor -->
        <div class="p-6 flex flex-col min-h-0 overflow-y-auto space-y-4">
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div>
              <label
                for="snippet-name-input"
                class="text-xs font-semibold text-foreground block mb-1">Nombre del Snippet *</label
              >
              <Input
                id="snippet-name-input"
                bind:value={formName}
                disabled={editingSnippet.is_preset}
                placeholder="Mi Receta"
                class="w-full text-xs"
              />
            </div>
            <div>
              <label
                for="snippet-type-select"
                class="text-xs font-semibold text-foreground block mb-1">Tipo de Documento</label
              >
              <select
                id="snippet-type-select"
                bind:value={formType}
                disabled={editingSnippet.is_preset}
                class="input w-full text-xs"
              >
                <option value="user-data">user-data (#cloud-config)</option>
                <option value="network-config">network-config</option>
                <option value="meta-data">meta-data</option>
              </select>
            </div>
          </div>

          <div>
            <label for="snippet-desc-input" class="text-xs font-semibold text-foreground block mb-1"
              >Descripción</label
            >
            <Input
              id="snippet-desc-input"
              bind:value={formDescription}
              disabled={editingSnippet.is_preset}
              placeholder="Describe qué paquetes o configuraciones aplica..."
              class="w-full text-xs"
            />
          </div>

          <!-- Editor Header & Variable Toolbar -->
          <div class="flex-1 flex flex-col min-h-0 pt-2">
            <div class="flex items-center justify-between mb-1.5">
              <label for="snippet-content-textarea" class="text-xs font-semibold text-foreground"
                >Código #cloud-config (YAML)</label
              >
              <div class="flex items-center gap-1">
                <span class="text-[10px] text-muted-foreground">Variables:</span>
                <button
                  type="button"
                  onclick={() => insertVariable('.VM.Hostname')}
                  class="px-1.5 py-0.5 rounded bg-muted hover:bg-accent/20 hover:text-accent font-mono text-[10px] border border-border"
                >
                  Hostname
                </button>
                <button
                  type="button"
                  onclick={() => insertVariable('.VM.User')}
                  class="px-1.5 py-0.5 rounded bg-muted hover:bg-accent/20 hover:text-accent font-mono text-[10px] border border-border"
                >
                  User
                </button>
                <button
                  type="button"
                  onclick={() => insertVariable('.VM.IP')}
                  class="px-1.5 py-0.5 rounded bg-muted hover:bg-accent/20 hover:text-accent font-mono text-[10px] border border-border"
                >
                  IP
                </button>
              </div>
            </div>

            <textarea
              id="snippet-content-textarea"
              bind:value={formContent}
              oninput={triggerLivePreview}
              disabled={editingSnippet.is_preset}
              rows="16"
              class="w-full flex-1 p-3 font-mono text-xs rounded-xl bg-background border border-border text-foreground focus:outline-none focus:ring-2 focus:ring-accent/40 leading-relaxed resize-none"
              placeholder="#cloud-config..."
              spellcheck="false"
            ></textarea>
          </div>
        </div>

        <!-- Right Column: Live Inspector & Previsualization -->
        <div class="p-6 bg-muted/20 flex flex-col min-h-0 overflow-y-auto space-y-4">
          <div class="flex items-center justify-between pb-2 border-b border-border">
            <div class="flex items-center gap-1.5 text-xs font-semibold text-foreground">
              <Icon name="eye" size={14} class="text-accent" />
              <span>Previsualización Renderizada</span>
            </div>
            {#if previewLoading}
              <div class="flex items-center gap-1 text-[11px] text-muted-foreground">
                <Spinner size="xs" />
                <span>Generando...</span>
              </div>
            {:else}
              <Badge variant="success" class="font-mono text-[10px] font-bold">✓ Renderizado</Badge>
            {/if}
          </div>

          <!-- Sample Parameters for Preview -->
          <div
            class="grid grid-cols-1 sm:grid-cols-3 gap-2 bg-background p-2.5 rounded-xl border border-border text-xs"
          >
            <div>
              <span class="text-[10px] text-muted-foreground block">User Test</span>
              <input
                bind:value={previewUser}
                oninput={triggerLivePreview}
                class="w-full bg-transparent font-mono text-[11px] text-foreground focus:outline-none"
              />
            </div>
            <div>
              <span class="text-[10px] text-muted-foreground block">Hostname Test</span>
              <input
                bind:value={previewHostname}
                oninput={triggerLivePreview}
                class="w-full bg-transparent font-mono text-[11px] text-foreground focus:outline-none"
              />
            </div>
            <div>
              <span class="text-[10px] text-muted-foreground block">IP Test</span>
              <input
                bind:value={previewIP}
                oninput={triggerLivePreview}
                class="w-full bg-transparent font-mono text-[11px] text-foreground focus:outline-none"
              />
            </div>
          </div>

          <!-- Live Output Window -->
          <div class="flex-1 flex flex-col min-h-0">
            <div class="flex items-center justify-between text-[11px] text-muted-foreground mb-1">
              <span>Resultado final inyectado:</span>
              <button
                onclick={() => copyContent(previewData)}
                class="hover:text-accent flex items-center gap-1"
              >
                <Icon name="copy" size={11} />
                <span>Copiar</span>
              </button>
            </div>
            <pre
              class="flex-1 p-3 rounded-xl bg-background border border-border text-foreground font-mono text-[11px] leading-relaxed overflow-auto select-text whitespace-pre-wrap">{previewData ||
                '# Introduce contenido para previsualizar...'}</pre>
          </div>
        </div>
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
