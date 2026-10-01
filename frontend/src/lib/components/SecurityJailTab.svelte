<script>
  import { onMount } from 'svelte';
  import { api } from '$lib/stores/auth.svelte.js';
  import { toast } from '$lib/components/ui/toast';
  import { Button } from '$lib/components/ui/button';
  import { Card } from '$lib/components/ui/card';

  let banned = $state([]);
  let loading = $state(false);

  async function load() {
    loading = true;
    try {
      banned = (await api.listJailedIPs()) || [];
    } catch (e) {
      toast.error('Error cargando lista de IPs bloqueadas: ' + e.message);
    } finally {
      loading = false;
    }
  }

  async function unban(ip) {
    try {
      await api.unbanJailedIP(ip);
      toast.success(`IP ${ip} desbloqueada`);
      await load();
    } catch (e) {
      toast.error('Error al desbloquear IP: ' + e.message);
    }
  }

  onMount(() => {
    load();
    const interval = setInterval(load, 15000);
    return () => clearInterval(interval);
  });
</script>

<div class="space-y-6">
  <!-- Tarjetas de estado perimetral -->
  <div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
    <Card class="p-4 border border-border bg-card/60">
      <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        Estado Perimetral
      </div>
      <div class="mt-2 flex items-center gap-2">
        <span class="inline-block w-2.5 h-2.5 rounded-full bg-emerald-500 animate-pulse"></span>
        <span class="text-base font-semibold text-foreground">nftables Activo</span>
      </div>
      <div class="mt-1 text-xs text-muted-foreground">Protección delegada al kernel Linux</div>
    </Card>

    <Card class="p-4 border border-border bg-card/60">
      <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        IPs Bloqueadas
      </div>
      <div
        class="mt-2 text-2xl font-bold font-mono {banned.length > 0
          ? 'text-destructive'
          : 'text-foreground'}"
      >
        {banned.length}
      </div>
      <div class="mt-1 text-xs text-muted-foreground">En conjunto de descarte inmediato</div>
    </Card>

    <Card class="p-4 border border-border bg-card/60">
      <div class="text-xs font-medium text-muted-foreground uppercase tracking-wider">
        Redes Protegidas
      </div>
      <div class="mt-2 text-xs font-mono text-foreground font-semibold">
        127.0.0.0/8, 10.0.0.0/8, 192.168.0.0/16
      </div>
      <div class="mt-1 text-xs text-muted-foreground">Whitelist inmune a bloqueos automáticos</div>
    </Card>
  </div>

  <!-- Encabezado y Tabla -->
  <div class="space-y-3">
    <div class="flex items-center justify-between">
      <div>
        <h3 class="text-sm font-semibold text-foreground">
          Registro de Aislamiento Activo (Kernel Jail)
        </h3>
        <p class="text-xs text-muted-foreground">
          Cualquier IP con 5 intentos erróneos de inicio de sesión es descartada a nivel kernel por
          15 minutos.
        </p>
      </div>
      <Button
        variant="outline"
        size="sm"
        class="!h-8 !text-xs gap-1.5"
        onclick={load}
        disabled={loading}
      >
        <svg
          class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}"
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
        Actualizar
      </Button>
    </div>

    <Card class="overflow-hidden border border-border">
      <div class="overflow-x-auto">
        <table class="w-full text-left text-xs">
          <thead class="bg-muted/40 border-b border-border text-muted-foreground font-medium">
            <tr>
              <th class="p-3">Dirección IP</th>
              <th class="p-3">Motivo de Bloqueo</th>
              <th class="p-3">Intentos Registrados</th>
              <th class="p-3">Hora de Bloqueo</th>
              <th class="p-3">Expira</th>
              <th class="p-3 text-right">Acción</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-border">
            {#if banned.length === 0}
              <tr>
                <td colspan="6" class="p-6 text-center text-muted-foreground">
                  <div class="flex flex-col items-center justify-center gap-1.5">
                    <svg
                      class="w-6 h-6 text-emerald-500"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="2"
                      viewBox="0 0 24 24"
                    >
                      <path
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z"
                      />
                    </svg>
                    <span class="font-medium text-foreground"
                      >Sin amenazas perimetrales activas</span
                    >
                    <span class="text-[11px]">No hay direcciones IP bloqueadas actualmente.</span>
                  </div>
                </td>
              </tr>
            {:else}
              {#each banned as item (item.ip)}
                <tr class="hover:bg-muted/20 transition-colors">
                  <td class="p-3 font-mono font-semibold text-destructive">{item.ip}</td>
                  <td class="p-3 text-foreground">{item.reason || 'Fuerza bruta'}</td>
                  <td class="p-3 font-mono">{item.fail_count}</td>
                  <td class="p-3 text-muted-foreground font-mono">
                    {new Date(item.banned_at).toLocaleTimeString()}
                  </td>
                  <td class="p-3 text-muted-foreground font-mono">
                    {new Date(item.expires_at).toLocaleTimeString()}
                  </td>
                  <td class="p-3 text-right">
                    <Button
                      variant="outline"
                      size="sm"
                      class="!h-7 !text-[11px] text-destructive hover:bg-destructive/10"
                      onclick={() => unban(item.ip)}
                    >
                      Desbloquear
                    </Button>
                  </td>
                </tr>
              {/each}
            {/if}
          </tbody>
        </table>
      </div>
    </Card>
  </div>
</div>
