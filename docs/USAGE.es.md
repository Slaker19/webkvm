# WebKVM — Manual de uso

Cómo usar WebKVM una vez instalado. Para la instalación y la documentación
técnica consulta [INSTALLATION.es.md](INSTALLATION.es.md).

[**English**](USAGE.md) • [**Español**](USAGE.es.md)

## 1. Primer inicio de sesión

1. Abre la URL que imprime el instalador (por defecto `https://IP:8080`, o
   `http://IP:8080` si elegiste HTTP plano).
2. Inicia sesión como `admin` con la contraseña guardada en
   `/opt/webkvm/admin-password.initial` (también se muestra al terminar la
   instalación).
3. El sistema te pedirá **cambiar la contraseña** en el primer acceso.

Con un certificado autofirmado, el navegador avisa la primera vez. Para quitar
el aviso, descarga el certificado desde `https://IP:PUERTO/api/system/cert` y
confíalo en tu sistema operativo.

## 2. Interfaz

La interfaz es una aplicación de página única con una barra lateral:

| Pestaña | Qué hace |
|---------|----------|
| **VMs** | Lista y cuadrícula de VMs, creación, acciones de encendido y consola. |
| **Almacenamiento** | Pools de discos, volúmenes y biblioteca de ISOs. |
| **Redes** | Redes NAT y bridge, bridges del host, firewall. |
| **Backup** | Destinos, programaciones, trabajos, archivos y restauración. |
| **Usuarios** | Cuentas, roles, grupos y tokens de API. |
| **Estado** | Estado del backend, de libvirt y del host, y logs. |
| **Ajustes** | Configuración del servidor (puerto, TLS, CORS, etc.). |

## 3. Máquinas virtuales

### Crear una VM

1. Ve a **VMs → Nueva VM**.
2. Elige el sistema operativo (los presets fijan el firmware UEFI/BIOS, el
   TPM y el arranque seguro, el bus de disco y la RAM/vCPU).
3. Adjunta una ISO de la biblioteca de **Almacenamiento** (o sube una).
4. Elige la red (NAT para acceso aislado a internet, o bridge para una IP real
   de la LAN).
5. Opciones avanzadas: rendimiento de disco recomendado
   (`cache=none, io=native`), descarte/TRIM para recuperar espacio, un
   desplegable de modelo de CPU con los presets habituales (o «Personalizado»
   para un modelo manual) y una topología de CPU explícita
   (zócalos/núcleos/hilos) en lugar de un número plano de vCPU. Siempre se
   adjunta un RNG por hardware virtio (no hay que activarlo).
6. Créala — la VM arranca y puedes abrir su consola.

### Passthrough de dispositivos USB (solo administradores)

Desde la pestaña **Interfaces** de la VM, los administradores pueden conectar un
dispositivo USB del host directamente a una VM encendida o apagada (y
desconectarlo después), viendo la lista de lo que hay enchufado en el host.

### Passthrough PCI / GPU (solo administradores)

Desde la pestaña **Hardware** de la VM, los administradores pueden asignar un
dispositivo PCI del host (GPU, tarjeta de red, controladora NVMe…) a una VM
apagada. WebKVM ejecuta antes una **comprobación previa** — IOMMU activo, grupo
IOMMU del dispositivo aislado y `vfio-pci` disponible — y se niega a conectarlo
cuando el grupo arrastraría consigo dispositivos no relacionados.

### Plantillas y cloud-init

- **Crear una plantilla**: con la VM apagada, usa «Convertir en plantilla».
  Después podrás crear VMs nuevas a partir de ella.
- **cloud-init (NoCloud)**: al instanciar una plantilla puedes aprovisionar
  automáticamente un usuario, una contraseña, una clave SSH y el nombre de host.
- **Clonar**: duplica una VM existente (discos incluidos) con un solo clic.

#### Compartir una plantilla con todos los usuarios (solo administradores)

Por defecto, una plantilla solo es visible para su propietario y para quien las
reglas normales de ACL, grupo y etiquetas ya autoricen. La página de detalle de
una plantilla incluye un interruptor **«Compartir con todos los usuarios»** que
solo los administradores pueden accionar.

Una vez compartida:

- **Cualquier usuario puede verla e instanciarla**, con independencia del
  propietario y del acceso por grupo o por etiqueta. El interruptor *salta* el
  alcance habitual — no es una entrada de ACL adicional, así que revisa el
  contenido de la plantilla antes de activarlo.
- **Cada instanciación hace una copia completa del disco de la plantilla.** Diez
  usuarios instanciando una plantilla de 40 GiB consumen 400 GiB. La interfaz
  muestra este aviso junto al interruptor precisamente por eso.
- **Clonar una plantilla compartida no arrastra la marca.** El clon vuelve a ser
  privado y un administrador debe compartirlo de forma deliberada.

Volver a desactivar el interruptor oculta la plantilla al resto de usuarios; no
toca las instancias ya creadas a partir de ella.

### Ciclo de vida y snapshots

- Acciones de encendido: arrancar, apagar, reiniciar, suspender, reanudar y
  apagado forzado.
- **Snapshots**: créalos desde la página de detalle de la VM (solo disco o con
  memoria), consulta el árbol con su historial, revierte o borra. Los snapshots
  de solo disco de una VM encendida son consistentes a nivel de sistema de
  ficheros cuando el agente invitado está disponible (se congela mediante el
  `qemu-guest-agent` preinstalado por cloud-init), y si no, se repliegan a un
  snapshot consistente ante caída.
- **Redimensionar disco**: amplía un disco con la VM apagada (cualquier cambio)
  o con la VM encendida (solo ampliar, y solo discos qcow2) — no hace falta
  parar nada para añadir espacio al disco de una VM en marcha.
- **Autoarranque**: marca las VMs que deben arrancar automáticamente con el
  host.

### Consola y gráficos

- **Consola VNC en el navegador**: desde la página de detalle de la VM (noVNC
  embebido). Se reconecta sola con backoff si se corta la conexión, y su propio
  panel lateral expone controles de calidad y compresión de imagen, un
  conmutador de cursor de punto y acciones de encendido (reiniciar, apagar,
  forzar apagado) sin salir de la consola.
- **Consola serie**: terminal embebida (80×24) que sobrevive a los reinicios del
  invitado.
- **SPICE/RDP**: descarga ficheros `.vv` (SPICE) o `.rdp` para clientes
  externos. La IP incrustada procede de `PUBLIC_HOST` o de la primera dirección
  que no sea de loopback.
- Todas las consolas embebidas (serie, terminal del host, VNC) se autentican con
  tickets de vida corta — nunca viajan credenciales de sesión de larga duración
  por la URL. El ticket de VNC está acotado a esa única VM y a las acciones
  relacionadas con la consola, y caduca al cabo de una hora (reutilizable dentro
  de esa ventana, porque la consola puede reconectarse y sus botones de
  encendido también lo necesitan), a diferencia de los tickets de un solo uso y
  30 segundos que usan las terminales serie y del host.

## 3.1. Contenedores (LXC)

WebKVM gestiona **contenedores LXC de forma nativa** (el instalador autónomo los
instala por defecto; ver [INSTALLATION.es.md](INSTALLATION.es.md) →
«Contenedores (LXC)»). Los contenedores comparten la misma interfaz que las VMs:

- **Lista unificada**: los contenedores aparecen junto a las VMs, cada tarjeta
  con una etiqueta `KVM`/`Incus` y una marca de aprovisionamiento; el filtro
  `Todo · VMs · Contenedores` acota la lista.
- **Creación**: el mismo formulario de «Crear», con el selector de tipo de
  instancia en **Contenedor (Incus / LXC)** — elige una distribución en el
  desplegable de imágenes (o «Personalizada / Otra…»), elige la red (los mismos
  bridges que las VMs) y define las **credenciales de Incus / LXC** (el usuario
  es opcional; si lo dejas en blanco, la contraseña se aplica a `root`). Sin
  ISOs y sin YAML manual.
- **Pool de almacenamiento**: el paso 3 del asistente («Recursos») ofrece un
  selector de **pool de almacenamiento** que solo lista los pools cuyo propósito
  es `container` (por defecto, `webkvm-incus`). Los pools de discos de VM, de
  ISOs, de backup y de plantillas nunca aparecen ahí — y los pools de
  contenedores nunca aparecen cuando creas una VM de KVM, porque libvirt e Incus
  gestionan mundos separados.
- **Detalle**: los contenedores admiten **redimensionar** el disco raíz,
  **añadir, quitar y cambiar** interfaces de red, consola serie y **métricas** de
  CPU y RAM en vivo. Los controles exclusivos de KVM (chipset/UEFI/TPM, VNC,
  CD-ROM, restablecer contraseña por agente invitado, clonar) quedan ocultos.

#### Mover un contenedor a otro pool de almacenamiento

Usa **«Mover almacenamiento»** en la barra lateral de la página de detalle del
contenedor. Reubica el rootfs — snapshots incluidos — en otro pool de propósito
`container` mediante la migración nativa de Incus, informando del progreso a
nivel de bytes.

- El contenedor **debe estar detenido**; una instancia en marcha se rechaza.
- El destino debe ser un pool `container` *distinto*.
- Los movimientos **entre hipervisores** (un pool de KVM ⇄ un pool de Incus) se
  rechazan por diseño. Exporta y vuelve a importar en su lugar.

> La pestaña **LXC** de la página de Almacenamiento no ofrece botón de mover
> deliberadamente. El rootfs de un contenedor es un volumen de tipo `container`
> que pertenece a su instancia, no un volumen `custom` desacoplable, así que se
> mueve desde la instancia — no desde la lista de volúmenes, donde no aparece.

## 4. Almacenamiento

- **Pools**: el backend gestiona sus propios pools bajo `/opt/webkvm/pools`:
  - `webkvm-disks` — discos de VM.
  - `webkvm-isos` — imágenes ISO.
  - `webkvm-incus` — rootfs de contenedores (cuando el módulo de contenedores
    está activo).
- Se pueden crear pools adicionales de tipo `dir` (local) o `netfs`
  (NFS/SMB/CIFS); los pools de propósito `iso` son de solo lectura para las
  operaciones de volumen.
- **Propósitos**: cada pool declara qué puede contener — `disk` (qcow2/raw de
  libvirt), `container` (rootfs de Incus), `iso`, `backup` o `template`. Un pool
  puede combinar varios. Los propósitos son el criterio por el que filtra cada
  selector de pool de la interfaz, de modo que un contenedor nunca puede acabar
  en un pool de discos de VM ni un qcow2 en la biblioteca de ISOs.
- **Mover almacenamiento**: un disco de VM se mueve desde **Almacenamiento →
  Discos → Mover**; el rootfs de un contenedor se mueve desde su propia **página
  de detalle de instancia** (ver «Contenedores (LXC)» más arriba). No es posible
  mover entre hipervisores.
- **Volúmenes**: crea, redimensiona y borra discos dentro de un pool, o **sube
  una imagen de disco existente** (`.qcow2`, `.img`, `.raw`, `.qed`)
  directamente a un pool — el fichero se transmite directo a disco y libvirt
  detecta su formato al refrescar, sin necesidad de conversión.
- **Biblioteca de ISOs**: sube, descarga (con progreso) y borra ISOs desde la
  web.

### Pools de red CIFS (SMB) con autenticación

Para montar un recurso compartido SMB3 con credenciales (por ejemplo, para
copias de seguridad):

1. **Almacenamiento → Nuevo pool**, tipo `netfs`, formato `cifs`, rellenando
   `source_host`, `source_dir`, `source_username` y `source_password`.
2. La contraseña se almacena como **secreto de libvirt** (la API nunca la
   devuelve; en disco solo queda su UUID).
3. Rota las credenciales actualizando el pool (debe estar detenido).
4. Si se reinstala libvirtd, recupera el pool enviando `cifs-needs-reauth: true`
   junto con las credenciales actuales.

## 5. Redes y firewall

### Bridges del host

WebKVM usa **bridges Linux reales a nivel de sistema operativo** (al estilo de
Proxmox); las redes virtuales de libvirt (`default`, `virbr0`, …) han
desaparecido.

- **L2 compartido `vmbr0`/`br0`** (por defecto, recomendado): las VMs y los
  contenedores salen a la LAN real y obtienen su IP del router (DHCP o
  estática).
- **NAT aislada `vmbr1`** (opcional): un bridge Linux sobre una interfaz `dummy`
  del kernel con `100.0.0.1/24`, DHCP de `dnsmasq` y `MASQUERADE`.
- Desde la página de **Redes** se pueden crear **bridges del host** adicionales
  con IP opcional y rango DHCP personalizado (inicio/fin, puerta de enlace,
  DNS), y activar el autoarranque por bridge.

### Firewall por VM

Desde la página de detalle de la VM:

- **Reglas de entrada** (nftables) para exponer puertos concretos.
- **Redirección de puertos** del host a la VM.

Las reglas se aplican de forma atómica con salvaguardas que evitan que te dejes
fuera del SSH.

## 6. Copias de seguridad

1. Crea un **destino** de copia (carpeta local o montaje NFS/SMB/SFTP).
2. Crea una **programación** (expresión cron) o lanza una a mano.
3. El runner produce un archivo por VM (`vm-<nombre>.tar.zst`) más un archivo de
   configuración (`config.tar.zst`), con verificación SHA-256 opcional y
   retención automática (conservar-últimos / conservar-días).
4. **Restauración**: restauración completa (con límites de tamaño y
   comprobaciones de rutas protegidas) o «restaurar como VM» (reimporta la copia
   como una VM nueva).
5. Los secretos (webhooks, SMTP, SFTP, CIFS) nunca se incluyen en las copias de
   configuración.

El progreso se ve en vivo en el centro de notificaciones y en **Backup →
Trabajos**.

## 7. Alertas y notificaciones

Configura notificaciones por **webhook (HTTPS)** o por **correo (SMTP)** para
eventos como:

- Una VM que se cae de forma inesperada.
- Poco espacio en disco.
- Éxito o fallo de una copia de seguridad.

## 8. Cuotas y planificación

- **Cuotas por usuario**: límites de número de VMs, vCPUs, RAM y disco — se
  aplican al crear, clonar, importar, restaurar y redimensionar. Los
  administradores están exentos.
- **Planificación de VMs**: encendido y apagado automáticos por cron (por
  ejemplo, apagar las VMs de pruebas por la noche).

## 9. Usuarios, roles y API

### Roles (RBAC)

| Rol | Capacidades |
|-----|-------------|
| **admin** | Todo: usuarios, ajustes, nodos, copias de seguridad y acciones destructivas. |
| **operator** | Crear, editar y borrar VMs, acciones de encendido, discos, redes, pools y copias. |
| **viewer** | Solo lectura. |

- Crea usuarios desde la pestaña **Usuarios** y asígnales rol y grupo.
- Cada usuario cambia su propia contraseña desde **Cuenta**.
- La contraseña inicial debe cambiarse en el primer inicio de sesión.

### Tokens de API

Desde **Cuenta → Tokens de API** puedes crear tokens de larga duración
(`wvmb_…`) para scripts. Úsalos con la cabecera
`Authorization: Bearer <token>`. Solo se almacena el hash SHA-256. Los tokens
caducan (30 días por defecto) y son revocables.

### Log de auditoría

Toda acción sensible queda registrada en `/opt/webkvm/audit.log` (JSONL) con
usuario, rol, acción, recurso e IP de origen.

## 10. Appliances de la comunidad

El diálogo **Apps de la comunidad** despliega VMs listas para usar a partir de
imágenes cloud oficiales (Ubuntu, Debian, Rocky, CentOS Stream, Fedora, Arch,
Alpine, openSUSE), además de appliances llave en mano (Home Assistant OS,
OpenWrt, OPNsense) y aplicaciones de un clic que se instalan en el primer
arranque sobre Ubuntu 24.04 (WordPress, Nextcloud, Odoo, Moodle).

Elige usuario y contraseña (obligatorios para las imágenes cloud-init), la red de
destino y el nombre de la VM; el despliegue se ejecuta como trabajo en segundo
plano con progreso en vivo.

## 11. Logs y resolución de problemas

```bash
systemctl status webkvm           # estado del servicio
journalctl -u webkvm -f           # logs en vivo
cat /opt/webkvm/logs/backend.log  # cuando WEBKVM_LOG_FILE está activo
```

| Síntoma | Solución |
|---------|----------|
| El servicio no arranca | `systemctl status webkvm` + `journalctl -u webkvm -n 100` |
| Falta `/dev/kvm` | Activa la virtualización en la BIOS o la virtualización anidada |
| Aviso de certificado en el navegador | Descarga el certificado de `/api/system/cert` y confíalo |
| No conecta con libvirt | Comprueba `systemctl status libvirtd`; el backend se ejecuta como root |
| No se puede crear una VM desde ISO | Sube antes la ISO en **Almacenamiento** y después adjúntala |
| La consola serie dice que la VM está apagada | Arranca la VM; la consola se reconecta sola en el siguiente intento |
