<script>
  import { Skeleton } from '$lib/components/ui/skeleton/index.js';

  /**
   * CardGridSkeleton — loading placeholder for card-grid catalogs
   * (AppStore, ImageHub, Media, SnippetsTab). Mimics a grid of cards
   * with a thumbnail/icon block, a title line, and a couple of
   * secondary text lines, so the loading state doesn't feel like an
   * empty page with a spinner floating in the middle.
   *
   *   <CardGridSkeleton count={8} cols="grid-cols-2 sm:grid-cols-3 lg:grid-cols-4" />
   */
  let {
    count = 8,
    cols = 'grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4',
    thumbnail = true,
  } = $props();
</script>

<div class="grid {cols} gap-4" role="presentation" aria-hidden="true">
  {#each Array(count) as _, i (i)}
    <div class="border border-border rounded-xl bg-card p-4 flex flex-col gap-3">
      {#if thumbnail}
        <Skeleton class="h-24 w-full rounded-lg" />
      {/if}
      <Skeleton class="h-4 w-3/4" />
      <Skeleton class="h-3 w-1/2" />
      <Skeleton class="h-3 w-full" />
    </div>
  {/each}
</div>
