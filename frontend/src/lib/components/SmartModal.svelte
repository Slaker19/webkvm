<script>
  import { api } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Card } from '$lib/components/ui/card';

  let { disk = null, open = false, onClose = () => {} } = $props();

  let testing = $state(false);
  let loadingSmart = $state(false);
  let smartData = $state(null);

  $effect(() => {
    if (open && disk) {
      smartData = disk.smart;
      refresh();
    }
  });

  async function refresh() {
    if (!disk?.path) return;
    loadingSmart = true;
    try {
      const data = await api.getHostDiskSMART(disk.path, true);
      smartData = data;
    } catch {
      // Fallback silently to existing disk.smart
    } finally {
      loadingSmart = false;
    }
  }

  async function triggerTest(type) {
    if (!disk || !disk.path) return;
    testing = true;
    try {
      await api.runSMARTTest(disk.path, type);
      toast.success(`Autotest ${type} iniciado en ${disk.path}`);
      setTimeout(refresh, 2000);
    } catch (e) {
      toast.error('Error al iniciar autotest: ' + e.message);
    } finally {
      testing = false;
    }
  }

  const activeSmart = $derived(smartData || disk?.smart);
</script>

{#if open && disk}
  <div
    class="fixed inset-0 z-50 bg-background/80 backdrop-blur-sm flex items-center justify-center p-4"
  >
    <div
      class="bg-card border border-border rounded-xl shadow-2xl max-w-xl w-full p-6 space-y-5 animate-in fade-in zoom-in-95"
    >
      <div class="flex items-start justify-between border-b border-border pb-3">
        <div>
          <h2 class="text-base font-bold text-foreground flex items-center gap-2">
            <span>Telemetría S.M.A.R.T.</span>
            <span class="font-mono text-sm px-2 py-0.5 rounded bg-muted text-muted-foreground"
              >{disk.path}</span
            >
          </h2>
          <p class="text-xs text-muted-foreground mt-0.5">
            {disk.model || 'Dispositivo de almacenamiento'} &bull; {disk.size_human || ''}
          </p>
        </div>
        <div class="flex items-center gap-1.5">
          <Button
            variant="ghost"
            size="sm"
            class="!h-7 !px-2 text-xs text-muted-foreground"
            onclick={refresh}
            disabled={loadingSmart}
          >
            <svg
              class="w-3.5 h-3.5 {loadingSmart ? 'animate-spin' : ''}"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
              />
            </svg>
          </Button>
          <button
            onclick={onClose}
            class="text-muted-foreground hover:text-foreground text-sm font-semibold p-1"
          >
            ✕
          </button>
        </div>
      </div>

      {#if activeSmart}
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
          <Card class="p-3 border border-border bg-card/50 text-center">
            <span class="text-[10px] text-muted-foreground uppercase font-semibold"
              >Salud Global</span
            >
            <div
              class="mt-1 font-mono font-bold text-sm {activeSmart.healthy
                ? 'text-success'
                : 'text-destructive'}"
            >
              {activeSmart.status || 'DESCONOCIDO'}
            </div>
          </Card>
          <Card class="p-3 border border-border bg-card/50 text-center">
            <span class="text-[10px] text-muted-foreground uppercase font-semibold"
              >Temperatura</span
            >
            <div
              class="mt-1 font-mono font-bold text-sm {activeSmart.temperature_c >= 55
                ? 'text-destructive'
                : activeSmart.temperature_c >= 45
                  ? 'text-warning'
                  : 'text-foreground'}"
            >
              {activeSmart.temperature_c > 0 ? activeSmart.temperature_c + ' °C' : 'N/D'}
            </div>
          </Card>
          <Card class="p-3 border border-border bg-card/50 text-center">
            <span class="text-[10px] text-muted-foreground uppercase font-semibold"
              >Horas Encendido</span
            >
            <div class="mt-1 font-mono font-bold text-sm text-foreground">
              {activeSmart.power_on_hours > 0 ? activeSmart.power_on_hours + ' h' : 'N/D'}
            </div>
          </Card>
          <Card class="p-3 border border-border bg-card/50 text-center">
            <span class="text-[10px] text-muted-foreground uppercase font-semibold"
              >Sectores Reasignados</span
            >
            <div
              class="mt-1 font-mono font-bold text-sm {activeSmart.reallocated_sectors > 0
                ? 'text-destructive'
                : 'text-success'}"
            >
              {activeSmart.reallocated_sectors ?? 0}
            </div>
          </Card>
        </div>

        {#if activeSmart.wear_percentage >= 0}
          <div class="p-3 rounded-lg border border-border bg-muted/20 space-y-1.5">
            <div class="flex justify-between text-xs font-semibold">
              <span>Desgaste de Memoria Flash (SSD/NVMe)</span>
              <span class="font-mono">{activeSmart.wear_percentage}% usado</span>
            </div>
            <div class="w-full h-2 rounded-full bg-muted overflow-hidden">
              <div
                class="h-full {activeSmart.wear_percentage > 85
                  ? 'bg-destructive'
                  : activeSmart.wear_percentage > 60
                    ? 'bg-warning'
                    : 'bg-accent'}"
                style="width: {Math.min(activeSmart.wear_percentage, 100)}%"
              ></div>
            </div>
          </div>
        {/if}
      {:else}
        <div
          class="p-6 text-center text-xs text-muted-foreground border border-dashed border-border rounded-lg"
        >
          No hay telemetría S.M.A.R.T. activa reportada para este disco o el controlador emula un
          dispositivo virtual.
        </div>
      {/if}

      <div
        class="border-t border-border pt-4 flex flex-col sm:flex-row items-center justify-between gap-3"
      >
        <div class="text-[11px] text-muted-foreground">
          Autodiagnóstico del hardware en segundo plano
        </div>
        <div class="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            class="!h-8 !text-xs"
            onclick={() => triggerTest('short')}
            disabled={testing}
          >
            Test Corto (~2 min)
          </Button>
          <Button
            variant="outline"
            size="sm"
            class="!h-8 !text-xs"
            onclick={() => triggerTest('extended')}
            disabled={testing}
          >
            Test Extendido
          </Button>
          <Button
            variant="destructive"
            size="sm"
            class="!h-8 !text-xs"
            onclick={() => triggerTest('abort')}
            disabled={testing}
          >
            Abortar Test
          </Button>
          <Button size="sm" class="!h-8 !text-xs" onclick={onClose}>Cerrar</Button>
        </div>
      </div>
    </div>
  </div>
{/if}
