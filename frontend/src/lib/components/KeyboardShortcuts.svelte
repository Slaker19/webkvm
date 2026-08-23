<script>
  import { navigate, getRoute } from '$lib/router.svelte.js';
  import * as Dialog from '$lib/components/ui/dialog';

  let show = $state(false);

  // Group "g x" navigation: pressing g then a letter within 1.2s
  // navigates to that route.
  let gPrefix = false;
  let gPrefixTimer = null;

  function flashGPrefix() {
    gPrefix = true;
    clearTimeout(gPrefixTimer);
    gPrefixTimer = setTimeout(() => (gPrefix = false), 1200);
  }

  function onKey(e) {
    // Skip when an input/textarea has focus (so /, g, ?, c
    // don't break form fields). Skip if a modifier is held
    // (⌘K already works through the CommandPalette).
    const t = e.target;
    const isFormField =
      t &&
      (t.tagName === 'INPUT' ||
        t.tagName === 'TEXTAREA' ||
        t.tagName === 'SELECT' ||
        t.isContentEditable);
    if (isFormField) return;
    if (e.metaKey || e.ctrlKey || e.altKey) return;

    // Global shortcuts.
    if (e.key === '?') {
      e.preventDefault();
      show = !show;
      return;
    }
    if (e.key === 'g') {
      flashGPrefix();
      return;
    }
    if (gPrefix) {
      const map = { v: '/vms', s: '/storage', n: '/networks', u: '/users', a: '/account' };
      if (map[e.key]) {
        e.preventDefault();
        navigate(map[e.key]);
        clearTimeout(gPrefixTimer);
        gPrefix = false;
        return;
      }
    }
    // "/" — focus the first search input on the page. Currently
    // only the VMs list has one; fall back to opening the
    // command palette.
    if (e.key === '/') {
      e.preventDefault();
      const sel = document.querySelector('input[type="search"]');
      if (sel) {
        sel.focus();
        sel.select();
      } else {
        window.dispatchEvent(new CustomEvent('open-command-palette'));
      }
    }
    // "c" — create VM.
    if (e.key === 'c' && !gPrefix) {
      const route = getRoute();
      if (route.name === 'vms' || route.name === 'vm-detail') {
        e.preventDefault();
        navigate('/vms/new');
      }
    }
  }

  $effect(() => {
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });

  const groups = [
    {
      label: 'Navigation',
      shortcuts: [
        { keys: ['g', 'v'], desc: 'Go to Virtual Machines' },
        { keys: ['g', 's'], desc: 'Go to Storage' },
        { keys: ['g', 'n'], desc: 'Go to Networks' },
        { keys: ['g', 'u'], desc: 'Go to Users' },
        { keys: ['g', 'a'], desc: 'Go to Account' },
      ],
    },
    {
      label: 'Actions',
      shortcuts: [
        { keys: ['/'], desc: 'Focus the page search field' },
        { keys: ['c'], desc: 'Create a new VM (when on the VMs list)' },
        { keys: ['Esc'], desc: 'Close dialogs and palettes' },
      ],
    },
    {
      label: 'Help',
      shortcuts: [
        { keys: ['⌘', 'K'], desc: 'Open the command palette' },
        { keys: ['?'], desc: 'Show this cheatsheet' },
      ],
    },
  ];
</script>

<Dialog.Root open={show} onOpenChange={(v) => (show = v)}>
  <Dialog.Content>
    <Dialog.Header>
      <Dialog.Title>Keyboard shortcuts</Dialog.Title>
      <Dialog.Description
        >Press <kbd class="text-xs font-mono px-1.5 py-0.5 rounded bg-muted">?</kbd> any time to show
        this again.</Dialog.Description
      >
    </Dialog.Header>
    <div class="space-y-4 py-2">
      {#each groups as g}
        <div>
          <div class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">
            {g.label}
          </div>
          <div class="space-y-1">
            {#each g.shortcuts as s}
              <div
                class="flex items-center justify-between text-sm py-1 border-b border-border/50 last:border-0"
              >
                <span class="text-muted-foreground">{s.desc}</span>
                <span class="flex items-center gap-1">
                  {#each s.keys as k, i}
                    <kbd
                      class="text-xs font-mono px-1.5 py-0.5 rounded bg-muted border border-border text-foreground min-w-[1.5rem] text-center"
                      >{k}</kbd
                    >
                    {#if i < s.keys.length - 1}<span class="text-muted-foreground text-xs">+</span
                      >{/if}
                  {/each}
                </span>
              </div>
            {/each}
          </div>
        </div>
      {/each}
    </div>
  </Dialog.Content>
</Dialog.Root>
