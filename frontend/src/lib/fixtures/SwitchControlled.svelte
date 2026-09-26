<script>
  // Parent pattern used by VmDetail (autostart / share toggle): the value
  // is passed WITHOUT bind, `onchange` saves, and a failure puts the
  // parent's state back to the value it already had.
  import Switch from '../components/Switch.svelte';

  let { save } = $props();
  let value = $state(false);

  async function onchange(next) {
    const previous = value;
    try {
      await save(next);
      value = next;
    } catch {
      value = previous;
    }
  }
</script>

<Switch checked={value} {onchange} ariaLabel="controlled" />
<span data-testid="parent">{String(value)}</span>
