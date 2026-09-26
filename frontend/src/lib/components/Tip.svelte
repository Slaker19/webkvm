<script>
  /**
   * Tip — thin wrapper around the bits-ui Tooltip primitives so call
   * sites don't have to spell out Provider→Root→Trigger→Content for a
   * one-line hint. This is the shadcn Tooltip (lib/components/ui/tooltip)
   * that shipped fully wired but had zero real usages: every "hover for
   * a hint" in the app used the native `title` attribute instead, which
   * is not reachable by keyboard focus and does not show on touch
   * devices at all.
   *
   * Usage:
   *   <Tip text="Restart the VM">
   *     <Button ...>...</Button>
   *   </Tip>
   *
   * The child must be a single focusable element (button, link, icon
   * wrapped in a button) — Tooltip.Trigger renders `asChild`-style by
   * forwarding a snippet, so this component passes the child straight
   * through as the trigger's content.
   *
   * `text` may be empty/undefined, in which case children render
   * unwrapped — lets call sites keep a single conditional `title`-style
   * expression without an `{#if}` around the whole tree.
   */
  import * as Tooltip from '$lib/components/ui/tooltip';

  let { text = '', side = 'top', children } = $props();
</script>

{#if text}
  <Tooltip.Provider>
    <Tooltip.Root>
      <Tooltip.Trigger>
        {@render children?.()}
      </Tooltip.Trigger>
      <Tooltip.Content {side}>
        {text}
      </Tooltip.Content>
    </Tooltip.Root>
  </Tooltip.Provider>
{:else}
  {@render children?.()}
{/if}
