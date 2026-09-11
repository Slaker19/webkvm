# ANÁLISIS DE CÓDIGO — WebKVM

> **Nota histórica:** auditoría puntual de 2026-09-05 sobre el SHA `51898b1`
> (`v1.1.10`). El código ha evolucionado hasta **v2.4.1** (ver `CHANGELOG.md`,
> `FIXES.md` y `CLAUDE.md`); los hallazgos aquí referencian un árbol antiguo y
> muchos ya están corregidos. Los entornos de pruebas citados (`192.168.1.20`,
> `webvm` 192.168.1.121) quedan obsoletos.

Auditoría profunda del repositorio completo, con inventario para memoria futura
y hallazgos con fix sugerido.

- **Fecha:** 2026-09-05
- **SHA auditado:** `51898b1` (`v1.1.10`, rama `main`, árbol limpio)
- **Alcance:** `backend/` (Go, módulo `webkvm`), `frontend/src` (Svelte 5),
  `scripts/`, `packaging/`, `Dockerfile`, `docker-compose.yml`,
  `.github/workflows/`, `Makefile`. Excluidos generados: `frontend/dist`,
  `backend/internal/frontend/dist`, `package-lock.json`, binarios
  `backend/webkvm*`.
- **Método:** lectura por áreas (API, libvirt/sistema, auth/datos/ops,
  frontend) + comprobaciones mecánicas (keys i18n, `{@html}`, secretos) +
  verificación por muestreo contra el código de los hallazgos graves.
- **Leyenda:** 🔴 Crítico (seguridad / pérdida de datos / RCE) · 🟠 Alto
  (bypass de cuota/ACL, bug funcional grave) · 🟡 Medio (validación, errores,
  UX, robustez) · 🔵 Bajo (nitpick, deuda).

## Índice

1. [Mapa del sistema](#1-mapa-del-sistema)
2. [Inventario-memoria](#2-inventario-memoria)
3. [Modelo de dominio e invariantes](#3-modelo-de-dominio-e-invariantes)
4. [Hallazgos críticos](#4-hallazgos-críticos)
5. [Hallazgos altos](#5-hallazgos-altos)
6. [Hallazgos medios](#6-hallazgos-medios)
7. [Hallazgos bajos](#7-hallazgos-bajos)
8. [Concurrencia y TOCTOU (transversal)](#8-concurrencia-y-toctou-transversal)
9. [Tests: cobertura y huecos](#9-tests-cobertura-y-huecos)
10. [Deuda técnica y recomendaciones priorizadas](#10-deuda-técnica-y-recomendaciones-priorizadas)
11. [Apéndice](#11-apéndice)

> **ACTUALIZACIÓN 2026-09-05:** los críticos C-01…C-06 y los altos A-01…A-04
> (parcial) están **corregidos y verificados funcionalmente** contra una VM
> con libvirt real; ver `FIXES.md` para el detalle y la matriz de pruebas.
> **Nuevo hallazgo C-07** (cobro de cuota al dueño de la plantilla) añadido
> durante la verificación.

---

## 1. Mapa del sistema

```
┌─────────────┐  embed   ┌──────────────────┐  CGO   ┌───────────┐
│ frontend/   │ ───────▶ │ backend/         │ ─────▶ │ libvirtd  │
│ Svelte 5 +  │  dist→   │ Go 1.26 + chi    │ libvirt│ QEMU/KVM  │
│ Vite+Tailw. │ embed.go │ JWT + RBAC       │ -go    │ nftables  │
└─────────────┘          └──────────────────┘        └───────────┘
        Un solo binario (~15 MB). HTTPS nativo con cert autofirmada.
        Estado en DATA_DIR (/opt/webkvm). Servicio systemd `webkvm`.
```

Flujo de una petición con mutación típica (p. ej. crear VM):

`chi router` → `auth.RequireAtLeast/RequireRole` → `requireVMOwnership`
→ handler (`api/*.go`) → `userStore.Get` + `checkQuota/checkDiskQuota` +
`assertPoolAllowed` → `libvirt.Connector` (XML) → `libvirtd` → auditoría
(`audit.Log`) + respuesta JSON (`jsonResp`/`jsonErr`).

Build/embed (ver `Makefile`): `frontend: npm run build` →
copiar `frontend/dist/*` a `backend/internal/frontend/dist/` →
`CGO_ENABLED=1 go build ./cmd/server` (embed vía
`internal/frontend/embed.go`). Sin CGO + headers libvirt no compila.

## 2. Inventario-memoria

### 2.1 Endpoints API (de `api/router.go`)

Convención: `PUB` sin JWT · `AUTH` cualquier JWT · `OP` operator+ ·
`OP+OWN` operator + dueño de la VM (`requireVMOwnership`, `authz.go:32`) ·
`ADM` admin.

Públicos: `GET /api/health`, `GET /api/covers/{path}`, `GET /api/system/cert`,
`POST /api/auth/login`, `/static/*`, `GET /console/{id}` (con ticket), `GET /*` (SPA).

AUTH (lecturas y cuenta propia): `/auth/logout|refresh|me`,
`GET /api/users/`, `PUT /users/me/password`, `GET /api/vms/` (+`/snapshots`,
`/{id}/`, `/{id}/firewall|disks|networks|vlan-support|meta|metrics|boot|autostart|snapshots`),
`POST /api/events/ticket` + `GET /api/events?ticket=`,
`GET /api/templates|appliances|storage/{pools,volumes,isos,jobs}|networks|host/*|groups|system/{status,logs}|settings/*|notify/*|tokens|nodes|backup/{targets,schedules,jobs}`.

OP sin ownership: `POST /api/vms/` (+`/import`, `/import-ova`),
`POST /templates/{id}/instantiate`, appliances `deploy`, storage
(`POST /pools`, `PUT /pools/{name}`, `POST /volumes`, `PATCH /volumes/*`,
`upload-iso`, `upload-iso/raw`, `upload-disk`, `download-iso`), networks
(create/update/start/stop).

OP+OWN (`/{id}`): update/delete, power (`start|shutdown|forceoff|reboot|
suspend|resume`), discos (`POST /disks`, `PUT|DELETE /disks/{dev}`,
`POST /disks/{dev}/{resize,bus}`), redes, meta/cover, `clone`, `boot`,
`autostart`, snapshots, firewall, make/unset-template, schedule,
`power/{action}`, `reset-password`, graphics/vnc/serial/console-ticket/
vnc-ticket/rdp/spice/export/clipboard.

ADM: CRUD usuarios, USB (`POST|DELETE /vms/{id}/usb`), host-terminal,
CRUD appliances, borrados storage/ISO/rename, `DELETE /networks/{id}`,
bridges/USB host, grupos, system (restart/apply-restart/update/backup/
backups), `GET /api/audit`, settings, notify, nodes, backup targets/
schedules/jobs + restore/verify.

### 2.2 Connector libvirt (`internal/libvirt`)

- **Dominios** (`domain.go`): `ListDomains:67`, `GetDomain:89`,
  `CreateDomain:207` (q35/pc, UEFI/SB/TPM, RNG virtio siempre),
  `Start/Shutdown/ForceOff/Reboot/Suspend/Resume:492-537`,
  `DeleteDomain:546`, `UpdateDomain:793` (regex sobre XML),
  `CloneDomain:2254` (solo primer disco), `Export/ImportDomain:2609/2756`,
  `GetDomainIP:1139` (solo lease).
- **Discos** (`domain.go`): `AttachDisk:1575`, `DetachDisk:1711`,
  `ChangeDiskBus:1759` (detach+reattach con rollback),
  `UpdateDiskSource:1981`, `ResizeDomainDisk:2428` (live `BlockResize` /
  `qemu-img`), helpers `nextVirtioDev:2488`, `nextSCSIDev:2509`,
  `nextDriveUnit:2535`.
- **Snapshots**: `List/Create/Delete/Revert:562/643/730/777`
  (disk-only intenta QUIESCE→DISK_ONLY→normal).
- **Redes** (`network.go`): CRUD + `direct`/macvtap, `assertMACUnique`,
  `CheckVLANSupport`.
- **Storage** (`storage.go`): pools/volúmenes/ISO, `ResizeStorageVolume:748`,
  `FindVolumeAttachments:486`, `DeleteVMDiskFiles:530`.
- **USB** (`domain.go:1858-1962`), **consola** (`console.go`,
  `guestagent.go`), **métricas** (`metrics.go`, `hostmetrics.go`,
  `events.go`).
- Invariantes: targets `virtio→vd*`, `sata/scsi/ide→sd*`; `.img` como cdrom
  se fuerza a `disk`+`sata`; naming `<vm>.qcow2`, adjunto `<vm>-<dev>.qcow2`,
  snapshot `<vm>.<snap>`; direcciones sata/scsi `unit=max+1`.

### 2.3 Env vars, estado, scripts, CI

- **Env** (`config/config.go`): `PORT=8080`, `BIND_ADDR=127.0.0.1`,
  `LIBVIRT_URI=qemu:///system`, `DATA_DIR=/opt/webkvm`,
  `JWT_SECRET` (genera `jwt.key` 0600 si vacío; rechaza débiles),
  `REPO_DIR/WEBKVM_REPO_DIR`, `VNC_PROXY_HOST`, `PUBLIC_HOST`,
  `CORS_ORIGIN=*`, `WEBKVM_VERSION/BUILD_TIME/LOG_FORMAT/LOG_LEVEL/LOG_FILE`,
  `WEBKVM_TRUSTED_RATELIMIT_CIDRS`, `WEBKVM_TRUST_PROXY`,
  `WEBKVM_TRUSTED_PROXY_CIDRS` (no documentada), `WEBKVM_ADMIN_PASSWORD`
  (solo si `users.json` vacío). `configstore/defaults.go`: `server.*`,
  `auth.token_ttl 24h`, `network.vlan_aware_default`, etc.
- **Estado en DATA_DIR** (casi todo `0600`): `users.json`, `jwt.key`,
  `audit.log[.1]`, `config.json`, `api-tokens.json`, `notify-secrets.json`,
  `sftp/cifs-secrets.json`, `admin-password.initial/.reset`,
  `vm-schedules.json`, `nodes.json`, `covers/`, `certs/`, `logs/`.
  Excepciones a revisar: `firewall.json` **0640**, `groups.json`/`appliances.json`
  **0644**, `logs/backend.log` **0644** (ver B-11).
- **Scripts**: `install.sh`/`docker-install.sh` (wrappers) →
  `packaging/standalone|docker/install.sh` (`--dry-run`, preflight,
  binario local > URL+SHA > build > release GitHub), `update.sh`
  (`--source` o release + health-check + rollback `.previous`),
  `uninstall.sh` (`PURGE_DATA/NETWORKS`; ya no purga paquetes),
  `scripts/setup-{network,bridge}.sh`, `generate-self-signed.sh`,
  `webkvm-backup.sh`. `docker-entrypoint.sh` bootstrap + self-signed.
- **CI** (`.github/workflows/`): `ci.yml` (lint + `go test -race` + frontend
  lint/prettier/build), `codeql.yml`, `docker-publish.yml` (tags `v*` →
  `slaker1908/webkvm`), `dependabot.yml` semanal.
- **Docker**: base `ubuntu:rolling` (flotante), binario precompilado,
  `network_mode: host` + `privileged`, monta sockets libvirt/systemd y
  `/etc/{passwd,shadow,group,pam.d}` RO.

### 2.4 Frontend (`frontend/src`)

- **Rutas** (`router.svelte.js`, `App.svelte` lazy): `/vms` (lista),
  `/vms/new`, `/vms/:id`, `/storage`, `/networks`, `/snapshots` (todos);
  `/backup`, `/users`, `/nodes`, `/host-console`, `/settings` (admin);
  `/account`, `/status` (todos). `mustChangePassword` fuerza `/account`.
- **Stores**: `auth` (token/user/role en `localStorage` + wrapper `request`
  con `Bearer` + ~100 funciones `api.*`), `events` (SSE con ticket 1 uso),
  `tasks`, `toast`, `fleetSnapshots`, `vmLayout`, `sidebarMode`.
- **Componentes**: `DataTable` (sin expand), `BlockCard`, `StatCard`,
  `ConfirmDialog` (con `loading`), `TerminalPanel`, `CommandPalette`,
  `BulkActionBar`, `Tabs`, `Icon`, `ui/*`.
- **i18n** (`i18n.svelte.js`): bloques `en/es/ca` sincronizados; `t()`
  escapa interpolaciones salvo `htmlVar()`; 3 keys faltantes en los 3
  idiomas (ver M-28).

## 3. Modelo de dominio e invariantes

(`models/types.go`, `api/quota.go`, `api/pools.go`, `user/store.go`)

- `Quota{MaxVMs, MaxVCPUs, MaxRAMMB, MaxDiskGB, PoolQuotas map[string]int}`.
  **`MaxDiskGB` es global** (todas las VMs del dueño, on/off);
  **`PoolQuotas` es cap de disco por pool**; `Enabled()` incluye pool-only.
- **vCPU/RAM solo contra VMs en ejecución** (`runningUsageOf`: Running,
  Paused, Crashed) y solo en `StartVM/ResumeVM` (`checkStartQuota`).
  En creación se usa `usageOf` (todas) — ver M-06.
- `User.AllowedPools []string`: **vacío = todos** (`poolAllowSet`);
  **admin siempre exento** de cuota y ACL. `UpdateUserRequest.AllowedPools
  *[]string` (nil = no cambiar, vacío = limpiar).
- `assertPoolAllowed` → **403**; se aplica en CreateVM/CreateDisk/Clone/
  Import/templates/appliances/backup/CreateVolume/ResizeVolume/UploadDisk.
- Dueño en `VMMeta.OwnerID`; `requireVMAccess`: no-admin debe ser dueño;
  sin dueño = solo admin. Lector viewer de meta/logs: ver A-08.

## 4. Hallazgos críticos

### C-01 · Inyección XML en `CreateStorageVolume`
**`backend/internal/libvirt/storage.go:442`.** `Name` y `Format` se interpolan
sin `xmlEscape` ni whitelist:
```go
xmlStr := fmt.Sprintf(`<volume><name>%s</name>...<format type='%s'/>`, req.Name, ..., format)
```
Impacto: `'><evil>` rompe `StorageVolCreateXML` (definición arbitraria/DoS).
Fix: `xmlEscape(req.Name)+xmlEscape(format)` + `nameRE` + whitelist
`qcow2|raw` (como `domain.go:1217`).

### C-02 · `volName` sin escapar en `createDiskInPool`
**`backend/internal/libvirt/domain.go:1229`.** `volName` deriva de
`req.Name`/nombre de VM (`2232`, `1646`) sin escapar.
Fix: `xmlEscape(volName)` + validar `req.Name` con `nameRE` en
Create/Attach/Clone antes de usarlo como fichero.

### C-03 · Inyección XML vía OVA
**`backend/internal/libvirt/ova.go:570,1079,1092`.**
`doc.VirtualSystem.Name` y `Href` (controlados por el atacante) llegan al
XML del dominio y a `<source file>`. Un OVA malicioso referencia rutas
arbitrarias (`/etc/passwd` como disco) o rompe el XML.
Fix: `xmlEscape(doc.VirtualSystem.Name)` y `xmlEscape(diskPath)` en
`ovfToLibvirtXML`; `xmlEscape` en `buildOVF:440`.

### C-04 · Ticket VNC reutilizable 1h con poder de apagado
**`backend/internal/auth/jwt.go:380-397` + `vnc_ticket.go:22,58-65`.**
El allowlist incluye `/vnc /clipboard /reboot /shutdown /forceoff` y
`CheckVNCTicket` no consume (verificado). Un `?vt=` filtrado (historial,
logs de proxy, XSS) pilota consola + clipboard (guest-agent) + power 1h.
Fix: sacar power del allowlist (exigir `Bearer`), TTL 5–10 min con refresh
vía WS, ligar `vt→{vmID,IP,UA}` + revocación + `PurgeExpired` periódico.

### C-05 · Update/install instalan binario sin checksum
**`packaging/standalone/update.sh:104-109` + `install.sh:407-424`.**
El fallback a `raw.githubusercontent…/backend/webkvm` avisa pero sigue
(`install -m 0755`) e instala como root tras `systemctl restart`.
Fix: fallar cerrado sin entrada exacta en `SHA256SUMS`;
`WEBKVM_BINARY_SHA256` obligatorio.

### C-06 · `SystemUpdate`: RCE como root + `eval` en instalador
**`backend/internal/api/system.go:453-498`**: `git pull + make build +
make install-systemd + systemctl restart` según `RepoDir` de `config.json`,
sin firma ni confirmación. **`packaging/standalone/install.sh:95-147`**:
`eval "${var}=\"${ans}\""` con input interactivo (un pegado con
`"; curl …|sh; #` ejecuta como root).
Fix: deshabilitar update por defecto (`WEBKVM_ALLOW_UPDATE=1` + token
one-time + allowlist de `RepoDir` + firma de tag); `printf -v "$var" '%s'`
+ allowlist en el instalador.

### C-07 · Instancear una plantilla cobra cuota al dueño de la PLANTILLA (descubierto en verificación real)
**`backend/internal/api/templates.go:74` (`InstantiateTemplate`).** El owner
para toda la cadena de cuota/ACL se tomaba como `meta.OwnerID` del VM
**origen** (la plantilla), y solo si estaba vacío se usaba el llamante.
Como toda plantilla normalmente pertenece al admin (exento), **cualquier
operador podía instancar plantillas de admin evadiendo TODA su cuota y
ACL** (verificado: instantiate de plantilla de 2 GB hacia un pool con cap
1 GB devolvía 201 con t01 limitado a 1 GB en ese pool). Combinado con el
fail-open de M-04 (`if serr == nil`) quedaba el bloque entero fuera.
Fix verificado: cobrar al **llamante** (`o = owner`, fallback a meta);
fail-closed si `GetDomain` falla (503), y lo mismo en `CloneVM`
(`vms.go:643`), `UpdateVM` crecimiento vCPU/RAM (`vms.go:204`), y
`ResizeDomainDisk` (`vms.go:769`). El clon hereda `OwnerID` del llamante
(bookkeeping ya lo hacía así).

## 5. Hallazgos altos

**Cuota/ACL/API**
- **A-01 · Template carga cuota al pool equivocado.**
  `api/templates.go:129`: calcula `tplPool` (117-120) y valida ACL contra él
  (121), pero `checkDiskQuota(o, map[h.defaultPool():diskGB])`. El cap del
  pool pedido jamás se evalúa. Fix: usar `tplPool` (verificado con código).
- **A-02 · `UploadDisk` sin re-chequeo post-escritura.**
  `api/storage.go:676-703`: solo chequea si `ContentLength>0`, con valor
  del cliente; con chunked (`-1`) no hay chequeo; nunca mide `written` ni
  revierte. El fichero luego se anexa como `existing_disk` sin más cuota
  (`vms.go:115-119`). Fix: tras `io.Copy`+`RefreshPool`, chequear
  `bytesToGB(fi.Size())` y `os.Remove` + 409 si excede.
- **A-03 · Cloud-init validado DESPUÉS de crear.**
  `api/vms.go:136` crea; `150-154` valida (el comentario 147-148 dice
  "BEFORE" pero el código es AFTER). Body inválido deja VM huérfana con
  dueño y 400 engañoso. Fix: mover la validación antes de `CreateDomain`
  (como `appliances.go:258-264`).
- **A-04 · Appliance: `network` sin validar + TOCTOU asíncrono.**
  `api/appliances.go:201-236,265,274,418-425`: sin `validateVMName(network)`
  (vms.go:1022 sí lo exige) y el job (`go deployApplianceJob`) corre
  minutos después de la cuota. Fix: validar + re-chequear al inicio del job
  (mutex por owner o jobs serializados).
- **A-05 · Import/restore estiman con tamaño comprimido + overrides sin cota.**
  `api/vms.go:1032-1058`, `api/backup.go:641-649`: `diskGB=bytesToGB(hdr.Size)`
  (comprimido; 800 MiB→30 GiB elude caps), `vcpus/ram` por `Atoi` sin rango
  (negativo resta cuota). Fix: `vcpus 1..64`, `ram 128..1M`; tras importar,
  medir `vmTotalDiskGB` real y rollback + 409 si excede.
- **A-06 · `CreateDisk`/`UpdateDiskSource` fail-open.**
  `api/vms.go:477-478,523-526`: si `userStore.Get(owner)` falla, salta
  ACL+cuota y crea igual. Fix fail-closed (500/401 + return).
- **A-07 · `POST /pools` para operator.**
  `api/router.go:298` + `storage.go:32-42`: crear pool (escritura host) es
  OP mientras borrar es ADM; denylist de solo 9 prefijos (permite
  `/tmp /mnt /opt/webkvm`). Combinado con upload = primitiva de escritura.
  Fix: exigir ADM (como DELETE) o `EvalSymlinks` + confinar a `PoolsDir()`.
- **A-08 · `/system/logs` y `/status` para viewer.**
  `api/router.go:358-360` fuera del grupo ADM; vuelcan log/journal con
  rutas, IPs, usuarios y errores libvirt. Fix: mover a ADM (y auditar qué
  expone `/status`: `LibvirtURI`, paths de pools).

**libvirt**
- **A-09 · Bus sin validar.** `domain.go:1661,1759`: `bus='foo'` llega a
  libvirt; `ide` en q35 deja el disco detached hasta rollback; ventana
  detach→attach sin disco ante crash. Fix: whitelist + matriz chipset×bus
  **antes** del detach.
- **A-10 · Targets duplicables.** `domain.go:2488,2509`: solo miran primera
  letra y saturan en `z` (27º disco colisiona `vdz`). Fix: sufijo base26
  completo + error si no hay hueco + comprobar `targetRe` antes de attach.
- **A-11 · Regex solo-comilla-simple parseando XML.**
  `domain.go:1499,1516,1730,1772,2000,2083`, `metrics.go:359,372`:
  `dev='…'` falla con `dev="…"` (bus vacío, detach del disco equivocado).
  Fix: `encoding/xml` para `<disk>` (o dual-quote `['"]`).
- **A-12 · Conexión usada fuera del lock.** `console.go:13,49`,
  `metrics.go:166`: se devuelve `dom/stream` y `Open()` puede `Close()`
  concurrente → use-after-close (pánico CGO). Fix: `withConn(func)` con
  RLock durante la operación / no exponer `*libvirt.Connect`.
- **A-13 · `qemu-img` con paths sin sanear.** `domain.go:2471,1473`,
  `ova.go:386,390,994`: `Source="-snapshot"` se parsea como flag;
  `..`/symlink fuera del pool. Fix: `Clean+IsAbs+EvalSymlinks` dentro del
  pool, rechazar base que empiece por `-`, `"--"` antes de paths.

**Auth/ops**
- **A-14 · Rate-limit bypaseable.** `auth/ratelimit.go:93-209`: loopback
  siempre trusted (SSRF/proceso local infinito) + lockout por
  `host|username` (rotar usuario evita bloqueo; sin bucket global por IP).
  Fix: bucket global por IP + trusted con límite elevado (no bypass) +
  `Retry-After`.
- **A-15 · `WEBKVM_ADMIN_PASSWORD` al unit 0644.**
  `packaging/standalone/install.sh:465-497`: `systemctl show` y el fichero
  exponen el secreto. Fix: `EnvironmentFile` 0600 / `systemd-creds`, solo
  primer arranque.
- **A-16 · Blacklist JWT solo en memoria.**
  `auth/blacklist.go:10-16` (+`jwt.go:204-216`): al reiniciar, los tokens
  revocados reviven dentro de su TTL 24h (logout/refresh pierden efecto).
  Fix: persistir `jti→exp` en `revoked.json` 0600 o TTL corto + rotación.
- **A-17 · `generatePasswordString` fail-open + sesgo.**
  `api/console_serial.go:347-362` (verificado): si falla urandom, `b` queda
  a ceros → `"aaaaaaaa"`; `%62` sesga. Fix: `crypto/rand.Read` + error, o
  `rand.Int` sin módulo / base64.
- **A-18 · Password policy solo-longitud.**
  `user/store.go:444-452`: `password`/`12345678` válidos. Fix: mín. 12,
  denylist 10k, 3/4 clases si corta.

**Frontend**
- **A-19 · JWT en `localStorage`.** `stores/auth.svelte.js:1-63`: cualquier
  XSS exfiltra `Bearer` persistente. Fix: cookie `HttpOnly;Secure;
  SameSite=Strict` (+`credentials:include`); a corto `sessionStorage`.
- **A-20 · `Snapshots` revienta con `null`.**
  `routes/Snapshots.svelte:48` (verificado): `s.vm_name.toLowerCase()` sin
  guard → página en blanco ante fila huérfana. Fix `(s.vm_name||'')`
  (como `Backup.svelte:267`).
- **A-21 · Estado desconocido = `crashed`.**
  `lib/utils/vmState.js:25-32` (verificado): `pmsuspended/blocked/idle` se
  pintan rojo. Fix: clase neutra `unknown` + `stateLabel()`.
- **A-22 · Filtros de pools inconsistentes.**
  Canónico `VmCreate:44-50`; `VmList:1048` y `Backup:697` ignoran
  allowlist; `VmDetail` ignora allowlist en 4 sitios; `Users.svelte:591`
  filtra por **nombre** (`/iso/i`) en vez de `purpose`. Efecto: 403 tardío
  o pools VDI ocultos / ISO renombrados visibles. Fix: helper central
  `canUsePool(pool,role,allowed)` y guardar `{name,purpose}` en Users.
- **A-23 · `toLowerCase` sin guard.**
  `VmList.svelte:302`, `Users.svelte:63`, `Settings.svelte:64`. Mismo
  `TypeError` que A-20. Fix `(x||'').toLowerCase()`.
- **A-24 · `?token=` latente en Sidebar.**
  `components/Sidebar.svelte:143` (verificado): `go(path, external)` abre
  con JWT en URL (hoy sin llamadores; contradice la política de
  `events.svelte.js:9-15`). Fix: eliminar rama o migrar a ticket 1-uso.

## 6. Hallazgos medios

- **M-01 · `bytesToGB` siempre +1** (`api/quota.go:282-287`): múltiplo
  exacto cuenta 1 GiB de más. Fix: `(n + 2^30 − 1) / 2^30`.
- **M-02 · `VNCProxy` responde tras Upgrade** (`api/console.go:120-133`):
  `jsonErr` con 101 ya enviado. Fix: close WS `1013` + return.
- **M-03 · `OwnerID` tragado** (`vms.go:143,160,663,1160`…): si
  `UpdateVMMeta` falla, el creador pierde su VM (`owner==""`→solo admin).
  Fix: comprobar error; si falla, `DeleteDomain` + 500.
- **M-04 · Clone/Instantiate/Update fail-open** si `GetDomain` falla
  (`vms.go:631,749`, `templates.go:109`): clonan igual. Fix: 404/500 + return.
- **M-05 · `UploadISO/ByCURL/DownloadISO` sin ACL** (`storage.go:560-748`)
  vs `UploadDisk`/`CreateVolume` que sí. Fix: `assertPoolAllowed`.
- **M-06 · Creación cuenta vCPU/RAM de apagadas** (`quota.go:62-81` vs
  invariante §3). Bloqueo excesivo (no bypass). Fix: en creación solo
  `MaxVMs`+disco; vCPU/RAM a `checkStartQuota`.
- **M-07 · Sin lock en start/deploy/restore** (ver §8).
- **M-08 · `SystemBackup` refleja 1 KiB del script** (`system.go:261-272`):
  filtra paths al llamante. Fix: mensaje genérico + `slog.Error`.
- **M-09 · nftables solo `table ip`** (`firewall/nft.go:88`): IPv6 bypasea
  input/dnat. Fix: `table inet` + espejo ip6, o deshabilitar IPv6.
- **M-10 · Masquerade depende de lease; sin chain `forward`**
  (`nft.go:91-154`): forward pendiente sin NAT de retorno hasta próximo
  Apply. Fix: re-Apply al obtener lease + chain forward explícita.
- **M-11 · TOCTOU check→uso** (`export_check.go:29→producer.go:235`,
  `storage.go:553→558`, `domain.go:2944→2961`): swap symlink entre stat y
  open/delete/sobrescritura. Fix: `O_NOFOLLOW|O_EXCL` + `fstat` + revalidar
  `EvalSymlinks` bajo lock.
- **M-12 · Tar-slip residual en import OVA** (`domain.go:2778`,
  `ova.go:761`; `runner.go:1735` admite symlink-chain): `Linkname` fuera del
  pool. Fix: `validateTarMemberName(name+linkname)`, rechazar links.
- **M-13 · `export_check.go:65` compara EOF imposible**
  (`errors.Is(err, errors.New("EOF"))` siempre false) + `Destroy`/errores
  tragados (`domain.go:555`, `network.go:377`: red queda indefinida).
  Fix: `errors.Is(err, io.EOF)`; propagar errores; rollback con XML previo.
- **M-14 · Cloud-init sin quote + password ≤12** (`cloudinit.go:75,111,158`):
  frágil si las regex se relajan; tope sin razón cripto. Fix:
  `yamlSingleQuote` siempre; elevar/eliminar tope.
- **M-15 · Loops de eventos duplicados** (`events.go:57` vs
  `connect.go:46`): compiten por `EventRegisterDefaultImpl`; conexión
  "lost" y polling silencioso. Fix: un único `ensureEventLoop` con
  `LockOSThread`.
- **M-16 · `BlockResize` sin mirar formato** (`domain.go:2455`, verificado):
  raw falla en libvirt en vez de rechazo previo. Fix: `qemu-img info`/vol
  antes; solo qcow2 en vivo.
- **M-17 · `io=native` sin check** (`pool_xml.go:131`, verificado): en
  NFS/tmpfs (`O_DIRECT`→`EINVAL`) falla definir/arrancar. Fix: `statfs` o
  fallback documentado.
- **M-18 · Doble-reopen concurrente** (`connect.go:382-393` + `Open:89`,
  verificado): la 2ª gorutina cierra la conn recién creada. Fix: doble-check
  en `Open` o `singleflight`.
- **M-19 · SFTP re-confía tras reinicio** (`backupstore/sftp.go:23-59`,
  verificado + Test sin pin). Fix: `known_hosts` persistente.
- **M-20 · JWT sin issuer/claims** (`auth/jwt.go:140-336`): sin
  `WithIssuer/NotBefore/Audience`; `username/role` vacíos pasan; `?token=`
  legacy en consola. Fix: validar claims + rechazar vacíos + solo tickets.
- **M-21 · `X-User/X-Role` spoofeables** (`jwt.go:225-291`,
  `audit.go:202-207`): los bypass públicos no limpian cabeceras que luego
  se loguean como actor. Fix: `Del` al inicio del middleware.
- **M-22 · Audit con una sola rotación** (`audit.go:22-87`): 10 MB de basura
  borran evidencia (máx 20 MB), sin fsync. Fix: `audit.log.1..7` + syslog.
- **M-23 · Doble fuente de CIDRs confiables** (`clientip.go:45-81` vs
  `ratelimit.go:140-160`; `WEBKVM_TRUSTED_PROXY_CIDRS` indocumentada):
  limiter y logger discrepan de IP. Fix: unificar en `server.trusted_cidrs`.
- **M-24 · Log 0644 + `LogFile` sin validar** (`logging.go:59`,
  `system.go:137-215`): usuarios locales leen IPs/paths; `LogFile=/etc/…`
  corrompe. Fix: 0600 + prefijo `DATA_DIR` + `O_NOFOLLOW`.
- **M-25 · Tickets sin sweeper** (`vnc_ticket.go:45-49`, `ticket.go:40-46`):
  purga solo en `Issue`; memoria crece. Fix: ticker 5 min.
- **M-26 · Shell sin `pipefail` + `curl|tar`** (`disable-netplan.sh:23`,
  `recover-network.sh:6`, `install-docker.sh:43`, `install-webkvm.sh:44`).
  Fix: `set -Eeuo pipefail`, descarga a tmp + `sha256sum --check`.
- **M-27 · Key USB colisiona** (`VmDetail.svelte:1786`, verificado):
  `vendor+product` sin separador + 2 sticks idénticos. Fix: key con
  bus/addr/índice; exponer bus/serial.
- **M-28 · 3 keys i18n faltantes en los 3 idiomas** (check mecánico):
  `common.saving` (`VmDetail:1934,1963`, `NotificationsTab:251`),
  `storage.isoSectionTitle` (`Storage:275`), `vms.cat_*` dinámico
  (`VmList:1880`). Los bloques están sincronizados entre sí. Fix: añadirlas
  + fallback `catLabel()`.
- **M-29 · Carreras `$effect`/polling** (`Account:156`, `App:117-130`,
  `Status:59-177`, `Storage:281-309`, `VmDetail:1134`): doble fetch, leaks,
  setState tras desmontar. Fix: patrón `cancelled` (`App:59-74`),
  `AbortController`, `onDestroy(clearInterval)`.

## 7. Hallazgos bajos

- **B-01 · N+1 + `continue` silencioso** (`vms.go:383-388`,
  `templates.go:57-61`, `quota.go:71-211`): flota de 100 VMs = 100+ RPCs;
  dominios rotos invisibles. Fix: `slog.Warn` + contador `skipped`.
- **B-02 · HTTP inconsistentes**: usuario inexistente→401 (debería 500/404),
  `GetDomain` fail→500 en make/unset-template/delete (debería 404),
  `volumeInUse` con forma distinta de `jsonErr`. Unificar.
- **B-03 · Revoke 403-vs-404 según rol** (`tokens.go:100-125`) filtra
  existencia. Unificar a 404.
- **B-04 · noVNC cache 1 año sin fingerprint** (`novnc_embed.go`, verificado):
  `?v=Version` o ETag.
- **B-05 · `_ = …` benignos** (`vms.go:936,951,1242`,
  `console_serial.go:68-184`, `backup.go:469-491`): al menos `slog.Debug`.
- **B-06 · `calculateCPUUsage` con elapsed fijo** (`domain.go:1241`): % sin
  sentido. Cachear `lastCpuTime+lastTime` por UUID.
- **B-07 · `CloneDomain` solo primer disco** (`domain.go:2301`): secundarios
  compartidos con el original (corrupción cruzada). Iterar todos los
  `<disk device=disk>`.
- **B-08 · Parsers single-quote devuelven 0** (`domain.go:1200,1380,1544`,
  `metrics.go:359`): DiskGB/IP/CPU a 0. Parser XML o dual-quote.
- **B-09 · `RenameISO`/rollback TOCTOU + `dirSize` sin límite**
  (`storage.go:872`, `cleanup.go:125`): lock por pool + timeout.
- **B-10 · Audit `q` omite `role/detail`** (`audit.go:106-178`): incluir +
  documentar exact-vs-substring.
- **B-11 · Permisos menores**: `firewall.json` 0640, `groups.json` y
  `appliances.json` 0644, `system-update.log` 0644, `.env` de `update.sh`
  hereda umask → todo a 0600.
- **B-12 · CI sin pin SHA + `ubuntu:rolling`** (`ci.yml`, `Dockerfile:11`):
  pin por hash, `GO 1.26.7` + `GOTOOLCHAIN=local`, `ubuntu:24.04`,
  `permissions: contents:read`.
- **B-13 · CORS `*`** (`config.go:111`, `router.go:53-57`): default
  same-origin; exigir origen explícito en prod.
- **B-14 · `admin-password.{initial,reset}` no rotan** (`user/store.go`):
  persisten tras lectura; cualquiera con escritura en `DATA_DIR` que vacíe
  `users.json` fuerza reseed. Borrar tras primer login + alerta.
- **B-15 · `{@html}` frágil** (`VmDetail.svelte:3144`): seguro hoy (HTML de
  desarrollador), migrar a snippet explícito. (Los otros 3 `{@html}` son
  constantes + `htmlVar`: seguros. No hay `{@html}` con datos de usuario;
  `t()` escapa por defecto.)
- **B-16 · a11y/doble-click**: `aria-label="Back"` hardcodeado
  (`VmDetail:1371`); botones sin `disabled` durante `actionLoading`
  (`VmDetail:2089,2850`, `Backup:959,1259`). Patrón `ConfirmDialog:49-56`.
- **B-17 · `Account` parpadeo/duplicados** (`Account.svelte:40-53`): mismo
  fix `cancelled` que M-29.

## 8. Concurrencia y TOCTOU (transversal)

Ventanas check→uso sin lock: `checkStartQuota→StartDomain` (dos `Start`
concurrentes sobrepasan cuota, M-07); `DeployAppliance`/`RestoreAsVM`
async sin reserva; `connect.go` doble-reopen (M-18); consola/stream fuera
del lock (A-12); `validateDisksReadable→ProduceVMArchive`,
`FindVolumeAttachments→Delete`, `VMDiskPath→O_TRUNC` (M-11);
`os.Rename` en `RenameISO` (B-09); `validateDiskSourcePath`→libvirt (TOCTOU
documentado en `pools.go`). Recomendación: mutex por owner en
check+mutación, `O_NOFOLLOW|O_EXCL`+`fstat`, `singleflight` en `Open`,
`withConn` con RLock.

## 9. Tests: cobertura y huecos

**Existe y pasa:** `quota_enforce_test.go` (cuota pura),
`firewall/zz_bug_test.go` (masquerade primer apply), `metadata_test.go`,
`audit/audit_test.go`, `auth/{jwt,ratelimit,vnc?}_test.go`,
`libvirt/{connect,network,pool_xml}_test.go`, `cloudinit_test.go`,
`logging/clientip_test.go`, `tokens/store_test.go`,
`backupstore/{runner,sftp?}_test.go`, `config/config_test.go`.
CI corre `go test -race` + lint + build frontend.

**Huecos:** sin tests para `UploadDisk` (cuota con Content-Length),
`ChangeDiskBus` (+rollback), `nextDriveUnit`/`nextVirtioDev` (colisión
27º disco), `validateDiskSourcePath`, `assertPoolAllowed` en handlers,
`UpdateDiskSource` rechaza-`.img`, `pool_xml` `io=native`, XML escaping
(C-01–C-03: tests de inyección pendientes), `generatePasswordString`
fail-open, `bytesToGB` borde exacto, `calculateCPUUsage`, parsers
dual-quote, `ova.go` (tar-slip/links), `sftp` TOFU, blacklist persistente.
**Frontend: cero tests** (solo build/prettier): candidatos — `vmState.js`,
`avgSeries/sumSeries`, `canUsePool` (cuando exista), guards null,
`progressLabel`, `passwordStrength`. Sin e2e en CI (solo script manual
`/tmp/qatest.py` ya desaparecido).

## 10. Deuda técnica y recomendaciones priorizadas

- **P0 (ya):** C-01–C-06 (XML escaping centralizado, VT, checksums,
  SystemUpdate/eval). Estimación: 1–2 sesiones.
- **P1 (esta semana):** A-01–A-08 (cuota/ACL), A-14–A-18 (auth),
  A-19–A-24 (frontend auth/null/pools).
- **P2:** M-01–M-29 por tandas (primero M-03/M-04/M-06/M-09/M-20/M-24).
- **P3:** B-01–B-17 + refactors: parser XML real para `<disk>`
  (mata A-11/B-08 de un golpe), helper `canUsePool` compartido,
  mapeo de errores central (`not found→404, cuota→409`),
  `diskDriverXMLAttrs` con capability check, rotación `audit.log.1..7`,
  pins SHA en CI.
- **Reglas para no regressar:** todo path de creación/import/clonado/resize
  pasa por `assertPoolAllowed`+`checkDiskQuota` (añadir test de matriz);
  ningún `Sprintf` a XML sin `xmlEscape` (grep en CI);
  i18n: CI que compare keys usadas vs definidas por idioma;
  `gofmt`+`go vet`+`go test -race` ya en CI — mantener.

## 11. Apéndice

### A. Sospechas refutadas (no son bug)

- **Kick de sesiones serial/eventos cross-tenant: FALSO.** `SerialProxy`
  (`console_serial.go:66-73`) hace newest-wins con close `4409`, pero la
  ruta exige `OP+OWN` + `requireVMAccess`: solo dueño/admin llegan. SSE es
  pub/sub sin desalojo; ticket 1-uso, VNC acotado a VM+allowlist.
- **Descuadre `avgSeries`: FALSO.** `VmList.svelte:393-431` alinea desde el
  más reciente y divide por series no vacías; solo trunca visualmente.
- **`auth.user` como objeto: FALSO.** Es `string` en todo el código;
  `allowed_pools` se obtiene vía `api.me()` (`VmCreate.svelte:278`).
- **Divergencia ACL/cuota en appliances: FALSA como bypass hoy**
  (ambas resuelven a `webkvm-disks`), CIERTA como fragilidad si el default
  se configura distinto — unificar constante.

### B. Ya corregido (constancia, no reabrir)

Tokens `Revoke/Delete` por rol del caller (`tokens/store.go:194-228`);
rate-limit con `trusted_cidrs` sobre IP resuelta (`ratelimit.go:162-200`);
masquerade en primer apply (`nft.go` + `zz_bug_test.go`); cuota en
power-on + per-pool (`quota.go` + `quota_enforce_test.go`); `.img` como
`disk`+`sata` (v1.1.8/v1.1.10); close `4409` anti-pelea de tabs (v1.1.4).

### C. Decisiones implícitas / gotchas

- `purpose` de pool es **informativo** (backend no lo impone; v1.1.6 solo
  oculta botones). `ListISOs` agrega todos los pools.
- Disco es global on/off; vCPU/RAM running-only; `AllowedPools` vacío =
  todos; admin exento de todo (incluso `requireVMOwnership`).
- `dist` embebido se regenera con `make build` (no commitear).
  `CLAUDE.md` está en `.gitignore` (acumula creds de sesión).
- `backend/webkvm` commiteado en el repo: el diff binario ensucia cada
  release; considerar releases de GitHub como única vía del binario.
- Deploys verifican contra libvirtd real (`alvin@192.168.1.20`), no mocks:
  mantener esa disciplina para cambios libvirt/red/discos.
