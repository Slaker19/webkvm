<script lang="ts">
  import type { HTMLAttributes } from 'svelte/elements';
  import { cn, type WithElementRef } from '$lib/utils.js';

  let {
    ref = $bindable(null),
    class: className,
    children,
    size = 'default',
    interactive = false,
    ...restProps
  }: WithElementRef<HTMLAttributes<HTMLDivElement>> & {
    size?: 'default' | 'sm';
    /** Adds the hover border/shadow lift used for clickable cards
     * (stat tiles, list items). Static cards (forms, settings panels)
     * leave this off. */
    interactive?: boolean;
  } = $props();
</script>

<div
  bind:this={ref}
  data-slot="card"
  data-size={size}
  class={cn(
    'bg-card text-card-foreground gap-4 overflow-hidden rounded-lg border border-border py-4 text-sm transition-[border-color,box-shadow] duration-200 ease-out has-data-[slot=card-footer]:pb-0 has-[>img:first-child]:pt-0 data-[size=sm]:gap-3 data-[size=sm]:py-3 data-[size=sm]:has-data-[slot=card-footer]:pb-0 *:[img:first-child]:rounded-t-lg *:[img:last-child]:rounded-b-lg group/card flex flex-col',
    interactive && 'hover:border-border-hover hover:shadow-md',
    className
  )}
  {...restProps}
>
  {@render children?.()}
</div>
