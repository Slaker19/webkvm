# Changelog

Todos los cambios notables de este proyecto se documentan en este
fichero, siguiendo [Keep a Changelog](https://keepachangelog.com/es/1.1.0/)
y [Semantic Versioning](https://semver.org/lang/es/).

## [Unreleased] — Explorador de subcarpetas remotas NFS/SMB

### Fixed

- **Revisión de Firewall y Settings (capturas + inspección de código)**:
  - Settings decía "12 wired settings across **4** tabs" habiendo
    realmente **5** (Server, Auth, Logging, Network, Notifications):
    `tabs` en `Settings.svelte` añade la pestaña "Notifications" a mano
    por encima de `sections` (que solo agrupa los campos del esquema
    del backend, sin incluirla), pero el subtítulo seguía contando
    `sections.length`. Corregido a `tabs.length`.
  - Firewall tenía un tooltip ("Always open" en los puertos de gestión
    siempre-abiertos) escrito directamente en inglés en el `.svelte` en
    vez de usar `t(...)` — no se traducía al cambiar de idioma. Movido a
    una clave i18n nueva (`firewall.protectedPortTooltip`) en los 3
    idiomas.
  - Comprobado a fondo (capturas + inspección del DOM real) que el
    formulario de Notificaciones SÍ tiene sus botones "Save"/"Send test
    alert" y su historial de alertas — parecía "cortado" en una captura
    a pantalla completa porque esa sección vive dentro de un `<main
    overflow-y-auto>` interno, no del `<body>`; las herramientas de
    captura de pantalla completa no expanden ese contenedor interno,
    pero un usuario real simplemente hace scroll dentro de él con
    normalidad. Descartado como falso positivo tras verificarlo
    manualmente (no se tocó código).

- **Tabla de Redes rota visualmente (nombre y acciones superpuestos) por
  debajo de ~1200px de ancho**: la columna "Name" no tenía ancho mínimo
  (`1fr` puro, colapsaba a 0) y la columna de acciones usaba
  `width: 'auto'`, que en `DataTable.svelte` se traduce en un track CSS
  Grid `auto` — confirmado en vivo inspeccionando los tracks calculados
  del navegador: bajo presión de espacio, ese track se encogía a **24px**
  (apenas un icono), y los 4 botones de acción (parar/ver leases/editar/
  borrar) se renderizaban superpuestos sobre la columna DHCP en vez de
  recortarse o hacer scroll. Arreglado dándole a "Name" un mínimo real
  (`minmax(180px, 1fr)`) y a "Actions" un ancho fijo (`160px`) acorde a
  sus 4 botones — por debajo de ese ancho la tabla ahora hace scroll
  horizontal limpiamente (mismo mecanismo `overflow-x-auto` que ya tenía
  `DataTable`, que hasta ahora nunca se activaba por este bug) en vez de
  corromperse visualmente. Verificado con capturas reales a 1400/1024/768px.
- **Los leases DHCP de la red NAT (vmbr1) no aparecían nunca**: su
  fichero de configuración de dnsmasq no tenía la línea
  `dhcp-leasefile=` (solo la tienen las redes creadas o re-guardadas
  desde que existe esa función — limitación de migración ya documentada
  en su momento), así que sus leases reales iban a parar al fichero
  global por defecto del sistema (`/var/lib/misc/dnsmasq.leases`) en vez
  de a su propio fichero por-bridge, y el endpoint de leases (que sí lee
  el fichero por-bridge) siempre veía la lista vacía. Arreglado
  re-guardando la configuración DHCP de vmbr1 (mismo camino que ya existe
  para editar una red — `PUT /api/networks/vmbr1` regenera el conf con
  `dhcp-leasefile=` y reinicia su dnsmasq), y forzando una renovación
  DHCP en un cliente real para verificar que el lease aparece de
  inmediato. Cualquier otro bridge creado antes de esta función necesita
  el mismo re-guardado una vez (editar y guardar sin cambios basta).

- **Detalle de VM/LXC — 4 fallos encontrados con capturas de pantalla
  reales (Playwright) contra la instancia en vivo**:
  - **Cada interfaz de red mostraba la lista COMPLETA de IPs de la
    VM/contenedor, no solo la suya**: con más de una NIC (comprobado con
    3 en un contenedor Incus y 3 en una VM KVM), las 3 filas de la
    pestaña "Network Interfaces" mostraban exactamente la misma badge
    con las 3 IPs juntas, en vez de una IP por fila. Causa: tanto
    `domainToVM` (KVM) como `instanceToVM`+estado (Incus) recogían todas
    las IPs en una sola lista plana (`vm.ips`) sin conservar a qué
    interfaz pertenecía cada una; el frontend pintaba esa misma lista en
    cada fila. Arreglado añadiendo `NetIface.IPs` (nuevo campo) y
    rellenándolo por MAC en ambos backends (`libvirt` ya agrupaba las
    direcciones por interfaz vía `iface.Hwaddr`, simplemente se
    descartaba; Incus expone lo mismo vía `InstanceStateNetwork.Hwaddr`),
    y el frontend ahora usa `iface.ips` en vez de la lista global.
    Verificado en las dos VMs reales usadas para las capturas.
  - **Los contenedores Incus mostraban los botones RDP/SPICE**, que no
    tienen sentido para un LXC (sin canal gráfico ni protocolo de
    escritorio remoto — eso es exclusivo de VMs KVM). Ocultos ahora
    detrás de `!isContainerVm`, igual que ya se hacía con "Open Console".
  - **Cadenas en español hardcodeadas apareciendo aunque el idioma de la
    interfaz fuera inglés**: el título de la tarjeta de consola serie
    ("Consola serial") y el botón "Credenciales de la app" estaban
    escritos directamente en español en el `.svelte`, en vez de usar
    `t(...)`. Movidos a claves i18n nuevas (`vmDetail.serialConsoleTitle`,
    `vmDetail.appCredentials`, y de paso `vmDetail.serialConsole`/
    `resetPassword`/`resetPasswordHint`, que estaban hardcodeados en
    inglés y por tanto no se traducían al cambiar de idioma) en los 3
    idiomas.
  - **Un contenedor Incus en marcha mostraba "—" en Uptime para
    siempre**: solo las VMs KVM calculaban el tiempo activo (desde
    libvirt); el backend de Incus nunca rellenaba `uptime_sec`. Ahora se
    deriva de `InstanceState.StartedAt`, que Incus ya expone.

- **Los contenedores Incus no recibían IP al añadir una interfaz de red
  (sí funcionaba en las VMs KVM)**: diagnosticado en vivo contra un
  contenedor Debian real (`xd`) — `AttachNetworkIface` solo regeneraba el
  `user.network-config` (netplan de cloud-init) del contenedor si esa
  clave YA existía; un contenedor creado sin rellenar los campos de
  cloud-init nunca la tenía, así que una segunda/tercera NIC quedaba
  levantada a nivel de enlace pero sin IP para siempre. Peor aún: este
  contenedor concreto ni siquiera usa cloud-init — usa systemd-networkd
  con una unidad `/etc/systemd/network/eth0.network` fija que solo cubre
  `eth0`, algo que el mecanismo de cloud-init de WebKVM nunca podía
  arreglar en absoluto. Solución de dos partes: (1) `CreateDomain` y
  `AttachNetworkIface`/`DetachNetworkIface` ahora regeneran
  `user.network-config` siempre, tenga o no el contenedor cloud-init
  configurado, para los invitados que sí lo usan; (2)
  `afterAttachConfigureGuestNetwork` (nuevo), al adjuntar una NIC a un
  contenedor en marcha, además pide una concesión DHCP inmediata dentro
  del invitado (`dhclient`/`udhcpc`, lo que exista) para que la IP
  aparezca al momento sin esperar a un reinicio, y si detecta que el
  invitado usa systemd-networkd (mirando si ya hay alguna unidad en
  `/etc/systemd/network/`), añade una unidad a juego para la nueva
  interfaz para que la IP también sobreviva a un reinicio — limpiada de
  nuevo al desmontar la interfaz. Nota técnica descubierta en vivo: una
  operación `exec` de Incus se reporta como "Success" aunque el propio
  comando ejecutado termine con código de salida distinto de cero (el
  código real va en `metadata.return`), así que el código nuevo comprueba
  ese campo en vez de fiarse solo de si la operación en sí falló.
  Verificado end-to-end contra el contenedor real: adjuntar una interfaz
  a `vmbr1` le dio IP DHCP real de inmediato y dejó una unidad
  systemd-networkd persistente; al desmontarla, la unidad se limpió.

### Added

- **Explorador de subcarpetas** en los formularios de creación de pool de
  almacenamiento y de destino de backup NFS/SMB: tras indicar servidor +
  export/recurso compartido, un botón "Explorar carpetas" permite navegar
  paso a paso (entrar/subir de nivel) por las subcarpetas reales del
  recurso remoto y elegir una, sin depender de autocompletado del
  navegador. Nuevo paquete independiente `backend/internal/remotebrowse`
  (no depende de `internal/libvirt` ni `internal/backupstore`, y no es
  usado por ellos) tras el endpoint único `POST
  /api/storage/browse-remote` (admin-only, mismo nivel que crear un pool,
  ya que contacta hosts arbitrarios indicados por el operador). Para SMB
  usa el binario `smbclient` (nuevo en el instalador: `smbclient` en
  apt/pacman, `samba-client` en dnf — nombre de paquete Arch verificado
  en vivo contra la VM de pruebas) sin necesidad de montar nada. Para
  NFS, al no existir forma ligera de listar un export, se hace un
  montaje temporal real de solo lectura bajo `/run/webkvm` (no
  `/etc/webkvm`, reservado a credenciales persistentes), se lista con
  `os.ReadDir` y se desmonta — nunca la maquinaria de unidades systemd
  que sí usan los montajes persistentes de pools/destinos, innecesaria
  para una operación que vive solo dentro de una petición HTTP. Verificado
  en vivo end-to-end contra los recursos reales del Proxmox
  (`smb-seagate`/`nfs-seagate`): listado SMB en la raíz y un nivel
  dentro, fallo de autenticación, host inalcanzable, y subcarpeta
  inexistente (smbclient no falla el proceso en ese caso: se detecta
  explícitamente la línea `cd ... NT_STATUS_OBJECT_NAME_NOT_FOUND` en la
  salida en vez de fiarse del código de salida); listado NFS feliz
  probado contra una exportación temporal desechable en la propia VM de
  pruebas (para no tocar los permisos de la exportación real de
  Proxmox), confirmando el ciclo montar/listar/desmontar sin dejar
  montajes ni directorios temporales huérfanos.

### Fixed

- **Pestaña "Trabajos" de Backups se quedaba en blanco** al ver un job con
  más de una VM: `vmNameOf()` extraía el nombre de VM de cada archivo con
  una regex que asumía que el hostname no lleva guiones (`webkvm-[^-]+-...`)
  — el hostname real de la VM de pruebas, `wk-arch`, sí lleva uno, así que
  la regex nunca casaba y cada VM del job resolvía a `null`; Svelte 5
  lanza un error fatal (`each_key_duplicate`) al ver varias claves `null`
  repetidas en el `{#each}` con clave, y esa excepción dejaba en blanco
  toda la pestaña. Arreglado cambiando el segmento del hostname a
  coincidencia voraz (`.+`) en `vmNameOf` y en `runSuffixOf` (mismo fallo
  en el agrupado de la pestaña Archivos), y además la lista de VMs por
  job ahora usa el nombre de fichero (siempre único) como clave en vez
  del nombre mostrado, para que dos VMs con el mismo nombre tampoco
  puedan volver a producir este mismo choque. Reproducido y verificado
  con un navegador real (Playwright headless) contra la instancia de
  pruebas: antes del arreglo, `each_key_duplicate` aparecía al abrir la
  pestaña; después, los 4 trabajos se listan con sus insignias de VM
  correctamente.
- **Robustez de `pollJobs()` en Backups**: si el job en curso
  (`activeBackup`/`activeRestore`) desaparecía por completo de la lista
  devuelta por `/api/backup/jobs` (p. ej. tras reiniciar el backend a
  media ejecución), el código no hacía nada — ni paraba el sondeo ni
  avisaba — dejando un sondeo de fondo indefinido. Ahora, si el job
  rastreado no aparece, se trata como fallido, se limpia el estado y se
  detiene el sondeo.
- **Widget "Copias" de la página Host exigía un montaje fijo en
  `/mnt/webkvm-backup`**: el backup rápido de configuración del host
  (`scripts/webkvm-backup.sh`, endpoints `/api/system/backup` y
  `/api/system/backups`) sólo sabía mirar esa ruta convencional, montada
  a mano vía `/etc/fstab` — completamente ajeno al pool de almacenamiento
  o destino de backup NFS/SMB que el operador ya hubiera configurado con
  el auto-montaje de esta sesión. Ahora, si `/mnt/webkvm-backup` no está
  montado, se reutiliza automáticamente el primer destino de backup
  NFS/SMB ya montado (prioridad sobre los pools, por ser el sitio natural
  para este tipo de fichero) o, si no hay ninguno, el primer pool de
  almacenamiento cuya ruta esté realmente montada — comprobado con el
  mismo binario `mountpoint` que el propio script ya exigía, nunca
  adivinado por convención de nombre. De paso, se corrigió un fallo real
  en `webkvm-backup.sh`: su cadena de reserva para el hostname
  (`hostname -s` → `hostname` → `unknown`) sólo silenciaba stderr en el
  primer intento, así que en un sistema sin el binario `hostname` en
  absoluto el "command not found" del segundo intento contaminaba la
  salida y rompía el parseo JSON en el backend; se añadió `uname -n` como
  reserva intermedia y se silenció stderr en todos los intentos.
  Verificado en vivo end-to-end: creación y listado del backup de
  configuración usando el destino `smb-backups-seagate` ya montado, sin
  tocar `/etc/fstab`.

## [2.5.1] — DNS/leases DHCP en Redes + página Host ampliada (2026-09-11)

### Added (adicional)

- **Destinos de backup NFS/SMB: WebKVM puede montarlos él mismo**, igual
  que para los pools de almacenamiento. Antes se asumía siempre que el
  operador ya tenía la ruta montada a mano (vía fstab/systemd); ahora,
  si el formulario de "Nuevo destino" NFS/SMB lleva un servidor
  relleno, WebKVM monta el recurso él mismo (unidad `systemd .mount`,
  sobrevive a reinicios; para SMB con usuario, credenciales en fichero
  solo-root — nunca en `targets.json` ni en la línea de montaje). Dejar
  el servidor vacío mantiene el comportamiento de siempre (ruta ya
  montada por el operador). Implementado de forma independiente de
  libvirt (`backupstore/net_mount.go`) ya que un destino de backup no es
  un pool de almacenamiento. Borrar el destino desmonta y limpia todo.
  Verificado en vivo end-to-end contra los mismos recursos NFS/SMB
  reales del Proxmox: destino NFS y destino SMB con usuario/contraseña,
  ambos montados de verdad (`mount` lo confirma), "Probar destino"
  funcionando en el SMB (webkvm corre como root, y el montaje SMB usa
  `uid=0`). El destino NFS, en cambio, dio "permission denied" al
  probarlo — **comportamiento correcto, no un bug**: esa misma
  exportación se restringió antes en esta sesión a solo el usuario
  `alvin` (sin `no_root_squash`), así que root queda igual de bloqueado
  ahí que cualquier otro usuario sin permiso; para que un backup pueda
  escribir por NFS hace falta apuntar a una exportación donde root
  tenga (o quede mapeado a) permiso de escritura.

### Fixed (adicional)

- **No se podía borrar un pool que hubiera quedado "inactive"** (p. ej. un
  pool `netfs` cuyo montaje remoto falló o se cayó): el guard de "¿tiene
  volúmenes con VMs adjuntas?" en `DeletePool` solo toleraba un error de
  `ListStorageVolumes` que contuviera "not found", no "not active" —
  cualquier pool inactivo devolvía 500 y quedaba imposible de borrar por
  la API/UI. Ahora también se tolera "not active" (un pool inactivo no
  puede tener adjuntos reales de todas formas).

### Descartado tras probarlo en vivo

- **Intento de "montar como usuario" para NFS (mapeo UID/GID por
  pool)**: se implementó vía `<mount_opts>` de libvirt
  (`-o uid=,gid=`) pensando en dar a NFS algo parecido al campo de
  usuario de SMB. Probado en vivo contra un montaje NFSv4 real: el
  propio cliente NFS del kernel **rechaza** `uid=`/`gid=` combinado con
  `vers=4.2` con "Invalid argument" — esas opciones son exclusivas de
  NFSv2/v3, no existen en NFSv4 (que es la versión que usamos en todos
  los montajes de esta sesión). Sin forzar la versión, el intento de
  compensar caía a v3 y colgaba con "Connection timed out" (los puertos
  extra de NFSv3 — rpcbind/mountd — no estaban abiertos). Revertido por
  completo (modelo, XML, formulario, i18n) en vez de dejar una función
  que rompe montajes que antes funcionaban. El control de acceso real
  por usuario/grupo en NFS sigue siendo, como siempre, cosa del
  servidor remoto (permisos Unix + opciones de export), no algo que el
  formulario de montaje del cliente pueda forzar.

### Added

- **Tarjetas visuales para los pools de almacenamiento**: la lista vertical
  de "Grupos de almacenamiento" pasa a una cuadrícula de tarjetas (icono,
  nombre, insignia ISO/VDI, ruta, barra de progreso), responsive (1
  columna en móvil, hasta 3 en escritorio).
- **Montaje real de pools NFS/SMB desde la propia app**, reactivando una
  funcionalidad que se había desactivado a propósito: el formulario de
  "Nuevo Pool" ahora tiene un selector "Directorio local" / "Recurso de
  red (NFS/SMB)"; para NFS/SMB pide servidor, ruta de exportación remota,
  punto de montaje local y, para SMB, usuario/contraseña opcionales.
  Para NFS y para SMB anónimo (sin usuario), WebKVM delega en el driver
  `netfs` de libvirt tal cual — no hace falta tocar `/etc/fstab` a mano.
  **Para SMB con usuario/contraseña, el mecanismo es otro**: probando en
  vivo contra un recurso SMB real que exige login, el primer intento
  (credenciales como secreto de libvirt, heredado de una versión
  anterior) montaba como invitado y fallaba con "Permission denied" —
  confirmado contra el propio esquema RelaxNG de libvirt instalado
  (`storagepool.rng`): el elemento `<auth>` de un pool `netfs` solo
  admite los tipos `chap` (iscsi) y `ceph`, nunca `cifs`; probablemente
  la razón real por la que esto se desactivó en su día. Para SMB
  autenticado, WebKVM monta el recurso él mismo vía una unidad systemd
  `.mount` propia (nombrada según el path, sobrevive a reinicios) más un
  fichero de credenciales solo-root (`/etc/webkvm/smb-creds-<pool>`,
  0600) — la contraseña nunca pasa por libvirt — y define un pool `dir`
  normal sobre el punto de montaje ya autenticado; borrar el pool
  desmonta la unidad y borra las credenciales. Añadidos
  `nfs-common`/`cifs-utils` (apt), `nfs-utils`/`cifs-utils` (dnf/pacman)
  al instalador, y `virtsecretd.socket` habilitado donde hiciera falta
  (daemon "split" de libvirt para secretos, parado por defecto en Arch).
  Verificado en vivo end-to-end contra un disco físico real en el host
  Proxmox: export NFS real y recurso SMB real con usuario/contraseña,
  ambos montados y con ficheros de prueba visibles a través del punto de
  montaje, `mount`/`systemctl status` confirmando el montaje SMB real
  (`username=alvin` en las opciones de montaje activas), `device_id`
  correctamente distinto al del disco local en ambos casos (no se
  mezclan en los totales agregados de Storage), y borrado de ambos pools
  desmontando limpiamente. Validación de credenciales CIFS incompletas y
  de host/ruta de NFS faltantes confirmadas con 400 claros.

- **Mínimo real de contraseña bajado a 8 caracteres**, alineado con lo que
  el propio formulario ya anunciaba ("Password (min 8)") — antes el
  backend exigía silenciosamente 12, así que una contraseña que la UI
  sugería como válida se rechazaba igual. Se mantiene la exigencia de
  "3 de 4 clases de carácter" por debajo de 16 caracteres.
- **Formulario de "Nuevo usuario" mucho más completo**: ahora es un
  diálogo con confirmación de contraseña, casilla "exigir cambio de
  contraseña en el primer inicio de sesión" (nuevo `must_change_password`
  en `CreateUserRequest`, activada por defecto), lista blanca de pools y
  de etiquetas, y cuota completa (VMs/vCPUs/RAM/disco + límite por pool)
  — todo lo que ya existía en el diálogo de edición, ahora también al
  crear, en vez de crear-y-editar en dos pasos.

- **DNS personalizado por DHCP** en redes `nat`/`isolated`, en creación y
  edición (`dns []string`, validado como lista de IPs).
- **Lista de leases DHCP activos** por red: `GET /api/networks/{id}/leases`
  y `DELETE /api/networks/{id}/leases/{mac}?ip=...` (libera el lease con el
  binario real `dhcp_release`, nunca editando el leasefile a mano). Panel
  nuevo en `Networks.svelte` con conteo agregado de leases activos.
- **Página Host/System ampliada**: tarjeta de almacenamiento agregado del
  host, tarjeta de carga y tiempo activo (`/proc/loadavg`, `/proc/uptime`),
  tabla de servicios systemd (libvirtd/virtqemud, incus, un dnsmasq por
  bridge con DHCP activo) y tarjeta de plataforma del hipervisor (kernel,
  versión de QEMU y de libvirt, versión de Incus, virtualización anidada,
  IOMMU/VFIO).

### Fixed

- **Cada bridge con DHCP ahora tiene su propio fichero de leases**
  (`dhcp-leasefile=`) — antes todas las instancias de dnsmasq compartían en
  silencio el fichero por defecto del sistema y se pisaban los leases entre
  sí.
- **Versión de QEMU/libvirt intercambiadas**: para el driver QEMU,
  `conn.GetVersion()` devuelve la versión del hipervisor (QEMU), no la de
  libvirtd — confirmado en vivo contra `virsh version`. La tarjeta de
  plataforma nueva ya lee cada versión de la llamada correcta.
- **Liberar un lease devolvía 500 con cualquier MAC real**: el router
  `chi` conserva el segmento de ruta con el `:` de la MAC todavía
  porcentaje-escapado (`%3A`) cuando el cliente lo codifica — se aplica
  `url.PathUnescape`, el mismo arreglo que ya usa `DetachNetworkIface`.
- **Borrar una red con una VM/contenedor todavía conectado devolvía 500**
  en vez de 409 — el guard en sí funcionaba bien (rechazaba el borrado con
  el mensaje correcto), solo el código HTTP era el de un error de
  servidor genérico. Nuevo sentinel `compute.ErrNetworkInUse` mapeado a
  409, mismo patrón que ya usan los conflictos de estado de VM.
- **Liberar un lease inexistente devolvía 200** en vez de un error: el
  binario `dhcp_release` manda el paquete DHCPRELEASE y sale con código 0
  tanto si la dirección estaba realmente arrendada como si no — no hay
  forma de distinguirlo desde su propio código de salida. Ahora se
  comprueba el fichero de leases antes de invocarlo y se devuelve 404
  (`compute.ErrLeaseNotFound`) si no hay ningún lease activo con esa
  pareja IP/MAC.
- **La lista de "Servicios del sistema" mostraba libvirt como "detenido"
  aunque funcionase perfectamente**: en distros con daemons "split"
  (Arch, Fedora recientes), `libvirtd.service` sigue instalado pero
  inactivo (activado por socket solo por compatibilidad) mientras el
  daemon que de verdad hace el trabajo es `virtqemud.service`. El
  comprobador se quedaba con el primer candidato *encontrado*, no con el
  primero *activo* — ahora prefiere el que esté realmente activo.
- **CSRF token inválido tras cerrar y reabrir el navegador** (visible como
  `[error obteniendo ticket: invalid csrf token]` en cualquier acción que
  pidiera un ticket — consola serie, terminal del host, etc.): la cookie
  CSRF no llevaba `MaxAge`, así que el navegador la trataba como cookie de
  sesión (se borra al cerrar el navegador) mientras la cookie de sesión
  JWT sí persistía — quedando una sesión "aparentemente conectada" sin
  CSRF válido. Ahora la cookie CSRF usa el mismo `MaxAge` que la de
  sesión. Para una sesión ya afectada (creada antes de este arreglo) no
  hay forma segura de recuperarla sin relogin — `/api/auth/refresh`
  exige la misma pareja CSRF que ya falta, así que reintentarlo
  automáticamente no sirve; en su lugar, ese error ahora redirige a
  `/login?reason=session_expired` en vez de dejar un mensaje críptico en
  el panel de terminal.
- **El aviso "tu sesión ha caducado" nunca se mostraba** en la página de
  login (para ningún motivo, no solo el CSRF de arriba): al router de
  hash le faltaba la propia ruta `login` en su tabla, así que
  `#/login?reason=...` caía en el fallback de "ruta no encontrada", que
  descarta la query string — `getRoute()?.query?.reason` era siempre
  `undefined`.
- **Un `/assets/*.js` que ya no existe (bundle antiguo en una pestaña
  abierta durante una actualización) devolvía 200 con el HTML del propio
  SPA** en vez de un 404 real — el navegador intenta parsear ese HTML
  como módulo JS y falla en silencio, sin ningún error visible ("no me
  carga nada"). `assets/` es siempre una ruta de build con hash de
  contenido, nunca una ruta de cliente, así que un fallo ahí ahora es un
  404 real; el fallback a `index.html` para rutas de cliente genuinas
  (recargar `/vms/abc-123`) sigue funcionando igual.
- **Las tarjetas de `/vms` desbordaban la lista de IPs fuera del borde**
  cuando una VM en marcha tenía varias direcciones: el contenedor no
  encogía ni truncaba. Ahora usa elipsis con el listado completo en un
  tooltip.
- **Ningún toast (éxito o error) se mostraba nunca, en toda la
  aplicación** — la causa real de "no funciona la creación de usuarios":
  al fallar la contraseña por política (p. ej. "es una versión decorada
  de una contraseña común"), el fallo era real pero completamente
  invisible, indistinguible de que el botón no hiciera nada.
  `Toaster.svelte` capturaba la lista de toasts UNA SOLA VEZ al montar
  (`const toasts = getToasts()`) en vez de leerla de forma reactiva —
  como el store reasigna el array en cada aviso nuevo en lugar de
  mutarlo, ese snapshot nunca se actualizaba. Confirmado con un
  `MutationObserver` sobre toda la página durante 4s: cero nodos de
  toast, aunque la llamada a `toast.error(...)` sí ocurría. Arreglado con
  `$derived(getToasts())`.
- **El resumen de cuota de un usuario mostraba literalmente
  `{max_ram_mb}`/`{max_disk_gb}`** en vez de un número cuando solo se
  habían fijado VMs/vCPUs: el backend omite del JSON los campos de cuota
  en cero (`omitempty`), así que `t('users.quotaSummary', row.quota)`
  recibía un objeto sin esas claves y `t()` deja el `{placeholder}` tal
  cual cuando no encuentra la clave. Se normaliza a `0` antes de pasarlo
  a `t()`.
- **La tabla de usuarios se desalineaba fila a fila** cada vez que una
  fila tenía menos botones de acción que las demás (la fila del propio
  admin conectado no tiene botón "Delete") — la columna de acciones usaba
  `width: 'auto'` (una pista de grid dimensionada por su contenido), así
  que esa fila con menos contenido dejaba más espacio libre para la única
  columna flexible (`username`, sin `width` = `1fr`), desplazando el
  resto de columnas de esa fila hacia la derecha respecto a todas las
  demás. Fijada a un ancho fijo en píxeles.
- **La insignia de rol "Viewer" era invisible**: usaba `bg-muted` (sólido)
  en vez de una capa translúcida como las de "Admin"/"Operator"
  (`bg-accent/10`/`bg-info/10`), y ese color sólido es casi idéntico al
  fondo de la propia fila — el texto "Viewer" aparecía sin ninguna
  cápsula visible, icono, ni contraste. Cambiado a
  `bg-muted-foreground/10` y añadido un icono de ojo, a la par de las
  otras dos insignias.
- **El "Total" de Almacenamiento sumaba el mismo disco varias veces**
  cuando dos pools (típicamente uno de ISOs y otro de discos de VM)
  viven en el mismo filesystem — el backend de libvirt para pools tipo
  `dir` calcula `Capacity`/`Allocated`/`Available` con `statvfs()` sobre
  el filesystem subyacente, **no** son cifras propias de cada pool.
  Confirmado directamente con `virsh pool-info` (sin pasar por nuestro
  código): un pool completamente vacío reportaba la misma `Allocation`
  que otro pool en el mismo disco con varios GB de discos qcow2 reales,
  ambos iguales al uso real del disco entero. Nuevo campo `device_id`
  en `StoragePool` (el `st_dev` de `stat()` sobre el path del pool);
  `Storage.svelte` ahora sólo cuenta el disco compartido una vez al
  sumar tanto el total como el usado. Antes de este arreglo, con dos
  pools de 15.7GB en el mismo disco de 15.7GB reales, "Total" mostraba
  ~31.4GB y "usado" podía superar el 100% (149% en la VM de pruebas);
  después, ambos coinciden con `df`.

Verificado en vivo end-to-end contra la VM Arch de pruebas: DNS
personalizado reflejado en la conf de dnsmasq generada, un cliente DHCP
real (network namespace) obtuvo y liberó un lease a través de la API/UI,
las tarjetas nuevas de Host coinciden con `virsh version`/
`cat /proc/loadavg`/`uptime -p`/`systemctl show`, la suite Playwright
completa (123 comprobaciones: login, CRUD de usuarios/grupos/tokens,
storage, redes, ciclo de vida completo de VM, consola serie/VNC, backup,
firewall, nodos, status) pasa 123/123 tras cada arreglo, y el escenario
exacto de la cookie CSRF (simulando el cierre del navegador) se reprodujo
y se confirmó arreglado con Chromium headless.

## [2.5.0] — Unificación de redes NAT/aislada/directa (2026-09-11)

### Added

- **Los 3 tipos de red, unificados en un solo endpoint**: `/api/networks`
  ahora soporta `kind` = `nat` (subred con salida real a Internet, vía dos
  cadenas nftables nuevas `nat_bridges`/`nat_bridges_forward`), `isolated`
  (subred sin Internet) y `direct` (adaptador real → bridge con nombre
  propio, como el `vmbr0` del host) — los tres funcionan igual para KVM e
  Incus. Se elimina `/api/host/bridges` (toda su funcionalidad pasa a ser
  `kind=direct` de `/api/networks`).
- **Rango DHCP configurable** (`dhcp_start`/`dhcp_end`) para redes
  `isolated`/`nat` — antes siempre se calculaba automáticamente a partir del
  CIDR, ignorando estos campos (que ya existían en el modelo pero ningún
  código los leía).
- **`DeleteNetwork` real** para los 3 tipos: libera cualquier NIC física
  esclavizada (restaurando su IP) y retira la regla NAT si la tenía, en un
  solo camino de código.
- Paquete nuevo `internal/netstore`: persiste qué tipo es cada bridge creado
  por la API, ya que el estado del kernel por sí solo no distingue
  "aislada" de "NAT con la regla borrada a mano".

### Fixed

- **3 bugs reales** encontrados probando en vivo la antigua función
  "Host bridges" (`/api/host/bridges`, ahora eliminada): (1) la NIC
  esclavizada podía quedar en NO-CARRIER porque nunca se hacía
  `ip link set <iface> up` tras esclavizarla; (2) un bridge creado con una
  NIC física **nunca se podía borrar** — el guard de borrado confundía la
  NIC física esclavizada a propósito con una interfaz de VM/contenedor
  todavía conectada; (3) al borrar, la NIC liberada se quedaba sin IP (sin
  ningún restablecimiento). Verificado en vivo contra la VM de pruebas
  Arch: se creó un bridge `direct` sobre una NIC libre, quedó con conectividad
  real a la LAN sin ningún paso manual, y se borró correctamente por la API.
- **Bridge principal mal protegido**: `IsManagedBridge` solo reconocía el
  nombre `"br0"`, pero el instalador real usa `vmbr0` por defecto — el
  bridge principal del host no estaba protegido contra borrado. Ahora
  reconoce `vmbr0`/`br0` y, dinámicamente, cualquier bridge que lleve
  actualmente la ruta por defecto del host.

## [2.4.1] — Hardening tras QA exhaustivo (2026-09-11)

### Added

- **Clone e instantáneas asíncronos**: `POST /api/vms/{id}/clone` y
  `POST /api/vms/{id}/snapshots` responden **202 + `{job}`** al instante
  en lugar de bloquear el handler durante la copia del qcow2; el cliente
  hace polling de **`GET /api/jobs/{id}`** hasta el estado terminal
  (`done`/`error`). Las cuotas y ACL siguen validándose de forma síncrona
  (fallo rápido 4xx). El CLI espera el job y sigue reportando el id nuevo.
- **`safego.Recover`**: recuperación de pánicos en las 19 goroutines de
  fondo (backup, consolas, eventos libvirt, jobs…) para que un pánico en
  segundo plano no tumbe todo el proceso. En pruebas capturó dos
  nil-derefs reales de la consola serial.

### Fixed

- **Borrado de bridges del host**: `DELETE /api/host/bridges/{name}`
  devolvía siempre 409 (el guard "en uso por una red libvirt" coincidía
  con cualquier bridge en el modelo v2.4). Ahora comprueba los puertos
  reales del bridge y, al borrar, detiene/elimina la unit y config de
  dnsmasq y el puerto dummy.
- **Operaciones en estado incorrecto** (`forceoff` con la VM apagada,
  `resume` sin pausa, `start` ya en marcha) devolvían 500; ahora **409**.
- **Renombrar una VM** fallaba siempre con 500 (`DomainDefineXML` con el
  mismo UUID). Ahora usa `dom.Rename` (solo con la VM apagada; en marcha
  409) y `KVMBackend.UpdateDomain` pasa por `kvmErr`.
- **Discos huérfanos al renombrar**: borrar una VM renombrada con
  `?disks=true` dejaba su disco (`<nombre-viejo>.qcow2`) en el pool. Ahora
  el borrado usa los nombres reales de disco del dominio.
- **`effect_update_depth_exceeded` en la página de Instantáneas**: la
  paginación de `DataTable` calculaba `NaN` (`ceil(0/0)`) con `pageSize=0`
  y 0 filas, y el `$effect` escribía `page=NaN` en bucle (NaN≠NaN), lo que
  rompía **toda la navegación in-page** hasta recargar. Corregido el
  cálculo y la escritura condicional.
- **URL corrupta al navegar**: `VmList` sincronizaba sus filtros con
  `history.replaceState` también durante el teardown de la ruta,
  reescribiendo el hash (p.ej. `#/storage` → `#/vms`) sin `hashchange`.
  Ahora solo actúa cuando la ruta activa es `/vms`.
- **Consola serial**: guards nil-safe en `stream.Recv/Send/Finish/Free`
  (un stream nil tras un reacquire fallido provocaba nil-deref).

## [2.4.0] — Arquitectura de red unificada estilo Proxmox (2026-09-08)

### Added

- **Modelo de red único y agnóstico**: WebKVM solo busca, lista y usa
  **puentes Linux reales del SO** (`vmbr0`, `vmbr1`, `vmbrX`). Las redes
  lógicas de los hipervisores (`default`, `webkvm-bridge`, `br0-bridge`,
  `virbr0`, `lxdbr0`) desaparecen del backend, la UI y los scripts.
  - **KVM**: `<interface type='bridge'><source bridge='vmbrX'/>` siempre
    (se eliminó el camino `<interface type='network'>`/NAT).
  - **Incus**: `nictype=bridged parent=vmbrX` siempre; `bridgeForNetwork`
    ya no traduce nombres lógicos (se eliminó el resolver). Un nombre de
    red debe ser un bridge real del host.
  - **`ListNetworks`** devuelve únicamente bridges reales (con su IP);
    `CreateNetwork` crea un bridge a nivel de SO (jamás una red libvirt);
    Update/Delete/Start/Stop se rechazan (los bridges se gestionan en el
    SO). Se elimina la creación en el arranque de `default`/`webkvm-bridge`.
- **Fix contenedores con eth0+eth1 (Incus)**: el NIC de la instancia se
  llama estrictamente `eth0` (clave + `name`), que sobreescribe el NIC del
  perfil `default` — un solo interfaz, nunca el duplicado de `lxdbr0`.
  Verificado en vivo: los contenedores muestran `NICs=1`.
- **Adjuntar/desadjuntar NIC a un contenedor ahora configura el guest**:
  `AttachNetworkIface`/`DetachNetworkIface` regeneran `user.network-config`
  (netplan) con todos los NICs (`eth0`, `eth1`, …) en `dhcp4: true`, así
  que tras el siguiente reinicio cada interfaz adjuntada recibe DHCP de su
  bridge (LAN vía router o `vmbr1` vía dnsmasq). Antes, un NIC adjuntado al
  vuelo quedaba sin configuración en el guest (sin IP), lo que parecía un
  NIC roto/duplicado.
- **Varias IPs por instancia**: `VM.IPs []string` expone una IP por NIC
  (KVM vía guest agent/ARP; Incus vía estado de instancia), con `VM.IP` como
  primaria (eth0). La UI (lista, detalle y red) muestra todas las IPs
  (`vmIps()`); un contenedor con 2 NICs ya enseña sus 2 IPs, no solo la
  primera. La búsqueda también filtra por cualquier IP.
- **Instalador automático: detección DHCP/estática y fijado en todas las
  distros** (`setup-network.sh` + `install.sh`):
  - `is_iface_dhcp` detecta DHCP por la ruta del kernel (`proto dhcp`) o por
    el gestor en uso: NetworkManager (`ipv4.method auto`), netplan
    (`dhcp4: true`), systemd-networkd (`DHCP=`), ifupdown (`inet dhcp`).
  - Por defecto el bridge fija la IP actual: si hay DHCP, la concesión se
    convierte en **estática** sobre vmbr0 (misma IP, GW y DNS) — con un
    prompt **y/n** al inicio pidiendo aceptarlo (no bloqueante en modo no
    interactivo; `WEBKVM_PIN_STATIC=0` o `--dhcp` la dejan en DHCP). Avisa de
    que el router sigue viendo la IP en el pool hasta añadir la reserva.
  - Aplicación multi-gestor para vmbr0: **netplan** (estático), **nmcli**
    (bridge manual + puerto con `ipv4.method disabled`), **systemd-networkd**
    (nuevo: `.netdev`/`.network` estáticos) e **ifupdown** (nuevo, estilo
    Proxmox: `bridge-ports`, `stp off`, `fd 0`). Luego crea `vmbr1` (NAT).
  - Re-run seguro: si `vmbr0`/`br0` ya existen, se reutilizan sin tocar la
    red (el paso de resolución estática se omite).
  - `install.sh`: en modo bridge/both exporta `BRIDGE_APPLY=1` (aplicación
    automática) y deja que setup-network decida el fijado; el prompt
    interactivo ahora sugiere "static (fijar la actual)" por defecto.
- **`vmbr1` — bridge NAT/aislado estilo Proxmox** (`setup-network.sh`):
  crea `dummy0` (ancla del bridge sin NIC física), el bridge `vmbr1` con
  `100.0.0.1/24`, **MASQUERADE/NAT** hacia el uplink principal
  (firewalld/UFW/iptables) y **dnsmasq** sirviendo DHCP `100.0.0.100-200`
  — igual que los `vmbrN` NAT de Proxmox. Persistencia vía netplan
  (+ oneshot `webkvm-dummy0.service`), systemd-networkd o nmcli.
  Verificado en vivo: contenedor en `vmbr1` recibe `100.0.0.x` y sale a
  internet por NAT.
- **Scripts**: se elimina la creación de la red libvirt `br0-bridge`; el
  puente físico `vmbr0` sigue siendo el default (LAN real, DHCP del router).

### Changed

- El backend ya no crea/gestióna redes libvirt virtuales: `ensureDefaultNetwork`
  y `ensureDefaultBridgeNetwork` fueron eliminadas; `ListNetworks` no consulta
  `libvirtd`. El conector Incus ya no necesita `WithNetworkResolver`.

## [2.3.1] — Red física por defecto en todo el stack (2026-09-08)

### Added

- **Bridge físico como default absoluto**: VMs KVM y contenedores Incus nacen
  conectados a la red física (192.168.1.0/24) y obtienen su IP del router por
  DHCP — exactamente igual que Proxmox — sin tocar ningún ajuste.
  - **Incus (perfil `default`)**: `setup-network.sh` inyecta
    `profile device set default eth0 parent=<vmbr0> nictype=bridged`
    (detecta el CLI `incus` o `lxc`/LXD snap), eliminando la dependencia de
    `lxdbr0`: cualquier `incus launch` cae en la LAN real.
  - **Libvirt (NAT de fábrica)**: el script detiene y desactiva el
    autoarranque de la red NAT `default` (`virsh net-destroy default`,
    `net-autostart --disable default`) — las VMs no pueden recaer en
    `virbr0`.
  - **Frontend (`VmCreate.svelte`)**: el selector de Red ya no arranca en
    `default` (NAT). Auto-preselecciona el bridge físico principal (`vmbr0`/
    `br0`) o la red WebKVM que apunte a él; solo queda vacío si no existe
    bridge físico (entonces el backend falla en voz alta, nunca NAT). El
    resumen de contenedor ya no muestra `lxdbr0` falso, sino la red real.
  - **Backend (fallback estricto)**: con `network` vacío, KVM
    (`interfaceXML`/`mainBridge`) e Incus (`bridgeForNetwork`/
    `incusMainBridge`) asignan el bridge físico y, si el host no tiene uno,
    abortan con `ErrNoPhysicalBridge` — jamás una red NAT.
  - **Backend (NAT `default` redefinida al arrancar)**: `ensureDefaultNetwork`
    detectaba una red libvirt "default" existente y se limitaba a dejarla
    activa — resucitando la NAT de fábrica (`virbr0`) en cada boot. Ahora
    inspecciona su forward mode: si no es `forward='bridge'` (NAT/route/
    aislada), la destruye, la redefine como `forward='bridge'` sobre el
    puente físico (`vmbr0`/`br0`) y recién entonces la activa. Jamás NAT.

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
- **Backend Incus: los contenedores muestran su IP real en la UI** — antes
  `instanceToVM` nunca rellenaba `vm.IP`, así que los contenedores aparecían
  "aislados" (sin IP) en la interfaz aunque tuvieran IP de la LAN por DHCP a
  través del bridge. Ahora `GetDomain` y `ListDomains` consultan
  `GetInstanceState` y exponen la IPv4 de `eth0` (con fallback a cualquier
  interfaz no-loopback). Coste N+1 en listado, aceptable para homelab.
- **Blindaje multi-distro del bridge (`setup-network.sh`)**: inyecta y aplica
  `net.ipv4.ip_forward=1` (+ IPv6) y, si `br_netfilter` está cargado, fuerza
  `net.bridge.bridge-nf-call-iptables=0`/`ip6tables=0` para que el tráfico L2
  del bridge no sea re-filtrado por el firewall del host. Además abre la
  cadena **FORWARD** para el bridge según distro: firewalld (zona `trusted`),
  UFW (`before.rules` → `ufw-before-forward ACCEPT`) e iptables/nftables
  (`FORWARD -i vmbr0 -o vmbr0 -j ACCEPT`). Preventivo para distros con
  políticas FORWARD restrictivas (el tráfico bridged no atraviesa FORWARD
  salvo que `br_netfilter` esté cargado).

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