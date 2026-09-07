# Fase 5 — Opciones Avanzadas y Tuning de Instancias (Plan de Arquitectura)

> **Para Hermes:** usar subagent-driven-development para implementar el plan tarea a tarea.

**Objetivo:** Añadir control avanzado tipo Proxmox a WebKVM: opciones de seguridad/perfiles/nesting para contenedores Incus (LXC), boot order + CPU + firmware + vídeo para VMs KVM, y auto-arranque global, expuestos en backend (modelos/API/drivers) y frontend (`VmCreate`, `VmDetail`) bajo una sección "Opciones Avanzadas" colapsable.

**Arquitectura:** Ampliar los modelos de dominio y los drivers (`libvirt` y `incus`) de forma aditiva (campos `omitempty`/punteros), pasar los nuevos campos por `compute.Backend` → API → frontend, y renderizar en los drivers: en KVM el elemento `<boot dev='…'>` del XML de dominio; en Incus las claves `security.privileged`/`security.nesting`/`boot.autostart` + `Profiles` del `InstancePut`. Frontend: nueva sección colapsable "Opciones Avanzadas" en el wizard de creación y panel avanzado en el detalle.

**Stack:** Go 1.26 (backend), libvirt.org/go/libvirt (CGO), github.com/lxc/incus/v6 v6.23.0, Svelte 5 (runes) + Tailwind, i18n 3 idiomas (en/es/ca).

---

## 1. Contexto actual (análisis hecho)

### Ya implementado (NO rehacer)
- **KVM `internal/libvirt/domain.go` `CreateDomain`**: CPU mode (`host-passthrough` default; `custom` + `cpu_model`), topología CPU (sockets/cores/threads), `VideoModel` (default `virtio`; `none` ⇒ sin video = serial-only), `Chipset` (`q35` default / `i440fx`→`pc`), `Firmware` (`uefi`/`seabios`), `SecureBoot`, `TPM` — líneas 259-408. XML generado en 427-474.
- **KVM `GetBootDevice`/`SetBootDevice`** (`domain.go:2340-2373`) vía regex sobre `<boot dev='…'/>`; expuesto por `compute/kvm.go:80-83` y API `GET/POST /api/vms/{id}/boot` (`router.go:222,276`). El **create hardcodea `<boot dev='hd'/>`** (líneas 378 y 387).
- **Autostart KVM**: `SetDomainAutostart`/`GetDomainAutostart` + API `GET/POST /api/vms/{id}/autostart`; toggle ya en `VmDetail`.
- **Incus `internal/compute/incus/incus.go` `CreateDomain` (240-320)**: `config` con `boot.autostart: "true"` (hardcoded), `limits.cpu`, `limits.memory`; `devices` root/eth0/cloud-init. Lee `inst.Profiles` en varias funciones (373, 466, 497, 519, 554, 945) pero **nunca lo escribe ni lo lista**.
- **Modelos** `internal/models/types.go`: `CreateVMRequest` ya tiene CPUMode/CPUModel/VideoModel/Chipset/Firmware/SecureBoot/TPM/topología (58-105). `VM` ya tiene `Autostart`, `CPUMode`, `Firmware`, `VideoModel`, `Chipset`, `SecureBoot`, `TPMEnabled` (13-56).
- **Frontend**: `VmCreate.svelte` (1189 líneas) es un wizard con paso `step-advanced` (194) que ya edita chipset/firmware/TPM/CPU/video/disk_bus/network_model (37-75, 640-660). `VmDetail.svelte` ya edita CPU/video/chipset/firmware (650-686) y tiene toggle autostart (848-867).

### Huecos a implementar (el trabajo real de Fase 5)
| Opción | KVM | Incus |
|---|---|---|
| Boot order (disk/cdrom/network) | ⚠️ runtime existe, create hardcodea `hd` | n/a |
| `security.privileged` (unprivileged default) | n/a | ❌ no existe |
| `security.nesting` | n/a | ❌ no existe |
| Perfiles Incus (selector) | n/a | ❌ no se escribe ni lista |
| Autostart en creación (toggle) | ⚠️ vía endpoint aparte, no en create | ⚠️ hardcoded `true` en create |

---

## 2. Diseño propuesto

### 2.1 Modelos (`backend/internal/models/types.go`)

Añadir a `CreateVMRequest` (aditivo, `omitempty`/punteros):
```go
// BootOrder: dispositivo de arranque primario KVM ("disk"|"cdrom"|"network").
BootOrder string `json:"boot_order,omitempty"`
// Opciones avanzadas Incus (LXC).
Privileged *bool     `json:"privileged,omitempty"` // security.privileged (default false)
Nesting    *bool     `json:"nesting,omitempty"`    // security.nesting
Profiles   []string  `json:"profiles,omitempty"`   // perfiles Incus (default ["default"])
// Start at boot (KVM + Incus). nil ⇒ comportamiento actual (true).
Autostart  *bool     `json:"autostart,omitempty"`
```
Añadir a `UpdateVMRequest`: `BootOrder *string`, `Privileged *bool`, `Nesting *bool`, `Profiles []string`, `Autostart *bool`.

Añadir a `VM` (para `VmDetail`): `BootOrder string`, `Privileged bool`, `Nesting bool`, `Profiles []string`.

### 2.2 Interfaz compute (`backend/internal/compute/backend.go`)

Añadir método para listar perfiles Incus (el API lo necesita para el selector):
```go
ListIncusProfiles() ([]string, error)
```
- `compute/kvm.go`: devolver `[]string{}, nil` (sin perfiles).
- `compute/incus/incus.go`: `b.client.GetProfileNames()` (SDK incus v6; fallback `GetProfiles()`).

### 2.3 Driver KVM (`backend/internal/libvirt/domain.go`)

- **`CreateDomain`**: sustituir el `<boot dev='hd'/>` hardcodeado (líneas 378 y 387) por un helper `bootDeviceAttr(req.BootOrder)`:
  ```go
  func bootDeviceAttr(order string) string {
      switch order {
      case "cdrom":   return "<boot dev='cdrom'/>"
      case "network": return "<boot dev='network'/>"
      default:        return "<boot dev='hd'/>" // "disk" / vacío
      }
  }
  ```
- **`UpdateDomain`** (línea 793+): si `req.BootOrder != nil` → validar enum y llamar `c.SetBootDevice(id, mapped)` (reutiliza el regex existente). `GetBootDevice` ya devuelve el valor → mapear "hd"→"disk" en `domainToVM`/`GetDomain`.

### 2.4 Driver Incus (`backend/internal/compute/incus/incus.go`)

- **`CreateDomain`** (240-320): añadir al mapa `config`:
  ```go
  if req.Privileged != nil { config["security.privileged"] = strconv.FormatBool(*req.Privileged) }
  if req.Nesting    != nil { config["security.nesting"]    = strconv.FormatBool(*req.Nesting) }
  if req.Autostart  != nil { config["boot.autostart"]      = strconv.FormatBool(*req.Autostart) }
  // else: se mantiene boot.autostart="true" (compatibilidad)
  ```
  Y `post.Profiles = req.Profiles`; si vacío → `[]string{"default"}`. Validar perfiles (no vacíos, sin caracteres raros).
- **`UpdateDomain`** (326-387): aceptar `Privileged`, `Nesting`, `Autostart` (escribir claves en `Config` preservando el resto mediante el helper de update existente que conserva devices/profiles), y `Profiles` (sustituir la lista). Nota: cambiar `security.privileged` en Incus exige contenedor **parado**; si está corriendo devolver error claro (o stop/start automático).
- **`GetDomain`/mapeo a `models.VM`** (líneas ~373, 466...): poblar `VM.Privileged`/`Nesting`/`Profiles` desde `inst.Config`/`inst.Profiles`.
- **`ListIncusProfiles()`** nuevo método.

### 2.5 API (`backend/internal/api/vms.go`, `router.go`)

- `CreateVM` (80-149): validar `BootOrder` ∈ {disk,cdrom,network,""} y perfiles; el resto pasa solo (ya envía `req` completo a `h.compute.CreateDomain`).
- `updateVM` (236): validar `BootOrder`; pasar nuevos campos.
- Nuevo endpoint `GET /api/vms/incus-profiles` → `h.compute.ListIncusProfiles()` → `{"profiles": [...]}` (si incus no conectado → `[]`). Registrar en `router.go` dentro del grupo `/api/vms`.
- Autostart en create: no requiere cambio de ruta (campo pasa en `CreateVMRequest`).

### 2.6 Frontend

- **`frontend/src/lib/api/vms.js`**: añadir `fetchIncusProfiles()`; incluir `boot_order`, `privileged`, `nesting`, `profiles`, `autostart` en payloads de `createVM`/`updateVM`.
- **`VmCreate.svelte`**: nueva sección colapsable **"Opciones Avanzadas"** (desplegable/`<details>` o pestaña en el paso advanced):
  - **Contenedor (LXC)** — solo visible si `type === 'container'`:
    - Toggle *Unprivileged* (switch, default ON) → `privileged:false`; OFF → `privileged:true`.
    - Toggle *Nesting* (default OFF) → `nesting:true/false`.
    - Selector *Perfiles* (multi/select, fetch `fetchIncusProfiles()`, default `["default"]`).
  - **VM (KVM)**:
    - *Orden de arranque*: selector Disco / CD-ROM / Red → `boot_order`.
    - *Tipo de CPU*: reutilizar el existente (asegurar opciones host-passthrough/kvm64/qemu64 → `cpu_mode`+`cpu_model`).
    - *Firmware/Placa*: reutilizar (BIOS↔UEFI, q35↔i440fx).
    - *Vídeo*: reutilizar + opción **Serial-only** (mapea a `video_model:"none"`).
  - **Global (ambos)**: Toggle *Start at boot* (default ON) → `autostart`.
- **`VmDetail.svelte`**: ampliar el diálogo de edición avanzada (650-686) con Boot Order (KVM) y Privileged/Nesting/Perfiles (LXC); mantener el toggle autostart existente.
- **i18n `frontend/src/lib/i18n.svelte.js`**: añadir claves nuevas en **los 3 bloques** (en/es/ca) — p. ej. `advanced.*` (privileged, nesting, profiles, bootOrder, serialOnly, startAtBoot…).

---

## 3. Tareas (granularidad 2-5 min, TDD)

### Tarea 1 — Modelos: campos avanzados
- Modificar `backend/internal/models/types.go` (CreateVMRequest/UpdateVMRequest/VM).
- Test: `backend/internal/models/types_test.go` (si no existe, crearlo) — validar marshaling/omitempty y helper de validación de `BootOrder`.

### Tarea 2 — Driver KVM: boot order en CreateDomain
- Modificar `backend/internal/libvirt/domain.go` (helper `bootDeviceAttr`, líneas 378/387, UpdateDomain).
- Test: `backend/internal/libvirt/domain_test.go` — `CreateDomain` con `BootOrder:"cdrom"` emite `<boot dev='cdrom'/>`; default `hd`; `UpdateDomain` cambia el boot.

### Tarea 3 — Driver Incus: privileged/nesting/profiles/autostart
- Modificar `backend/internal/compute/incus/incus.go` (CreateDomain, UpdateDomain, GetDomain→VM, ListIncusProfiles).
- Test: `backend/internal/compute/incus/incus_test.go` — InstancePut con Config correcta (privileged false default, nesting true, profiles ["default"] + custom, boot.autostart), y mapeo VM.

### Tarea 4 — Interfaz compute + backend KVM stub
- Modificar `backend/internal/compute/backend.go` (ListIncusProfiles) y `backend/internal/compute/kvm.go` (stub []).
- Test: `backend/internal/compute/backend_test.go`.

### Tarea 5 — API: validación + endpoint perfiles
- Modificar `backend/internal/api/vms.go` (CreateVM/updateVM validación, handler `incusProfiles`), `backend/internal/api/router.go` (ruta `GET /api/vms/incus-profiles`).
- Test: `backend/internal/api/*_test.go` — enum inválido → 400; endpoint devuelve perfiles.

### Tarea 6 — Frontend API client + i18n
- Modificar `frontend/src/lib/api/vms.js`, `frontend/src/lib/i18n.svelte.js` (3 idiomas).
- Validar: `npm run check-i18n`.

### Tarea 7 — VmCreate: sección "Opciones Avanzadas" colapsable
- Modificar `frontend/src/routes/VmCreate.svelte`.
- Validar: `npm run build`, lint, vitest.

### Tarea 8 — VmDetail: edición avanzada
- Modificar `frontend/src/routes/VmDetail.svelte`.
- Validar: `npm run build`.

---

## 4. Archivos que cambiarán

**Backend:**
- `backend/internal/models/types.go`
- `backend/internal/libvirt/domain.go`
- `backend/internal/compute/backend.go`
- `backend/internal/compute/kvm.go`
- `backend/internal/compute/incus/incus.go`
- `backend/internal/api/vms.go`
- `backend/internal/api/router.go`
- Tests: `domain_test.go`, `incus_test.go`, `backend_test.go`, `models/types_test.go`, `api/*_test.go`

**Frontend:**
- `frontend/src/lib/api/vms.js`
- `frontend/src/lib/i18n.svelte.js`
- `frontend/src/routes/VmCreate.svelte`
- `frontend/src/routes/VmDetail.svelte`

---

## 5. Validación

- Backend: `cd backend && go build ./... && go vet ./... && go test ./...` (relevantes con `-race`: `internal/libvirt`, `internal/compute/incus`, `internal/api`).
- Frontend: `cd frontend && npm run build && npm run lint && npm run check-i18n && npm run test` (vitest).
- Manual (homelab):
  1. Crear VM KVM con `boot_order:"cdrom"` → `virsh dumpxml` muestra `<boot dev='cdrom'/>`; con network → `network`.
  2. Crear contenedor Incus con `privileged:false,nesting:true,profiles:["default"]` → `incus config show <name>`: `security.privileged=false`, `security.nesting=true`; con `privileged:true` → true.
  3. Toggle autostart en create → `boot.autostart` (Incus) / autostart flag (libvirtd).
  4. `VmDetail` muestra/edita los nuevos campos; la sección "Opciones Avanzadas" solo enseña LXC en contenedores y KVM en VMs.

---

## 6. Riesgos, tradeoffs y preguntas abiertas

- **Campos aditivos**: añadir campos a `CreateVMRequest`/`VM` es retrocompatible (JSON `omitempty`); clientes antiguos no se rompen.
- **`security.privileged` en contenedor en marcha**: Incus exige contenedor parado para cambiarlo. Decisión: en `UpdateDomain`, si el contenedor está corriendo, devolver error claro (`"stop the container before toggling privileged"`) en vez de stop/start automático (más seguro, UX honesta). Abierto a discutir.
- **Selector de perfiles sin Incus conectado**: el endpoint devolverá `[]`; la UI ocultará/deshabilitará la sección LXC si no hay perfiles.
- **Boot order UEFI**: el `<boot dev='…'/>` funciona igual en la rama UEFI/OVMF; sustituir ambos `hd` hardcodeados.
- **"Serial-only"**: mapea a `video_model:"none"` (el driver ya lo soporta); en `VmDetail` se muestra "serial-only". No confundir con la consola serial existente.
- **kvm64/qemu64** son modelos → se envían como `cpu_mode:"custom"` + `cpu_model`. La UI debe mantener esa traducción (ya existe parcialmente).
- **Default de autostart**: mantener `true` (comportamiento actual) para no cambiar la semántica de despliegues existentes.
- **Default `security.privileged`**: `false` (unprivileged) — el valor seguro y el que pide el usuario.
- **`ListIncusProfiles`**: verificar la firma exacta del SDK `github.com/lxc/incus/v6@v6.23.0` (`GetProfileNames()` vs `GetProfiles()`) al implementar (GOMODCACHE no estaba descargado durante el análisis).
- **i18n**: cualquier clave nueva debe añadirse a los 3 bloques o el build falla (convención del repo).