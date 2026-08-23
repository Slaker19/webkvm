<script>
  import Spinner from '$lib/components/Spinner.svelte';
  import { auth, api, passwordStrength } from '../lib/stores/auth.svelte.js';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { navigate } from '../lib/router.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { t } from '../lib/i18n.svelte.js';

  let me = $state(null);
  let loading = $state(true);
  let saving = $state(false);

  let oldPassword = $state('');
  let newPassword = $state('');
  let confirmPassword = $state('');
  let showOld = $state(false);
  let showNew = $state(false);
  let showConfirm = $state(false);

  // --- API tokens ---
  let tokens = $state([]);
  let showTokenDialog = $state(false);
  let newTokenName = $state('');
  let newTokenTtl = $state(720); // 30 days in hours
  let newTokenPlain = $state(''); // shown ONCE after creation
  let newTokenSaving = $state(false);
  let confirmRevoke = $state(null);
  let confirmDelete = $state(null);

  const strength = $derived(passwordStrength(newPassword));
  const passwordsMatch = $derived(
    newPassword === '' || confirmPassword === '' || newPassword === confirmPassword
  );
  const canSubmit = $derived(
    oldPassword.length > 0 && newPassword.length >= 8 && newPassword === confirmPassword
  );

  async function load() {
    loading = true;
    try {
      me = await api.me();
      if (!me.must_change_password) {
        auth.setMustChange(false);
      }
      await loadTokens();
    } catch (e) {
      toast.error(e.message || t('account.loadFailed'));
    } finally {
      loading = false;
    }
  }

  async function loadTokens() {
    try {
      const r = await api.listTokens(false);
      tokens = r.tokens || [];
    } catch {
      // tokens may be disabled — silently ignore
      tokens = [];
    }
  }

  async function createToken() {
    if (!newTokenName.trim()) {
      toast.error(t('account.nameRequired'));
      return;
    }
    newTokenSaving = true;
    try {
      const r = await api.createToken(newTokenName.trim(), newTokenTtl);
      newTokenPlain = r.plain;
      newTokenName = '';
      await loadTokens();
    } catch (e) {
      toast.error(e.message || t('account.createTokenFailed'));
    } finally {
      newTokenSaving = false;
    }
  }

  async function revokeToken(token) {
    confirmRevoke = token;
  }

  async function doRevoke() {
    const token = confirmRevoke;
    confirmRevoke = null;
    try {
      await api.revokeToken(token.id);
      toast.success(t('account.tokenRevoked'));
      await loadTokens();
    } catch (e) {
      toast.error(e.message || t('account.revokeFailed'));
    }
  }

  async function deleteToken(token) {
    confirmDelete = token;
  }

  async function doDelete() {
    const token = confirmDelete;
    confirmDelete = null;
    try {
      await api.deleteToken(token.id);
      toast.success(t('account.tokenDeleted'));
      await loadTokens();
    } catch (e) {
      toast.error(e.message || t('account.deleteFailed'));
    }
  }

  function copyToken() {
    navigator.clipboard.writeText(newTokenPlain).then(
      () => toast.success(t('account.tokenCopied')),
      () => toast.error(t('account.copyFailed'))
    );
  }

  function fmtDate(iso) {
    if (!iso) return '—';
    return new Date(iso).toLocaleString();
  }

  async function changePassword(e) {
    e.preventDefault();
    if (!canSubmit) return;
    saving = true;
    try {
      await api.changeMyPassword(oldPassword, newPassword);
      toast.success(t('account.passwordChanged'));
      oldPassword = '';
      newPassword = '';
      confirmPassword = '';
      auth.setMustChange(false);
      await load();
    } catch (e) {
      toast.error(e.message || t('account.changePasswordFailed'));
    } finally {
      saving = false;
    }
  }

  async function doLogout() {
    try {
      await api.logoutApi();
    } catch (_) {
      // ignore network errors; we still want to clear locally
    }
    auth.logout();
    navigate('/vms'); // re-evaluated by App.svelte to render Login
  }

  $effect(() => {
    if (auth.token) load();
  });
</script>

<div class="p-6 max-w-2xl">
  <div class="mb-6">
    <h1 class="text-xl font-semibold tracking-tight">{t('account.title')}</h1>
    <p class="text-sm text-muted-foreground mt-1">{t('account.subtitle')}</p>
  </div>

  {#if loading}
    <div class="flex items-center gap-2 text-sm text-muted-foreground">
      <Spinner size="sm" />
      {t('account.loading')}
    </div>
  {:else if me}
    {#if me.must_change_password}
      <div role="alert" class="mb-6 p-4 border border-warning/40 bg-warning/10 rounded-lg text-sm">
        <div class="flex items-start gap-3">
          <svg
            class="w-5 h-5 mt-0.5 text-warning shrink-0"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
            viewBox="0 0 24 24"
          >
            <path
              d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"
            />
            <line x1="12" y1="9" x2="12" y2="13" />
            <line x1="12" y1="17" x2="12.01" y2="17" />
          </svg>
          <div>
            <strong class="font-semibold">{t('account.mustChangePassword')}</strong>
            <p class="text-muted-foreground mt-1">{t('account.mustChangeDesc')}</p>
          </div>
        </div>
      </div>
    {/if}

    <!-- Profile -->
    <section class="mb-8 border border-border rounded-lg bg-card p-5">
      <h2 class="text-sm font-semibold mb-4">{t('account.profile')}</h2>
      <dl class="grid grid-cols-[160px_1fr] gap-y-2 text-sm">
        <dt class="text-muted-foreground">{t('account.username')}</dt>
        <dd class="font-mono">{me.username}</dd>

        <dt class="text-muted-foreground">{t('account.role')}</dt>
        <dd>
          <span
            class="px-2 py-0.5 rounded text-xs font-medium
						{me.role === 'admin'
              ? 'bg-accent/20 text-accent'
              : me.role === 'operator'
                ? 'bg-info/20 text-info'
                : 'bg-muted text-muted-foreground'}"
          >
            {me.role}
          </span>
        </dd>

        <dt class="text-muted-foreground">{t('account.email')}</dt>
        <dd class="text-muted-foreground">{me.email || '—'}</dd>

        <dt class="text-muted-foreground">{t('account.createdAt')}</dt>
        <dd class="text-muted-foreground">{me.created_at || '—'}</dd>

        <dt class="text-muted-foreground">{t('account.lastLogin')}</dt>
        <dd class="text-muted-foreground">{me.last_login_at || '—'}</dd>
      </dl>
    </section>

    <!-- Change password -->
    <section class="mb-8 border border-border rounded-lg bg-card p-5">
      <h2 class="text-sm font-semibold mb-4">{t('account.changePassword')}</h2>
      <form onsubmit={changePassword} class="space-y-4">
        <div>
          <label for="old-pw" class="block text-sm font-medium mb-1.5"
            >{t('account.oldPassword')}</label
          >
          <div class="relative">
            <input
              id="old-pw"
              bind:value={oldPassword}
              type={showOld ? 'text' : 'password'}
              required
              autocomplete="current-password"
              class="input pr-10"
            />
            <button
              type="button"
              onclick={() => (showOld = !showOld)}
              class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground p-1"
              aria-label={showOld ? t('login.hidePassword') : t('login.showPassword')}
            >
              {#if showOld}
                <svg
                  class="w-4 h-4"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  viewBox="0 0 24 24"
                  ><path
                    d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"
                  /><line x1="1" y1="1" x2="23" y2="23" /></svg
                >
              {:else}
                <svg
                  class="w-4 h-4"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  viewBox="0 0 24 24"
                  ><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" /><circle
                    cx="12"
                    cy="12"
                    r="3"
                  /></svg
                >
              {/if}
            </button>
          </div>
        </div>

        <div>
          <label for="new-pw" class="block text-sm font-medium mb-1.5"
            >{t('account.newPassword')}</label
          >
          <div class="relative">
            <input
              id="new-pw"
              bind:value={newPassword}
              type={showNew ? 'text' : 'password'}
              required
              minlength="8"
              autocomplete="new-password"
              class="input pr-10"
            />
            <button
              type="button"
              onclick={() => (showNew = !showNew)}
              class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground p-1"
              aria-label={showNew ? t('login.hidePassword') : t('login.showPassword')}
            >
              {#if showNew}
                <svg
                  class="w-4 h-4"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  viewBox="0 0 24 24"
                  ><path
                    d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"
                  /><line x1="1" y1="1" x2="23" y2="23" /></svg
                >
              {:else}
                <svg
                  class="w-4 h-4"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  viewBox="0 0 24 24"
                  ><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" /><circle
                    cx="12"
                    cy="12"
                    r="3"
                  /></svg
                >
              {/if}
            </button>
          </div>
          {#if newPassword}
            <div class="mt-2 flex items-center gap-2 text-xs">
              <div class="flex-1 h-1.5 rounded bg-muted overflow-hidden">
                <div
                  class="h-full transition-all {strength.color}"
                  style="width: {(strength.score + 1) * 20}%"
                ></div>
              </div>
              <span class="text-muted-foreground w-16 text-right">{strength.label}</span>
            </div>
          {/if}
        </div>

        <div>
          <label for="confirm-pw" class="block text-sm font-medium mb-1.5"
            >{t('account.confirmPassword')}</label
          >
          <div class="relative">
            <input
              id="confirm-pw"
              bind:value={confirmPassword}
              type={showConfirm ? 'text' : 'password'}
              required
              minlength="8"
              autocomplete="new-password"
              class="input pr-10"
            />
            <button
              type="button"
              onclick={() => (showConfirm = !showConfirm)}
              class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground p-1"
              aria-label={showConfirm ? t('login.hidePassword') : t('login.showPassword')}
            >
              {#if showConfirm}
                <svg
                  class="w-4 h-4"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  viewBox="0 0 24 24"
                  ><path
                    d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"
                  /><line x1="1" y1="1" x2="23" y2="23" /></svg
                >
              {:else}
                <svg
                  class="w-4 h-4"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                  viewBox="0 0 24 24"
                  ><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z" /><circle
                    cx="12"
                    cy="12"
                    r="3"
                  /></svg
                >
              {/if}
            </button>
          </div>
          {#if confirmPassword && !passwordsMatch}
            <p class="mt-1 text-xs text-destructive">{t('account.passwordsDoNotMatch')}</p>
          {/if}
        </div>

        <Button type="submit" disabled={!canSubmit || saving}>
          {saving ? t('account.changing') : t('account.changePassword')}
        </Button>
      </form>
    </section>

    <!-- API tokens -->
    <section class="mb-8 border border-border rounded-lg bg-card p-5">
      <div class="flex items-center justify-between mb-2">
        <h2 class="text-sm font-semibold">{t('account.apiTokens')}</h2>
        <Button size="sm" onclick={() => (showTokenDialog = true)}>{t('account.newToken')}</Button>
      </div>
      <p class="text-sm text-muted-foreground mb-4">
        {@html t('account.tokenHelp', {
          code: '<code class="text-xs bg-muted px-1 py-0.5 rounded">Authorization: Bearer wvmb_…</code>',
        })}
      </p>
      {#if tokens.length === 0}
        <p class="text-sm text-muted-foreground">{t('account.noTokensYet')}</p>
      {:else}
        <div class="divide-y divide-border">
          {#each tokens as tok (tok.id)}
            <div class="flex items-center gap-3 py-2.5">
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2">
                  <span class="font-medium text-sm truncate">{tok.name}</span>
                  {#if tok.revoked}
                    <span
                      class="text-[10px] px-1.5 py-0.5 rounded bg-destructive/10 text-destructive"
                      >{t('account.revoked')}</span
                    >
                  {:else if new Date(tok.expires_at) < new Date()}
                    <span class="text-[10px] px-1.5 py-0.5 rounded bg-warning/10 text-warning"
                      >{t('account.expired')}</span
                    >
                  {/if}
                </div>
                <div class="text-xs text-muted-foreground font-mono mt-0.5">
                  {tok.prefix}…
                </div>
                <div class="text-xs text-muted-foreground mt-0.5">
                  {t('account.createdExpires', {
                    created: fmtDate(tok.created_at),
                    expires: fmtDate(tok.expires_at),
                  })}
                  {#if tok.last_used_at}{t('account.lastUsed', {
                      used: fmtDate(tok.last_used_at),
                    })}{/if}
                </div>
              </div>
              <div class="flex gap-1">
                {#if !tok.revoked}
                  <Button size="xs" variant="outline" onclick={() => revokeToken(tok)}
                    >{t('account.revoke')}</Button
                  >
                {/if}
                <Button size="xs" variant="destructive" onclick={() => deleteToken(tok)}>
                  {t('account.deleteToken')}
                </Button>
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </section>

    <!-- Logout -->
    <section class="mb-8 border border-border rounded-lg bg-card p-5">
      <h2 class="text-sm font-semibold mb-2">{t('account.session')}</h2>
      <p class="text-sm text-muted-foreground mb-4">{t('account.sessionDesc')}</p>
      <Button variant="outline" onclick={doLogout}>{t('nav.logout')}</Button>
    </section>
  {:else}
    <p class="text-sm text-muted-foreground">{t('account.couldNotLoad')}</p>
  {/if}
</div>

<!-- Create token dialog -->
<ConfirmDialog
  open={showTokenDialog}
  title={t('account.createApiToken')}
  message=""
  confirmLabel={newTokenPlain ? t('account.done') : t('common.create')}
  hideCancel={!!newTokenPlain}
  onConfirm={() => {
    if (newTokenPlain) {
      showTokenDialog = false;
      newTokenPlain = '';
    } else {
      createToken();
    }
  }}
  onCancel={() => {
    showTokenDialog = false;
    newTokenPlain = '';
  }}
>
  {#if newTokenPlain}
    <div class="space-y-3">
      <p class="text-sm text-muted-foreground">{t('account.copyTokenNow')}</p>
      <div class="flex items-center gap-2">
        <Input value={newTokenPlain} readonly class="font-mono text-xs" />
        <Button onclick={copyToken} size="sm">{t('common.copy')}</Button>
      </div>
    </div>
  {:else}
    <div class="space-y-3">
      <div>
        <div class="text-sm font-medium block mb-1">{t('account.tokenName')}</div>
        <Input bind:value={newTokenName} placeholder="e.g. ci-deploy, monitoring" />
      </div>
      <div>
        <div class="text-sm font-medium block mb-1">{t('account.expiresInHours')}</div>
        <Input type="number" bind:value={newTokenTtl} min="1" max="8760" />
        <p class="text-xs text-muted-foreground mt-1">{t('account.ttlDefault')}</p>
      </div>
      {#if newTokenSaving}
        <p class="text-sm text-muted-foreground">{t('account.creating')}</p>
      {/if}
    </div>
  {/if}
</ConfirmDialog>

<ConfirmDialog
  open={!!confirmRevoke}
  title={t('account.revokeTokenTitle')}
  message={confirmRevoke ? t('account.revokeTokenMsg', { name: confirmRevoke.name }) : ''}
  confirmLabel={t('account.revoke')}
  onConfirm={doRevoke}
  onCancel={() => (confirmRevoke = null)}
/>

<ConfirmDialog
  open={!!confirmDelete}
  title={t('account.deleteTokenTitle')}
  message={confirmDelete ? t('account.deleteTokenMsg', { name: confirmDelete.name }) : ''}
  confirmLabel={t('account.deleteToken')}
  onConfirm={doDelete}
  onCancel={() => (confirmDelete = null)}
/>
