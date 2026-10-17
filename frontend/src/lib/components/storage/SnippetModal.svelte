<script>
  import * as Dialog from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { toast } from '$lib/components/ui/toast';
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';

  let {
    open = $bindable(false),
    editingSnippet = null,
    initialForm = null,
    onSaved = () => {},
  } = $props();

  let snippetSaving = $state(false);
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

  $effect(() => {
    if (open) {
      previewingRender = false;
      previewRenderResult = '';
      if (initialForm) {
        snippetForm = { ...initialForm };
      } else if (editingSnippet) {
        snippetForm = {
          name: editingSnippet.name,
          description: editingSnippet.description || '',
          category: editingSnippet.category || (editingSnippet.is_preset ? 'devops' : 'custom'),
          type: editingSnippet.type || 'user-data',
          content: editingSnippet.content || '',
        };
      } else {
        snippetForm = {
          name: '',
          description: '',
          category: 'custom',
          type: 'user-data',
          content:
            '#cloud-config\n# Tu configuración personalizada aquí\npackage_update: true\npackages:\n  - curl\n',
        };
      }
    }
  });

  function copySnippetContent(content) {
    if (!content) return;
    navigator.clipboard
      .writeText(content)
      .then(() => toast.success(t('storage.yamlCopied')))
      .catch(() => toast.error(t('storage.copyFailed')));
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
      open = false;
      onSaved();
    } catch (e) {
      toast.error(e.message || t('storage.saveSnippetError'));
    } finally {
      snippetSaving = false;
    }
  }
</script>

<Dialog.Root bind:open>
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

      <div>
        <div class="flex items-center justify-between mb-1">
          <label
            for="sn-content"
            class="text-xs font-semibold text-foreground flex items-center gap-2"
          >
            <span>{t('storage.snippetContent')}</span>
            {#if snippetForm.content.trim().startsWith('#cloud-config')}
              <span
                class="text-[10px] font-mono font-bold text-success bg-success/10 px-1.5 py-0.2 rounded border border-success/20 inline-flex items-center gap-1"
              >
                <Icon name="check" size={10} />
                <span>{t('storage.cloudConfigValid')}</span>
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
      <Button variant="outline" onclick={() => (open = false)}>
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
