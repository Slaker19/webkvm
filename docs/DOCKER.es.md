# Ejecutar WebKVM en Docker

WebKVM es, ante todo, una aplicación **nativa** (ver
[INSTALLATION.es.md](INSTALLATION.es.md)); la imagen de contenedor es un
empaquetado *alternativo* exactamente del mismo binario, para quien prefiera
gestionarlo con Docker/Compose. **No** es un producto distinto ni más ligero: el
objetivo es una paridad 1:1 con la instalación nativa, con una única excepción
inevitable (la autoactualización, ver más abajo).

[**English**](DOCKER.md) • [**Español**](DOCKER.es.md)

## Cómo funciona

El contenedor **no** incluye libvirtd, QEMU ni Incus. Se conecta al libvirtd (y,
para los **contenedores LXC**, al demonio de Incus/LXD) que **ya se está
ejecutando en el host**, igual que hace el binario nativo — las VMs y los
contenedores los gestionan los demonios del host, no algo que corra dentro del
contenedor. Eso implica:

- **Requisito previo:** el host debe tener ya libvirt/QEMU instalados y en
  marcha. Si aún no los tiene, ejecuta `install.sh` una vez (o simplemente
  `make install-deps`) para prepararlo — el modo Docker no instala libvirt/QEMU
  por ti. Para los contenedores, monta el socket de Incus del host (ver la tabla
  de montajes más abajo) y activa el módulo con `WEBKVM_INCUS_ENABLED=1`.
- El contenedor solo necesita las herramientas *de cliente* a las que llama el
  binario de webkvm (`qemu-img`, `virsh`, `ip`, `nft`, `xorriso`, `openssl`,
  `tar`, …), nunca `/dev/kvm` ni un servidor QEMU/libvirt.
- `--network host --privileged` es necesario para tener toda la funcionalidad
  (puente de las VMs a la LAN real, reglas nftables por VM) — el mismo nivel de
  confianza que ya tiene la instalación nativa, cuya unidad de systemd también
  se ejecuta como root en el host. No existe un modo «reducido».

## Inicio rápido (automatizado)

Una sola línea, desde un servidor recién instalado — instala libvirt/QEMU y
Docker si faltan, y después despliega la imagen publicada:

```bash
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-docker.sh -o /tmp/webkvm-docker-install.sh && sudo bash /tmp/webkvm-docker-install.sh
```

O bien, desde una copia de este repositorio:

```bash
sudo ./docker-install.sh              # descarga la imagen publicada
sudo ./docker-install.sh --source     # construye la imagen en local
sudo ./docker-install.sh --dry-run    # solo vista previa, sin cambios
```

Esto verifica e instala el mismo conjunto libvirt/QEMU/KVM que prepara
`install.sh` para una instalación nativa (ver **Paquetes del host** más abajo —
el resto lo trae el propio contenedor), verifica e instala Docker Engine y el
plugin de compose, y después ejecuta `docker compose up -d` con todos los
privilegios y montajes de `docker-compose.yml`. Los requisitos previos son
idempotentes: se puede repetir sin riesgo.

## Inicio rápido (manual, con la imagen publicada)

```bash
# 1) Edita docker-compose.yml si tus rutas no son las predeterminadas, y luego:
docker compose up -d
```

`docker-compose.yml` apunta por defecto a `slaker1908/webkvm:latest`, así que
`docker compose up -d` simplemente la descarga — no hace falta compilar nada en
local.

¿Prefieres construirla desde el código fuente (por ejemplo, para probar un
cambio local)?

```bash
make docker-build   # compila el binario y la imagen en local, con la etiqueta webkvm:latest
docker compose build
docker compose up -d
```

Abre `https://<ip-del-host>:8080` — en el primer arranque se generan un
certificado autofirmado y una contraseña inicial de administrador, exactamente
igual que en una instalación nativa nueva. (Si `DATA_DIR` ya contiene una
instalación nativa previa, el fichero de «contraseña de admin» que se imprime
puede ser un resto obsoleto del primer arranque de *aquella* instalación, no la
contraseña actual — inicia sesión con tus credenciales existentes.)

## Paquetes del host (requisitos — instálalos ANTES que Docker)

El modo Docker no instala libvirt/QEMU por ti: el contenedor es un cliente del
libvirtd que ya se ejecuta en el host, así que ese conjunto debe estar ya
instalado y en marcha allí, exactamente como lo prepara `install.sh` para una
instalación nativa. `docker-install.sh` los instala por ti; si prefieres hacerlo
a mano:

| Familia de distribución | Comando |
|---|---|
| Debian/Ubuntu (`apt`) | `sudo apt-get install libvirt-daemon-system libvirt-clients libvirt-daemon-driver-qemu qemu-system-x86 qemu-utils ovmf swtpm swtpm-tools virtinst bridge-utils dnsmasq-base` |
| Fedora/RHEL (`dnf`) | `sudo dnf install libvirt-daemon libvirt-daemon-driver-qemu libvirt-daemon-driver-storage libvirt-daemon-driver-network libvirt-daemon-driver-interface libvirt-daemon-driver-nodedev libvirt-daemon-driver-nwfilter libvirt-daemon-driver-secret libvirt-daemon-config-network libvirt-client qemu-kvm-core qemu-img edk2-ovmf swtpm-tools virt-install bridge-utils dnsmasq` |
| Arch (`pacman`) | `sudo pacman -S libvirt qemu-full qemu-img swtpm edk2-ovmf virt-install dnsmasq` |

Después activa el demonio y el propio Docker:

```bash
sudo systemctl enable --now libvirtd   # o virtqemud en libvirt reciente (9.7+)
sudo systemctl enable --now docker
```

Docker Engine y el plugin de compose, si no están ya instalados:

| Familia de distribución | Comando |
|---|---|
| Debian/Ubuntu | `sudo apt-get install docker.io docker-compose-v2` |
| Fedora/RHEL | `sudo dnf install moby-engine docker-compose-plugin` (o añade el repositorio propio de Docker: https://docs.docker.com/engine/install/fedora/) |
| Arch | `sudo pacman -S docker docker-compose` |

Fíjate en lo que **no** aparece en estas listas: `xorriso`, `openssl`, `python3`,
`nftables`, las herramientas CLI de `qemu-utils` para uso *del propio
contenedor*, etc. — todo eso vive dentro de la imagen (ver el Dockerfile), no en
el host. El contenedor también habla con las nftables del host a través de
`--network host`, que comparte directamente su espacio de nombres de red: para
eso tampoco hace falta ningún paquete adicional en el host.

## `docker run` manual (sin compose)

Equivalente a `docker-compose.yml`, para quien prefiera un único comando a
Compose (pon un `DATA_DIR` real si no es `/opt/webkvm`):

```bash
docker run -d \
  --name webkvm \
  --restart unless-stopped \
  --network host \
  --privileged \
  -e DATA_DIR=/opt/webkvm \
  -e LIBVIRT_URI=qemu:///system \
  -e BIND_ADDR=0.0.0.0 \
  -e PORT=8080 \
  -v /opt/webkvm:/opt/webkvm \
  -v /var/run/libvirt:/var/run/libvirt \
  -v /run/systemd:/run/systemd \
  -v /var/log/journal:/var/log/journal:ro \
  -v /etc/passwd:/etc/passwd:ro \
  -v /etc/shadow:/etc/shadow:ro \
  -v /etc/group:/etc/group:ro \
  -v /etc/pam.d:/etc/pam.d:ro \
  slaker1908/webkvm:latest
```

Consulta **Montajes — qué y por qué** más abajo para entender cada `-v`/`-e`.
Después se gestiona con `docker logs -f webkvm`, `docker restart webkvm`,
`docker rm -f webkvm`.

## Montajes — qué y por qué

| Ruta del host | Ruta del contenedor | Por qué |
|---|---|---|
| `/opt/webkvm` | `/opt/webkvm` | `DATA_DIR`: usuarios, ajustes, certificados, pools de discos e ISOs. Se monta en la **misma ruta absoluta** en ambos lados — libvirt guarda la ubicación de un pool como una ruta de directorio, así que si el contenedor la viera en una ruta distinta a la que ve el libvirtd del host, el pool apuntaría a un directorio vacío o equivocado. |
| `/var/run/libvirt` | `/var/run/libvirt` | El socket de libvirt del host, para que `LIBVIRT_URI=qemu:///system` llegue al libvirtd del host — idéntico a como habla con él el binario nativo. |
| `/run/systemd` | `/run/systemd` | Permite que el `systemctl` del contenedor actúe sobre el systemd real del host (se usa para `journalctl` y para reiniciar el servicio). |
| `/var/log/journal` (solo lectura) | `/var/log/journal` | Para que `journalctl` pueda leer los logs reales de los servicios del host. |
| `/etc/passwd`, `/etc/shadow`, `/etc/group`, `/etc/pam.d` (solo lectura) | las mismas rutas | La función de **terminal del host** autentica cuentas reales del host vía PAM. Solo se comparten los ficheros de *datos*: el contenedor usa su **propio** binario `/bin/login` (misma imagen base Debian), no el del host, para evitar un desajuste de versiones de glibc entre ambos sistemas de ficheros. El módulo `pam_unix` de PAM solo lee esos ficheros de datos, así que la autenticación sigue comprobando las contraseñas reales del host. |
| `/var/lib/incus/unix.socket` *(contenedores)* | `/var/lib/incus/unix.socket` | El **socket de Incus** del host, para que el módulo de contenedores (`WEBKVM_INCUS_ENABLED=1`) llegue al demonio de Incus del host — idéntico a como habla con él el binario nativo. Para un demonio LXD antiguo, monta su socket (`/var/snap/lxd/common/lxd/unix.socket` en snap o `/var/lib/lxd/unix.socket` en apt). El proceso del contenedor debe poder leer el socket (se ejecuta como root dentro del contenedor, acorde al modelo de confianza del host). |

Compartir los ficheros de autenticación y el socket de systemd del host, junto
con `--privileged`, da al contenedor **confianza equivalente a root sobre el
host** — no es una regresión nueva, es la misma confianza que ya tiene la
instalación nativa (su unidad de systemd se ejecuta con `User=root`). No expongas
este contenedor a operadores en los que no confíes sin entender esto.

## Variables de entorno

| Variable | Por defecto | Significado |
|---|---|---|
| `DATA_DIR` | `/opt/webkvm` | Dónde viven el estado, los certificados y los pools. Mantenlo sincronizado con el montaje de arriba. |
| `LIBVIRT_URI` | `qemu:///system` | URI de conexión de libvirt (habla con el host por el socket montado). |
| `BIND_ADDR` | `0.0.0.0` | Dirección de escucha dentro del contenedor (con `network_mode: host` es la propia interfaz del host). |
| `PORT` | `8080` | Puerto de escucha. |

## La única excepción deliberada: la autoactualización

El botón de actualización de la instalación nativa reemplaza el binario del
host y reinicia la unidad de systemd — con la release verificada de GitHub o,
en un checkout, recompilando desde el código fuente. Ninguna de las dos cosas
tiene sentido en un contenedor: un contenedor en marcha no se recompila, se
reemplaza. **No uses el flujo de actualización de la aplicación en modo
Docker.** En su lugar, actualiza descargando la imagen nueva y recreando el
contenedor:

```bash
docker compose pull
docker compose up -d
```

(Si compilas desde el código fuente en vez de usar la imagen publicada, el
equivalente es `git pull && make docker-build && docker compose up -d`.)

Todo lo demás — terminal del host, logs, reinicio del servicio, red de las VMs,
firewall — está diseñado para comportarse exactamente igual que en la
instalación nativa.

## Notas

- En un host dado, ejecuta **o** la instalación nativa **o** el contenedor, no
  ambos a la vez: se pelearían por el mismo puerto y por los mismos pools de
  discos e ISOs.
- **Nunca apuntes una segunda instancia de webkvm (Docker o nativa) al mismo
  libvirtd con un `DATA_DIR` distinto, ni siquiera «solo para probar».** Los
  pools de almacenamiento de libvirt se identifican **por nombre** en la
  conexión, no por `DATA_DIR`: si la segunda instancia ya tiene pools con los
  mismos nombres (por ejemplo, `webkvm-disks` o `webkvm-isos`), **redefinirá en
  silencio su ruta de destino** hacia su propio `DATA_DIR/pools/...`, rompiendo
  la vista que la primera instancia tiene de su propio almacenamiento (los
  ficheros subyacentes no se tocan, pero sí el objeto de pool que apunta a
  ellos). Si necesitas probar el modo Docker, hazlo contra un libvirtd que
  todavía no tenga pools, o renombra antes los pools de prueba.
- La imagen se construye para la misma arquitectura de destino que el binario
  (`amd64`); constrúyela en (o para) el host donde vayas a ejecutarla.
- Compila el binario en una distribución cuya libvirt **no sea más nueva** que la
  de la imagen (Ubuntu 24.04, la misma que usa CI): el binario enlaza libvirt de
  forma dinámica vía CGO, y un binario compilado contra libvirt 12 (por ejemplo,
  Debian 13 o Ubuntu 26.04) falla dentro de la imagen con `LIBVIRT_11.x not
  found` — ni siquiera puede imprimir su versión. Ante la duda, compila dentro de
  una cadena de herramientas 24.04.
