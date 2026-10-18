# WebKVM — Contexto para desarrollo (AGENTS)

> Documento vivo para agentes y humanos. Actualizar al cerrar cada fase.
> Última actualización: 2026-10-19. Rama `main` en `v0.1.9-dev`. Working tree limpio.

## 1. Qué es

WebKVM convierte cualquier Linux (Ubuntu/Debian/Fedora/Arch/Alma/Rocky) en nube privada en 1 min, sin reinstalar.
Binario único Go ~14 MB, ~15 MB RAM en idle. Gestiona **QEMU/KVM + Incus/LXC** bajo un solo dashboard.

- Web UI: Svelte 5 (runes) + Tailwind v4 + xterm WebGL + noVNC, servida **embebida** en el binario.
- Server: Go 1.26 + CGO libvirt + chi + JWT + WebSocket + SSE.
- CLI: `webkvm-cli` (Go puro, sin CGO), zero-token como root.
- Instalación: `scripts/install-webkvm.sh` → `packaging/standalone/install.sh` (nativo systemd) o Docker (`docker-compose.yml`).
- Docs usuario en `docs/` (cada `.md` tiene espejo `.es.md`). No duplicar aquí guías de uso; esto es contexto técnico.

## 2. Mapa del repo

```
webkvm/
  backend/           Go (module webkvm, go 1.26.9). 433 .go, ~117k líneas (~76k sin tests)
    cmd/server/      main.go (1123L) + tls.go — único binario prod
    cmd/cli/         main.go (2007L) — webkvm-cli
    cmd/migrate-disk-names/  one-shot renombrado discos
    internal/        37 paquetes (ver §3)
    internal/frontend/dist/  copia de ../frontend/dist (la hace make, no editar a mano)
  frontend/          Svelte 5 + Vite 6. 257 ficheros en src/ (168 .svelte)
    src/routes/      22 rutas hash `#/...` (VmList, VmDetail, VmCreate, MultiView, Storage, Networks, Backup, Firewall, ...)
    src/lib/components/vm-detail/  16 submódulos (overview/hardware/disks/net/usb/pci/console/metrics/...)
    src/lib/stores/  15 stores .svelte.js (auth, events, tasks, theme, terminalSettings, ...)
    src/lib/utils/   18 helpers + 18 *.test.js
    src/lib/i18n.svelte.js  11k líneas, locales en/es/ca
    dist/            salida vite (no editar; la regenera make)
  scripts/           instaladores y utilidades (ver §5)
  packaging/         standalone/ (install/update/uninstall canónicos), nfpm deb/rpm, arch PKGBUILD, docker/
  docs/ + docs/runbooks/  guías usuario y runbooks ops
  Dockerfile, docker-compose.yml, docker-entrypoint.sh
  Makefile (SHELL=bash, VERSION sin `v`), install.sh, .env.example
  .github/workflows/  ci.yml, release.yml, docker-publish.yml
```

## 3. Backend (Go)

### 3.1 Entradas
- `backend/cmd/server/main.go`: carga `config.Load` + `configstore`, TLS (`tls.go`: off/selfsigned/letsencrypt), abre libvirt, `EnsureDefaults + CleanupStaleImports`, inyecta todo en `api.NewRouter(...)` (sin globales salvo `libvirt.SetNetStore/SetNATChecker`, `compute.BindHelpers`). HTTP: 30s header / 30m write / 120s idle + `LimitListener` + `Cooldown`. Shutdown ordenado con `FlushAndWait()` y `hub.Close()` antes.
- `backend/cmd/cli/main.go`: `--server/--token/--insecure/--json`, `login/logout`, zero-token local firmando HS256 con `/opt/webkvm/jwt.key` como admin. Subcomandos: `vms/images/storage/disks/networks/settings/audit/users/tokens/backup/snippets`.
- `backend/internal/frontend/embed.go`: `//go:embed all:dist`. El frontend se sirve desde aquí.

### 3.2 Paquetes `internal/` (37)
`api audit auth backupstore backup user cloudinit compute compute/incus config configstore diskprobe diskstate events firewall frontend fsutil helperscripts hostcaps imgsafe libvirt libvirtbackup logging mdraid metrics models netguard netstore nodes notify remotebrowse safego smart sysutil tokens vmsched vzdump zvol`

Ficheros grandes (orientación, no editar monolitos sin descomponer):
`compute/incus/incus.go:4220 backupstore/runner.go:2727 api/vms.go:2208 api/storage.go:2003 api/console.go:1535 api/host_disks.go:1528 models/types.go:1500 libvirt/domain.go:1395 libvirt/storage.go:1383`

### 3.3 Módulos clave
- **Libvirt/KVM** `internal/libvirt/`: `domain.go` + `domain_{xml,lifecycle,devices,snapshots,pci,usb,shares,policy,export_import}.go`, `storage.go`, `network.go`, `guestagent.go`, `ova.go`, `secret.go`, `metrics.go`, etc.
- **Compute seam** `internal/compute/`: `backend.go:533` interfaz `Backend` (~80 métodos). `kvm.go` adaptador delgado, `combined.go` KVM+Incus. Handlers **nunca** importan libvirt directo: van vía `compute.Backend`.
- **Auth** `internal/auth/`: `jwt.go` (Manager, Claims epoch/jti/mcp, CSRF double-submit), `rbac.go`, `ticket.go` (ticket `?ticket=` 30s un solo uso para serial/host-PTY/SSE), `vnc_ticket.go` (`?vt=` 1h reutilizable acotado a VM), `cookie/ratelimit/blacklist/totp/webauthn/must_change.go`.
- **API** `internal/api/`: `router.go:788` (chi + Recoverer/securityHeaders/requestLogger/limitBody/cors/JWT/rateLimit/SessionEnforcer), `handler.go` (todo inyectado + `deployLock` por nombre VM), `vms/storage/console/console_serial/events/networks/firewall/cloudinit/backup/host_*.go`, `middleware_*.go`, `authz.go`, `capability_gate`.
- **WS/PTY**: `gorilla/websocket` en `console.go` (VNC/SPICE binario, ping 25s), `console_serial.go` (`creack/pty`, single-flight por VM, timeout inactividad).
- **SSE** `internal/events/hub.go:146` (buffer 16, drop slow clients). Origen: `libvirt.StartEventLoop` + `MetricsCollector.SetSink`. Frontend: `events.svelte.js` con backoff 1s→30s.
- **Storage/Red**: vía `compute.Backend` → libvirt; más `zvol/`, `diskprobe/`, `diskstate/`, `netstore/`, `remotebrowse/` (SMB/NFS).
- **Transversales**: `backupstore/` (store/runner/producer/retention/s3/sftp/chain), `metrics/` (history/grafana/prometheus/alerts), `notify/`, `nodes/registry.go`, `audit/audit.go`, `config/` + `configstore/` (hot-reload), `netguard/` (jail/limitlistener/cooldown/ssh_watcher), `vmsched/` (cron), `vzdump/`, `appliances/`, `models/types.go:1500`.

### 3.4 Patrones obligatorios
1. **`sysutil.CommandRunner`** (`sysutil/runner.go:9`): `OSCommandRunner` en prod, `MockCommandRunner` en tests. Todo `exec` nuevo **debe** usarlo (inyectado). Ref: `smart/smart.go`, `mdraid/`, `zvol/`, `hostcaps/`.
2. **`context`**: `exec.CommandContext`, `RunWithTimeout`, timeout 15s en virsh guest-agent. Nada de `time.Sleep` rígido en backend: usar `<-ctx.Done()` / canales.
3. **Tickets, no JWT en URL**: JWT solo en cookie HttpOnly o `Authorization: Bearer`. En URLs solo `?ticket=` (30s/1 uso) o `?vt=` (consola). Validar scope por path (`ticketAllowedPath` / `vncTicketAllowedPath`).
4. **Seam `compute.Backend`**: handlers contra interfaz, no contra libvirt.
5. **Sin `panic()` en prod**: devolver error + defaults seguros (`internal/cloudinit`, `internal/user` ya saneados).
6. **Seguridad storage/imágenes**: `backing_guard`, `imgsafe`, `confine/path_safety`, pools fail-closed, dialer anti-SSRF (bloquea 10/8, 172.16/12, 192.168/16, 127/8, 169.254/16, 100.64/10, fc00::/7...), firewall con ventana de confirmación 30s + rollback.
7. **Tests**: 239 `*_test.go`. Convención: unit con mocks (`CommandRunner`, stores temp), sin integración real. Cobertura fuerte en auth/RBAC, XML libvirt, storage/backup, red/firewall, métricas.

## 4. Frontend (Svelte 5)

- Router hash en `src/lib/router.svelte.js:40-69`. Rutas: `vms`, `vms/new`, `vms/:id`, `apps/images/multiview/storage/media/networks/backup/firewall/audit/users/nodes/snapshots/status/host-console/settings/account`, `login`.
- `src/App.svelte`: lazy `routeLoaders`, ciclo SSE (`events.svelte.js`), RBAC, `HostConsole` persistente.
- `routes/VmDetail.svelte` (~2500L, orquestador con tabs) + `lib/components/vm-detail/` (16 piezas). No re-monolitizar.
- `TerminalPanel.svelte:830`: xterm + Fit/Webgl(fallback DOM)/Unicode11/WebLinks/Clipboard. `mode=vm` → `/api/vms/{id}/serial?ticket=`, `mode=host` → `/api/host/terminal?ticket=`. Temas en `utils/terminalConfig.js`, prefs en `stores/terminalSettings.svelte.js` (`webkvm.term.*`).
- `routes/MultiView.svelte`: video-wall noVNC vía `iframe /console/{id}?vt=&embedded=1`, grid 1-4 cols, `postMessage` broadcast, persistencia `webkvm.multiview.*`.
- `stores/tasks.svelte.js` + `TaskDrawer.svelte` + `TaskCenter.svelte`: drawer `Ctrl+J`, altura 160-600px (`webkvm.taskdrawer.height.v1`), split lista+logs.
- `stores/events.svelte.js`: `EventSource /api/events` (cookie, sin ticket), backoff 1s→30s. Un solo punto de consolidación SSE: no crear más `setInterval` de polling.
- i18n `src/lib/i18n.svelte.js`: `t(key,vars)`, locales `en/es/ca`, persistencia `webkvm.locale`. **Toda string visible debe tener clave en los 3 idiomas.** Verificar con `scripts/check-i18n.sh`.
- Componentes capitalizados deben estar importados (verifica `scripts/check-undefined-components.sh`).
- Build: `vite build` → `frontend/dist/` (`manualChunks`: vendor-xterm/novnc/lucide/ui/cron/qr, app, i18n). `make build-backend` copia a `backend/internal/frontend/dist/` y compila con CGO. Dev sin embed: `make dev-backend` (go run :8080) + `make dev-frontend` (vite :5173 con proxy a 8080).
- Tests: `npm test` (26 node puros `src/lib/**/*.test.js`) y `npm run test:dom` (7 happy-dom `*.dom.test.js`). Config en `vite.config.js` / `vitest.dom.config.js`.

## 5. Ops / build / install

- `make build` = frontend + backend (`webkvm`) + cli (`webkvm-cli`). `make binary/dist` empaqueta tarball + 2 binarios + `SHA256SUMS`. `make deb/rpm` (nfpm), `make docker-build`.
- `make install-systemd`: instala en `/usr/local/bin`, guarda `.previous`, health-check 20s con auto-`rollback`. `make rollback` restaura. `make install` (completo), `install-systemd-force` (sin check), `install-caddy` (`SKIP_INSTALL=1` solo Caddyfile), `regen-cert`, `install-all`, `uninstall` (conserva `/opt/webkvm` y logs).
- Instalador canónico `packaging/standalone/install.sh`: detecta apt/dnf/pacman, instala runtime (libvirt/qemu/ovmf/swtpm), despliega binario precompilado (nunca compila en destino), escribe `config.json`, TLS, `enable --now`, health-check. Vars: `WEBKVM_*`, `NETWORK_MODE`, `NONINTERACTIVE`.
- `scripts/`: `setup-network.sh` (bridge/NAT), `setup-bridge.sh`, `generate-self-signed.sh`, `install-caddy-systemd.sh`, `detect-webkvm-endpoint.sh` (usado por Makefile, no hardcodear 8080), `smoke.sh` (E2E pool→volumen→define→start→teardown), `build-in-docker.sh`, `webkvm-backup.sh`, `check-i18n.*`, `check-undefined-components.*`.
- Systemd `scripts/webkvm.service`: `After libvirtd/network-online`, root, `DATA_DIR=/opt/webkvm`, `ExecStart=/usr/local/bin/webkvm`, hardening con `ReadWritePaths`.
- Docker: imagen `slaker1908/webkvm:latest`, `network_mode:host`, `privileged:true`, `pid:host`. Monta `/opt/webkvm` en misma ruta, `/dev:rslave`, `/var/run/libvirt`, sockets systemd, `/etc/passwd+shadow+group+pam.d:ro`. No bundlea libvirtd.
- CI `.github/workflows/`: `ci.yml` (frontend: lint+prettier+i18n+undefined+test+build; backend: libvirt-dev + reuse artifact frontend-dist + golangci-lint + `go test -race`; packaging: `test-installer.sh`), `release.yml` (tag `v*` → tarball+deb/rpm+SHA256 fail-closed), `docker-publish.yml`.
- Datadir `/opt/webkvm` es única fuente de estado (users, jwt.key, config, pools-meta). Backup sin qcow2/iso.

## 6. Estado actual (Fases 1-6 hechas, ver CONTEXT_NOTES.md)

- F1 Red/build: bind `0.0.0.0:8080`, `build-in-docker.sh`.
- F2 Frontend: `VmDetail` 6479L → 16 comps, i18n en/es/ca 100%.
- F3 Backend: `domain.go` 4048→~1400L (`lifecycle/snapshots/devices/export_import/xml` + tests).
- F4 Concurrencia: sleeps → `ctx.Done()`, virsh con timeout, SSE consolidado.
- F5 Abstracción: `CommandRunner` OS/Mock, `smart` con mock, sin `panic` en `cloudinit/user`.
- F6 Validación: binarios en `/usr/local/bin`, systemd healthy, `go test -short ./...` OK, `smoke.sh` OK.
- F7 (0.1.8-dev): Soporte completo para formateo directo, montaje persistente systemd y creación automática de storage pools en arrays RAID (`c9c679c`). Integración nativa por kernel sysfs (`/sys/block/md*/md`) para descubrimiento instantáneo sin subprocesos, ciclo de vida (`GET /api/host/raid`, `DELETE /api/host/raid/{device}` protegido contra desmontaje), scrub/check en vivo y tarjetas de monitorización en UI (`be6e06d`). Soporte no destructivo para montaje de RAIDs existentes sin formatear (`mode: "mount"`), conservación de datos probada y selección granular de storage pools (multi-carpeta o raíz dedicado) (`d163b7c`). Auditoría exhaustiva web: depuración de errores 400 en SMART sobre arrays virtuales, saneamiento de payloads en diálogos de almacenamiento, corrección en tests DOM y pase al 100% en todas las suites (`b7db7e6`).
- F8 (0.1.8-dev): Refactorización integral de almacenamiento: descomposición de `frontend/src/routes/Storage.svelte` (5.295L → 542L) en 14 componentes modulares en `src/lib/components/storage/` (`32d7301`). Abstracción con inyección de `sysutil.CommandRunner` en los paquetes de bajo nivel `internal/mdraid` e `internal/zvol` con tests unitarios basados en mocks (`0bb8f47`). Descomposición del monolito `backend/internal/api/storage.go` (2.003L) en 6 submódulos limpios de dominio (`storage_pools.go`, `storage_volumes.go`, `storage_isos.go`, `storage_uploads.go`, `storage_jobs.go`, `storage_download_client.go`) conservando el 100% de compatibilidad de API (`60fd69c`). Saneamiento de rutas dobles en RAID sync/stop, migración universal al wrapper seguro `safeStorage` (con fallback en memoria) para prevenir bloqueos de `localStorage` en navegación privada o iframes en todos los stores (`i18n`, `vmOrder`, `vmLayout`, `sidebarOrder`, `terminalSettings`, `tasks`, `MultiView`, `VmCreate`) y corrección del ciclo de vida y memory leaks en `Status.svelte` (`3113c2e`). Blindaje contra path traversal en volúmenes/ISOs y mapeo de estados no-running en suspensión a 409 Conflict (`220fc1a`). Desbloqueo de restauración de backups UEFI modernos (libvirt 9/10 / Ubuntu 24/26) permitiendo el bloque `<firmware>` en la política de sanitización XML (`domain_policy.go`) y saneamiento de alias de borrado de discos en `DELETE /api/vms/{id}` (`4cc3efd`). Suite exhaustiva E2E de 54 pruebas multi-rol en vivo al 100% de éxito. Desplegado y verificado en VM 101 `webkvm-dev` (192.168.1.40).
- F9 (0.1.8-dev): Rediseño radical del subsistema de aplicaciones de terceros (Helper Scripts / App Store): reemplazo del crawler de 600+ peticiones individuales a GitHub API (que moría por rate limit 403) por una ingesta streaming en un único tarball comprimido de 630 KB (`catalog.go`), parseando e indexando los 625 scripts en solo 0.65 segundos. Arranque automático del contenedor en el deploy (`started: true`), generación de credenciales automáticas sin colisión de nombres de grupo (`ubuntu`, password aleatorio de 12 chars), saneamiento de nombres de sugerencia (eliminación de prefijos `cs-` y sufijos aleatorios feos), enlace directo a la instancia/consola en el modal de éxito y tarjetas UI completamente modernizadas con insignias de puerto de servicio (`:3000`), tags, specs y avatares (`7f94a48`). Desplegado y verificado en vivo en VM 101 con despliegue de AdGuard Home en 8.8 segundos.
- F10 (0.1.8): Corrección de la fricción real reportada en el [issue #5](https://github.com/Slaker19/webkvm/issues/5) del repositorio: (1) cookies de sesión/CSRF sin flag `Secure` cuando el TLS está apagado y `WEBKVM_COOKIE_SECURE` no está fijado explícitamente — el navegador descartaba la sesión en HTTP plano y el login entraba en bucle (helper `cookiesSecure` + `TestCookiesSecure`); (2) subcomando `webkvm-cli users update` (password/role/email/active vía `PUT /api/users/{username}`), antes anunciado en la ayuda pero inexistente; (3) autodetección del endpoint local en `webkvm-cli` leyendo `{DATA_DIR}/config.json` (puerto, bind, HTTP/HTTPS) con reintento único de esquema ante desajuste HTTPS/HTTP y zero-token extendido a binds LAN locales de la propia máquina (tests `TestDetectLocalServer` / `TestClient_SchemeFlipRetry`); (4) el instalador ya no crea `vmbr1` + dnsmasq en modo solo-bridge (opt-in con `--network=both` o `WEBKVM_NAT_BRIDGE=1`, coherente con el contrato documentado del modo).
- F11 (0.1.9-dev): Aceleración GPU y refactorización modular integral: (1) Aceleración GPU 1-clic (`/dev/dri/renderD128`) para contenedores Incus en creación (`VmCreate.svelte`) y caliente (`VmHardwareTab.svelte`); (2) Passthrough de dispositivos PCI para contenedores Incus (`AttachPCIDevice`/`DetachPCIDevice` y `VmPciCard.svelte`); (3) Descomposición del monolito `backend/internal/api/vms.go` (2.216L → 494L) en 5 submódulos de dominio limpios (`vm_lifecycle.go`, `vm_snapshots.go`, `vm_devices.go`, `vm_clones.go`, `vm_export_import.go`); (4) Unificación de `vmScheduler` con `computeBackend` para soporte multiplataforma KVM/Incus; (5) Poda manual de copias de seguridad bajo demanda (`POST /api/backup/targets/{id}/prune`, `webkvm-cli backup prune`, botón en `Backup.svelte`); (6) Soporte de etiquetado 802.1Q VLAN en `VmDetail.svelte` y badges visuales en `VmNetCard.svelte`. Cobertura total de pruebas en verde.
- Últimos commits: `7f53303 feat(gpu,pci)`, `e036452 refactor(api)`, `855ba22 feat(backup)`, `e0a838e feat(network)`, `a0a4279 chore(version)`.

Pendiente conocido (de `AUDIT_IMPROVEMENTS.md`): quedan god-objects (`api/vms.go`, `api/storage.go`, `Storage.svelte` ~5000L con 8 pestañas), resto de `exec` sin `CommandRunner` (`mdraid`, `zvol`), resto de `setInterval` vs SSE, preflight contenedor FS-ro.
Limitación funcional detectada (para 0.1.8): `CreateHostRAID` crea el arreglo MDRAID vía `mdadm`, pero NO formatea el dispositivo `/dev/md*` resultante con un sistema de archivos ni lo monta en `/mnt` / systemd mount units. Se necesita opción/flujo post-creación para formatear y montar el RAID y poder usarlo directamente como storage pool de WebKVM.

## 7. Cómo seguir desarrollando

1. Rama por fase desde `main`. Cambios locales, sin push salvo que se pida.
2. Dev: `make dev-backend` + `make dev-frontend`. Build completo: `make build`. Limpieza: `make clean`.
3. Antes de dar por hecha una tarea:
   - Backend: `cd backend && go test -short ./...` (y `go vet` / golangci si se tocó API/auth/libvirt). Nuevo `exec` → vía `CommandRunner` + test con `MockRunner`.
   - Frontend: `cd frontend && npm test && npm run test:dom`, `npm run build`, `bash ../scripts/check-i18n.sh`, `bash ../scripts/check-undefined-components.sh`.
   - Si se tocó install/docker: `packaging/standalone/test-installer.sh` y/o `smoke.sh`.
4. Convenciones: no re-monolitizar `vm-detail/` ni `domain_*.go`; handlers vía `compute.Backend`; errores sin `panic`; strings UI con clave en en+es+ca; `localStorage` con clave `webkvm.*`.
5. Al cerrar fase: actualizar este archivo (§6 + fecha) y `CHANGELOG.md` si hay cambio visible. No editar `frontend/dist/` ni `backend/internal/frontend/dist/` a mano.
6. Comandos útiles:
   `systemctl status webkvm` · `journalctl -u webkvm -f` · `curl -kfsS https://127.0.0.1:8080/api/health`
   `webkvm-cli vms list` · `webkvm-cli --json vms list | jq .`

## 8. Archivos de referencia rápida

- Entradas: `backend/cmd/server/main.go`, `backend/cmd/cli/main.go`, `frontend/src/main.js`, `frontend/src/App.svelte`
- Router/API: `backend/internal/api/router.go`, `frontend/src/lib/router.svelte.js`
- Auth/seguridad: `backend/internal/auth/jwt.go|ticket.go|vnc_ticket.go|rbac.go`, `SECURITY.md`, `docs/runbooks/SECURITY-MODEL.md`
- Dominio: `backend/internal/libvirt/domain*.go`, `backend/internal/compute/backend.go`
- UI clave: `frontend/src/routes/VmDetail.svelte`, `frontend/src/lib/components/vm-detail/`, `TerminalPanel.svelte`, `MultiView.svelte`, `TaskDrawer.svelte`, `frontend/src/lib/stores/events.svelte.js`, `frontend/src/lib/i18n.svelte.js`
- Ops: `Makefile`, `Dockerfile`, `docker-compose.yml`, `scripts/webkvm.service`, `packaging/standalone/install.sh`, `docs/runbooks/RUNBOOKS.md`
- Historia: `CHANGELOG.md`, `AUDIT_IMPROVEMENTS.md`, `CONTEXT_NOTES.md`

## graphify

This project has a knowledge graph at graphify-out/ with god nodes, community structure, and cross-file relationships.

When the user types `/graphify`, use the installed graphify skill or instructions before doing anything else.

Rules:
- When graphify-out/graph.json exists, run `graphify query "<question>"` before your first grep, search or source-file read for a codebase question, and put this rule in every subagent prompt that explores code. Read raw files only for lines the graph or an earlier read already located. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- Dirty graphify-out/ files are expected after hooks or incremental updates; dirty graph files are not a reason to skip graphify. Only skip graphify if the task is about stale or incorrect graph output, or the user explicitly says not to use it.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).
- A committed graphify-out/graph.json is rewritten whole on every build, so a large diff to it in a pull request is expected. Do not flag it as churn or suggest skipping the graph rebuild or its hooks.
