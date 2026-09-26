<script>
  /**
   * PageHeader — standard top-of-page title block.
   *
   * Replaces the recurring `<div class="flex items-center justify-between mb-6 ...">`
   * + `<h1 class="text-xl ...">` + `<p class="text-sm text-muted-foreground">`
   * triple that opens every route.
   *
   *   <PageHeader title="Users" subtitle="Manage user accounts and roles">
   *     {#snippet actions()}
   *       <Button>Add</Button>
   *     {/snippet}
   *   </PageHeader>
   */
  let { title, subtitle = '', actions } = $props();
</script>

<header class="flex flex-wrap items-center justify-between mb-6 gap-x-4 gap-y-2">
  <!-- basis-48: with a zero flex-basis the title never forced the actions
       to wrap, so on a phone it was squeezed to "C" / "C…" instead. -->
  <div class="min-w-0 flex-1 basis-48">
    <h1 class="text-xl font-semibold tracking-tight truncate">{title}</h1>
    {#if subtitle}
      <p class="text-sm text-muted-foreground mt-0.5 truncate min-w-0">{subtitle}</p>
    {/if}
  </div>
  {#if actions}
    <!-- max-w-full: a row of buttons wider than a phone screen must wrap
         inside the header instead of overflowing it (Firewall's
         "Importar" used to be clipped at ~400px). -->
    <div class="flex flex-wrap items-center gap-2 shrink-0 max-w-full">
      {@render actions()}
    </div>
  {/if}
</header>
