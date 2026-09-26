<script>
  // RemoteFolderBrowser — one-level-at-a-time subfolder picker for an
  // NFS export or SMB share, shared by Storage.svelte (pool creation)
  // and Backup.svelte (nfs/smb target creation). Backed by
  // POST /api/storage/browse-remote (internal/remotebrowse).
  //
  // Deliberately does NOT own the parent's source-dir text input: that
  // field stays editable at all times (the operator can still type an
  // arbitrary/not-yet-existing path by hand). This component only calls
  // onSelect(subpath) when the operator confirms a folder — it never
  // disables or replaces the text input.
  import { api } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { Button } from '$lib/components/ui/button';
  import Spinner from '$lib/components/Spinner.svelte';

  let {
    format, // 'nfs' | 'cifs'
    host,
    sourceDir,
    username = '',
    password = '',
    onSelect, // (subpath: string) => void — full subpath relative to sourceDir
  } = $props();

  let open = $state(false);
  let segments = $state([]);
  let entries = $state([]);
  let loading = $state(false);
  let errorMsg = $state('');

  let currentSubpath = $derived(segments.join('/'));
  let canBrowse = $derived(!!host && !!sourceDir);

  async function load() {
    loading = true;
    errorMsg = '';
    try {
      const res = await api.browseRemote({
        format,
        host,
        source_dir: sourceDir,
        subpath: currentSubpath,
        username,
        password,
      });
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
    if (open) {
      segments = [];
      load();
    }
  }
  function descend(name) {
    segments = [...segments, name];
    load();
  }
  function up() {
    segments = segments.slice(0, -1);
    load();
  }
  function choose() {
    onSelect(currentSubpath);
  }
</script>

<div class="mt-1">
  <Button size="sm" variant="outline" type="button" onclick={toggle} disabled={!canBrowse}>
    {open ? t('remoteBrowse.hide') : t('remoteBrowse.browse')}
  </Button>

  {#if open}
    <div class="mt-2 border border-border rounded-lg p-2 bg-background max-w-md">
      <div class="flex items-center gap-2 text-xs text-muted-foreground mb-2">
        <Button
          size="sm"
          variant="ghost"
          type="button"
          onclick={up}
          disabled={segments.length === 0 || loading}
        >
          ..
        </Button>
        <span class="truncate font-mono"
          >/{sourceDir}{currentSubpath ? '/' + currentSubpath : ''}</span
        >
      </div>

      {#if loading}
        <div class="flex justify-center py-4">
          <Spinner size="sm" />
        </div>
      {:else if errorMsg}
        <p class="text-xs text-destructive py-2">{errorMsg}</p>
      {:else if entries.length === 0}
        <p class="text-xs text-muted-foreground py-2">{t('remoteBrowse.empty')}</p>
      {:else}
        <ul class="max-h-48 overflow-y-auto divide-y divide-border">
          {#each entries as e (e.name)}
            <li>
              <button
                type="button"
                class="w-full text-left px-2 py-1.5 text-sm hover:bg-muted/50 rounded"
                onclick={() => descend(e.name)}
              >
                {e.name}
              </button>
            </li>
          {/each}
        </ul>
      {/if}

      <div class="mt-2 flex justify-end">
        <Button size="sm" type="button" onclick={choose} disabled={loading || !!errorMsg}>
          {t('remoteBrowse.selectFolder')}
        </Button>
      </div>
    </div>
  {/if}
</div>
