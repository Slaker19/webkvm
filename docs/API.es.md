# WebKVM — Especificación de la API REST y WebSockets

Referencia técnica completa para desarrolladores e integraciones con la API de WebKVM.

[**English**](API.md) • [**Español**](API.es.md)

---

## Métodos de Autenticación

WebKVM admite tres mecanismos de autenticación según el tipo de cliente:

1. **Cookies de Sesión (Interfaz Web SPA)**:
   - Obtenidas vía `POST /api/auth/login`.
   - Utiliza cookies `session` HttpOnly y protección CSRF double-submit.
2. **Tokens de API Bearer (CLI y Scripts externos)**:
   - Cabecera HTTP: `Authorization: Bearer wvmb_<token>`
   - Creados desde el panel o con `webkvm-cli tokens create <nombre>`.
3. **Tickets de WebSocket de Un Solo Uso (Consolas y Terminal)**:
   - Los endpoints de WebSocket (`/api/vms/{id}/vnc`, `/api/vms/{id}/serial`, `/api/host/terminal`) aceptan tickets efímeros (`?ticket=<token>`).
   - Tienen un TTL de 30 segundos en memoria y se queman tras el primer handshake de conexión, eliminando credenciales permanentes en URLs.
   - `?ticket=` está limitado a endpoints de terminal/SSE (`/api/host/terminal`, `/api/events`, `/api/vms/{id}/serial`): presentar un ticket de consola válido en cualquier otra ruta `/api/*` devuelve `403`, así que un ticket de consola nunca puede sustituir a una sesión completa.

---

## Referencia de Endpoints

### 1. Autenticación y Cuentas

#### `POST /api/auth/login`
Inicia sesión y genera las cookies o el token JWT.
- **Cuerpo de la Petición**:
  ```json
  {
    "username": "admin",
    "password": "tu-contraseña"
  }
  ```
- **Respuesta `200 OK`**:
  ```json
  {
    "token": "eyJhbGciOiJIUzI1NiIs...",
    "csrf": "6f2e...",
    "user": "admin",
    "role": "admin",
    "must_change_password": false
  }
  ```

#### `GET /api/auth/me`
Devuelve el perfil y rol del usuario autenticado.

---

### 2. Máquinas Virtuales y Contenedores

#### `GET /api/vms`
Devuelve la lista de máquinas virtuales KVM y contenedores Incus.
- **Respuesta `200 OK`**:
  ```json
  [
    {
      "id": "3036beb7-4116-4d73-8db8-a71e94033526",
      "name": "ubuntu-srv",
      "type": "vm",
      "state": "running",
      "vcpus": 2,
      "ram_mb": 2048,
      "disk_gb": 20,
      "ip": "192.168.1.150",
      "autostart": true
    }
  ]
  ```

#### `POST /api/vms`
Crea una nueva máquina virtual KVM o un contenedor Incus.
- **Cuerpo de la Petición**:
  ```json
  {
    "name": "debian-prod",
    "type": "vm",
    "vcpus": 2,
    "ram_mb": 2048,
    "disk_gb": 20,
    "image": "debian-12",
    "storage_pool": "webkvm-disks",
    "network": "vmbr0",
    "cloud_init": {
      "user": "admin",
      "password": "ClaveSegura123!",
      "ssh_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5...",
      "hostname": "debian-prod"
    }
  }
  ```

#### Acciones de Ciclo de Vida
- `POST /api/vms/{id}/start` — Encender instancia
- `POST /api/vms/{id}/shutdown` — Apagado ACPI limpio
- `POST /api/vms/{id}/forceoff` — Apagado forzado inmediato (destroy)
- `POST /api/vms/{id}/reboot` — Reiniciar instancia
- `POST /api/vms/{id}/suspend` — Suspender VM o congelar contenedor
- `POST /api/vms/{id}/resume` — Reanudar ejecución
- `DELETE /api/vms/{id}` — Eliminar instancia y sus discos asociados

#### Discos de VM
- `POST /api/vms/{id}/disks` — Adjuntar una imagen existente (`source`) o crear un volumen nuevo (`pool`, `size_gb`, `format`). Adjuntar una imagen que ya contiene datos se rechaza con `409` salvo que la petición incluya `"force": true`; adjuntar un volumen ya adjunto a otra VM también se rechaza con `409`.
- `GET /api/vms/{id}/disks/{dev}/probe` — Inspección de solo lectura de un disco adjunto (formato, tamaño virtual/asignado, `has_data`). Accesible a viewers autenticados.

#### Migración de Almacenamiento
- `POST /api/vms/{id}/move-storage` — Mover el almacenamiento de una instancia a otro pool. Devuelve `202 Accepted` y un `job_id`; consulta `GET /api/storage/jobs/{id}` para el progreso a nivel de bytes.
  - **Cuerpo de la Petición**: `{"pool": "webkvm-incus-2"}`
  - El propósito del pool destino debe coincidir con la instancia: `container` para un contenedor Incus, `disk` (o `template`) para una VM de KVM. Un desajuste se rechaza con `400` (`pool "X" cannot hold a container`).
  - **VMs de KVM**: cada disco qcow2/raw se copia al pool destino y se reescribe el XML del dominio.
  - **Contenedores Incus**: el rootfs *y sus snapshots* se reubican con la migración nativa de instancias de Incus. El contenedor debe estar **detenido** (`409` en caso contrario) y el destino debe ser distinto del pool actual.
  - Mover entre hipervisores (un pool de libvirt ⇄ un pool de Incus) se rechaza por diseño: exporta y vuelve a importar.

#### Metadatos de la Instancia
- `PUT /api/vms/{id}/meta` — Actualiza alias, notas, portada, grupos, propietario y las marcas `template` / `shared`.
  - `"shared": true` en una plantilla la hace visible e instanciable por **todos** los usuarios, saltándose el alcance por propietario, grupo y etiquetas. **Solo administradores.** Cada instanciación copia el disco completo de la plantilla. Los clones nunca heredan la marca.

#### Hardware y Dispositivos
- `GET /api/vms/{id}` — Detalle completo de la instancia; `PATCH /api/vms/{id}` actualiza vCPU, RAM, modelo/topología de CPU, firmware y ajustes de disco.
- `GET`/`POST /api/vms/{id}/boot` — Leer o fijar el dispositivo de arranque (solo `disk`, `cdrom` o `network`; cualquier otro valor se rechaza).
- `GET`/`POST`/`DELETE /api/vms/{id}/networks[/{mac}]` — Listar, añadir, editar (`PATCH`) o quitar interfaces de red. `GET /api/vms/{id}/vlan-support` indica si el bridge subyacente acepta etiquetas VLAN.
- `POST /api/vms/{id}/usb` y `DELETE /api/vms/{id}/usb/{vendorId}/{productId}` — Conectar y desconectar un dispositivo USB del host. **Solo administradores.**
- `POST /api/vms/{id}/pci` — Asignar una o varias direcciones PCI del host a una VM **apagada**: `{"addresses": ["0000:01:00.0", "0000:01:00.1"]}`. **Solo administradores.** Normalmente se pasan de golpe todas las direcciones de un mismo grupo IOMMU; la GPU de arranque/consola se rechaza siempre. Se audita como `vm.pci_attach`.
- `DELETE /api/vms/{id}/pci/{address}` — Desasignar un dispositivo. La dirección viaja codificada en la URL (contiene `:` y `.`). Se audita como `vm.pci_detach`.
- `POST`/`DELETE /api/vms/{id}/shared-folders[/{tag}]` — Gestionar carpetas compartidas virtiofs.
- `GET /api/vms/{id}/graphics`, `/spice`, `/rdp` — Configuración gráfica y ficheros de conexión `.vv` / `.rdp` descargables.

#### Snapshots
- `GET`/`POST /api/vms/{id}/snapshots` — Listar o crear un snapshot (`{"name": "...", "memory": false}`). Los snapshots de solo disco de una VM encendida son consistentes a nivel de sistema de ficheros cuando el agente invitado responde, y si no, se repliegan a consistencia ante caída.
- `DELETE /api/vms/{id}/snapshots/{sid}` y `POST /api/vms/{id}/snapshots/{sid}/revert`.
- `GET /api/vms/snapshots` — Lista global de snapshots, limitada a las ACL de VM del usuario.

#### Integración con el Invitado y Monitorización
- `GET /api/vms/{id}/guest-info` — Datos que reporta `qemu-guest-agent` (IPs, nombre de host, SO).
- `POST /api/vms/{id}/reset-password` — Restablecer la contraseña de una cuenta del invitado vía agente.
- `POST /api/vms/{id}/clipboard` — Enviar texto al portapapeles del invitado.
- `GET /api/vms/{id}/metrics` y `/metrics/history` — Contadores de CPU/RAM/IO en vivo e históricos.
- `GET /api/vms/{id}/logs` — Últimas líneas de log de libvirt/QEMU de la instancia.
- `GET`/`PUT /api/vms/{id}/alerts` — Umbrales de alerta por instancia.
- `GET`/`PUT /api/vms/{id}/schedule` — Encendido y apagado automáticos por cron.

#### Clonado, Plantillas y Cloud-Init
- `POST /api/vms/{id}/clone` — Duplicar una instancia con sus discos.
- `POST /api/vms/{id}/make-template` y `POST /api/vms/{id}/unset-template`.
- `GET`/`POST /api/vms/{id}/cloudinit` — Leer o reemplazar el user-data NoCloud de la instancia.
- `POST`/`DELETE /api/vms/{id}/cover` — Fijar o borrar la imagen de la tarjeta.

#### Catálogo de Incus
- `GET /api/vms/incus-images` — Imágenes de distribución que ofrece el asistente de contenedores.
- `GET /api/vms/incus-profiles` — Perfiles de Incus disponibles.

---

### 2.1. Plantillas

- `GET /api/templates` — Lista las plantillas visibles para quien llama: las propias o concedidas, más todas las marcadas como `shared`.
- `POST /api/templates/{id}/instantiate` — Crear una instancia a partir de una plantilla, copiando su disco. Acepta campos de cloud-init (usuario, contraseña, clave SSH, nombre de host) para el aprovisionamiento NoCloud.

---

### 3. Image Hub y Tareas en Segundo Plano

#### `GET /api/images/cloud-base`
Lista las imágenes base oficiales disponibles y su estado en la caché local.

#### `POST /api/images/cloud-base/pull`
Inicia la descarga en segundo plano de una imagen cloud oficial.
- **Cuerpo de la Petición**: `{"id": "ubuntu-24.04"}`
- **Respuesta `202 Accepted`**:
  ```json
  {
    "id": "ubuntu-24.04",
    "job_id": "base_img_1789672293311288289",
    "status": "queued"
  }
  ```

#### `GET /api/storage/jobs/{id}`
Consulta el progreso, velocidad de descarga en vivo y estado de una tarea.
- **Respuesta `200 OK`**:
  ```json
  {
    "id": "base_img_1789672293311288289",
    "name": "Download Ubuntu 24.04",
    "progress": 48.47,
    "status": "downloading",
    "bytes_done": 89050176,
    "bytes_total": 183697408,
    "speed_bps": 50447203,
    "eta_seconds": 1,
    "updated_at": 1789672295
  }
  ```

---

### 4. Almacenamiento y Pools

- `GET /api/storage/pools` — Listar pools con capacidad y propósito. Un propósito es uno o varios de `disk` (imágenes de VM de libvirt), `container` (rootfs de Incus), `iso`, `backup` o `template`, separados por comas cuando un pool sirve para varios.
- `POST /api/storage/pools` — Crear un pool local, NFS o CIFS.
- `GET /api/storage/volumes` — Listar volúmenes de disco en el sistema (limitado a los `AllowedPools` del usuario; usuarios restringidos ven lista vacía en otros pools).
- `GET /api/storage/isos` — Listar archivos ISO disponibles (mismo filtrado por pool; la subida/descarga de ISOs también exige `AllowedPools`).
- `POST /api/storage/isos/download` — Descargar una ISO directamente a un pool de tipo `iso`.
- `POST /api/storage/probe-disk` — Inspeccionar una imagen de disco de un pool antes de adjuntarla: `{"path": "...", "deep": false}` devuelve `{format, virtual_size, allocated, has_data, backing_file}` vía `qemu-img`. Con `"deep": true` (requiere `libguestfs` en el host, ver `guestfs` en capacidades del host) devuelve `202` + un job; consultar `GET /api/storage/jobs/{id}` para el informe de SO/particiones/filesystems.

---

### 5. Consolas y WebSockets

- `POST /api/vms/{id}/console-ticket` — Generar ticket de 30s para consola serie de VM.
- `GET /api/vms/{id}/serial?ticket={tk}` — WebSocket a la consola serie del invitado.
- `POST /api/vms/{id}/vnc-ticket` — Generar ticket para consola gráfica VNC.
- `GET /api/vms/{id}/vnc?vt={ticket}` — WebSocket proxy para el framebuffer noVNC.
- `POST /api/host/terminal-ticket` — Generar ticket de administrador para la shell del host.
- `GET /api/host/terminal?ticket={tk}&cols={N}&rows={N}` — Flujo PTY interactivo acelerado con WebGL.

---

### 6. Eventos en Tiempo Real (Server-Sent Events)

#### `GET /api/events`
Abre una conexión SSE unidireccional que emite eventos del hipervisor en vivo:
- `vm.state_changed`: Transiciones de estado (`running`, `shutoff`, `paused`).
- `vm.created` / `vm.deleted`: Creación o eliminación de instancias.
- `task.progress`: Actualizaciones de progreso de tareas en segundo plano.

---

### 7. Capacidades del Host y Discos Físicos

- `GET /api/host/capabilities` — Informa de lo que acepta el QEMU/libvirt local (modelos de vídeo/gráficos, buses de disco, modelos/flags de CPU, `spice_supported`, `parsed`). Incluye `guestfs` / `guestfs_bin`: si `virt-inspector` + `guestfish` están instalados, lo que habilita la inspección profunda de discos.
- `GET /api/host/disks` — Lista discos físicos con `fstype`, puntos de montaje y particiones hijas (vía `lsblk`).
- `POST /api/host/capabilities/refresh` — Volver a sondear QEMU/libvirt en vez de servir el informe cacheado.
- `GET /api/host/disks/filesystems` y `GET /api/host/disks/orphan-mounts` — Sistemas de ficheros detectados y montajes cuyo dispositivo de respaldo ya no existe.
- `POST /api/host/disks/wipe` y `POST /api/host/disks/initialize-directory` (solo admin) — Las guardas destructivas son **fail-closed**: si la sonda de seguridad `lsblk` no puede ejecutarse o parsearse, la petición se rechaza con `503` en vez de continuar. `mount_point` debe vivir bajo `/mnt/` o `/srv/`; si el paso de wipe falla, se aborta toda la operación.
- `GET /api/host/pci-devices` — Dispositivos PCI del host disponibles para passthrough, agrupados por grupo IOMMU. **Solo administradores.**
- `GET /api/host/pci-preflight` — Estado de IOMMU/VFIO: si IOMMU está activo, cuántos grupos hay y si son asignables de forma limpia. La interfaz bloquea la asignación cuando esto indica que el grupo del dispositivo arrastraría dispositivos no relacionados.
- `GET /api/host/usb-devices` — Dispositivos USB conectados al host. **Solo administradores.**
- `GET /api/host/stats` y `GET /api/host/metrics` — Contadores de CPU, memoria, carga y almacenamiento del host.

---

### 8. Redes

- `GET`/`POST /api/networks` — Listar o crear un bridge del host (IP opcional, rango DHCP, puerta de enlace, DNS, autoarranque).
- `PUT`/`DELETE /api/networks/{id}` — Actualizar o eliminar un bridge.
- `POST /api/networks/{id}/start` y `/stop` — Levantar o bajar un bridge gestionado.
- `GET /api/networks/{id}/leases` y `DELETE /api/networks/{id}/leases/{mac}` — Consultar y liberar concesiones DHCP.
- `GET /api/host/interfaces` — Tarjetas de red físicas y bridges existentes en el host.

---

### 9. Firewall

Las reglas por VM viven en la instancia; las reglas del host usan un protocolo
de **Safe-Apply** que no te puede dejar fuera.

- `GET`/`PUT /api/vms/{id}/firewall` — Reglas de entrada por VM y redirecciones de puerto host→VM.
- `GET /api/firewall/host` — Conjunto de reglas del host actualmente confirmado.
- `POST /api/firewall/host/preview` — Renderiza el conjunto nftables que produciría un payload, sin aplicarlo.
- `POST /api/firewall/host/apply` — Aplica el conjunto nuevo de forma provisional y arranca un temporizador de confirmación (30 s por defecto, configurable con `firewall.confirm_window_secs` entre 10 y 300). Devuelve el conjunto anterior y el `deadline`. Solo puede haber un apply pendiente a la vez (`400` en caso contrario).
- `POST /api/firewall/host/confirm` — Confirma el conjunto provisional. **Si esta llamada no llega antes del deadline, se restaura automáticamente el último conjunto confirmado** — el mecanismo que protege al administrador remoto que acaba de cerrarse su propio SSH.
- `POST /api/firewall/host/rollback` — Revierte de inmediato el conjunto provisional; devuelve `{"status": "no_pending"}` si no hay nada pendiente.
- `GET /api/firewall/host/export` y `POST /api/firewall/host/import` — Conjunto de reglas portable en JSON (límite de 1 MB de cuerpo).

---

### 10. Copias de Seguridad

- `GET`/`POST /api/backup/targets` — Listar o crear un destino (carpeta local, montaje NFS/SMB, SFTP).
- `PUT`/`DELETE /api/backup/targets/{id}` — Actualizar o eliminar un destino; `DELETE /api/backup/targets/{id}/config` borra solo su archivo de configuración almacenado.
- `POST /api/backup/targets/test` — Validar conectividad y permisos de escritura antes de guardar.
- `POST /api/backup/targets/{id}/run` — Lanzar una copia; devuelve el identificador del trabajo.
- `GET /api/backup/targets/{id}/files` y `DELETE …/files/{filename}` — Explorar y purgar los archivos de un destino.
- `GET /api/backup/targets/{id}/verify` — Recomprobar el SHA-256 de los archivos almacenados.
- `POST /api/backup/targets/{id}/restore` — Restauración completa (con límites de tamaño y comprobaciones de rutas protegidas).
- `POST /api/backup/targets/{id}/restore-as-vm` — Reimportar un archivo como una VM nueva.
- `DELETE /api/backup/targets/{id}/runs/{suffix}` — Borrar una ejecución completa.
- `GET`/`POST /api/backup/schedules`, `PUT`/`DELETE /api/backup/schedules/{id}` — Programaciones cron con retención conservar-últimos / conservar-días.
- `GET /api/backup/jobs` y `GET /api/vms/{id}/backup/jobs` — Historial de ejecuciones, global o por instancia.
- `POST /api/vms/{id}/backup` — Copiar una única instancia a un destino concreto; `GET /api/vms/{id}/backup/targets` lista los destinos válidos.

Los secretos (webhooks, SMTP, SFTP, credenciales CIFS) nunca se escriben en una
copia de configuración.

---

### 11. Usuarios, Grupos, Cuotas y Tokens

- `GET`/`POST /api/users` — Listar o crear usuarios (`role`: `admin`, `operator`, `viewer`). **Solo administradores.**
- `PUT`/`DELETE /api/users/{username}` — Actualizar rol, grupo, `allowed_pools` y cuotas, o borrar la cuenta.
- `POST /api/users/{username}/revoke-sessions` — Invalidar todas las sesiones activas de ese usuario.
- `GET /api/users/{username}/usage` y `GET /api/users/me/usage` — Consumo actual frente a la cuota (número de VMs, vCPU, RAM, disco).
- `PUT /api/users/me/password` — Cambiar la propia contraseña; aplica la política de contraseñas y limpia la marca `must_change_password`.
- `GET`/`POST`/`PUT`/`DELETE /api/groups[/{name}]` — Gestionar los grupos usados para el alcance de las ACL.
- `GET`/`POST /api/tokens` — Listar o emitir tokens de API (`wvmb_…`, se devuelven una sola vez, se guardan como hash SHA-256, caducidad de 30 días por defecto).
- `POST /api/tokens/{id}/revoke` y `DELETE /api/tokens/{id}` — Revocar de inmediato (se persiste en `revoked.json`) o borrar el registro.

Las cuotas cubren vCPU/RAM de las instancias **en ejecución** y el disco de
forma **global** en todos los pools. Instanciar una plantilla carga la cuota al
usuario que la instancia. Los administradores están exentos de cuotas y de ACL
de pool por diseño.

---

### 12. Snippets de Cloud-Init

- `GET`/`POST /api/cloudinit/snippets` — Listar o crear snippets de user-data reutilizables.
- `GET`/`PUT`/`DELETE /api/cloudinit/snippets/{id}` — Gestionar un snippet.
- `POST /api/cloudinit/preview` — Renderizar el user-data NoCloud final de un conjunto de valores sin crear nada.

---

### 13. Appliances, Helper Scripts y Media

- `GET`/`POST /api/appliances` — Catálogo de appliances de la comunidad; `PUT`/`DELETE /api/appliances/{id}` para entradas propias.
- `POST /api/appliances/{id}/deploy` — Desplegar un appliance. La red destino se valida contra los bridges disponibles (`400` antes de crear ningún job) y el payload de cloud-init se valida antes de encolar. Un fallo de inicialización marca el job como `error`, no escribe `AppInfo` y audita `appliance.deploy_cloudinit_failed`.
- `GET /api/appliances/{id}/provision` — Script de aprovisionamiento que ejecuta el appliance en el primer arranque.
- `GET /api/helper-scripts`, `POST /api/helper-scripts/refresh`, `GET /api/helper-scripts/{slug}/provision` — Catálogo de helper scripts de la comunidad.
- `GET`/`POST /api/media`, `POST /api/media/upload`, `DELETE /api/media/{id}`, `GET /api/media/{id}/raw`, `POST /api/media/apply-usage` — Portadas de tarjetas y recursos de marca.

---

### 14. Sistema, Ajustes, Notificaciones y Auditoría

- `GET /api/system/status` — Estado del backend, de libvirt y del host, más actualizaciones disponibles.
- `GET /api/system/logs` — Logs recientes del servicio.
- `POST /api/system/update` — Lanza la actualización desde la aplicación (solo instalaciones nativas; ver DOCKER.es.md). Requiere que el backend se ejecute como root y que el entorno del servicio tenga `WEBKVM_ALLOW_UPDATE=1`; si no, devuelve `403`. Responde `202` con `{status, mode, updater, log}`; `mode` es `release` (instala la release verificada de GitHub) o `source` (recompila desde el checkout), elegido automáticamente según si `REPO_DIR` es un checkout de git. Devuelve `503` si falta el script actualizador o `systemd-run`. El progreso se añade a la ruta `log` devuelta.
- `POST /api/system/restart` y `POST /api/system/apply-restart` — Reinicia el servicio, aplicando antes los ajustes pendientes si procede.
- `POST /api/system/backup` y `GET /api/system/backups` — Copias del datadir.
- `GET /api/system/cert` — Descarga el certificado del servidor para poder confiarlo en local (sin autenticación, por diseño).
- `GET`/`PUT /api/settings` — Leer o escribir la configuración del servidor; `GET /api/settings/schema` describe cada campo, `POST /api/settings/apply-live` aplica lo que puede cambiar sin reiniciar y `POST /api/settings/reset` restaura los valores por defecto.
- `GET`/`PUT /api/notify/config`, `GET /api/notify/events`, `POST /api/notify/test` — Canales de notificación por webhook/SMTP, lista de eventos suscribibles y envío de prueba.
- `GET`/`POST`/`PUT`/`DELETE /api/nodes[/{id}]` — Registro de nodos remotos para vistas multi-host.
- `GET /api/audit` — Registros de auditoría paginados y más recientes primero, sobre el log activo y los rotados; `GET /api/audit/export` los exporta para archivado. **Solo administradores.**
- `GET /api/alerts/active`, `GET /api/tags`, `GET /api/jobs/{id}` — Alertas activas, lista global de etiquetas y consulta genérica de trabajos.

---

### Exportación VM (`GET /api/vms/{id}/export`)
- `?format=proxmox` — Genera archivo `vzdump-lxc-*.tar.zst` compatible con `pct restore` de Proxmox. Solo para contenedores `hypervisor=="incus"`. Genera `etc/vzdump/pct.conf` + árbol `rootfs/` (comprimido con zstd).

### Importación VM (`POST /api/vms/import`)
- Detecta automáticamente archivos Proxmox `vzdump-lxc-*.tar.zst` / `.tar.gz` (con `./etc/vzdump/pct.conf` o rootfs en raíz) y los convierte a backups nativos de Incus mediante `internal/vzdump`. Maneja layout Proxmox (archivos en raíz) e Incus (`rootfs/`), hard links y symlinks. El resultado sigue la convención `backup/container/` de Incus (`backup.yaml`, `index.yaml`, `rootfs/`).
- El campo **nombre del contenedor** es obligatorio al importar.
