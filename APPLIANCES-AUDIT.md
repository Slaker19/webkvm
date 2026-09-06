# APPLIANCES-AUDIT — Plantillas de apps comunitarias (WebKVM)

Auditoría exhaustiva del sistema de **aplicaciones comunitarias** ("appliances",
deploy 1-click): catalogo de 34 plantillas, capa de API, job de deploy,
scripts de provisión, frontend y verificación **en la VM real** (URLs testeadas,
store desplegado comparado contra el binario).

- **Fecha:** 2026-09-05 · SHA auditado: `51898b1` + fixes posteriores de FIXES.md
- **Ficheros:** `backend/internal/appliances/` (`catalog.go` 365 ln,
  `defaults.go` 554, `provision.go` 921, `source.go` 60, `store_test.go`),
  `backend/internal/api/appliances.go` (651), rutas `router.go:270-288`,
  job store `api/storage.go:26-95`, frontend `VmList.svelte` (2 447 ln),
  `CredentialsModal.svelte`, `i18n.svelte.js`.
- **Catálogo desplegado verificado:** `/opt/webkvm/appliances.json`
  (VM `webvm` 192.168.1.121): `version 2`, **34 items**, `builtin_override`
  vacío → el store coincide **1:1** con los builtins embebidos en el binario.
- **Leyenda:** 🔴 *AP-C* crítico · 🟠 *AP-A* alto · 🟡 *AP-M* medio ·
  🔵 *AP-B* bajo. Los hallazgos llevan prefijo **AP-** para no chocar con
  `ANALISIS-CODIGO.md`.

---

## 1. Arquitectura del sistema

### 1.1 Modelo de datos (`catalog.go:27-66`)

| Campo | Significado |
|---|---|
| `ID` | clave del store; para builtins también prefijo del nombre sugerido |
| `Name/Description` | texto UI (sin i18n en algunos sitios, ver AP-B14) |
| `Category` | `router|home|nas|cloud|app` — **no validado** contra el enum |
| `URL` | fuente de la imagen, validada solo en Create/Update contra whitelist |
| `Format` | `qcow2\|raw` (inmutable de builtin, re-aplicado al cargar) |
| `Compression` | `none|gz|xz|bz2` |
| `SizeBytes` | tamaño esperado; solo se usa como **cota superior** (≤ 3×) |
| `VCPUs/RAMMB/DiskGB` | recursos recomendados (no bloqueantes) |
| `CloudInitSupported` | bool inmutable de builtin |
| `BaseImageID` | usa la imagen de otra plantilla como disco base (apps → `ubuntu-24.04`) |
| `ProvisionScript` | bash run-on-first-boot vía cloud-init; **no** viaja en `List/Get` |
| `Builtin / BuiltinOverride` | sembrado del binario / admin lo sustituyó (persiste) |

Store JSON en `DATA_DIR/appliances.json`, mutex + escritura atómica
tmp+rename (`catalog.go:198-217`). Seed desde `Defaults` (binario) en
primer arranque; merge de builtins nuevos en arrancues posteriores
(`catalog.go:104-132`); `layoutVersion=2` descarta scripts v1 no-overridden
(`catalog.go:137`); campos inmutables re-aplicados desde Default
(`catalog.go:183-192`).

### 1.2 Endpoints (`router.go:270-288`)

| Método | Ruta | Rol | Handler |
|---|---|---|---|
| GET | `/api/appliances/` | autenticado (viewer+) | `ListAppliances` |
| POST | `/api/appliances/{id}/deploy` | operator+ | `DeployAppliance` → `deployApplianceJob` (goroutine) |
| GET | `/api/appliances/{id}/provision` | operator+ | `GetApplianceProvision` → `{script,is_builtin,overridden}` |
| POST/PUT/DELETE | `/api/appliances[/{id}]` | **admin** | CRUD + `provision_script` admin-only |
| GET | `/api/storage/jobs/{id}` | autenticado | polling de progreso (`GetDownloadJob`) |

No hay feature flag: la feature vive incondicionalmente (rechazada la
sospecha de flag admin; RBAC puro).

### 1.3 Flujo de deploy (`api/appliances.go`)

`DeployAppliance:190` → valida → **quota/ACL (solo handler, pre-job)** →
audit `vm.appliance_deploy` → job `app_<unixnano>` →
`go deployApplianceJob:265`:

1. Pool de destino **hardcodeado** `config.DiskPoolName` (`webkvm-disks`,
   `config.go:17`); no se elige pool en la UI.
2. Resuelve `BaseImageID` (una capa) → URL/format/cmp/size del base
   (`:285-295`).
3. Descarga → `<DATA_DIR>/appliances/<vmName>.download` con
   `downloadTo` (timeout 60 min, cap 10 GiB, ≤10 redirects, dialer
   anti-DNS-rebind `isBlockedIP`, re-valida https en redirects `:500-511`
   — pero **no** re-valida la whitelist de dominio en redirects).
4. Descompresión: `gz` (multistream off p/ OpenWrt trailing garbage), `xz`
   (binario del SO), `bz2` (stdlib). Validación: ≥5 MiB, magic `QFI\xfb`
   si qcow2, size ≤ 3× `SizeBytes`. **Sin SHA256 ni firma** del artefacto.
5. `os.Rename` a `<poolPath>/<vmName>.qcow2|.img` → `qemu-img resize` a
   `DiskGB` si virtual-size menor → `RefreshPool`+`GetStorageVolume` →
   `CreateDomain` (`ExistingDiskPool/Name`).
6. Meta: `OwnerID=llamante`, `CiUser`, `DBPass=GeneratePassword(16)`
   sustituye `{{WEBKVM_DB_PASS}}` en el script; `AppInfo` JSON a meta de VM
   (muestra credenciales en la UI al dueño).
7. `applyCloudInit`: seed ISO NoCloud (script **base64 en `write_files`** +
   runcmd) adjunta como cdrom SATA. Fallo → job `completed` *con warning*
   en el texto (no como error).
8. Limpiexa `defer`: solo borra `rawPath`/`decompPath` **pre-rename**.

Seguridad de entrada: `validateVMName` (`vms.go:1270-1277`,
`^[A-Za-z0-9_.-]{1,64}$`), `CloudInitRequest` con `ProvisionScript
json:"-"` (el cliente **nunca** puede pasar scripts), cloud-init validado
(user regex, password 6-12, sin grupos de sistema), password va como hash
SHA-512-crypt, script va **base64** (`!!binary`) → sin YAML/command
injection. XSS: cero `{@html}` en el flujo; credenciales/sitio renderizan
como texto plano.

---

## 2. INVENTARIO COMPLETO — 34 plantillas comunitarias

Fuentes: `defaults.go` (los valores del binario) == store de la VM
(repoblado a versión 2). Estado de URL verificado con `curl -IL` hoy
desde 2 entornos (host Proxmox + VM). **OK** = 200 tras redirects ·
**404** = recurso muerto confirmado · **ERR** = 000 (host no concluye
handshake desde ambos entornos — revisar de otro upstream).

### 2.1 cloud (base pura, login vía cloud-init, sin install-script)

| ID | Distro/versión | Fuente | fmt/cmp | vCPU·RAM·Disco | size | URL |
|---|---|---|---|---|---|---|
| `ubuntu-24.04` | Ubuntu Noble current | cloud-images.ubuntu.com | qcow2/— | 2·2048·10 | 624 447 488 | OK (además base de 15 apps) |
| `ubuntu-26.04` | Ubuntu "resolute" (26.04 daily, tracks latest) | cloud-images.ubuntu.com | qcow2/— | 2·2048·10 | 863 000 000 | OK |
| `debian-13` | ⚠️ dice "Debian 13 (trixie)" pero **descarga Debian 12 bookworm** | cloud.debian.org | qcow2/— | 2·2048·10 | 325 000 000 | OK (URL viva, contenido erróneo) |
| `rocky-9` | Rocky GenericCloud 9 | dl.rockylinux.org | qcow2/— | 2·2048·10 | 645 988 352 | OK |
| `centos-stream-9` | CentOS Stream 9 | cloud.centos.org | qcow2/— | 2·2048·10 | 1 500 000 000 | OK |
| `fedora-44` | Fedora Cloud 44-1.7 (pinneada) | download.fedoraproject.org | qcow2/— | 2·2048·10 | 583 729 152 | OK |
| `arch-linux` | Arch rolling latest | geo.mirror.pkgbuild.com | qcow2/— | 1·1024·5 | 624 447 488 (≈ bases) | OK |
| `alpine-3.24` | Alpine 3.24 generic cloud | dl-cdn.alpinelinux.org | qcow2/— | 1·1024·5 | 183 697 408 | OK |
| `opensuse-leap` | openSUSE Leap 16.1 | download.opensuse.org | qcow2/— | 2·2048·10 | 340 395 520 | URL **404** (Build2.1 desaparecido) |
| `truenas-scale` | TrueNAS SCALE 25.04 (Fangtooth) — **ISO** installer | download.truenas.com | raw/none | 4·8192·20 | 2 100 000 000 | OK |
| `openmediavault` | OMV 8.0-12 — **ISO** installer, login `admin/openmediavault` | sourceforge.net | raw/none | 2·2048·20 | 1 200 000 000 | URL **404** |
| `home-assistant` | HAOS 18.2 (_sin_ cloud-init); UI :8123 | github.com (path permitido) | qcow2/xz | 2·2048·20 | 535 936 800 | OK |

### 2.2 nas / router

| ID | Descripción | Geometría | Detalle de estado |
|---|---|---|---|
| `openwrt-23.05` | OpenWrt 23.05.5 x86/64 combinada | 1·512·2 · raw/**gz** | `SizeBytes=NULL/0`; LuCI http://192.168.1.1 · URL **OK** |
| `opnsense` | OPNsense 26.7 vga | 1·1024·5 · raw/**bz2** · admin/opnsense | mirror.opnsense.org — **sin conexión (000)** desde 2 redes distintas |
| `pfsense-ce` | pfSense CE 2.8.1 memstick-serial | 2·2048·10 · raw/**gz** · admin/pfsense | nyifiles.pfsense.org — **sin conexión (000)** desde 2 redes |
| `ipfire` | IPFire 2.29-core203 | 1·1024·4 · raw/**xz** · GUI :444 | OK |
| `vyos` | VyOS 1.5 rolling — **ISO** **pinneada a 2026-08-05** | 2·4096·10 · raw/none | URL **404**: los nightlies caducan/desaparecen |

### 2.3 apps — 16 scripts de provisión embebidos en `provision.go`

Todas `qcow2` con `base=ubuntu-24.04` y cloud-init; puertos y credenciales
según la tabla:

| ID | script (`provision.go`) | resumen de lo que instala | publicado / credenciales |
|---|---|---|---|
| `wireguard-easy` | `:306` | get.docker.com \| sh + compose `weejewel/wg-easy:latest` `network_mode: host` + `openssl rand` | web :51821 · UDP :51820 · PASS rand |
| `openvpn-ui` | `:364` | get.docker.com \| sh · `mknod /dev/net/tun` · compose `shuricksumy/openvpn-ui:latest` | web :8080 · OpenVPN UDP :1194 · admin/rand |
| `docker-ce` | **⚠ NO TIENE script** (`docker-ce` ausente de `provisionScripts`) | **despliega Ubuntu 24.04 SIN Docker** — template funcionalmente muerto | — |
| `portainer-ce` | `:421` | docker CE + `docker run portainer/portainer-ce:lts` con `-v /var/run/docker.sock` | https://IP:9443 · admin/rand |
| `gitea` | `:453` | binario wget (pin **1.22.6**, **sin checksum**) + systemd + sqlite + `INSTALL_LOCK=true` | :3001 · SSH :22 · usuario/rand |
| `vaultwarden` | `:531` | docker CE + `vaultwarden/server:latest` (`SIGNUPS_ALLOWED=true`) | :8080→80 · ADMIN_TOKEN rand |
| `minio` | `:566` | binario dl.min.io + systemd + `chmod 600 /etc/default/minio` | :9000 API / :9001 UI · root/rand (único script que aplica chmod 600) |
| `nginx-proxy-manager` | `:622` | docker CE + compose `jc21/nginx-proxy-manager` (:80/:443/:81) | admin digamos `admin@example.com/changeme` |
| `homer` | `:666` | docker CE + `b4bz/homer:latest` | :8080 |
| `pihole` | `:707` | docker CE · **elimina `/etc/resolv.conf`** + `disable systemd-resolved` | :80 admin · DNS :53 · pass rand |
| `adguard-home` | `:767` | **`disable --now` + `mask` systemd-resolved** · resolv.conf=1.1.1.1 · tarball GitHub | :80 · :53 DNS · pass rand |
| `wordpress` | `:12` | apt LAMP + mysql + wget latest.tar.gz; cred `-n /etc/webkvm-app.txt` | :80 · admin/rand |
| `nextcloud` | `:71` | apt LAMP + latest.tar.bz2 + servicio *webkvm-trusted-domains* | :80/nextcloud · admin/rand |
| `odoo` | `:167` | apt nightly repo **`[trusted=yes]` http://** · hide normalized shadow root · psql pass **«odoo» fija** · patch lxml/safe_eval (`sed`) + pin `werkzeug==2.2.3` | :8069 · **DB pass «odoo»** |
| `moodle` | `:238` | apt LAMP + moodle-latest-405.tgz → `wget` latest | :80/moodle · admin/rand |
| `beszel` | `:835` | docker hub :8090 + agent host-network `-v docker.sock:ro` | :8090 · rand |
| `uptime-kuma` | `:883` | docker CE + `louislam/uptime-kuma:1` | :3001 · on/first-run |
| `docker-ce` | **NO SCRIPT** (AP-A3) | Ubuntu limpia — no llega a instalar Docker ni compose plugin | — |

Patrón transversal de todos los scripts docker:技术和 `curl -fsSL
https://get.docker.com | sh` (8×: `provision.go:313,370,427,628,671,712,841,889`)
— sin firma ni SHA. Las credenciales generadas terminan en
`/etc/webkvm-app.txt` (world-readable, CP-AP-C3) y además el script se
auto-adjunta al `/root/.bashrc` del guest (MARK/grep) + meta `AppInfo`
visible en la UI (CredentialsModal).

**Cloud-init wiring crítico (AP-A3b):** el bloque `if req.CloudInit != nil`
(`api/appliances.go:437-462`)concACTIVA **también la provisión/install del
app** — si un operador deja el form de cloud-init sin completar, la "app"
se despliega **sin la app** (deploy "completed" verde, Ubuntu pelada).

---

## 3. Hallazgos (ordenados por severidad)

> Prefijo **AP-** (no colisiona con `ANALISIS-CODIGO.md`). Pocos aún
> corregidos, marcados con `(fix aplicado)` en la última lista del section 6.

### 🔴 AP-C1 · Deploy pisa discos de VMs existentes (data loss)
`api/appliances.go:196-217` (sin check de colisión) +
`:375-377` `os.Rename(finalPath, poolDest)` + `domain.go:207` (CreateDomain
no distingue duplicates). `os.Rename` **sobrescribe silenciosamente** un
`<poolDest>` existente; `CreateDomain` rechazará después por nombre
duplicado… y el disco de la VM original **ya está destruido**.
Reproducible: deploy con `name` == VM existente (la validación anti-choque
existe en Import/Clone — `resolveUniqueDomainName`, `domain.go:2644` —
pero NO aquí). **Severidad datos: máxima.**

### 🔴 AP-C2 · Fallo post-rename deja disco huérfano sin VM y sin cuota
`api/appliances.go:311-316` (defer solo raw/laptop files): fallo en
`qemu-img resize` `:401`, `CreateDomain` `:430-433`, o semilla cloud-init
`orneado` `:458-461` deja el disco/imagen ya movida al pool sin VM. Sin
acounting ni alerta. (Detalle: `GetStorageVolume` `:411-415` es el único
fallo que sí borra).

### 🔴 AP-C3 · Credenciales del app legibles por todos los usuarios del guest
16× `cat > /etc/webkvm-app.txt` sin `chmod 600`/umask (`provision.go:51,
147, 217, 283, 347, ...)`: WP/MINIO/PIHOLE/OVPN/WG/GITEA/etc pass quedan
0644; el script además **cat el fichero en cada login root** (bashrc-mark)
y lo pinta en el motd. Además via meta `AppInfo` la UI muestra las
credenciales a quien pueda ver la VM. Se interpone: `install -m 600` o
`chmod 600` en cada fichero, lim.ToDouble-ink del chmod único del script.

### 🔴 AP-C4 · TOCTOU de cuota/ACL en deploys concurrently (verçuutado)
`appliances.go:221-236` cobra cuota **solo en el handler**; el job async
`:265→273` no re-chequea (src `grep`, 0 menciones). N deploys concurrentes
de operadores (descarga de 10 min, ventana reallarge) evaden
MaxDiskGB/PoolQuotas. FIXES.md lo marca como SOSPECHA CONFIRMADA →
pendiente de parchear (re-chequear dentro del job antes de `os.Rename`
al pool).

### 🟠 AP-A1 · To `network` sin validar → fallo de fase tardía (huérfano)
`appliances.go:202-209` acepta cualquier string; CreateDomain la inyecta
xmlEscapada pero **sin validar contra las redes existentes** ni
ACL. El fallo aparece **tras** descargar/descomprimir/resize (minutos),
generando el huérfano AP-C2. Sugerido: validar `network ∈ ListNetworks`
antes de arrancar el job (404/400 inmediato).

### 🟠 AP-A2 · `docker-ce` sin script + flujo cloud-init-without-credentials
`defaults.go:309-323` declara Docker CE; `provision.go` **no** define
`docker-ce` → la plantilla instal normalno прост Uruguay 24.04 limpia.
Y el flujo completo de provisión se activa **solo** si el operador envía
`cloud_init` (mismo handler), thus "apps" deployadas sin credenciales
quedan sin app. doble bug funcional.

### 🟠 AP-A3 · Artefactos sin verificación criptográfica + whitelist ancha
- Deploy valida solo magic bytes + **cota superior 3×**: no hay SHA256SUMS
  ni firma del `URL` par la imagen del appliance; las imágenes se están
  descargando de un OTTO chip. → permuta de espejo dentro de la whitelist
  (p.ej. github.com, sourceforge.net, bfsu.edu.cn) = root en guest
  (`provision.go` script lo hace tan atómico como darle ejecución).
- El comentario `provision.go:5-7` — "script never user-editable...
  prevents an admin from injecting arbitrary commands" — es **falso**:
  `SetProvision`/`UpdateAppliance` permiten al admin cambiar el script
  (con auto-act cap 64 KiB, apagado de built-in se mueve a override).
  Admin→root-en-guest es aceptable (admin ya es root del host), pero el
  comentario/documentación mienten y el audit log solo registra **len**
  (`api/appliances.go:171`), no hash ni diff del script.
- Whitelist `github.com/*` completo (`source.go:28/54-58`) + `sourceforge.net`
  (mux de mirrors) + `mirrors.bfsu.edu.cn` era no-oficial OPNsense —
  superficiales/falsamente "official".

### 🟠 AP-A4 · Jobs purgados en carrera + poller infinito del frontend
`storage.go:61-95` — `cleanOldJobs()` (activado **solo** al iniciar una
descarga ISO) borra TODOS los jobs completed/error, incluido un deploy
async de appliance que nadie está sondeando; y `pollApplianceJob`
(VmList.svelte:994-1042) trata **cualquier** error (404 incluido) como
"transient; keep polling" → **diálogo de deploy bloqueado** con
botón Cerrar disabled (enum estados `queued|downloading|processing`,
`:2044-2047`) para siempre + **leak del `setInterval`** (sin `onDestroy`).
Se manifiesta fácilmente: (a) otro operador descarga ISO; (b) reinicio del
backend (jobs en memoria RAM). PENDIENTE FIX (P1 tras los ya corregidos).

### 🟡 AP-M1 · Plantillas con URL breaks (verificadas hoy)
| ID | Motivo | Revisión |
|---|---|---|
| `opensuse-leap` | Leap 16.1 Minimal-VM Build2.1 qcow2: 404 | **ROMPE** — deploy falla tras download |
| `openmediavault` | sourceforge `_8.0-12.iso/download`: 404 | **ROMPE** |
| `vyos` | nightly 202608050033 expiró: 404 | **ROMPE** — rolling-URL pinneada |
| `opnsense` | mirror.opnsense.org: 000 (DNS/TLS) desde 2 redes | probablemente caído — probar otra mirror |
| `pfsense-ce` | nyifiles.pfsense.org: 000 | ídem |
| `debian-13` | URL → **Debian 12 bookworm** (`debian-12-genericcloud`): 200 pero contenido equivocado | bug de contenido |
| `fedora-44`/`ubuntu-26.04` | current/rolling: viven; `SizeBytes` exacto puede romper la cota ×3 si crece la imagen (solo hace reject, no hrefresh automático) | vigilar |

El resto (25/34) verificado **OK** (200) hoy.

### 🟡 AP-M2 · Colisión de escritura concurrente por nombre
Dos deploys con el mismo `vmName` comparten `rawPath/decompPath/poolDest`
(`appliances.go:306,375-377`) — sin lock por nombre ni O_EXCL: se pisan en
descarga (imagen corrupta) y en rename (el último gana el pool). CreateDomain
detecta el dup solo al final (tras minutes de I/O).

### 🟡 AP-M3 · Odoo: packages desde `http://` con `[trusted=yes]` + password «odoo»
`provision.go:176-208`: apt nightly sin firma GPG sobre **http** (MITM de
paquetes), psql `PASSWORD 'odoo'` **hardcoded** y publicado en la UI
(`appMetaFor` DBPass), `pip3 --break-system-packages` rompe el python del
sistema, pin `werkzeug==2.2.3` arbitrario, patch con `sed` a librería del
paquete. Combinación especialmente agresiva en un template "de catálogo".

### 🟡 AP-M4 · Pi-hole/AdGate destruyen el DNS del guest y sin reintentos
`provision.go:719-724` / `:771-774`: `rm resolv.conf` / `mask
systemd-resolved` + resolv.conf 1.1.1.1 — si el provision falla a mitad
(menor: sin retries en `docker compose up`), el guest queda sin resolver
y el job marca **completed de todos modos** (100%, verde) — el fallo real
no se refleja (ver AP-M5).

### 🟡 AP-M5 · `applyCloudInit` falla → job "completed" con warning apagado
`appliances.go:458-461`: status 100% completed `warning`; el **frontend
no muestra** ese warning (toast genérico)": operador ve success y la VM no
tiene la app ni credenciales. Además `AppInfo`/meta ya fueron escritos
(453-456) → CredentialsModal enseña credenciales de una app **no
instalada**.

### 🟡 AP-M6 · Recursos sin acotar en CRUD + pool-quota mismatch
- `vcpus/ram_mb/disk_gb` sin validación al crear/editar plantilla
  (catalog.go:311/334-339): aceptan 0/negativos; un `disk_gb` negativo
  salta el resize y revienta después (tras descar).
- ACL/quota chequean `config.DiskPoolName` (constante, `webkvm-disks`)
  pero el checkdisk usa `h.defaultPool()`=`lv.DiskPoolName()` — si el
  administrador configura otro pool de disco, el delta se contabiliza al
  pool equivocado y la imagen aterriza en el del config (`config.go:17`).

### 🔵 AP-B1..B16 (nível bajo)
- **B1** `ListAppliances` no audita (deploy/CRUD sí). ·
- **B2** botón Install visible a viewers (UI sin gate, backend sí) — UX. ·
- **B3** deploy valida cloud-init **después** de crear el job → job
  `queued` zombi y audit `vm.appliance_deploy` sin deploy real
  (appliances.go:246-264). ·
- **B4** jobs cross-user visibles/asdivinables (IDs `app_<unixnano>`)
  y sin expiración si nadie repite una descarga ISO (`storage.go:87-95`),
  crecimiento sin bound del mapa. ·
- **B5** credenciales también en mensajes del deploy/`bashrc` del guest +
  motd; se persisten tanto `AppInfo` (all viewers de la VM) como el propio
  fichero del caso — riesgo asumido pero cubra: (document). ·
- **B6** `provision.go` curl-pipe-sh en 8 get.docker.com, wget sin SHA
  para validez del binario gitea/pin específicos (1.22.6, werkzeug==2.2.3,
  controlling moodle latest, WordPress latest) — sin reproducibilidad. ·
- **B7** discriminación de credenciales intra-vuela: placeholders
  `{{WEBKVM_DB_PASS}}` enlined en urls del script del admin se sustituyen
  de sopetón (intrusión de password en URL semántica) — normal. ·
- **B8** error del meta `_ = h.lv.UpdateVMMeta(...)` (appliances.go:435,
  440, 455): si falla, la VM sin OwnerID no cuenta para la cuota del
  owner (accounting ghost). ·
- **B9** multi-idioma: la modal de credenciales y ~40 strings de la UI
  appliances están en **español/franciscano** duro (CredentialsModal
  completo; "Restaurar original", "Add appliance", "Edit appliance",
  labels del editor…) — mezcla EN/ES en la UI. ·
- **B10** keys i18n de cat están completas en 3 idiomas para los 6 bloques
  `vms.cat_*`/`vms.appliance*` (sincronizados desde la checker), pero keys
  **dinámicas** (`vms.cat_' + category` con categoría libre de admin,
  VmList.svelte:1880 → keys crudas visibles si la categoría es custom). ·
- **B11** backend log por `fmt.Println` (`appliances.go:409`). ·
- **B12** descarga single-shot (sin retry/partial resume) — descargas
  grandes (truenas 2.1GB, 60 min timeout) **finn** sensibles. ·
- **B13** la seed ISO falla y la VM **no** la posee pero el job sigue
  `completed` y **AppInfo ya escribido** — duplicado de AP-M5 vista
  servidor. ·
- **B14** bucle sin `onDestroy` del poller (leak, parte de AP-A4). ·
- **B15** `xzFile` dependiente del binario host, sin re-verify. ·
- **B16** files generados por `os.Create` en `DATA_DIR/appliances`
  quedan 0644 (el dir a 0700, mitigado).

### Sospechas previas marcadas (veredicto)
| Sospecha de ANALISIS-CODIGO | Veredicto |
|---|---|
| (a) TOCTOU cuota en deploy async | **CONFIRMADA** (AP-C4) — el job re-chequea 0 veces |
| (b) `network` sin validar | **CONFIRMADA parcial**: no valida presencia; sin XML injection (xmlEscape) pero rompe en fase tardía (→ AP-C2) |
| (c) feature flag admin | **REFUTADA** — no existe flag; RBAC puro |
| (d) limpieza en fallo del job | **PARCIAL** — sí pre-rename, no post-rename (→ AP-C2) |
| (e) fetch remoto de catálogo que inyecte scripts | **REFUTADA** (no existe; script solo vía admin del store local) |

---

## 4. Verificación del store desplegado (VM real)

- `/opt/webkvm/appliances.json`: `{"version":2, "items":[34]}`,
  `builtin_override` **0** (no hay customizaciones admin activas).
- Los 34 ids, categories, urls, formatos, recursos y `has_script`
  (Y en 16 apps; `docker-ce` = **n**) coinciden con `defaults.go` +
  `provisionScripts` — es decir, **los bugs del catálogo embebido NO se han
  parcheado vía override** (debian-12→13, docker-ce sin script, etc.
  están vivos en la instalación).
- Solo `openwrt-23.05` tiene `SizeBytes=null` en el store (build bazado en
  KeyError de version → el solo override del cap ×3 no se aplica; usa
  fallback del cap total).

---

## 5. Recomendaciones priorizadas

- **P0 (seguridad/datos):**
  1. AP-C1: colisión — antes del download, comprobar
     `GetDomain(vmName)`/volumen y 409 vía `resolveUniqueDomainName`
     (o fail fast pre-I/O) + **lock por nombre concurrente**.
  2. AP-C2: mover `poolDest` bajo el `defer` cleanup + borrarlo en
     cualquier error posterior al rename.
  3. AP-C3: `install -m 600 /dev/null /etc/webkvm-app.txt` (o umask 077)
     en los 16 scripts + no cat en bashrc/motd (o permisos root-only y
     nota en UI).
  4. AP-C4: re-chequear `checkQuota+checkDiskQuota` DENTRO del job justo
     antes del `os.Rename` al pool (o reservar disco en la fase HTTP con
     mutex por-usuario).
- **P1:** AP-A2 (añadir script `docker-ce` a provisionScripts + hacer el
  bloque de provisión independiente de `cloud_init != nil`), AP-A3 (SHA o
  firmas al fetch; tu imposición de whitelist al redirect), AP-A4 (limpiar
  jobs con autóexesión basada en TTL + el poller con 3x retry y 404 →
  estado terminal limpio + `onDestroy`).
- **P2:** AP-M1 (corregir 3 URLs muertas + debian-13→trixie; nightly
  expuesto → "latest"), AP-M2 (validar `network` contra ListNetworks
  pre-job), AP-M3 (odoo https + password aleatoría), AP-M4/M5 (retries y
  tratar applyCloudInit-fail como `error` y limpiar AppInfo), AP-M6
  (quadrar `config.DiskPoolName`==`lv.DiskPoolName()` o usar uno solo).
- **P3:** AP-B (min): ACL fail-open compensado, auditar ListAppliances,
  sanitizar categoría a enum fijo, i18n de las ~40 strings pendientes,
  job-retention TTL cross-user, retry en descarga.

## 6. Qué está BIEN (para memoria)

- Whitelist SSRF sólida (blocks IP internas + DNS-rebind-safe dialer +
  re-validación en redirects de esquema/IP).
- Sin XSS ni YAML/command injection alcanzables desde operador (script
  base64 `!!binary`, password como SHA-512-crypt, argv passthrough en
  xorriso; `validateVMName` anti-traversal; filenames derivados de regex
  estricta).
- Cloud-init fail-fast con payload válido (400 pre-job); credenciales
  server-side (`GeneratePassword(16)` alfanumérico) sustituidas en el
  host, no por el cliente (`ProvisionScript json:"-"`).
- Store con mutex + atómicidad tmp+rename + anti regression de builtins
  (campos inmutables re-aplicados, scripts v1 descartados); Save completo
  hace `List/Get` **limpiando ProvisionScript** (leak de script reducido).
- RBAC correcto: GET autenticado; deploy operator; CRUD/script admin —
  decidido en router, no solo en la UI.
