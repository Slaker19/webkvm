<script>
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import { toast } from '$lib/components/ui/toast';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import SnippetModal from './SnippetModal.svelte';

  let {
    cloudInitSnippets = [],
    onRefresh = () => {},
    askConfirm = () => {},
    showStorageErr = () => {},
  } = $props();

  let snippetSearch = $state('');
  let snippetCategoryFilter = $state('all');
  let showSnippetModal = $state(false);
  let editingSnippet = $state(null);
  let snippetInitialForm = $state(null);

  const filteredSnippets = $derived.by(() => {
    let list = cloudInitSnippets || [];
    if (snippetCategoryFilter === 'custom') {
      list = list.filter((s) => !s.is_preset);
    } else if (snippetCategoryFilter !== 'all') {
      list = list.filter((s) => s.category === snippetCategoryFilter);
    }
    if (!snippetSearch.trim()) return list;
    const q = snippetSearch.toLowerCase();
    return list.filter(
      (s) =>
        (s.name && s.name.toLowerCase().includes(q)) ||
        (s.description && s.description.toLowerCase().includes(q)) ||
        (s.category && s.category.toLowerCase().includes(q))
    );
  });

  function openNewSnippet() {
    editingSnippet = null;
    snippetInitialForm = {
      name: '',
      description: '',
      category: 'custom',
      type: 'user-data',
      content:
        '#cloud-config\n# Tu configuración personalizada aquí\npackage_update: true\npackages:\n  - curl\n',
    };
    showSnippetModal = true;
  }

  function openEditSnippet(sn) {
    editingSnippet = sn;
    snippetInitialForm = {
      name: sn.name,
      description: sn.description || '',
      category: sn.category || (sn.is_preset ? 'devops' : 'custom'),
      type: sn.type || 'user-data',
      content: sn.content || '',
    };
    showSnippetModal = true;
  }

  function clonePresetAsCustom(sn) {
    editingSnippet = null;
    snippetInitialForm = {
      name: sn.name + ' (Personalizado)',
      description: sn.description || '',
      category: sn.category || 'custom',
      type: sn.type || 'user-data',
      content: sn.content || '',
    };
    showSnippetModal = true;
  }

  function copySnippetContent(content) {
    if (!content) return;
    navigator.clipboard
      .writeText(content)
      .then(() => toast.success(t('storage.yamlCopied')))
      .catch(() => toast.error(t('storage.copyFailed')));
  }

  function deleteSnippet(sn) {
    askConfirm({
      title: t('storage.deleteSnippetConfirm', { name: sn.name }),
      description: t('storage.deleteIsoDesc'),
      confirmLabel: t('common.delete'),
      onConfirm: async () => {
        try {
          await api.deleteCloudInitSnippet(sn.id);
          toast.success(t('storage.snippetDeleted'));
          onRefresh();
        } catch (e) {
          showStorageErr(t('storage.deleteSnippetError'), e);
        }
      },
    });
  }
</script>

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

<SnippetModal
  bind:open={showSnippetModal}
  {editingSnippet}
  initialForm={snippetInitialForm}
  onSaved={onRefresh}
/>
