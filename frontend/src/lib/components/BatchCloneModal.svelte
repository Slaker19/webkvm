<script>
  import { api } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';

  let { vmId, open = false, onClose = () => {}, onSuccess = () => {} } = $props();

  let count = $state(2);
  let prefix = $state('clone');
  let linked = $state(true);
  let autostart = $state(false);
  let cloning = $state(false);

  let namesPreview = $derived(
    Array.from(
      { length: Math.min(Math.max(count, 1), 10) },
      (_, i) => `${prefix || 'clone'}-${i + 1}`
    )
  );

  async function handleBatchClone() {
    if (!vmId || count < 1) return;
    cloning = true;
    try {
      const res = await api.batchCloneVM(vmId, {
        count: parseInt(count, 10),
        base_name: prefix.trim(),
        prefix: prefix.trim(),
        linked: linked,
        autostart: autostart,
      });
      if (res && res.job) {
        await api.waitJob(res.job);
      }
      toast.success(`Lote de ${count} clones creado exitosamente`);
      onSuccess();
      onClose();
    } catch (e) {
      toast.error('Error al clonar por lotes: ' + e.message);
    } finally {
      cloning = false;
    }
  }
</script>

{#if open}
  <div
    class="fixed inset-0 z-50 bg-background/80 backdrop-blur-sm flex items-center justify-center p-4"
  >
    <div
      class="bg-card border border-border rounded-xl shadow-2xl max-w-md w-full p-6 space-y-4 animate-in fade-in zoom-in-95"
    >
      <div class="flex items-start justify-between border-b border-border pb-3">
        <div>
          <h2 class="text-base font-bold text-foreground">Clonado por Lotes (Batch Clone)</h2>
          <p class="text-xs text-muted-foreground mt-0.5">
            Crea múltiples instancias a partir de la máquina actual.
          </p>
        </div>
        <button
          onclick={onClose}
          class="text-muted-foreground hover:text-foreground text-sm font-semibold p-1"
        >
          ✕
        </button>
      </div>

      <div class="space-y-3.5">
        <div>
          <Label class="text-xs font-semibold">Cantidad de Clones (1 a 10)</Label>
          <Input type="number" min="1" max="10" bind:value={count} class="mt-1" />
        </div>

        <div>
          <Label class="text-xs font-semibold">Prefijo de Nombre</Label>
          <Input
            type="text"
            bind:value={prefix}
            placeholder="ej. web-worker"
            class="mt-1 font-mono text-xs"
          />
        </div>

        <div class="p-3 bg-muted/30 border border-border rounded-lg space-y-1">
          <span class="text-[10px] font-semibold uppercase text-muted-foreground"
            >Vista Previa de Nombres:</span
          >
          <div class="flex flex-wrap gap-1 max-h-24 overflow-y-auto pt-1">
            {#each namesPreview as name (name)}
              <span
                class="px-2 py-0.5 rounded bg-card border border-border font-mono text-[11px] text-foreground"
              >
                {name}
              </span>
            {/each}
          </div>
        </div>

        <div class="space-y-2 pt-1">
          <label class="flex items-start gap-2 cursor-pointer">
            <input type="checkbox" bind:checked={linked} class="mt-0.5 rounded border-border" />
            <div class="text-xs">
              <span class="font-medium text-foreground">Linked Clone (Copy-on-Write)</span>
              <p class="text-muted-foreground text-[11px]">
                Instantáneo y ahorra espacio; comparte el disco base como solo lectura.
              </p>
            </div>
          </label>

          <label class="flex items-start gap-2 cursor-pointer">
            <input type="checkbox" bind:checked={autostart} class="mt-0.5 rounded border-border" />
            <div class="text-xs">
              <span class="font-medium text-foreground">Arrancar clones tras crearlos</span>
              <p class="text-muted-foreground text-[11px]">
                Inicia de inmediato cada máquina virtual una vez aprovisionada.
              </p>
            </div>
          </label>
        </div>
      </div>

      <div class="border-t border-border pt-4 flex items-center justify-end gap-2">
        <Button variant="outline" size="sm" onclick={onClose} disabled={cloning}>Cancelar</Button>
        <Button size="sm" onclick={handleBatchClone} disabled={cloning}>
          {cloning ? 'Clonando...' : `Crear ${count} Clones`}
        </Button>
      </div>
    </div>
  </div>
{/if}
