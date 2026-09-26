# Manual de Referencia de `webkvm-cli`

Guía técnica completa para la herramienta de línea de comandos `webkvm-cli`, diseñada para automatización, scripting y administración vía SSH.

[**English**](CLI.md) • [**Español**](CLI.es.md)

---

## Descripción General

`webkvm-cli` es un cliente de terminal autónomo que interactúa directamente con la API REST de WebKVM.

### Características Clave
- **Acceso Local Zero-Token**: Al ejecutarse en el servidor anfitrión como root, lee la clave local `/opt/webkvm/jwt.key` y genera un token administrativo automáticamente sin pedir contraseñas.
- **Autenticación Remota**: Permite iniciar sesión con `webkvm-cli login`, guardando el token de sesión de forma segura en `~/.config/webkvm/token`.
- **Modo Scripting (`--json`)**: La opción global `--json` devuelve respuestas JSON estructuradas ideales para procesar con `jq`.
- **Salida en Tablas Alineadas**: Formato de texto limpio y ajustado para consolas de administración.

---

## Opciones Globales

```text
webkvm-cli [OPCIONES] <COMANDO> [ARGUMENTOS...]

Opciones:
  --server URL    URL del servidor WebKVM (por defecto: https://127.0.0.1:8080 o $WEBKVM_SERVER)
  --token TOKEN   Token de API o JWT (o $WEBKVM_TOKEN). Auto-detectado en localhost.
  --insecure      Omitir verificación de certificados TLS autofirmados (por defecto: true)
  --secure        Exigir verificación estricta de certificados TLS
  --json          Salida en formato JSON estructurado
  -h, --help      Muestra la ayuda
```

---

## Autenticación

### Ejecución Local en el Servidor Anfitrión
Si ejecutas el comando directamente en el host donde corre WebKVM:

```bash
sudo webkvm-cli vms list
sudo webkvm-cli storage pools
```

### Ejecución Remota desde otra máquina
Para gestionar un servidor WebKVM remoto desde tu equipo:

```bash
# 1. Iniciar sesión (solicita usuario y contraseña interactivamente)
webkvm-cli --server https://192.168.1.10:8080 login

# 2. Los siguientes comandos usan el token guardado en ~/.config/webkvm/token
webkvm-cli --server https://192.168.1.10:8080 vms list

# 3. Cerrar sesión y eliminar el token en caché
webkvm-cli logout
```

---

## Estado y control del servidor

| Comando | Qué hace |
|---------|----------|
| `webkvm-cli status` | Comprobación de salud (`GET /api/health`): imprime `status`, `data_dir` y si libvirt responde. |
| `webkvm-cli info` | Resumen del host (`GET /api/host`): CPU, memoria, hipervisor y capacidades. |
| `webkvm-cli restart` | Pide al backend que se reinicie (`POST /api/system/restart`). Solo administradores. |

---

## Copias de seguridad (`backup`)

| Comando | Qué hace |
|---------|----------|
| `webkvm-cli backup targets` | Lista los destinos de copia configurados. |
| `webkvm-cli backup run` | Lanza una copia ahora. |
| `webkvm-cli backup jobs` | Lista los trabajos de copia con estado, inicio y tamaño. |
| `webkvm-cli backup schedules` | Lista las copias programadas. |

---

## Máquinas Virtuales y Contenedores (`vms`)

### Listar Instancias
```bash
# Formato tabular
webkvm-cli vms list

# Formato JSON con filtro jq
webkvm-cli --json vms list | jq .
```

### Crear Instancia
Creación de VMs QEMU/KVM o Contenedores de Sistema Incus:

```bash
# Crear una VM Debian 12 KVM con Cloud-Init
webkvm-cli vms create \
  --name servidor-web \
  --ram 2048 \
  --vcpus 2 \
  --disk 20 \
  --image debian-12 \
  --user admin \
  --password "ContraseñaSegura123!" \
  --ssh-key "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5..."

# Crear un contenedor Incus Alpine Linux
webkvm-cli vms create \
  --name alpine-ct \
  --type container \
  --ram 512 \
  --vcpus 1 \
  --disk 10 \
  --image images:alpine/3.21

# Crear una VM clásica con ISO
webkvm-cli vms create \
  --name bsd-test \
  --ram 4096 \
  --vcpus 4 \
  --disk 50 \
  --iso FreeBSD-14.0-RELEASE-amd64-disc1.iso \
  --pool webkvm-disks
```

### Acciones de Ciclo de Vida
```bash
webkvm-cli vms start <id>
webkvm-cli vms stop <id>        # Apagado ACPI limpio
webkvm-cli vms forceoff <id>    # Apagado forzado inmediato (destroy)
webkvm-cli vms reboot <id>
webkvm-cli vms suspend <id>
webkvm-cli vms resume <id>
webkvm-cli vms delete <id>
webkvm-cli vms clone <id>
webkvm-cli vms autostart <id> <on|off>
```

### Snapshots
```bash
# Listar snapshots de una instancia
webkvm-cli vms snapshots <id>

# Crear snapshot
webkvm-cli vms snapshot <id> create "punto-restauracion-1"

# Revertir a snapshot
webkvm-cli vms snapshot <id> revert <snapshot-id>

# Eliminar snapshot
webkvm-cli vms snapshot <id> delete <snapshot-id>
```

---

## Image Hub (`images`)

Gestión de discos cloud oficiales e imágenes de contenedores:

```bash
# Listar todas las imágenes locales y disponibles
webkvm-cli images list

# Descargar imagen base cloud oficial
webkvm-cli images pull-cloud ubuntu-24.04
webkvm-cli images pull-cloud alpine-3.24
webkvm-cli images pull-cloud debian-12

# Descargar imagen de contenedor Incus
webkvm-cli images pull-container images:alpine/3.21
webkvm-cli images pull-container images:centos/10-stream

# Eliminar imágenes cacheadas en local
webkvm-cli images delete-cloud alpine-3.24
webkvm-cli images delete-container <fingerprint>
```

---

## Gestión de Almacenamiento (`storage`)

```bash
# Listar pools de almacenamiento (KVM e Incus)
webkvm-cli storage pools

# Crear un nuevo pool KVM VDI o ISO
webkvm-cli storage pool-create backup-pool --type dir --purpose disk --path /opt/webkvm/pools/backup-pool

# Crear un nuevo pool Incus (LXC) para contenedores
webkvm-cli storage pool-create lxc-nvme --purpose container --type dir --path /var/lib/incus/storage-pools/lxc-nvme
webkvm-cli storage pool-create lxc-zfs --purpose container --type zfs

# Eliminar un pool de almacenamiento
webkvm-cli storage pool-delete backup-pool

# Listar volúmenes de disco (KVM o Incus)
webkvm-cli storage volumes
webkvm-cli storage volumes webkvm-disks
webkvm-cli storage volumes default

# Listar biblioteca de ISOs
webkvm-cli storage isos

# Descargar una ISO desde una URL directamente a un pool
webkvm-cli storage download-iso \
  --url "https://releases.ubuntu.com/24.04/ubuntu-24.04-live-server-amd64.iso" \
  --name "ubuntu-24.04-server.iso" \
  --pool webkvm-isos
```

### Discos Físicos del Host (`host disks`)

```bash
# Listar discos físicos (tamaño, fstype, puntos de montaje)
webkvm-cli host disks list

# Inspeccionar una imagen de disco de un pool antes de adjuntarla (formato, tamaño, tiene-datos)
webkvm-cli host disks probe /opt/webkvm/pools/webkvm-disks/ubuntu-26.04.qcow2

# Inspección profunda (SO/particiones, requiere libguestfs en el host; usa un job)
webkvm-cli host disks probe /opt/webkvm/pools/webkvm-disks/ubuntu-26.04.qcow2 --deep
```

---

## Cloud-Init Studio y Recetas (`snippets`)

Gestiona recetas YAML de aprovisionamiento declarativo:

```bash
# Listar todas las recetas de Cloud-Init (Predefinidas oficiales + Personalizadas)
webkvm-cli snippets list

# Ver contenido YAML completo y metadatos de una receta
webkvm-cli snippets show preset-k3s
webkvm-cli snippets show preset-docker

# Crear un nuevo snippet personalizado
webkvm-cli snippets create \
  --name "PostgreSQL Bootstrap" \
  --category devops \
  --type user-data \
  --desc "Instala PostgreSQL 16 y configura base de datos inicial" \
  --file ./postgres-init.yaml

# Eliminar un snippet personalizado
webkvm-cli snippets delete <snippet-id>
```

---

## Gestión de Redes (`networks`)

```bash
# Listar redes y bridges
webkvm-cli networks list

# Ver detalle de configuración de una red
webkvm-cli networks show vmbr0

# Consultar concesiones DHCP activas
webkvm-cli networks leases vmbr1

# Iniciar o detener un bridge de red
webkvm-cli networks start <nombre>
webkvm-cli networks stop <nombre>
```

---

## Ajustes del Servidor (`settings`)

Consultar y modificar parámetros en caliente:

```bash
# Listar todos los ajustes y sus valores actuales
webkvm-cli settings list

# Obtener el valor de un ajuste específico
webkvm-cli settings get terminal.idle_timeout_min

# Modificar un ajuste (se aplica de inmediato si es hot-reloadable)
webkvm-cli settings set terminal.idle_timeout_min 0
webkvm-cli settings set logging.level debug
```

---

## Registro de Auditoría (`audit`)

```bash
# Ver las últimas 20 entradas de auditoría
webkvm-cli audit list

# Ver entradas en JSON estructurado para integración SIEM
webkvm-cli --json audit list --limit 100 | jq .
```

---

## Usuarios y Tokens (`users`, `tokens`)

```bash
# Listar cuentas de usuario
webkvm-cli users list

# Crear cuenta de operador
webkvm-cli users create operador1 "ClaveSegura123!" --role operator --email user@example.com

# Eliminar cuenta
webkvm-cli users delete operador1

# Listar y crear tokens de API permanentes
webkvm-cli tokens list
webkvm-cli tokens create "token-para-ci-cd"
```
