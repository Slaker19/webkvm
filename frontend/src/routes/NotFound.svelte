<script>
  import { navigate } from '../lib/router.svelte.js';
  import { t } from '../lib/i18n.svelte.js';
  import { Button } from '$lib/components/ui/button';
  import EmptyState from '$lib/components/EmptyState.svelte';
</script>

<div class="p-4 sm:p-6 w-full max-w-2xl mx-auto">
  <EmptyState icon="empty" title={t('common.notFound')} description={t('common.notFoundDesc')}>
    {#snippet action()}
      <Button onclick={() => navigate('/vms')}>{t('common.backToVms')}</Button>
    {/snippet}
  </EmptyState>
</div>
