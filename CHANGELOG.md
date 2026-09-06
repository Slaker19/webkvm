# Changelog

Todos los cambios notables de este proyecto se documentan en este
fichero, siguiendo [Keep a Changelog](https://keepachangelog.com/es/1.1.0/)
y [Semantic Versioning](https://semver.org/lang/es/).

## [Unreleased] — v1.2.0 "Estabilización"

El objetivo de esta versión es estabilizar la plataforma sobre los
cimientos del sprint de auditoría: seguridad de datos, endurecimiento del
catálogo de aplicaciones, y operaciones de producción reproducibles.

### Added

- **Sprint de Auditoría — correcciones críticas aplicadas**:
  - C-01/C-02: inyección XML en nombre de VM y template — whitelist de
    formato + regex de nombre estricta (CVE-class hardening).
  - C-03: escape XML en descriptores OVA antes de incrustar metadatos.
  - C-04: botón de apagado retirado del panel noVNC (el ticket de
    energía solo se mostraba por una regla de firewalling de vCPU/RAM;
    ahora la consola no permite poder sobre el VM).
  - C-05/C-06: checksums de instalación en modo *fail-closed* + compuerta
    `WEBKVM_ALLOW_UPDATE=1` para actualizaciones.
  - C-07: la cuota de un template se carga ahora al **usuario** que lo
    instancia, no al propietario del template (exento de cuota).
  - A-17: generación de contraseñas con `crypto/rand` (CSPRNG) en
    todos los puntos que antes usaban `math/rand`.
  - A-20/21/23/24: guardas en el frontend (permisos de acciones VM,
    denial no destructivo en errores 403, estado de carga, etc.).
  - M-04: cierre de fail-open en instantiate/clone/update/resize (un
    `if err == nil` omitido habría saltado la liberación de cuota).

- **V12-DATA-01 — verificación de destino de deploy fail-closed**:
  - `DomainExists`/`VolumeExists` consultan libvirt directamente.
  - `verifyDeployTargetFree` comprueba espacio de fs Y libvirt.
  - `acquireDeployLock` con refcount y limpieza automática.
  - Pre-flight del job de deploy bajo el lock (409 en conflicto).

- **V12-DATA-02 — limpieza de orfandades de deploy**:
  - `removePoolImage` ejecutado ante cualquier fallo (resize,
    `GetStorageVolume`, `CreateDomain`) — no queda ningún disco huérfano.
  - Guardia de propiedad por inode (`fileInode`/`assertStillOurs`).
  - Audit `appliance.deploy_cleanup`.

- **V12-SEC-01 — secretos con permisos estrictos**:
  - Fichero de credenciales del appliance escrito con `install -m 600`.

- **V12-SEC-02 — blacklist de tokens persistente**:
  - `revoked.json` con escritura atómica (tmp → fsync → rename → dir
    fsync); ante corrupción, fail-open con backup `.corrupt-<ts>`.
  - El blacklist sobrevive reinicios del servicio.

- **V12-SEC-03 — política de contraseñas**:
  - Mínimo 12 caracteres, 3 de 4 clases, sin diccionario (lista de
    ~1.100 contraseñas comunes embebida) y sin caracteres decorados.
  - Eliminación transaccional de `admin-password.*` solo tras guardado
    correcto.

- **V12-SEC-04 — pools solo-admin**:
  - `POST/PUT /api/storage/pools` movidos al grupo `admin`.
  - Compuerta de UI en `Storage.svelte` + toast no destructivo en 403.

- **V12-CAT-00 — migración del store de appliances a layout v3**:
  - Los built-ins reciben automáticamente los nuevos defaults (URL,
    SizeBytes, recursos) al cargar el store, respetando estrictamente
    `ProvisionScript`, `Customized` y `BuiltinOverride` editados por el
    administrador.
  - Migración transparente de store v2 → v3 sin intervención manual.

- **V12-CAT-01..06 — URLs y tamaños del catálogo corregidos**:
  - debian-13 → imagen `trixie` (339214336 B).
  - opensuse-leap → `Build2.16` (341735936 B).
  - openmediavault → ISO `8.3.1` (1244774400 B).
  - opnsense → espejo oficial `pkg.opnsense.org`.
  - vyos → build rolling `2026.09.01-0034-rolling` (673185792 B).
  - pfsense → notas de instalación actualizadas.

- **V12-CAT-07 — provisionamiento oficial de Docker CE**:
  - Script de instalación con keyring GPG (`/etc/apt/keyrings/docker.asc`)
    firmado y fuente APT con `signed-by`. **Prohibido `curl | sh`**.

- **V12-CAT-08 — validación pre-vuelo de deploys**:
  - La red de destino se valida contra `ListNetworks` → 400 inmediato
    antes de crear el job (nada de jobs zombie).
  - El payload cloud-init se valida antes de encolar el job.
  - Si `applyCloudInit` falla, el job pasa a `error` (no `completed` con
    warning), no se escribe `AppInfo`, se audita y se limpia.

- **V12-OPS-01 — release reproducible**:
  - Target `make release` + workflow GitHub Actions `release.yml` que
    construye el tarball autónomo y publica la release con `SHA256SUMS`
    en cualquier tag `v*`.

- **V12-OPS-03 — smoke test a prueba de balas**:
  - `scripts/smoke.sh`: arranca una VM desechable, verifica el stack y
    **siempre** limpia (pool, volumen, VM) — con `trap` EXIT/INT/TERM,
    teardown único, idempotente y con timeout por paso.

- **V12-OPS-05 — rotación de auditoría durable**:
  - Hasta 7 backups rotados (`maxBackups=7`, límite ~80 MB totales).
  - `fsync` tras cada `Flush` y antes del `Close` durante la rotación —
    cero pérdida de registros por buffers.
  - `Logger.Close()` idempotente, invocado en el apagado del servicio.

- **V12-OPS-06 — job sweeper thread-safe**:
  - Goroutine independiente (ticker 5 min) que purga jobs con TTL > 24 h.
  - Solo purga estados terminales (`completed`/`error`); los jobs
    `queued`/`running` nunca se tocan.

### Changed

- Actualización de la política de rotación del log de auditoría
  (10 MB × 7 = 80 MB máx.), reflejada también en `docs/`.
- El tarball de distribución incluye ahora `scripts/smoke.sh`.

### Security

- Endurecimiento completo listado arriba: inyección XML, tickets noVNC,
  checksums fail-closed, CSPRNG, política de contraseñas, pools
  admin-only, blacklist persistente, cleanup de deploys y fail-open
  cerrados.

---

## [1.1.10] — 2025-XX-XX

### Fixed

- `.img` como medio de arranque forzado a bus SATA (fallaba en virtio).
- Cambiar el bus de un disco existente (con rollback y fix real del
  fallo virtio → sata).
- Diálogo de Force Off/Force Reboot con estado de carga.
- Docker base migrada a `ubuntu:rolling`.

## [1.1.9]

### Added

- Adjuntar un volumen de disco existente a un VM tras su creación.

## [1.1.8]

### Fixed

- `.img` como CDROM no arrancaba (adjuntado como cdrom fallaba).

## [1.1.7]

### Changed

- Botones de energía de noVNC muestran feedback de éxito/fallo.

## [1.1.6]

### Changed

- Oculta las acciones crear/subir disco en pools de propósito ISO.

## [1.1.5]

### Changed

- Los pools ISO también aceptan ficheros `.img`.

## [1.1.4]

### Fixed

- Conflicto de expulsión entre dos pestañas abiertas en la consola serial.

## [1.1.3]

### Security

- Correcciones de seguridad detectadas por Docker Scout.

## [1.1.2]

### Changed

- Migración de bindings libvirt `libvirt-go` → `libvirt.org/go/libvirt`.

## [1.1.1]

### Added

- Soporte Docker.

## [1.1.0]

### Added

- 8 nuevas opciones de libvirt/QEMU, overhaul de noVNC, subida de
  imágenes de disco, arreglos del instalador.