<!-- Reproduces the key expression VmDetail.svelte uses for USB devices. -->
<script>
  let { items = [] } = $props();
</script>

<ul>
  {#each items as dev (dev.vendor_id + ':' + dev.product_id + ':' + (dev.name || ''))}
    <li>{dev.vendor_id}:{dev.product_id}</li>
  {/each}
</ul>
