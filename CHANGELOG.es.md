# Changelog

Todos los cambios notables de este proyecto se documentan en este
fichero, siguiendo [Keep a Changelog](https://keepachangelog.com/es/1.1.0/)
y [Semantic Versioning](https://semver.org/lang/es/).

Versión en inglés: [CHANGELOG.md](CHANGELOG.md).

## [0.1.2-fix2] — 2026-09-30

### Corregido

- **Desduplicación y Cálculo de Capacidad en Pools de Almacenamiento:**
  - Añadida la resolución del `DeviceID` (`st_dev`) en los pools de almacenamiento de Incus en `IncusBackend.ListStoragePools()` y `CreateStoragePool()`.
  - Evitada la suma duplicada de capacidad y espacio usado en sistemas de archivos compartidos entre pools de KVM e Incus en el host.

## [0.1.2-fix1] — 2026-09-30

### Seguridad

- **Blindaje contra Path Injection:**
  - Reforzada la validación y sanitización de rutas en `zvol.Resolve` y `mdraid.ensureDeviceNode` mediante `filepath.Clean`, límites de prefijo explícitos y comprobaciones regex localizadas.
- **Actualización de Dependencias Vulnerables:**
  - Corregidas las alertas de Dependabot (CVE-2026-102277 / GHSA-q2hr-2g5m-vwhr) fijando las versiones parcheadas de `brace-expansion` (`1.1.21` y `5.0.12`) mediante *overrides* de npm en las herramientas de desarrollo del frontend.

## [0.1.2] — 2026-09-30

### Añadido

- **Pools de Almacenamiento ZFS y ZVols:**
  - Gestión integral de pools de almacenamiento ZFS con soporte para topologías stripe, mirror, raidz1 y raidz2.
  - Creación y ciclo de vida de volúmenes de bloques crudos (ZVols) con aprovisionamiento fino (*sparse*) para conexión nativa directa a VMs.
  - Endpoints REST dedicados: `GET /api/host/zpools`, `POST /api/host/zpools`, `GET /api/host/zvols`, `POST /api/host/zvols` y `DELETE /api/host/zvols/{name}`.
  - Pestaña de gestión ZFS dedicada en la vista de Almacenamiento con asistentes para creación de pools y volúmenes.
- **Software RAID en Linux (`mdadm`):**
  - Creación y gestión de arrays software RAID soportando niveles 0, 1, 5, 6 y 10.
  - Persistencia automática en `/etc/mdadm/mdadm.conf` y validaciones de seguridad de discos y puntos de montaje.
  - Endpoint REST dedicado: `POST /api/host/raid`.
  - Asistente interactivo de creación de arrays RAID en la vista de Almacenamiento.
- **Filtrado de Discos del Sistema Anfitrión:**
  - Exclusión automática de dispositivos loop correspondientes a squashfs, iso9660 y paquetes snap en el listado de discos físicos.
  - Reconocimiento y soporte para dispositivos de bloques `/dev/md*`.
- **Hardware de VM en vivo y ballooning:**
  - Soporte de límite inferior de ballooning de memoria (`min_ram_mb`) para recuperación dinámica con suelo garantizado.
  - Soporte de IOThreads dedicados (`iothreads`) para controladores de almacenamiento VirtIO-SCSI y concurrencia de bucle de eventos.
- **Configuración de Red Cloud-Init y Redes Directas:**
  - Configuración de redes directas (puente Linux) con soporte de IP/Subred (CIDR) estática, puerta de enlace predeterminada y servidores DNS (primario y secundario opcional).
  - Configuración de IPv4/IPv6 estática (CIDR), puerta de enlace y DNS en semillas NoCloud de Cloud-Init.
  - Selector visual interactivo en asistentes de creación y edición entre modos DHCP e IP Estática.
- **Extensiones del agente QEMU:**
  - Endpoint de API para TRIM de sistemas de archivos (`fstrim`) en `POST /api/vms/{id}/guest/fstrim` y botón en la pestaña de Convidat/SO.
  - Telemetría de sesiones/usuarios activos en el huésped (`guest-get-users`) y zona horaria (`guest-get-timezone`).

### Corregido

- **Compatibilidad con Netplan y NetworkManager en scripts de instalación:**
  - Priorización de Netplan como capa declarativa principal en sistemas modernos Ubuntu/Debian, evitando colisiones de unidades efímeras de red.
  - Corrección de la creación de puentes en NetworkManager usando UUIDs de conexión para evitar fallos por nombres de conexión con espacios (`Wired connection 1`).
  - Creación explícita del perfil de esclavo del puente (`nmcli con add type ethernet master ...`) en modo NetworkManager para garantizar la persistencia del enlace tras reinicios.
  - Filtrado de servidores DNS IPv6 en la detección estática de IPv4 para evitar perfiles de conexión inválidos rechazados por NetworkManager.
  - Desactivación de `systemd-networkd` cuando NetworkManager es el renderizador activo de Netplan, eliminando bloqueos en `*-wait-online.service` durante el arranque.
  - Bucle de sondeo asíncrono en la creación de puentes para esperar de forma fiable su registro en sysfs.

## [0.1.1] — 2026-09-29

### Añadido

- **Autoactualización desde la aplicación:**
  - Soporte de actualización automática mediante `POST /api/system/update` y `/#/status`.
  - Detección automática del modo de actualización: **release** (descarga e instala binarios oficiales verificados de GitHub) o **source** (actualiza el checkout git y recompila frontend y backend).
  - Verificación criptográfica con `SHA256SUMS` y política *fail-closed* antes de ejecutar instaladores con privilegios root.
  - Ejecución en unidad transitoria de systemd (`systemd-run`) para aislar el updater del cgroup principal durante las paradas y reinicios del servicio.
  - Bloqueo de concurrencia mediante `flock` sobre `/run/webkvm-update.lock`.
  - Despliegue atómico con copia previa y recuperación automática (*rollback*) si falla el chequeo de salud.
  - Detección dinámica del puerto del servicio desde `config.json` y el entorno de systemd.
  - Controles en la interfaz de usuario, modales de confirmación y traducciones completas en español, inglés y catalán.

## [0.1.0] — 2026-09-29

### Añadido

- **Cadenas de copias de seguridad incrementales:**
  - Copias incrementales en modo push de libvirt con seguimiento de disco mediante `CheckpointEntry`.
  - Soporte para checkpoints multidisco con mapeo `DiskFiles` para evitar problemas de rebase entre dispositivos.
  - Restauración en directorio de destino para evitar errores `EXDEV` entre sistemas de archivos montados.
  - Política de retención adaptada que protege las cadenas de copias incrementales durante el purgado.
  - Compatibilidad con pools de almacenamiento externos para listados `.qcow2` en SFTP, S3 y carpetas locales.

### Corregido

- Sanitización de rutas de pools de almacenamiento y destinos para resolver alertas de seguridad.
- Análisis de XML multidisco en dominios libvirt y exclusión de dispositivos CD-ROM durante los backups.

---

## [0.0.1] — 2026-09-26

Primera versión pública de WebKVM: un panel web autoalojado (un único
binario Go con el frontend Svelte embebido) para gestionar un host de
virtualización Linux.

### Añadido

- **Máquinas virtuales KVM/QEMU vía libvirt:** creación, clonado, edición de
  hardware, consola VNC/serie en el navegador, snapshots, discos y
  migración de almacenamiento.
- **Contenedores Incus/LXC** (módulo opcional) gestionados desde la misma
  interfaz que las VMs, incluido el catálogo de imágenes Incus. Los
  contenedores eligen su pool de almacenamiento al crearse y pueden
  moverse después entre pools de propósito `container`: el rootfs y sus
  snapshots se reubican con la migración nativa de Incus, con la
  instancia parada.
- **Almacenamiento:** pools locales y de red (NFS/SMB) con propósitos
  (`disk`, `container`, `iso`, `backup`, `template`), inspección de discos
  físicos y un almacén multimedia único para ISOs e imágenes.
- **Redes:** puentes, redes NAT/aisladas y redes L2 unificadas estilo
  Proxmox, con cortafuegos por VM.
- **Copias de seguridad** programadas y bajo demanda, con restauración e
  importación/exportación en formato vzdump de Proxmox para contenedores.
- **Plantillas** de VM y contenedor, con un interruptor por plantilla
  «compartir con todos los usuarios» reservado a administradores: una
  plantilla compartida la puede listar e instanciar cualquier usuario
  sin importar propietario, grupo ni etiquetas, y cada instanciación
  copia su disco completo.
- **Usuarios, roles y cuotas:** RBAC con ACL por recurso, cuotas de
  recursos, 2FA/TOTP, tokens de API y registro de auditoría.
- **Cloud-init** para la configuración inicial de invitados (usuarios,
  claves SSH, red, paquetes).
- **Tienda de aplicaciones:** appliances y *helper scripts* listos para
  desplegar, y catálogo de imágenes cloud oficiales.
- **Passthrough PCI y USB** con comprobación previa de IOMMU/VFIO.
- **Interfaz en tres idiomas:** inglés, español y catalán.
- **Instalación:** instalador standalone multidistro (apt/dnf/pacman) con
  HTTPS autofirmado y *rollback*, imagen Docker, paquetes deb/rpm y un
  cliente de línea de comandos (`webkvm-cli`).


---

[0.1.2-fix2]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.2-fix2
[0.1.2-fix1]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.2-fix1
[0.1.2]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.2
[0.1.1]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.1
[0.1.0]: https://github.com/Slaker19/webkvm/releases/tag/v0.1.0
[0.0.1]: https://github.com/Slaker19/webkvm/releases/tag/v0.0.1
