<script>
  // Profile picture with an initials fallback. The fallback is not just
  // for "no picture set": it also catches a reference whose file was
  // deleted, so a missing image degrades to initials instead of a broken
  // image icon.
  import { isMediaRef } from '$lib/utils/mediaRef.js';
  import { cn } from '$lib/utils.js';

  let { src = '', name = '', size = 28, class: className = '' } = $props();

  let failed = $state(false);
  // Reset the failure flag when the source changes, otherwise picking a
  // new picture after a broken one would stay stuck on initials.
  $effect(() => {
    if (src !== undefined) {
      failed = false;
    }
  });

  const safeSrc = $derived(isMediaRef(src) ? src : '');
  const initials = $derived(
    (name || '')
      .trim()
      .split(/[\s._-]+/)
      .filter(Boolean)
      .slice(0, 2)
      .map((w) => w[0].toUpperCase())
      .join('') || '?'
  );
</script>

<span
  class={cn(
    'inline-flex items-center justify-center rounded-full overflow-hidden bg-accent/15 text-accent font-medium shrink-0 select-none',
    className
  )}
  style="width:{size}px;height:{size}px;font-size:{Math.round(size * 0.4)}px"
  title={name || undefined}
>
  {#if safeSrc && !failed}
    <img src={safeSrc} alt="" class="w-full h-full object-cover" onerror={() => (failed = true)} />
  {:else}
    {initials}
  {/if}
</span>
