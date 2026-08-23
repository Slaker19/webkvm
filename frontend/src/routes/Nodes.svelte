<script>
  import { t } from '../lib/i18n.svelte.js';
  import PageHeader from '$lib/components/PageHeader.svelte';
</script>

<div class="p-6 max-w-5xl">
  <PageHeader title={t('nodes.title')} subtitle={t('nodes.subtitle')} />

  <div class="flex flex-col items-center justify-center py-24 text-center">
    <p class="text-lg font-medium text-muted-foreground">{t('nodes.comingSoon')}</p>
    <p class="text-sm text-muted-foreground mt-1">{t('nodes.comingSoonDesc')}</p>
  </div>
</div>
