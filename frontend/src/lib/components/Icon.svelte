<script>
  /**
   * Icon — declarative wrapper around @lucide/svelte.
   *
   * Usage: <Icon name="computer" size={16} />
   *
   * This exists so the navigation items, header icons, and any
   * other place that needs a pictogram don't have to import a
   * specific lucide component (which would force us to keep
   * parallel lists of "name → component" in multiple files).
   *
   * Sizes are in pixels (not Tailwind classes) because they map
   * directly to lucide's `size` prop. Pass `class` for color /
   * stroke overrides (e.g. `class="text-muted-foreground"`).
   */
  import {
    Server,
    HardDrive,
    Network,
    Users,
    Settings as SettingsIcon,
    Activity,
    Archive,
    Computer,
    Terminal,
    Power,
    LogOut,
    Search,
    ChevronLeft,
    ChevronRight,
    Menu,
    X,
    Plus,
    Trash2,
    Pencil,
    Eye,
    EyeOff,
    Info,
    AlertTriangle,
    AlertCircle,
    CheckCircle2,
    Check,
    Loader2,
    RefreshCw,
    Download,
    Upload,
    Copy,
    Play,
    Square,
    RotateCw,
    Pause,
    Camera,
    Save,
    HelpCircle,
  } from '@lucide/svelte';

  const registry = {
    server: Server,
    hardDrive: HardDrive,
    network: Network,
    users: Users,
    settings: SettingsIcon,
    activity: Activity,
    archive: Archive,
    computer: Computer,
    terminal: Terminal,
    power: Power,
    logOut: LogOut,
    search: Search,
    chevronLeft: ChevronLeft,
    chevronRight: ChevronRight,
    menu: Menu,
    x: X,
    plus: Plus,
    trash: Trash2,
    pencil: Pencil,
    eye: Eye,
    eyeOff: EyeOff,
    info: Info,
    warning: AlertTriangle,
    error: AlertCircle,
    success: CheckCircle2,
    check: Check,
    spinner: Loader2,
    refresh: RefreshCw,
    download: Download,
    upload: Upload,
    copy: Copy,
    play: Play,
    stop: Square,
    restart: RotateCw,
    pause: Pause,
    camera: Camera,
    save: Save,
    help: HelpCircle,
  };

  let { name, size = 16, class: className = '' } = $props();

  const Component = $derived(registry[name]);
</script>

{#if Component}
  <Component {size} class={className} strokeWidth={1.75} />
{:else}
  <!-- Fallback: render a small dot so missing icons are obvious in QA. -->
  <span
    class="inline-block rounded-full bg-muted {className}"
    style="width: {size}px; height: {size}px"
    aria-hidden="true"
  ></span>
{/if}
