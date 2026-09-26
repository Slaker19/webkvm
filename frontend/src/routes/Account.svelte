<script>
  import { onMount } from 'svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Icon from '$lib/components/Icon.svelte';
  import Avatar from '$lib/components/Avatar.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import { auth, api, passwordStrength } from '../lib/stores/auth.svelte.js';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Badge } from '$lib/components/ui/badge';
  import { navigate } from '../lib/router.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import * as Dialog from '$lib/components/ui/dialog';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import { t, htmlVar } from '../lib/i18n.svelte.js';
  import {
    theme,
    setTheme,
    setAccent,
    THEMES,
    ACCENTS,
    ACCENT_COLORS,
  } from '$lib/stores/theme.svelte.js';
  import { sidebarMode, setDesktopSidebarMode } from '$lib/stores/sidebarMode.svelte.js';
  import { gaugeStyle, setGaugeStyle } from '$lib/stores/gaugeStyle.svelte.js';
  import {
    terminalSettings,
    setTerminalTheme,
    setTerminalFontSize,
    setTerminalTuiBar,
    setTerminalFnKeys,
  } from '$lib/stores/terminalSettings.svelte.js';
  import { TERMINAL_THEMES } from '$lib/utils/terminalConfig.js';
  import Switch from '$lib/components/Switch.svelte';
  import QRCode from 'qrcode';
  import {
    Shield,
    ShieldCheck,
    ShieldAlert,
    Key,
    Sparkles,
    Mail,
    Calendar,
    Clock,
    LogOut,
    Check,
    Copy,
    Eye,
    EyeOff,
    Plus,
    Trash2,
    Ban,
    Sun,
    Moon,
    Lock,
    ChevronDown,
    ChevronUp,
    AlertTriangle,
    Palette,
    Globe,
    PanelLeft,
    Activity,
    Terminal,
    Image,
  } from '@lucide/svelte';
  import LanguageSelector from '$lib/components/LanguageSelector.svelte';

  let me = $state(null);
  let loading = $state(true);
  let saving = $state(false);
  let activeTab = $state('security'); // 'security' | 'appearance' | 'tokens'

  onMount(() => {
    load();
  });

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

  // --- 2FA / TOTP ---
  let show2FADialog = $state(false);
  let setup2FAData = $state(null);
  let qrCodeDataUrl = $state('');
  let generatingQr = $state(false);
  let showManualSecret = $state(false);
  let test2FACode = $state('');
  let enabling2FA = $state(false);
  let showDisable2FADialog = $state(false);
  let disable2FAPassword = $state('');
  let disabling2FA = $state(false);

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
      if (me) {
        if (me.avatar) {
          auth.setAvatar(me.avatar);
        }
        if (!me.must_change_password) {
          auth.setMustChange(false);
        } else {
          activeTab = 'security';
        }
      }
      await loadTokens();
    } catch (e) {
      toast.error(e.message || t('account.loadFailed'));
      if (!me) {
        me = {
          username: auth.user || 'admin',
          role: auth.role || 'admin',
          totp_enabled: false,
          active: true,
          must_change_password: false,
        };
      }
    } finally {
      loading = false;
    }
  }

  async function loadTokens() {
    try {
      const r = await api.listTokens(false);
      tokens = r.tokens || [];
    } catch {
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
    try {
      return new Date(iso).toLocaleString(undefined, {
        dateStyle: 'medium',
        timeStyle: 'short',
      });
    } catch {
      return iso;
    }
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
      // ignore network errors; clear locally
    }
    auth.logout();
    navigate('/vms');
  }

  async function start2FASetup() {
    try {
      setup2FAData = await api.setup2FA();
      test2FACode = '';
      qrCodeDataUrl = '';
      showManualSecret = false;
      if (setup2FAData && setup2FAData.otpauth_url) {
        generatingQr = true;
        try {
          qrCodeDataUrl = await QRCode.toDataURL(setup2FAData.otpauth_url, {
            width: 220,
            margin: 2,
            color: {
              dark: '#0f172a',
              light: '#ffffff',
            },
          });
        } catch (qrErr) {
          console.error('Error generando código QR:', qrErr);
        } finally {
          generatingQr = false;
        }
      }
      show2FADialog = true;
    } catch (e) {
      toast.error(e.message || t('account.start2FAFailed'));
    }
  }

  async function confirmEnable2FA() {
    if (!test2FACode.trim()) {
      toast.error(t('account.enter6DigitCode'));
      return;
    }
    enabling2FA = true;
    try {
      await api.enable2FA(setup2FAData.secret, test2FACode.trim(), setup2FAData.backup_codes);
      toast.success(t('account.totpEnabled'));
      show2FADialog = false;
      setup2FAData = null;
      test2FACode = '';
      await load();
    } catch (e) {
      toast.error(e.message || t('account.verify2FAFailed'));
    } finally {
      enabling2FA = false;
    }
  }

  async function confirmDisable2FA() {
    if (!disable2FAPassword) {
      toast.error(t('account.enterCurrentPassword'));
      return;
    }
    disabling2FA = true;
    try {
      await api.disable2FA(disable2FAPassword);
      toast.success(t('account.totpDisabled'));
      showDisable2FADialog = false;
      disable2FAPassword = '';
      await load();
    } catch (e) {
      toast.error(e.message || t('account.disable2FAFailed'));
    } finally {
      disabling2FA = false;
    }
  }
</script>

<div class="w-full max-w-5xl mx-auto p-4 sm:p-6 lg:p-8 space-y-6">
  <!-- Top Page Header -->
  <PageHeader title={t('account.title')} subtitle={t('account.subtitle')}>
    {#snippet actions()}
      <Button
        variant="outline"
        size="sm"
        onclick={doLogout}
        class="border-destructive/30 text-destructive hover:bg-destructive/10 hover:border-destructive/50 transition-colors"
      >
        <LogOut class="w-4 h-4 mr-1.5" />
        {t('nav.logout')}
      </Button>
    {/snippet}
  </PageHeader>

  {#if loading}
    <div
      class="flex flex-col items-center justify-center p-12 border border-border rounded-2xl bg-card/50"
    >
      <Spinner size="md" />
      <span class="text-sm text-muted-foreground mt-3">{t('account.loading')}</span>
    </div>
  {:else if me}
    <!-- Profile Hero Card -->
    <div
      class="relative overflow-hidden rounded-2xl border border-border/80 bg-gradient-to-br from-card via-card to-card/40 p-6 sm:p-7 shadow-sm"
    >
      <!-- Background Ambient Glow -->
      <div
        class="absolute -right-12 -top-12 w-64 h-64 rounded-full bg-accent/10 blur-3xl pointer-events-none"
        aria-hidden="true"
      ></div>

      <div class="relative z-10 flex flex-col md:flex-row md:items-center justify-between gap-6">
        <!-- Avatar and Identity -->
        <div class="flex items-center gap-4 sm:gap-5 min-w-0">
          <div class="relative shrink-0 group">
            <Avatar
              name={me.username || auth.user}
              src={me.avatar || auth.avatar}
              size={64}
              class="ring-4 ring-background shadow-md font-semibold text-xl"
            />
            {#if me.totp_enabled}
              <div
                class="absolute -bottom-1 -right-1 w-6 h-6 rounded-full bg-success text-success-foreground flex items-center justify-center ring-2 ring-background shadow-sm z-10"
                title={t('account.twoFAActive')}
              >
                <ShieldCheck class="w-3.5 h-3.5" />
              </div>
            {/if}
            <button
              type="button"
              onclick={() => navigate('/media')}
              title={t('account.changeAvatar')}
              aria-label={t('account.changeAvatar')}
              class="absolute inset-0 rounded-full bg-black/50 text-white opacity-0 group-hover:opacity-100 transition-opacity flex items-center justify-center cursor-pointer shadow-md"
            >
              <Image class="w-5 h-5 drop-shadow" />
            </button>
          </div>

          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2 mb-1">
              <h2 class="text-xl sm:text-2xl font-bold tracking-tight text-foreground truncate">
                {me.username || auth.user}
              </h2>
              <Badge
                variant={me.role === 'admin'
                  ? 'default'
                  : me.role === 'operator'
                    ? 'info'
                    : 'secondary'}
                class="uppercase text-[10px] tracking-wider font-semibold"
              >
                {me.role}
              </Badge>
              {#if me.totp_enabled}
                <Badge variant="success" class="text-[11px] gap-1 py-0.5">
                  <ShieldCheck class="w-3 h-3" />
                  {t('account.twoFAActive')}
                </Badge>
              {:else}
                <Badge variant="warning" class="text-[11px] gap-1 py-0.5">
                  <ShieldAlert class="w-3 h-3" />
                  {t('account.twoFAInactive')}
                </Badge>
              {/if}
            </div>

            <!-- Profile Metadata Chips -->
            <div
              class="flex flex-wrap items-center gap-y-1 gap-x-4 text-xs text-muted-foreground mt-2"
            >
              <span class="inline-flex items-center gap-1.5">
                <Mail class="w-3.5 h-3.5 text-muted-foreground/80 shrink-0" />
                <span class="truncate">{me.email || '—'}</span>
              </span>
              <span class="inline-flex items-center gap-1.5">
                <Calendar class="w-3.5 h-3.5 text-muted-foreground/80 shrink-0" />
                <span>{t('account.createdAt')}: {fmtDate(me.created_at)}</span>
              </span>
              <span class="inline-flex items-center gap-1.5">
                <Clock class="w-3.5 h-3.5 text-muted-foreground/80 shrink-0" />
                <span>{t('account.lastLogin')}: {fmtDate(me.last_login_at)}</span>
              </span>
            </div>
          </div>
        </div>

        <!-- Quick Status Indicator -->
        <div class="shrink-0 flex items-center gap-3">
          <div class="text-right hidden sm:block">
            <div class="text-xs text-muted-foreground font-medium">{t('account.session')}</div>
            <div
              class="text-xs text-success flex items-center justify-end gap-1.5 font-medium mt-0.5"
            >
              <span class="w-2 h-2 rounded-full bg-success animate-pulse"></span>
              {me.active ? t('common.active') : t('common.inactive')}
            </div>
          </div>
        </div>
      </div>

      <!-- Password Change Required Banner -->
      {#if me.must_change_password}
        <div
          role="alert"
          class="mt-5 p-4 border border-warning/40 bg-warning/10 rounded-xl text-sm flex items-start gap-3.5"
        >
          <AlertTriangle class="w-5 h-5 text-warning shrink-0 mt-0.5" />
          <div class="flex-1 min-w-0">
            <strong class="font-semibold text-warning">{t('account.mustChangePassword')}</strong>
            <p class="text-muted-foreground text-xs sm:text-sm mt-0.5">
              {t('account.mustChangeDesc')}
            </p>
          </div>
        </div>
      {/if}
    </div>

    <!-- Navigation Tabs -->
    <div class="flex items-center gap-1 border-b border-border/80 overflow-x-auto pb-px">
      <button
        type="button"
        onclick={() => (activeTab = 'security')}
        class="inline-flex items-center gap-2 px-4 py-2.5 text-sm font-medium border-b-2 transition-all whitespace-nowrap {activeTab ===
        'security'
          ? 'border-accent text-accent font-semibold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Shield class="w-4 h-4" />
        {t('account.tabSecurity')}
      </button>

      <button
        type="button"
        onclick={() => (activeTab = 'appearance')}
        class="inline-flex items-center gap-2 px-4 py-2.5 text-sm font-medium border-b-2 transition-all whitespace-nowrap {activeTab ===
        'appearance'
          ? 'border-accent text-accent font-semibold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Sparkles class="w-4 h-4" />
        {t('account.tabAppearance')}
      </button>

      <button
        type="button"
        onclick={() => (activeTab = 'tokens')}
        class="inline-flex items-center gap-2 px-4 py-2.5 text-sm font-medium border-b-2 transition-all whitespace-nowrap {activeTab ===
        'tokens'
          ? 'border-accent text-accent font-semibold'
          : 'border-transparent text-muted-foreground hover:text-foreground'}"
      >
        <Key class="w-4 h-4" />
        {t('account.tabTokens')}
        {#if tokens.length > 0}
          <span
            class="px-1.5 py-0.2 rounded-full text-[10px] font-bold {activeTab === 'tokens'
              ? 'bg-accent/20 text-accent'
              : 'bg-muted text-muted-foreground'}"
          >
            {tokens.length}
          </span>
        {/if}
      </button>
    </div>

    <!-- TAB 1: SEGURIDAD Y CONTRASEÑA -->
    {#if activeTab === 'security'}
      <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <!-- 2FA TOTP Card -->
        <div
          class="rounded-2xl border border-border bg-card p-5 sm:p-6 flex flex-col justify-between shadow-sm"
        >
          <div>
            <div class="flex items-center justify-between mb-3">
              <div class="flex items-center gap-2.5">
                <div class="p-2 rounded-xl bg-accent/10 text-accent">
                  <Shield class="w-5 h-5" />
                </div>
                <div>
                  <h3 class="text-base font-semibold tracking-tight">{t('account.twoFATitle')}</h3>
                  <p class="text-xs text-muted-foreground mt-0.5">{t('account.twoFactorStatus')}</p>
                </div>
              </div>
              <Badge variant={me.totp_enabled ? 'success' : 'outline'} class="text-xs">
                {me.totp_enabled ? t('account.twoFAActive') : t('account.twoFAInactive')}
              </Badge>
            </div>

            <p class="text-xs sm:text-sm text-muted-foreground leading-relaxed mt-2">
              {t('account.twoFADesc')}
            </p>

            <div
              class="my-5 p-4 rounded-xl border {me.totp_enabled
                ? 'border-success/30 bg-success/5'
                : 'border-border/80 bg-muted/30'}"
            >
              <div class="flex items-start gap-3">
                {#if me.totp_enabled}
                  <ShieldCheck class="w-5 h-5 text-success shrink-0 mt-0.5" />
                  <div>
                    <h4 class="text-xs font-semibold text-foreground">
                      {t('account.twoFAActive')}
                    </h4>
                    <p class="text-xs text-muted-foreground mt-0.5">
                      {t('account.twoFactorEnabledDesc')}
                    </p>
                  </div>
                {:else}
                  <ShieldAlert class="w-5 h-5 text-warning shrink-0 mt-0.5" />
                  <div>
                    <h4 class="text-xs font-semibold text-foreground">
                      {t('account.twoFAInactive')}
                    </h4>
                    <p class="text-xs text-muted-foreground mt-0.5">
                      {t('account.twoFactorDisabledDesc')}
                    </p>
                  </div>
                {/if}
              </div>
            </div>
          </div>

          <div class="pt-2">
            {#if me.totp_enabled}
              <Button
                variant="outline"
                size="sm"
                onclick={() => (showDisable2FADialog = true)}
                class="border-destructive/30 text-destructive hover:bg-destructive/10 hover:border-destructive/50 transition-colors"
              >
                <Ban class="w-4 h-4 mr-1.5" />
                {t('account.disable2FAButton')}
              </Button>
            {:else}
              <Button
                size="sm"
                onclick={start2FASetup}
                class="bg-accent hover:bg-accent-hover text-accent-foreground font-medium shadow-sm transition-all"
              >
                <ShieldCheck class="w-4 h-4 mr-1.5" />
                {t('account.setupAndEnable2FA')}
              </Button>
            {/if}
          </div>
        </div>

        <!-- Change Password Card -->
        <div class="rounded-2xl border border-border bg-card p-5 sm:p-6 shadow-sm">
          <div class="flex items-center gap-2.5 mb-3">
            <div class="p-2 rounded-xl bg-accent/10 text-accent">
              <Lock class="w-5 h-5" />
            </div>
            <div>
              <h3 class="text-base font-semibold tracking-tight">{t('account.changePassword')}</h3>
              <p class="text-xs text-muted-foreground mt-0.5">
                {t('account.passwordRequirements')}
              </p>
            </div>
          </div>

          <form onsubmit={changePassword} class="space-y-4 mt-4">
            <!-- Current Password -->
            <div>
              <label
                for="old-pw"
                class="block text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1.5"
              >
                {t('account.oldPassword')}
              </label>
              <div class="relative">
                <input
                  id="old-pw"
                  bind:value={oldPassword}
                  type={showOld ? 'text' : 'password'}
                  required
                  autocomplete="current-password"
                  class="input pr-10 w-full rounded-xl bg-background/80"
                  placeholder="••••••••"
                />
                <button
                  type="button"
                  onclick={() => (showOld = !showOld)}
                  class="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors p-1"
                  aria-label={showOld ? t('login.hidePassword') : t('login.showPassword')}
                >
                  {#if showOld}
                    <EyeOff class="w-4 h-4" />
                  {:else}
                    <Eye class="w-4 h-4" />
                  {/if}
                </button>
              </div>
            </div>

            <!-- New Password -->
            <div>
              <label
                for="new-pw"
                class="block text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1.5"
              >
                {t('account.newPassword')}
              </label>
              <div class="relative">
                <input
                  id="new-pw"
                  bind:value={newPassword}
                  type={showNew ? 'text' : 'password'}
                  required
                  minlength="8"
                  autocomplete="new-password"
                  class="input pr-10 w-full rounded-xl bg-background/80"
                  placeholder="••••••••"
                />
                <button
                  type="button"
                  onclick={() => (showNew = !showNew)}
                  class="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors p-1"
                  aria-label={showNew ? t('login.hidePassword') : t('login.showPassword')}
                >
                  {#if showNew}
                    <EyeOff class="w-4 h-4" />
                  {:else}
                    <Eye class="w-4 h-4" />
                  {/if}
                </button>
              </div>

              <!-- Segmented Password Strength Bar -->
              {#if newPassword}
                <div class="mt-2.5 space-y-1.5">
                  <div class="flex items-center justify-between text-xs">
                    <span class="text-muted-foreground">{t('account.passwordStrengthLabel')}</span>
                    <span
                      class="font-medium {strength.score >= 3
                        ? 'text-success'
                        : strength.score >= 2
                          ? 'text-warning'
                          : 'text-destructive'}"
                    >
                      {strength.labelKey ? t(strength.labelKey) : strength.label}
                    </span>
                  </div>
                  <div class="grid grid-cols-4 gap-1.5">
                    {#each [0, 1, 2, 3] as idx (idx)}
                      <div
                        class="h-1.5 rounded-full transition-all duration-300 {idx <=
                        strength.score - 1
                          ? strength.color
                          : 'bg-muted'}"
                      ></div>
                    {/each}
                  </div>
                </div>
              {/if}
            </div>

            <!-- Confirm New Password -->
            <div>
              <label
                for="confirm-pw"
                class="block text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1.5"
              >
                {t('account.confirmPassword')}
              </label>
              <div class="relative">
                <input
                  id="confirm-pw"
                  bind:value={confirmPassword}
                  type={showConfirm ? 'text' : 'password'}
                  required
                  minlength="8"
                  autocomplete="new-password"
                  class="input pr-10 w-full rounded-xl bg-background/80 {confirmPassword &&
                  !passwordsMatch
                    ? 'border-destructive focus:border-destructive'
                    : ''}"
                  placeholder="••••••••"
                />
                <button
                  type="button"
                  onclick={() => (showConfirm = !showConfirm)}
                  class="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors p-1"
                  aria-label={showConfirm ? t('login.hidePassword') : t('login.showPassword')}
                >
                  {#if showConfirm}
                    <EyeOff class="w-4 h-4" />
                  {:else}
                    <Eye class="w-4 h-4" />
                  {/if}
                </button>
              </div>

              <!-- Matching Indicator -->
              {#if confirmPassword}
                {#if passwordsMatch}
                  <div class="flex items-center gap-1.5 mt-1.5 text-xs text-success font-medium">
                    <Check class="w-3.5 h-3.5" />
                    <span>{t('account.passwordMatch')}</span>
                  </div>
                {:else}
                  <div
                    class="flex items-center gap-1.5 mt-1.5 text-xs text-destructive font-medium"
                  >
                    <AlertTriangle class="w-3.5 h-3.5" />
                    <span>{t('account.passwordsDoNotMatch')}</span>
                  </div>
                {/if}
              {/if}
            </div>

            <div class="pt-2">
              <Button
                type="submit"
                disabled={!canSubmit || saving}
                class="w-full sm:w-auto bg-accent hover:bg-accent-hover text-accent-foreground font-medium shadow-sm transition-all"
              >
                {#if saving}
                  <Spinner size="xs" class="mr-2" />
                  {t('account.changing')}
                {:else}
                  <Lock class="w-4 h-4 mr-1.5" />
                  {t('account.changePassword')}
                {/if}
              </Button>
            </div>
          </form>
        </div>
      </div>
    {/if}

    <!-- TAB 2: APARIENCIA Y TEMA -->
    {#if activeTab === 'appearance'}
      <div class="space-y-6">
        <!-- Interface Language Card -->
        <div class="rounded-2xl border border-border bg-card p-5 sm:p-6 shadow-sm">
          <div class="flex items-center gap-2.5 mb-2">
            <div class="p-2 rounded-xl bg-accent/10 text-accent">
              <Globe class="w-5 h-5" />
            </div>
            <div>
              <h3 class="text-base font-semibold tracking-tight">{t('account.languageTitle')}</h3>
              <p class="text-xs text-muted-foreground mt-0.5">{t('account.languageDesc')}</p>
            </div>
          </div>

          <div class="mt-5">
            <LanguageSelector variant="cards" />
          </div>
        </div>

        <!-- Theme Mode Cards -->
        <div class="rounded-2xl border border-border bg-card p-5 sm:p-6 shadow-sm">
          <div class="flex items-center gap-2.5 mb-2">
            <div class="p-2 rounded-xl bg-accent/10 text-accent">
              <Palette class="w-5 h-5" />
            </div>
            <div>
              <h3 class="text-base font-semibold tracking-tight">{t('theme.mode')}</h3>
              <p class="text-xs text-muted-foreground mt-0.5">{t('account.themeDesc')}</p>
            </div>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-3 gap-4 mt-5">
            {#each THEMES as mode (mode)}
              <button
                type="button"
                onclick={() => setTheme(mode)}
                class="relative flex flex-col p-4 rounded-xl border text-left transition-all group overflow-hidden {theme.mode ===
                mode
                  ? 'border-accent bg-accent/5 ring-2 ring-accent/30 shadow-sm'
                  : 'border-border bg-card/60 hover:bg-muted/40 hover:border-border-hover'}"
              >
                <!-- Mini UI Mockup Illustration -->
                <div
                  class="w-full h-20 rounded-lg border border-border/80 overflow-hidden mb-3.5 flex flex-col {mode ===
                  'light'
                    ? 'bg-slate-100 text-slate-800'
                    : mode === 'oled'
                      ? 'bg-black text-white'
                      : 'bg-slate-900 text-slate-200'}"
                >
                  <!-- Mock Header -->
                  <div
                    class="h-4 border-b border-border/40 px-2 flex items-center justify-between {mode ===
                    'light'
                      ? 'bg-white'
                      : mode === 'oled'
                        ? 'bg-black'
                        : 'bg-slate-950/80'}"
                  >
                    <span class="w-8 h-1.5 rounded-full bg-accent/60"></span>
                    <span class="w-3 h-1.5 rounded-full bg-muted-foreground/30"></span>
                  </div>
                  <!-- Mock Body -->
                  <div class="flex-1 flex p-1.5 gap-1.5">
                    <div
                      class="w-1/4 rounded border border-border/30 h-full flex flex-col gap-1 p-0.5"
                    >
                      <span class="w-full h-1 rounded bg-muted-foreground/20"></span>
                      <span class="w-3/4 h-1 rounded bg-muted-foreground/20"></span>
                    </div>
                    <div
                      class="flex-1 rounded border border-border/30 h-full p-1 flex flex-col justify-between"
                    >
                      <span class="w-1/2 h-1.5 rounded bg-foreground/40"></span>
                      <div class="flex gap-1">
                        <span class="w-1/3 h-2 rounded bg-accent/40"></span>
                        <span class="w-1/3 h-2 rounded bg-muted-foreground/20"></span>
                      </div>
                    </div>
                  </div>
                </div>

                <div class="flex items-center justify-between w-full mb-1">
                  <div class="flex items-center gap-2">
                    {#if mode === 'light'}
                      <Sun class="w-4 h-4 text-amber-500" />
                    {:else if mode === 'oled'}
                      <Sparkles class="w-4 h-4 text-purple-400" />
                    {:else}
                      <Moon class="w-4 h-4 text-blue-400" />
                    {/if}
                    <span class="font-semibold text-sm text-foreground">{t(`theme.${mode}`)}</span>
                  </div>
                  {#if theme.mode === mode}
                    <div
                      class="w-5 h-5 rounded-full bg-accent text-accent-foreground flex items-center justify-center"
                    >
                      <Check class="w-3 h-3" />
                    </div>
                  {/if}
                </div>

                <p class="text-xs text-muted-foreground mt-1 leading-normal">
                  {#if mode === 'zinc'}
                    {t('account.themeZincDesc')}
                  {:else if mode === 'oled'}
                    {t('account.themeOledDesc')}
                  {:else}
                    {t('account.themeLightDesc')}
                  {/if}
                </p>
              </button>
            {/each}
          </div>
        </div>

        <!-- Accent Color Picker -->
        <div class="rounded-2xl border border-border bg-card p-5 sm:p-6 shadow-sm">
          <div class="flex items-center gap-2.5 mb-2">
            <div class="p-2 rounded-xl bg-accent/10 text-accent">
              <Sparkles class="w-5 h-5" />
            </div>
            <div>
              <h3 class="text-base font-semibold tracking-tight">{t('theme.accent')}</h3>
              <p class="text-xs text-muted-foreground mt-0.5">
                {t('theme.toggle', { current: theme.accent })}
              </p>
            </div>
          </div>

          <div class="flex items-center flex-wrap gap-3 mt-4">
            {#each ACCENTS as acc (acc)}
              <button
                type="button"
                onclick={() => setAccent(acc)}
                class="flex items-center gap-2.5 px-3.5 py-2 rounded-xl border text-xs font-medium transition-all {theme.accent ===
                acc
                  ? 'border-accent bg-accent/10 ring-2 ring-accent/30 text-foreground font-semibold shadow-sm'
                  : 'border-border bg-card/60 hover:bg-muted/60 text-muted-foreground'}"
              >
                <span
                  class="w-3.5 h-3.5 rounded-full shrink-0 shadow-xs"
                  style="background-color: {ACCENT_COLORS[acc]};"
                ></span>
                <span class="capitalize">{t(`theme.${acc}`)}</span>
                {#if theme.accent === acc}
                  <Check class="w-3.5 h-3.5 text-accent ml-0.5" />
                {/if}
              </button>
            {/each}
          </div>

          <!-- Live Component Preview -->
          <div class="mt-6 pt-5 border-t border-border/80">
            <h4 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-3">
              {t('account.themePreview')}
            </h4>
            <div
              class="p-4 rounded-xl border border-border/70 bg-background/50 flex flex-wrap items-center gap-3"
            >
              <Button
                size="sm"
                class="bg-accent hover:bg-accent-hover text-accent-foreground font-medium"
              >
                {t('account.previewPrimary')}
              </Button>
              <Button size="sm" variant="outline">{t('account.previewSecondary')}</Button>
              <Badge variant="default" class="py-1 px-2.5">{t('account.previewAccent')}</Badge>
              <div class="text-xs text-muted-foreground italic sm:ml-auto">
                {t('account.themePreviewSample')}
              </div>
            </div>
          </div>
        </div>

        <!-- Sidebar Layout Mode -->
        <div class="rounded-2xl border border-border bg-card p-5 sm:p-6 shadow-sm">
          <div class="flex items-center gap-2.5 mb-2">
            <div class="p-2 rounded-xl bg-accent/10 text-accent">
              <PanelLeft class="w-5 h-5" />
            </div>
            <div>
              <h3 class="text-base font-semibold tracking-tight">
                {t('account.sidebarModeTitle')}
              </h3>
              <p class="text-xs text-muted-foreground mt-0.5">{t('account.sidebarModeDesc')}</p>
            </div>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-3 gap-4 mt-5">
            {#each [{ id: 'full', label: t('account.sidebarModeFull'), desc: t('account.sidebarModeFullDesc') }, { id: 'rail', label: t('account.sidebarModeRail'), desc: t('account.sidebarModeRailDesc') }, { id: 'hover', label: t('account.sidebarModeHover'), desc: t('account.sidebarModeHoverDesc') }] as opt (opt.id)}
              <button
                type="button"
                onclick={() => setDesktopSidebarMode(opt.id)}
                class="relative flex flex-col p-4 rounded-xl border text-left transition-all group overflow-hidden {sidebarMode.value ===
                opt.id
                  ? 'border-accent bg-accent/5 ring-2 ring-accent/30 shadow-sm'
                  : 'border-border bg-card/60 hover:bg-muted/40 hover:border-border-hover'}"
              >
                <!-- Mini layout illustration -->
                <div
                  class="w-full h-16 rounded-lg border border-border/80 bg-background/60 mb-3 flex overflow-hidden p-1.5 gap-1.5"
                >
                  <div
                    class="rounded border border-border/60 bg-muted/40 h-full flex flex-col gap-1 p-1 transition-all {opt.id ===
                    'rail'
                      ? 'w-3.5'
                      : opt.id === 'hover'
                        ? 'w-4'
                        : 'w-10'}"
                  >
                    <span class="w-full h-1 rounded bg-accent/70"></span>
                    <span class="w-full h-1 rounded bg-muted-foreground/30"></span>
                    <span class="w-full h-1 rounded bg-muted-foreground/30"></span>
                  </div>
                  <div
                    class="flex-1 rounded border border-border/40 bg-card h-full p-1.5 flex flex-col justify-between"
                  >
                    <span class="w-1/3 h-1.5 rounded bg-muted-foreground/30"></span>
                    <div class="grid grid-cols-2 gap-1">
                      <span class="h-3 rounded bg-muted/50 border border-border/30"></span>
                      <span class="h-3 rounded bg-muted/50 border border-border/30"></span>
                    </div>
                  </div>
                </div>

                <div class="flex items-center justify-between w-full mb-1">
                  <span class="font-semibold text-sm text-foreground">{opt.label}</span>
                  {#if sidebarMode.value === opt.id}
                    <div
                      class="w-5 h-5 rounded-full bg-accent text-accent-foreground flex items-center justify-center"
                    >
                      <Check class="w-3 h-3" />
                    </div>
                  {/if}
                </div>
                <p class="text-xs text-muted-foreground mt-1 leading-normal">{opt.desc}</p>
              </button>
            {/each}
          </div>
        </div>

        <!-- Resource Gauge Style Card -->
        <div class="rounded-2xl border border-border bg-card p-5 sm:p-6 shadow-sm">
          <div class="flex items-center gap-2.5 mb-2">
            <div class="p-2 rounded-xl bg-accent/10 text-accent">
              <Activity class="w-5 h-5" />
            </div>
            <div>
              <h3 class="text-base font-semibold tracking-tight">{t('account.gaugeStyleTitle')}</h3>
              <p class="text-xs text-muted-foreground mt-0.5">{t('account.gaugeStyleDesc')}</p>
            </div>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4 mt-5">
            {#each [{ id: 'radial', label: t('account.gaugeRadial'), desc: t('account.gaugeRadialDesc') }, { id: 'linear', label: t('account.gaugeLinear'), desc: t('account.gaugeLinearDesc') }] as opt (opt.id)}
              <button
                type="button"
                onclick={() => setGaugeStyle(opt.id)}
                class="relative flex flex-col p-4 rounded-xl border text-left transition-all group overflow-hidden {gaugeStyle.mode ===
                opt.id
                  ? 'border-accent bg-accent/5 ring-2 ring-accent/30 shadow-sm'
                  : 'border-border bg-card/60 hover:bg-muted/40 hover:border-border-hover'}"
              >
                <!-- Mini visualization -->
                <div
                  class="w-full h-16 rounded-lg border border-border/80 bg-background/60 mb-3 flex items-center justify-center p-2"
                >
                  {#if opt.id === 'radial'}
                    <div class="flex items-center gap-4">
                      <div class="relative w-10 h-10 flex items-center justify-center">
                        <svg class="w-10 h-10 -rotate-90" viewBox="0 0 36 36">
                          <circle
                            cx="18"
                            cy="18"
                            r="14"
                            fill="none"
                            class="stroke-muted"
                            stroke-width="3"
                          />
                          <circle
                            cx="18"
                            cy="18"
                            r="14"
                            fill="none"
                            class="stroke-accent"
                            stroke-width="3"
                            stroke-dasharray="88"
                            stroke-dashoffset="35"
                            stroke-linecap="round"
                          />
                        </svg>
                        <span class="absolute text-[9px] font-mono font-bold">60%</span>
                      </div>
                      <div class="relative w-10 h-10 flex items-center justify-center">
                        <svg class="w-10 h-10 -rotate-90" viewBox="0 0 36 36">
                          <circle
                            cx="18"
                            cy="18"
                            r="14"
                            fill="none"
                            class="stroke-muted"
                            stroke-width="3"
                          />
                          <circle
                            cx="18"
                            cy="18"
                            r="14"
                            fill="none"
                            class="stroke-success"
                            stroke-width="3"
                            stroke-dasharray="88"
                            stroke-dashoffset="50"
                            stroke-linecap="round"
                          />
                        </svg>
                        <span class="absolute text-[9px] font-mono font-bold">42%</span>
                      </div>
                    </div>
                  {:else}
                    <div class="w-full max-w-[200px] flex flex-col gap-2">
                      <div class="w-full">
                        <div class="flex justify-between text-[10px] text-muted-foreground mb-1">
                          <span>CPU</span>
                          <span class="font-mono">60%</span>
                        </div>
                        <div class="h-2 w-full rounded-full bg-muted overflow-hidden">
                          <div class="h-full bg-accent rounded-full" style="width: 60%"></div>
                        </div>
                      </div>
                      <div class="w-full">
                        <div class="flex justify-between text-[10px] text-muted-foreground mb-1">
                          <span>RAM</span>
                          <span class="font-mono">42%</span>
                        </div>
                        <div class="h-2 w-full rounded-full bg-muted overflow-hidden">
                          <div class="h-full bg-success rounded-full" style="width: 42%"></div>
                        </div>
                      </div>
                    </div>
                  {/if}
                </div>

                <div class="flex items-center justify-between w-full mb-1">
                  <span class="font-semibold text-sm text-foreground">{opt.label}</span>
                  {#if gaugeStyle.mode === opt.id}
                    <div
                      class="w-5 h-5 rounded-full bg-accent text-accent-foreground flex items-center justify-center"
                    >
                      <Check class="w-3 h-3" />
                    </div>
                  {/if}
                </div>
                <p class="text-xs text-muted-foreground mt-1 leading-normal">{opt.desc}</p>
              </button>
            {/each}
          </div>
        </div>

        <!-- Web Terminal Preferences Card -->
        <div class="rounded-2xl border border-border bg-card p-5 sm:p-6 shadow-sm">
          <div class="flex items-center gap-2.5 mb-2">
            <div class="p-2 rounded-xl bg-accent/10 text-accent">
              <Terminal class="w-5 h-5" />
            </div>
            <div>
              <h3 class="text-base font-semibold tracking-tight">
                {t('account.terminalSettingsTitle')}
              </h3>
              <p class="text-xs text-muted-foreground mt-0.5">
                {t('account.terminalSettingsDesc')}
              </p>
            </div>
          </div>

          <!-- Color Themes -->
          <div class="mt-5">
            <h4 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-3">
              {t('account.terminalTheme')}
            </h4>
            <div class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-5 gap-3">
              {#each Object.values(TERMINAL_THEMES) as th (th.id)}
                <button
                  type="button"
                  onclick={() => setTerminalTheme(th.id)}
                  class="flex items-center justify-between p-3 rounded-xl border text-xs font-medium transition-all {terminalSettings.theme ===
                  th.id
                    ? 'border-accent bg-accent/10 ring-2 ring-accent/30 text-foreground font-semibold shadow-sm'
                    : 'border-border bg-card/60 hover:bg-muted/60 text-muted-foreground'}"
                >
                  <div class="flex items-center gap-2 truncate">
                    <span
                      class="w-3.5 h-3.5 rounded-full border border-border shrink-0 shadow-xs"
                      style="background-color: {th.background}; border-color: {th.cursor};"
                    ></span>
                    <span class="truncate">{th.name}</span>
                  </div>
                  {#if terminalSettings.theme === th.id}
                    <Check class="w-3.5 h-3.5 text-accent ml-1 shrink-0" />
                  {/if}
                </button>
              {/each}
            </div>
          </div>

          <!-- Font Size Selector -->
          <div class="mt-6 pt-5 border-t border-border/80">
            <h4 class="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-3">
              {t('account.terminalFontSize')}
            </h4>
            <div class="flex items-center gap-2 flex-wrap">
              {#each [11, 12, 13, 14, 15, 16, 18] as size (size)}
                <button
                  type="button"
                  onclick={() => setTerminalFontSize(size)}
                  class="px-3.5 py-1.5 rounded-lg border text-xs font-mono font-medium transition-all {terminalSettings.fontSize ===
                  size
                    ? 'border-accent bg-accent text-accent-foreground font-bold shadow-sm'
                    : 'border-border bg-card/60 hover:bg-muted/60 text-muted-foreground'}"
                >
                  {size}px
                </button>
              {/each}
            </div>
          </div>

          <!-- TUI & Function Key Toggles -->
          <div class="mt-6 pt-5 border-t border-border/80 space-y-4">
            <Switch
              checked={terminalSettings.showTuiBar}
              onchange={(val) => setTerminalTuiBar(val)}
              label={t('account.terminalTuiBar')}
              description={t('account.terminalTuiBarDesc')}
            />
            <Switch
              checked={terminalSettings.showFnKeys}
              onchange={(val) => setTerminalFnKeys(val)}
              label={t('account.terminalFnKeys')}
              description={t('account.terminalFnKeysDesc')}
            />
          </div>
        </div>
      </div>
    {/if}

    <!-- TAB 3: TOKENS DE API -->
    {#if activeTab === 'tokens'}
      <div class="space-y-6">
        <!-- API Info Banner -->
        <div class="rounded-2xl border border-border bg-card p-5 sm:p-6 shadow-sm">
          <div class="flex flex-col sm:flex-row sm:items-center justify-between gap-4">
            <div class="flex items-start gap-3">
              <div class="p-2.5 rounded-xl bg-accent/10 text-accent shrink-0 mt-0.5">
                <Key class="w-5 h-5" />
              </div>
              <div>
                <h3 class="text-base font-semibold tracking-tight">{t('account.apiTokens')}</h3>
                <p class="text-xs sm:text-sm text-muted-foreground mt-1 leading-relaxed">
                  {@html t('account.tokenHelp', {
                    code: htmlVar(
                      '<code class="text-xs bg-muted font-mono px-1.5 py-0.5 rounded text-accent font-medium">Authorization: Bearer wvmb_…</code>'
                    ),
                  })}
                </p>
              </div>
            </div>

            <Button
              size="sm"
              onclick={() => (showTokenDialog = true)}
              class="bg-accent hover:bg-accent-hover text-accent-foreground shrink-0 shadow-sm font-medium"
            >
              <Plus class="w-4 h-4 mr-1.5" />
              {t('account.newToken')}
            </Button>
          </div>
        </div>

        <!-- Token List -->
        {#if tokens.length === 0}
          <div class="rounded-2xl border border-border bg-card p-8 shadow-sm">
            <EmptyState
              icon="key"
              title={t('account.noTokensYet')}
              description={t('account.noTokens')}
            >
              {#snippet action()}
                <Button
                  size="sm"
                  onclick={() => (showTokenDialog = true)}
                  class="bg-accent hover:bg-accent-hover text-accent-foreground font-medium"
                >
                  <Plus class="w-4 h-4 mr-1.5" />
                  {t('account.createToken')}
                </Button>
              {/snippet}
            </EmptyState>
          </div>
        {:else}
          <div
            class="rounded-2xl border border-border bg-card overflow-hidden shadow-sm divide-y divide-border"
          >
            {#each tokens as tok (tok.id)}
              <div
                class="p-4 sm:p-5 flex flex-col sm:flex-row sm:items-center justify-between gap-4 hover:bg-muted/20 transition-colors"
              >
                <div class="min-w-0 flex-1">
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="font-semibold text-sm text-foreground truncate">{tok.name}</span>
                    {#if tok.revoked}
                      <Badge variant="destructive" class="text-[10px]">
                        {t('account.revoked')}
                      </Badge>
                    {:else if new Date(tok.expires_at) < new Date()}
                      <Badge variant="warning" class="text-[10px]">
                        {t('account.expired')}
                      </Badge>
                    {:else}
                      <Badge variant="success" class="text-[10px]">
                        {t('common.active')}
                      </Badge>
                    {/if}
                  </div>

                  <div class="flex items-center gap-2 mt-1.5">
                    <span
                      class="text-xs font-mono text-muted-foreground bg-muted/60 px-2 py-0.5 rounded border border-border/50"
                    >
                      {tok.prefix}…
                    </span>
                  </div>

                  <div
                    class="text-xs text-muted-foreground mt-2 flex flex-wrap items-center gap-x-3 gap-y-1"
                  >
                    <span>
                      {t('account.createdExpires', {
                        created: fmtDate(tok.created_at),
                        expires: fmtDate(tok.expires_at),
                      })}
                    </span>
                    {#if tok.last_used_at}
                      <span>
                        {t('account.lastUsed', {
                          used: fmtDate(tok.last_used_at),
                        })}
                      </span>
                    {/if}
                  </div>
                </div>

                <div class="flex items-center gap-2 shrink-0">
                  {#if !tok.revoked}
                    <Button
                      size="xs"
                      variant="outline"
                      onclick={() => revokeToken(tok)}
                      class="border-warning/30 text-warning hover:bg-warning/10"
                    >
                      <Ban class="w-3.5 h-3.5 mr-1" />
                      {t('account.revoke')}
                    </Button>
                  {/if}
                  <Button size="xs" variant="destructive" onclick={() => deleteToken(tok)}>
                    <Trash2 class="w-3.5 h-3.5 mr-1" />
                    {t('account.deleteToken')}
                  </Button>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
  {:else}
    <div
      class="p-8 text-center text-sm text-muted-foreground border border-border rounded-2xl bg-card"
    >
      {t('account.couldNotLoad')}
    </div>
  {/if}
</div>

<!-- Create Token Dialog -->
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
    <div class="space-y-4">
      <div class="p-3 border border-warning/30 bg-warning/10 rounded-xl text-xs text-warning">
        <div class="flex items-start gap-2">
          <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
          <p>{t('account.copyTokenNow')}</p>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <Input value={newTokenPlain} readonly class="font-mono text-xs select-all" />
        <Button onclick={copyToken} size="sm" class="shrink-0">
          <Copy class="w-4 h-4 mr-1.5" />
          {t('common.copy')}
        </Button>
      </div>
    </div>
  {:else}
    <div class="space-y-4">
      <div>
        <label
          for="tok-name"
          class="text-xs font-semibold text-muted-foreground uppercase tracking-wider block mb-1.5"
        >
          {t('account.tokenName')}
        </label>
        <Input id="tok-name" bind:value={newTokenName} placeholder="e.g. ci-deploy, monitoring" />
      </div>
      <div>
        <label
          for="tok-ttl"
          class="text-xs font-semibold text-muted-foreground uppercase tracking-wider block mb-1.5"
        >
          {t('account.expiresInHours')}
        </label>
        <Input id="tok-ttl" type="number" bind:value={newTokenTtl} min="1" max="8760" />
        <p class="text-xs text-muted-foreground mt-1">{t('account.ttlDefault')}</p>
      </div>
      {#if newTokenSaving}
        <div class="flex items-center gap-2 text-xs text-muted-foreground">
          <Spinner size="xs" />
          <span>{t('account.creating')}</span>
        </div>
      {/if}
    </div>
  {/if}
</ConfirmDialog>

<!-- Revoke Confirmation -->
<ConfirmDialog
  open={!!confirmRevoke}
  title={t('account.revokeTokenTitle')}
  message={confirmRevoke ? t('account.revokeTokenMsg', { name: confirmRevoke.name }) : ''}
  confirmLabel={t('account.revoke')}
  onConfirm={doRevoke}
  onCancel={() => (confirmRevoke = null)}
/>

<!-- Delete Confirmation -->
<ConfirmDialog
  open={!!confirmDelete}
  title={t('account.deleteTokenTitle')}
  message={confirmDelete ? t('account.deleteTokenMsg', { name: confirmDelete.name }) : ''}
  confirmLabel={t('account.deleteToken')}
  onConfirm={doDelete}
  onCancel={() => (confirmDelete = null)}
/>

<!-- 2FA Setup Modal -->
<Dialog.Root bind:open={show2FADialog}>
  <Dialog.Content class="sm:max-w-lg max-h-[90vh] flex flex-col">
    {#if setup2FAData}
      <Dialog.Header>
        <Dialog.Title class="flex items-center gap-2">
          <Shield class="w-5 h-5 text-accent" />
          {t('account.setup2FATitle')}
        </Dialog.Title>
      </Dialog.Header>

      <div class="space-y-5 overflow-y-auto text-sm pr-1">
        <!-- Step 1: Scan QR Code -->
        <div class="space-y-3">
          <div class="flex items-center gap-2">
            <span
              class="w-6 h-6 rounded-full bg-accent/20 text-accent font-bold text-xs flex items-center justify-center shrink-0"
            >
              1
            </span>
            <h4 class="font-bold text-foreground">{t('account.scanQrStep')}</h4>
          </div>
          <p class="text-xs text-muted-foreground leading-relaxed">
            {t('account.scanQrInstructions1')}<strong>{t('account.scanQrInstructions2')}</strong>{t(
              'account.scanQrInstructions3'
            )}
          </p>

          <!-- QR Code Display Box -->
          <div
            class="flex flex-col items-center justify-center p-5 bg-slate-950/50 rounded-2xl border border-border/80 shadow-inner"
          >
            {#if generatingQr}
              <div
                class="w-48 h-48 flex flex-col items-center justify-center gap-2 text-muted-foreground"
              >
                <Spinner size="md" />
                <span class="text-xs">{t('account.generatingQr')}</span>
              </div>
            {:else if qrCodeDataUrl}
              <div
                class="p-3 bg-white rounded-2xl shadow-xl border border-slate-200 inline-block transition-transform hover:scale-[1.02]"
              >
                <img src={qrCodeDataUrl} alt={t('account.qrAlt')} class="w-48 h-48 block" />
              </div>
              <p class="text-[11px] text-muted-foreground mt-2 font-mono flex items-center gap-1.5">
                <Icon name="qrCode" size={13} class="text-accent" />
                <span>{t('account.qrHint')}</span>
              </p>
            {:else}
              <div
                class="w-48 h-48 flex items-center justify-center bg-muted/40 rounded-2xl text-xs text-muted-foreground"
              >
                {t('account.qrRenderFailed')}
              </div>
            {/if}
          </div>

          <!-- Collapsible Manual Secret Key -->
          <div class="pt-1">
            <button
              type="button"
              class="text-xs text-accent hover:text-accent-hover font-medium flex items-center gap-1.5 transition-colors"
              onclick={() => (showManualSecret = !showManualSecret)}
            >
              {#if showManualSecret}
                <ChevronUp class="w-3.5 h-3.5" />
              {:else}
                <ChevronDown class="w-3.5 h-3.5" />
              {/if}
              <span>
                {showManualSecret ? t('account.hideManualKey') : t('account.showManualKey')}
              </span>
            </button>

            {#if showManualSecret}
              <div
                class="p-3.5 bg-muted/50 rounded-xl border border-border/60 flex items-center justify-between font-mono text-xs mt-2"
              >
                <div class="min-w-0 pr-2">
                  <span class="text-[10px] text-muted-foreground block font-sans mb-0.5">
                    {t('account.base32SecretLabel')}
                  </span>
                  <span class="font-bold tracking-widest text-accent select-all break-all">
                    {setup2FAData.secret}
                  </span>
                </div>
                <button
                  type="button"
                  onclick={() => {
                    navigator.clipboard.writeText(setup2FAData.secret);
                    toast.success(t('account.secretCopied'));
                  }}
                  class="px-3 py-1.5 rounded-lg bg-card border border-border text-xs hover:text-foreground font-sans font-medium transition-colors shrink-0"
                >
                  {t('common.copy')}
                </button>
              </div>
            {/if}
          </div>
        </div>

        <!-- Step 2: Backup Codes -->
        <div class="space-y-2 pt-3 border-t border-border/60">
          <div class="flex items-center gap-2">
            <span
              class="w-6 h-6 rounded-full bg-accent/20 text-accent font-bold text-xs flex items-center justify-center shrink-0"
            >
              2
            </span>
            <h4 class="font-bold text-foreground">{t('account.backupCodesStep')}</h4>
          </div>
          <p class="text-xs text-muted-foreground leading-relaxed">
            {t('account.backupCodesInstructions')}
          </p>
          <div
            class="grid grid-cols-2 sm:grid-cols-4 gap-2 p-3 bg-background/80 rounded-xl border border-border font-mono text-xs text-foreground"
          >
            {#each setup2FAData.backup_codes as code (code)}
              <span
                class="p-1.5 bg-muted/40 rounded-lg text-center font-bold select-all tracking-wider"
              >
                {code}
              </span>
            {/each}
          </div>
          <button
            type="button"
            class="text-[11px] text-muted-foreground hover:text-foreground underline flex items-center gap-1.5 transition-colors"
            onclick={() => {
              navigator.clipboard.writeText(setup2FAData.backup_codes.join('\n'));
              toast.success(t('account.backupCodesCopied'));
            }}
          >
            <Copy class="w-3 h-3" />
            {t('account.copyAllBackupCodes')}
          </button>
        </div>

        <!-- Step 3: Verification Code -->
        <div class="space-y-2.5 pt-3 border-t border-border/60">
          <div class="flex items-center gap-2">
            <span
              class="w-6 h-6 rounded-full bg-accent/20 text-accent font-bold text-xs flex items-center justify-center shrink-0"
            >
              3
            </span>
            <h4 class="font-bold text-foreground">{t('account.verifyCodeStep')}</h4>
          </div>
          <p class="text-xs text-muted-foreground">
            {t('account.verifyCodeInstructions')}
          </p>
          <input
            type="text"
            bind:value={test2FACode}
            placeholder="000 000"
            maxlength="6"
            class="input text-center text-xl font-mono tracking-widest font-bold py-2.5 border-accent/50 focus:border-accent focus:ring-1 focus:ring-accent rounded-xl"
          />
        </div>
      </div>

      <Dialog.Footer>
        <Button variant="outline" onclick={() => (show2FADialog = false)} disabled={enabling2FA}>
          {t('common.cancel')}
        </Button>
        <Button
          onclick={confirmEnable2FA}
          disabled={enabling2FA || !test2FACode.trim()}
          class="bg-accent hover:bg-accent-hover text-accent-foreground"
        >
          {#if enabling2FA}
            <Spinner size="xs" class="mr-2" />
            {t('account.verifying')}
          {:else}
            {t('account.enable2FAButton')}
          {/if}
        </Button>
      </Dialog.Footer>
    {/if}
  </Dialog.Content>
</Dialog.Root>

<!-- 2FA Disable Modal -->
<ConfirmDialog
  open={showDisable2FADialog}
  title={t('account.disable2FADialogTitle')}
  message=""
  confirmLabel={disabling2FA ? t('account.disabling') : t('account.disable2FAButton')}
  onConfirm={confirmDisable2FA}
  onCancel={() => {
    showDisable2FADialog = false;
    disable2FAPassword = '';
  }}
>
  <div class="space-y-3">
    <p class="text-xs text-muted-foreground">
      {t('account.disable2FAConfirmText')}
    </p>
    <Input
      type="password"
      bind:value={disable2FAPassword}
      placeholder={t('account.currentPasswordPlaceholder')}
      required
    />
  </div>
</ConfirmDialog>
