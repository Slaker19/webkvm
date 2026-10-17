<script>
  import { onMount } from 'svelte';
  import { api, auth } from '$lib/stores/auth.svelte.js';
  import { t } from '$lib/i18n.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';

  let { vmId, vm = null, isContainerVm = false } = $props();

  let ciStatus = $state(null);
  let ciStatusLoading = $state(false);
  let ciUser = $state('');
  let ciPassword = $state('');
  let ciSSHKey = $state('');
  let ciHostname = $state('');
  let ciApplying = $state(false);
  let ciSnippetIds = $state([]);
  let ciAvailableSnippets = $state([]);

  export async function loadCloudInitStatus() {
    if (isContainerVm || !vmId) return;
    ciStatusLoading = true;
    try {
      ciStatus = await api.getCloudInitStatus(vmId);
      if (ciStatus?.user && !ciUser) ciUser = ciStatus.user;
    } catch (_e) {
      ciStatus = null;
    } finally {
      ciStatusLoading = false;
    }
  }

  export async function loadCloudInitSnippets() {
    if (ciAvailableSnippets.length) return;
    try {
      const list = await api.listCloudInitSnippets();
      ciAvailableSnippets = list || [];
    } catch (_e) {
      ciAvailableSnippets = [];
    }
  }

  async function reapplyCloudInit() {
    if (!ciUser && !ciSSHKey && !ciHostname && ciSnippetIds.length === 0) {
      toast.warning(t('vmDetail.cloudInitEmptyWarning'));
      return;
    }
    if (ciUser && !ciPassword) {
      toast.warning(t('vmDetail.cloudInitPasswordRequired'));
      return;
    }
    ciApplying = true;
    try {
      const payload = {
        user: ciUser || undefined,
        password: ciPassword || undefined,
        ssh_key: ciSSHKey || undefined,
        hostname: ciHostname || undefined,
        snippet_ids: ciSnippetIds.length ? ciSnippetIds : undefined,
      };
      const res = await api.reapplyCloudInit(vmId, payload);
      ciPassword = '';
      toast.success(
        res?.reprovisioned
          ? t('vmDetail.cloudInitAppliedLive')
          : t('vmDetail.cloudInitAppliedNeedsReboot')
      );
      await loadCloudInitStatus();
    } catch (e) {
      toast.error(e.message);
    } finally {
      ciApplying = false;
    }
  }

  onMount(() => {
    loadCloudInitStatus();
    loadCloudInitSnippets();
  });
</script>

<BlockCard bid="cloudinit" title="Cloud-Init">
  {#snippet headerActions()}
    {#if auth.canMutate() && vm?.state !== 'shutoff'}
      <span
        class="text-[11px] px-2 py-0.5 rounded border border-accent/30 bg-accent/10 text-accent"
      >
        {t('vmDetail.cloudInitLiveHint')}
      </span>
    {/if}
  {/snippet}

  {#if ciStatusLoading}
    <div class="flex justify-center py-6"><Spinner size="md" /></div>
  {:else}
    <div class="space-y-4">
      <!-- Current status strip -->
      <div
        class="flex flex-wrap items-center gap-3 p-3 rounded-lg border border-border bg-muted/20"
      >
        <div class="flex items-center gap-1.5">
          <div
            class="w-2 h-2 rounded-full {ciStatus?.has_seed_iso
              ? 'bg-success'
              : 'bg-muted-foreground/40'}"
          ></div>
          <span class="text-sm font-medium">
            {ciStatus?.has_seed_iso
              ? t('vmDetail.cloudInitSeedAttached')
              : t('vmDetail.cloudInitNoSeed')}
          </span>
        </div>
        {#if ciStatus?.user}
          <span class="text-xs font-mono px-2 py-0.5 rounded bg-background border border-border">
            {t('vmCreate.cloudInitUser')}: {ciStatus.user}
          </span>
        {/if}
      </div>

      <p class="text-xs text-muted-foreground">
        {t('vmDetail.cloudInitDesc')}
      </p>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
        <div>
          <label for="ci-detail-user" class="text-xs font-semibold text-foreground block mb-1"
            >{t('vmCreate.cloudInitUser')}</label
          >
          <Input id="ci-detail-user" bind:value={ciUser} placeholder="webkvm" class="w-full" />
        </div>
        <div>
          <label for="ci-detail-password" class="text-xs font-semibold text-foreground block mb-1"
            >{t('common.password')}</label
          >
          <Input
            id="ci-detail-password"
            type="password"
            bind:value={ciPassword}
            placeholder="••••••••"
            class="w-full"
          />
        </div>
        <div>
          <label for="ci-detail-hostname" class="text-xs font-semibold text-foreground block mb-1"
            >{t('vmCreate.cloudInitHostname')}</label
          >
          <Input
            id="ci-detail-hostname"
            bind:value={ciHostname}
            placeholder="my-vm"
            class="w-full"
          />
        </div>
        <div>
          <label for="ci-detail-sshkey" class="text-xs font-semibold text-foreground block mb-1"
            >{t('vmCreate.cloudInitSSHKey')}</label
          >
          <Input
            id="ci-detail-sshkey"
            bind:value={ciSSHKey}
            placeholder="ssh-ed25519 AAAA..."
            class="w-full"
          />
        </div>
      </div>

      {#if ciAvailableSnippets.length > 0}
        <div>
          <span class="text-xs font-semibold text-foreground block mb-1.5"
            >{t('snippets.selectPreset')}</span
          >
          <div class="flex flex-wrap gap-1.5">
            {#each ciAvailableSnippets as sn (sn.id)}
              {@const active = ciSnippetIds.includes(sn.id)}
              <button
                type="button"
                onclick={() => {
                  if (active) {
                    ciSnippetIds = ciSnippetIds.filter((x) => x !== sn.id);
                  } else {
                    ciSnippetIds = [...ciSnippetIds, sn.id];
                  }
                }}
                class="text-xs px-2.5 py-1 rounded-full border transition-all cursor-pointer font-medium {active
                  ? 'border-accent bg-accent/15 text-accent'
                  : 'border-border bg-background text-muted-foreground hover:bg-muted/60'}"
              >
                {sn.name}
              </button>
            {/each}
          </div>
        </div>
      {/if}

      {#if auth.canMutate()}
        <div class="flex justify-end pt-1">
          <Button onclick={reapplyCloudInit} disabled={ciApplying}>
            {#if ciApplying}
              <Spinner size="sm" color="text-white" />
              {t('vmDetail.cloudInitApplying')}
            {:else}
              <Icon name="refresh" size={14} class="mr-1.5" />
              {t('vmDetail.cloudInitRegenerate')}
            {/if}
          </Button>
        </div>
      {/if}
    </div>
  {/if}
</BlockCard>
