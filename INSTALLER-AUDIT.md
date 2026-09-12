# INSTALLER-AUDIT — Prueba en vivo del instalador de WebKVM

Auditoría práctica (no solo lectura de código) del instalador real
(`packaging/standalone/install.sh` + `scripts/install-webkvm.sh` +
`packaging/standalone/uninstall.sh` + `scripts/setup-network.sh`),
ejecutado de verdad contra VMs limpias en un host Proxmox, una por cada
familia de gestor de paquetes soportada. Fecha: 2026-09-11. Commit de
referencia: `a595daa` (main).

> **Estado: los 3 bugs de la sección "Hallazgos" están arreglados y
> re-verificados en vivo** (misma metodología: pruebas reales contra las
> VMs, no solo lectura de código). Ver "Verificación de los arreglos" al
> final de este documento para el detalle exacto de qué se probó y qué
> resultado dio. La sección de hallazgos de más abajo se conserva tal
> cual se escribió durante la auditoría original, como registro de qué
> se encontró y cómo — con una corrección marcada en el hallazgo #1,
> donde la propia re-verificación reveló que la conclusión original
> sobre Arch/Fedora era parcialmente incorrecta.

## Metodología

Se usó el one-liner **exactamente como lo documenta el propio README**:

```
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-webkvm.sh -o /tmp/webkvm-install.sh \
  && sudo bash /tmp/webkvm-install.sh
```

Sin flags ni variables de entorno propias — el comportamiento real que
vería cualquier usuario nuevo que siga el README al pie de la letra.
Piped ⇒ `WEBKVM_NONINTERACTIVE=1` automático ⇒ valores por defecto en
todo: HTTPS con certificado autofirmado, red en modo **bridge** físico
(vmbr0/vmbr1), Incus instalado por defecto.

VMs usadas (todas ya existentes en el host, prefijo `wk-`):

| Distro | IP | Familia | Resultado |
|---|---|---|---|
| Arch Linux (rolling) | 192.168.1.141 | pacman | ✅ completo |
| Debian 13 "trixie" | 192.168.1.142 | apt | ✅ completo |
| Fedora Linux 44 (Cloud) | 192.168.1.181 | dnf | ✅ completo |
| Ubuntu 26.x | 192.168.1.189 | apt | ❌ no se pudo — la VM no llegó a arrancar en dos intentos (problema de la propia VM/imagen, no del instalador; no se investigó más a fondo por no bloquear el resto) |

**Limitación importante de esta prueba**: ninguna de las 3 VMs
completadas estaba 100% virgen a nivel de paquetes — las 3 ya tenían
`libvirt`/`qemu`/`dnsmasq`/etc. instalados de sesiones de prueba
anteriores (no relacionadas). Esto significa que **no se observó en
vivo una instalación de paquetes realmente fría** (la fase de
`apt-get install`/`dnf install`/`pacman -S` de las dependencias de
virtualización pesadas) — en las 3 VMs esa fase fue casi instantánea
porque no había nada que descargar. Se compensó revisando el código de
esa ruta y, en Fedora, confirmando por `dnf history` que una instalación
real anterior sí arrastró 449 paquetes de golpe — así que en una máquina
genuinamente nueva esa fase tardará bastante más que los ~20-30
segundos vistos aquí.

En cada VM se ejecutó primero `packaging/standalone/uninstall.sh` (con y
sin `PURGE_DATA=1 PURGE_NETWORKS=1`) para intentar dejar la máquina lo
más limpia posible antes de la instalación real — este paso, de forma no
buscada, terminó siendo una de las partes más reveladoras de la prueba
(ver hallazgo #2).

---

## Hallazgos — bugs reales confirmados

### 1. `pkg_available()` está roto en las 3 familias de paquetes — Incus casi nunca se autoinstala de verdad

`packaging/standalone/install.sh:213-220`:
```bash
pkg_available() { # package provided by this distro?
  local p="$1"
  case "${PKG}" in
    apt)    dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q 'install ok installed' ;;
    dnf)    "${YUM_BIN:-dnf}" -q "$p" >/dev/null 2>&1 ;;
    pacman) pacman -Q "$p" >/dev/null 2>&1 ;;
  esac
}
```
El nombre de la función promete "¿está disponible (en el repo)?", pero
las 3 ramas comprueban en realidad **"¿está instalado ya?"** (`dpkg-query`,
`pacman -Q`) — o, en el caso de `dnf`, ni siquiera eso: `dnf -q "$p"`
**no es una invocación válida de dnf** (falta el subcomando, p. ej.
`list installed`). Verificado en vivo en la VM Fedora:
```
$ sudo dnf -q curl >/dev/null 2>&1; echo $?
2
$ sudo dnf -q list installed curl >/dev/null 2>&1; echo $?
0
```
`dnf -q curl` falla con exit 2 **aunque curl esté instalado** — es decir,
en dnf esta función **siempre devuelve falso**, sin importar nada.

Consecuencias reales, confirmadas en las 3 VMs:
- **Debian 13**: `incus` está genuinamente disponible en los repos
  (`apt-cache policy incus` → `Candidate: 6.0.4-2+deb13u9`,
  `apt-get install --dry-run incus` funciona), pero como nunca está
  "ya instalado" en una máquina nueva, `pkg_available` dice que no
  existe y el instalador **nunca lo instala**, imprimiendo
  `ADVERTENCIA: no hay paquete 'incus' (ni 'lxd') en apt` — un falso
  negativo puro. El "Incus instalado por defecto" que promete el
  README **no ocurre nunca en Debian/Ubuntu** a través de este código.
- **Arch**: mismo defecto de lógica (`pacman -Q` = instalado, no
  disponible). En el momento de escribir esto se asumió que el
  resultado final "coincidía con la realidad" porque se confió en el
  propio aviso del instalador ("no hay paquete incus ni lxd en
  pacman") — **error propio, corregido al verificar el fix**: `pacman
  -Si incus` muestra que Incus **sí está en el repo oficial `extra`**
  (versión 7.4.0-1). El falso negativo también aplicaba aquí.
- **Fedora**: como `dnf -q "$p"` siempre falla, `pkg_available` es
  **siempre falso para TODO paquete**, no solo Incus. Esto explica un
  patrón que se vio en el log: en vez de saltarse los paquetes ya
  instalados, el instalador invocó `dnf install -y <paquete>`
  **19 veces por separado** (una por cada dependencia de runtime),
  cada una recargando metadatos de repos, aunque los 19 ya estaban
  presentes. `pkg_install()` (línea 226: `pkg_available "$p" && continue`)
  nunca tiene ocasión de saltarse nada en dnf. También se asumió al
  principio que Incus no estaba en los repos oficiales de Fedora —
  **también incorrecto**: `dnf -q list incus` confirma que está
  disponible (`incus.x86_64 6.23-3.fc44`, repo `updates`).

  **Conclusión corregida: Incus está genuinamente disponible en los
  repos oficiales de las 3 distros probadas (Debian, Fedora, Arch), y
  el bug de `pkg_available()` hacía que el instalador NUNCA lo
  instalara en ninguna de las 3**, no solo en Debian como se pensó
  inicialmente. El arreglo (ver "Verificación de los arreglos") lo
  confirma: tras el fix, Incus se instala y queda activo de verdad.

**Arreglo aplicado** (`packaging/standalone/install.sh`): se separó la
función en dos — `pkg_installed()` (¿está instalado ya? — la
comprobación original de apt/pacman, correcta para este propósito, usada
como atajo en `pkg_install()`) y `pkg_available()`, reescrita para
comprobar disponibilidad real en el repo: `apt-cache show "$p"` (apt),
`dnf -q list "$p"` (dnf — cubre instalado o disponible; la sintaxis
`dnf -q "$p"` original ni siquiera era válida), `pacman -Si "$p"`
(pacman). Verificado en las 3 VMs (ver más abajo).

### 2. `uninstall.sh` no sabe nada del modelo de red actual (vmbr0/vmbr1) — quedó desincronizado desde el rediseño de red v2.4.0

`packaging/standalone/uninstall.sh:25-79` (todo el bloque
`PURGE_NETWORKS=1`) solo conoce el modelo de red **anterior a v2.4.0**:
redes libvirt virtuales (`br0-bridge`, `default`), `virbr0`/`virbr1`, y
un bridge+macvlan-slave con nombre `${BRIDGE_NAME:-br0}`. **No hay ni
una sola referencia a `vmbr0`, `vmbr1`, `dummy0`,
`webkvm-vmbr1-dnsmasq.service`, `/etc/webkvm/vmbr1-dnsmasq.conf`, ni a
los ficheros de netplan/systemd-networkd (`zz-webkvm-vmbr*.yaml`,
`vmbr0.netdev`) que el `setup-network.sh` ACTUAL crea de verdad** — se
confirmó en vivo en las 3 VMs que esos son exactamente los artefactos
que el instalador real deja hoy.

Efecto práctico, reproducido en la VM Arch: tras
`sudo PURGE_DATA=1 PURGE_NETWORKS=1 uninstall.sh`, quedó viva una red
libvirt huérfana (`webkvm-bridge`, de una instalación v2.2.0 anterior)
que el script ni tocó ni avisó que existía — hubo que borrarla a mano.
En una instalación v2.4+/v2.5 normal, `PURGE_NETWORKS=1` **hoy no
borraría `vmbr0`/`vmbr1` ni sus unidades de dnsmasq/netplan en
absoluto** — el usuario se quedaría con esos bridges y servicios activos
pensando que "purgó las redes".

**Esto es una regresión real y no relacionada con el trabajo de esta
sesión sobre `/api/networks`** (que unifica el modelo de red a nivel de
API/backend), pero toca exactamente la misma área — el
`uninstall.sh` necesita una pasada de actualización para reflejar el
modelo de red actual.

**Arreglo aplicado** (`packaging/standalone/uninstall.sh`): nuevo bloque
dedicado, además del bloque legacy (que se mantiene sin tocar, por si
algún host sigue en el modelo pre-v2.4), que replica en sentido inverso
exactamente lo que `ensure_vmbr1_nat()`/`apply_nat_vmbr1()`
(`scripts/setup-network.sh`) crean para `vmbr1` — nunca toca `vmbr0`/
`br0` (el bridge primario, protegido igual que en la API): para la
unidad `webkvm-vmbr1-dnsmasq.service` + su config; retira la
regla NAT (firewalld: llamadas idempotentes de retirada; iptables:
`-D` equivalente a lo que se añadió; ufw: solo avisa, no edita
`before.rules` a ciegas — demasiado arriesgado); borra los ficheros de
persistencia (netplan/systemd-networkd, comprobando siempre el marcador
`# Managed by webkvm` antes de tocar nada; nmcli, por nombre de
conexión) y por último el bridge `vmbr1` y la interfaz `dummy0` en
vivo. Verificado (ver más abajo).

### 3. La URL final "Web UI" del resumen coge la IP equivocada — confirmado en 2 distros con 2 IPs distintas y erróneas

`packaging/standalone/install.sh:741`:
```bash
lan_ip="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
```
`hostname -I` no garantiza ningún orden — devuelve todas las IPs del
host en el orden en que el kernel las enumera, no la "primaria". Efecto
observado, reproducido de forma independiente en dos VMs distintas:

- **Debian 13**: `hostname -I` → `192.168.122.1 172.17.0.1 192.168.1.142 100.0.0.1 ...`
  → el resumen imprimió `Web UI: https://192.168.122.1:8080` (la IP de
  `virbr0`, la red NAT interna de libvirt — inalcanzable desde fuera del
  host), en vez de `192.168.1.142` (la IP real de `vmbr0` que el propio
  instalador acababa de configurar en el mismo `setup-network.sh`).
- **Arch**: mismo bug, pero aquí `hostname -I` devolvió primero la IP de
  `docker0` (`172.17.0.1`) por tener Docker instalado — el resumen
  imprimió `Web UI: https://172.17.0.1:8080`. Confirmado con pruebas de
  conectividad reales desde fuera de la VM: `curl` contra `172.17.0.1`
  da timeout; contra la IP real de `vmbr0` (`192.168.1.141`) responde
  `{"status":"ok"}` sin problema.

En ambos casos la sección "Networks" justo debajo, en el mismo resumen,
**sí** identifica correctamente `vmbr0` como "physical bridge on the
real LAN" — el propio resumen se contradice a sí mismo. Cualquier host
con Docker o con la red `default` de libvirt activa (algo muy común)
mostrará una URL que no funciona desde otra máquina de la LAN, sin
ninguna pista de cuál es la IP correcta.

**Causa y arreglo**: el propio fichero ya usa la técnica correcta unas
líneas antes, para el SAN del certificado (línea 387):
```bash
lan_ip="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}' || true)"
```
Bastaría con usar esa misma técnica (IP de la interfaz por la que sale
la ruta por defecto) también en la línea 741, en vez de `hostname -I`.

**Arreglo aplicado**: exactamente eso — `lan_ip` ahora usa primero
`ip route get 1.1.1.1 | ... src`, con `hostname -I` y el barrido de
`ip addr` como fallbacks solo si esa ruta falla. Verificado (ver más
abajo).

### 4. `uninstall.sh` sin variables de entorno no deja la máquina limpia (documentado, pero sorprendente en la práctica)

Confirmado en las 3 VMs: sin `PURGE_DATA=1`/`PURGE_NETWORKS=1`, el
uninstall solo borra el binario y el `.service` — `/opt/webkvm`
(incluyendo `users.json`, certs, pools) y las redes/bridges se
conservan a propósito. El propio script lo anuncia con claridad
(`[3/4] kept data at /opt/webkvm; use PURGE_DATA=1 to remove it`), así
que **no es un bug**, pero tuvo un efecto colateral real en la prueba de
Debian: al reinstalar sobre datos preservados, el instalador detectó
`users.json` ya existente y **no generó un `admin-password.initial`
nuevo** (`preserving existing persistent server settings`) — quien siga
el flujo "reinstalar sin purgar" no obtiene una contraseña de admin
fresca y puede confundirse si esperaba una.

---

## Cómo se comporta el instalador, paso a paso (lo que SÍ funcionó bien)

1. **Detección de familia de paquetes**: correcta en las 3 (`apt`/`dnf`/`pacman`),
   una sola línea de log: `detected: <PRETTY_NAME> (family: <pkg>)`.
2. **Preflight** (KVM, RAM, disco, arquitectura, red): pasa en las 3,
   pero el log es un único `[preflight] ok — ...` agregado — no
   desglosa cada comprobación individualmente aunque el código sí las
   hace por separado.
3. **Detección de puerto ocupado**: funcionó exactamente como debía en
   2 de las 3 pruebas (un contenedor Docker huérfano de pruebas
   anteriores ocupaba el 8080 en Debian y en Arch) — el instalador
   abortó limpiamente con un mensaje claro y accionable
   (`ERROR: port 8080 is already in use...`) en vez de dejar un estado
   a medias. Buen comportamiento defensivo.
4. **Origen del binario**: en las 3 VMs, como el repo se clona/descarga
   sin binario precompilado (`backend/webkvm` no se versiona), el
   instalador **siempre** cae al último recurso: descarga el
   **último release oficial de GitHub** (`v2.4.1` en el momento de la
   prueba) y verifica su SHA256 contra `SHA256SUMS` antes de instalarlo
   — confirmado que aborta si no coincide. Nunca compila nada localmente
   (coherente con el diseño "el servidor nunca compila"). Importante:
   esto significa que ejecutar el one-liner hoy **no** despliega el
   código de `main` (que incluye el trabajo de esta sesión, v2.5.0) sino
   el último tag publicado — solo lo haría tras publicar un release
   nuevo.
5. **Certificado TLS autofirmado**: correcto en las 3, con SANs
   razonables (hostname, `localhost`, IP real de LAN, `127.0.0.1`).
6. **`setup-network.sh` (modo bridge, el único probado — es el
   default)**: el paso más elaborado del instalador, y el que más
   detalle imprime. En las 3 VMs, sin bridge físico previo:
   - Detecta la única interfaz física (`eth0`), migra su IP/gateway/DNS
     al nuevo bridge `vmbr0` **sin perder conectividad SSH en ningún
     momento** (verificado empíricamente — la sesión SSH nunca se cortó
     durante la migración de IP).
   - Crea además, siempre, un segundo bridge aislado `vmbr1`
     (`100.0.0.0/24`, con `dummy0` como ancla de carrier) con su propio
     `dnsmasq` (DHCP `100.0.0.100-200`) y MASQUERADE hacia la interfaz
     física — para tenants aislados con salida a internet. Esto no se
     menciona en el README como parte del flujo por defecto, pero el
     log lo documenta bien.
   - Detecta y neutraliza proactivamente configuración de red de
     cloud-init que entraría en conflicto (visto en Arch:
     `retiring other networkd config 10-cloud-init-eth0.network`).
   - Verificación final propia (`[verify] post-state checks`) confirma
     IP en vmbr0, ausencia de IP en la física, y ruta por defecto
     correcta — las 3 veces salió en verde.
7. **Arranque del servicio y `/api/health`**: activo y `"status":"ok"`
   en las 3. Contraseña de admin inicial generada y funcional por API
   (`must_change_password: true` — buena práctica) en Arch y Fedora
   (en Debian no se pudo comprobar por el motivo del hallazgo #4).
8. **Fallback de Incus con aviso, sin abortar**: en los 3 casos donde
   Incus no terminó instalado, el instalador avisó con claridad y
   continuó en modo KVM-only — nunca aborta la instalación completa por
   esto, que es el comportamiento correcto (aunque el motivo real del
   "no disponible" está mal diagnosticado, ver hallazgo #1).

---

## Hallazgos menores / cosméticos (no bloquean nada, pero vale la pena limpiarlos)

- Mensajes de advertencia (Incus no disponible) salen en **español**,
  mezclados en un log que es, por lo demás, todo en inglés.
- Texto de log potencialmente engañoso: `NetworkManager ... left active
  — macvlan mode` aparece incluso cuando el modo final configurado es
  bridge físico normal, no macvlan (frase que parece copiada de otra
  ruta de código ya no usada).
- Ficheros de netplan generados (`zz-webkvm-vmbr0.yaml`,
  `zz-webkvm-vmbr1.yaml`) quedan con permisos `644`, lo que dispara el
  propio warning de netplan sobre "Permissions... too open" (no hay
  secretos dentro, pero es ruido evitable con un `chmod 600`).
- 3 warnings idénticos de `netplan generate` sobre "Conflicting default
  route declarations... first declared in vmbr0 but also in eth0"
  durante la migración de IP en Debian — el resultado final es
  correcto, pero son alarmantes para un usuario nuevo sin necesidad.
- `webkvm-dummy0.service` queda `enabled` pero `inactive (dead)` tras
  la instalación (Debian) aunque `dummy0` está realmente activo (se creó
  por comandos de shell directos, no vía el unit) — confuso si alguien
  revisa `systemctl status`.
- Doble fuente de verdad para el bind address: la unit de systemd
  exporta `BIND_ADDR=127.0.0.1`, pero `config.json` fuerza
  `server.bind_addr: 0.0.0.0` y gana este último
  (`settings_overrode_addr` en el log) — funciona, pero puede confundir
  a quien solo mire la unit de systemd.
- En Fedora, justo tras crear `vmbr0`/`vmbr1`, el propio binario
  `webkvm` logueó `"skip_webkvm_bridge_network","reason":"no_linux_
  bridge_on_host"` — parece un falso negativo puntual en la
  autodetección de bridge al arrancar; no bloqueó nada (`/api/health`
  en verde) pero merece revisión.
- Warning transitorio y autocorregido en Arch: al primer arranque de
  `webkvm`, un fallo de conexión a libvirtd
  (`Cannot recv data: Connection reset by peer`,
  `running_in_offline_mode`) que se resolvió solo en menos de un
  segundo — probablemente una carrera entre el arranque de `libvirtd`
  y el primer intento de conexión del binario.
- Fedora 44 trae `virtqemud` activo por defecto; el instalador fuerza
  el modelo monolítico `libvirtd` en su lugar (y lo deja activo,
  `virtqemud` queda inactivo) — funciona, pero conviene confirmar que
  es una decisión de diseño intencional y no un descuido.

## Cosas que bloquearon la prueba pero NO son culpa del instalador

- Residuos de sesiones de pruebas anteriores no relacionadas
  (contenedores Docker huérfanos en el puerto 8080, un checkout previo
  de `/opt/webkvm-repo` sin `.git`, paquetes runtime ya instalados) —
  ninguno de estos es un defecto del instalador; si acaso, demuestran
  que sus comprobaciones defensivas (puerto ocupado, familia de
  paquetes, `pkg_available`) reaccionan de forma razonable ante un
  entorno no del todo limpio.
- La VM de Ubuntu 26 no llegó a arrancar en dos intentos (uso de CPU
  casi nulo, sin tráfico de red, sin respuesta de qemu-guest-agent) —
  parece un problema de la imagen/VM concreta, no investigado a fondo
  por no bloquear el resto de la prueba.

---

## Verificación de los arreglos (2026-09-11, misma tanda de pruebas)

Los 3 fixes se probaron en vivo, contra las mismas VMs reales usadas en
la auditoría (no solo lectura de código), copiando los ficheros
corregidos y ejecutando el código real (no una reimplementación de
prueba) contra el sistema.

**Hallazgo #1 — `pkg_available()`.** Se extrajeron y ejecutaron las
funciones corregidas directamente en cada VM:

- **Debian 13** (apt): `pkg_installed incus` → `false` (correcto, no
  estaba instalado); `pkg_available incus` → **`true`** (antes: `false`,
  el bug). `pkg_available` con un paquete inventado
  (`paquete-que-no-existe-de-verdad-xyz123`) → `false` (no da falsos
  positivos).
- **Fedora 44** (dnf): confirmado primero que `dnf -q curl` (la
  invocación original) falla con **exit code 2** aunque `curl` esté
  instalado — prueba directa de que la comprobación vieja nunca podía
  funcionar. Con el fix, `pkg_available curl` → `true`,
  `pkg_available incus` → `true` (¡Incus SÍ está disponible en Fedora
  44 — ver corrección en el hallazgo #1 más arriba!), paquete inventado
  → `false`.
- **Arch** (pacman): `pkg_available incus` → `true` (confirmado con
  `pacman -Si incus` directamente: está en el repo `extra`, versión
  7.4.0-1 — corrección del hallazgo #1).

**Prueba de instalación real de Incus** (Debian 13, `install_incus()`
completa, no solo la comprobación): instaló los 19 paquetes
(`incus`, `incus-base`, `liblxc-common`, etc.) sin errores. Verificado
después:
```
$ which incus
/usr/bin/incus
$ systemctl is-active incus
active
```
Incus quedó instalado y activo — antes de este fix, esto **nunca
ocurría** en ninguna de las 3 distros.

**Hallazgo #3 — URL de "Web UI".** En la misma VM Debian 13 (que sigue
teniendo `virbr0` activo, la causa original del bug):
```
$ hostname -I                                   # método viejo (roto)
192.168.122.1 172.17.0.1 192.168.1.142 100.0.0.1 ...
$ ip route get 1.1.1.1 | awk '... src ...'      # método nuevo
192.168.1.142
```
El método nuevo da la IP correcta (`vmbr0`, la real de LAN) en el mismo
entorno donde el viejo daba `virbr0` — reproduce y arregla el bug
exacto documentado arriba.

**Hallazgo #2 — `uninstall.sh` y vmbr1.** Estado antes, en la VM
Debian 13 (instalación real de una prueba anterior, con `vmbr1` y
`dummy0` activos): `webkvm-vmbr1-dnsmasq.service` activo,
`/etc/netplan/zz-webkvm-vmbr1.yaml` y
`/etc/systemd/system/webkvm-dummy0.service` presentes. Se ejecutó
`sudo PURGE_NETWORKS=1 uninstall.sh` (el `uninstall.sh` corregido) y
dio:
```
[2/4] removing libvirt networks and bridges
  - removing isolated NAT bridge vmbr1 (installer-managed)
  - removed /etc/netplan/zz-webkvm-vmbr1.yaml
  - removed /etc/systemd/system/webkvm-dummy0.service
  - bridge vmbr1 and dummy0 removed
```
Verificación posterior — todo lo que antes quedaba huérfano ya no
existe:
```
$ ip -br link show | grep -E 'vmbr1|dummy0'      # (sin salida — correcto)
$ systemctl status webkvm-vmbr1-dnsmasq.service
Unit webkvm-vmbr1-dnsmasq.service could not be found.
$ ls /etc/systemd/system/webkvm-dummy0.service /etc/netplan/zz-webkvm-vmbr1.yaml
ls: cannot access ... No such file or directory   (ambos)
```
Y, crítico, el bridge primario **no se tocó**:
```
$ ip -br addr show vmbr0
vmbr0            UP             192.168.1.142/24 ...
```

Las 3 VMs y ficheros de prueba se limpiaron tras la verificación.

---

## Resumen

1. **`pkg_available()` en los 3 gestores** (hallazgo #1) — ✅ **arreglado
   y verificado**. Era el más extendido: afectaba a Incus en las 3
   distros probadas (las 3 lo tienen realmente disponible en sus repos
   oficiales — dato que la propia auditoría inicial diagnosticó mal en
   2 de las 3, por confiar en el aviso del instalador roto en vez de
   comprobarlo directamente) y, en dnf, generaba trabajo redundante en
   cada instalación/actualización.
2. **`uninstall.sh` desactualizado respecto al modelo de red v2.4+**
   (hallazgo #2) — ✅ **arreglado y verificado**. Era el más
   "silencioso": no avisaba de nada, simplemente dejaba bridges y
   servicios de red huérfanos.
3. **URL de "Web UI" con la IP equivocada** (hallazgo #3) — ✅
   **arreglado y verificado**. El más visible de cara al usuario nuevo
   (rompe la primera impresión: instalar, ver el resumen, hacer clic en
   la URL y que no cargue).
4. El resto (hallazgo #4 y la lista de mejoras cosméticas) son
   comportamiento documentado o mejoras de calidad no bloqueantes —
   quedan sin tocar, documentadas arriba para quien quiera abordarlas
   más adelante.
