<script>
  import Switch from '../components/Switch.svelte';

  let value = $state(false);
</script>

<Switch bind:checked={value} ariaLabel="bound" />
<span data-testid="parent">{String(value)}</span>
