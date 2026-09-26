<script>
  import SearchInput from '../components/SearchInput.svelte';

  let { onInput = () => {}, debounce = 0 } = $props();
  let search = $state('');
</script>

<SearchInput bind:value={search} {onInput} {debounce} />
<span data-testid="parent">{search}</span>
