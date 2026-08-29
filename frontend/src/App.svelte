<script>
  import { fly } from 'svelte/transition';
  import { auth, api } from './lib/stores/auth.svelte.js';
  import { getRoute, navigate } from './lib/router.svelte.js';
  import { events } from './lib/stores/events.svelte.js';
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
    storage: () => import('./routes/Storage.svelte'),
    networks: () => import('./routes/Networks.svelte'),
    users: () => import('./routes/Users.svelte'),
    nodes: () => import('./routes/Nodes.svelte'),
    snapshots: () => import('./routes/Snapshots.svelte'),
    backup: () => import('./routes/Backup.svelte'),
    status: () => import('./routes/Status.svelte'),
    'host-console': () => import('./routes/HostConsole.svelte'),
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

  // Manage SSE connection lifecycle based on auth state
  $effect(() => {
    if (auth.token) {
      events.connect();
    } else {
      events.disconnect();
    }
  });

  // Force the user through /account when they log in with
  // must_change_password=true. The Account page is the only place
  // that can clear the flag.
  $effect(() => {
    if (auth.token && auth.mustChangePassword) {
      if (route.name !== 'account') {
        navigate('/account');
      }
    }
  });

  // RBAC: if the matched route declares a `roles` list and the
  // current role isn't in it, render AccessDenied.
  const access = $derived.by(() => {
    if (!auth.token) return { allowed: true };
    if (!route.roles) return { allowed: true };
    if (route.roles.includes(auth.role || '')) return { allowed: true };
    return { allowed: false, reason: `Requires role: ${route.roles.join(' or ')}` };
  });

  // On token rotation, re-validate the cached user/role by calling
  // /auth/me so a freshly-demoted user doesn't keep stale perms.
  $effect(() => {
    if (auth.token) {
      api
        .me()
        .then((u) => {
          if (u.username !== auth.user || u.role !== auth.role) {
            auth.setToken(auth.token, u.username, u.role, u.must_change_password);
          }
        })
        .catch(() => {
          /* 401 etc — auth.logout() already handled */
        });
    }
  });
</script>

<Toaster />

{#if !auth.token}
  <Login />
{:else if !access.allowed}
  <Layout>
    <AccessDenied />
  </Layout>
{:else}
  <Layout>
    {#key route.name + (route.params.id || '')}
      <div in:fly={{ y: 6, duration: 180 }}>
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
  </Layout>
{/if}

<CommandPalette />
<KeyboardShortcuts />
