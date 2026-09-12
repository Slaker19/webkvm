<script>
  /**
   * SettingsTab — renders a section's fields with a clean
   * Proxmox-style two-column layout.
   *
   * The parent (Settings.svelte) hands us the field list and the
   * combined `values ∪ editing` map; we render each field with a
   * type-aware editor. Under each input we show:
   *   - the i18n "live" / "restart required" badge,
   *   - a "modified" marker when the field has an unsaved edit,
   *   - and, when the value differs from its stock default, a
   *     one-click "reset to default" affordance.
   * Server-side validation failures arrive as `errors` (keyed by
   * field key) and are rendered inline in red.
   */
  import { Input } from '$lib/components/ui/input';
  import Icon from '$lib/components/Icon.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let { fields, values, editing, errors = {}, onChange } = $props();

  function currentValue(f) {
    if (editing[f.key] !== undefined) return editing[f.key];
    if (values[f.key] !== undefined) return values[f.key];
    return f.default;
  }

  function stringValue(f) {
    const v = currentValue(f);
    if (v == null) return '';
    if (f.type === 'list') return Array.isArray(v) ? v.join(', ') : '';
    return String(v);
  }

  // Loose equality that treats lists structurally and numeric strings
  // like their number (the int editor round-trips through a string).
  function sameValue(a, b) {
    if (Array.isArray(a) || Array.isArray(b)) {
      return JSON.stringify(a ?? []) === JSON.stringify(b ?? []);
    }
    if (a == null && b == null) return true;
    return a === b || String(a) === String(b);
  }

  function isPending(f) {
    return editing[f.key] !== undefined;
  }

  function isModified(f) {
    return !sameValue(currentValue(f), f.default);
  }

  function resetField(f) {
    onChange(f.key, f.default);
  }

  function handleChange(f, raw) {
    switch (f.type) {
      case 'bool':
        onChange(f.key, raw === true || raw === 'true' || raw === 'on');
        return;
      case 'int':
        onChange(f.key, Number.isFinite(+raw) ? Math.trunc(+raw) : raw);
        return;
      case 'list':
        onChange(
          f.key,
          raw
            .split(',')
            .map((s) => s.trim())
            .filter(Boolean)
        );
        return;
      default:
        onChange(f.key, raw);
    }
  }
</script>

<div class="border border-border rounded-lg bg-card divide-y divide-border max-w-3xl">
  {#each fields as f (f.key)}
    <div
      class="grid grid-cols-1 sm:grid-cols-[200px_1fr] gap-2 sm:gap-4 px-4 py-3 {errors[f.key]
        ? 'bg-destructive/5'
        : ''}"
    >
      <div class="flex items-center gap-1.5">
        <label for={f.key} class="text-sm font-medium">{f.label}</label>
        {#if f.description}
          <span
            class="inline-flex items-center justify-center w-3.5 h-3.5 rounded-full bg-muted text-muted-foreground text-[10px] cursor-help"
            title={f.description}
          >
            ?
          </span>
        {/if}
        {#if isModified(f)}
          <button
            type="button"
            title={t('settings.resetToDefault')}
            aria-label={t('settings.resetToDefault')}
            onclick={() => resetField(f)}
            class="text-muted-foreground hover:text-accent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 rounded"
          >
            <Icon name="resetDefault" size={13} />
          </button>
        {/if}
      </div>
      <div class="flex flex-col gap-1.5">
        {#if f.type === 'bool'}
          <label class="flex items-center gap-2 cursor-pointer">
            <input
              id={f.key}
              type="checkbox"
              checked={!!currentValue(f)}
              onchange={(e) => handleChange(f, e.currentTarget.checked)}
              class="rounded"
            />
            <span class="text-sm">{currentValue(f) ? 'On' : 'Off'}</span>
          </label>
        {:else if f.type === 'enum'}
          <select
            id={f.key}
            value={currentValue(f)}
            onchange={(e) => handleChange(f, e.currentTarget.value)}
            class="h-9 rounded-lg border border-border bg-background px-2 text-sm"
          >
            {#each f.enum || [] as opt}
              <option value={opt}>{opt}</option>
            {/each}
          </select>
        {:else if f.type === 'int'}
          <Input
            id={f.key}
            type="number"
            value={stringValue(f)}
            placeholder={f.placeholder}
            min={f.min ?? undefined}
            max={f.max ?? undefined}
            oninput={(e) => handleChange(f, e.currentTarget.value)}
          />
        {:else if f.type === 'duration'}
          <Input
            id={f.key}
            type="text"
            value={stringValue(f)}
            placeholder="e.g. 5m, 1h30m"
            oninput={(e) => handleChange(f, e.currentTarget.value)}
            class="font-mono"
          />
        {:else if f.type === 'list'}
          <Input
            id={f.key}
            type="text"
            value={stringValue(f)}
            placeholder="comma-separated"
            oninput={(e) => handleChange(f, e.currentTarget.value)}
          />
        {:else}
          <Input
            id={f.key}
            type="text"
            value={stringValue(f)}
            placeholder={f.placeholder}
            oninput={(e) => handleChange(f, e.currentTarget.value)}
          />
        {/if}
        <div class="flex items-center gap-2 text-[11px] text-muted-foreground">
          {#if f.hot_reload}
            <span class="text-success">{t('settings.badgeLive')}</span>
          {:else}
            <span class="text-warning">{t('settings.badgeRestart')}</span>
          {/if}
          {#if isPending(f)}
            <span class="text-accent">• {t('settings.modified')}</span>
          {/if}
        </div>
        {#if errors[f.key]}
          <p class="text-[11px] text-destructive">{errors[f.key]}</p>
        {/if}
      </div>
    </div>
  {/each}
</div>
