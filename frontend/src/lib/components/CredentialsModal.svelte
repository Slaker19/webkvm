<script>
  import * as Dialog from './ui/dialog';
  import { Button } from './ui/button';
  import { Copy, Check, AlertTriangle, Database } from '@lucide/svelte';

  let { open = $bindable(false), info = null, onClose = null } = $props();

  let copied = $state(false);

  const hasDB = $derived(Boolean(info && (info.engine || info.db_name || info.db_user)));

  function copyAll() {
    const lines = [];
    if (info?.app) lines.push(`App: ${info.app}`);
    if (info?.path) lines.push(`URL: http://<IP-de-la-VM>${info.path}`);
    if (hasDB) {
      lines.push(
        `Database: ${info.engine || ''} db=${info.db_name || '-'} user=${info.db_user || '-'} pass=${info.db_pass || '-'}`
      );
    }
    lines.push('Recuerda cambiar todas las contraseñas al terminar.');
    navigator.clipboard.writeText(lines.join('\n'));
    copied = true;
    setTimeout(() => (copied = false), 2000);
  }

  function handleClose() {
    if (onClose) onClose();
    else open = false;
  }
</script>

<Dialog.Root bind:open>
  <Dialog.Content class="sm:max-w-md">
    <Dialog.Header>
      <Dialog.Title>{info?.app || 'App'} — credenciales</Dialog.Title>
      <Dialog.Description>
        Termina la instalación en el navegador con estos datos.
      </Dialog.Description>
    </Dialog.Header>

    <div class="space-y-3">
      <div class="rounded-lg border border-border bg-muted/50 p-4 space-y-2">
        {#if info?.path}
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-muted-foreground">Ruta</span>
            <code class="text-sm font-mono">http://&lt;IP&gt;{info.path}</code>
          </div>
        {/if}
        {#if hasDB}
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-muted-foreground flex items-center gap-1">
              <Database class="h-3.5 w-3.5" /> Motor</span
            >
            <span class="text-sm font-mono">{info.engine}</span>
          </div>
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-muted-foreground">DB name</span>
            <span class="text-sm font-mono">{info.db_name || '-'}</span>
          </div>
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-muted-foreground">DB user</span>
            <span class="text-sm font-mono">{info.db_user || '-'}</span>
          </div>
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs text-muted-foreground">DB pass</span>
            <span class="text-sm font-mono break-all">{info.db_pass || '-'}</span>
          </div>
        {:else}
          <p class="text-sm text-muted-foreground">Esta app no usa base de datos gestionada.</p>
        {/if}
      </div>

      <div class="flex items-start gap-2 rounded-lg border border-amber-500/20 bg-amber-500/5 p-3">
        <AlertTriangle class="h-4 w-4 text-amber-500 mt-0.5 shrink-0" />
        <p class="text-xs text-amber-600 dark:text-amber-400">
          Por seguridad, cuando termines la instalación cambia TODAS las contraseñas: la del usuario
          admin que crees en la app y la de la base de datos.
        </p>
      </div>

      <p class="text-[11px] text-muted-foreground">
        Estas credenciales también están en la VM: entra por consola y verás el bloque WEBKVM APP
        INFO (bashrc/motd) o lee /var/log/webkvm-provision.log.
      </p>
    </div>

    <Dialog.Footer class="gap-2">
      <Button variant="outline" onclick={copyAll}>
        {#if copied}
          <Check class="h-3.5 w-3.5 mr-1.5" /> Copiado!
        {:else}
          <Copy class="h-3.5 w-3.5 mr-1.5" /> Copiar todo
        {/if}
      </Button>
      <Button onclick={handleClose}>Entendido</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>
