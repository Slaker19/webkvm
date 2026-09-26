<script>
  import { onMount } from 'svelte';
  import { fly } from 'svelte/transition';
  import { auth, api } from './lib/stores/auth.svelte.js';
  import { getRoute, navigate } from './lib/router.svelte.js';
  import { events } from './lib/stores/events.svelte.js';
  import { refreshBranding } from './lib/stores/branding.svelte.js';
  import { t } from './lib/i18n.svelte.js';
  import Login from './routes/Login.svelte';
  import Layout from './lib/components/Layout.svelte';
  import NotFound from './routes/NotFound.svelte';
  import AccessDenied from './routes/AccessDenied.svelte';
  import CommandPalette from './lib/components/CommandPalette.svelte';
  import KeyboardShortcuts from './lib/components/KeyboardShortcuts.svelte';
  import { Toaster } from './lib/components/ui/toast';
  import Spinner from './lib/components/Spinner.svelte';
  import { Button } from './lib/components/ui/button';

  const route = $derived(getRoute());

  // Route pages are lazy-loaded (one chunk each) instead of bundled
  // into the single main chunk — before this, every page's code (VM
  // detail, storage, networks, users, backup, settings...) loaded
  // upfront even if the user only ever visits /vms. `routeLoaders` is
  // a static map of literal `import()` calls so Vite can statically
  // analyze and split each one into its own chunk; NotFound/Login/
  // Layout stay eagerly bundled since they're needed immediately or
  // on every route.
  const routeLoaders = {
    vms: () => import('./routes/VmList.svelte'),
    'vms-new': () => import('./routes/VmCreate.svelte'),
    'vm-detail': () => import('./routes/VmDetail.svelte'),
    apps: () => import('./routes/AppStore.svelte'),
    images: () => import('./routes/ImageHub.svelte'),
    multiview: () => import('./routes/MultiView.svelte'),
    storage: () => import('./routes/Storage.svelte'),
    media: () => import('./routes/Media.svelte'),
    networks: () => import('./routes/Networks.svelte'),
    users: () => import('./routes/Users.svelte'),
    nodes: () => import('./routes/Nodes.svelte'),
    snapshots: () => import('./routes/Snapshots.svelte'),
    backup: () => import('./routes/Backup.svelte'),
    firewall: () => import('./routes/Firewall.svelte'),
    audit: () => import('./routes/AuditLog.svelte'),
    status: () => import('./routes/Status.svelte'),
    // NOTE: 'host-console' is deliberately absent — it's rendered
    // separately below so its shell session survives navigation.
    settings: () => import('./routes/Settings.svelte'),
    account: () => import('./routes/Account.svelte'),
  };

  // The currently-resolved page component (or null while loading /
  // for an unknown route, which falls back to NotFound below).
  let PageComponent = $state(null);
  // Set if the dynamic import() itself rejects (chunk 404, network
  // blip, etc). Without this the failure was silent: PageComponent
  // just stayed null forever with no way to recover short of a hard
  // reload.
  let loadError = $state(null);

  $effect(() => {
    const loader = routeLoaders[route.name];
    if (!loader) {
      PageComponent = null;
      loadError = null;
      return;
    }
    let cancelled = false;
    PageComponent = null;
    loadError = null;
    loader()
      .then((m) => {
        // Bail if the route changed again while this chunk was loading
        // — otherwise a slow chunk for a page the user already
        // navigated away from could clobber the current one.
        if (!cancelled) PageComponent = m.default;
      })
      .catch((err) => {
        if (!cancelled) loadError = err;
      });
    return () => {
      cancelled = true;
    };
  });

  function retryLoad() {
    const loader = routeLoaders[route.name];
    if (!loader) return;
    loadError = null;
    loader()
      .then((m) => (PageComponent = m.default))
      .catch((err) => (loadError = err));
  }

  // The Host Console is kept MOUNTED (just visually hidden) once the
  // admin has opened it, so navigating away and back doesn't kill the
  // shell session — that's why it can't go through `routeLoaders`,
  // which swaps a single PageComponent per route.
  //
  // It is, however, still loaded lazily: importing it eagerly pulled
  // @xterm/xterm plus its five addons (~250 kB) into the initial
  // bundle for *every* user, including viewers who can't even open a
  // terminal. Resolving it on first visit keeps the terminal stack out
  // of the critical path while preserving the persistent-session
  // behaviour from then on.
  let HostConsoleComponent = $state(null);
  $effect(() => {
    if (HostConsoleComponent) return; // already resolved — keep it mounted
    if (route.name !== 'host-console') return;
    if (!auth.isLoggedIn || auth.role !== 'admin') return;
    let cancelled = false;
    import('./routes/HostConsole.svelte')
      .then((m) => {
        if (!cancelled) HostConsoleComponent = m.default;
      })
      .catch((err) => {
        if (!cancelled) loadError = err;
      });
    return () => {
      cancelled = true;
    };
  });

  // V13-SEC-01: the session is a cookie, so on load we re-validate it
  // via /auth/me (the cookie rides along). Until that returns, the app
  // shows a bootstrap spinner instead of flashing the login page.
  onMount(() => {
    auth.bootstrap();
    // Branding is unauthenticated, so it can load in parallel and also
    // applies to the login screen.
    refreshBranding();
  });

  // Manage SSE connection lifecycle based on auth state
  $effect(() => {
    if (auth.isLoggedIn) {
      events.connect();
    } else {
      events.disconnect();
    }
  });

  // Force the user through /account when they log in with
  // must_change_password=true. The Account page is the only place
  // that can clear the flag.
  $effect(() => {
    if (auth.isLoggedIn && auth.mustChangePassword) {
      if (route.name !== 'account') {
        navigate('/account');
      }
    }
  });

  // RBAC: if the matched route declares a `roles` list and the
  // current role isn't in it, render AccessDenied.
  const access = $derived.by(() => {
    if (!auth.isLoggedIn) return { allowed: true };
    if (!route.roles) return { allowed: true };
    if (route.roles.includes(auth.role || '')) return { allowed: true };
    return { allowed: false, reason: `Requires role: ${route.roles.join(' or ')}` };
  });

  // On session changes, re-validate the cached user/role by calling
  // /auth/me so a freshly-demoted user doesn't keep stale perms.
  $effect(() => {
    if (auth.isLoggedIn) {
      api
        .me()
        .then((u) => {
          if (u.username !== auth.user || u.role !== auth.role) {
            auth.setSession(u.username, u.role, u.must_change_password);
          }
          // Keep the picture in step with the server, including the case
          // where another session cleared it.
          if ((u.avatar || '') !== auth.avatar) auth.setAvatar(u.avatar);
        })
        .catch(() => {
          /* 401 etc — auth.onUnauthorized() already handled */
        });
    }
  });
</script>

<Toaster />

{#if auth.status === 'checking'}
  <div class="flex items-center justify-center min-h-screen bg-background">
    <Spinner size="lg" />
  </div>
{:else if !auth.isLoggedIn}
  <Login />
{:else if !access.allowed}
  <Layout>
    <AccessDenied />
  </Layout>
{:else}
  <Layout>
    {#if auth.isLoggedIn && auth.role === 'admin' && HostConsoleComponent}
      <div class={route.name === 'host-console' ? 'block h-full' : 'hidden'}>
        <HostConsoleComponent />
      </div>
    {:else if route.name === 'host-console' && auth.isLoggedIn && auth.role === 'admin'}
      <div class="flex items-center justify-center py-24"><Spinner size="lg" /></div>
    {/if}

    {#if route.name !== 'host-console'}
      {#key route.name + (route.params.id || '')}
        <!-- 200ms matches --motion-slow (app.css): a full route swap is the
             largest-surface transition in the app. -->
        <div in:fly={{ y: 6, duration: 200 }}>
          {#if !routeLoaders[route.name]}
            <NotFound />
          {:else if loadError}
            <div class="flex flex-col items-center justify-center py-24 gap-3">
              <p class="text-sm text-destructive">{t('layout.pageLoadFailed')}</p>
              <Button size="sm" variant="outline" onclick={retryLoad}>{t('layout.retry')}</Button>
            </div>
          {:else if !PageComponent}
            <div class="flex items-center justify-center py-24"><Spinner size="lg" /></div>
          {:else if route.name === 'vm-detail'}
            <PageComponent vmId={route.params.id} />
          {:else}
            <PageComponent />
          {/if}
        </div>
      {/key}
    {/if}
  </Layout>
{/if}

<CommandPalette />
<KeyboardShortcuts />
