<script>
  /**
   * StatusBadge — pill for a VM/container run state (running/shutoff/
   * paused/crashed), built on the shadcn Badge so it inherits the same
   * variant tokens (success/warning/destructive) used everywhere else,
   * instead of the old bespoke `badge-running` CSS classes (app.css)
   * that only VmList/VmDetail ever consumed via a dot, never a pill.
   *
   * Unknown/other libvirt states (blocked, idle, pmsuspended, …) render
   * as the neutral "shutoff" variant — an unrecognized state must not
   * look like a failure (mirrors lib/utils/vmState.js's dot fallback).
   */
  import { Badge } from '$lib/components/ui/badge';
  import { stateDotClass } from '$lib/utils/vmState.js';
  import { t } from '../i18n.svelte.js';

  const VARIANT = {
    running: 'success',
    shutoff: 'outline',
    paused: 'warning',
    crashed: 'destructive',
    unknown: 'outline',
  };

  const LABEL_KEY = {
    running: 'common.running',
    shutoff: 'common.shutoff',
    paused: 'common.paused',
    crashed: 'common.crashed',
    unknown: 'common.unknown',
  };

  let { state, size = 'default', class: className = '' } = $props();

  const variant = $derived(VARIANT[state] || VARIANT.shutoff);
  // Resolve the fallback BEFORE calling t(): an unrecognized/transient
  // `state` (undefined mid-refresh, or a libvirt state we don't map
  // like "blocked"/"pmsuspended") must fall back to the shutoff label,
  // not to `t(undefined)` — t() only accepts a lookup key, and passing
  // it something that isn't one is a caller bug, not a page crash.
  const label = $derived(t(LABEL_KEY[state] || LABEL_KEY.shutoff));
</script>

<Badge {variant} class="capitalize {size === 'sm' ? 'h-4 text-[10px] px-1.5' : ''} {className}">
  <span class="w-1.5 h-1.5 rounded-full {stateDotClass(state)} shrink-0"></span>
  {label}
</Badge>
