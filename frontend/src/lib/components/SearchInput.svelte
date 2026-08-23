<script>
  /**
   * SearchInput — search field with clear button + optional debounce.
   *
   * Replaces the bespoke `<Input type="search">` calls in VmList
   * and Users. Two-way bind to `value`. The `onInput` callback
   * receives the value with a 250ms debounce so list filters
   * don't re-render on every keystroke.
   *
   *   <SearchInput bind:value={search} placeholder="Filter VMs..." />
   */
  import { Input } from '$lib/components/ui/input';
  import { t } from '../i18n.svelte.js';

  let {
    value = $bindable(''),
    placeholder = t('common.search'),
    autofocus = false,
    debounce = 0, // ms; 0 = no debounce, calls onInput synchronously
    onInput = () => {},
    class: className = '',
  } = $props();

  let raw = $derived(value);
  let timer;

  function fire() {
    clearTimeout(timer);
    if (debounce > 0) {
      timer = setTimeout(() => onInput(raw), debounce);
    } else {
      onInput(raw);
    }
  }

  function clear() {
    raw = '';
    value = '';
    clearTimeout(timer);
    onInput('');
  }
</script>

<div class="relative {className}">
  <svg
    class="absolute left-2.5 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground pointer-events-none"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    viewBox="0 0 24 24"
  >
    <circle cx="11" cy="11" r="7" />
    <line x1="21" y1="21" x2="16.65" y2="16.65" />
  </svg>
  <Input
    type="search"
    bind:value={raw}
    {placeholder}
    {autofocus}
    oninput={fire}
    class="pl-8 pr-8"
  />
  {#if raw}
    <button
      type="button"
      onclick={clear}
      class="absolute right-1.5 top-1/2 -translate-y-1/2 p-1 text-muted-foreground hover:text-foreground"
      aria-label={t('common.clearSearch')}
    >
      <svg
        class="w-3.5 h-3.5"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        viewBox="0 0 24 24"
      >
        <line x1="18" y1="6" x2="6" y2="18" />
        <line x1="6" y1="6" x2="18" y2="18" />
      </svg>
    </button>
  {/if}
</div>
