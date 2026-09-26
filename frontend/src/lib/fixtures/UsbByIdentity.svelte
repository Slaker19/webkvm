<!-- Same list, keyed on the position in the host's device list, which is
     what the fix uses: two identical USB sticks are still two devices. -->
<script>
  let { items = [] } = $props();
</script>

<ul>
  {#each items as dev, i (i)}
    <li>{dev.vendor_id}:{dev.product_id}</li>
  {/each}
</ul>
