<script>
  // LocalFolderBrowser — folder picker for a path on the server itself,
  // used by the storage-pool creation form. Backed by
  // POST /api/storage/browse-local.
  //
  // The sibling of RemoteFolderBrowser, and deliberately the same
  // shape: it never owns or disables the parent's text input. The
  // operator can still type a path by hand (including one that does not
  // exist yet); this only calls onSelect(path) when they confirm a
  // folder. It exists because typing the path is the step where a typo
  // quietly roots a pool somewhere it should not be.
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { Button } from '$lib/components/ui/button';
  import Icon from '$lib/components/Icon.svelte';
  import Spinner from '$lib/components/Spinner.svelte';

  let {
    // Where to open. Empty starts at the server's root list (the mount
    // points a pool can sensibly live under).
    startPath = '',
    onSelect, // (path: string) => void — absolute path on the server
  } = $props();

  let open = $state(false);
  let currentPath = $state('');
  let parentPath = $state('');
  let entries = $state([]);
  let loading = $state(false);
  let errorMsg = $state('');

  async function load(path) {
    loading = true;
    errorMsg = '';
    try {
      const res = await api.browseLocal(path);
      currentPath = res.path || '';
      parentPath = res.parent || '';
      entries = res.entries || [];
    } catch (e) {
      errorMsg = e.message;
      entries = [];
    } finally {
      loading = false;
    }
  }

  function toggle() {
    open = !open;
    if (open) load(startPath);
  }
  function descend(e) {
    load(e.path);
  }
  function up() {
    // An empty parent is the root list, not "/": the server does not
    // browse above the mount points it offers.
    load(parentPath);
  }
  function choose() {
    if (!currentPath) return;
    onSelect(currentPath);
    open = false;
  }
</script>

<div class="mt-1">
  <Button size="sm" variant="outline" type="button" onclick={toggle}>
    <Icon name="folder" size={13} class="mr-1" />
    {open ? t('localBrowse.hide') : t('localBrowse.browse')}
  </Button>

  {#if open}
    <div class="mt-2 border border-border rounded-lg p-2 bg-background max-w-md">
      <div class="flex items-center gap-2 text-xs text-muted-foreground mb-2">
        <Button
          size="sm"
          variant="ghost"
          type="button"
          onclick={up}
          disabled={!currentPath || loading}
        >
          ..
        </Button>
        <span class="truncate font-mono">{currentPath || t('localBrowse.rootLabel')}</span>
      </div>

      {#if loading}
        <div class="flex justify-center py-4">
          <Spinner size="sm" />
        </div>
      {:else if errorMsg}
        <p class="text-xs text-destructive py-2">{errorMsg}</p>
      {:else if entries.length === 0}
        <p class="text-xs text-muted-foreground py-2">{t('localBrowse.empty')}</p>
      {:else}
        <ul class="max-h-48 overflow-y-auto divide-y divide-border">
          {#each entries as e (e.path)}
            <li>
              <button
                type="button"
                class="w-full text-left px-2 py-1.5 text-sm hover:bg-muted/50 rounded flex items-center gap-2"
                onclick={() => descend(e)}
              >
                <Icon name="folder" size={13} class="shrink-0 text-muted-foreground" />
                <span class="truncate">{e.name}</span>
                <!-- A read-only mount is shown, not hidden: an operator
                     who mounted a disk read-only needs to see why it is
                     refused rather than wonder where it went. -->
                {#if !e.writable}
                  <span class="ml-auto text-[10px] text-warning shrink-0"
                    >{t('localBrowse.readOnly')}</span
                  >
                {/if}
              </button>
            </li>
          {/each}
        </ul>
      {/if}

      <div class="mt-2 flex items-center justify-between gap-2">
        <span class="text-[11px] text-muted-foreground truncate">
          {currentPath ? t('localBrowse.willUse') : t('localBrowse.pickOne')}
        </span>
        <Button size="sm" type="button" onclick={choose} disabled={loading || !currentPath}>
          {t('localBrowse.selectFolder')}
        </Button>
      </div>
    </div>
  {/if}
</div>
