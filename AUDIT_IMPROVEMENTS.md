# Diagnóstico Técnico Integral y Plan de Rediseño — WebKVM (v0.1.7)

**Fecha:** 5 de octubre de 2026  
**Proyecto:** WebKVM (Plataforma unificada de virtualización KVM e Incus)  
**Alcance:** +178.000 líneas de código (Backend en Go, Frontend en Svelte 5 / Vite).

---

## Índice
1. [Resumen Ejecutivo](#1-resumen-ejecutivo)
2. [Puntos Débiles y Antipatrones en el Backend (Go)](#2-puntos-débiles-y-antipatrones-en-el-backend-go)
3. [Puntos Débiles y Deuda Técnica en el Frontend (Svelte 5)](#3-puntos-débiles-y-deuda-técnica-en-el-frontend-svelte-5)
4. [Seguridad y Privilegios en el Host](#4-seguridad-y-privilegios-en-el-host)
5. [Rendimiento y Empaquetado](#5-rendimiento-y-empaquetado)
6. [Plan de Acción y Hoja de Ruta Priorizada](#6-plan-de-acción-y-hoja-de-ruta-priorizada)

---

## 1. Resumen Ejecutivo

WebKVM cuenta con una base sólida de características avanzadas (ZFS, MDRAID, NetGuard, VFIO Passthrough, WebAuthn, Incus), con suites de pruebas extensas y sin fallos actuales en build. Sin embargo, el rápido crecimiento funcional ha generado **puntos de concentración excesiva (God Objects)**, acoplamiento directo a binarios del host sin abstracción, patrones de polling intensivo dispersos y vistas de frontend monolíticas que complican el mantenimiento y degradan el rendimiento.

---

## 2. Puntos Débiles y Antipatrones en el Backend (Go)

### 2.1. El Monolito `internal/libvirt/domain.go` (5.130 líneas)
- **Problema:** Un único archivo gestiona el ciclo de vida del hipervisor, construcción de XML, GPU/VFIO, métricas en vivo, autostart, discos en caliente y sockets.
- **Impacto:** Alto riesgo de regresiones colaterales ante cualquier cambio menor; dificultad para tests unitarios aislados.
- **Rediseño necesario:** Desacoplar en archivos con un único dominio de responsabilidad:
  - `domain_lifecycle.go` (Start, Shutdown, ForceOff, Reboot, Suspend, Resume)
  - `domain_xml.go` (Parsers y constructores XML de libvirt)
  - `domain_pci_vfio.go` (Detección IOMMU, overrides de driver y passthrough)
  - `domain_devices.go` (Attach/Detach de discos, NICs, USB y carpetas 9p)
  - `domain_metrics.go` (Sondeo y cálculo de CPU, RAM, IOPS y red)

### 2.2. Polling disperso y bloqueos con `time.Sleep`
- **Problema:** Se identificaron más de 20 usos de `time.Sleep` en handlers de API y libvirt (`console_serial.go`, `domain.go`, `host_disks.go`, `system.go`).
  - Ejemplo: `console_serial.go` usa esperas fijas de hasta 800ms para reintentar la conexión del stream serie durante el arranque de una VM.
  - En `host_disks.go` y `domain.go`, se duermen rutinas esperando que el kernel asiente cambios (`udevadm settle`).
- **Riesgo:** Bloqueo innecesario de goroutines HTTP y retrasos artificiales en la respuesta al usuario.
- **Solución:** Reemplazar por bucles de polling reactivos con backoff exponencial basados en canales (`select` con `time.After` y `ctx.Done()`), o escuchar eventos netlink/inotify directos.

### 2.3. Ejecución directa de comandos de sistema (`exec.Command`) sin interfaz de Mock
- **Problema:** Llamadas directas a herramientas CLI (`smartctl`, `mdadm`, `zfs`, `zpool`, `apt`, `virsh`, `smbclient`, `xorriso`, `tar`, `unsquashfs`).
- **Riesgo:** 
  1. Si un disco del host tiene sectores dañados, comandos como `smartctl` o `zpool status` pueden bloquearse indefinidamente en estado D-state si el contexto no tiene un timeout estricto.
  2. Imposibilidad de probar flujos de error en entornos de CI donde estas utilidades no están instaladas como root.
- **Solución:** Introducir una abstracción `CommandRunner`:
  ```go
  type CommandRunner interface {
      Run(ctx context.Context, name string, args ...string) ([]byte, error)
  }
  ```
  Permite inyectar runners simulados en tests y forzar timeouts y políticas de cancelación centralizadas.

### 2.4. Residuos de `panic()` en producción
- **Problema:** En `internal/cloudinit/cloudinit.go` (`sha512_crypt` y `crypto/rand`) y `user/store.go` (`embedded denylist missing`), el backend hace `panic()` directo.
- **Solución:** Sustituir por propagación limpia de errores `error` para devolver `500 Internal Server Error` sin tirar el demonio de systemd.

---

## 3. Puntos Débiles y Deuda Técnica en el Frontend (Svelte 5)

### 3.1. Vistas Monolíticas gigantes: `VmDetail.svelte` (6.479 líneas) y `Storage.svelte` (4.993 líneas)
- **Problema:** `VmDetail.svelte` aloja toda la lógica de consola xterm/VNC, gráficas en tiempo real, configuración de discos, adaptadores de red, Cloud-Init, snapshots y tareas de backup en un único archivo.
- **Impacto:** Cualquier cambio de estado local (como el parpadeo de métricas por segundo) obliga al runtime de Svelte a evaluar el grafo de dependencias de un componente de más de 6.000 líneas.
- **Rediseño necesario:** Reestructurar en componentes especializados bajo `src/lib/components/vm-detail/`:
  - `VmOverview.svelte` (Resumen, estado y métricas)
  - `VmHardwareConfig.svelte` (Sub-pestañas: CPU/RAM, Discos, Red, PCI/USB)
  - `VmConsoleView.svelte` (Encapsulamiento aislado de xterm, noVNC y SPICE)
  - `VmSnapshotsView.svelte`
  - `VmBackupsView.svelte`

### 3.2. Proliferación de `setInterval` en lugar de Event Streams unificados
- **Problema:** Al menos 10 rutas (`Storage`, `Status`, `Backup`, `VmList`, `Nodes`, `Firewall`, `MultiView`, `AuditLog`, `TaskCenter`) crean sus propios `setInterval` individuales para consultar la API cada pocos segundos.
- **Impacto:** Con múltiples pestañas abiertas, el navegador satura de peticiones HTTP concurrentes al backend local.
- **Solución:**
  - Consolidar la telemetría en el canal SSE existente (`/api/events`), enviando deltas de estado en tiempo real.
  - Asegurar que todo `setInterval` se desmonte en `onDestroy` / ciclo de vida de Svelte 5.

### 3.3. Acceso sin envoltorio a `localStorage`
- **Problema:** Múltiples stores (`auth`, `theme`, `vmOrder`, `tasks`, `terminalSettings`, `sidebarMode`, etc.) leen y escriben directamente en `localStorage`.
- **Impacto:** Si `localStorage` no está presente (ej. Node v26 en tests, navegación privada estricta o iframes), la app falla en tiempo de ejecución.
- **Solución:** Crear un servicio unificado `src/lib/utils/storage.js` con fallback en memoria (como el que se creó para los tests).

---

## 4. Seguridad y Privilegios en el Host

### 4.1. Manejo de sistemas de archivos de solo lectura y contenedores
- **Problema:** En hosts con filesystem montado en solo lectura (`ro`) o entornos restringidos sin `CAP_SYS_MODULE`, las operaciones de inserción de módulos en caliente (`modprobe vfio-pci`) o escrituras en `/etc` fallan.
- **Mejora requerida:**
  - Crear un chequeo de pre-vuelo global al arrancar el backend (`host_caps`) que informe al frontend de las capacidades reales del entorno (si es contenedor, si el FS raíz es modificable, si `CAP_SYS_MODULE` está disponible).
  - Ocultar o deshabilitar proactivamente en la interfaz aquellas opciones que el host no puede soportar.

---

## 5. Rendimiento y Empaquetado

### 5.1. Chunks de gran tamaño en Vite/Rollup
- **Problema:** Vite advierte que `index.js` (>680 KB) y `vendor-xterm` (>474 KB) exceden los 500 KB recomendados.
- **Solución:**
  - Dividir `index.js` extrayendo las vistas de baja frecuencia (`AuditLog`, `SecurityJail`, `FirewallTemplates`) mediante imports dinámicos en el enrutador.
  - Extraer librerías complementarias (`qrcode`, `cronstrue`, `chart.js` o equivalentes) a chunks independientes bajo demanda.

---

## 6. Plan de Acción y Hoja de Ruta Priorizada

| Fase | Prioridad | Acción | Archivos Afectados |
|---|---|---|---|
| **Fase 1** | **Alta** | Descomponer `VmDetail.svelte` en 5 componentes modulares | `frontend/src/routes/VmDetail.svelte` -> `src/lib/components/vm-detail/*` |
| **Fase 2** | **Alta** | Modularizar `internal/libvirt/domain.go` en submódulos temáticos | `backend/internal/libvirt/domain.go` |
| **Fase 3** | **Media** | Crear abstracción `storage.js` para acceso seguro a `localStorage` | `frontend/src/lib/utils/storage.js`, stores |
| **Fase 4** | **Media** | Reemplazar `time.Sleep` por select/canales con timeout | `console_serial.go`, `host_disks.go`, `domain.go` |
| **Fase 5** | **Baja** | Optimizar Rollup manualChunks para eliminar chunks >500 KB | `frontend/vite.config.js` |
