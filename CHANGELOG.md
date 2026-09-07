# Changelog

Todos los cambios notables de este proyecto se documentan en este
fichero, siguiendo [Keep a Changelog](https://keepachangelog.com/es/1.1.0/)
y [Semantic Versioning](https://semver.org/lang/es/).

## [2.3.0] — Fase 5: opciones avanzadas y redes L2 unificadas (2026-09-07)

### Added

- **Fase 5 — Opciones avanzadas de instancias**: modelo, API, drivers y UI
  ampliados con control tipo Proxmox.
  - **KVM**: `boot_order` (disk/cdrom/network) en creación y edición
    (`<boot dev='…'/>`), `autostart` en creación, opción VGA "Serial-only".
  - **Incus (LXC)**: `security.privileged` (unprivileged por defecto),
    `security.nesting` y **perfiles** aplicados en creación/edición. Cambiar
    `security.privileged` en caliente aborta pidiendo apagar el contenedor.
  - **Endpoint** `GET /api/vms/incus-profiles` (selector de perfiles).
- **Redes L2 compartidas (filosofía Proxmox)**:
  - `ListNetworks` expone los **Linux bridges del host** (`vmbr0`, `br0`…)
    como recurso unificado; KVM ataca con `<interface type='bridge'>` y los
    contenedores Incus con `nictype=bridged parent=<bridge>` (misma LAN).
  - **Bridge como default absoluto**: `CreateNetwork` crea redes `bridge`
    por defecto (auto-detecta `vmbr0`/`br0`); la red por defecto del primer
    arranque es `forward='bridge'` al puente principal (ya no NAT aislado);
    el selector del modal de redes preselecciona "Bridge".
  - Fix: `virNetworkGetBridgeName` no reporta el bridge de redes
    `forward='bridge'` — se extrae del XML (`extractNetworkBridge`), lo que
    arregla la creación de contenedores sobre `webkvm-bridge`.
  - Los bridges del host están protegidos (no se borran/recargan vía API).
- **Capa 2 física OBLIGATORIA** (filosofía Proxmox): KVM e Incus requieren un
  Linux bridge físico (`vmbr0`/`br0` unido a la NIC física) y obtienen IPs del
  router por DHCP. Se elimina **todo fallback a NAT/virtual** (`virbr0`/
  `lxdbr0`): si no hay bridge físico, la creación de instancias/redes falla
  con el error `ErrNoPhysicalBridge` ("no physical bridge found on host…
  configure vmbr0 attached to your physical NIC").
- **Instalador (`setup-network.sh`)**: detecta la interfaz física de la ruta
  por defecto e intenta crear el bridge `vmbr0` (nmcli → netplan
  Ubuntu/Debian); si automatizarlo arriesga la conexión SSH, imprime las
  instrucciones exactas (Netplan/NetworkManager) y aborta — jamás NAT. El
  fallo de red del instalador es FATAL (no continúa sin bridge físico). Si la
  interfaz está en DHCP, advierte sobre la **reserva DHCP** (mover la IP al
  bridge promociona la concesión, pero el router sigue viéndola en el pool);
  la vía recomendada para el host es una **IP estática por debajo del rango
  DHCP** (p. ej. `192.168.1.30` con pool desde `.50`), mientras las instancias
  obtienen su IP del router por DHCP a través del bridge.
- **Instalador (`install.sh` + `setup-network.sh`)**: el modo por defecto pasa a
  ser **`bridge` (L2 compartido)** en lugar de NAT. La instalación reutiliza un
  bridge físico existente (`vmbr0`/`br0`) y ya **no crea la red NAT aislada**
  por defecto (NAT sigue disponible solo como opt-in explícito con
  `NETWORK_MODE=nat`). `update.sh` y el instalador Docker no tocan redes: la
  inicialización bridge la hace el backend en el primer arranque.

## [2.2.1] — Instalador: fallback de release y resiliencia de red (2026-09-07)

### Fixed

- **Fallback de binario de GitHub Releases**: el instalador ahora busca el
  tarball oficial `webkvm-*.tar.gz` (antes buscaba `*linux_amd64*`, que no
  existe como asset de release) y extrae `backend/webkvm` de él. Un
  `git clone` sin binario precompilado ya instala sin `WEBKVM_BINARY`. El
  checksum SHA-256 del tarball se verifica antes de extraer (fail-closed).
- **Reintentos en gestores de paquetes**: `pkg_update` y `pkg_install`
  reintentan automáticamente (3 intentos, 4 s de espera) cuando `apt`/`dnf`/
  `pacman` fallan por timeout de mirror. Configurable con
  `WEBKVM_PKG_RETRIES` / `WEBKVM_PKG_RETRY_DELAY`.
- `pkg_update` ya no aborta la instalación si no puede refrescar el índice de
  paquetes: avisa y continúa (la instalación de dependencias sigue con sus
  propios reintentos).
- El `--dry-run` ya no falla cuando no hay binario local: reporta el fallback
  del tarball de release como fuente.
- **Instalador Docker** (`packaging/docker/install.sh`): mismos reintentos de
  paquete que el instalador nativo, y corrección de los nombres de paquete de
  Compose — Debian 13 / Fedora 44 no tienen `docker-compose-v2` /
  `docker-compose-plugin`, ahora se instala el binario standalone
  `docker-compose` como fallback (la imagen `slaker1908/webkvm` se despliega
  igualmente con `docker-compose`).

## [2.2.0] — Migración estratégica a Incus (2026-09-07)

### Changed

- **Backend de contenedores de LXD → Incus** (fork comunitario oficial).
  El SDK Go de Incus (`github.com/lxc/incus/v6`) mantiene la API REST de LXD,
  por lo que el mismo binario gestiona **Incus y los LXD ya existentes** sin
  romper entornos.
- **Paquete interno** `internal/compute/lxd/` → `internal/compute/incus/`
  (`IncusBackend`), log `lxd_connected` → `incus_connected`, `Hypervisor`
  `"lxd"` → `"incus"`.
- **Variables de entorno renombradas**: `WEBKVM_LXD_ENABLED` →
  `WEBKVM_INCUS_ENABLED`, `LXD_SOCKET` → `INCUS_SOCKET`,
  `WEBKVM_INSTALL_LXD` → `WEBKVM_INSTALL_INCUS`.
- **Socket**: auto-detección Incus primero (`/var/lib/incus/unix.socket`,
  `/run/incus/*`) con fallback a las rutas LXD (snap/apt).
- **Instalador** `install_incus()`: paquete nativo **`incus`** en
  apt/pacman/dnf (fallback `lxd`), **cero snap**, aviso no-fatal si no hay
  paquete.
- **Frontend**: util `incusImages.js` (`INCUS_IMAGE_PRESETS`), badge de tarjeta
  `Incus`, etiquetas "Incus / LXC" en el selector/credenciales (i18n 3 idiomas).
- **Docs**: README, INSTALLATION, USAGE, DOCKER, `.env.example` → Incus
  (socket, grupo `incus-admin`, `apt/pacman/dnf install incus`).

## [2.1.2] — CI verde y limpieza (2026-09-07)

### Fixed

- **CI green**: `go test -race ./...` pasa al 100% — corregidos los 2 fallos
  preexistentes de `backupstore` que rompían el pipeline:
  - `ValidateTargetPath` ahora comprueba la **ruta cruda Y la resuelta** contra
    el deny-list — `/proc/1/root` (symlink que resuelve a `/`) ya no escapa del
    chequeo.
  - `TestAllocateOutputPathRejectsBadDir` apunta a `/proc` (procfs de solo
    lectura incluso para root); antes usaba `/proc/1/root`, que como root se
    convertía en `/` y **creaba archivos basura de 0 bytes en la raíz del
    filesystem** (se han limpiado ~130).
- **Frontend**: `prettier --check` pasa (formateados los ficheros nuevos de
  las Fases 4/4.1); `check-i18n.sh` OK (1352 claves en cada idioma);
  `package-lock.json` resincronizado con `package.json` (versión 2.1.2) para
  que Dependabot no detecte drift.
- Dependabot (`gomod`/`npm`/`github-actions`) ya queda configurado con el CI
  verde.

## [2.1.1] — Fix instalador LXD (2026-09-07)

### Fixed

- `install_lxd()`: snap se usa **solo en Ubuntu genuino** (`ID`/`ID_LIKE`).
  Debian, Mint, Zorin y el resto de la familia apt **no traen snap** → el
  instalador usa el paquete nativo `lxd` (o avisa y continúa KVM-only si no
  existe). Nunca se fuerza snap en ninguna distribución.

## [2.1.0] — Soporte Híbrido KVM/LXC (2026-09-07)

El salto de arquitectura a v2.x: WebKVM ya no gestiona solo máquinas
virtuales QEMU/KVM, sino también **contenedores LXC de forma nativa** a
través del daemon LXD, con una vista unificada en la que cada instancia
lleva su badge de identidad (`KVM` / `LXC`).

### Added

- **Backend LXD (Fases 1–3)**:
  - Conector `LXDBackend` contra el daemon LXD (socket unix, snap o apt)
    con seam `compute.Backend` y listado unificado (`Combined`) — la
    caída del daemon degrada a KVM-only sin regresión.
  - Ciclo de vida de contenedores (start/stop/forceoff/reboot/freeze)
    + **consola serial interactiva** (exec bash sobre websockets).
  - **Creación con imágenes oficiales (cero ISOs)**: `ubuntu:24.04`,
    `images:alpine/3.20`, … con cloud-init inyectado nativamente en
    `user.user-data`/`user.network-config`.
  - Tags/metadatos RBAC (`user.webkvm.tags`/`user.webkvm.desc`) y
    **backups en streaming** (`/1.0/instances/<name>/export`, io.Copy,
    sin doble buffer ni descarga temporal).
- **Frontend híbrido (Fase 4)**:
  - Formulario de despliegue dual (KVM / LXC), filtro `All · VMs ·
    Containers`, badge de tipo por tarjeta y chip de provisión.
  - Detalle por capabilities: se ocultan/adaptan los controles solo-KVM.
- **Paridad KVM/LXC (Fase 4.1)**:
  - Selector de **imágenes con etiquetas amigables** (+ Custom/Other).
  - **Disco raíz redimensionable** (`lxc config device set root size=X`),
    **interfaces de red** gestionables y **métricas CPU/RAM** propias
    (MetricsCollector LXD) con el mismo sink de history/alerts.
  - **Credenciales LXC** (usuario opcional → contraseña de root) y
    **redes unificadas**: los contenedores se atan a los mismos Linux
    bridges de libvirt que las VMs (adiós `lxdbr0` hardcodeado).
  - Nombres propios: "Ubuntu 24.04 LTS", "Red Interna (vmbr0)".
- **Instalador**: flag opcional `WEBKVM_INSTALL_LXD=1` (snap solo en la
  familia Ubuntu/Debian; paquete nativo en Arch/Fedora con aviso no-fatal)
  y documentación del opt-in `WEBKVM_LXD_ENABLED=1`.

### Fixed

- `debian:12` fallaba en `parseImageRef` → presets con refs válidas
  (`images:debian/12`).
- Los contenedores ya no instalan `qemu-guest-agent` (paquete QEMU).

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