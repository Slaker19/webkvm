<script>
  import TerminalPanel from '$lib/components/TerminalPanel.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';

  // Embedded host system console (root shell via PTY). Admin-only:
  // enforced client-side by the route roles AND server-side when the
  // ticket is issued and consumed.
</script>

<div class="p-6 space-y-4">
  <PageHeader
    title="Terminal del sistema"
    subtitle="Shell del host embebida — requiere usuario y contraseña del sistema"
  />
  <TerminalPanel mode="host" />
</div>
