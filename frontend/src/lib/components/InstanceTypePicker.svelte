<script>
  /**
   * InstanceTypePicker — KVM / LXC / Cloud-Init radio group.
   *
   * Replaces three hand-written <button role="radio"> blocks that only
   * differed by label and accent colour. Arrow keys move between the
   * options, as a real radiogroup should.
   */
  import { t } from '$lib/i18n.svelte.js';

  let { value = $bindable('vm'), onselect = null } = $props();

  const THEMES = {
    accent: {
      border: 'border-accent bg-accent/10 shadow-xs',
      text: 'text-accent',
    },
    warning: {
      border: 'border-warning bg-warning-subtle shadow-xs',
      text: 'text-warning',
    },
    success: {
      border: 'border-success bg-success-subtle shadow-xs',
      text: 'text-success',
    },
  };

  const types = $derived([
    {
      id: 'vm',
      label: t('vmCreate.typeKvm'),
      hint: t('vmCreate.typeKvmHint'),
      theme: 'accent',
    },
    {
      id: 'container',
      label: t('vmCreate.typeLxc'),
      hint: t('vmCreate.typeLxcHint'),
      theme: 'warning',
    },
    {
      id: 'cloudinit',
      label: t('vmCreate.typeCloudInit'),
      hint: t('vmCreate.typeCloudInitHint'),
      theme: 'success',
    },
  ]);

  function select(id) {
    value = id;
    onselect?.(id);
  }

  function onKeydown(e, index) {
    const keys = ['ArrowRight', 'ArrowDown', 'ArrowLeft', 'ArrowUp'];
    if (!keys.includes(e.key)) return;
    e.preventDefault();
    const last = types.length - 1;
    const forward = e.key === 'ArrowRight' || e.key === 'ArrowDown';
    const next = forward ? (index >= last ? 0 : index + 1) : index <= 0 ? last : index - 1;
    const target = types[next];
    if (!target) return;
    select(target.id);
    e.currentTarget.parentElement
      ?.querySelector(`[data-type-id="${CSS.escape(target.id)}"]`)
      ?.focus();
  }
</script>

<div
  class="grid grid-cols-1 sm:grid-cols-3 gap-2 mb-4"
  role="radiogroup"
  aria-label={t('vmCreate.instanceType')}
>
  {#each types as type, i (type.id)}
    {@const isActive = value === type.id}
    {@const theme = THEMES[type.theme]}
    <button
      type="button"
      role="radio"
      data-type-id={type.id}
      aria-checked={isActive}
      tabindex={isActive ? 0 : -1}
      onkeydown={(e) => onKeydown(e, i)}
      onclick={() => select(type.id)}
      class="text-left rounded-lg border p-3 transition-colors cursor-pointer {isActive
        ? theme.border
        : 'border-border hover:border-border-hover bg-background'}"
    >
      <span class="block text-sm font-medium {isActive ? theme.text : 'text-foreground'}">
        {type.label}
      </span>
      <span class="block text-[11px] text-muted-foreground mt-0.5">{type.hint}</span>
    </button>
  {/each}
</div>
