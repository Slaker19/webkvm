<script>
  /**
   * VmDetailSkeleton — loading placeholder for VmDetail.svelte.
   *
   * Mirrors the page's actual layout (title row, tab bar, two-column
   * grid with a wide main column and a 280px sidebar) so the initial
   * fetch doesn't collapse into a bare spinner floating in an
   * otherwise-empty page — the most-visited route in the app.
   */
  import { Skeleton } from '$lib/components/ui/skeleton/index.js';
</script>

<div class="space-y-5" role="presentation" aria-hidden="true">
  <!-- Title row -->
  <div class="flex items-center gap-3">
    <Skeleton class="h-6 w-48" />
    <Skeleton class="h-5 w-16 rounded-full" />
  </div>

  <!-- Tab bar -->
  <div class="flex items-center gap-2 border-b border-border pb-2">
    {#each Array(6) as _, i (i)}
      <Skeleton class="h-7 rounded-md" style="width: {60 + (i % 3) * 20}px" />
    {/each}
  </div>

  <div class="grid grid-cols-1 lg:grid-cols-[1fr_280px] gap-5">
    <!-- Main column -->
    <div class="space-y-5">
      <div class="border border-border rounded-lg bg-card p-4 space-y-3">
        <Skeleton class="h-4 w-32" />
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-4">
          {#each Array(4) as _, i (i)}
            <div class="space-y-2">
              <Skeleton class="h-3 w-16" />
              <Skeleton class="h-4 w-20" />
            </div>
          {/each}
        </div>
      </div>
      <div class="border border-border rounded-lg bg-card p-4 space-y-3">
        <Skeleton class="h-4 w-24" />
        <Skeleton class="h-24 w-full" />
      </div>
      <div class="border border-border rounded-lg bg-card p-4 space-y-3">
        <Skeleton class="h-4 w-28" />
        <Skeleton class="h-16 w-full" />
      </div>
    </div>

    <!-- Sidebar -->
    <div class="space-y-4">
      <div class="border border-border rounded-lg bg-card p-4 space-y-2">
        <Skeleton class="h-3 w-20" />
        <Skeleton class="h-9 w-full rounded-md" />
        <Skeleton class="h-9 w-full rounded-md" />
      </div>
      <div class="border border-border rounded-lg bg-card p-4 space-y-2">
        <Skeleton class="h-3 w-24" />
        <Skeleton class="h-3 w-full" />
        <Skeleton class="h-3 w-3/4" />
      </div>
    </div>
  </div>
</div>
