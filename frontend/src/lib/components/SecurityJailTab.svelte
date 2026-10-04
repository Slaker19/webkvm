<script>
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Card } from '$lib/components/ui/card';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import Switch from '$lib/components/Switch.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let activeSubTab = $state('banned'); // 'banned' | 'whitelist' | 'jails'
  let banned = $state([]);
  let whitelist = $state([]);
  let config = $state({
    enabled: true,
    ssh_jail: { enabled: true, max_attempts: 5, window_sec: 300, ban_duration_sec: 3600, port: 22 },
    webkvm_jail: { enabled: true, max_attempts: 5, window_sec: 300, ban_duration_sec: 900 },
    custom_jails: [],
  });

  let loading = $state(false);
  let saving = $state(false);

  // Modals state
  let showBanModal = $state(false);
  let banForm = $state({
    ip: '',
    reason: '',
    jail: 'manual',
    duration_sec: 86400,
  });

  let showWhitelistModal = $state(false);
  let whitelistForm = $state({
    cidr: '',
    description: '',
  });

  let showCustomJailModal = $state(false);
  let customJailForm = $state({
    name: '',
    max_attempts: 5,
    window_sec: 300,
    ban_duration_sec: 1800,
    port: 0,
    description: '',
  });

  async function load() {
    loading = true;
    try {
      const [bansRes, whiteRes, cfgRes] = await Promise.all([
        api.listJailedIPs(),
        api.getJailWhitelist(),
        api.getJailConfig(),
      ]);
      banned = bansRes || [];
      whitelist = whiteRes.whitelist || [];
      if (cfgRes && cfgRes.webkvm_jail) {
        config = cfgRes;
      }
    } catch (e) {
      toast.error(t('securityJail.loadError', { error: e.message }));
    } finally {
      loading = false;
    }
  }

  async function unban(ip) {
    try {
      await api.unbanJailedIP(ip);
      toast.success(t('securityJail.unbanSuccess', { ip }));
      await load();
    } catch (e) {
      toast.error(t('securityJail.unbanError', { error: e.message }));
    }
  }

  async function handleManualBan() {
    if (!banForm.ip.trim()) return;
    try {
      await api.manualBanIP({
        ip: banForm.ip.trim(),
        reason: banForm.reason.trim() || 'Manual ban by administrator',
        jail: banForm.jail,
        duration_sec: Number(banForm.duration_sec) || 86400,
      });
      toast.success(t('securityJail.banSuccess', { ip: banForm.ip }));
      showBanModal = false;
      banForm.ip = '';
      banForm.reason = '';
      await load();
    } catch (e) {
      toast.error(t('securityJail.banError', { error: e.message }));
    }
  }

  async function handleAddWhitelist() {
    if (!whitelistForm.cidr.trim()) return;
    try {
      await api.addJailWhitelist({
        cidr: whitelistForm.cidr.trim(),
        description: whitelistForm.description.trim(),
      });
      toast.success(t('securityJail.whitelistSuccess', { cidr: whitelistForm.cidr }));
      showWhitelistModal = false;
      whitelistForm.cidr = '';
      whitelistForm.description = '';
      await load();
    } catch (e) {
      toast.error(t('securityJail.whitelistError', { error: e.message }));
    }
  }

  async function handleRemoveWhitelist(cidr) {
    if (!confirm(t('securityJail.confirmRemoveWhitelist') + ` (${cidr})`)) return;
    try {
      await api.removeJailWhitelist(cidr);
      toast.success(t('securityJail.whitelistRemoveSuccess', { cidr }));
      await load();
    } catch (e) {
      toast.error(t('securityJail.whitelistError', { error: e.message }));
    }
  }

  async function handleSaveJailsConfig() {
    saving = true;
    try {
      await api.updateJailConfig(config);
      toast.success(t('securityJail.jailsConfigSaved'));
      await load();
    } catch (e) {
      toast.error(e.message);
    } finally {
      saving = false;
    }
  }

  async function handleCreateCustomJail() {
    if (!customJailForm.name.trim()) return;
    try {
      await api.addCustomJail({
        name: customJailForm.name.trim(),
        max_attempts: Number(customJailForm.max_attempts) || 5,
        window_sec: Number(customJailForm.window_sec) || 300,
        ban_duration_sec: Number(customJailForm.ban_duration_sec) || 1800,
        port: Number(customJailForm.port) || 0,
        description: customJailForm.description.trim(),
        enabled: true,
      });
      toast.success(t('securityJail.customJailCreated', { name: customJailForm.name }));
      showCustomJailModal = false;
      customJailForm = {
        name: '',
        max_attempts: 5,
        window_sec: 300,
        ban_duration_sec: 1800,
        port: 0,
        description: '',
      };
      await load();
    } catch (e) {
      toast.error(e.message);
    }
  }

  async function handleDeleteCustomJail(id) {
    if (!confirm(t('securityJail.deleteJail') + ` (${id})?`)) return;
    try {
      await api.deleteCustomJail(id);
      toast.success(t('securityJail.customJailDeleted'));
      await load();
    } catch (e) {
      toast.error(e.message);
    }
  }

  onMount(() => {
    load();
    const interval = setInterval(load, 15000);
    return () => clearInterval(interval);
  });
</script>

<div class="space-y-6">
  <!-- Tarjetas de estado perimetral -->
  <div class="grid grid-cols-1 sm:grid-cols-4 gap-4">
    <Card class="p-4 border border-border bg-card/60">
      <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        {t('securityJail.perimeterStatus')}
      </div>
      <div class="mt-2 flex items-center gap-2">
        <span class="inline-block w-2.5 h-2.5 rounded-full bg-emerald-500 animate-pulse"></span>
        <span class="text-base font-semibold text-foreground"
          >{t('securityJail.nftablesActive')}</span
        >
      </div>
      <div class="mt-1 text-xs text-muted-foreground">{t('securityJail.kernelProtection')}</div>
    </Card>

    <Card class="p-4 border border-border bg-card/60">
      <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        {t('securityJail.blockedIPs')}
      </div>
      <div
        class="mt-2 text-2xl font-bold font-mono {banned.length > 0
          ? 'text-destructive'
          : 'text-foreground'}"
      >
        {banned.length}
      </div>
      <div class="mt-1 text-xs text-muted-foreground">{t('securityJail.immediateDrop')}</div>
    </Card>

    <Card class="p-4 border border-border bg-card/60">
      <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        {t('securityJail.sshJailTitle')}
      </div>
      <div class="mt-2 flex items-center gap-2">
        <span
          class="inline-block w-2 h-2 rounded-full {config.ssh_jail?.enabled
            ? 'bg-emerald-500'
            : 'bg-muted-foreground'}"
        ></span>
        <span class="text-xs font-semibold font-mono text-foreground">
          {config.ssh_jail?.enabled ? 'PORT 22 (ACTIVE)' : 'DISABLED'}
        </span>
      </div>
      <div class="mt-1 text-xs text-muted-foreground">{t('securityJail.sshJailDesc')}</div>
    </Card>

    <Card class="p-4 border border-border bg-card/60">
      <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        {t('securityJail.protectedNetworks')}
      </div>
      <div class="mt-2 text-2xl font-bold font-mono text-foreground">
        {whitelist.length}
      </div>
      <div class="mt-1 text-xs text-muted-foreground">{t('securityJail.whitelistImmune')}</div>
    </Card>
  </div>

  <!-- Navegación de Subpestañas del Jail -->
  <div class="flex items-center justify-between border-b border-border pb-2">
    <div class="flex items-center gap-2">
      <Button
        variant={activeSubTab === 'banned' ? 'secondary' : 'ghost'}
        size="sm"
        class="text-xs font-medium"
        onclick={() => (activeSubTab = 'banned')}
      >
        {t('securityJail.tabBanned')} ({banned.length})
      </Button>
      <Button
        variant={activeSubTab === 'whitelist' ? 'secondary' : 'ghost'}
        size="sm"
        class="text-xs font-medium"
        onclick={() => (activeSubTab = 'whitelist')}
      >
        {t('securityJail.tabWhitelist')} ({whitelist.length})
      </Button>
      <Button
        variant={activeSubTab === 'jails' ? 'secondary' : 'ghost'}
        size="sm"
        class="text-xs font-medium"
        onclick={() => (activeSubTab = 'jails')}
      >
        {t('securityJail.tabJails')}
      </Button>
    </div>

    <div class="flex items-center gap-2">
      {#if activeSubTab === 'banned'}
        <Button
          size="sm"
          variant="default"
          class="!h-8 !text-xs"
          onclick={() => (showBanModal = true)}
        >
          {t('securityJail.manualBanBtn')}
        </Button>
      {:else if activeSubTab === 'whitelist'}
        <Button
          size="sm"
          variant="default"
          class="!h-8 !text-xs"
          onclick={() => (showWhitelistModal = true)}
        >
          {t('securityJail.addWhitelistBtn')}
        </Button>
      {:else if activeSubTab === 'jails'}
        <Button
          size="sm"
          variant="outline"
          class="!h-8 !text-xs"
          onclick={() => (showCustomJailModal = true)}
        >
          {t('securityJail.addCustomJailBtn')}
        </Button>
      {/if}

      <Button
        variant="outline"
        size="sm"
        class="!h-8 !text-xs gap-1.5"
        onclick={load}
        disabled={loading}
      >
        <svg
          class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}"
          fill="none"
          stroke="currentColor"
          stroke-width="2"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
          />
        </svg>
        {t('securityJail.refresh')}
      </Button>
    </div>
  </div>

  <!-- PESTAÑA 1: IPs Bloqueadas -->
  {#if activeSubTab === 'banned'}
    <div class="space-y-3">
      <div>
        <h3 class="text-sm font-semibold text-foreground">{t('securityJail.title')}</h3>
        <p class="text-xs text-muted-foreground">{t('securityJail.desc')}</p>
      </div>

      <Card class="overflow-hidden border border-border">
        <div class="overflow-x-auto">
          <table class="w-full text-left text-xs">
            <thead class="bg-muted/40 border-b border-border text-muted-foreground font-medium">
              <tr>
                <th class="p-3">{t('securityJail.colIp')}</th>
                <th class="p-3">{t('securityJail.colJail')}</th>
                <th class="p-3">{t('securityJail.colReason')}</th>
                <th class="p-3">{t('securityJail.colAttempts')}</th>
                <th class="p-3">{t('securityJail.colBannedAt')}</th>
                <th class="p-3">{t('securityJail.colExpires')}</th>
                <th class="p-3 text-right">{t('securityJail.colAction')}</th>
              </tr>
            </thead>
            <tbody class="divide-y border-border">
              {#if banned.length === 0}
                <tr>
                  <td colspan="7" class="p-6 text-center text-muted-foreground">
                    <div class="flex flex-col items-center justify-center gap-1.5">
                      <svg
                        class="w-6 h-6 text-emerald-500"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="2"
                        viewBox="0 0 24 24"
                      >
                        <path
                          stroke-linecap="round"
                          stroke-linejoin="round"
                          d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"
                        />
                      </svg>
                      <span class="font-medium text-foreground"
                        >{t('securityJail.noThreatsTitle')}</span
                      >
                      <span class="text-[11px]">{t('securityJail.noThreatsDesc')}</span>
                    </div>
                  </td>
                </tr>
              {:else}
                {#each banned as item (item.ip)}
                  <tr class="hover:bg-muted/20 transition-colors">
                    <td class="p-3 font-mono font-semibold text-destructive">{item.ip}</td>
                    <td class="p-3">
                      <span
                        class="px-2 py-0.5 rounded text-[10px] font-mono uppercase bg-muted text-foreground"
                      >
                        {item.jail || 'webkvm'}
                      </span>
                    </td>
                    <td class="p-3 text-foreground"
                      >{item.reason || t('securityJail.reasonBruteForce')}</td
                    >
                    <td class="p-3 font-mono">{item.fail_count}</td>
                    <td class="p-3 text-muted-foreground font-mono"
                      >{new Date(item.banned_at).toLocaleString()}</td
                    >
                    <td class="p-3 text-muted-foreground font-mono"
                      >{new Date(item.expires_at).toLocaleString()}</td
                    >
                    <td class="p-3 text-right">
                      <Button
                        variant="outline"
                        size="sm"
                        class="!h-7 !text-[11px] text-destructive hover:bg-destructive/10"
                        onclick={() => unban(item.ip)}
                      >
                        {t('securityJail.unban')}
                      </Button>
                    </td>
                  </tr>
                {/each}
              {/if}
            </tbody>
          </table>
        </div>
      </Card>
    </div>
  {/if}

  <!-- PESTAÑA 2: Redes en Whitelist -->
  {#if activeSubTab === 'whitelist'}
    <div class="space-y-3">
      <div>
        <h3 class="text-sm font-semibold text-foreground">{t('securityJail.whitelistTitle')}</h3>
        <p class="text-xs text-muted-foreground">{t('securityJail.whitelistDesc')}</p>
      </div>

      <Card class="overflow-hidden border border-border">
        <div class="overflow-x-auto">
          <table class="w-full text-left text-xs">
            <thead class="bg-muted/40 border-b border-border text-muted-foreground font-medium">
              <tr>
                <th class="p-3">{t('securityJail.colCidr')}</th>
                <th class="p-3">{t('securityJail.colDescription')}</th>
                <th class="p-3">{t('securityJail.colType')}</th>
                <th class="p-3 text-right">{t('securityJail.colAction')}</th>
              </tr>
            </thead>
            <tbody class="divide-y border-border">
              {#each whitelist as entry (entry.cidr)}
                <tr class="hover:bg-muted/20 transition-colors">
                  <td class="p-3 font-mono font-semibold text-foreground">{entry.cidr}</td>
                  <td class="p-3 text-muted-foreground">{entry.description || '—'}</td>
                  <td class="p-3">
                    <span
                      class="px-2 py-0.5 rounded text-[10px] font-mono {entry.system
                        ? 'bg-primary/10 text-primary'
                        : 'bg-muted text-foreground'}"
                    >
                      {entry.system ? t('securityJail.typeSystem') : t('securityJail.typeCustom')}
                    </span>
                  </td>
                  <td class="p-3 text-right">
                    <Button
                      variant="ghost"
                      size="sm"
                      class="!h-7 !text-[11px] text-destructive hover:bg-destructive/10"
                      onclick={() => handleRemoveWhitelist(entry.cidr)}
                    >
                      {t('securityJail.remove')}
                    </Button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
    </div>
  {/if}

  <!-- PESTAÑA 3: Jails y Servicios -->
  {#if activeSubTab === 'jails'}
    <div class="space-y-5">
      <div>
        <h3 class="text-sm font-semibold text-foreground">{t('securityJail.jailsTitle')}</h3>
        <p class="text-xs text-muted-foreground">{t('securityJail.jailsDesc')}</p>
      </div>

      <!-- General master switch -->
      <Card class="p-4 border border-border bg-card space-y-3">
        <Switch
          bind:checked={config.enabled}
          label="Perimeter Defense (Jail Engine)"
          description="Master switch for kernel-level brute force protection and IP isolation via nftables"
        />
      </Card>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <!-- WebKVM Jail Card -->
        <Card class="p-4 border border-border bg-card space-y-3">
          <div class="flex items-center justify-between">
            <span class="text-sm font-semibold text-foreground"
              >{t('securityJail.webkvmJailTitle')}</span
            >
            <span class="text-xs px-2 py-0.5 rounded bg-muted text-muted-foreground font-mono"
              >APP / WEB</span
            >
          </div>
          <p class="text-xs text-muted-foreground">{t('securityJail.webkvmJailDesc')}</p>

          <Switch
            bind:checked={config.webkvm_jail.enabled}
            label={t('securityJail.fieldEnabled')}
            description="Protect WebKVM logins and API endpoints"
          />

          <div class="grid grid-cols-3 gap-2 pt-2">
            <div class="space-y-1">
              <Label class="text-[11px]">{t('securityJail.fieldMaxAttempts')}</Label>
              <Input type="number" min="1" max="100" bind:value={config.webkvm_jail.max_attempts} />
            </div>
            <div class="space-y-1">
              <Label class="text-[11px]">{t('securityJail.fieldWindowSec')}</Label>
              <Input
                type="number"
                min="10"
                max="86400"
                bind:value={config.webkvm_jail.window_sec}
              />
            </div>
            <div class="space-y-1">
              <Label class="text-[11px]">{t('securityJail.fieldBanDurationSec')}</Label>
              <Input
                type="number"
                min="60"
                max="31536000"
                bind:value={config.webkvm_jail.ban_duration_sec}
              />
            </div>
          </div>
        </Card>

        <!-- SSH Jail Card -->
        <Card class="p-4 border border-border bg-card space-y-3">
          <div class="flex items-center justify-between">
            <span class="text-sm font-semibold text-foreground"
              >{t('securityJail.sshJailTitle')}</span
            >
            <span class="text-xs px-2 py-0.5 rounded bg-muted text-muted-foreground font-mono"
              >PORT 22</span
            >
          </div>
          <p class="text-xs text-muted-foreground">{t('securityJail.sshJailDesc')}</p>

          <Switch
            bind:checked={config.ssh_jail.enabled}
            label={t('securityJail.fieldEnabled')}
            description="Isolate SSH brute-force attackers at kernel level"
          />

          <div class="grid grid-cols-3 gap-2 pt-2">
            <div class="space-y-1">
              <Label class="text-[11px]">{t('securityJail.fieldMaxAttempts')}</Label>
              <Input type="number" min="1" max="100" bind:value={config.ssh_jail.max_attempts} />
            </div>
            <div class="space-y-1">
              <Label class="text-[11px]">{t('securityJail.fieldWindowSec')}</Label>
              <Input type="number" min="10" max="86400" bind:value={config.ssh_jail.window_sec} />
            </div>
            <div class="space-y-1">
              <Label class="text-[11px]">{t('securityJail.fieldBanDurationSec')}</Label>
              <Input
                type="number"
                min="60"
                max="31536000"
                bind:value={config.ssh_jail.ban_duration_sec}
              />
            </div>
          </div>
        </Card>
      </div>

      <!-- Custom Jails -->
      <Card class="p-4 border border-border bg-card space-y-4">
        <div class="flex items-center justify-between">
          <div>
            <h4 class="text-xs font-semibold text-foreground uppercase tracking-wider">
              {t('securityJail.customJailsTitle')}
            </h4>
            <p class="text-xs text-muted-foreground">{t('securityJail.customJailsDesc')}</p>
          </div>
          <Button
            size="sm"
            variant="outline"
            class="!h-7 !text-xs"
            onclick={() => (showCustomJailModal = true)}
          >
            {t('securityJail.addCustomJailBtn')}
          </Button>
        </div>

        {#if !config.custom_jails || config.custom_jails.length === 0}
          <div
            class="p-4 rounded border border-dashed border-border text-center text-xs text-muted-foreground"
          >
            {t('securityJail.customJailsEmpty')}
          </div>
        {:else}
          <div class="space-y-2">
            {#each config.custom_jails as cj (cj.id)}
              <div
                class="flex items-center justify-between p-3 rounded border border-border bg-muted/20"
              >
                <div class="space-y-0.5">
                  <div class="flex items-center gap-2">
                    <span class="text-xs font-semibold text-foreground">{cj.name}</span>
                    <span
                      class="px-1.5 py-0.2 rounded text-[10px] font-mono bg-muted text-muted-foreground"
                      >ID: {cj.id}</span
                    >
                    {#if cj.port > 0}
                      <span
                        class="px-1.5 py-0.2 rounded text-[10px] font-mono bg-primary/10 text-primary"
                        >PORT: {cj.port}</span
                      >
                    {/if}
                  </div>
                  <div class="text-[11px] text-muted-foreground">
                    {cj.description || 'Custom service'} &bull; {cj.max_attempts} attempts / {cj.window_sec}s
                    &bull; Ban: {cj.ban_duration_sec}s
                  </div>
                </div>

                <div class="flex items-center gap-2">
                  <Button
                    variant="ghost"
                    size="sm"
                    class="!h-7 !text-xs text-destructive hover:bg-destructive/10"
                    onclick={() => handleDeleteCustomJail(cj.id)}
                  >
                    {t('securityJail.deleteJail')}
                  </Button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </Card>

      <div class="flex justify-end pt-2">
        <Button size="sm" onclick={handleSaveJailsConfig} disabled={saving}>
          {saving ? '...' : t('securityJail.saveJailsConfig')}
        </Button>
      </div>
    </div>
  {/if}
</div>

<!-- MODAL: Banear IP a mano -->
{#if showBanModal}
  <div
    class="fixed inset-0 z-50 bg-background/80 backdrop-blur-sm flex items-center justify-center p-4"
  >
    <div
      class="bg-card border border-border rounded-xl shadow-2xl max-w-md w-full p-6 space-y-4 animate-in fade-in zoom-in-95"
    >
      <div class="border-b border-border pb-2">
        <h3 class="text-sm font-bold text-foreground">{t('securityJail.modalBanTitle')}</h3>
        <p class="text-xs text-muted-foreground mt-0.5">{t('securityJail.modalBanDesc')}</p>
      </div>

      <div class="space-y-3">
        <div class="space-y-1">
          <Label class="text-xs">{t('securityJail.fieldIp')}</Label>
          <Input placeholder="e.g. 203.0.113.42" bind:value={banForm.ip} />
        </div>

        <div class="space-y-1">
          <Label class="text-xs">{t('securityJail.fieldReason')}</Label>
          <Input placeholder="e.g. Port scanning, SSH brute force" bind:value={banForm.reason} />
        </div>

        <div class="space-y-1">
          <Label class="text-xs">{t('securityJail.fieldJail')}</Label>
          <select
            class="w-full h-9 rounded-md border border-border bg-background px-3 text-xs"
            bind:value={banForm.jail}
          >
            <option value="manual">Manual</option>
            <option value="ssh">SSH</option>
            <option value="webkvm">WebKVM</option>
            {#each config.custom_jails || [] as cj (cj.id)}
              <option value={cj.id}>{cj.name}</option>
            {/each}
          </select>
        </div>

        <div class="space-y-1">
          <Label class="text-xs">{t('securityJail.fieldDuration')}</Label>
          <select
            class="w-full h-9 rounded-md border border-border bg-background px-3 text-xs"
            bind:value={banForm.duration_sec}
          >
            <option value={900}>{t('securityJail.duration15m')}</option>
            <option value={3600}>{t('securityJail.duration1h')}</option>
            <option value={86400}>{t('securityJail.duration24h')}</option>
            <option value={604800}>{t('securityJail.duration7d')}</option>
            <option value={2592000}>{t('securityJail.duration30d')}</option>
            <option value={31536000}>{t('securityJail.duration1y')}</option>
          </select>
        </div>
      </div>

      <div class="flex items-center justify-end gap-2 pt-2 border-t border-border">
        <Button variant="ghost" size="sm" onclick={() => (showBanModal = false)}>
          {t('securityJail.cancel')}
        </Button>
        <Button variant="destructive" size="sm" onclick={handleManualBan}>
          {t('securityJail.confirmBan')}
        </Button>
      </div>
    </div>
  </div>
{/if}

<!-- MODAL: Añadir Red a Whitelist -->
{#if showWhitelistModal}
  <div
    class="fixed inset-0 z-50 bg-background/80 backdrop-blur-sm flex items-center justify-center p-4"
  >
    <div
      class="bg-card border border-border rounded-xl shadow-2xl max-w-md w-full p-6 space-y-4 animate-in fade-in zoom-in-95"
    >
      <div class="border-b border-border pb-2">
        <h3 class="text-sm font-bold text-foreground">{t('securityJail.modalWhitelistTitle')}</h3>
        <p class="text-xs text-muted-foreground mt-0.5">{t('securityJail.modalWhitelistDesc')}</p>
      </div>

      <div class="space-y-3">
        <div class="space-y-1">
          <Label class="text-xs">{t('securityJail.fieldCidr')}</Label>
          <Input placeholder="e.g. 192.168.1.0/24 or 10.8.0.5" bind:value={whitelistForm.cidr} />
        </div>

        <div class="space-y-1">
          <Label class="text-xs">{t('securityJail.fieldDesc')}</Label>
          <Input placeholder="e.g. Admin Office VPN" bind:value={whitelistForm.description} />
        </div>
      </div>

      <div class="flex items-center justify-end gap-2 pt-2 border-t border-border">
        <Button variant="ghost" size="sm" onclick={() => (showWhitelistModal = false)}>
          {t('securityJail.cancel')}
        </Button>
        <Button size="sm" onclick={handleAddWhitelist}>
          {t('securityJail.confirmAddWhitelist')}
        </Button>
      </div>
    </div>
  </div>
{/if}

<!-- MODAL: Crear Custom Jail -->
{#if showCustomJailModal}
  <div
    class="fixed inset-0 z-50 bg-background/80 backdrop-blur-sm flex items-center justify-center p-4"
  >
    <div
      class="bg-card border border-border rounded-xl shadow-2xl max-w-md w-full p-6 space-y-4 animate-in fade-in zoom-in-95"
    >
      <div class="border-b border-border pb-2">
        <h3 class="text-sm font-bold text-foreground">{t('securityJail.modalCustomJailTitle')}</h3>
        <p class="text-xs text-muted-foreground mt-0.5">{t('securityJail.modalCustomJailDesc')}</p>
      </div>

      <div class="space-y-3">
        <div class="space-y-1">
          <Label class="text-xs">{t('securityJail.fieldJailName')}</Label>
          <Input placeholder="e.g. OpenVPN Gateway, FTP Server" bind:value={customJailForm.name} />
        </div>

        <div class="grid grid-cols-2 gap-2">
          <div class="space-y-1">
            <Label class="text-xs">{t('securityJail.fieldPort')}</Label>
            <Input type="number" placeholder="e.g. 1194" bind:value={customJailForm.port} />
          </div>
          <div class="space-y-1">
            <Label class="text-xs">{t('securityJail.fieldMaxAttempts')}</Label>
            <Input type="number" min="1" max="100" bind:value={customJailForm.max_attempts} />
          </div>
        </div>

        <div class="grid grid-cols-2 gap-2">
          <div class="space-y-1">
            <Label class="text-xs">{t('securityJail.fieldWindowSec')}</Label>
            <Input type="number" min="10" max="86400" bind:value={customJailForm.window_sec} />
          </div>
          <div class="space-y-1">
            <Label class="text-xs">{t('securityJail.fieldBanDurationSec')}</Label>
            <Input
              type="number"
              min="60"
              max="31536000"
              bind:value={customJailForm.ban_duration_sec}
            />
          </div>
        </div>

        <div class="space-y-1">
          <Label class="text-xs">{t('securityJail.fieldDesc')}</Label>
          <Input
            placeholder="e.g. Blocks port scanners after failed handshakes"
            bind:value={customJailForm.description}
          />
        </div>
      </div>

      <div class="flex items-center justify-end gap-2 pt-2 border-t border-border">
        <Button variant="ghost" size="sm" onclick={() => (showCustomJailModal = false)}>
          {t('securityJail.cancel')}
        </Button>
        <Button size="sm" onclick={handleCreateCustomJail}>
          {t('securityJail.confirmCreateJail')}
        </Button>
      </div>
    </div>
  </div>
{/if}
