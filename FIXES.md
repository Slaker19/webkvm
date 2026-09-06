# FIXES — Correcciones aplicadas (2026-09-05)

## Sprint C (roadmap v1.2) — Frontend robusto: V12-FE-01..04 (2026-09-06)

- **V12-FE-01 · Fugas de pollers/timers** — cada `setInterval`/`setTimeout`
  se cancela en `onDestroy`:
  - `VmList`: `stopAppPoller()` en `onDestroy`; el poller de jobs de
    appliance aborta tras **10 errores 404 consecutivos** (el job ya no
    existe en el backend, p.ej. purgado por el sweeper de 24 h) y **los
    fallos de red genéricos resetean el contador** (sin falsos positivos);
    toast + estado `error` con la clave `vms.applianceJobGone`.
  - `Backup`: `forceStopJobsPoller()` en `onDestroy` (la versión normal
    mantiene el poll con job activo, pero el unmount lo fuerza).
  - `Storage`: `downloadInterval` subido a scope de componente + limpieza
    en `onDestroy` (antes era local a `startDownload` y filtraba tras
    navegar).
  - `TerminalPanel`: registro `later()` para TODOS los timers (reconnect
    con backoff, restart 250 ms, fit ×3) cancelados en `onDestroy`.
  - `Status`: timers de feedback (restart/backup) con `later()` + cleanup.
  - `VmDetail`: `later()` para deep-link serial, reset de notas y cierre
    de modal de export + `onDestroy`/cleanup.
  - `CommandPalette`, `KeyboardShortcuts`, `SearchInput`, `PasswordModal`,
    `CredentialsModal`: timers de focus/copy/debounce cancelados.
  - `TaskCenter` ya limpiaba (`return () => clearInterval(iv)`).
- **V12-FE-02 · Doble-click guards** — mutaciones envueltas con flag
  booleano síncrono (`disabled` + spinner), con re-entry guard al inicio:
  - `VmList`: `quickAction` (start/shutdown/forceoff/clone/template) con
    `quickBusy` por clave + botones `disabled` + `Spinner`; `deployAppliance`
    con `deployBusy` (el diálogo de confirmación sigue abierto durante el
    await — antes un doble-click lanzaba dos deploys).
  - `Storage`: `poolCreating`/`volCreating`/`volResizing` en createPool,
    createVolume y resizeVolume.
  - `Users`: `userAdding`/`userSaving` en addUser y saveEdit.
  - `Backup`: `runBackup` con guard síncrono por target (`runningBackups`).
  - `VmCreate`/`VmDetail`/`Networks`/`Settings` ya disponían de flags
    (`loading`, `actionLoading`, `saving`, `restarting`, …) — verificados.
- **V12-FE-03 · i18n completo + check en CI** —
  - Claves faltantes añadidas a en/es/ca: `common.saving`,
    `storage.isoSectionTitle`, y `vmCreate.cloudInit{Label,Helper,Enable,
    User,Hostname,SSHKey}` (existían bajo `networks.*`, no bajo `vmCreate`),
    más `vms.applianceJobGone` (poller FE-01). Escapado correcto de
    apóstrofes en ca (`Activa l'aprovisionament…`) y comillas dobles para
    el texto con apóstrofe.
  - `scripts/check-i18n.mjs` (comparación por brace-matching de los tres
    bloques) + wrapper `scripts/check-i18n.sh` que falla con exit 1 en
    claves asimétricas (verificado rompiendo simetría). Integrado en
    `.github/workflows/ci.yml` (job frontend).
  - Verificado: las 876 claves literales usadas por los componentes
    resuelven en el diccionario.
- **V12-FE-04 · Null-guards + Vitest (utils only, sin jsdom)** —
  - `Settings`: `sections` defiende ante schema sin `section` (`|| 'general'`),
    `(s.name || '').toLowerCase()`, `schema?.fields?.find`, label con fallback.
  - `VmList`: `appDeleteTarget?.name || ''` en el confirm de borrado.
  - Vitest configurado **exclusivamente** para `src/lib/utils/**/*.test.js`
    con `environment: 'node'` (sin jsdom). Tests de `formatRate`,
    `stateDotClass`/`stateBadgeClass` (fallback neutro para estados
    desconocidos) y `browser` (false en node). `npm test` verde; integrado
    en CI.
- **Gates VM** (`webvm` 192.168.1.121): binario `v1.2.0-rc1` desplegado,
  servicio `active`, `<title>WebKVM</title>`, login 200 + token,
  `/api/system/status` con libvirt conectado. Tests frontend (`npm test`)
  y `check-i18n` verdes también en la VM.

## Sprint B (roadmap v1.2) — OPS: V12-OPS-01..06 (2026-09-06)

- **V12-OPS-01 · Release reproducible** — `Makefile` target `release`
  (verifica tag, arma `dist/webkvm-<tag>.tar.gz` + `SHA256SUMS`, publica
  con `gh` si `RELEASE_PUBLISH=1`) y `.github/workflows/release.yml` que
  en cada tag `v*` (o dispatch) construye el binario versionado
  (`-ldflags Version=tag`), el CLI, empaqueta el tarball con `smoke.sh` y
  crea la GitHub release con checksums. Validado el ensamblado en la VM:
  tarball + SHA256SUMS correctos.
- **V12-OPS-02 · CHANGELOG** — `CHANGELOG.md` en formato Keep a Changelog
  con la sección Unreleased (v1.2.0) documentando Sprint A + B y el
  histórico 1.1.0…1.1.10. La versión es visible en `webkvm version`, en
  `/api/system` y en la UI (Status).
- **V12-OPS-03 · `scripts/smoke.sh` a prueba de balas** — arranca una VM
  desechable (pool scratch + volumen + dominio + estado running) y usa un
  único `trap cleanup EXIT` + `trap 'exit 130' INT` / `'exit 143' TERM`.
  Todos los recursos llevan prefijo único `webkvm-smoke-$$-<ts>` y se
  destruyen de forma idempotente y con timeout por paso. Validado con
  harness (éxito, fallo, SIGTERM, SIGINT → siempre teardown) y **en la VM
  real**: PASS + cero residuos (0 VMs/pools/dirs `webkvm-smoke`).
- **V12-OPS-04 · Runbooks** — `docs/runbooks/`: `RUNBOOKS.md` (índice +
  reglas de oro), `UPGRADE-ROLLBACK.md`, `DISASTER-RECOVERY.md`,
  `SECURITY-MODEL.md`, `OPS-DAILY.md`. Enlazados desde el README.
- **V12-OPS-05 · Rotación de auditoría durable** — `audit.go`:
  `maxBackups=7` (límite ~80 MB), rotación con desplazamiento
  `.6→.7…1→2` y descarte del más antiguo, **`file.Sync()` tras cada
  `Flush` y antes del `Close`** durante la rotación, `Logger.Close()`
  idempotente (flush+sync+close) invocado en el apagado del servicio.
  `List()` lee los 7 backups. Tests nuevos (`rotation_test.go`) + gates
  VM: servicio con el binario nuevo y `audit.log` íntegro (0600, 211
  líneas) tras reinicio.
- **V12-OPS-06 · Job sweeper thread-safe** — `storage.go`:
  `DownloadJob.UpdatedAt` (unix, renovado en cada create/update);
  `pruneExpiredJobs(now, ttl)` bajo `jobsMu` que **solo purga estados
  terminales (`completed`/`error`) con TTL > 24 h** (nunca `queued`/
  `running`); `StartJobSweeper(ctx, 5m, 24h)` goroutine independiente
  arrancada en main. Tests con `-race` (`job_sweeper_test.go`). Gate VM:
  `job_sweeper_started interval=5m ttl=24h` en journal.

## Sprint B (roadmap v1.2) — CAT: V12-CAT-00..08 (2026-09-06)

- **V12-CAT-00 · Migración del store de appliances a layout v3** —
  `appliances/catalog.go`: `layoutVersion=3`; al cargar, los built-ins
  reciben los defaults nuevos (URL, SizeBytes, recursos, compression,
  notes) **solo si `!Customized && !BuiltinOverride`**; el `ProvisionScript`
  modificado por admin se respeta estrictamente (vía `GetProvision`).
  `Update`/`SetProvision` marcan `Customized`. `save()` ahora escribe 0600.
  Tests (`migration_test.go`): refresh de defaults, preservación de
  Customized/BuiltinOverride/no-builtin, persistencia de version 3,
  marcado de Customized.
- **V12-CAT-01..06 · URLs/SizeBytes corregidos** — `appliances/defaults.go`:
  debian-13→trixie (339214336), opensuse-leap→Build2.16 (341735936),
  openmediavault→iso 8.3.1 (1244774400), opnsense→pkg.opnsense.org,
  vyos→2026.09.01-0034-rolling (673185792), notas pfsense actualizadas.
- **V12-CAT-07 · Provisionamiento oficial Docker CE** —
  `appliances/provision.go`: script `docker-ce` con keyring
  `/etc/apt/keyrings/docker.asc` firmado (`signed-by`) vía
  `curl -fsSL --proto =https` (descarga del GPG key, **sin `curl|sh`**),
  fuente APT pinned y `docker compose version` como sanity check.
- **V12-CAT-08 · Validación pre-vuelo de deploys** — `api/appliances.go`:
  - La red se valida contra `ListNetworks` **antes** de crear el job →
    400 inmediato (`networkExists`/`networkInList`); libvirt caído → 503.
  - El payload cloud-init se valida antes de `storeJob` (sin jobs zombie
    ni entrada de auditoría para deploys que no arrancan).
  - Fallo de `applyCloudInit` → job `error` (antes `completed` con
    warning), **sin** escritura de `AppInfo` (la UI nunca muestra
    credenciales de un app sin semilla), audit
    `appliance.deploy_cloudinit_failed`, VM conservada (disco válido).
  - Tests `api/cat08_test.go` (membership de red, red vacía permitida).
  - Se movió `updateJob(...,"completed","warning"...)` → `"error"`.

## Sprint A (roadmap v1.2) — batch 3: V12-SEC-04 (cierre del Sprint A)

- **V12-SEC-04 · `POST|PUT /pools` admin-only + gate UI** —
  `api/router.go`: las mutaciones de pool (CreatePool/UpdatePool) salen del
  grupo `RequireAtLeast("operator")` y pasan al grupo `RequireRole(admin)`,
  junto a los borrados. Los operadores conservan `GET /pools` y `GET
  /volumes` (monitorización intacta) y las subidas de volúmenes/ISO.
  - `frontend/routes/Storage.svelte`: botón "+ Create Pool" con
    `disabled={!auth.isAdmin()}`, `title` y span explicativos
    (`storage.poolAdminOnly` en en/es/ca), guard en `createPool()` y
    catch de 403 forzado → toast claro + reset del diálogo (sin colapso).
  - Test `api/pools_rbac_test.go` con los middlewares reales
    (`auth.RequireRole`/`RequireAtLeast`) replicando el mapeo del grupo
    como canario de regresión: operator → GET 200, POST/PUT pools 403,
    POST volumes 200; admin → POST/PUT pools 200.
  - Gates VM: operator GET /pools=200, GET /volumes=200, POST /pools=403
    ("insufficient role for this action"); admin POST /pools=201.

## Sprint A (roadmap v1.2) — batch 2: V12-SEC-02 + V12-SEC-03 (2026-09-06)

- **V12-SEC-02 · Blacklist JWT persistente** — `auth/blacklist.go` reescrito
  con persistencia en `DATA_DIR/revoked.json`:
  - Escritura atómica: `revoked.json.tmp` (0600) → `fsync` → `rename` →
    `fsync` del directorio (sello POSIX contra panic/kernel crash).
  - Fail-open con custodia: archivo corrupto → `revoked.json.corrupt-<ts>`
    (jamás destruido) y arranque con lista vacía (nunca aborta).
  - `NewTokenBlacklistWithPath` / `NewManagerWithPath` (retrocompatible:
    `NewTokenBlacklist`/`NewManager` intactos para tests). `main.go` usa
    `cfg.RevokedFile()`. Save en `Revoke`, en GC (solo si dirty/purged) y en
    `Close`.
  - Gates VM: logout → `revoked.json` 0600 v1 → **restart → token viejo 401**.
  - Tests `-race`: persistencia entre instancias, drop de expirados,
    corrupción→custodia+recuperación, atomicidad (sin `.tmp`, 0600),
    100 revocaciones concurrentes sin pérdida, lectores durante writers.

- **V12-SEC-03 · Password policy + rotación del secreto inicial** —
  `user/store.go`:
  - `validatePasswordStrength`: mín 12, máx 128, 3/4 clases si <16, denylist
    embebida (`go:embed common_passwords.txt`, ~1.1k entradas + clásicos)
    con rechazo exacto (case-insensitive) y de "decoraciones" (cola de
    dígitos/símbolos, p.ej. `freedom123`). Grandfather: solo hashes nuevos.
  - Borrado transaccional de `admin-password.initial` / `.reset`: SOLO
    después de `save()` exitoso en `ChangePassword`/`Update` para el admin,
    sin depender del flag `must_change` (el secreto se agota al fijar
    cualquier contraseña nueva). Falla la persistencia → ficheros intactos.
  - Gates VM: débil → 400 `decorated version of a common password`;
    cambio fuerte → 200 + fichero borrado + login con la nueva contraseña.
  - Tests `-race`: matriz de policy (longitud/clases/denylist/decoración),
    denylist poblada, borrado en éxito, intacto en fallo (policy y old
    wrong), reset admin, no-admin no toca ficheros, policy aplicada a
    non-admins.

  > Credenciales staging VM `webvm` (192.168.1.121) tras gates: admin
  > `FinalAdminPass#2026` (el fichero inicial se eliminó correctamente).

## Repositorio (local main)

### Sprint A (roadmap v1.2) — reparaciones de datos/seguridad (2006-09-05
- **V12-DATA-01 (AP-C1)** · `Connector.DomainExists()/VolumeExists()`
  + `Handler.verifyDeployTargetFree()` (mira filesystem + libvirt, fail-closed)
  + lock por nombre (`acquireDeployLock`, purga propia) + pre-flight
  en handler (409 inmediato) y en el JOB bajo el lock (cierre TOCTOU),
  con audit `vm.appliance_deploy_blocked`. Verificado en VM staging:
  G1 409 por dominio, G2 409 por volumen, G3 deploy concurrente mismo
  nombre → 1 VM + 1 job bloqueado con audit (sin pérdida de datos).
- **V12-DATA-02 (AP-C2)**. `removePoolImage` con guard de propiedad por
  inodo (`fileInode`): cleanup en fallos de `qemu-img resize`,
  `GetStorageVolume`, y `CreateDomain` con `RefreshPool` post-delete +
  audit `appliance.deploy_cleanup`. NUNCA borra un artefacto ajeno que
  ganó la carrera por el mismo path (solo limpia lo que colocó el job).
- **V12-SEC-01 (AP-C3)** `install -m 600 /dev/null /etc/webkvm-app.txt`
  en los 16 scripts de provisión + (bashrc-mark conservado, root-only).
  Aceptación verificada: el script servido empieza con control 0600
  (`static -c %a` = 600 en el guest tras deploy real). NOTA: el store
  desplegado con las plantillas viejas necesita que el `layoutVersion`
  se refresque hacia v3 (debe ser `roadmap v1.2 V12-CAT-00`; en staging
  reseteamos el store manualmente).

# FIXES — Correcciones aplicadas (2026-09-05)

Correcciones derivadas de `ANALISIS-CODIGO.md` (auditoría en `51898b1`, v1.1.10).
Cada fix fue **verificado contra el código real** antes de aplicarse. La
documentación de referencia completa con evidencia está en
`ANALISIS-CODIGO.md` (secciones §4–§7).

## Verificación aplicada

- Backend: `go build ./...` OK · `go test -count=1 ./internal/api
  ./internal/libvirt ./internal/auth ./internal/cloudinit` OK.
  (Los 2 fails de `internal/backupstore` en esta máquina son preexistentes y
  provocados por ejecutar los tests como root: `TestValidateTargetPathDenyList`
  (*root* lee `/proc/1/root`) y `TestAllocateOutputPathRejectsBadDir` (root
  escribe en dir "read-only"). No son regresiones.)
- Frontend: `npm run build` OK (9.1 s, sin errores).
- Scripta: `bash -n packaging/standalone/install.sh` y `update.sh` OK.
- Frontend compilado y copiado a `backend/internal/frontend/dist`?: NO —
  ejecutar `make build` antes de recompilar el binario de producción.

## Críticos corregidos

### C-01 · `CreateStorageVolume` — formato sin whitelist
`backend/internal/libvirt/storage.go`. `req.Name` ya validaba regex
(`nameRE`), pero `req.Format` llegaba crudo al XML del volumen (inyección).
Fix: whitelist explícita `qcow2|raw` con rechazo (fail-closed, antes
silenceaba inválidos).

### C-02 · `createDiskInPool` — `volName` sin validar
`backend/internal/libvirt/domain.go:1209`. `volName` (deriva del nombre de
VM/req.Name) se interpolaba al XML y es nombre de fichero backing.
Fix: `nameRE` validación + whitelist de formato `qcow2|raw` (además de
eliminar el bloque redundante que silenciosamente volvía a `qcow2`).

### C-03 · Import OVA — XML de atacante al dominio
`backend/internal/libvirt/ova.go`. `doc.VirtualSystem.Name` al
`<name>` del dominio y `diskPath` al `<source file=…>` sin escapar.
Fix: `xmlEscape()` en ambos (y en `buildOVF` para name/osType del descriptor
de export).

### C-04 · Ticket VNC con poder de apagado
`backend/internal/auth/jwt.go` + `backend/internal/api/console.go`.
Confirmado: la consola embebida tenía botones Reboot/Shutdown/Force-off que
POSTeaban `?vt=` (ticket reutilizable 1 h), y el allowlist del middleware
aceptaba esas rutas.
Fix: eliminados `/reboot /shutdown /forceoff` del allowlist `vncTicketAllowedPath`
(solo quedan `/vnc` y `/clipboard`), eliminados los botones y el panel power
de la página de consola; ahora el power exige el Bearer JWT de la app
principal. Un `vt` filtrado ya no puede apagar la VM.

### C-05 · Update/install aceptaban binario sin checksum
- `packaging/standalone/update.sh`: el fallback a `raw.githubusercontent`
  (sin checksum posible) ahora exige `WEBKVM_ALLOW_UNVERIFIED=1`; sin
  SHA256SUMS en el release → `die` (fail-closed).
- `packaging/standalone/install.sh`: cuando el release no trae suma, antes
  solo logueaba WARNING e instalaba root igual; ahora `die`.

### C-06 · RCE root: `SystemUpdate` siempre activo + `eval` con input
- `backend/internal/api/system.go:SystemUpdate`: opt-in
  `WEBKVM_ALLOW_UPDATE=1` (env, apagado por defecto); sigue admin+root.
  Log `/var/log/webkvm/update.log` baja de 0644 a 0600.
- `packaging/standalone/install.sh` (líneas 95-147): los 6 `eval "${var}="${ans}""`
  con input interactivo reemplazados por `printf -v "${var}" '%s' "${ans}"`;
  un pegado malicioso ya no ejecuta shell como root. (Líneas 103, 106, 112,
  124, 144, 146.)

## Altos corregidos

- **A-01 · `api/templates.go`** — Instantiate/crear-desde-template cargaba
  el disco al pool equivocado: `checkDiskQuota` usaba `h.defaultPool()`
  aunque se computaba `tplPool` (y validaba ACL contra él). Fix: cuota
  evaluada sobre `tplPool` real.
- **A-02 · `api/storage.go` (UploadDisk)** — la cuota se cobraba con
  `ContentLength` del cliente (inexistente en chunked, contaminable).
  Fix: tras `io.Copy`, se re-cheque a la cuota con el tamaño real
  (`os.Stat`) y si excede se hace `os.Remove` del fichero + 409 → el
  espacio no queda ocupado por un disco fuera de cuota.
- **A-03 · `api/vms.go` (CreateVM cloud-init)** — la validación de
  cloud-init corría DESPUÉS de `CreateDomain` (contradecía su propio
  comentario): fallo de validación dejaba VM huérfana + 400 engañoso.
  Fix: validación movida antes de crear el dominio.
- **A-17 · `api/console_serial.go:generatePasswordString`** — si fallaba la
  lectura de urandom devolvía `"aaaaaaaa…"` (fail-open) y había sesgo de
  módulo. Fix: `crypto/rand` + muestreo por rechazo (unbiased), fail-closed
  (pánico si no hay CSPRNG).

## Frontend corregidos

- **A-20 · `routes/Snapshots.svelte`** — `s.vm_name.toLowerCase()` sin guard
  → página en blanco con snapshot huérfana (`null`). Fix `(x || '')` guards.
- **A-21 · `lib/utils/vmState.js`** — estado desconocido (libvirt
  `blocked/idle/pmsuspended`, etc.) caía al fallback `crashed` (rojo).
  Fix: fallback a `shutoff` (gris neutro) en `stateDotClass` y
  `stateBadgeClass`.
- **A-23 · `routes/VmList.svelte` + `routes/Users.svelte`** — mismo patrón
  null-guard en `v.name` / `u.username` de los filtros de búsqueda.
- **A-24 · `lib/components/Sidebar.svelte`** — rama muerta abriría enlaces
  externos con el JWT en la URL (`?token=`). Fix: eliminada la rama
  (nada definía `item.external=true`); navegación 100 % interna.

## Verificación funcional (VM `webvm` 192.168.1.121 — libvirt real)

Build `dev` (2026-09-05 20:47 UTC) desplegada en `/usr/local/bin/webkvm`,
servicio reiniciado, `curl /api/health` OK. Tests ejecutados vía API con
usuarios operator reales y quotas/pools cap limitados:

| Test | Fix | Resultado real |
|------|-----|----------------|
| POST volume `format:"qcow2'><svg…"` (t01 cap) | C-01 | **500** `unsupported volume format … (allowed: qcow2, raw)` ✔ |
| POST volume `name:"('><evil/>"` | C-02 | **500** `invalid volume name …` ✔ |
| POST volume sane (t02) | sanity | **201** ✔ |
| POST `/api/vms/{id}/shutdown?vt=<VT>` (t01 cap) | C-04 | **403** `ticket not valid for this endpoint` ✔ |
| POST `/api/vms/{id}/clipboard?vt=<VT>` | C-04 | llega al handler (500 guest-OS, esperado en VM sin agent) ✔ |
| GET `/console/{id}?vt=<VT>` | C-04 | **200** HTML consola ✔ |
| POST `/api/system/update` | C-06 | **403** `disabled` ✔ |
| Upload 2 GB preallocated **chunked** sin Content-Length (t03 cap pool 1) | A-02 | **409** `upload exceeded disk quota …; file deleted` + fichero fuera del pool ✔ |
| POST VM con cloud-init password 5 chars | A-03 | **400** `at least 6` + **ninguna VM huérfana** ✔ |
| Instantiate plantilla 2 GB → pool ISOS (cap **1**) | C-07/A-01 | **409** `t01 uses 2/1 GB disk on pool "ISOS"` ✔ |
| Instantiate plantilla 2 GB → pool webkvm-disks (cap 2) | control | **201** ✔ |
| `bash -n` install.sh / update.sh | C-05/C-06 | OK ✔ |

**Descubrimiento nuevo durante la verificación (C-07):** el cobro de cuota
en `InstantiateTemplate` usaba el owner de la **plantilla** — si la
plantilla era de admin (exento), cualquier operador instanciaba sin pasar
por su cuota ni sus pools (reproducido: 201 con t01 limitado, A-61 tras el
fix 409). Corregido cobrando al llamante + fail-closed en el miss de
`GetDomain` (también en CloneVM/UpdateVM/ResizeDomainDisk — M-04).

UI smoke con **chromium headless** instalado en la VM: `http://localhost:8080/` —
`<title>WebKVM</title>`, markup de login presente. Sistema limpio tras
tests (solo `admin`, VMs y volúmenes del dueño intactos).

## No corregido en esta pasada (para siguientes tandas)

Se mantienen abiertos en `ANALISIS-CODIGO.md`: C-04 extra (TTL del VT y
revocación — mitigado el power, falta acortar/rotar), A-04/A-05 (cuota TOCTOU
en appliance/import-restore, estimación con tamaño comprimido), A-06..A-16,
A-18..A-22, los M-01..M-29 y B-01..B-17. M-04 quedó resuelto en los 4 puntos
detectados (instantiate/clone/update/resize). La eliminación de discos al
borrar una VM sigue necesitando `?disks=true` (comportamiento por diseño,
documentado). Prioridad siguiente sugerida: A-04–A-08 (cuota/ACL en
vms.go/backup.go) — la resta de tamaños reales y re-cheques post-operación.
