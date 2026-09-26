<script>
  import { auth, api } from '../lib/stores/auth.svelte.js';
  import { navigate, getRoute } from '../lib/router.svelte.js';
  import { t } from '../lib/i18n.svelte.js';
  import { SITE_NAME } from '../lib/brand.js';
  import Icon from '$lib/components/Icon.svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import LanguageSelector from '$lib/components/LanguageSelector.svelte';

  let username = $state('');
  let password = $state('');
  let showPassword = $state(false);
  let error = $state('');
  let loading = $state(false);

  // 2FA state
  let mfaRequired = $state(false);
  let mfaToken = $state('');
  let totpCode = $state('');

  let sessionExpired = $derived(getRoute()?.query?.reason === 'session_expired');

  async function handleLogin(e) {
    e?.preventDefault();
    if (loading) return;
    error = '';
    loading = true;
    try {
      if (mfaRequired) {
        // Step 2: verify 2FA TOTP code or backup code
        const res = await api.login2FA(mfaToken, totpCode.trim());
        auth.setSession(res.username, res.role, res.must_change_password, res.csrf);
        if (res.must_change_password) {
          navigate('/account');
        } else {
          navigate('/vms');
        }
        return;
      }

      // Step 1: standard login
      const res = await api.login(username, password);
      if (res.mfa_required) {
        mfaRequired = true;
        mfaToken = res.mfa_token;
        totpCode = '';
        return;
      }

      auth.setSession(res.username, res.role, res.must_change_password, res.csrf);
      if (res.must_change_password) {
        navigate('/account');
      } else {
        navigate('/vms');
      }
    } catch (e) {
      error = loginErrorMessage(e);
    } finally {
      loading = false;
    }
  }

  // Map the backend's failures to the UI language. A 401 here is always
  // "what you typed is wrong" (the API layer no longer treats it as an
  // expired session); the raw server strings are English or Spanish
  // regardless of the selected language.
  function loginErrorMessage(e) {
    if (e?.status === 429) {
      const seconds = e.data?.retryAfter ?? e.retryAfter;
      return t('login.rateLimited', { seconds: seconds || '?' });
    }
    if (e?.status === 401 || e?.code === 'invalid_credentials') {
      if (!mfaRequired) return t('login.invalid');
      if (/mfa session/i.test(e.message || '')) return t('login.mfaExpired');
      return t('login.invalidCode');
    }
    return e?.message || t('login.invalid');
  }

  function resetToStep1() {
    mfaRequired = false;
    mfaToken = '';
    totpCode = '';
    error = '';
  }
</script>

<div class="min-h-screen flex items-center justify-center bg-background p-4 relative">
  <!-- Top-right Language Selector -->
  <div class="absolute top-4 right-4 sm:top-6 sm:right-6">
    <LanguageSelector
      side="bottom"
      align="end"
      class="border border-border/80 bg-card/80 backdrop-blur-xs px-2.5 py-1.5 shadow-xs hover:border-border"
    />
  </div>

  <div class="w-full max-w-sm animate-fade-in">
    <div
      class="border border-border rounded-xl p-8 bg-card shadow-lg shadow-black/20 transition-[border-color,box-shadow] duration-200 hover:border-border-hover"
    >
      <div class="text-center mb-8">
        <div class="w-12 h-12 rounded-xl overflow-hidden mx-auto mb-4 shadow-sm">
          <img src="/favicon.png" alt="WebKVM" class="w-full h-full object-cover" />
        </div>
        <h1 class="text-xl font-semibold tracking-tight">{SITE_NAME}</h1>
        <p class="text-muted-foreground text-sm mt-1">
          {mfaRequired ? t('login.twoFactorTitle') : t('brand.tagline')}
        </p>
      </div>

      {#if error}
        <div
          role="alert"
          aria-live="assertive"
          class="mb-4 p-3 border border-destructive/30 bg-destructive/10 rounded-md text-destructive text-sm"
        >
          <div class="flex items-center gap-2">
            <Icon name="error" size={16} class="shrink-0" />
            {error}
          </div>
        </div>
      {/if}
      {#if sessionExpired && !mfaRequired && !error}
        <div
          class="rounded-md border border-warning/30 bg-warning-subtle px-3 py-2 text-sm text-warning mb-4"
        >
          {t('login.sessionExpired')}
        </div>
      {/if}

      <form onsubmit={handleLogin} class="space-y-4">
        {#if !mfaRequired}
          <div>
            <label for="login-user" class="block text-sm font-medium mb-1.5"
              >{t('login.username')}</label
            >
            <Input
              id="login-user"
              bind:value={username}
              type="text"
              required
              placeholder="admin"
              autocomplete="username"
              autocapitalize="off"
              autocorrect="off"
              spellcheck="false"
            />
          </div>

          <div>
            <label for="login-pass" class="block text-sm font-medium mb-1.5"
              >{t('login.password')}</label
            >
            <div class="relative">
              <Input
                id="login-pass"
                bind:value={password}
                type={showPassword ? 'text' : 'password'}
                required
                placeholder="••••••••"
                class="pr-10"
                autocomplete="current-password"
              />
              <button
                type="button"
                onclick={() => (showPassword = !showPassword)}
                class="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground p-1 rounded"
                aria-label={showPassword ? t('login.hidePassword') : t('login.showPassword')}
                aria-pressed={showPassword}
              >
                <Icon name={showPassword ? 'eyeOff' : 'eye'} size={16} />
              </button>
            </div>
          </div>
        {:else}
          <!-- Step 2: 2FA TOTP Code -->
          <div class="space-y-3">
            <div
              class="p-3 bg-info-subtle border border-info/20 rounded-lg text-xs text-info flex items-start gap-2"
            >
              <Icon name="shield" size={16} class="shrink-0 mt-0.5" />
              <span>{t('login.totpDesc')}</span>
            </div>

            <div>
              <label for="totp-code" class="block text-sm font-medium mb-1.5"
                >{t('login.totpCode')}</label
              >
              <Input
                id="totp-code"
                bind:value={totpCode}
                type="text"
                required
                maxlength="10"
                placeholder="000 000"
                class="text-center text-lg tracking-widest font-mono font-semibold"
                autocomplete="one-time-code"
                autofocus
              />
            </div>
          </div>
        {/if}

        <Button type="submit" disabled={loading} class="w-full mt-2">
          {#if loading}
            <Icon name="spinner" size={16} class="animate-spin" />
            {t('login.signingIn')}
          {:else if mfaRequired}
            {t('login.verifyAndSignIn')}
          {:else}
            {t('login.signIn')}
          {/if}
        </Button>

        {#if mfaRequired}
          <button
            type="button"
            onclick={resetToStep1}
            class="w-full text-center text-xs text-muted-foreground hover:text-foreground transition-colors mt-2"
          >
            {t('login.backToSignIn')}
          </button>
        {/if}
      </form>
    </div>
  </div>
</div>
