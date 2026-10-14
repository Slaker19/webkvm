<script>
  import { t } from '$lib/i18n.svelte.js';
  import BlockCard from '$lib/components/BlockCard.svelte';
  import TerminalPanel from '$lib/components/TerminalPanel.svelte';

  let { vmId, isContainerVm = false } = $props();
</script>

<div id="vm-serial-block">
  <BlockCard
    bid="serial"
    title={isContainerVm ? 'Terminal Shell (xterm)' : t('vmDetail.serialConsoleTitle')}
  >
    <TerminalPanel mode="vm" {vmId} />
  </BlockCard>
</div>
