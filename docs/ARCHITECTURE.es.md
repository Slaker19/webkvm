# WebKVM — Arquitectura Interna y Diseño de Ingeniería

Documento técnico detallado sobre la arquitectura de software, patrones de concurrencia en Go y diseño del frontend en Svelte 5.

[**English**](ARCHITECTURE.md) • [**Español**](ARCHITECTURE.es.md)

---

## Visión General del Sistema

```text
┌────────────────────────────────────────────────────────────────────────┐
│                   Navegador Web / Clientes CLI                         │
│        SPA Svelte 5 • Terminal WebGL xterm.js • noVNC RFB • SSE        │
└───────────────────────────────────┬────────────────────────────────────┘
                                    │ HTTP/2 / TLS / WebSockets / SSE
┌───────────────────────────────────▼────────────────────────────────────┐
│                    Backend WebKVM (Binario Único Go)                   │
│                                                                        │
│   ┌─────────────────────┐ ┌───────────────────┐ ┌──────────────────┐   │
│   │   Enrutador Chi     │ │ Auth JWT / Tokens │ │ Rate Limiting    │   │
│   └──────────┬──────────┘ └─────────┬─────────┘ └────────┬─────────┘   │
│              │                      │                    │             │
│   ┌──────────▼──────────────────────▼────────────────────▼─────────┐   │
│   │                Controladores y Capa de Servicio                │   │
│   │   Instancias • Storage • Redes • Image Hub • Firewall • Ajustes│   │
│   └───────────────────────┬──────────────────────┬─────────────────┘   │
│                           │                      │                     │
│   ┌───────────────────────▼────────┐  ┌──────────▼─────────────────┐   │
│   │       Driver KVM / libvirt     │  │     Driver Incus / LXD     │   │
│   │     (CGO + Socket Unix RPC)    │  │    (Cliente REST Nativo Go)│   │
│   └────────────────────────────────┘  └────────────────────────────┘   │
│                                                                        │
│   ┌────────────────────────────────────────────────────────────────┐   │
│   │             Recursos Estáticos de Frontend Embebidos           │   │
│   │                  //go:embed all:dist (SPA)                     │   │
│   └────────────────────────────────────────────────────────────────┘   │
└────────────────────────────────────────────────────────────────────────┘
```

---

## Subsistemas Principales

### 1. El Paradigma del Binario Único (`go:embed`)
WebKVM prescinde de dependencias complejas en el despliegue:
- Durante la compilación (`make build`), Vite compila la aplicación Svelte 5 a `frontend/dist/`.
- El compilador de Go incrusta todos los ficheros, estilos compilados en Tailwind v4, fuentes y scripts en el binario final mediante `embed.FS`.
- En ejecución, `internal/frontend/embed.go` sirve la SPA directamente desde la memoria con cabeceras de caché optimizadas (`Cache-Control: public, max-age=31536000`).

### 2. Capa de Cómputo Híbrida (`internal/compute`)
WebKVM define una interfaz unificada en `internal/compute/backend.go`:
- **`KVMBackend` (`internal/compute/kvm.go`)**: Se comunica con KVM a través de `libvirt.org/go/libvirt` (CGO). Gestiona la virtualización por hardware, generación de XML de dominios QEMU, conexión de dispositivos PCI y controladores VirtIO.
- **`IncusBackend` (`internal/compute/incus/incus.go`)**: Se conecta directamente al socket de Incus/LXD (`/var/lib/incus/unix.socket`) mediante el SDK oficial en Go de Incus.
- **`CombinedBackend` (`internal/compute/combined.go`)**: Enruta dinámicamente las llamadas hacia KVM o Incus en función del identificador de la instancia, presentando una API homogénea.

### 3. Concurrencia y Resiliencia (`internal/safego`)
- Todas las goroutines en segundo plano (recolectores de basura de tareas, descargas asíncronas, proxies de WebSocket, bucles de eventos) están encapsuladas con `safego.Recover(nombre)`.
- Si se produce un pánico inesperado en un proceso aislado, se captura de forma segura, se registra en journald con traza completa y el servidor principal continúa funcionando sin interrupción.

### 4. Perfil de Rendimiento y Memoria
- **Cero Asignaciones en Reposo**: Cuando no hay conexiones interactivas ni tareas pesadas activas, el consumo de memoria de WebKVM se sitúa en **~12 MB a 18 MB de RAM**.
- **Eficiencia en I/O**: Los proxies de terminal y consola transmiten los flujos de datos mediante buffers fijos de 64 KB, minimizando la presión sobre el recolector de basura (GC) de Go durante sesiones intensivas.

### 5. Inspección de Contenido de Discos (`internal/diskprobe` + `hostcaps` GuestFS)
Adjuntar un disco existente a una VM antes era a ciegas: nada comprobaba si la imagen estaba vacía, contenía un SO ya instalado o eran datos de otra VM. `internal/diskprobe` responde una sola pregunta antes de adjuntar/formatear — *¿este disco ya contiene datos?* — en dos niveles:
- **Básico (siempre disponible)**: `qemu-img info` informa del formato real y los bytes asignados. Un qcow2 recién creado asigna ~0; cualquier cosa por encima de 1 MiB de margen (o cualquier backing file) activa `HasData`. Rápido, sin dependencias, solo lectura, timeout de 15 s.
- **Profundo (solo con libguestfs instalado)**: `virt-inspector --no-applications` arranca un appliance mínimo para leer particiones/filesystems e identificar el SO instalado (timeout de 120 s, servido como job en segundo plano vía `submitJob` porque es lento).
`hostcaps` detecta `virt-inspector` + `guestfish` (`LookPath`) y expone `GuestFS`/`GuestFSBin` vía `/api/host/capabilities`; su ausencia es normal y solo desactiva la vía profunda. `POST /api/vms/{id}/disks` rechaza con `409` una imagen no vacía salvo `force: true`, y rechaza igual el doble adjuntado (una imagen, una VM) — ambos exigidos en servidor, con el diálogo Add-Disk mostrando el aviso primero en cliente.

---

## Arquitectura del Frontend (Svelte 5 con Runes)

- **Reactividad con Runes**: La gestión del estado utiliza primitivas de Svelte 5 (`$state`, `$derived`, `$effect`) que garantizan actualizaciones quirúrgicas en el DOM sin la penalización de un virtual DOM.
- **División Dinámica de Módulos**: Cada ruta principal (`VmDetail`, `Storage`, `ImageHub`, `HostConsole`, `Settings`) se compila como un chunk independiente cargado bajo demanda vía `import()`.
- **Terminal Acelerada por WebGL**: El componente de terminal integra `@xterm/xterm` con `@xterm/addon-webgl` y `@xterm/addon-unicode11`, procesando la salida gráfica a 60 FPS directamente mediante la GPU.
- **Design system**: tokens semánticos de color/movimiento en `app.css` (3 temas, 6 acentos), primitivas basadas en shadcn más componentes propios (`StatusBadge`, `Tip`, variantes de `EmptyState`, `TableSkeleton`/`CardGridSkeleton`/`VmDetailSkeleton`, `Sheet` drawer). Los modales viven en `Dialog`/`Sheet`; no se usa `window.confirm`.
- **Split de componentes**: los diálogos/formularios autocontenidos se extraen de las rutas grandes con props explícitas y datos propiedad del padre (`DeleteVmDialog`, `ManageGroupsDialog`, `CloudInitPreviewDialog`, `AddScheduleDialog`, `CreateVolumeInlineForm`). Regla práctica: solo se extrae un bloque cuando su estado es genuinamente autocontenido — los formularios cuyas docenas de `bind:` se validan de forma cruzada con el resto de la página se quedan en la ruta.
- **i18n**: todo texto visible pasa por `t()` (`en`/`es`/`ca` en `i18n.svelte.js`); `t()` devuelve la clave cruda si falta la entrada, así que `t(k) || 'fallback'` nunca se ejecuta — no usar ese patrón.

### Compatibilidad Proxmox LXC (vzdump)
El paquete `internal/vzdump` gestiona la conversión bidireccional entre los formatos de backup Proxmox vzdump LXC e Incus:

- **Importar** (`POST /api/vms/import`): detecta `vzdump-lxc-*.tar.zst` / `.tar.gz` buscando `./etc/vzdump/pct.conf` o entradas de filesystem en raíz. Convierte al formato nativo de Incus bajo `backup/container/` (`backup.yaml` con config del contenedor + volume struct, `index.yaml` con name/backend/pool/type, `rootfs/`). Maneja layout Proxmox (archivos en raíz) e Incus (`rootfs/`), hard links (con prefijo correcto `backup/container/rootfs/` para `tar --strip-components=2`) y symlinks.
- **Exportar** (`GET /api/vms/{id}/export?format=proxmox`): lee backup de Incus (elimina prefijo `backup/container/`), expande `rootfs.squashfs` con `unsquashfs` si existe, genera `pct.conf` desde metadata de `backup.yaml` y produce `vzdump-lxc-*.tar.zst` con `./etc/vzdump/pct.conf` + `./rootfs/`.
- Requiere `squashfs-tools` (`unsquashfs`, `mksquashfs`) en el host (`.49`).
- **7 tests unitarios** en `vzdump_test.go` cubren detección gzip/zstd, import, export y generación de nombres.
