<script>
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import Switch from '$lib/components/Switch.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let loading = $state(true);
  let saving = $state(false);
  let testing = $state(false);

  // Config (no secrets come from the server).
  let cfg = $state({});
  let hasWebhookSecret = $state(false);
  let hasSMTPUser = $state(false);
  let hasSMTPPassword = $state(false);

  // Secret inputs: empty = keep existing (never clear by accident).
  // These values are only sent if the user types something.
  let webhookSecret = $state('');
  let smtpUser = $state('');
  let smtpPassword = $state('');
  let clearSecrets = $state(false);

  let events = $state([]);

  async function load() {
    try {
      const [s, e] = await Promise.all([api.getNotifyConfig(), api.listNotifyEvents()]);
      cfg = s.config || {};
      hasWebhookSecret = s.has_webhook_secret;
      hasSMTPUser = s.has_smtp_user;
      hasSMTPPassword = s.has_smtp_password;
      events = e.events || [];
    } catch (err) {
      toast.error(err.message);
    } finally {
      loading = false;
    }
  }

  onMount(load);

  async function save() {
    saving = true;
    try {
      // Coerce numeric fields so an emptied input ("") doesn't break
      // the JSON decode on the backend (ints only).
      cfg.smtp_port = Number(cfg.smtp_port) || 0;
      cfg.disk_free_percent = Number(cfg.disk_free_percent) || 0;
      cfg.check_interval_sec = Number(cfg.check_interval_sec) || 60;
      const body = { config: cfg, clear_secret: clearSecrets };
      if (webhookSecret) body.webhook_secret = webhookSecret;
      if (smtpUser) body.smtp_user = smtpUser;
      if (smtpPassword) body.smtp_password = smtpPassword;
      const s = await api.updateNotifyConfig(body);
      cfg = s.config || {};
      hasWebhookSecret = s.has_webhook_secret;
      hasSMTPUser = s.has_smtp_user;
      hasSMTPPassword = s.has_smtp_password;
      webhookSecret = '';
      smtpUser = '';
      smtpPassword = '';
      clearSecrets = false;
      toast.success(t('settings.notificationsSaved'));
    } catch (err) {
      toast.error(err.message);
    } finally {
      saving = false;
    }
  }

  async function sendTest() {
    testing = true;
    try {
      await api.testNotify();
      toast.success(t('settings.notificationsTestSent'));
      await load();
    } catch (err) {
      toast.error(err.message);
    } finally {
      testing = false;
    }
  }

  function fmtTime(iso) {
    if (!iso) return '—';
    const d = new Date(iso);
    return d.toLocaleString();
  }

  const levelColor = {
    info: 'text-muted-foreground',
    warning: 'text-warning',
    critical: 'text-destructive',
  };
</script>

{#if loading}
  <p class="text-sm text-muted-foreground">{t('common.loading')}</p>
{:else}
  <div class="space-y-6 max-w-2xl">
    <!-- Enable alerts -->
    <div class="border border-border rounded-lg p-4 space-y-3">
      <Switch
        bind:checked={cfg.enabled}
        label={t('settings.notificationsEnabled')}
        description={t('settings.notificationsEnabledDesc')}
      />

      <div class="grid grid-cols-2 gap-3 pt-2">
        <div class="space-y-1.5">
          <Label for="disk-threshold">{t('settings.diskThreshold')}</Label>
          <Input
            id="disk-threshold"
            type="number"
            min="0"
            max="99"
            bind:value={cfg.disk_free_percent}
            placeholder="10"
          />
          <p class="text-xs text-muted-foreground">{t('settings.diskThresholdHint')}</p>
        </div>
      </div>
    </div>

    <!-- Webhook -->
    <div class="border border-border rounded-lg p-4 space-y-3">
      <div class="flex items-center justify-between">
        <span class="text-sm font-medium">{t('settings.webhookChannel')}</span>
        <span
          class="text-xs px-2 py-0.5 rounded-full {hasWebhookSecret
            ? 'bg-success/10 text-success'
            : 'bg-muted text-muted-foreground'}"
        >
          {hasWebhookSecret ? t('settings.secretConfigured') : t('settings.secretNotConfigured')}
        </span>
      </div>

      <Switch
        bind:checked={cfg.webhook_enabled}
        label={t('settings.webhookEnabled')}
        description={t('settings.webhookEnabledDesc')}
      />

      <div class="space-y-1.5">
        <Label for="webhook-url">{t('settings.webhookUrl')}</Label>
        <Input
          id="webhook-url"
          bind:value={cfg.webhook_url}
          placeholder="https://hooks.example.com/…"
        />
        <p class="text-xs text-muted-foreground">{t('settings.webhookUrlHint')}</p>
      </div>

      <div class="space-y-1.5">
        <Label for="webhook-secret">{t('settings.webhookSecret')}</Label>
        <Input
          id="webhook-secret"
          type="password"
          bind:value={webhookSecret}
          placeholder={hasWebhookSecret ? t('settings.secretKeepPlaceholder') : ''}
          autocomplete="new-password"
        />
        <p class="text-xs text-muted-foreground">{t('settings.secretKeepHint')}</p>
      </div>
    </div>

    <!-- SMTP -->
    <div class="border border-border rounded-lg p-4 space-y-3">
      <div class="flex items-center justify-between">
        <span class="text-sm font-medium">{t('settings.smtpChannel')}</span>
        <span
          class="text-xs px-2 py-0.5 rounded-full {hasSMTPPassword
            ? 'bg-success/10 text-success'
            : 'bg-muted text-muted-foreground'}"
        >
          {hasSMTPPassword ? t('settings.secretConfigured') : t('settings.secretNotConfigured')}
        </span>
      </div>

      <Switch
        bind:checked={cfg.smtp_enabled}
        label={t('settings.smtpEnabled')}
        description={t('settings.smtpEnabledDesc')}
      />

      <div class="grid grid-cols-2 gap-3">
        <div class="space-y-1.5">
          <Label for="smtp-host">{t('settings.smtpHost')}</Label>
          <Input id="smtp-host" bind:value={cfg.smtp_host} placeholder="smtp.example.com" />
        </div>
        <div class="space-y-1.5">
          <Label for="smtp-port">{t('settings.smtpPort')}</Label>
          <Input id="smtp-port" type="number" bind:value={cfg.smtp_port} placeholder="587" />
        </div>
      </div>

      <div class="grid grid-cols-2 gap-3">
        <div class="space-y-1.5">
          <Label for="smtp-from">{t('settings.smtpFrom')}</Label>
          <Input id="smtp-from" bind:value={cfg.smtp_from} placeholder="webkvm@example.com" />
        </div>
        <div class="space-y-1.5">
          <Label for="smtp-to">{t('settings.smtpTo')}</Label>
          <Input id="smtp-to" bind:value={cfg.smtp_to} placeholder="admin@example.com" />
        </div>
      </div>

      <div class="grid grid-cols-2 gap-3">
        <div class="space-y-1.5">
          <Label for="smtp-user">{t('settings.smtpUser')}</Label>
          <Input
            id="smtp-user"
            bind:value={smtpUser}
            placeholder={hasSMTPUser ? t('settings.secretKeepPlaceholder') : ''}
          />
        </div>
        <div class="space-y-1.5">
          <Label for="smtp-pass">{t('settings.smtpPassword')}</Label>
          <Input
            id="smtp-pass"
            type="password"
            bind:value={smtpPassword}
            placeholder={hasSMTPPassword ? t('settings.secretKeepPlaceholder') : ''}
            autocomplete="new-password"
          />
        </div>
      </div>

      <div class="space-y-2">
        <Switch
          bind:checked={cfg.smtp_tls}
          label={t('settings.smtpTls')}
          description={t('settings.smtpTlsDesc')}
        />
        <Switch
          bind:checked={cfg.smtp_insecure}
          label={t('settings.smtpInsecure')}
          description={t('settings.smtpInsecureDesc')}
        />
      </div>
    </div>

    <!-- Actions -->
    <div class="flex items-center gap-3 pt-2">
      <Button onclick={save} disabled={saving}>
        {saving ? t('common.saving') : t('common.save')}
      </Button>
      <Button variant="outline" onclick={sendTest} disabled={testing || !cfg.enabled}>
        {testing ? t('common.loading') : t('settings.sendTest')}
      </Button>
      <label class="flex items-center gap-2 text-sm text-muted-foreground cursor-pointer">
        <input type="checkbox" bind:checked={clearSecrets} class="w-4 h-4 rounded border-border" />
        {t('settings.clearSecrets')}
      </label>
    </div>

    <!-- History -->
    <div>
      <h3 class="text-sm font-medium mb-2">{t('settings.alertHistory')}</h3>
      {#if events.length === 0}
        <p class="text-sm text-muted-foreground">{t('settings.alertHistoryEmpty')}</p>
      {:else}
        <div class="space-y-1.5 max-h-64 overflow-y-auto">
          {#each events as e (e.time + e.subject)}
            <div class="flex items-start gap-3 border border-border rounded-md px-3 py-2">
              <span
                class="text-xs font-medium {levelColor[e.level] ||
                  'text-muted-foreground'} uppercase w-20 shrink-0"
              >
                {e.level}
              </span>
              <div class="min-w-0 flex-1">
                <div class="text-sm font-medium">{e.subject}</div>
                {#if e.message}
                  <div class="text-xs text-muted-foreground">{e.message}</div>
                {/if}
              </div>
              <span class="text-xs text-muted-foreground shrink-0 tnum">{fmtTime(e.time)}</span>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  </div>
{/if}
