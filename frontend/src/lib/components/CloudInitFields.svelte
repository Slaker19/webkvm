<script>
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import { t } from '$lib/i18n.svelte.js';

  let {
    user = $bindable(''),
    password = $bindable(''),
    hostname = $bindable(''),
    sshKey = $bindable(''),
    ipMode = $bindable('dhcp'),
    staticIP = $bindable(''),
    gateway = $bindable(''),
    dns = $bindable(''),
    selectedSnippetIds = $bindable([]),
    customUserData = $bindable(''),
    availableSnippets = [],
    error = '',
    isContainer = false,
    userPlaceholder = '',
    hostnamePlaceholder = '',
    idPrefix = 'ci',
    onpreview = null,
    oninput = null,
  } = $props();

  let showPass = $state(false);

  function toggleSnippet(snId) {
    if (selectedSnippetIds.includes(snId)) {
      selectedSnippetIds = selectedSnippetIds.filter((x) => x !== snId);
    } else {
      selectedSnippetIds = [...selectedSnippetIds, snId];
    }
  }
</script>

<div class="space-y-4">
  <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
    <!-- Usuario -->
    <div>
      <label for="{idPrefix}-user" class="text-xs font-semibold text-foreground block mb-1">
        {t('vmCreate.cloudInitUser')}
        {isContainer ? `(${t('common.optional')})` : '*'}
      </label>
      <input
        id="{idPrefix}-user"
        bind:value={user}
        class="input w-full"
        placeholder={userPlaceholder || (isContainer ? t('vmCreate.lxcUserPlaceholder') : 'webkvm')}
        oninput={() => oninput?.('user')}
      />
      <p class="text-[11px] text-muted-foreground mt-1">
        {isContainer ? t('vmCreate.lxcUserOptional') : t('vmCreate.cloudInitUserHelper')}
      </p>
    </div>

    <!-- Contraseña -->
    <div>
      <label for="{idPrefix}-pass" class="text-xs font-semibold text-foreground block mb-1">
        {t('common.password')} *
      </label>
      <div class="relative">
        <input
          id="{idPrefix}-pass"
          bind:value={password}
          type={showPass ? 'text' : 'password'}
          minlength="6"
          maxlength="12"
          class="input w-full pr-10"
          placeholder={t('vmCreate.cloudInitPasswordPlaceholder')}
          oninput={() => oninput?.('password')}
        />
        <button
          type="button"
          onclick={() => (showPass = !showPass)}
          class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground p-1 rounded cursor-pointer"
          aria-label={showPass ? t('login.hidePassword') : t('login.showPassword')}
          aria-pressed={showPass}
        >
          <Icon name={showPass ? 'eyeOff' : 'eye'} size={16} />
        </button>
      </div>
      <p class="text-[11px] text-muted-foreground mt-1">
        {t('vmCreate.cloudInitPasswordHelper')}
      </p>
    </div>

    <!-- Hostname -->
    <div class="sm:col-span-2">
      <label for="{idPrefix}-hostname" class="text-xs font-semibold text-foreground block mb-1">
        {t('vmCreate.cloudInitHostname')}
      </label>
      <input
        id="{idPrefix}-hostname"
        bind:value={hostname}
        class="input w-full"
        placeholder={hostnamePlaceholder || 'my-instance'}
      />
    </div>

    <!-- Clave SSH -->
    <div class="sm:col-span-2">
      <label for="{idPrefix}-sshkey" class="text-xs font-semibold text-foreground block mb-1">
        {t('vmCreate.cloudInitSSHKey')}
      </label>
      <textarea
        id="{idPrefix}-sshkey"
        bind:value={sshKey}
        class="input w-full font-mono text-xs"
        rows="2"
        placeholder={t('vmCreate.cloudInitSSHKeyPlaceholder')}
      ></textarea>
    </div>

    <!-- Red / IP Visual Config -->
    <div class="sm:col-span-2 pt-2 border-t border-border/60 space-y-2.5">
      <div class="flex items-center justify-between">
        <span class="text-xs font-semibold text-foreground flex items-center gap-1.5">
          <Icon name="network" size={14} class="text-accent" />
          {t('cloudInit.networkConfig')}
        </span>
        <div class="flex rounded-md border border-border bg-muted/40 p-0.5">
          <button
            type="button"
            class="px-2 py-0.5 text-xs font-medium rounded transition-colors cursor-pointer {ipMode === 'dhcp' ? 'bg-background shadow-xs text-foreground font-semibold' : 'text-muted-foreground hover:text-foreground'}"
            onclick={() => (ipMode = 'dhcp')}
          >
            DHCP
          </button>
          <button
            type="button"
            class="px-2 py-0.5 text-xs font-medium rounded transition-colors cursor-pointer {ipMode === 'static' ? 'bg-background shadow-xs text-foreground font-semibold' : 'text-muted-foreground hover:text-foreground'}"
            onclick={() => (ipMode = 'static')}
          >
            {t('cloudInit.staticIP')}
          </button>
        </div>
      </div>

      {#if ipMode === 'static'}
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-3 pt-1">
          <div>
            <label for="{idPrefix}-static-ip" class="text-xs font-medium text-foreground block mb-1">
              {t('cloudInit.ipAddressCIDR')} *
            </label>
            <input
              id="{idPrefix}-static-ip"
              bind:value={staticIP}
              class="input w-full font-mono text-xs"
              placeholder="192.168.1.50/24"
            />
          </div>
          <div>
            <label for="{idPrefix}-gateway" class="text-xs font-medium text-foreground block mb-1">
              {t('cloudInit.gateway')}
            </label>
            <input
              id="{idPrefix}-gateway"
              bind:value={gateway}
              class="input w-full font-mono text-xs"
              placeholder="192.168.1.1"
            />
          </div>
          <div class="sm:col-span-2">
            <label for="{idPrefix}-dns" class="text-xs font-medium text-foreground block mb-1">
              {t('cloudInit.dnsServers')}
            </label>
            <input
              id="{idPrefix}-dns"
              bind:value={dns}
              class="input w-full font-mono text-xs"
              placeholder="1.1.1.1, 8.8.8.8"
            />
            <p class="text-[11px] text-muted-foreground mt-1">{t('cloudInit.dnsHelper')}</p>
          </div>
        </div>
      {/if}
    </div>

    <!-- Cloud-Init Studio / Snippets Picker -->
    <div class="sm:col-span-2 pt-2 border-t border-border/60 space-y-2.5">
      <div class="flex items-center justify-between gap-2">
        <div>
          <span class="text-xs font-semibold text-foreground flex items-center gap-1.5">
            <Icon name="code" size={14} class="text-accent" />
            {t('snippets.studioTitle')}
          </span>
          <p class="text-[11px] text-muted-foreground">
            {t('snippets.studioSubtitle')}
          </p>
        </div>
        {#if onpreview}
          <Button
            type="button"
            variant="outline"
            size="sm"
            class="h-7 text-xs gap-1 shrink-0 cursor-pointer"
            onclick={onpreview}
          >
            <Icon name="eye" size={13} />
            {t('snippets.previewBtn')}
          </Button>
        {/if}
      </div>

      {#if availableSnippets.length > 0}
        <div class="space-y-1">
          <span class="text-xs text-muted-foreground block">{t('snippets.selectPreset')}</span>
          <div class="flex flex-wrap gap-1.5">
            {#each availableSnippets as sn (sn.id)}
              {@const isSelected = selectedSnippetIds.includes(sn.id)}
              <button
                type="button"
                aria-pressed={isSelected}
                onclick={() => toggleSnippet(sn.id)}
                class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-lg text-xs font-medium border transition-all cursor-pointer {isSelected
                  ? 'bg-accent text-accent-foreground border-accent font-semibold shadow-xs'
                  : 'bg-card text-muted-foreground hover:text-foreground hover:bg-muted border-border'}"
              >
                <Icon name={isSelected ? 'check' : 'plus'} size={12} />
                <span>{sn.name}</span>
                {#if sn.is_preset}
                  <span class="text-[9px] px-1 py-0.2 rounded bg-black/20 font-mono">PRESET</span>
                {/if}
              </button>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Custom User Data Accordion -->
      <details class="text-xs">
        <summary
          class="cursor-pointer text-muted-foreground hover:text-foreground font-medium select-none py-1"
        >
          {t('snippets.customUserDataToggle')}
        </summary>
        <div class="mt-2 space-y-1">
          <textarea
            bind:value={customUserData}
            rows="5"
            class="w-full rounded-md border border-input bg-background p-2 text-xs font-mono placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
            placeholder={t('vmCreate.customUserDataPlaceholder')}
          ></textarea>
          <p class="text-[10px] text-muted-foreground">
            {t('vmCreate.customUserDataHint')}
          </p>
        </div>
      </details>
    </div>
  </div>

  {#if error}
    <p class="text-xs text-destructive mt-1 font-medium">{error}</p>
  {/if}
</div>
