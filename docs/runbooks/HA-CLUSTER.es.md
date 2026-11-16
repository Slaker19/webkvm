# Runbook del clúster HA

Guía del operador para la función opcional Cluster/HA de WebKVM: instalar el
stack, crear el clúster, fencing, acceso SSH gestionado a otros nodos, abrir y
editar VMs desde cualquier nodo, y HA por carga (KVM e Incus).

Etiquetas de evidencia usadas en este documento:

- **medido** — observado en un entorno de pruebas privado; no es una cifra de capacidad de producción.
- **solo simulado** — cubierto por tests unitarios, mocks o una copia temporal
  del CIB.
- **no probado en vivo** — implementado y revisado, nunca ejecutado en un nodo
  real.

Guía complementaria: [GUIDED-VALIDATION.es.md](GUIDED-VALIDATION.es.md)
(checklist paso a paso de la UI para el operador). Versión en inglés:
[HA-CLUSTER.md](HA-CLUSTER.md).

## Contenido

1. [Elige tu camino](#1-elige-tu-camino)
2. [Distribuciones soportadas](#2-distribuciones-soportadas)
3. [Instalar las dependencias](#3-instalar-las-dependencias)
4. [Crear el clúster desde el panel](#4-crear-el-clúster-desde-el-panel)
5. [Fencing](#5-fencing)
6. [Acceso SSH gestionado a nodos KVM remotos](#6-acceso-ssh-gestionado-a-nodos-kvm-remotos)
7. [Abrir y editar VMs e Incus desde cualquier nodo](#7-abrir-y-editar-vms-e-incus-desde-cualquier-nodo)
8. [HA por carga (KVM e Incus)](#8-ha-por-carga-kvm-e-incus)
9. [Resultados de validación](#9-resultados-de-validación)
10. [Límites conocidos y qué no está validado en vivo](#10-límites-conocidos-y-qué-no-está-validado-en-vivo)
11. [Resolución de problemas](#11-resolución-de-problemas)
12. [Topología de referencia y operación diaria](#12-topología-de-referencia-y-operación-diaria)

## 1. Elige tu camino

| | FÁCIL (asistente de la UI) | AVANZADO (comandos manuales) |
|---|---|---|
| Dependencias | Página Nodos, tarjeta "Dependencias de Cluster y HA", botón "Instalar dependencias de cluster/HA" | `webkvm-cli ha deps ...` o `webkvm-ha-stack` |
| Clúster | Página Nodos, asistente "Crear el clúster" (modo Fácil) | `pcs host auth`, `pcs cluster setup`, `pcs stonith sbd enable` a mano |
| SSH a otros nodos | Tarjeta "Acceso del clúster (SSH gestionado)" | `ssh-keygen` / `authorized_keys` a mano y registrar el nodo |
| HA por carga | Interruptor "Alta disponibilidad" en una VM o contenedor, panel "Cargas HA" | `pcs` sobre el CIB (ver sección 12) |

La tarjeta y el asistente tienen también modo Avanzado (lista de paquetes,
capas opcionales, clústeres de dos nodos, fencing adicional). Elige un camino
por clúster; no mezcles recursos creados por la UI (`ha-kvm-*`, `ha-incus-*`)
con otros escritos a mano para la misma carga.

Qué necesita cada nodo antes de empezar:

1. La misma distribución y versión en todos los nodos (sección 2).
2. WebKVM instalado en todos, con `modules.cluster_ha` apagado hasta instalar
   los paquetes.
3. Un hostname resoluble y una red privada de clúster entre los nodos.
4. Un dispositivo watchdog (`ls /dev/watchdog*`): hardware, `i6300esb` en una
   VM, o `softdog` solo para pruebas (sección 5).
5. Para cargas Incus: un clúster Incus con un pool `linstor` o `ceph`.
6. Para nodos KVM remotos: una cuenta SSH en cada nodo con acceso a libvirt.
7. Root o `sudo` en cada nodo. Ni el asistente ni el instalador piden nunca la
   contraseña de `hacluster`.

## 2. Distribuciones soportadas

| Distribución | Nivel | Qué hace WebKVM |
|---|---|---|
| Ubuntu 22.04 o superior | Soportada | Instalación automática desde los repositorios de la distro |
| Debian 12 o superior | Soportada | Instalación automática desde los repositorios de la distro |
| Derivadas de Debian/Ubuntu | Experimental | Igual, pero debes aceptar el nivel experimental |
| Fedora | Experimental | `dnf install` tras tu confirmación. Fedora 44 usa DNF5: las llamadas del instalador se revisaron para DNF5 pero **no se ejecutaron en una Fedora real** (no probado en vivo) |
| Derivadas de RHEL (RHEL, Rocky, Alma, CentOS, Oracle) | Experimental | Igual que Fedora, pero debes habilitar a mano el repositorio High Availability (WebKVM nunca habilita repositorios) |
| Arch, Manjaro y derivadas | Guiada | Ningún paquete del stack está en core/extra (comprobado con `pacman -Si` en una VM Arch real): todo viene de AUR y lo instalas tú como usuario normal |

Reglas que valen en todas:

- Nunca mezcles distribuciones en un clúster: una distro y una versión de Incus
  por clúster. La tabla de miembros avisa de distribuciones, versiones de
  distribución y versiones de Incus distintas.
- DRBD 9 / LINSTOR: solo en apt (Ubuntu/Debian, desde LINBIT) o guiado. El
  repositorio de LINBIT debe estar ya configurado por ti; WebKVM nunca añade
  repositorios de terceros ni canaliza un script a una shell. En
  Fedora/derivadas de RHEL LINSTOR es manual. En Arch `drbd-dkms` es un
  paquete AUR y LINSTOR no está empaquetado.
- Arch: WebKVM nunca ejecuta ayudantes AUR ni `makepkg`, nunca compila como
  root y nunca ejecuta un `pacman -Sy` suelto. Si faltan las bases de datos de
  paquetes te pide que ejecutes tú `sudo pacman -Syu`.
- Derivadas de RHEL: se muestra el comando del repositorio, por ejemplo
  `sudo dnf config-manager --set-enabled highavailability` (requiere
  `dnf-plugins-core`; el id puede variar según la versión:
  `dnf repolist --all`). RHEL usa `subscription-manager repos --enable` y
  necesita una suscripción con High Availability.

Verificado en nodos reales: Ubuntu 26.04 (`ha deps check` informó
`apt`/`supported`, watchdog `i6300esb`) y Arch (`ha deps check` informó
`pacman`/`partial`, seis paquetes AUR; no se instaló nada). Debian 13 no se
verificó en vivo.

## 3. Instalar las dependencias

La instalación base de WebKVM no incluye clustering. Instala el stack en
**todos** los miembros, un nodo cada vez. `webkvm-ha-stack` es una de tres
entradas equivalentes, no el único instalador.

### 3.1 Desde la UI

1. Abre la página Nodos, tarjeta "Dependencias de Cluster y HA".
2. Modo Fácil: pulsa "Instalar dependencias de cluster/HA", lee el plan y
   confirma. Modo Avanzado: elige las capas opcionales (DRBD/LINSTOR, agentes
   de fencing de energía), revisa cada paquete y copia los comandos exactos.
3. En Fedora/derivadas de RHEL marca el reconocimiento de nivel experimental.
   En Arch no hay botón de instalar: ejecuta los comandos mostrados y pulsa
   "Verificar de nuevo".
4. Repite en cada nodo (el bloque "Instalar en cada nodo" enlaza a la web de
   cada uno) y pulsa "Activar módulo" en cada uno.

### 3.2 Desde la CLI

```bash
webkvm-cli ha deps check                         # solo lectura, sin root
sudo webkvm-cli ha deps install                  # muestra el plan y pide confirmar
sudo webkvm-cli ha deps install --yes            # sin terminal: --yes es obligatorio
sudo webkvm-cli ha deps install --storage        # añade DRBD/LINSTOR donde estén empaquetados
sudo webkvm-cli ha deps install --accept-experimental --yes   # Fedora, derivadas de RHEL, Arch
webkvm-cli ha deps check --json                  # para automatización
```

Códigos de salida (compartidos con `webkvm-ha-stack`):

| Código | Significado |
|---|---|
| 0 | OK |
| 1 | `check` encontró componentes que faltan |
| 2 | Uso o privilegios (por ejemplo `install` sin root o sin `--yes`) |
| 10 | Distribución, nivel o repositorio no instalable tal cual (no soportada, experimental no aceptada, falta el repositorio HA, faltan las bases de datos de pacman) |
| 11 | Fallo del gestor de paquetes o del preflight (la limpieza de unidades ya se ejecutó) |
| 12 | Hay unidades del clúster en marcha que requieren tu acción |
| 13 | La parte de WebKVM está hecha; quedan pasos manuales (por ejemplo los paquetes AUR) |

### 3.3 Con el script `webkvm-ha-stack`

```bash
sudo webkvm-ha-stack --check       # lo instala el instalador de WebKVM; solo lectura
sudo webkvm-ha-stack --install --yes
sudo webkvm-ha-stack --doctor      # servicios, DRBD, quórum, puertos, watchdog, errores del kernel
webkvm-ha-stack --json --check
```

Desde un checkout o el tarball de release usa `scripts/install-ha-stack.sh`
con los mismos flags. Opciones extra: `--accept-experimental`, `--with-incus`
y `--with-drbd-dkms` (solo apt), `--load-drbd`. Incus y DKMS ya no se instalan
por defecto.

### 3.4 Qué hace y qué nunca hace el instalador

La instalación es fail-closed y no enmascara unidades de systemd:

1. Instantánea: registra si `corosync`, `pacemaker`, `pcsd` y `sbd` (y las
   unidades de LINSTOR con `--storage`) están habilitadas/activas.
2. Instalación: instala los paquetes; un fallo es un error, nunca se ignora.
3. Restauración: en cualquier salida deshabilita y detiene SOLO las unidades
   que estaban definitivamente deshabilitadas e inactivas antes. Las que
   estaban habilitadas o activas, o cuyo estado no se pudo leer, no se tocan.
4. Verificación: vuelve a leer el estado de las unidades.

Si una unidad que no estaba en marcha sigue en marcha, el resultado la lista
en `units_active` con el comando exacto para detenerla. Las unidades cuyo
estado previo no se pudo leer van en `unknown_units` y se dejan como están:
revísalas tú con `systemctl status`.

Nunca forma un clúster, formatea discos, escribe configuración de SBD/fencing
ni arranca Pacemaker. Estado: la lógica del instalador se ejercita en tests
con un ejecutor de comandos simulado; **no se ejecutó de verdad en un nodo
vivo** durante la validación (no probado en vivo). Ruta de instalación en
Arch: solo se ejecutó `check` en una VM real.

## 4. Crear el clúster desde el panel

Estado: el asistente **no está probado en vivo**. Sus comprobaciones del plan y
las sondas de host/paquetes tienen tests unitarios; los comandos `pcs` de la
creación no se ejecutaron de extremo a extremo en un clúster real. Revisa el
plan antes de confirmar.

### 4.1 El asistente de 4 pasos

1. **Nodos y red**: cada miembro con la IP que usan los demás. El nodo local y
   los nodos registrados vienen rellenados. Se recomiendan tres nodos; dos solo
   se permiten explícitamente en modo Avanzado.
2. **Fencing y SBD**: tipo de watchdog y timeout de SBD (5 a 120 s).
3. **Revisar el plan**: bloqueos, avisos y los comandos exactos.
4. **Crear el clúster**: confirma marcando la casilla y escribiendo el nombre
   del clúster.

Comandos que ejecuta el asistente (se muestran en el paso 3, como líneas
`sudo`):

```bash
sudo pcs cluster setup CLUSTER node1 addr=IP1 node2 addr=IP2 node3 addr=IP3
sudo pcs stonith sbd enable watchdog=/dev/watchdog SBD_WATCHDOG_TIMEOUT=5
sudo pcs cluster start --all
sudo pcs cluster enable --all            # solo con "Iniciar los servicios del clúster en el arranque"
sudo pcs property set stonith-enabled=true stonith-watchdog-timeout=10s no-quorum-policy=stop
```

El dispositivo watchdog y el timeout salen de tus elecciones (el timeout de
arriba es un ejemplo). El fichero de SBD lo editas tú en cada nodo
(`/etc/default/sbd` o `/etc/sysconfig/sbd`: `SBD_WATCHDOG_DEV`,
`SBD_WATCHDOG_TIMEOUT`, `SBD_PACEMAKER=yes`).

### 4.2 Por qué `pcs host auth` y la contraseña de hacluster son manuales

Una contraseña en la línea de comandos aparece en la lista de procesos. El
asistente nunca la pide: te muestra los pasos y después comprueba el fichero de
known-hosts de `pcsd` (bloqueo `host_auth_missing` hasta que esté hecho). En
cada nodo:

```bash
sudo passwd hacluster                         # la misma contraseña fuerte en todos
sudo systemctl enable --now pcsd
```

Después, en un nodo (pcs pide la contraseña de forma interactiva):

```bash
sudo pcs host auth node1 node2 node3 -u hacluster
```

### 4.3 Tipos de watchdog

| Tipo | Significado |
|---|---|
| hardware | El mejor: un temporizador de hardware reinicia la máquina aunque el kernel esté congelado |
| `i6300esb` | Bueno dentro de una VM: el hipervisor emula el watchdog y reinicia la VM |
| `softdog` | Último recurso, solo pruebas: vive en el kernel y no protege ante un kernel colgado (el asistente muestra un aviso) |
| ninguno | SBD no se puede configurar: carga un módulo o añade un watchdog virtual |

VM de Proxmox: Hardware, Añadir, Watchdog, modelo `i6300esb`, acción `reset` y
reinicia la VM.

### 4.4 Seguridad

- Un clúster que ya existe en el nodo nunca se toca: el asistente se detiene
  con "Ya existe un clúster en este nodo". Elimínalo a mano antes si de verdad
  quieres empezar de cero.
- No se cambia nada hasta el último paso; una creación interrumpida lista los
  comandos que faltan.
- Tras crearlo, ejecuta "Comprobar la preparación HA" y después la prueba de
  fencing (sección 5).

## 5. Fencing

El fencing aquí es **SBD en modo solo watchdog** (`stonith-watchdog-timeout`,
sin dispositivos stonith): un nodo que pierde el clúster se reinicia a sí mismo
con su watchdog. El failover automático y el HA por carga se rechazan hasta que
el fencing esté demostrado.

### 5.1 Resultado medido

Colgar un nodo (kernel panic por sysrq, ver abajo) en el entorno de pruebas: la
pérdida se detectó y el fencing terminó en unos 20 s (igual a
`stonith-watchdog-timeout=20s`), la VM la reinició su watchdog `i6300esb` y
volvió sola, y el clúster mantuvo el quórum (2/3) todo el tiempo. Después
Pacemaker registró un `fence_event ... status="success"` en `pcs status xml`,
que es lo que el producto interpreta como "fencing demostrado". Una segunda
prueba midió ~18 s. Ambas: **medido** en el entorno de pruebas.

### 5.2 Pruébalo con honestidad: simula un fallo

Con fencing solo watchdog, `pcs stonith fence NODO` sobre un nodo **sano** no
garantiza que se reinicie: sin dispositivo de fencing, Pacemaker solo da por
hecho que el nodo se aisló a sí mismo tras el timeout. No demuestra nada. La
prueba fiable es colgar el objetivo de verdad. En el nodo que quieres aislar,
como root:

```bash
echo 1 > /proc/sys/kernel/sysrq
echo c > /proc/sysrq-trigger          # kernel panic: el watchdog reinicia el nodo
```

Reglas: usa un nodo que no ejecute el plano de control de WebKVM, hazlo en una
ventana de mantenimiento, y espera que el nodo desaparezca 20 a 30 s y vuelva
solo.

En la UI (página Nodos, "Probar el fencing"): el panel detecta cómo aísla el
clúster. Con un dispositivo de fencing ofrece la prueba directa ("Aislar este
nodo ahora", con confirmación escrita). Con solo watchdog ofrece "Simular un
fallo": elige el nodo, copia el comando (`sudo sh -c 'echo 1 >
/proc/sys/kernel/sysrq; echo c > /proc/sysrq-trigger'`), ejecútalo tú en ese
nodo y pulsa "Ya lo he ejecutado: esperar la evidencia". WebKVM no cuelga nada.
La página consulta cada 5 s durante un máximo de 10 minutos. La prueba directa
sobre un nodo sano se rechaza con `watchdog_only_unreliable`. Flujo de la UI:
**no probado en vivo** (el cuelgue en sí se hizo a mano y se midió).

Evidencia de solo lectura desde cualquier nodo o la API:

```bash
pcs stonith history show NODO
pcs status xml | grep fence_event
curl -k -H "Authorization: Bearer $TOKEN" https://VIP:8080/api/ha/fencing/evidence
```

`GET /api/ha/fencing/evidence` devuelve el modo (`watchdog_only`, `device` o
`none`), si está demostrado, los eventos y la plantilla del comando de cuelgue.

**La evidencia persiste tras reiniciar el clúster.** Pacemaker guarda
`fence_history` solo en memoria: tras un reinicio completo del clúster (hallado
en el entorno de pruebas, donde las dos pruebas reales y deliberadas de fencing del día
anterior desaparecieron de `pcs stonith history` y solo quedó el fence del
arranque en frío) el fencing volvería en silencio a "sin probar". Por eso WebKVM
vuelca cada evento `reboot`/`off` exitoso que lee de un nodo de este clúster en
un libro de evidencia, `DATA_DIR/ha-fencing-events.json` (0600, máximo 50
eventos, el más reciente primero, se reinicia si cambia el nombre del clúster).
El libro también se rellena en segundo plano cada 60 s (primera pasada ~10 s tras arrancar, con espera creciente hasta 5 min si falla la sonda) mientras el módulo HA esté activo, así que un evento se captura aunque nadie abra la página de readiness. La evidencia es historial más libro; cada evento lleva `source` (`history` o
`ledger`) y `recorded_by` puede ser `ledger`. La API de fencing directo nunca
alimenta el libro en modo `watchdog_only`, y un fichero corrupto o que no sea
0600 se ignora.

### 5.3 Watchdog de una VM Proxmox

Para nodos de prueba que son VMs Proxmox, el watchdog es un dispositivo de
la VM: `model=i6300esb,action=reset` (Hardware, Añadir, Watchdog). Con una
`action` distinta de `reset` la VM no se reinicia cuando salta el watchdog.

### 5.4 Segunda capa

SBD con watchdog de VM funciona; se recomienda además fencing por hipervisor
(`fence_pve`) o por BMC (Redfish/IPMI). El instalador solo instala los agentes;
la configuración es un paso posterior. Una instalación de prueba en un solo host
no protege de perder ese host.

## 6. Acceso SSH gestionado a nodos KVM remotos

WebKVM abre y edita VMs de otros nodos KVM por libvirt sobre SSH con **una
clave propia**. WebKVM nunca copia la clave a un nodo: ejecutas un comando por
nodo. Estado: probado con mocks en tests unitarios; el cliente libvirt del
entorno de pruebas (12.0.0) enlaza libssh y sshd permite el reenvío (comprobaciones de
solo lectura). El flujo completo en la UI **no está probado en vivo**.

### 6.1 Configurarlo (página Nodos, tarjeta "Acceso del clúster (SSH gestionado)")

1. **Clave del clúster**: "Generar clave del clúster" crea un par ed25519 en
   `DATA_DIR/cluster-ssh/`. La clave privada nunca sale de este servidor.
2. **Autorizar**: para cada nodo remoto, copia el comando y ejecútalo en ese
   nodo con el usuario SSH del nodo. Instala esta línea de `authorized_keys`:

   ```text
   restrict,port-forwarding,permitopen="127.0.0.1:*" ssh-ed25519 AAAA... comentario
   ```

   `restrict` desactiva todo salvo lo listado; la clave solo puede reenviar a
   `127.0.0.1` de ese nodo (necesario para el túnel de la consola).
3. **Verificar la identidad**: "Comprobar huella del host" lee la clave de host
   SSH del nodo. Compara la huella SHA-256 en el nodo y luego fíjala:

   ```bash
   ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
   ```

4. **Probar**: "Probar conexión" recorre clave, huella del host, login SSH y
   libvirt, e informa del primer fallo con un código estable (sección 11).

Registra antes el nodo con una dirección simple
`qemu+ssh://[usuario@]host[:puerto]/system` (sin contraseña ni query;
`qemu+tcp` se rechaza).

### 6.2 Rotación de la clave (probar antes de confirmar)

1. "Iniciar rotación" crea una clave pendiente. La actual sigue funcionando.
2. Autoriza la clave NUEVA en todos los nodos (la tarjeta muestra el comando
   por nodo).
3. "Probar todos los nodos" entra en cada nodo habilitado con la clave
   pendiente. El resultado vale 15 minutos.
4. "Cambiar a la clave nueva" se rechaza (`rotation_unverified`) salvo que todos
   los nodos libvirt habilitados hayan aceptado la clave nueva; se puede forzar
   con una confirmación explícita, y entonces los nodos que no la aceptaron
   responden `ssh_auth_denied`.
5. "Volver a la clave anterior" restaura la clave activa antes del último
   cambio (solo se conserva una clave anterior).

"Cancelar rotación" descarta la clave pendiente.

### 6.3 Ajustes y ficheros relacionados

- `cluster.libvirt_transport`: `libssh2` (por defecto, el cliente libssh2
  integrado de libvirt con el `known_hosts` fijado propio de WebKVM, falla
  cerrado si no coincide), `libssh` (cliente libssh de libvirt, mismas
  comprobaciones) o `ssh` (el binario `ssh` del sistema). Medido en vivo:
  `libssh` se detiene exactamente 5 s (o se cuelga) en cada respuesta de más de
  ~4 KB (listas de VM y snapshots, `ssh/test`); `libssh2` y `ssh` responden en
  menos de 5 ms. Usa `libssh` o `ssh` si la prueba informa
  `transport_unsupported`. Límite conocido: libssh2 solo verifica una huella
  ECDSA fijada (la que WebKVM fija si el nodo la ofrece); un nodo con solo
  clave de host ed25519 falla cerrado como `host_key_changed`: usa `libssh`.
  Se aplica a conexiones nuevas. Las llamadas a un nodo de más de 3 s se
  registran como `libvirt_remote_slow`.
- `DATA_DIR/cluster-ssh/` (clave privada, huellas fijadas) está **excluido de
  las copias de configuración** (cubierto por un test). Al restaurar una copia
  en un servidor nuevo debes generar y autorizar una clave otra vez.
- La clave gestionada abre todos los nodos: mantén mínimo el usuario SSH
  dedicado y rota la clave si sospechas una fuga.

### 6.4 `ui_url` por nodo y por qué las direcciones privadas fallan desde tu PC

La dirección del nodo que usa WebKVM suele estar en la red privada del clúster
(por ejemplo `198.51.100.x`), que tu PC no alcanza. En cada nodo rellena
"Dirección en tu red (URL)" (`ui_url`), la dirección por la que TÚ llegas a ese
nodo, por ejemplo `https://192.0.2.10:8080`. Solo se admite
`esquema://host:puerto` (sin ruta, credenciales ni query). Se usa para enlaces y
el botón "Abrir WebKVM", y se devuelve junto a los errores `remote_unsupported`
para que la UI ofrezca abrir el nodo. Abre el propio panel por la VIP de la LAN,
no por la red privada.

## 7. Abrir y editar VMs e Incus desde cualquier nodo

El acceso remoto es **solo para administradores**. Usa `?node=` (y
`hv=kvm|incus` cuando el nombre del miembro Incus coincide con un hostname
KVM). Casi todo esto se ejercitó con mocks; la edición remota y la consola
remota **no están probadas en vivo**.

Qué funciona en remoto, por clase de ruta (79 rutas: 37 A, 16 B, 26 L):

| Clase | Significado | Ejemplos |
|---|---|---|
| A | Se sirve igual | Detalle de VM, acciones de energía, lista de discos, `/meta`, `/boot`, `/autostart`, `/snapshots`, agente invitado, serie |
| B | Se sirve con adaptación | Migrar (orquestada por el cliente), adjuntar disco, redes, `/cover`, `/cloudinit`, `/logs`, consola (`/vnc*`, `/spice*`, `/graphics`, `/console-ticket`), `GET /api/vms` (federado, con `node_id` y `stale`) |
| L | Solo local: responde `501 remote_unsupported` | clonar, clonar en lote, mover almacenamiento, exportar, backup, firewall, alertas, planificador, `/metrics*`, `/disks/{dev}/probe`, `/vlan-support` |

Para la clase L abre ese nodo en su propio WebKVM (`ui_url`).

Consola remota: VNC y serie van por un túnel SSH `direct-tcpip` a
`127.0.0.1:<puerto>` de ese nodo, con la huella fijada; los tickets están
ligados a (nodo, VM). SPICE en el navegador no está disponible en remoto: usa
noVNC o descarga el fichero `.vv` (SPICE remoto: no probado en vivo).

Errores posibles (el cuerpo trae `code`, `node`, `node_id`; nunca la salida
cruda de ssh):

| Código | HTTP | Significado | Solución |
|---|---|---|---|
| `node_unreachable` | 502 | No se alcanza el puerto SSH del nodo | Revisa alimentación, host, puerto, firewall |
| `node_timeout` | 504 | El nodo no respondió a tiempo | Revisa la red y la carga; reintenta |
| `ssh_auth_denied` | 502 | El nodo rechazó la clave del clúster | Ejecuta el comando de autorización en el nodo y prueba de nuevo |
| `host_key_unpinned` | 409 | Huella del host sin confirmar | "Comprobar huella del host", compara y fija |
| `host_key_changed` | 409 | La huella difiere de la fijada | ¿Reinstalado? revisa y reemplaza; si no, trátalo como un incidente |
| `node_disabled` | 404 | El nodo está desactivado | Actívalo en Nodos |
| `transport_unsupported` | 501 | El cliente libvirt no tiene el transporte configurado | Pon `cluster.libvirt_transport` a `libssh`, `libssh2` o `ssh` |
| `libvirt_unavailable` | 502 | SSH funciona, libvirt no responde | Arranca `libvirtd`/`virtqemud`; instala `virt-ssh-helper` o `nc` |
| `no_key` | 409 | No hay clave del clúster | Genérala |
| `remote_unsupported` | 501 | Acción solo local | Abre el WebKVM propio del nodo (`ui_url`) |
| `console_busy` | 429 | Demasiadas consolas abiertas en ese nodo | Cierra una y reintenta |
| `console_forward_denied` | 502 | El nodo rechazó el túnel | Revisa las opciones de `authorized_keys` en el nodo |
| `vm_not_running` | 409 | No hay puerto gráfico | Arranca la VM |
| `ticket_scope` | 403 | El ticket de consola es de otro nodo o VM | Cierra y vuelve a abrir la consola |

Quien no es administrador recibe 403 en cualquier `?node=` remoto. Los miembros
Incus del clúster local se alcanzan por el socket local; mover instancias entre
miembros (`POST /api/vms/{id}/move`) responde `member_offline`, `needs_stop`,
`same_member` o `not_clustered` (409) según el caso.

## 8. HA por carga (KVM e Incus)

Pacemaker es el único cerebro: el CIB es la fuente de verdad. Lo que no aparece
en "Cargas HA" nunca se toca. Todos los endpoints son solo admin y necesitan el
módulo activo. Estado: la secuencia de comandos `pcs` del planificador se
ejecutó contra una copia temporal del CIB real (`pcs -f`, pcs 0.12.1, todo
rc=0: **solo simulado**); activar HA en una VM o instancia real desde la UI/API,
`cib-push` y el flujo de mantenimiento **no están probados en vivo**.

### 8.1 Activar y desactivar en la UI

1. Activa el interruptor "Alta disponibilidad" en la VM o contenedor (o usa el
   panel "Cargas HA").
2. El panel comprueba la elegibilidad. Si está bloqueada, "¿Por qué?" lista cada
   motivo con "Cómo solucionarlo" (y comandos copiables para redes de Incus).
3. El modo Fácil activa con valores por defecto. Avanzado: "Opciones de
   ubicación" (nodos permitidos, nodo preferido, stickiness, umbral de
   migración) y "Ver comandos pcs" (simulacro).
4. Confirma "Activar HA".

Requisitos del clúster (bloqueos, HTTP 409 `precondition_failed`):
`fencing_test_required`, `sbd_not_active`, `quorum_missing`,
`not_enough_nodes`, `incus_healing_enabled`, `incus_member_not_online`,
`control_plane_unhealthy`, `drbd_not_uptodate`, `not_eligible`.

Motivos de elegibilidad y soluciones:

| Motivo | Solución |
|---|---|
| `disk_not_on_replicated_storage` | Mueve los discos a un pool replicado/compartido (DRBD, LINSTOR, Ceph) |
| `xml_not_on_replicated_storage` | Mantén la definición de la VM en el sistema de ficheros replicado del clúster |
| `drbd_not_uptodate` | Espera a que termine la resincronización o arregla el nodo degradado |
| `pool_not_shared` | Usa un pool que vean todos los nodos (Incus: driver `linstor`, `ceph` o `cephfs`) |
| `local_devices` | Desconecta dispositivos USB/PCI/discos locales |
| `boot_autostart_enabled` | Desactiva el autoarranque (en Incus la propia activación pone `boot.autostart=false`) |
| `vm_running` | Apaga antes la VM KVM (v1); Pacemaker la arranca |
| `bridge_missing_on_nodes` | Crea el mismo bridge en los nodos que faltan |
| `nic_network_not_on_all_members` | Usa una red que exista en todos los miembros Incus (8.3) |
| `name_collision` | Renombra una de las cargas |
| `incus_member_not_online` | Pon el miembro Incus en línea |

El HA de KVM es para VMs sobre el volumen DRBD de este nodo; activarlo para una
VM de un nodo remoto se rechaza. Otras acciones por carga: "Mover", "Limpiar
fallos", "Mantenimiento", "Desactivar HA".

Modos de desactivación: **Pausar HA** (por defecto y el más seguro: conserva
los recursos, Pacemaker deja de actuar, la VM sigue definida y visible),
**Detenerla** (Pacemaker la para y quita los recursos), **Olvidarla** (quita
los recursos de una carga que ya está detenida; se rechaza con
`workload_active` mientras alguna parte esté en marcha, o con `workload_paused`
si está en pausa: reanúdala y luego detenla, o detenla directamente; se escribe
la clave para confirmar; no disponible en bloque). Activar/desactivar en bloque:
hasta 50 cargas, cada una se comprueba por separado.

#### Desactivar HA en una VM KVM (Detenerla / Olvidarla): qué esperar

En KVM, Detenerla y Olvidarla liberan el almacenamiento compartido de la VM. La
VM se para, su recurso DRBD baja en todos los nodos, el sistema de ficheros se
desmonta y el enlace `/etc/libvirt/qemu/<nombre>.xml` hacia el sistema de
ficheros compartido desaparece (el VirtualDomain de Pacemaker ejecutaba la VM
como dominio transitorio), así que libvirt deja de tener la VM. Si solo quieres
suspender los reinicios automáticos, usa **Pausar HA**.

**Antes de tocar el clúster**, WebKVM guarda una entrada de inventario
"liberada" en `DATA_DIR/ha-released-vms.json` (modo 0600, escritura atómica,
nunca se borra sola): nombre, UUID, XML del dominio, recurso DRBD, dispositivo,
punto de montaje, tipo de sistema de ficheros, último nodo y ubicación. El XML
sale, por este orden, de: la definición en el sistema de ficheros replicado
(solo si la VM corre en el nodo que sirve el panel), el libvirt de este nodo,
el libvirt del miembro que la ejecuta (nodo registrado) o una captura anterior.
Si no se puede capturar un XML válido con el mismo nombre (por ejemplo, la carga
está en pausa: no corre nada y el sistema de ficheros no está montado), la
desactivación se **rechaza** con `409 release_capture_failed` y no cambia nada;
reanuda la carga y repite, o pasa `accept_unrestorable=true` (en la UI, una
casilla explícita) para liberarla sin entrada restaurable. Si después la
desactivación falla sin cambiar nada, se revierte la entrada escrita por esa
petición.

La VM aparece entonces en **VMs liberadas** (panel de cargas HA y lista de VMs,
solo administradores) con la etiqueta "Liberada · almacenamiento libre" y dos
acciones:

- **Restaurar** (escribe el nombre de la VM; opcional "arrancar después"). En
  el nodo que sirve el panel, y solo tras comprobar que Pacemaker ya no
  gestiona la VM ni su recurso DRBD. Pasos:
  1. **Requisito — levantar las otras réplicas.** Tras Detener/Olvidar, el
     agente de recursos DRBD deja el recurso `down` en **todos** los nodos; con
     `quorum=majority` no se puede promover en un nodo solo. En cada uno de
     los otros nodos con réplica ejecuta `drbdadm up <res>` (la entrada los
     lista como `drbd_peers`, anotados en solo lectura al desactivar HA).
     WebKVM no puede hacerlo: no tiene canal de órdenes hacia otros nodos.
  2. `drbdadm up` aquí (si está caído) y espera a los pares. Si DRBD indica
     `quorum:no` (o, sin ese campo, no hay mayoría de réplicas conectadas) la
     restauración se detiene **antes de promover** con
     `409 drbd_peers_down`, con cada par, su estado de conexión y la orden
     exacta (`drbdadm up <res>`); el diálogo los muestra con un botón de
     copiar y un botón **Volver a comprobar** que reintenta. No queda nada
     hecho (un `up` local de esa llamada se deshace; un recurso que ya estaba
     activo sigue activo).
  3. Rechaza si otro nodo es Primary (`drbd_primary_elsewhere`) o el disco
     local no está UpToDate; `drbdadm primary`.
  4. `mkdir -p` + `mount` del dispositivo registrado **en el espacio de
     nombres de montaje del host** (`systemd-run --pipe --wait --collect`,
     como las actualizaciones del host: `webkvm.service` tiene su propio
     espacio de montaje por `ProtectSystem=full`/`PrivateTmp`, y un montaje
     hecho ahí es invisible para libvirt y Pacemaker y deja DRBD abierto).
     Después `findmnt` en el espacio del host debe mostrarlo; si no, se
     deshace todo (`mount_not_visible`).
  5. `virsh define` del XML guardado (o de la definición del sistema de
     ficheros si tiene el mismo nombre y UUID; la respuesta indica
     `xml_differs`).
  6. Solo tras un `define` correcto: la definición se guarda en el sistema de
     ficheros replicado (`<ruta config>` o `<mnt>/<nombre>.xml`: escritura
     atómica, 0600, solo si no existe ningún fichero ahí — nunca se
     sobrescribe un fichero que no sea la definición de esta VM,
     `xml_link_failed`) y `/etc/libvirt/qemu/<nombre>.xml` pasa a ser un
     enlace a ella (`ln -sfnT`, espacio del host), que es lo que esperan la
     activación de HA (`xml_not_on_replicated_storage`) y `config=` de
     VirtualDomain. La respuesta indica `xml_linked`.
  7. `virsh start` opcional.

  Cualquier fallo deshace lo que hizo esa llamada, en orden inverso
  (`undefine --keep-nvram`, borrar el fichero de definición que escribió,
  `umount` en el espacio del host, `drbdadm secondary`, `drbdadm down`);
  nunca se borran datos. Una VM ya definida con el UUID guardado solo es
  no-op (`changed:false`) si el montaje es visible para el host, DRBD es
  Primary aquí y la definición está enlazada; si no, se reparan las piezas
  que faltan (montaje, promoción, enlace — el dominio existente nunca se
  borra). Otro dominio con el mismo nombre se rechaza (`domain_conflict`).
  La entrada se conserva marcada como "Restaurada".
- **Descartar entrada** (escribe el nombre): solo elimina la entrada del
  inventario.

Para protegerla de nuevo, activa HA desde la página de la VM (siguen aplicando
las comprobaciones de elegibilidad). No se ofrece restaurar en otro nodo: abre
el panel servido por ese nodo o restaura a mano.

Restauración manual (alternativa, p. ej. sin XML guardado). En un nodo, con
`<res>` el recurso DRBD y `<mnt>` el directorio donde estaban los ficheros de
la VM:

1. `drbdadm up <res>` (en los nodos que deban tener réplica).
2. `drbdadm primary <res>` en el nodo que ejecutará la VM.
3. Monta de nuevo el sistema de ficheros en `<mnt>` (el mismo punto de montaje).
4. `virsh define <mnt>/<nombre>.xml` (o recrea el enlace
   `/etc/libvirt/qemu/<nombre>.xml`) y `virsh start <nombre>`.

La restauración manual ejecuta cada orden en una shell root del host (no con
`nsenter` dentro del servicio WebKVM). Si una VM se restauró con v13 (montaje
solo dentro del espacio del servicio), desmóntalo antes ahí:
`nsenter -t $(systemctl show -p MainPID --value webkvm) -m umount <mnt>`.

**No probado en vivo**: la captura y el botón Restaurar solo se han probado con
dobles (ver STABLE-REPORT 8.20); las correcciones de 8.22 (espacio de montaje
del host, `drbd_peers_down`, enlace de la definición) tampoco.

Una carga bajo HA tiene su estado de energía en Pacemaker: usa Mover, no
Migrar; la API responde `409 pacemaker_managed` a los botones de energía
normales.

### 8.2 Ediciones seguras: flujo de mantenimiento

1. "Iniciar mantenimiento" (ventana de 15 min, 1 h o 4 h): Pacemaker deja de
   reaccionar a la carga.
2. Edítala. Incus: desde su página. KVM: apágala antes, edítala y nunca la
   redefinas a mano.
3. "Terminar mantenimiento": Pacemaker la recupera y comprueba que arranca. Si
   no arranca, puede que la configuración editada sea incorrecta.

Si la ventana caduca no se reactiva nada automáticamente: solo aparece el aviso
`workload_unmanaged_expired`. Estado: **solo simulado**.

### 8.3 Requisitos de Incus

- Disco raíz en un pool `linstor` o `ceph` (también se acepta `cephfs`).
- La red de cada NIC (perfil o instancia) debe existir en TODOS los miembros.
  Hallazgo real: el perfil `default` usaba `vmbr0`, que existía solo en un
  miembro, así que un contenedor movido fallaba con
  `Parent device "vmbr0" doesn't exist`.
- `boot.autostart=false` (al activar HA se pone solo).
- `cluster.healing_threshold=0`: Pacemaker es el único cerebro. La API se niega
  a activar el healing nativo (`409 healing_managed_by_pacemaker`) y solo acepta
  0. Hallazgo real: un umbral residual de 60 s evacuaba miembros por su cuenta.

Perfil portable recomendado usado en el entorno de pruebas (macvlan sobre `eth0`, que
existe en los tres miembros y da dirección en la LAN; adapta la NIC padre a tu
hardware). El perfil `default` del entorno de pruebas no se tocó:

```bash
incus profile create ha-lan
incus profile device add ha-lan eth0 nic nictype=macvlan parent=eth0 name=eth0
incus profile device add ha-lan root disk path=/ pool=ha-shared
incus launch IMAGEN NOMBRE -p ha-lan       # un contenedor con él arrancó en los 3 miembros
```

La solución que ofrece la UI por el mismo motivo es una red gestionada de todo
el clúster (`incus network create ha-net --target MIEMBRO` en cada miembro y
luego `incus network create ha-net`); esa vía no se ejecutó en vivo.

### 8.4 Nombres de recursos y agente

KVM: clon `ha-kvm-<nombre>-data-clone`, grupo `ha-kvm-<nombre>-grp` (`-fs` +
VM). Incus: una primitiva `ha-incus-<proyecto>-<nombre>` de
`ocf:webkvm:incus` (ver sección 12). Los recursos llevan los meta
`webkvm-kind`, `webkvm-name`, `webkvm-project`; los ids nunca se interpretan.
Las restricciones propias son `webkvm-loc-*` / `webkvm-anti-*` / `webkvm-col-*`;
solo esas se borran.

### 8.5 Resumen de la API (`/api/ha`, admin, módulo activo)

| Método y ruta | Para qué |
|---|---|
| `GET /workloads`, `GET /workloads/{kind}/{name}` | Listar y detalle (`kind` = `kvm` o `incus`) |
| `POST /workloads/{kind}/{name}/enable` | Activar (`dry_run`, `allowed_nodes`, `preferred_node`, ...) |
| `DELETE /workloads/{kind}/{name}?mode=stop\|pause\|forget` | Desactivar |
| `PUT .../placement`, `POST .../move`, `POST .../clear-failcount` | Ubicación, mover, borrar fallos |
| `POST .../maintenance` `{action: begin\|end, ttl_s}` | Ventana de mantenimiento |
| `POST /workloads/bulk` | Activar/desactivar en bloque |
| `DELETE /workloads/kvm/{nombre}?mode=stop&accept_unrestorable=true` | Liberar sin entrada restaurable (explícito) |
| `GET /released` | VMs KVM liberadas (sin XML en la respuesta) |
| `POST /released/{nombre}/restore` `{confirm, start, node}` | Restaurar en el nodo que sirve el panel |
| `DELETE /released/{nombre}?confirm={nombre}` | Descartar solo la entrada del inventario |
| `GET /readiness`, `GET /fencing/evidence`, `POST /fencing/test` | Preparación y fencing |
| `POST /nodes/{name}/standby`, `.../unstandby` | Mantenimiento de nodo con guardas de quórum |

Claves de carga: `kvm:<nombre>`, `incus:<proyecto>/<nombre>`.

## 9. Resultados de validación

Resultados de un entorno de pruebas de tres nodos. Build 0.1.12 del
2026-11-12. Muestran comportamiento, no capacidad de producción.

| Prueba | Resultado |
|---|---|
| Standby del nodo que ejecuta el plano de control | Plano de control de vuelta en otro nodo en ~4 s; las VIPs respondieron, DRBD UpToDate |
| Cuelgue de un nodo (sysrq) sin el plano de control | Aislado en ~18 a 20 s, reiniciado por el watchdog, quórum 2/3 todo el rato, sin failcounts después |
| VM KVM HA (grupo `kvm-ha` existente, no creado con la UI nueva) | Recuperada tras el fencing |
| Contenedor Incus con `ocf:webkvm:incus` tras caída de un nodo | Fencing en ~18 s, arranque a ~31 s, `incus move` con el miembro de origen APAGADO, RUNNING a **~59 s** |
| Fallo local de un contenedor Incus HA (`stop --force`) | Recuperado en ~15 s, failcount 1 |
| `pcs resource move` de un recurso Incus | 10 s y 12 s |
| Movimiento Incus entre miembros EN LÍNEA, como recurso Pacemaker | ~5 s |
| `ocf-tester` sobre un contenedor desechable (pool LINSTOR) | PASS |

Las pruebas de Incus usaron una instancia desechable como recurso Pacemaker
creado a mano, no con el interruptor "Alta disponibilidad".

## 10. Límites conocidos y qué no está validado en vivo

No probado en vivo:

- Ruta dnf de Fedora/derivadas de RHEL (y DNF5 en Fedora 44), ruta de
  instalación en Arch (solo se ejecutó `check`), Debian 13.
- El asistente de clúster de extremo a extremo y `pcs stonith sbd enable`.
- SSH gestionado en la UI, edición remota, consola remota (VNC, serie, SPICE),
  `permitopen`/`restrict` con el OpenSSH real de tus nodos.
- Activar HA en una VM/instancia real desde la UI/API, `cib-push --config`, el
  flujo de mantenimiento, las operaciones en bloque.
- Los flujos de "Probar el fencing" de la UI; snapshots con discos; migrar
  entre nodos.
- Restauración completa de la base LINSTOR (ver sección 12).

Límites:

- Una instalación de prueba en un solo host no protege de perder ese host ni su
  SSD raíz.
- No hay migración en vivo de instancias Incus: mover es parar + arrancar.
- Crear un volumen LINSTOR nuevo de 3 réplicas exige los tres miembros en línea.
- Las rutas de clase L solo funcionan desde el WebKVM propio del nodo; el
  estado local por nodo (portadas, planificadores, alertas, grupos) se indexa
  por nombre y puede colisionar entre nodos.
- No hay canal de alertas configurado: `webkvm-ha-health-check` solo escribe en
  el journal (añade `OnFailure=` a su unidad).

## 11. Resolución de problemas

| Síntoma | Causa | Solución |
|---|---|---|
| UI en blanco o inalcanzable desde tu PC | Usaste una dirección privada del clúster | Usa la VIP de la LAN o el `ui_url` del nodo; rellena `ui_url` por nodo |
| `ssh_auth_denied` justo tras autorizar | Comando ejecutado con otro usuario o en otro nodo; permisos de `~/.ssh` o `authorized_keys`; el pool guardó un fallo anterior | Ejecuta el comando con el usuario del nodo registrado en `usuario@host`; revisa `chmod 700 ~/.ssh; chmod 600 ~/.ssh/authorized_keys`; pulsa "Probar conexión" otra vez (una prueba explícita ignora la caché de fallos) |
| `host_key_unpinned` | Huella del host nunca confirmada | "Comprobar huella del host", compara con `ssh-keygen -lf` y fija |
| `host_key_changed` | Nodo reinstalado, claves SSH regeneradas o un impostor | Si se reinstaló: revisa de nuevo y "Reemplazar huella fijada" tras verificar en el nodo. Si no: trátalo como un incidente de seguridad |
| `transport_unsupported` | Cliente libvirt sin el transporte configurado | Pon `cluster.libvirt_transport` a `libssh`, `libssh2` o `ssh` |
| `libvirt_unavailable` | `libvirtd`/`virtqemud` caído, sin `virt-ssh-helper`/`nc` | Arranca el demonio e instala el ayudante |
| Falla `incus move` o el movimiento HA | Instancia `RUNNING` en otro miembro en línea (el agente nunca la mueve), pool raíz local, miembro caído o `Parent device ... doesn't exist` | Para antes la instancia; usa un pool `linstor`/`ceph`; pon el miembro en línea; arregla la red de la NIC (abajo) |
| `Parent device "vmbr0" doesn't exist` | Una NIC de perfil usa un bridge que existe solo en un miembro | Usa el perfil `ha-lan` (8.3) o una red de todo el clúster; ajusta con `incus profile ...`/`incus config device ...` y arranca de nuevo |
| `nic_network_not_on_all_members` al activar | La misma causa, detectada antes de activar | Aplica los comandos de "Cómo solucionarlo" |
| Un miembro Incus aparece `Evacuated` | El healing nativo (o una evacuación manual) movió sus instancias | `incus cluster restore MIEMBRO`; asegúrate de que `cluster.healing_threshold` es `0` |
| `409 precondition_failed` al activar | Faltan requisitos del clúster | Lee `blockers`: normalmente ejecuta la prueba de fencing (sección 5) |
| `409 incus_healing_enabled` | El healing nativo está activo | `incus config set cluster.healing_threshold=0` |
| `watchdog_only_unreliable` | Fencing directo de un nodo sano con SBD solo watchdog | Usa "Simular un fallo" (5.2) |
| `host_auth_missing` en el asistente | Falta `pcs host auth` | Pon la contraseña de `hacluster` y luego `pcs host auth` (4.2); "Volver a comprobar" |
| `cluster_exists` | Ya hay un clúster configurado aquí | El asistente no lo tocará |
| Instalador sale con 12 / `units_active` | Hay una unidad del clúster en marcha que antes no lo estaba | Deténla y deshabilítala con el comando exacto mostrado |
| Instalador sale con 10 | Distro no soportada, experimental no aceptada, falta el repositorio HA o las bases de pacman | Sigue el paso manual mostrado y luego `webkvm-cli ha deps check` |
| `409 pacemaker_managed` | La carga pertenece a Pacemaker | Usa Mover/Pausar/Desactivar HA o `pcs` |
| `rotation_unverified` | La clave SSH nueva no la aceptan todos los nodos | Autorízala donde falla y "Probar todos los nodos" |

## 12. Topología de referencia y operación diaria

Arquitectura de clúster de 3 nodos de ejemplo (valida la topología en tu propio entorno).
En entornos de producción, distribuye los nodos entre hipervisores físicos,
dominios de fallo y fuentes de alimentación independientes.

### 12.1 Topología

| Pieza | Dónde |
|---|---|
| Clúster Incus | `node-a` (192.0.2.10), `node-b` (192.0.2.12), `node-c` (192.0.2.11) |
| Red privada | `198.51.100.10/.11/.12` en `eth1` |
| Controlador LINSTOR | VIP `198.51.100.30:3370`, promovido por `drbd-reactor` |
| Plano de control WebKVM | VIP privada `198.51.100.31:8080`; VIP de la LAN `192.0.2.13:8080` (`webkvm-vip-lan`) para tu PC |
| Datos WebKVM | DRBD `webkvm_data` (1 GiB, 3 réplicas) en `/opt/webkvm` |
| Disco de la VM HA | DRBD `kvm_data` (3 GiB, 3 réplicas) en `/var/lib/libvirt/images-ha` |
| Grupos Pacemaker | `webkvm-cp` (`webkvm-fs`, `webkvm-vip`, `webkvm-app`), `kvm-ha` (`kvm-fs`, `kvm-vm`) |

`node-a` puede quedar excluido de los grupos con una restricción de
localización si su almacenamiento local o cargas independientes no HA viven
bajo `/opt/webkvm` (por ejemplo `example-app` independiente). El plano de
control se identifica por el grupo de Pacemaker indicado en `ha.cp_group` (por
defecto `webkvm-cp`).

### 12.2 Comprobaciones diarias

```bash
pcs status                                   # quórum, recursos, fail counts
pcs resource failcount show
drbdsetup status                             # estado de disco y pares
curl -kfsS https://198.51.100.31:8080/api/health
curl -fsS http://198.51.100.30:3370/v1/nodes   # controlador y miembros Incus
systemctl list-timers webkvm-ha-health-check.timer
```

La página Nodos muestra lo mismo: `HA is active`, recursos de Pacemaker, flota
Incus (3/3) y avisos del clúster.

### 12.3 Mantenimiento

Sacar un nodo sin fencing (o usar los botones "Standby"/"Reanudar", que rechazan
un cambio que haría perder el quórum):

```bash
pcs node standby node-b        # los recursos pasan a node-c
# trabajar en el nodo y después
pcs node unstandby node-b
```

Pausar solo el servicio WebKVM:

```bash
pcs resource disable webkvm-app
pcs resource enable webkvm-app
```

Para reiniciar un miembro: standby, reinicio, esperar Incus/Corosync y
unstandby. Nunca `systemctl enable webkvm`: Pacemaker arranca la unidad solo en
el nodo que posee `webkvm-cp`.

Una VM bajo Pacemaker (por ejemplo `ha-test`) pertenece a Pacemaker. Se
gestiona con `pcs` o los controles HA, no con los botones de energía; la API
devuelve `409 pacemaker_managed` a propósito.

### 12.4 Prueba de failover (segura, sin apagar nodos)

```bash
t0=$(date +%s)
pcs node standby node-b
until pcs status | grep -q "webkvm-app.*Started node-c"; do sleep 2; done
echo "movido en $(( $(date +%s) - t0 ))s"
pcs node unstandby node-b
```

Validado en un entorno de pruebas: failover completado en ~8 s en cada sentido
para ambos grupos, incluido el arranque en frío de `ha-test`; el plano de
control se movió en ~4 s. Verificar después:

```bash
curl -kfsS https://198.51.100.31:8080/api/health
virsh domstate ha-test
```

### 12.5 Healing nativo de Incus

El healing nativo de Incus (`cluster.healing_threshold`) debe quedar
**apagado (0)**: evacúa miembros por su cuenta y pelea con Pacemaker. La API
solo acepta 0. Si un miembro se queda en estado `Evacuated`:

```bash
incus config get cluster.healing_threshold    # debe mostrar 0 (o vacío)
incus config set cluster.healing_threshold=0
incus cluster restore node-c
```

### 12.6 El agente `ocf:webkvm:incus`

El agente OCF `ocf:webkvm:incus` (`/usr/lib/ocf/resource.d/webkvm/incus`,
incluido en el paquete) ejecuta una instancia Incus como recurso Pacemaker.
`start` la arranca en el miembro local, moviéndola antes en frío
(`incus move --target`) si está parada en otro miembro o su miembro está
caído; `stop` la para de forma ordenada y solo fuerza tras `stop_timeout`;
`monitor` la da por activa solo si está `RUNNING` en el miembro local. No hay
migración en vivo: mover es parar + arrancar.

Requisitos: pool remoto (`validate-all` rechaza drivers locales),
`boot.autostart=false`, `cluster.healing_threshold=0`, fencing operativo (el
agente confía en que Pacemaker ya ha hecho fencing del nodo anterior antes de
ejecutar `start` en otro).

Instalación manual en un nodo (los paquetes ya lo hacen):

```bash
sudo install -m0755 incus /usr/lib/ocf/resource.d/webkvm/incus
```

Pruébalo **solo con una instancia de pruebas desechable** (ocf-tester la
arranca y la para varias veces):

```bash
ocf-tester -n x -o instance=NAME -o project=default /usr/lib/ocf/resource.d/webkvm/incus
```

Parámetros: `instance` (obligatorio), `project` (`default`), `stop_timeout`
(60 s), `move_wait` (90 s, tiempo que `start` reintenta el movimiento mientras
Incus sigue viendo en línea al miembro de origen).

Modelo de fallo: el agente nunca mueve una instancia en ejecución: si está
`RUNNING` en otro miembro en línea (o cuyo estado no se puede leer), `start`
falla con rc 1 y no se toca nada. Solo mueve instancias `STOPPED` o `ERROR`
(`ERROR` es lo que muestra un miembro perdido tras un fencing). Si el demonio
Incus no responde, `monitor` y `stop` fallan y Pacemaker hace fencing del nodo.
El harness offline es `sh packaging/ocf/test/run.sh` (66 escenarios, simulados).

### 12.7 Arranque tras un fencing

Los tres miembros llevan un drop-in de `systemd-networkd-wait-online` que solo
espera `eth1` con un timeout de 20 s. Sin él, un miembro fenced podía quedarse
colgado en el arranque por un `vmbr0` sin carrier y no arrancar Pacemaker. No
borres `/etc/systemd/system/systemd-networkd-wait-online.service.d/10-webkvm-ha.conf`.

### 12.8 Copias de seguridad

Recomendaciones de copia de seguridad externa y cifrada:

- Script: `/usr/local/sbin/webkvm-ha-state-backup`
- Destino: `/mnt/backup-share/webkvm-backups/`
- Contenido: `/opt/webkvm` (usuarios, configuración, tokens, claves TLS), CIB de
  Pacemaker, configuración DRBD/LINSTOR, tar de la base LINSTOR, versiones y
  estado.
- Cifrado: AES-256-CTR con clave en `/etc/webkvm-ha-backup.key` del host.
  **Si se pierde esa clave, ninguna copia se puede leer.**
- Retención: 14 días. Timer: `webkvm-ha-state-backup.timer`.
- Integridad: SHA-256 junto a cada fichero; descifrar y verificar cada
  archivo antes de darlo por bueno.
- Las copias de configuración de la aplicación excluyen `cluster-ssh/` (la
  clave SSH gestionada).

Ensayo de restauración (validado en un entorno de pruebas): copiar el archivo a un
miembro en standby, descifrar, extraer en `/tmp`, apuntar `DATA_DIR` a la copia,
arrancar un segundo `webkvm` en `127.0.0.1:18080` y consultarlo con
`webkvm-cli`. La instancia restaurada respondió health 200, listó `example-app` y
devolvió el usuario `admin`. Valida la restauración en tu entorno antes de
confiar en una copia.

La restauración completa de la base LINSTOR exige parar el controlador y el
recurso DRBD, así que debe tratarse como un procedimiento documentado de
recuperación ante desastres.

### 12.9 Recuperación ante desastre

1. Si `webkvm_data` se corrompe: `pcs resource disable webkvm-cp`, apartar el
   volumen antiguo antes de recrear el ext4, restaurar `/opt/webkvm` desde la
   copia y `pcs resource enable webkvm-cp`.
2. Si se pierde la base del controlador LINSTOR: restaurar el tar en
   `/var/lib/linstor` del primario DRBD y reiniciar `drbd-reactor`.
3. Si se pierden todos los hosts físicos o los pools de almacenamiento
   compartido, recupera a partir de las copias externas: re-aprovisiona los
   nodos, restaura la copia cifrada desde `backup-share` y reinicia el servicio.

### 12.10 Consideraciones operativas

- Asegúrate de que los nodos físicos cuenten con alimentación, discos e interfaces de red independientes para evitar puntos únicos de fallo.
- Las cargas no HA (por ejemplo instancias independientes de `example-app` con almacenamiento local) deben respaldarse de forma independiente según sus necesidades.

### 12.11 Actualizar WebKVM en un clúster HA

El botón **Actualizar** de la UI (`/#/status`) instala un binario en el nodo que
sirve el panel y reinicia su `webkvm.service` local. En un clúster HA esa unidad
la posee Pacemaker: el botón **no** actualiza el clúster y puede competir con el
recurso. Actualiza todos los nodos con la misma release.

Procedimiento rodante recomendado (el plano de control está caído mientras el
recurso está deshabilitado, así que anuncia una ventana de mantenimiento):

1. Copia `/opt/webkvm` y el CIB de Pacemaker; confirma que `pcs status` muestra
   quórum y ninguna acción fallida.
2. Deshabilita el recurso del plano de control y espera a que pare:
   ```bash
   sudo pcs resource disable webkvm-app --wait
   sudo pcs status
   ```
3. En **cada** nodo, instala el mismo binario. `/usr/local/bin` es local al
   nodo; `/opt/webkvm` es el volumen DRBD compartido y no debe recibir el
   binario. Desde el tarball de la release:
   ```bash
   sudo cp -a /usr/local/bin/webkvm /usr/local/bin/webkvm.previous
   sudo install -m 0755 backend/webkvm /usr/local/bin/webkvm.new
   sudo mv /usr/local/bin/webkvm.new /usr/local/bin/webkvm
   sudo install -m 0755 packaging/standalone/update.sh /usr/local/bin/webkvm-update
   ```
   O usa el asset directo `webkvm-0.1.13-linux_amd64` con el mismo `install`/`mv`.
4. Reactiva el recurso y verifica:
   ```bash
   sudo pcs resource enable webkvm-app --wait
   sudo pcs resource restart webkvm-app
   curl -kfsS https://<VIP>:8080/api/health
   webkvm version        # 0.1.13
   ```
   Repite `webkvm version` en cada nodo.
5. AppArmor: las VMs existentes se migran en el siguiente arranque, revert o
   borrado de snapshot gestionado por WebKVM; no hay que editar el XML. Las VMs
   gestionadas por Pacemaker las arranca Pacemaker, así que actualiza su XML solo
   si usas operaciones de snapshot de WebKVM sobre ellas.
6. Rollback: deshabilita el recurso, restaura `webkvm.previous` en cada nodo y
   vuelve a activarlo.

No uses el botón de la UI como actualizador del clúster. No ejecutes
`systemctl enable webkvm` en un miembro: la unidad la posee Pacemaker.
