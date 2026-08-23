<script>
  /**
   * SettingsTab — renders a section's fields with a clean
   * Proxmox-style two-column layout.
   *
   * The parent (Settings.svelte) hands us the field list and the
   * combined `values ∪ editing` map; we render each field with a
   * type-aware editor. Field-level "live" / "restart" badges sit
   * under the input on the right.
   *
   * Layout:
   *   - Single column for narrow screens, two columns (label
   *     | input) from `sm:` up.
   *   - Label has a `?` icon for a tooltip with the long
   *     description (no more inline helper text that pushes
   *     the input around).
   */
  import { Input } from '$lib/components/ui/input';

  let { fields, values, editing, onChange } = $props();

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
    <div class="grid grid-cols-1 sm:grid-cols-[200px_1fr] gap-2 sm:gap-4 px-4 py-3">
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
        <div class="text-[11px] text-muted-foreground">
          {#if f.hot_reload}
            <span class="text-success">live</span>
          {:else}
            <span class="text-warning">restart required</span>
          {/if}
        </div>
      </div>
    </div>
  {/each}
</div>
