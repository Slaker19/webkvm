<script>
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import * as Dialog from '$lib/components/ui/dialog';
  import Icon from '$lib/components/Icon.svelte';
  import QuotaBar from '$lib/components/QuotaBar.svelte';
  import { passwordStrength } from '$lib/stores/auth.svelte.js';
  import { t } from '../i18n.svelte.js';
  import { usableForDisks } from '$lib/purpose.js';
  import {
    ROLES,
    capabilitiesForRole,
    effectiveCapabilities,
    buildPermissionsPayload,
    applyPreset,
    matchPreset,
    validateUsername,
    validatePassword,
    limitBelowUsage,
  } from '$lib/utils/permissions.js';

  /**
   * One dialog for both creating and editing a user.
   *
   * The two flows were previously separate blocks of markup with their
   * own state variables and their own defaults, which is how the edit
   * form came to show viewers a console permission they do not have
   * while the create form defaulted every capability to true regardless
   * of role. Sharing the component makes divergence impossible.
   */
  let {
    open = false,
    mode = 'create', // 'create' | 'edit'
    user = null, // the user being edited, for mode='edit'
    pools = [],
    networks = [],
    groups = [],
    tags = [],
    usage = null, // live quota consumption, for mode='edit'
    saving = false,
    existingUsernames = [],
    onsubmit,
    onclose,
  } = $props();

  let tab = $state('general');

  let username = $state('');
  let password = $state('');
  let passwordConfirm = $state('');
  let role = $state('operator');
  let email = $state('');
  let active = $state(true);
  let mustChangePassword = $state(true);

  let caps = $state({});
  let allowedPools = $state([]);
  let allowedNetworks = $state([]);
  let allowedGroups = $state([]);
  let allowedTags = $state([]);

  let qMaxVMs = $state(0);
  let qMaxVCPUs = $state(0);
  let qMaxRAMMB = $state(0);
  let qMaxDiskGB = $state(0);
  let qPoolRows = $state([{ pool: '', gb: 0 }]);

  const isCreate = $derived(mode === 'create');
  const isAdmin = $derived(role === 'admin');
  const availableCaps = $derived(capabilitiesForRole(role));
  const activePreset = $derived(matchPreset(role, caps));
  const strength = $derived(passwordStrength(password));
  const poolNames = $derived(pools.map((p) => p.name));
  const quotaPools = $derived(pools.filter((p) => usableForDisks(p)).map((p) => p.name));

  // Re-seed the form whenever the dialog opens, or the target changes.
  let lastKey = $state(null);
  $effect(() => {
    const key = open ? `${mode}:${user?.username ?? ''}` : null;
    if (key === lastKey) return;
    lastKey = key;
    if (open) reset();
  });

  function reset() {
    tab = 'general';
    password = '';
    passwordConfirm = '';
    if (isCreate) {
      username = '';
      role = 'operator';
      email = '';
      active = true;
      mustChangePassword = true;
      caps = applyPreset('operator', 'full');
      allowedPools = [];
      allowedNetworks = [];
      allowedGroups = [];
      allowedTags = [];
      qMaxVMs = 0;
      qMaxVCPUs = 0;
      qMaxRAMMB = 0;
      qMaxDiskGB = 0;
      qPoolRows = [{ pool: '', gb: 0 }];
      return;
    }
    username = user?.username ?? '';
    role = user?.role ?? 'operator';
    email = user?.email ?? '';
    active = user?.active !== false;
    mustChangePassword = false;
    caps = effectiveCapabilities(user);
    allowedPools = [...(user?.allowed_pools ?? [])];
    allowedNetworks = [...(user?.allowed_networks ?? [])];
    // Groups and tags are separate grants and are edited separately.
    // The old form bound tags to the group checkboxes, so opening this
    // dialog and pressing Save replaced a user's tags with their groups.
    allowedGroups = [...(user?.allowed_groups ?? [])];
    allowedTags = [...(user?.allowed_tags ?? [])];
    qMaxVMs = user?.quota?.max_vms ?? 0;
    qMaxVCPUs = user?.quota?.max_vcpus ?? 0;
    qMaxRAMMB = user?.quota?.max_ram_mb ?? 0;
    qMaxDiskGB = user?.quota?.max_disk_gb ?? 0;
    const pq = Object.entries(user?.quota?.pool_quotas ?? {}).map(([pool, gb]) => ({ pool, gb }));
    qPoolRows = pq.length ? pq : [{ pool: '', gb: 0 }];
  }

  // Dropping capabilities that no longer apply keeps the summary honest
  // when the role changes mid-edit.
  function onRoleChange() {
    const next = {};
    for (const cap of capabilitiesForRole(role)) next[cap.key] = caps[cap.key] ?? true;
    caps = next;
  }

  const usernameError = $derived.by(() => {
    if (!isCreate) return null;
    if (!username) return null;
    const err = validateUsername(username);
    if (err) return t(`users.usernameError.${err}`);
    if (existingUsernames.includes(username)) return t('users.usernameError.taken');
    return null;
  });

  const passwordError = $derived.by(() => {
    if (!password) return isCreate ? null : null;
    const err = validatePassword(password);
    return err ? t(`users.passwordError.${err}`) : null;
  });

  const confirmError = $derived.by(() => {
    if (!password || !passwordConfirm) return null;
    return password === passwordConfirm ? null : t('users.passwordMismatch');
  });

  // A single reason the form cannot be submitted, so the button is never
  // just greyed out with no explanation.
  const blockedReason = $derived.by(() => {
    if (isCreate) {
      if (!username) return t('users.blocked.noUsername');
      if (usernameError) return usernameError;
      if (!password) return t('users.blocked.noPassword');
    }
    if (passwordError) return passwordError;
    if (password && !passwordConfirm) return t('users.blocked.noConfirm');
    if (confirmError) return confirmError;
    return null;
  });

  const quotaWarnings = $derived.by(() => {
    if (!usage) return [];
    const out = [];
    const dims = [
      ['users.quotaVMs', usage.vms?.used ?? 0, qMaxVMs],
      ['users.quotaVCPUs', usage.vcpus?.used ?? 0, qMaxVCPUs],
      ['users.quotaRAMMB', usage.ram_mb?.used ?? 0, qMaxRAMMB],
      ['users.quotaDiskGB', usage.disk_gb?.used ?? 0, qMaxDiskGB],
    ];
    for (const [key, used, limit] of dims) {
      if (limitBelowUsage(used, Number(limit))) out.push(t(key));
    }
    return out;
  });

  /** Plain-language summary of what this user will be able to do. */
  const summary = $derived.by(() => {
    if (isAdmin) return { admin: true, can: [], cannot: [], scope: [] };
    const can = [];
    const cannot = [];
    for (const cap of availableCaps) {
      (caps[cap.key] ? can : cannot).push(t(`users.perm.${cap.key}.title`));
    }
    const scope = [];
    if (allowedPools.length) scope.push(t('users.summaryPools', { list: allowedPools.join(', ') }));
    if (allowedNetworks.length)
      scope.push(t('users.summaryNetworks', { list: allowedNetworks.join(', ') }));
    if (allowedGroups.length)
      scope.push(t('users.summaryGroups', { list: allowedGroups.join(', ') }));
    if (allowedTags.length) scope.push(t('users.summaryTags', { list: allowedTags.join(', ') }));
    // Quotas belong in the summary too: they cap what the permissions
    // above actually let the user consume. 0 means unlimited.
    const limit = (v, unit = '') =>
      Number(v) > 0 ? `${Number(v)}${unit ? ` ${unit}` : ''}` : t('users.quotaUnlimited');
    const quotas = [
      [t('users.quotaVMs'), limit(qMaxVMs)],
      [t('users.quotaVCPUs'), limit(qMaxVCPUs)],
      [t('users.quotaRAMMB'), limit(qMaxRAMMB)],
      [t('users.quotaDiskGB'), limit(qMaxDiskGB)],
    ];
    const poolQuotas = qPoolRows
      .filter((r) => r.pool && Number(r.gb) > 0)
      .map((r) => [r.pool, `${Number(r.gb)} GB`]);
    return { admin: false, can, cannot, scope, quotas, poolQuotas };
  });

  function addPoolRow() {
    qPoolRows = [...qPoolRows, { pool: '', gb: 0 }];
  }
  function removePoolRow(i) {
    qPoolRows = qPoolRows.filter((_, idx) => idx !== i);
  }

  function submit() {
    if (saving || blockedReason) return;
    const poolQuotas = {};
    for (const row of qPoolRows) {
      if (row.pool && Number(row.gb) > 0) poolQuotas[row.pool] = Number(row.gb);
    }
    const payload = {
      role,
      email,
      quota: {
        max_vms: Number(qMaxVMs) || 0,
        max_vcpus: Number(qMaxVCPUs) || 0,
        max_ram_mb: Number(qMaxRAMMB) || 0,
        max_disk_gb: Number(qMaxDiskGB) || 0,
        pool_quotas: poolQuotas,
      },
      allowed_pools: allowedPools,
      allowed_networks: allowedNetworks,
      allowed_groups: allowedGroups,
      allowed_tags: allowedTags,
    };
    const permissions = buildPermissionsPayload(role, caps);
    if (permissions) payload.permissions = permissions;
    if (isCreate) {
      payload.username = username;
      payload.password = password;
      payload.must_change_password = mustChangePassword;
    } else {
      payload.active = active;
      if (password) payload.password = password;
    }
    onsubmit?.(payload);
  }

  const TABS = [
    { id: 'general', icon: 'users' },
    { id: 'permissions', icon: 'shield' },
    { id: 'scope', icon: 'network' },
    { id: 'quota', icon: 'gauge' },
    { id: 'summary', icon: 'check' },
  ];
</script>

<Dialog.Root {open} onOpenChange={(v) => !v && onclose?.()}>
  <Dialog.Content class="sm:max-w-2xl">
    <Dialog.Header>
      <Dialog.Title>
        {isCreate ? t('users.newUser') : t('users.editUser', { name: username })}
      </Dialog.Title>
    </Dialog.Header>

    <div class="flex items-center gap-1 border-b border-border -mx-4 px-4 overflow-x-auto">
      {#each TABS as tb (tb.id)}
        <button
          type="button"
          onclick={() => (tab = tb.id)}
          class="flex items-center gap-1.5 px-3 py-2 text-xs font-medium border-b-2 transition-colors -mb-px whitespace-nowrap {tab ===
          tb.id
            ? 'border-accent text-accent'
            : 'border-transparent text-muted-foreground hover:text-foreground'}"
        >
          <Icon name={tb.icon} size={13} />
          {t(`users.tab.${tb.id}`)}
        </button>
      {/each}
    </div>

    <div class="space-y-3 text-left pt-3 min-h-[320px] max-h-[60vh] overflow-y-auto pr-1">
      {#if tab === 'general'}
        <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-3 gap-y-3 items-start">
          <div class="flex flex-col gap-1">
            <label
              for="uf-username"
              class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >
              {t('users.username')}
            </label>
            <Input
              id="uf-username"
              bind:value={username}
              disabled={!isCreate}
              placeholder={t('users.username')}
              class="!py-1 !text-xs"
              autocomplete="off"
            />
            {#if usernameError}
              <p class="text-[10px] text-destructive">{usernameError}</p>
            {:else if isCreate}
              <p class="text-[10px] text-muted-foreground">{t('users.usernameHint')}</p>
            {/if}
          </div>

          <div class="flex flex-col gap-1">
            <label for="uf-email" class="text-[10px] text-muted-foreground uppercase tracking-wide">
              {t('users.email')}
            </label>
            <Input id="uf-email" bind:value={email} class="!py-1 !text-xs" autocomplete="off" />
          </div>

          <div class="flex flex-col gap-1">
            <label
              for="uf-password"
              class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >
              {isCreate ? t('users.password') : t('users.newPasswordOptional')}
            </label>
            <Input
              id="uf-password"
              type="password"
              bind:value={password}
              class="!py-1 !text-xs"
              autocomplete="new-password"
            />
            {#if passwordError}
              <p class="text-[10px] text-destructive">{passwordError}</p>
            {:else if password}
              <div class="flex items-center gap-1.5">
                <div class="h-1 flex-1 rounded-full bg-muted overflow-hidden">
                  <div
                    class="h-full rounded-full transition-all {strength.score >= 3
                      ? 'bg-emerald-500'
                      : strength.score === 2
                        ? 'bg-amber-500'
                        : 'bg-destructive'}"
                    style="width: {(strength.score / 4) * 100}%"
                  ></div>
                </div>
              </div>
            {:else}
              <p class="text-[10px] text-muted-foreground">{t('users.passwordRules')}</p>
            {/if}
          </div>

          <div class="flex flex-col gap-1">
            <label
              for="uf-password2"
              class="text-[10px] text-muted-foreground uppercase tracking-wide"
            >
              {t('users.confirmPassword')}
            </label>
            <Input
              id="uf-password2"
              type="password"
              bind:value={passwordConfirm}
              class="!py-1 !text-xs"
              autocomplete="new-password"
            />
            {#if confirmError}
              <p class="text-[10px] text-destructive">{confirmError}</p>
            {/if}
          </div>

          <div class="flex flex-col gap-1 sm:col-span-2">
            <span class="text-[10px] text-muted-foreground uppercase tracking-wide">
              {t('users.role')}
            </span>
            <div class="grid grid-cols-1 sm:grid-cols-3 gap-2">
              {#each ROLES as r (r)}
                <button
                  type="button"
                  onclick={() => {
                    role = r;
                    onRoleChange();
                  }}
                  class="text-left rounded-lg border p-2 transition-colors {role === r
                    ? 'border-accent bg-accent/5'
                    : 'border-border hover:bg-muted/50'}"
                >
                  <span class="block text-xs font-medium">{t(`users.roleInfo.${r}.title`)}</span>
                  <span class="block text-[10px] text-muted-foreground mt-0.5">
                    {t(`users.roleInfo.${r}.desc`)}
                  </span>
                </button>
              {/each}
            </div>
          </div>

          {#if isCreate}
            <label class="flex items-center gap-2 text-xs text-muted-foreground sm:col-span-2">
              <input type="checkbox" bind:checked={mustChangePassword} class="rounded" />
              {t('users.mustChangePassword')}
            </label>
          {:else}
            <label class="flex items-center gap-2 text-xs text-muted-foreground sm:col-span-2">
              <input type="checkbox" bind:checked={active} class="rounded" />
              {t('users.activeAccount')}
            </label>
          {/if}
        </div>
      {:else if tab === 'permissions'}
        {#if isAdmin}
          <div class="rounded-xl border border-accent/30 bg-accent/5 p-4 flex items-start gap-3">
            <Icon name="shield" size={18} class="text-accent shrink-0 mt-0.5" />
            <p class="text-xs text-muted-foreground">{t('users.adminFullAccessNotice')}</p>
          </div>
        {:else}
          <div class="flex items-center gap-1.5 flex-wrap">
            <span class="text-[10px] text-muted-foreground uppercase tracking-wide mr-1">
              {t('users.presets')}:
            </span>
            {#each ['full', 'support', 'readonly'] as preset (preset)}
              <button
                type="button"
                onclick={() => (caps = applyPreset(role, preset))}
                class="text-[10px] px-2 py-0.5 rounded-full border transition-colors {activePreset ===
                preset
                  ? 'border-accent bg-accent/10 text-accent'
                  : 'border-border hover:bg-muted'}"
              >
                {t(`users.preset.${preset}`)}
              </button>
            {/each}
            {#if activePreset === null}
              <span class="text-[10px] text-muted-foreground">{t('users.presetCustom')}</span>
            {/if}
          </div>

          <div class="space-y-1.5">
            {#each availableCaps as cap (cap.key)}
              <label
                class="flex items-start gap-2.5 rounded-lg border border-border p-2 cursor-pointer hover:bg-muted/40 transition-colors"
              >
                <input
                  type="checkbox"
                  checked={!!caps[cap.key]}
                  onchange={(e) => (caps = { ...caps, [cap.key]: e.currentTarget.checked })}
                  class="rounded mt-0.5"
                />
                <Icon name={cap.icon} size={14} class="text-muted-foreground shrink-0 mt-0.5" />
                <span class="min-w-0">
                  <span class="block text-xs font-medium">{t(`users.perm.${cap.key}.title`)}</span>
                  <span class="block text-[10px] text-muted-foreground">
                    {t(`users.perm.${cap.key}.desc`)}
                  </span>
                </span>
              </label>
            {/each}
          </div>

          {#if role === 'viewer'}
            <p class="text-[10px] text-muted-foreground">{t('users.viewerCapsNotice')}</p>
          {/if}
        {/if}
      {:else if tab === 'scope'}
        {#if isAdmin}
          <div class="rounded-xl border border-accent/30 bg-accent/5 p-4 flex items-start gap-3">
            <Icon name="shield" size={18} class="text-accent shrink-0 mt-0.5" />
            <p class="text-xs text-muted-foreground">{t('users.adminScopeNotice')}</p>
          </div>
        {:else}
          {@render checkboxGroup(
            t('users.allowedPoolsTitle'),
            t('users.allowedPoolsHint'),
            poolNames,
            allowedPools,
            (v) => (allowedPools = v),
            t('users.allowedPoolsLoading')
          )}
          {@render checkboxGroup(
            t('users.allowedNetworksTitle'),
            t('users.allowedNetworksHint'),
            networks,
            allowedNetworks,
            (v) => (allowedNetworks = v),
            t('users.allowedNetworksLoading')
          )}
          {@render checkboxGroup(
            t('users.allowedGroupsTitle'),
            t('users.allowedGroupsHint'),
            groups,
            allowedGroups,
            (v) => (allowedGroups = v),
            t('users.allowedGroupsEmpty')
          )}
          {@render checkboxGroup(
            t('users.allowedTagsTitle'),
            t('users.allowedTagsHint'),
            tags,
            allowedTags,
            (v) => (allowedTags = v),
            t('users.allowedTagsEmpty')
          )}
        {/if}
      {:else if tab === 'quota'}
        {#if isAdmin}
          <div class="rounded-xl border border-accent/30 bg-accent/5 p-4 flex items-start gap-3">
            <Icon name="shield" size={18} class="text-accent shrink-0 mt-0.5" />
            <p class="text-xs text-muted-foreground">{t('users.adminQuotaNotice')}</p>
          </div>
        {:else}
          {#if usage}
            <div class="rounded-lg border border-border p-3 space-y-2.5">
              <div class="text-[10px] text-muted-foreground uppercase tracking-wide">
                {t('users.currentUsage')}
              </div>
              <QuotaBar
                label={t('users.quotaVMs')}
                used={usage.vms?.used ?? 0}
                limit={Number(qMaxVMs)}
              />
              <QuotaBar
                label={t('users.quotaVCPUs')}
                used={usage.vcpus?.used ?? 0}
                limit={Number(qMaxVCPUs)}
              />
              <QuotaBar
                label={t('users.quotaRAMMB')}
                used={usage.ram_mb?.used ?? 0}
                limit={Number(qMaxRAMMB)}
                unit="MB"
              />
              <QuotaBar
                label={t('users.quotaDiskGB')}
                used={usage.disk_gb?.used ?? 0}
                limit={Number(qMaxDiskGB)}
                unit="GB"
              />
            </div>
          {/if}

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-x-3 gap-y-2">
            {@render numberField(
              'uf-qvms',
              t('users.quotaVMs'),
              () => qMaxVMs,
              (v) => (qMaxVMs = v)
            )}
            {@render numberField(
              'uf-qvcpus',
              t('users.quotaVCPUs'),
              () => qMaxVCPUs,
              (v) => (qMaxVCPUs = v)
            )}
            {@render numberField(
              'uf-qram',
              t('users.quotaRAMMB'),
              () => qMaxRAMMB,
              (v) => (qMaxRAMMB = v)
            )}
            {@render numberField(
              'uf-qdisk',
              t('users.quotaDiskGB'),
              () => qMaxDiskGB,
              (v) => (qMaxDiskGB = v)
            )}
          </div>
          <p class="text-[10px] text-muted-foreground">{t('users.quotaHint')}</p>

          <div class="border-t border-border pt-2">
            <div class="text-[10px] text-muted-foreground uppercase tracking-wide mb-1">
              {t('users.poolQuotasTitle')}
            </div>
            {#each qPoolRows as row, i (i)}
              <div class="flex items-center gap-2 mb-1">
                <select
                  bind:value={row.pool}
                  class="input !py-1 !text-xs flex-1"
                  aria-label={t('users.poolQuotaPool')}
                >
                  <option value="">{t('users.poolQuotaPick')}</option>
                  {#each quotaPools as p (p)}
                    <option value={p}>{p}</option>
                  {/each}
                </select>
                <input
                  type="number"
                  min="0"
                  bind:value={row.gb}
                  class="input !py-1 !text-xs tnum w-24"
                  aria-label={t('users.poolQuotaGB')}
                />
                <button
                  type="button"
                  onclick={() => removePoolRow(i)}
                  class="text-muted-foreground hover:text-destructive transition-colors"
                  aria-label={t('common.delete')}
                >
                  <Icon name="trash" size={14} />
                </button>
              </div>
            {/each}
            <button
              type="button"
              onclick={addPoolRow}
              class="text-[10px] px-2 py-0.5 rounded-full border border-border hover:bg-muted transition-colors"
            >
              {t('users.poolQuotaAdd')}
            </button>
          </div>
        {/if}
      {:else if tab === 'summary'}
        {#if summary.admin}
          <div
            class="rounded-xl border border-amber-500/30 bg-amber-500/5 p-4 flex items-start gap-3"
          >
            <Icon name="shield" size={18} class="text-amber-500 shrink-0 mt-0.5" />
            <div>
              <p class="text-xs font-medium">{t('users.summaryAdminTitle')}</p>
              <p class="text-[11px] text-muted-foreground mt-1">{t('users.summaryAdminDesc')}</p>
            </div>
          </div>
        {:else}
          <div class="space-y-3">
            <div class="rounded-lg border border-border p-3">
              <div class="text-[10px] text-muted-foreground uppercase tracking-wide mb-1.5">
                {t('users.summaryCan')}
              </div>
              {#if summary.can.length}
                <ul class="space-y-0.5">
                  {#each summary.can as item (item)}
                    <li class="flex items-center gap-1.5 text-xs">
                      <Icon name="check" size={12} class="text-emerald-500 shrink-0" />
                      {item}
                    </li>
                  {/each}
                </ul>
              {:else}
                <p class="text-xs text-muted-foreground">{t('users.summaryNothing')}</p>
              {/if}
            </div>

            {#if summary.cannot.length}
              <div class="rounded-lg border border-border p-3">
                <div class="text-[10px] text-muted-foreground uppercase tracking-wide mb-1.5">
                  {t('users.summaryCannot')}
                </div>
                <ul class="space-y-0.5">
                  {#each summary.cannot as item (item)}
                    <li class="flex items-center gap-1.5 text-xs text-muted-foreground">
                      <Icon name="x" size={12} class="text-muted-foreground shrink-0" />
                      {item}
                    </li>
                  {/each}
                </ul>
              </div>
            {/if}

            <div class="rounded-lg border border-border p-3">
              <div class="text-[10px] text-muted-foreground uppercase tracking-wide mb-1.5">
                {t('users.summaryScope')}
              </div>
              {#if summary.scope.length}
                <ul class="space-y-0.5">
                  {#each summary.scope as item (item)}
                    <li class="text-xs text-muted-foreground">{item}</li>
                  {/each}
                </ul>
              {:else}
                <p class="text-xs text-muted-foreground">{t('users.summaryScopeAll')}</p>
              {/if}
            </div>

            <div class="rounded-lg border border-border p-3">
              <div class="text-[10px] text-muted-foreground uppercase tracking-wide mb-1.5">
                {t('users.summaryQuotas')}
              </div>
              <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs">
                {#each summary.quotas as [label, value] (label)}
                  <dt class="text-muted-foreground">{label}</dt>
                  <dd class="tnum">{value}</dd>
                {/each}
              </dl>
              {#if summary.poolQuotas.length}
                <div class="text-[10px] text-muted-foreground uppercase tracking-wide mt-2 mb-1">
                  {t('users.poolQuotasTitle')}
                </div>
                <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs">
                  {#each summary.poolQuotas as [pool, value], i (i)}
                    <dt class="text-muted-foreground font-mono">{pool}</dt>
                    <dd class="tnum">{value}</dd>
                  {/each}
                </dl>
              {/if}
            </div>

            {#if quotaWarnings.length}
              <div class="rounded-lg border border-destructive/30 bg-destructive/5 p-3">
                <p class="text-xs text-destructive">
                  {t('users.summaryQuotaWarning', { list: quotaWarnings.join(', ') })}
                </p>
              </div>
            {/if}

            {#if !isCreate}
              <p class="text-[10px] text-muted-foreground">{t('users.summarySessionNotice')}</p>
            {/if}
          </div>
        {/if}
      {/if}
    </div>

    <Dialog.Footer>
      <div class="flex-1 text-left">
        {#if blockedReason}
          <p class="text-[11px] text-destructive">{blockedReason}</p>
        {/if}
      </div>
      <Button variant="outline" onclick={() => onclose?.()}>{t('common.cancel')}</Button>
      <Button onclick={submit} disabled={saving || !!blockedReason}>
        {isCreate ? t('common.create') : t('common.save')}
      </Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

{#snippet checkboxGroup(title, hint, items, selected, onchange, emptyLabel)}
  <div class="border-t border-border pt-2 first:border-t-0 first:pt-0">
    <div class="text-[10px] text-muted-foreground uppercase tracking-wide mb-1">{title}</div>
    {#if items.length}
      <div class="flex flex-wrap gap-x-3 gap-y-1">
        {#each items as item (item)}
          <label class="flex items-center gap-1 text-xs text-muted-foreground">
            <input
              type="checkbox"
              checked={selected.includes(item)}
              onchange={(e) =>
                onchange(
                  e.currentTarget.checked ? [...selected, item] : selected.filter((x) => x !== item)
                )}
              class="rounded"
            />
            {item}
          </label>
        {/each}
      </div>
      <p class="text-[10px] text-muted-foreground mt-1">{hint}</p>
    {:else}
      <p class="text-[10px] text-muted-foreground">{emptyLabel}</p>
    {/if}
  </div>
{/snippet}

{#snippet numberField(id, label, get, set)}
  <div>
    <label for={id} class="text-[10px] text-muted-foreground uppercase tracking-wide">{label}</label
    >
    <input
      {id}
      type="number"
      min="0"
      value={get()}
      oninput={(e) => set(Number(e.currentTarget.value) || 0)}
      class="input !py-1 !text-xs tnum w-full"
    />
  </div>
{/snippet}
