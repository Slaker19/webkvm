# WebKVM — Guía de instalación y documentación técnica

Documento único que cubre la **instalación** del gestor WebKVM y la
**documentación técnica** de la aplicación y su código. Para el uso diario
consulta [USAGE.es.md](USAGE.es.md).

[**English**](INSTALLATION.md) • [**Español**](INSTALLATION.es.md)

---

## Parte I — Instalación

### 1. Requisitos

| Requisito | Detalle |
|-----------|---------|
| Sistema operativo | Debian/Ubuntu (apt), Fedora/RHEL (dnf) o Arch (pacman) |
| Arquitectura | amd64 / x86_64 |
| KVM | `/dev/kvm` presente (virtualización activada en la BIOS/UEFI, o anidada en el hipervisor) |
| RAM | 2 GB como mínimo |
| Disco | 5 GB libres como mínimo |
| Red | salida a internet (paquetes y descargas de herramientas) |
| Incus *(opcional, contenedores)* | el daemon **Incus** instalado y en marcha en el host para gestionar **contenedores LXC**. Omítelo si solo vas a usar KVM. Ver [Contenedores (LXC)](#7-contenedores-lxc--híbrido-kvmlxc) más abajo. |

### 2. Instalación con un solo comando

```bash
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-webkvm.sh | sudo bash
# pregunta: solo IP (HTTP) o con SSL — el certificado autofirmado sirve para IP y dominio
# + dominio opcional (p. ej. webkvm.example.com) + modo de red (nat / bridge / both)
```

El script descarga el repositorio (tarball, no hace falta `git`) a un directorio
de trabajo bajo `/var/tmp` y ejecuta el instalador autónomo. La salida completa
queda en **`/var/log/webkvm-install.log`**; si algo falla, el directorio de
trabajo se conserva para que puedas adjuntarlo a un informe de error.

O desde un clon del repositorio:

```bash
git clone https://github.com/Slaker19/webkvm
cd webkvm
sudo ./install.sh              # delega en packaging/standalone/install.sh
sudo ./packaging/standalone/install.sh --dry-run   # comprueba sin tocar nada
```

Lo que hace el instalador:

1. Comprobaciones previas (KVM, RAM ≥ 2 GB, disco ≥ 5 GB, amd64) con errores
   accionables.
2. Instala libvirt, QEMU, OVMF, swtpm, xorriso, dnsmasq y las herramientas de
   gestión de discos (gdisk/parted, e2fsprogs, xfsprogs, btrfs-progs,
   f2fs-tools — las usa Almacenamiento > Discos del host para formatear y montar
   discos físicos) vía apt/dnf/pacman. En Arch refresca antes
   `archlinux-keyring` (las ISOs antiguas traen claves caducadas).
3. Despliega el **binario precompilado** (frontend embebido). El servidor
   **nunca compila**: no necesita cadena de herramientas de Go ni Node.
4. Instala `webkvm.service`, con los datos en `/opt/webkvm`.
5. Pregunta por HTTPS y por la red de forma interactiva, o aplica los valores
   por defecto cuando se ejecuta por tubería (`curl … | sudo bash` → no
   interactivo: HTTPS sí, red `both` — bridge L2 compartido **y** red NAT
   aislada — e Incus instalado).
6. Comprueba la salud del servicio (`/api/health`) e imprime el resumen: URL,
   contraseña de administrador y redes.

### 3. Instalación desde un binario precompilado

El binario lleva el frontend embebido: es el único artefacto que necesita el
servidor (aparte de libvirt/QEMU del sistema). Compílalo una vez en una máquina
de desarrollo o en CI:

```bash
make binary   # backend/webkvm
make dist     # dist/webkvm-<versión>.tar.gz (binario + instalador + scripts + SHA256SUMS)
```

En el servidor de destino:

```bash
sudo WEBKVM_BINARY=backend/webkvm ./install.sh
# desde un tarball:
tar xzf webkvm-<versión>.tar.gz
sudo WEBKVM_BINARY=backend/webkvm bash packaging/standalone/install.sh
# servido por HTTPS con checksum obligatorio:
sudo WEBKVM_BINARY_URL=https://tu-servidor/webkvm \
     WEBKVM_BINARY_SHA256=<sha256> \
     ./install.sh
```

**Un único binario amd64 genérico funciona en todas las distribuciones
soportadas**: necesita GLIBC ≥ 2.34 y `libvirt.so.0`, presentes en Debian 13+,
Ubuntu 24+, Fedora 43/44 y Arch. El instalador añade los paquetes de libvirt
propios de cada distribución.

> Las actualizaciones in situ están soportadas: volver a ejecutar el instalador
> conserva todos los datos bajo `/opt/webkvm` y revierte automáticamente si algo
> falla.

### 4. Opciones y variables del instalador

Cada ajuste tiene una opción de línea de comandos y una variable de entorno
equivalente — son intercambiables, y una opción explícita gana sobre una
variable ya exportada. Ejecuta `install.sh --help` para ver la lista integrada.

| Opción | Variable equivalente |
|--------|----------------------|
| `--dry-run` | — (informa de lo que cambiaría sin tocar nada) |
| `--yes`, `-y`, `--unattended` | `WEBKVM_NONINTERACTIVE=1` |
| `--port=8080` | `WEBKVM_PORT` |
| `--bind=0.0.0.0` | `WEBKVM_BIND_ADDR` |
| `--https=yes\|no` | `WEBKVM_HTTPS` |
| `--domain=webkvm.example.com` | `WEBKVM_TLS_DOMAIN` |
| `--network=nat\|bridge\|both\|none` | `NETWORK_MODE` |
| `--incus=yes\|no` | `WEBKVM_INSTALL_INCUS` |
| `--prefix=/usr/local` | `WEBKVM_PREFIX` |
| `--data-dir=/opt/webkvm` | `WEBKVM_DATA_DIR` |
| `--admin-password=…` | `WEBKVM_ADMIN_PASSWORD` |
| `--bridge-dhcp` | `BRIDGE_DHCP=true` |
| `--bridge-static=10.0.0.5/24` | `BRIDGE_STATIC_IP` (implica `BRIDGE_DHCP=false`) |
| `--gateway=10.0.0.1` | `BRIDGE_STATIC_GW` |
| `--dns=1.1.1.1` | `BRIDGE_STATIC_DNS` |

| Variable | Qué hace |
|----------|----------|
| `WEBKVM_DATA_DIR` | Directorio de datos (por defecto `/opt/webkvm`). |
| `WEBKVM_PREFIX` | Prefijo de instalación (por defecto `/usr/local`). |
| `WEBKVM_BIND_ADDR` | Dirección de escucha (por defecto `0.0.0.0`). |
| `WEBKVM_PORT` | Puerto (por defecto `8080`). |
| `WEBKVM_BINARY` | Ruta a un binario local. |
| `WEBKVM_BINARY_URL` | URL HTTPS del binario (checksum obligatorio). |
| `WEBKVM_BINARY_SHA256` | Checksum SHA-256 de ese binario. |
| `WEBKVM_HTTPS=yes\|no` | `yes` = HTTPS nativo con certificado autofirmado (el SAN cubre IP + nombre de host [+ dominio]); `no` = HTTP plano. El valor por defecto de la pregunta interactiva es `yes`. |
| `WEBKVM_TLS_DOMAIN` | Dominio del certificado (p. ej. `webkvm.example.com`). Vacío = solo IP/nombre de host. Si se define, el SAN lo incluye y se intenta Let's Encrypt con repliegue automático a autofirmado. |
| `NETWORK_MODE` | `nat`, `bridge`, `both` o `none`. Valor interactivo por defecto **`bridge`**; las instalaciones desatendidas (`--yes` / `WEBKVM_NONINTERACTIVE=1`) usan **`both`**, porque ninguna de las dos mitades se puede añadir después sin volver a ejecutar el instalador. |
| `BRIDGE_DHCP`, `BRIDGE_STATIC_IP`, `BRIDGE_STATIC_GW`, `BRIDGE_STATIC_DNS` | Ajustes del bridge br0. El valor por defecto es **estático**: el instalador fija la dirección que el host tiene en ese momento. Define `BRIDGE_DHCP=true` (o pasa `--bridge-dhcp`) para dejar el bridge en DHCP. |
| `WEBKVM_NONINTERACTIVE=1` | No pregunta nada; aplica los valores por defecto. |
| `WEBKVM_INSTALL_INCUS=0\|1` | **Por defecto `1`.** Instala y activa el daemon **Incus** para contenedores (paquete nativo `incus` en apt/pacman/dnf, con repliegue a `lxd`; nunca snap; aviso no fatal si no hay paquete disponible) y fija `WEBKVM_INCUS_ENABLED=1` en la unidad. Ponlo a `0` (o pasa `--incus=no`) para un host solo de KVM. |
| `WEBKVM_ADMIN_PASSWORD` | Elige la contraseña inicial de administrador (si no, se genera una aleatoria y se guarda). |

### 5. HTTPS (sin proxy inverso)

El instalador pregunta directamente:

```
¿Cómo quieres acceder a WebKVM?
  1) Solo IP (HTTP plano)
  2) Con SSL — certificado autofirmado (vale para IP y para dominios, recomendado)
Dominio del certificado (opcional — p. ej. webkvm.example.com; vacío = solo IP/nombre de host;
el SAN incluye la IP de la LAN + hostname.local + localhost)
```

- **Solo IP (`WEBKVM_HTTPS=no`)** — HTTP plano en el puerto 8080.
- **Con SSL (por defecto)** — el backend sirve **HTTPS de forma nativa** con un
  certificado autofirmado RSA-2048 válido durante 10 años.
  SAN = `DNS:webkvm, DNS:localhost, DNS:<hostname>.local, IP:127.0.0.1, IP:<LAN>
  [+ DNS:<dominio>]`. Funciona por IP y por dominio sin nginx/apache/caddy.
- **Dominio público + Let's Encrypt** — cuando `WEBKVM_TLS_DOMAIN` resuelve al
  servidor y los puertos 80/443 son accesibles, autocert emite y renueva
  certificados reales (caché en `DATA_DIR/tls`). Si la validación falla, se
  repliega al certificado autofirmado incluyendo ese dominio — el servicio nunca
  se cae.

Los navegadores avisan de los certificados autofirmados. Descarga el tuyo desde
`https://IP:8080/api/system/cert` y confíalo en todo el sistema. Puedes cambiarlo
después en **Ajustes → Servidor → Certificado TLS / Dominio TLS** y luego
`systemctl restart webkvm`.

**¿Proxy inverso?** Solo si ya operas uno para WAF, límite de tasa o
autenticación centralizada. En ese caso instala con `WEBKVM_HTTPS=no` y apunta tu
vhost a `http://127.0.0.1:8080`.

### 6. Red de las máquinas virtuales

WebKVM usa **un único modelo: bridges Linux reales a nivel de sistema operativo**
(al estilo de Proxmox). Las redes virtuales de libvirt (`default`, `virbr0`, NAT,
macvtap/direct) ya no se crean, ni se listan, ni se usan. KVM se conecta con
`<interface type='bridge'><source bridge='vmbrX'/>` e Incus con
`nictype=bridged parent=vmbrX`.

Se elige con `NETWORK_MODE`:

- **Bridge L2 compartido (`bridge`, por defecto, recomendado)** — las máquinas
  virtuales y los contenedores salen a la LAN real a través de un bridge Linux
  físico (`vmbr0`/`br0`) y obtienen su IP del router por DHCP (o estática con
  `BRIDGE_STATIC_IP/CIDR` + puerta de enlace + DNS). Reutiliza un bridge
  existente en el host si lo hay; si no, crea uno (esclavo macvlan) sin tocar la
  dirección propia del host.
- **NAT aislada (`nat`, opcional)** — un bridge aislado (`vmbr1`) anclado a una
  interfaz `dummy0` del kernel, con `100.0.0.1/24` estática, DHCP de `dnsmasq` y
  `MASQUERADE`, de modo que sus inquilinos llegan a internet por el enlace de
  subida del host.
- **Ambas (`both`, opcional)** — los dos bridges disponibles; eliges por VM.

Desde la interfaz (**Redes**) puedes crear bridges adicionales en el host con IP
y DHCP opcionales, y reglas nftables de firewall por VM. El instalador lo
configura mediante `scripts/setup-network.sh`.

### 7. Contenedores (LXC) — híbrido KVM/LXC

WebKVM gestiona **contenedores LXC de forma nativa** mediante **Incus** (el fork
comunitario de LXD) en el mismo host, en una vista unificada (cada instancia
lleva una etiqueta `KVM`/`Incus`). El cliente Go de Incus mantiene la API REST de
LXD, así que **los daemons LXD existentes funcionan sin cambios**. El instalador
autónomo configura este módulo **por defecto**; pasa `--incus=no` (o
`WEBKVM_INSTALL_INCUS=0`) para un host solo de KVM. En una instalación manual el
módulo queda desactivado hasta que definas `WEBKVM_INCUS_ENABLED=1`.

**Dependencia del host.** El daemon Incus debe estar instalado y en marcha
(paquetes nativos, nada de snap):

```bash
# Debian / Ubuntu / Arch / Fedora / familia RedHat
sudo apt install incus    # o: sudo pacman -S incus / sudo dnf install incus
sudo systemctl enable --now incus
sudo incus admin init     # LXD antiguo: sudo lxd init
```

**Automatización del instalador.** El instalador autónomo se ejecuta con
`WEBKVM_INSTALL_INCUS=1` salvo que indiques lo contrario: instala y activa Incus
y conecta el módulo en la unidad de systemd. Usa el **paquete nativo `incus` en
todos los gestores de paquetes** (apt/pacman/dnf), con repliegue al paquete
nativo `lxd` cuando `incus` no está disponible; **nunca** usa snap. Si no hay
ningún paquete disponible, imprime un aviso pidiéndote que instales Incus o LXD a
mano según la wiki de tu distribución y **continúa solo con KVM (nunca falla)**:

```bash
# Host solo de KVM — omitir Incus por completo
sudo bash install-webkvm.sh --incus=no
```

**Activación (entorno).** Define `WEBKVM_INCUS_ENABLED=1` en la unidad de systemd
(`Environment=WEBKVM_INCUS_ENABLED=1`) o en `.env`. `INCUS_SOCKET` sobrescribe el
socket autodetectado (primero Incus: `/var/lib/incus/unix.socket` o
`/run/incus/*`; después el LXD antiguo por snap
`/var/snap/lxd/common/lxd/unix.socket` o por apt `/var/lib/lxd/unix.socket`).

**Permisos.** El usuario del proceso debe poder leer el socket del daemon. El
servicio nativo se ejecuta como `root` (no hace falta nada); para una ejecución
sin root o en Docker, añade el usuario al grupo **`incus-admin`** (Incus) o
**`lxd`** (LXD antiguo):
`sudo usermod -aG incus-admin <usuario> && sudo systemctl restart webkvm`.

**Verificación.** Busca `incus_connected` en el log del backend al arrancar; los
contenedores aparecen en la lista de VMs y se pueden crear desde el mismo
formulario que las VMs (selector de imágenes amigable, credenciales de
cloud-init, redimensionado del disco raíz, interfaces y métricas en vivo).

### 8. Instalación no interactiva (servidores y pipelines)

Todas las opciones se pueden sobrescribir por entorno; sin TTY se aplican los
valores por defecto:

```bash
sudo WEBKVM_PORT=8080 \
     WEBKVM_HTTPS=yes \
     WEBKVM_TLS_DOMAIN=webkvm.example.com \
     NETWORK_MODE=nat \
     WEBKVM_NONINTERACTIVE=1 \
     ./packaging/standalone/install.sh
```

La salida completa de la instalación queda en
**`/var/log/webkvm-install.log`**; si falla, se conserva el directorio de trabajo
bajo `/var/tmp` y se imprime su ruta.

### 9. Actualización

Vuelve a ejecutar el instalador con el código o el binario nuevo. El instalador:

1. Respalda el binario y el fichero de unidad actuales (`*.previous`).
2. Instala los nuevos.
3. Reinicia el servicio y comprueba `/api/health`.
4. **Revierte todo automáticamente si algún paso falla.**

Todo lo que hay bajo `/opt/webkvm` (configuración, discos, ISOs, usuarios) se
conserva.

### 10. Desinstalación

```bash
cd packaging/standalone
sudo ./uninstall.sh                                  # conserva los datos
sudo PURGE_DATA=1 PURGE_NETWORKS=1 ./uninstall.sh     # elimina también datos y redes/bridges
```

La desinstalación solo elimina lo que WebKVM posee: el binario, el servicio de
systemd y — de forma opcional — sus propios datos (`PURGE_DATA`) y las redes y
bridges de libvirt que creó (`PURGE_NETWORKS`). Deliberadamente **no** elimina
los paquetes de tiempo de ejecución (libvirt, qemu, swtpm, ovmf, dnsmasq, …):
son herramientas compartidas del sistema y de virtualización de las que puede
depender otro software del host, así que la desinstalación nunca las toca.

### 11. Resolución de problemas

| Síntoma | Causa probable / solución |
|---------|---------------------------|
| Falta `/dev/kvm` | Activa la virtualización en la BIOS/UEFI, o la anidada en el hipervisor |
| El instalador revierte | Falló la comprobación de salud; mira `journalctl -u webkvm -n 100` |
| Aviso de certificado en el navegador | Es lo esperado con autofirmado; confía el certificado desde `/api/system/cert` |
| El servicio no escucha | `systemctl status webkvm` y el journal |
| El dominio no obtiene certificado real | No es accesible públicamente; el repliegue a autofirmado es normal |
| El puerto ya está en uso | Falla rápido antes de instalar; libera el puerto o define `WEBKVM_PORT` |

El log de instalación está en `/var/log/webkvm-install.log`; una ejecución
fallida conserva su directorio de trabajo bajo `/var/tmp` (se imprime la ruta).

---

## Parte II — Documentación técnica

### 12. Arquitectura

WebKVM es una **aplicación monolítica en dos partes**:

- **Backend en Go** (`backend/`): API REST + consola noVNC + SPA de Svelte
  embebida, todo en el único binario `webkvm`.
- **Frontend en Svelte 5** (`frontend/`): SPA compilada a recursos estáticos,
  embebida con `go:embed`.

El backend habla con **libvirt** (`qemu:///system`) para gestionar dominios,
redes y almacenamiento. Todo el estado de la aplicación (usuarios, tokens,
ajustes, copias de seguridad, grupos, nodos) persiste como **ficheros JSON dentro
de `DATA_DIR`** — sin base de datos externa.

```
Navegador ──HTTP/SSE + Bearer JWT──► Backend Go (:8080)
                                      │
                                      ├─ API REST (chi v5)
                                      ├─ Auth JWT + RBAC (admin/operator/viewer)
                                      ├─ Hub de eventos SSE (estado de VM, métricas)
                                      ├─ Proxy noVNC (WebSocket → VNC), WebSocket serie
                                      └─ Runner de backups (cron) + import/export OVA
                                      │  API C de libvirt (libvirt.org/go/libvirt)
                                      ▼
                          libvirtd / qemu:///system
                          VMs · Redes · Pools de almacenamiento · Snapshots
                                      ▼
                              Hipervisor QEMU/KVM
```

Tres planos:

1. **Plano de control (backend Go)** — orquesta libvirt y expone la API.
2. **Plano de datos (libvirt/QEMU)** — el hipervisor real; aquí viven las VMs.
3. **Plano de estado (`DATA_DIR`)** — configuración y metadatos de la aplicación.

### 13. Estructura del repositorio

| Ruta | Contenido |
|------|-----------|
| `backend/` | Backend en Go (`cmd/server`, `cmd/cli`, `cmd/migrate-disk-names`, `internal/*`). |
| `frontend/` | SPA de Svelte 5 (Vite, Tailwind v4). |
| `docs/` | Esta guía y el manual de uso. |
| `packaging/standalone/` | Instalador para metal desnudo con rollback automático, desinstalador y tests estáticos. |
| `scripts/` | Envoltorio de un solo comando, configuración de red, unidad de systemd, logrotate y ayudante de backups. |
| `Makefile` | build / test / dist / clean. |
| `install.sh` | Punto de entrada unificado (delega en el instalador autónomo). |

### 14. Paquetes del backend (`backend/internal/`)

| Paquete | Responsabilidad |
|---------|-----------------|
| `api/` | Router HTTP (chi v5), handlers, middleware y consola noVNC embebida. |
| `libvirt/` | Integración con libvirt: dominios, almacenamiento, redes, OVA, snapshots, métricas, eventos y bridges del host. |
| `auth/` | JWT, middleware, RBAC, lista negra de tokens, límite de tasa en el login y tickets de consola. |
| `user/` | Almacén de usuarios (bcrypt) en `users.json`; siembra del administrador inicial. |
| `tokens/` | Tokens de API persistentes (`wvmb_…`, con hash sha256) en `api-tokens.json`. |
| `configstore/` | Ajustes tipados y recargables en caliente en `config.json`. |
| `config/` | Configuración de arranque (entorno + `.env`) y resolución del secreto JWT. |
| `backupstore/` | Backup v2: destinos, programaciones, trabajos, productor tar, soporte SFTP y restauración. |
| `audit/` | Log de auditoría en JSONL (`audit.log`, rotación a 10 MB). |
| `events/` | Hub de difusión SSE (estado de VM, métricas). |
| `nodes/` | Registro de nodos libvirt (`nodes.json`). |
| `models/` | Tipos compartidos (VM, pools, redes, usuarios, RBAC, cuotas, métricas…). |
| `appliances/` | Catálogo comunitario de appliances, valores por defecto y scripts de aprovisionamiento. |
| `cloudinit/` | Generación de la ISO semilla NoCloud (validada, con YAML escapado, vía xorriso). |
| `notify/` | Notificaciones por webhook y SMTP. |
| `vmsched/` | Planificación de encendido y apagado de VMs por cron. |
| `firewall/` | Reglas nftables por VM con aplicación atómica. |
| `frontend/` | Recursos compilados de Svelte embebidos (`go:embed`). |
| `logging/` | `log/slog` estructurado (JSON) con copia opcional a fichero. |

### 15. Variables de entorno

| Variable | Por defecto | Notas |
|----------|-------------|-------|
| `PORT` | `8080` | Puerto HTTP/HTTPS del backend. |
| `BIND_ADDR` | `127.0.0.1` → `0.0.0.0` en instalaciones nuevas | Persiste como `server.bind_addr` en `config.json`. |
| `LIBVIRT_URI` | `qemu:///system` | URI de libvirt. |
| `DATA_DIR` | `/opt/webkvm` | Directorio de datos persistentes. |
| `JWT_SECRET` | autogenerado | Se guarda en `{DATA_DIR}/jwt.key` (0600); los secretos débiles se rechazan. |
| `WEBKVM_LOG_FILE` | `""` | Copia del log a fichero (la lee `GET /api/system/logs`). |
| `VNC_PROXY_HOST` | `127.0.0.1` | Host destino de las conexiones TCP noVNC→VNC. |
| `PUBLIC_HOST` | `""` | IP incrustada en los ficheros `.rdp`/`.vv` (se autodetecta si está vacía). |
| `CORS_ORIGIN` | `*` | Orígenes CORS permitidos (separados por comas). |
| `TLS_CERT` / `TLS_KEY` / `TLS_DOMAIN` | — | Ajustes TLS (persisten como `server.tls_*`). |
| `WEBKVM_ADMIN_PASSWORD` | — | Contraseña inicial de administrador (si no, se genera aleatoria). |
| `WEBKVM_INCUS_ENABLED` | `0` | Activa el módulo de contenedores Incus/LXD (requiere el daemon en el host). |
| `INCUS_SOCKET` | automático | Sobrescribe el socket unix del daemon de contenedores (primero Incus: `/var/lib/incus/unix.socket`, `/run/incus/*`; luego las rutas de LXD por snap y por apt). |

El fichero `.env` del directorio de trabajo se carga como respaldo (godotenv);
las variables de entorno reales siempre ganan.

### 16. Estructura en disco (`DATA_DIR`)

| Ruta | Contenido |
|------|-----------|
| `pools/webkvm-disks` | Pool de discos de VM (pool de libvirt `webkvm-disks`). |
| `pools/webkvm-isos` | Pool de la biblioteca de ISOs (las instalaciones antiguas que usaban `ISOS` se renombran solas al primer arranque). |
| `pool-purposes.json` | Propósito de cada pool (`disk`/`iso`). |
| `users.json` | Usuarios con hashes bcrypt (0600). |
| `jwt.key` | Secreto JWT (0600). |
| `api-tokens.json` | Hashes sha256 de los tokens de API. |
| `config.json` | Ajustes persistidos (esquema tipado). |
| `backup/{targets,schedules,jobs}.json` | Registro de Backup v2. |
| `nodes.json` | Nodos de libvirt. |
| `groups.json` | Grupos y etiquetas de VM. |
| `audit.log` | Log de auditoría en JSONL. |
| `cifs-secrets.json` | UUIDs de secretos CIFS para pools netfs (0600). |
| `covers/` | Imágenes de portada de las VMs. |
| `logs/backend.log` | Log estructurado (cuando `WEBKVM_LOG_FILE` está activo). |
| `admin-password.initial` | Contraseña inicial de administrador (solo en el primer arranque). |
| `certs/` | Certificados TLS (autofirmados). |
| `appliances.json` | Catálogo de appliances (sembrado desde los valores por defecto en el primer arranque). |
| `tls/` | Caché de autocert (Let's Encrypt). |

### 17. Autenticación y seguridad

- **JWT HS256** (TTL de 24 h por defecto, recargable en caliente). El servidor
  **se niega a arrancar** con secretos de ejemplo o débiles; genera uno aleatorio
  y lo persiste en `jwt.key`.
- **Límite de tasa en el login**: 5 fallos / 15 min → bloqueo de 15 minutos
  (429 + `Retry-After`); el loopback siempre es de confianza;
  `WEBKVM_TRUSTED_RATELIMIT_CIDRS` añade CIDRs de confianza;
  `WEBKVM_TRUST_PROXY` controla `X-Forwarded-For`.
- **RBAC**: jerarquía fija `admin > operator > viewer`.
- **Tokens de API**: prefijo `wvmb_` + 32 bytes aleatorios; solo se almacena el
  hash sha256; caducan y son revocables.
- **`must_change_password`** bloquea todos los endpoints salvo
  auth/password/health hasta que se rota la contraseña inicial.
- **Lista de bloqueo SSRF** en las descargas de ISO (loopback, link-local,
  rangos privados, CGNAT).
- **Path traversal**: saneamiento en la subida y el renombrado de ISOs; rutas
  validadas en los backups (`backupstore/path_safety.go`, lista de denegación
  consciente de enlaces simbólicos).
- **Firewall**: tabla nftables dedicada, validación atómica (`nft -c`), reglas
  aplicadas con exec de argumentos separados (sin shell).
- **Endurecimiento de systemd**: `NoNewPrivileges`, `ProtectSystem=full`,
  `ProtectHome=read-only`, `PrivateTmp`, `CapabilityBoundingSet` restringido y
  `ReadWritePaths` acotado.
- **Análisis de código**: golangci-lint + eslint + prettier obligatorios en CI.

### 18. API REST (resumen)

URL base: `http(s)://<host>:8080/api`. Autenticación:
`Authorization: Bearer <JWT o token de API>`.

| Área | Endpoints principales |
|------|------------------------|
| Auth | `POST /auth/login`, `POST /auth/logout`, `GET /auth/me`, `PUT /users/me/password` |
| Usuarios/grupos | `GET/POST/PUT/DELETE /users/{username}`, `GET/POST /groups`, `GET /accounts` |
| VMs | `GET/POST /vms`, `GET/PUT/DELETE /vms/{id}`, acciones de encendido, clone/import/import-OVA/export, `POST/DELETE /vms/{id}/usb` (passthrough USB, solo administradores) |
| Consola | WebSocket serie (+ tickets de un solo uso), noVNC (`POST /vms/{id}/vnc-ticket` + ticket reutilizable con alcance de VM), descarga `.rdp`/`.vv`, portapapeles |
| Almacenamiento | CRUD de pools/volúmenes/ISOs, subida y descarga de ISO, `POST /storage/upload-disk` (qcow2/img/raw/qed) |
| Redes | CRUD de bridges y redes, conmutador VLAN-aware |
| Snapshots/backups | CRUD de snapshots + revert; destinos, ejecuciones, trabajos y programaciones de backup |
| Appliances | CRUD del catálogo, despliegue (trabajo en segundo plano), scripts de aprovisionamiento |
| Sistema | `GET /health`, `/status`, `/metrics`, `/logs`, `GET /events` (SSE), `/system/cert`, `/system/version` |

El frontend usa un único cliente de API
(`frontend/src/lib/stores/auth.svelte.js`); las rutas se registran en
`backend/internal/api/router.go`.

### 19. Pools netfs con autenticación CIFS

Los pools `netfs` con formato `cifs` admiten autenticación mediante **secretos de
libvirt**: el usuario y la contraseña se envían juntos (400 si van incompletos),
la contraseña se almacena únicamente como UUID de secreto de libvirt en
`{DATA_DIR}/cifs-secrets.json` (0600) — la API nunca la devuelve. Rota las
credenciales con `PUT /api/storage/pools/{name}` (el pool debe estar detenido).
Tras reinstalar libvirtd (que borra los secretos), recupera el pool enviando
`cifs-needs-reauth: true` junto con las credenciales actuales.

### 20. Compilar desde el código fuente

```bash
make build          # npm ci + build del frontend → go:embed → go build -o backend/webkvm
make test           # go test ./... && go vet ./... ; eslint + prettier en el frontend
make dist           # dist/webkvm-<versión>.tar.gz (binario + instalador + SHA256SUMS)
```

La versión se estampa con `git describe --tags --always` o `WEBKVM_VERSION`
(ldflags), con `dev` como respaldo.

### 21. Invariantes que no se deben romper

> Cambiar esto rompe la compatibilidad con las instalaciones y las VMs
> existentes.

| Invariante | Valor |
|------------|-------|
| Espacio de nombres XML de los metadatos del dominio | `https://webvm.local/ns` |
| Nombre del pool de discos | `webkvm-disks` |
| Nombre del pool de ISOs | `webkvm-isos` |
| Módulo de Go | `webkvm` |
| Servicio de systemd | `webkvm.service` |
| Patrón del nombre de fichero de backup | `webkvm-<host>-<ts>` |

### 22. Seguridad y licencia

- Informes de vulnerabilidades: [SECURITY.es.md](../SECURITY.es.md).
- Licencia **AGPLv3**: [LICENSE](../LICENSE).
