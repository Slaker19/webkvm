<script>
  import { fly } from 'svelte/transition';
  import { auth, api } from './lib/stores/auth.svelte.js';
  import { getRoute, navigate } from './lib/router.svelte.js';
  import { events } from './lib/stores/events.svelte.js';
  import Login from './routes/Login.svelte';
  import Layout from './lib/components/Layout.svelte';
  import VmList from './routes/VmList.svelte';
  import VmDetail from './routes/VmDetail.svelte';
  import VmCreate from './routes/VmCreate.svelte';
  import Storage from './routes/Storage.svelte';
  import Networks from './routes/Networks.svelte';
  import Users from './routes/Users.svelte';
  import Nodes from './routes/Nodes.svelte';
  import Backup from './routes/Backup.svelte';
  import Status from './routes/Status.svelte';
  import HostConsole from './routes/HostConsole.svelte';
  import Settings from './routes/Settings.svelte';
  import Account from './routes/Account.svelte';
  import NotFound from './routes/NotFound.svelte';
  import AccessDenied from './routes/AccessDenied.svelte';
  import CommandPalette from './lib/components/CommandPalette.svelte';
  import KeyboardShortcuts from './lib/components/KeyboardShortcuts.svelte';
  import { Toaster } from './lib/components/ui/toast';

  const route = $derived(getRoute());

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
        {#if route.name === 'vms'}
          <VmList />
        {:else if route.name === 'vms-new'}
          <VmCreate />
        {:else if route.name === 'vm-detail'}
          <VmDetail vmId={route.params.id} />
        {:else if route.name === 'storage'}
          <Storage />
        {:else if route.name === 'networks'}
          <Networks />
        {:else if route.name === 'users'}
          <Users />
        {:else if route.name === 'nodes'}
          <Nodes />
        {:else if route.name === 'backup'}
          <Backup />
        {:else if route.name === 'status'}
          <Status />
        {:else if route.name === 'host-console'}
          <HostConsole />
        {:else if route.name === 'settings'}
          <Settings />
        {:else if route.name === 'account'}
          <Account />
        {:else if route.name === 'not-found'}
          <NotFound />
        {:else}
          <NotFound />
        {/if}
      </div>
    {/key}
  </Layout>
{/if}

<CommandPalette />
<KeyboardShortcuts />
