# PLAN-LXD — Soporte de contenedores (LXC) vía LXD nativo (v1.4)

> Blueprint arquitectónico para añadir contenedores LXC a WebKVM.
> Decisión base del roadmap: **LXD nativo** (API REST + cliente Go oficial
> `github.com/canonical/lxd/client`), **NO** el driver libvirt-LXC
> (abandonado upstream).
>
> Estado: acordado (2026-09-06). Ubicación: **v1.4 completo**.

---

## 1. Decisiones de arquitectura

| # | Decisión | Opción elegida |
|---|---|---|
| D1 | Abstracción | Fase 0 con interfaz `ComputeBackend` **completa** (~70 métodos); `KVMBackend` = adapter fino sobre el `*libvirt.Connector` existente |
| D2 | Modelo de datos | **Unificado**: `models.VM.Type ("vm"\|"container")` + `Hypervisor ("kvm"\|"lxd")`; rutas/cuotas/audit/frontend compartidos |
| D3 | Storage | **Pools LXD nativos** (dir/zfs/btrfs/lvm); sección "Container storage" en la página Storage, separada de los pools libvirt |
| D4 | Backup contenedores | **`lxc export` mínimo** (tar → targets local/SFTP) en Fase 1; S3 queda en backlog v1.3 |
| D5 | UX/UI | **Vista Unificada híbrida** (sección 5): una sola lista/detalle con badge de tipo + indicador de provisión |

**Principios transversales:**
- KVM y LXD **coexisten** en la misma instancia de WebKVM (el backend arranca libvirt y LXD a la vez).
- Todo lo que no aplique a contenedores se rechaza con **501 + mensaje claro** (no errores raros) vía `Capabilities()`.
- El usuario root de WebKVM accede al daemon LXD por unix socket; el aislamiento multi-usuario y la credencial dedicada son Fase 3.

---

## 2. Fase 0 — Abstracción `ComputeBackend` (la costura; riesgo cero)

Objetivo: introducir la interfaz **sin ningún cambio de comportamiento** —
el único backend sigue siendo KVM.

1. **Interfaz** en `internal/compute/backend.go` a partir del método-set
   real del `Connector` (~70 métodos), agrupados:
   - Lifecycle de instancia (`List/Get/Create/Start/Stop/ForceOff/Reboot/
     Suspend/Resume/Delete/Update/Clone/Autostart/Exists/BootDevice`).
   - Discos/dispositivos (`Attach/Detach/ChangeBus/UpdateSource/USB/…`).
   - Snapshots (`List/Create/Delete/Revert/Export`).
   - Storage/pools/volúmenes/ISO (`ListPools/CreatePool/Volumes/Resize/
     Delete/ISO CRUD/…`).
   - Redes (`List/Create/Delete/Update/Start/Stop`).
   - Cloud-init/console/meta (`OpenSerialConsole/SetUserPassword/GetVM-
     Meta/UpdateVMMeta/…`).
   - Backup/export/OVA (`ExportDomain/ExportDomainOVA/ImportOVA/Import
     Domain/Estimate*`).
   - `Capabilities()` → struct de flags: `SupportsOVA, SupportsSnapshots,
     SupportsVNC, SupportsSerialConsole, SupportsUSB, SupportsNoCloudISO,
     SupportsQemuGuestAgent, SupportsNftPortForwards`.
2. **Neutralizar tipos**: elevar a `internal/compute` los tipos `libvirt.*`
   que asoman en firmas de handlers (`libvirt.ExportBackupOptions`,
   `OVAOptions`, `OVATarget*`). El `KVMBackend` convierte hacia/desde.
3. **Única excepción (infraestructura)**: `Get()`, `StartEventLoop`,
   `MetricsCollector`, `HostMetricsCollector`, `CleanupStaleImports` son
   runtime de libvirt, no operaciones de instancia. Se quedan en el
   concreto; `Handler` guarda `compute compute.Backend` (operaciones) **y**
   `lv *libvirt.Connector` (infra). *No se paga el coste de abstraer la
   infraestructura de métricas/eventos.*
4. **Migración mecánica** en los ~20 ficheros `api/*.go`: `h.lv.M` →
   `h.compute.M`; firmas con tipos libvirt → `compute.*`; `api.NewRouter`
   recibe `compute.Backend`.
5. **Gates Fase 0**: `go build`/`go vet`, `golangci-lint`, `go test -race`
   verdes; smoke VM + backup real idénticos; **cero diff de comportamiento**
   en la API. Tests canario existentes: `deploylock_test`, `pools_rbac_test`,
   `cat08_test`, backup.

---

## 3. Fase 1 — Backend LXD (contenedores reales)

- `internal/lxd/` con el cliente oficial; config `WEBKVM_LXD_ENABLED` +
  `LXD_SOCKET` (default `/var/sockets/lxd/unix.socket`) en `config`/
  `configstore`. Conexión con backoff similar a libvirt.
- **Adapter `compute.Backend`** sobre LXD:
  - Traduce instances↔`models.VM` (estado running/shutoff/error; vCPU/RAM/
    disco desde `/state` y config).
  - `Capabilities()`: OVA/VNC/USB/serial/cdrom/NoCloudISO/qemu-guest-agent
    = `false` → handlers devuelven **501 limpio**.
- **Cloud-init nativo** de LXD (`cloud-init.user-data` + `network-config`
  como device config, sin ISO NoCloud). Reutiliza `buildUserData`
  (adaptado: sin `qemu-guest-agent` para contenedores) y los scripts de
  provision de `appliances/provision.go` (apt/systemd corren dentro del
  contenedor).
- **Console**: websocket de exec de LXD (pty) → reutiliza `TerminalPanel`
  con nueva ruta `/api/containers/{id}/console`. Reset de contraseña vía
  `lxc exec … passwd` o `cloud-init.set_passwords`.
- **Métricas**: sondeo `/1.0/instances/{name}/state` → alimenta los
  collectors existentes (CPU/RAM).
- **Red**: la red gestionada de LXD (`lxdbr0`, NAT propia). El firewall
  nftables de WebKVM queda **solo para VMs**; port-forwards de contenedores
  vía *proxy devices* de LXD (no se toca nft en Fase 1).
- **Backup mínimo**: `lxc export <name>` → tar; integrado en los targets
  local/SFTP existentes como tipo "container-export".
- **Gates Fase 1**: crear/arrancar/parar/borrar contenedor real; deploy de
  un appliance tipo "app" (p.ej. portainer-ce) vía cloud-init; consola exec
  funcional; métricas visibles; export + restore de contenedor.

---

## 4. Fase 2 — Catálogo de containers

- Appliances con `kind: container`; imágenes desde
  `images.linuxcontainers.org` (ubuntu, debian, nginx, alpine…).
- Reuso del flujo de deploy: elegir imagen + provision script + cloud-init.
- Solo se catalogan appliances **compatibles** con contenedor (los que
  requieran kernel/full-OS o docker-in-lxd necesitan contenedores
  privileged/nested — validar uno a uno).
- **Gate**: 3–4 appliances corriendo en contenedores con deploy `completed`.

---

## 5. Frontend — UX/UI (Vista Unificada híbrida) — **ACORDADO**

### 5.1 Patrón global
Una **sola lista** (`VmList`) y una **sola ruta de detalle** (`VmDetail`),
coherente con el modelo unificado. Diferenciación mediante:
1. **Filtro de tipo global** junto a los existentes (grupos + estado):
   `All · VMs · Containers`, compuesto con AND, default `All`.
2. **Badge de identidad por tarjeta**.
3. **Indicador de provisión** terciario.

### 5.2 Jerarquía visual de la tarjeta (aprobada)
- **Top-left** — badge de **estado** (dot + `running/shutoff/…`): operación. *(existe, sin cambios)*.
- **Top-right** — badge de **tipo**: identidad.
  - KVM → icono `Cpu`/`MonitorCog` + etiqueta `KVM`, color neutro (accent).
  - LXC → icono `Container`/`Box` + etiqueta `LXC`, color distintivo (ámbar/violeta) para escaneo inmediato.
- **Footer** — chip discreto de **provisión** junto a las specs (icono +
  tooltip, nunca un tercer badge ruidoso):
  - `cloud-init` nativo → `CloudCog` (LXD user-data o NoCloud ISO).
  - `script` → `TerminalSquare` (provision.sh).
  - `seed-iso` / `none` → `Box`.

### 5.3 Utilidades puras y tests
- Nuevo `src/lib/utils/computeType.js`:
  - `computeTypeBadgeClass(type)` y `computeTypeIcon(type)` — mismo patrón
    que `vmState.js`, **testeado con Vitest** (node env, sin jsdom — política FE-04).
- `ProvisionMethod` expuesto por el backend en `models.VM` (desde `VMMeta`):
  valores `cloud-init | script | seed-iso | none`. El indicador de provisión
  es **ortogonal al hipervisor** (un LXC con cloud-init y una VM con NoCloud
  comparten chip `cloud-init`).

### 5.4 Detalle (`VmDetail`) — estrictamente regido por *capabilities*
- Las secciones que no aplican a contenedores (chipset/UEFI/TPM, buses de
  disco, cdrom, VNC, guest agent, export OVA) se **ocultan por capabilities**
  y se sustituyen por una nota "No aplica a contenedores".
- Fila "Provisioning" en la tarjeta de identidad.
- Acciones de bulk/quick-menu que no aplican a un contenedor se ocultan
  (no deshabilitadas con mensaje confuso).

### 5.5 Flujo de creación y deploy
- **`VmCreate`**: segmented control superior **"Virtual machine /
  Container"** que condiciona el formulario al subconjunto aplicable
  (container → imagen + profile + privileged/nesting + cloud-init;
  VM → formulario actual). Ahí se elige el tipo.
- **Appliances (Fase 2)**: el catálogo gana `kind` (`vm`|`container`); el
  deploy ofrece selector de target cuando el appliance soporta ambos y
  muestra el badge de tipo en su tarjeta.

---

## 6. Fase 3 — Hardening

- Credencial LXD dedicada (no root) o socket con ACLs; profiles de LXD por
  usuario para reforzar las cuotas de WebKVM.
- Pinning de imágenes por fingerprint; política de imágenes públicas.
- Eventos de contenedor al `audit.log` (mismo patrón que VMs).
- Restore de contenedor con verificación sha256.
- **Gate**: revisión de seguridad + tests de cuota multi-usuario sobre
  contenedores.

---

## 7. Riesgos a vigilar

- **Fase 0 "completa"** = ~70 métodos × ~20 ficheros: el riesgo de regresión
  se mitiga porque `KVMBackend` delega directo y los tests existentes actúan
  de canario; se recomienda hacerlo en PRs pequeños por área.
- **Appliances actuales** asumen kernel/full-OS; algunos (docker-in-lxd)
  requieren privileged/nesting → Fase 2 cataloga solo los compatibles.
- **Firewall**: el modelo NAT/masquerade de WebKVM (nft) no se aplica a
  contenedores; LXD gestiona su propia red → riesgo bajo en Fase 1 (sin
  tocar nft), pero hay que documentar el solapamiento para el operador.
- **Backup**: el pipeline OVA/qcow2/domain-XML no aplica a contenedores;
  `lxc export` es un formato distinto → el manifest de backup debe marcar
  el tipo de instancia.

---

## 8. Gates de entrega v1.4 (resumen)

- Fase 0: CI verde + smoke/backup real idénticos (cero regresión).
- Fase 1: lifecycle real de contenedores + deploy appliance vía cloud-init
  nativo + consola exec + métricas + export/restore.
- Fase 2: 3–4 appliances en contenedores con deploy `completed`.
- Fase 3: hardening + tests de cuota/aislamiento + revisión de seguridad.