<script lang="ts" module>
  import { type VariantProps, tv } from 'tailwind-variants';

  export const sheetVariants = tv({
    base: 'bg-popover text-popover-foreground data-open:animate-in data-closed:animate-out fixed z-50 flex flex-col gap-4 p-4 shadow-lg ring-1 ring-foreground/10 outline-none',
    variants: {
      side: {
        right:
          'data-closed:slide-out-to-right data-open:slide-in-from-right inset-y-0 right-0 h-full w-3/4 border-l sm:max-w-md duration-300 ease-out',
        left: 'data-closed:slide-out-to-left data-open:slide-in-from-left inset-y-0 left-0 h-full w-3/4 border-r sm:max-w-md duration-300 ease-out',
        top: 'data-closed:slide-out-to-top data-open:slide-in-from-top inset-x-0 top-0 h-auto border-b duration-300 ease-out',
        bottom:
          'data-closed:slide-out-to-bottom data-open:slide-in-from-bottom inset-x-0 bottom-0 h-auto border-t duration-300 ease-out',
      },
    },
    defaultVariants: {
      side: 'right',
    },
  });

  export type SheetSide = VariantProps<typeof sheetVariants>['side'];
</script>

<script lang="ts">
  import { Dialog as DialogPrimitive } from 'bits-ui';
  import SheetPortal from './sheet-portal.svelte';
  import type { Snippet } from 'svelte';
  import * as Sheet from './index.js';
  import { cn, type WithoutChildrenOrChild } from '$lib/utils.js';
  import type { ComponentProps } from 'svelte';
  import { Button } from '$lib/components/ui/button/index.js';
  import XIcon from '@lucide/svelte/icons/x';

  let {
    ref = $bindable(null),
    class: className,
    portalProps,
    side = 'right',
    children,
    showCloseButton = true,
    ...restProps
  }: WithoutChildrenOrChild<DialogPrimitive.ContentProps> & {
    portalProps?: WithoutChildrenOrChild<ComponentProps<typeof SheetPortal>>;
    side?: SheetSide;
    children: Snippet;
    showCloseButton?: boolean;
  } = $props();
</script>

<SheetPortal {...portalProps}>
  <Sheet.Overlay />
  <DialogPrimitive.Content
    bind:ref
    data-slot="sheet-content"
    class={cn(sheetVariants({ side }), className)}
    {...restProps}
  >
    {@render children?.()}
    {#if showCloseButton}
      <DialogPrimitive.Close data-slot="sheet-close">
        {#snippet child({ props })}
          <Button variant="ghost" class="absolute top-3 right-3" size="icon-sm" {...props}>
            <XIcon />
            <span class="sr-only">Close</span>
          </Button>
        {/snippet}
      </DialogPrimitive.Close>
    {/if}
  </DialogPrimitive.Content>
</SheetPortal>
