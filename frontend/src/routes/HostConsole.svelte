<script>
  import TerminalPanel from '$lib/components/TerminalPanel.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import { t } from '$lib/i18n.svelte.js';

  // Embedded host system console (PTY via /bin/login). Admin-only:
  // enforced client-side by the route roles AND server-side when the
  // ticket is issued and consumed.
</script>

<div class="p-4 sm:p-6 flex flex-col h-full space-y-4">
  <PageHeader title={t('hostConsole.title')} subtitle={t('hostConsole.subtitle')} />
  <div class="flex-1 min-h-0">
    <TerminalPanel mode="host" fullPage={true} />
  </div>
</div>
