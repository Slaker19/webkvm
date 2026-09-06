# Roadmap WebKVM — v1.2.0 "Estabilización"

> Estado de partida: `main` en `51898b1` + fixes de `FIXES.md` ya desplegados en
> la VM de staging (`webvm` 192.168.1.121).
> Evidencia técnica: `ANALISIS-CODIGO.md`, `APPLIANCES-AUDIT.md`, `FIXES.md`.
> Convención: cada ítem trae **Aceptación** (qué demuestra que está hecho),
> **Ficheros** (dónde se toca) y **Gate** (cómo se verifica en la VM real).
> Cada Solution contiene los pasos arquitectónicos acordados en el estudio de
> viabilidad v1.2 (3 auditorías paralelas + verificación de URLs en vivo).

## 1. Alcance v1.2 — IN / OUT

**IN:** P0 seguridad/datos, catálogo roto, release profesional, robustez
frontend, observabilidad mínima (audit+jobs).

**OUT → v1.3:** cuota fina async (A-04/A-05, AP-C4 re-check en job), JWT→cookie
HttpOnly, rate-limit global por IP, CORS fino, pool elegible en deploy,
odoo→https, retries de scripts, editor firewall UI, backups 2.0 (retention,
S3, verify), scheduler, tags/búsqueda global, historial de métricas + alertas.

**OUT → v2.0:** multi-host (solo preparar interfaces), `/api/v1`,
supply-chain firmada (cosign+SBOM), OIDC/SSO/2FA, e2e Playwright, fuzz del
parser XML de `<disk>` (mata A-11/B-08 de un golpe).

## 2. Sprint A — Datos y seguridad P0

### V12-DATA-01 · Colisión de deploy → 409 pre-I/O + lock por nombre (AP-C1) — ✅ v1.2
**Decisión:** fail-fast 409, NO auto-renombrado (el renombrado silencioso
rompe la predictibilidad de la API y el polling del frontend por nombre).
**Pasos:** 1) `Connector.DomainExists(name)` (`libvirt/domain.go`,
envolviendo `LookupDomainByName` como en `:2651`). 2)
`Connector.VolumeExists(pool, vol)` (`libvirt/storage.go`, envolviendo
`LookupStorageVolByName` como en `:293`). 3) `Handler.deployMu` +
`deployLocks map[string]*sync.Mutex` (package-level si hay riesgo de varias
instancias). 4) En `deployApplianceJob` (`:273`): lock por `vmName` (defer
unlock); pre-flight: `DomainExists` o `VolumeExists` → job `error` +
auditoría `vm.appliance_deploy_blocked` + return. 5) Test: doble deploy
concurrente del mismo nombre → exactamente 1 avanza.
**Aceptación:** 2× POST mismo nombre → uno `202` y otro job `error`
"already exists"; disco original intacto (hash antes/después).
**Ficheros:** `api/appliances.go:273,311,375`, `libvirt/domain.go:2641`,
`libvirt/storage.go:282`. **Talla:** M. **Gate VM:** deploys duplicados
secuenciales + concurrentes sobre `wordpress`; `qemu-img compare` del disco
previo.

### V12-DATA-02 · Cleanup post-rename en todo error tardío (AP-C2) — ✅ v1.2
**Pasos:** helper `removePoolImage(jobID, poolName, poolFileName)` →
`os.Remove(poolDest)` + best-effort `RefreshPool` (patrón `ova.go:872`),
invocado en: error de `qemu-img resize` (`:401`), `GetStorageVolume`
(`:411`, conservando el existente + añadiendo refresh) y error de
`CreateDomain` (`:429`); cada cleanup audita `appliance.deploy_cleanup`.
**Aceptación:** tres fallos provocados → cero volúmenes huérfanos en
`vol-list` tras refresh. **Talla:** S.

### V12-SEC-01 · Credenciales guest 0600 + política motd/bashrc (AP-C3) — ✅ v1.2
**Pasos:** 1) En los 16 scripts con `cat > /etc/webkvm-app.txt`:
`install -m 600 /dev/null /etc/webkvm-app.txt` antes del heredoc.
2) bashrc-append se mantiene (root-owned 0600); el `cat` del motd solo
para root. 3) Nota de migración: VMs ya desplegadas conservan 0644 (sin
remediación remota segura en v1.2; aviso en CredentialsModal + docs).
**Aceptación:** deploy `wordpress` → `stat -c %a /etc/webkvm-app.txt` = 600.
**Talla:** S. **Riesgo residual aceptado:** ficheros 0644 heredados.

### V12-SEC-02 · Blacklist JWT persistente (`revoked.json`) — ✅ v1.2
`config.RevokedFile()` (patrón `AuditLogFile:240`); `blacklist.go` con
`path`+`load/saveLocked` JSON `{key:exp}` `0600`+`Sync`; guardar en
`Revoke` y `gc`; corrupción → fail-open con `slog` + backup
`.corrupt-<ts>` (patrón `user/store.go:340`); `NewManagerWithPath`
retrocompatible; `main.go:250` lo usa. **Aceptación:** logout → restart →
token viejo 401. **Talla:** S.

### V12-SEC-03 · Password policy + rotación del secreto inicial — ✅ v1.2
Solo `validatePasswordStrength` (`store.go:444`): mín 12, denylist top-10k
(`go:embed`), 3/4 clases si <16; aplica a los 3 callers de golpe;
grandfather (no re-validar existentes). Borrar `admin-password.initial`
(`/ .reset`) SOLO al limpiar `MustChangePassword` (cambio efectivo, no al
login). **Aceptación:** `password`/`12345678` rechazados con mensaje con
requisitos; fichero inicial desaparece tras primer cambio. **Talla:** S.

### V12-SEC-04 · `POST|PUT /pools` admin-only + gate UI — ✅ v1.2
`router.go:298-299` al grupo admin (junto al `DELETE :310`);
`Storage.svelte:511-540` gate `auth.isAdmin()` con mensaje accionable.
Verificado: ningún flujo operator legítimo llama `createPool`.
**Aceptación:** operator → 403 con mensaje claro; admin crea dir+CIFS OK.
**Talla:** S.

## 3. Sprint B — Catálogo, release y ops

### V12-CAT-00 · Migración de store a layout v3 (requisito previo)
Subir `layoutVersion` a 3; en `load()`, builtins SIN override re-aplican
desde `Defaults`: `URL, SizeBytes, VCPUs, RAMMB, DiskGB, Notes, Format,
Compression` (nunca `ProvisionScript` si hay override). Test: store v2 con
URL vieja → arranque → URL nueva + override intacto. **Talla:** S.

### V12-CAT-01..06 · URLs corregidas (verificadas `curl -IL` 2026-09-05)
| ID | Cambio | SizeBytes nuevo |
|---|---|---|
| `debian-13` | `…/trixie/latest/debian-13-genericcloud-amd64.qcow2` (200) | 339214336 |
| `opensuse-leap` | `…/16.1/appliances/Leap-16.1-Minimal-VM.x86_64-Cloud-Build2.16.qcow2` (200) | 341735936 |
| `openmediavault` | `…/files/iso/8.3.1/openmediavault_8.3.1-amd64.iso/download` (200) + Notes "OMV 8" | 1244774400 |
| `vyos` | pin `2026.09.01-0034-rolling` (200) + Notes "revalidar trimestralmente" | 673185792 |
| `opnsense` | host → `pkg.opnsense.org` (misma ruta; CL coincide) | sin cambio |
| `pfsense-ce` | SIN drop-in (Netgate retiró hotlinks): Notes "descarga manual" + docs netgate-installer | sin cambio |

**Aceptación por plantilla:** `curl -IL` 200 (o política documentada) +
deploy real hasta `completed`. **Talla:** S en conjunto.

### V12-CAT-07 · Script `docker-ce` (repo apt oficial, NO pipe-to-sh)
Keyring `/etc/apt/keyrings/docker.asc` + `sources.list.d` noble +
`apt install docker-ce docker-ce-cli containerd.io docker-buildx-plugin
docker-compose-plugin` + `enable --now`. Repo noble verificado (200).
**Aceptación:** deploy → `docker compose version` OK. **Talla:** S.

### V12-CAT-08 · Validación de red pre-job + fallo cloud-init como error
`network ∈ ListNetworks` antes del job (400 inmediato);
`applyCloudInit` error → `updateJob(error)` (no `completed`+warning), sin
escribir `AppInfo`, VM conservada y documentada; mover `Validate` de
cloud-init ANTES de `storeJob` (cierra jobs `queued` zombis). **Talla:** S.

### V12-OPS-01 · Release profesional
`make release` + `.github/workflows/release.yml` (tag `v*` → `make dist
VERSION=${tag}` → `gh release create` con `*.tar.gz` + `SHA256SUMS` +
notes). Hace efectivos los fail-closed de `update.sh`/`install.sh`.
**Aceptación:** release de prueba con assets verificables. **Talla:** S.

### V12-OPS-02 · `CHANGELOG.md` (Keep-a-Changelog) + versión visible
Footer UI con `__APP_VERSION__` + enlace al changelog. **Talla:** S.

### V12-OPS-03 · `scripts/smoke.sh` e2e en VM
Login (cred por env) → VM nombre único → snapshot → power-cycle → delete
`?disks=true` → asserts + `trap` cleanup. Gate de merge manual hasta CI
self-hosted. **Talla:** S.

### V12-OPS-04 · Runbooks `docs/`
`install.md`, `upgrade-rollback.md`, `disaster-recovery.md`
(inventario DATA_DIR), `security-model.md`. **Talla:** M.

### V12-OPS-05 · Audit rotación 1..7 + fsync
`maxBackups=7` (`audit.go:21`); bucle rename en `rotateIfNeededLocked:64`;
`List:121` itera `.7..base`; `file.Sync()` tras `Flush` en `Log:102` +
`Sync` antes de `Close` en rotación. **Aceptación:** 10MB×2 → `.1..2`
presentes y leídos. **Talla:** S.

### V12-OPS-06 · Jobs con TTL (solo estados terminales)
Sweeper 5 min: purga `completed/error` >24h; NUNCA en-flight. **Talla:** S.

## 4. Sprint C — Frontend robusto

### V12-FE-01 · Fugas de pollers
`VmList:982` `onDestroy` + umbral ≥10× 404 seguidos → estado `error`
terminal (solo cuenta 404 de job; errores de red resetean el contador).
`Backup:105` `onDestroy(stopJobsPoller)` incondicional. `Storage:282`
cancelación al desmontar. `TerminalPanel` cancela el `setTimeout` de
backoff en `onDestroy:260`. **Talla:** S.

### V12-FE-02 · Doble-click guards (patrón `actionLoading`/`appSaving`)
`deployAppliance` (`disabled={!!appDeploying}`), `quickAction`
(`disabled={quickBusy}`), `createPool/createVolume/resizeVolume`
(`Storage:537,690,708` + flags), schedules (`Backup:385-442`),
`removeCover` simétrico. Regla: todo `async onX` con `await api.*` lleva
flag + `disabled` + spinner. **Talla:** S.

### V12-FE-03 · i18n completa del ámbito appliances
~60 keys `VmList` + ~18 `CredentialsModal` en en/es/ca simultáneos
(apóstrofes CA/ES vigilar) + `scripts/check-i18n.sh` en CI. **Talla:** S
(0.5–1 día).

### V12-FE-04 · Null-guards + vitest
`(s.name||'')` en `Settings:64,146`; `(search||'')` en búsquedas;
`vitest` solo `utils/` (`vmState`, series, guards) + paso en CI;
componentes con jsdom → v1.3. **Talla:** S.

## 5. Criterios de salida v1.2.0

1. Cero P0 abiertos (ítems 2–4 en done). 2. `smoke.sh` verde contra VM
limpia. 3. Tag `v1.2.0` con release + `SHA256SUMS` + `CHANGELOG.md`.
4. Deploys reales `completed` de las 6 plantillas tocadas. 5. `go vet`,
`go test -race`, `eslint`, `prettier --check`, `vite build`,
`check-i18n.sh` verdes.

## 6. Más allá de v1.2 (resumen)

- **v1.3 "Features"**: cuota fina async (re-check en job + real-size
  post-restore), JWT→cookie HttpOnly, rate-limit global, deploy con pool
  elegible, backups 2.0 (retention/verify/S3/SFTP known_hosts), editor
  firewall UI por reglas, scheduler (auto start/stop), tags + búsqueda
  global, historial de métricas + alertas (notify existente).
- **v2.0:** multi-host solo preparado (interfaz por nodo, placement
  dry-run), `/api/v1`, OIDC/SSO+2FA, supply-chain firmada, e2e Playwright,
  parser XML real para `<disk>` (A-11/B-08), fuzz de parsers.
- **LXC:** vía LXD nativo (no driver libvirt-LXC, muerto upstream):
  Fase 0 abstracción `ComputeBackend`, Fase 1 LXD completo, Fase 2
  catálogo containers, Fase 3 hardening. Detallado en la conversación de
  planificación; ubicación por decidir (v1.3 o v1.4).
