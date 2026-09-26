<script>
  /**
   * OsPresetPicker — one-click hardware profiles for KVM guests.
   *
   * The five tiles (Linux / Win11 / Win10 / BSD / Legacy) used to be
   * five near-identical 20-line blocks. They are data now; the parent
   * still owns what each preset actually applies to the form.
   */
  import Icon from '$lib/components/Icon.svelte';
  import { t } from '$lib/i18n.svelte.js';

  let { value = $bindable('linux'), onselect = null } = $props();

  const presets = $derived([
    {
      id: 'linux',
      icon: '/os/linux.svg',
      alt: 'Linux',
      label: t('vmCreate.osPresetLinux'),
      desc: t('vmCreate.osPresetLinuxDesc'),
    },
    {
      id: 'win11',
      icon: '/os/windows.svg',
      alt: 'Windows 11',
      label: t('vmCreate.osPresetWin11'),
      desc: t('vmCreate.osPresetWin11Desc'),
    },
    {
      id: 'win10',
      icon: '/os/windows.svg',
      alt: 'Windows 10',
      label: t('vmCreate.osPresetWin10'),
      desc: t('vmCreate.osPresetWin10Desc'),
    },
    {
      id: 'bsd',
      icon: '/os/freebsd.svg',
      alt: 'BSD',
      label: t('vmCreate.osPresetBsd'),
      desc: t('vmCreate.osPresetBsdDesc'),
    },
    {
      id: 'legacy',
      icon: '/os/windows-legacy.svg',
      alt: 'Legacy',
      label: t('vmCreate.osPresetLegacy'),
      desc: t('vmCreate.osPresetLegacyDesc'),
    },
  ]);
</script>

<div class="pt-4 mt-2 border-t border-border/60 space-y-2.5">
  <div class="flex items-center justify-between">
    <span class="text-xs font-semibold text-foreground flex items-center gap-1.5">
      <Icon name="sparkles" size={13} class="text-accent" />
      <span>{t('vmCreate.osProfileTitle')}</span>
    </span>
    <span class="text-[11px] text-muted-foreground font-mono">
      {t('vmCreate.osProfileSubtitle')}
    </span>
  </div>
  <div
    class="grid grid-cols-2 sm:grid-cols-5 gap-2"
    role="radiogroup"
    aria-label={t('vmCreate.osProfileTitle')}
  >
    {#each presets as preset (preset.id)}
      {@const isActive = value === preset.id}
      <button
        type="button"
        role="radio"
        aria-checked={isActive}
        onclick={() => {
          value = preset.id;
          onselect?.(preset.id);
        }}
        class="flex flex-col items-center justify-center p-2.5 rounded-xl border text-center transition-all cursor-pointer {isActive
          ? 'border-accent bg-accent/10 shadow-xs ring-1 ring-accent/30'
          : 'border-border bg-background hover:bg-muted/40'}"
      >
        <img
          src={preset.icon}
          alt=""
          aria-hidden="true"
          class="w-9 h-9 object-contain mb-1.5"
          draggable="false"
        />
        <span class="text-xs font-bold text-foreground">{preset.label}</span>
        <span class="text-[10px] text-muted-foreground">{preset.desc}</span>
      </button>
    {/each}
  </div>
</div>
