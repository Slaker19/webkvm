<script>
  /**
   * CronPicker — visual cron expression editor.
   *
   * Produces a standard 5-field cron string (minute hour day-of-month
   * month day-of-week) bound to a $bindable `expression` prop. Three
   * presets cover the common cases (hourly / daily 2am / weekly Sun
   * 3am) and a "Custom" mode reveals five dropdowns for fine-grained
   * control. The current value is always rendered as a humanised
   * sentence via cronstrue, so the user sees "Every day at 02:00 AM"
   * instead of the raw "0 2 * * *".
   *
   * Usage:
   *   <CronPicker bind:expression={newScheduleCron} />
   *
   * The component is intentionally write-only on the dropdowns: it
   * parses the incoming expression on mount to pick the closest
   * preset, and from then on it owns the value until the parent
   * changes it from the outside (which re-runs the parse).
   */
  import cronstrue from 'cronstrue';

  let { expression = $bindable('0 2 * * *') } = $props();

  // --- Presets ---------------------------------------------------------
  // The first three are the "common" picks a user reaches for. Any
  // expression that doesn't match one of them lands in "Custom" and
  // reveals the dropdowns.
  const PRESETS = [
    { label: 'Hourly', expression: '0 * * * *', match: '0 * * * *' },
    { label: 'Daily 2 AM', expression: '0 2 * * *', match: '0 2 * * *' },
    { label: 'Weekly Sun 3 AM', expression: '0 3 * * 0', match: '0 3 * * 0' },
  ];

  // --- Mode ------------------------------------------------------------
  // 'preset' = the expression is one of PRESETS (and the right button
  // is highlighted); 'custom' = user has the dropdowns open. We pick
  // mode on mount based on the incoming expression; switching modes
  // overwrites the expression with the corresponding default.
  let mode = $state(detectMode(expression));

  function detectMode(expr) {
    return PRESETS.some((p) => p.match === expr) ? 'preset' : 'custom';
  }

  // --- Custom-mode fields ---------------------------------------------
  // Parsed from the expression; bound back via updateExpression.
  let minute = $state('0');
  let hour = $state('2');
  let dayOfMonth = $state('*');
  let month = $state('*');
  let dayOfWeek = $state('*');

  // Dropdown options. '*' is "every" for that field. The numeric
  // values are kept as strings so they can be joined with the
  // cronstrue-friendly '*' literal without a cast.
  const minutes = ['*', '0', '15', '30', '45'];
  const hours = [
    '*',
    '0',
    '1',
    '2',
    '3',
    '4',
    '5',
    '6',
    '7',
    '8',
    '9',
    '10',
    '11',
    '12',
    '13',
    '14',
    '15',
    '16',
    '17',
    '18',
    '19',
    '20',
    '21',
    '22',
    '23',
  ];
  const daysOfMonth = ['*', '1', '15'];
  const months = ['*', '1', '2', '3', '4', '5', '6', '7', '8', '9', '10', '11', '12'];
  const daysOfWeek = [
    { value: '*', label: 'Every day' },
    { value: '0', label: 'Sunday' },
    { value: '1', label: 'Monday' },
    { value: '2', label: 'Tuesday' },
    { value: '3', label: 'Wednesday' },
    { value: '4', label: 'Thursday' },
    { value: '5', label: 'Friday' },
    { value: '6', label: 'Saturday' },
  ];

  // Re-parse when the expression changes from outside (parent
  // called us with a saved schedule, etc.). The prevExpression
  // gate is critical: without it, the $effect re-runs whenever
  // the user clicks "Custom" (because switchToCustom writes to
  // `mode` and the dropdown vars), recomputes mode from the
  // unchanged expression, and flips mode right back to "preset"
  // — so the Custom dropdowns would never appear. Tracking only
  // `expression` and short-circuiting on equality fixes that.
  //
  // prevExpression is intentionally a plain `let` rather than
  // $state: we want it to be a non-reactive memory cell, not
  // something the effect tracks. If you ever add a "Reset"
  // button, set prevExpression = null before reassigning
  // expression to force the auto-detect to run again.
  let prevExpression = null;

  $effect(() => {
    const expr = expression;
    if (expr === prevExpression) return;
    prevExpression = expr;

    const parts = expr.trim().split(/\s+/);
    if (parts.length !== 5) return;
    const detected = detectMode(expr);
    mode = detected;
    if (detected === 'custom') {
      [minute, hour, dayOfMonth, month, dayOfWeek] = parts;
    }
  });

  function updateExpression() {
    expression = [minute, hour, dayOfMonth, month, dayOfWeek].join(' ');
  }

  function pickPreset(p) {
    expression = p.expression;
    mode = 'preset';
  }

  function switchToCustom() {
    // When the user clicks Custom, seed the dropdowns from whatever
    // the current expression looks like, even if it's a known preset.
    const parts = expression.trim().split(/\s+/);
    if (parts.length === 5) {
      [minute, hour, dayOfMonth, month, dayOfWeek] = parts;
    }
    mode = 'custom';
  }

  // --- Humaniser -------------------------------------------------------
  // cronstrue throws on garbage; in that case we show the raw
  // expression with a "(invalid)" hint so the user knows something
  // is off. The wrapping `try` is cheap: cronstrue runs in <1ms.
  let humanised = $derived.by(() => {
    try {
      return cronstrue.toString(expression);
    } catch {
      return `${expression} (invalid)`;
    }
  });
</script>

<div class="space-y-2">
  <div class="flex flex-wrap gap-1">
    {#each PRESETS as p}
      <button
        type="button"
        class="px-2.5 py-1 text-xs rounded-md border transition-colors {mode === 'preset' &&
        expression === p.match
          ? 'border-accent bg-accent/10 text-accent'
          : 'border-border bg-background text-muted-foreground hover:text-foreground hover:border-foreground/30'}"
        onclick={() => pickPreset(p)}
      >
        {p.label}
      </button>
    {/each}
    <button
      type="button"
      class="px-2.5 py-1 text-xs rounded-md border transition-colors {mode === 'custom'
        ? 'border-accent bg-accent/10 text-accent'
        : 'border-border bg-background text-muted-foreground hover:text-foreground hover:border-foreground/30'}"
      onclick={switchToCustom}
    >
      Custom
    </button>
  </div>

  {#if mode === 'custom'}
    <div class="grid grid-cols-2 sm:grid-cols-5 gap-2">
      <div>
        <label class="text-xs text-muted-foreground block mb-1" for="cron-min">Minute</label>
        <select
          id="cron-min"
          class="w-full h-8 rounded-lg border border-border bg-background px-2 text-sm"
          bind:value={minute}
          onchange={updateExpression}
        >
          {#each minutes as m}
            <option value={m}>{m === '*' ? 'Every' : m}</option>
          {/each}
        </select>
      </div>
      <div>
        <label class="text-xs text-muted-foreground block mb-1" for="cron-hr">Hour</label>
        <select
          id="cron-hr"
          class="w-full h-8 rounded-lg border border-border bg-background px-2 text-sm"
          bind:value={hour}
          onchange={updateExpression}
        >
          {#each hours as h}
            <option value={h}>{h === '*' ? 'Every' : h.padStart(2, '0')}</option>
          {/each}
        </select>
      </div>
      <div>
        <label class="text-xs text-muted-foreground block mb-1" for="cron-dom">Day</label>
        <select
          id="cron-dom"
          class="w-full h-8 rounded-lg border border-border bg-background px-2 text-sm"
          bind:value={dayOfMonth}
          onchange={updateExpression}
        >
          {#each daysOfMonth as d}
            <option value={d}>{d === '*' ? 'Every' : d}</option>
          {/each}
        </select>
      </div>
      <div>
        <label class="text-xs text-muted-foreground block mb-1" for="cron-mon">Month</label>
        <select
          id="cron-mon"
          class="w-full h-8 rounded-lg border border-border bg-background px-2 text-sm"
          bind:value={month}
          onchange={updateExpression}
        >
          {#each months as m}
            <option value={m}>{m === '*' ? 'Every' : m}</option>
          {/each}
        </select>
      </div>
      <div>
        <label class="text-xs text-muted-foreground block mb-1" for="cron-dow">Weekday</label>
        <select
          id="cron-dow"
          class="w-full h-8 rounded-lg border border-border bg-background px-2 text-sm"
          bind:value={dayOfWeek}
          onchange={updateExpression}
        >
          {#each daysOfWeek as d}
            <option value={d.value}>{d.label}</option>
          {/each}
        </select>
      </div>
    </div>
  {/if}

  <p class="text-xs text-muted-foreground">
    <span class="font-mono text-foreground/70">{expression}</span>
    <span class="mx-1.5">·</span>
    {humanised}
  </p>
</div>
