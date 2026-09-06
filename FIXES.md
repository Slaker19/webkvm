# FIXES — Correcciones aplicadas (2026-09-05)

## Sprint C (v1.3) — V13-C-03/C-04: Histórico de métricas (time-series) y Alertas (2026-09-06)

- **`metrics/history.go` (nuevo)** — almacén time-series con **Zero DB Bloat**:
  - **Nada va a la DB principal**: muestras (5s) se agregan en **buckets en memoria**
    por VM (minuto + hora) y se **flushean cada 60s a archivos append-only JSONL**
    independientes: `{dataDir}/metrics/history/<vmID>.jsonl` (minutos) y
    `metrics/rollup/<vmID>.jsonl` (horas), modo 0600. Nada toca el store principal.
  - **Downsampling**: resolución fina **por minuto (promedio)** para las últimas 24h;
    **rollup horario (promedio/max)** para 7/30 días. `History(vmID, window)`: ≤24h →
    minutos, >24h → horas.
  - **Retención acotada**: memoria podada a 24h/30d; ficheros recortados (rewrite
    ocasional ≤1×/día/VM). `Load()` rehidrata al reiniciar y marca `lastFlushed` →
    **append idempotente sin duplicados** tras restart. Flush en timer + al shutdown.
  - `MetricsCollector.SetSink` → alimenta el histórico y las alertas tras cada sample.
- **`metrics/alerts.go` (nuevo)** — máquina de estados **anti-spam**:
  - `idle → pending (al cruzar umbral) → FIRING (solo tras `duration`, por defecto 5m)
    → cooldown (1h) antes de re-disparar`. Un **pico de 1s no dispara nada**; un 0
    transitorio rompe la racha y reinicia el PENDING.
  - `AlertRule` (metric/threshold/above/duration/cooldown/enabled, VMID opcional
    "" = todas las VMs), persistidas en `{dataDir}/alerts.json` (0600, fichero de
    config diminuto, no métricas). Fire → `notifier.Record` (eventos + canales) +
    evento SSE `vm.alert`. `Active()` y `Statuses()` limpian estados huérfanos de
    reglas borradas/deshabilitadas.
  - Fix JSON: slices vacíos → `[]` (no `null`) en Active/Statuses/History.
- **API**: `GET /api/vms/{id}/metrics/history?window=24h|168h|720h`, `GET/PUT
  /api/vms/{id}/alerts` (admin), `GET /api/alerts/active`. `main.go`: wire store +
  engine + sink + flusher.
- **Frontend `VmDetail`**: nuevas pestañas **History** (selector 24h/7d/30d, gráficas
  **SVG nativo** reutilizando `Chart.svelte` — sin librerías pesadas) y **Alerts**
  (editor de reglas con umbral/dirección/duración/cooldown + estado en vivo
  idle/pending…/FIRING). i18n +28 claves ×3 (1301 cada uno, check-i18n OK).
- **Tests** (`-race`): history (buckets promedio, minutos ordenados, flush idempotente
  + append-only + 0600, restart sin duplicados, rollup horario, VM vacía), alerts
  (spike no dispara, firing tras duración, **cooldown suprime re-fire**, resolución a
  idle, below-threshold, scoping VM/global, persistencia 0600, invalid refused).
- **Gate real en VM (v1.3.0-c04)**: VM fedora encendida → history 24h/720h poblados ✓
  · regla guardada ✓ · **PENDING→FIRING live** ✓ · `/alerts/active` muestra la alerta ✓
  · al borrar la regla → activas a 0 ✓ · **flush JSONL verificado en disco** (minutos
  427 B + rollup horario) ✓ · VM apagada.

## Sprint C (v1.3) — V13-C-01/C-02: Editor de Firewall (host) con Safe Apply y templates (2026-09-06)

- **`firewall/host.go` (nuevo)** — firewall a nivel de HOST:
  - `HostInputRule` (proto/port/src/action) para el tráfico hacia el host y
    `HostForwardRule` (DNAT host:port → guest IP:port) para tráfico hacia VMs.
  - `HostStore` persiste el ruleset CONFIRMADO en `firewall-host.json` (0600).
  - `ValidateHostFirewall` + **anti-lockout (crítico)**: se rechazan las reglas
    `drop` sobre puertos de gestión (`ProtectedPorts` = 22, puerto UI, 5900-5903);
    además la cadena input mantiene `policy accept` y **rails ACCEPT implícitos y
    no borrables** ANTES de cualquier regla. Doble línea de defensa: imposible
    bloquearse el acceso a la propia herramienta.
- **`firewall/nft.go`**:
  - `BuildRuleset` (refactor → `buildRulesetWith`) incorpora reglas host: rails +
    reglas por-VM + reglas input del host en la cadena `input`; forwards del host +
    por-VM en `prerouting`; masquerade agregada por IP destino.
  - **Aplicación atómica**: `nft -c` (check) + `nft -f` sobre archivo temporal;
    un error de sintaxis aborta toda la transacción sin tocar nada (ya era así).
  - **Fix de bug real preexistente**: `allow` no es veredicto válido de nft
    (`nftVerdict` traduce `allow`→`accept`). Las reglas "allow" de antes habrían
    fallado el `nft -c` en producción. Ahora se emiten como `accept`.
  - **Safe Apply (V13-C-01)**: `StageHostApply` (valida → aplica → inicia ventana
    de 30s + timer de rollback), `ConfirmHostApply` (persiste + cancela timer),
    `RollbackHostApply` (restaura el ruleset anterior). Solo una aplicación en
    vuelo (single-flight); un `Apply()` por-VM no interfiere con el estado.
- **API** (`/api/firewall/host`, admin): `GET /` (rules + protected_ports +
  pending), `POST /preview` (render nft sin tocar nada), `POST /apply`
  (`pending_confirm` + deadline), `POST /confirm`, `POST /rollback`. Audit para
  apply/confirm/rollback. `main.go`: `NewHostStore` + `SetHostStore` + apply al
  arranque (sobrevive reinicios).
- **Frontend `Firewall.svelte` (nuevo)**:
  - Secciones **claramente separadas**: "Forward (VM traffic)" y "Input (traffic
    to this host)", cada una con tabla (proto/port/src/action o host/guest),
    añadir/eliminar/reordenar (up/down).
  - **Rails de gestión bloqueados**: franja "Always open (management ports)" con
    candado, no editable.
  - **Safe Apply UI**: barra con countdown (30s) y botones Confirm/Rollback; si el
    deadline expira sin confirmar, recarga mostrando el ruleset restaurado.
  - **Templates (C-02)**: presets web-server / ssh-gateway / media (util puro
    `firewallTemplates.js` + vitest), botón "Preview rules" con el nft exacto.
  - Ruta `/firewall` (admin) + nav + `nav.firewall` + bloque `firewall` i18n
    **+50 claves ×3 idiomas (1279 cada uno, check-i18n OK)**.
- **Tests** (`host_test.go`, `-race`): anti-lockout (drop en puertos protegidos
  rechazado, allow permitido), malformed reject, ruleset host (src ip saddr +
  dnat + masquerade + rails primero), Safe Apply confirm persiste / **timeout
  150ms auto-rollback** / single-flight / sin pending, `HostStore` 0600 + reload.
  Frontend: vitest `firewallTemplates` (templates, moveRule, ids únicos) → 14/14.
- **Gate real en la VM (v1.3.0-c02)**: GET protected_ports ✓ · preview (allow→
  accept, dnat) ✓ · apply→pending ✓ · confirm persiste y regla en el kernel
  (`nft list` muestra `dport 8443 accept`) ✓ · **drop en :22 → 400 anti-lockout** ✓
  · rollback manual restaura [8443] ✓ · **auto-rollback tras 30s sin confirmar** ✓
  · cleanup a ruleset vacío (tabla eliminada) ✓.

## Sprint B (v1.3) — V13-BCK-06: API & Frontend (formulario S3, retention UI, verifyOnWrite, fingerprints, secretos preservados) (2026-09-06)

- **API `backup.go`**:
  - `backupTargetCreateRequest` + handler de create: campos **S3** (`bucket`, `region`,
    `endpoint`, `access_key`, `secret_key`), **`known_hosts`** (allowlist de fingerprints
    SFTP), **`verify_on_write`**.
  - Update: campos `*string`/`*[]string`/`*bool` para S3/KnownHosts/VerifyOnWrite + 
    **`clear_secret`** explícito.
  - **Secrets (crítico)**: `resolvedSecret(v, existing)` + `isSecretMask` — un campo de
    credencial **omiti­do, vacío o enmascarado** (`""`, `••••`, `****`) CONSERVA el secreto
    almacenado; solo un valor real lo reemplaza. `clear_secret=true` es la única vía de
    borrado (nunca un blank implícito). La retención se preserva en updates parciales
    (si el body no trae `retention`, se mantiene la existente).
  - Store `UpdateTarget`: fusión **preserve-on-blank** por capa (defensa en profundidad
    con `firstNonEmpty`); `ClearSecret` ahora es decisión explícita.
  - `TestBackupTarget`: rama **S3** (`backupstore.TestS3`, lista sin escribir).
- **Frontend `Backup.svelte`**:
  - Tipo **S3** en el dropdown + formulario completo (Endpoint, Region, Bucket,
    Access/Secret key, prefijo de objeto opcional; las credenciales usan placeholder
    `••••••••` al editar y **no se reenvían si no cambian**).
  - SFTP: textarea **known host fingerprints** (1 por línea) + hint de pegar la huella
    que devuelve "Test connection".
  - Toggle **Verify after upload** (para sftp/s3).
  - Retención: añadidos **KeepDaily/KeepWeekly/KeepMonthly** junto a KeepLast/KeepDays.
  - Edit prefill de todos los campos; validación relajada para S3 (bucket + endpoint|región).
- **i18n**: +26 claves por idioma (s3*, knownHosts*, verifyOnWrite*, retentionKeep{Daily,
  Weekly,Monthly}, verifyTimeout/Failed/At, s3BucketRegionRequired, etc.) en `en`/`es`/`ca`
  → **1225 claves por idioma** (check-i18n OK, paridad exacta).
- **Gates**: script API real en la VM — create S3 ✓, update con blank/mask ✓ (http 200),
  **secreto AKIA123 conservado en `sftp-secrets.json`**, **0 ocurrencias en `targets.json`**
  (sin leaks), `verify_on_write` persistido a false (omitempty). Frontend lint/prettier/
  vitest/build verdes.

## Sprint B (v1.3) — V13-BCK-05: SFTP known_hosts estricto (eliminado el TOFU ciego) (2026-09-06)

- **`sftp.go`**: eliminado el pinning TOFU en memoria (`pinnedHostKeys`). Nuevo
  `hostKeyCallbackFor` **estricto**:
  - Target configurado **sin fingerprints** → conexión **REFUSED** con error que
    incrusta la clave presentada (`ssh-ed25519 SHA256:…`).
  - Fingerprint presentado que **no coincide** con el allowlist → REFUSED mostrando el
    presentado vs los configurados.
  - Match contra `configuredFingerprints` (tolera `SHA256:…` suelto, línea completa de
    ssh-keyscan, listas por espacio/nueva línea).
  - Dial de **Test connection** (tgt.ID=="") permitido y **registra la huella presentada**
    (`LastPresentedHostKey`) → `TestSFTP` la añade al mensaje de éxito para que el
    operador la copie al campo known_hosts.
  - `TargetOptions`/`Target` ganan **`KnownHosts []string`** (persistido en targets.json,
    no es secreto).
- **Tests** (`sftp_test.go`): sin fingerprints → rechaza y muestra huella; mismatch →
  rechaza; match (suelto/línea completa/lista) → acepta; ad-hoc test → permite y registra;
  parsing de `configuredFingerprints`.

## Sprint B (v1.3) — V13-BCK-04: Verify (streaming, fail+purge en VerifyOnWrite, on-demand async + LastVerified) (2026-09-06)

- **Streaming sin RAM**: `sftpVerify` pasa a `io.Copy(sha256, remoteStream)` (el loop
  manual anterior tragaba errores de lectura a mitad de stream y devolvía un checksum
  truncado). `s3Verify` ya usaba `io.Copy`. Ningún fichero se descarga a disco ni se
  carga en RAM; el hashing local del staging también es `io.Copy` (`sha256LocalFile`).
- **VerifyOnWrite → job FAIL + purge**: `runJob` verifica el archivo primario (config
  tar, `primaryOf`) ANTES de marcar success y con el staging en disco: `verifyWrittenPrimary`
  compara el sha256 remoto (stream) contra el local; en mismatch **el job falla**, se
  purga la copia remota corrupta (`DeleteBackupFile`), se registra `RecordVerification(ok=false)`
  y se loguea `backup_verify_failed`. Toggle **por target** (`Target.VerifyOnWrite`,
  expuesto en API+UI).
- **Verify On-Demand async**: `GET /{id}/verify` devuelve **202** `{status:verifying}` e
  inmediato; una goroutine calcula el hash y persiste el resultado (`RecordVerification`).
  El listado de ficheros lo adjunta vía `Store.AttachVerified` → `last_verified` /
  `sha256` (última verificación OK) / `last_verify_error`. `verified.json` (0600) en
  `{dataDir}/backup/` sobrevive a reinicios. `BackupFile` gana `LastVerified` /
  `LastVerifyError`.
- **Frontend**: `verifyFile`/`verifyConfig` disparan el 202 y **pollan** la lista de
  ficheros hasta que `last_verified` ≥ el momento del clic (o `last_verify_error`),
  con timeout de 90s; el diálogo de resultado muestra `Verified at`. `ListBackupsOnTarget`
  adjunta verificaciones también al snapshot de configuración.
- **Tests**: `verify_test.go` (corrupción simulada unitaria: copia remota con byte
  alterado → mismatch detectado + **purgada**; intacta pasa), `verified_test.go`
  (persistencia + reload + 0600 + sin secretos), **live** `verify_live_test.go`
  (`TestVerifyCorruption_AgainstMinIO`: upload → verify OK → **tamper real del objeto** →
  mismatch → objeto purgado). **Gate ejecutado en la VM** contra MinIO real: PASS.

## Sprint B (v1.3) — V13-BCK-03: Retention (prune inline async + janitor desacoplado) (2026-09-06)

- **`backupstore/retention.go` (nuevo)** — motor de retención:
  - **Decisión PURA** `decideRetention(policy, now, runs)`: sin I/O, 100%
    testeable. Ordena los runs **estrictamente por timestamp descendente**
    (más nuevo → más viejo) y aplica las reglas en **OR**: `KeepLast`,
    `KeepDays` y las nuevas franjas **`KeepDaily`/`KeepWeekly`/`KeepMonthly`**
    (conserva los N runs más nuevos por bucket de día/ISO-week/mes).
    **Bucketing 100% UTC** (sin mezclar zonas).
  - `RetentionPolicy` (store.go) gana `KeepDaily`/`KeepWeekly`/`KeepMonthly`;
    `Enabled()` los incluye.
  - `ApplyRetention` reescrito sobre `decideRetention`; borra runs como
    unidades completas; **un fallo por run se loguea y se continúa**
    (idempotente: los huérfanos de un borrado parcial se re-podan el
    siguiente ciclo, sin panic ni bloqueo de cola).
  - **Janitor** `StartRetentionJanitor(ctx, 6h, store, logger)`: goroutine
    dedicada, desacoplada del cron ticker que dispara los jobs; `sweepRetention`
    visita todos los targets y **un fallo o panic en un target nunca aborta
    el ciclo** (aislamiento por target con recover + log).
  - `DeleteBackupRun` (store.go) gana la rama `s3` (`s3DeleteRun`).
- **Runner**: la poda inline tras un job pasa a **asíncrona** (goroutine que
  no toca `r.mu`) — un S3/SMB lento nunca bloquea la finalización del job ni
  el ticker de cron.
- **main.go**: `StartRetentionJanitor(eventCtx, 6h, backupStore, logger)` +
  log `retention_janitor_started`.
- **Tests** (`retention_test.go`, `-race`):
  - `TestDecideRetention_MonthlyKeepsNewestPerMonth`: **30 runs en 3 meses** →
    `KeepMonthly=1` retiene EXACTAMENTE el más nuevo de cada mes (3), nada más.
  - `TestDecideRetention_DailyAndWeekly` (1 por día / 1 por ISO-week),
    `TestDecideRetention_CombinedOR` (OR entre reglas), `TestDecideRetention_Disabled`,
    `TestRetentionBucket_UTC` (buckets UTC consistentes).
  - `TestApplyRetention_PrunesOldLocalRuns` (local, mtimes explícitos),
    `TestSweepRetention_IsolatesFailures` (**target que falla no aborta el
    sweep; el siguiente se procesa**).
  - **Live** `retention_live_test.go` (gated `MINIO_TEST_*`):
    `TestRetention_AgainstMinIO` — sube 3 runs a un MinIO real, `KeepLast=1`
    poda 2 y deja EXACTAMENTE el más nuevo en el bucket. **Gate ejecutado en
    la VM** (MinIO efímero 127.0.0.1:19000): PASS; bucket limpiado; MinIO
    apagado.
- Gates: `golangci-lint` 0 issues, `go build/vet` OK, `go test -race ./...`
  verde (solo los 2 `backupstore` root preexistentes), frontend + `check-i18n`
  verdes. Binario `v1.3.0-bck03` desplegado en la VM; janitor confirmado en
  journal (`retention_janitor_started interval=6h`).

## Sprint B (v1.3) — V13-BCK-02: Transporte S3 + integración al Runner (2026-09-06)

- **`internal/backupstore/s3.go` (nuevo)** — transporte S3 con
  `github.com/minio/minio-go/v7` (dependencia añadida):
  - **Streaming/RAM**: `s3UploadRun` pasa un `*os.File` a `PutObject` —
    cada archivo multi-GB se lee del disco (multipart implícito), nunca se
    carga en RAM. `srcStatSize` para tamaño o -1 (EOF).
  - **Contextos**: todas las llamadas (`s3EnsureBucket`, `s3UploadRun`,
    `s3List`, `s3Verify`, `s3Delete`, `s3DeleteRun`, `s3StageFileForRestore`,
    `stageS3Files`) reciben/respetan `context.Context`; un job cancelado o
    una red caída abortan sin colgar la goroutine. El runner pasa el `ctx`
    del job.
  - **Saneamiento de object keys**: `s3ObjectKey` elimina `/` iniciales,
    `//` dobles, `.` y `..` (anti-traversal) → rutas relativas limpias
    (`prefijo/backup.tar.gz`). `isS3NotFound` mapea NoSuchKey/Bucket → 404.
  - `s3ClientFor`: endpoint custom (MinIO/R2/B2, no requiere Region) vs AWS
    nativo (`s3.amazonaws.com` + Region); credenciales de `TargetSecret`;
    `s3EnsureBucket` crea el bucket si no existe (idempotente).
  - `s3CleanupTestObjects` para dejar el store de test limpio.
- **Runner** (`runner.go`): `runJob` ahora etapa el run en local (como SFTP)
  y sube por S3 con el `ctx` del job; `ListBackupsOnTarget`, `VerifyBackup`,
  `DeleteBackupConfig`, `LatestConfigInfo` y `RestoreRun` ganan ramas `s3`
  (`s3List`/`s3Verify`/`stageS3Files`). `DeleteBackupFile` (store.go) →
  `s3Delete`. `StageFileForRestore` (sftp.go) delega a `s3StageFileForRestore`.
- **Tests**:
  - Unitarios (`-race`): sanitización de keys (6 casos), validación de
    `s3ClientFor`, upload con contexto cancelado no se cuelga, upload lee
    de disco.
  - **Live (gated por env `MINIO_TEST_*`)** `s3_live_test.go`:
    `TestS3Transport_AgainstMinIO` (ensure bucket → upload streaming 4 MiB →
    list con nombre relativo limpio → verify sha256 coincide → delete-run →
    cleanup) y `TestS3_ObjectPersistsAcrossClients` (lectura con cliente
    minio-go independiente). **Gate ejecutado contra MinIO efímero real en
    la VM** (binario MinIO en 127.0.0.1:19000): ambos PASS; bucket creado y
    limpiado al final (sin basura). MinIO apagado tras el gate.
- `golangci-lint` 0 issues (corregido un govet tautológico en runJob);
  `go build/vet` OK; `go test -race ./...` verde (solo los 2 `backupstore`
  root preexistentes).

## Sprint B (v1.3) — V13-BCK-01: Modelo S3 (Backups 2.0) (2026-09-06)

- **`backupstore/store.go`**:
  - Nuevo `TargetType = "s3"` (cualquier store compatible S3: AWS, MinIO, R2,
    B2). `Target` gana `Bucket`/`Region`/`Endpoint` (con `json:"omitempty"`).
  - **Aislamiento de secretos**: `TargetSecret` gana `AccessKey`/`SecretKey`
    con tags `json:"access_key/secret_key"`; `Target.Secret` sigue siendo
    `json:"-"` → nunca se serializa a la API, y las credenciales viven solo
    en el fichero de secrets separado (0600), nunca en `targets.json`.
  - **Validación estricta** en `CreateTargetOpts`: para `s3`, `Bucket`
    obligatorio; si `Endpoint` está vacío (AWS nativo) `Region` obligatoria;
    `AccessKey`+`SecretKey` obligatorias. `Path` se vuelve opcional para s3
    (es un prefijo de objeto, no una ruta local). El switch de tipos valida
    por rama (`sftp`, `s3`, local/nfs/smb, vacío→local, default→error).
  - `CreateTargetOpts` mapea los campos S3 y guarda los secretos S3 en el
    fichero de secrets. `UpdateTarget` actualiza campos S3 y credenciales
    por tipo, y trata `Path` como remoto para `s3` igual que `sftp`.
  - **Retrocompat**: `local/nfs/smb/sftp` intactos; el store (re)carga los
    targets nuevos sin romper los existentes.
- **Tests** (`s3_model_test.go`, `-race`):
  - MinIO válido (endpoint custom sin Region), AWS nativo requiere Region,
    bucket obligatorio, credenciales obligatorias.
  - **`TestSecretsNeverSerialized`**: serialización del target + `targets.json`
    NO contienen `AccessKey`/`SecretKey`; el fichero de secrets SÍ los guarda.
  - `TestS3TargetPersistsAcrossReopen`: el target S3 y sus secretos sobreviven
    al reload del store (y los targets existentes siguen ahí).
  - `TestUpdateS3Target`: campos S3 y credenciales actualizables por separado.
- `golangci-lint` 0 issues; `go build/vet` OK; `go test -race ./...` verde
  (solo los 2 `backupstore` root preexistentes).

## Sprint A (v1.3) — V13-DATA-02: Deploy con pool elegible + cierre del Sprint (2026-09-06)

- **Backend — `DeployAppliance` (`appliances.go`)**:
  - El payload acepta `pool`; vacío → default `config.DiskPoolName`.
  - **RBAC estricto**: ANTES de encolar ningún job, `assertPoolAllowed(u, pool)`
    (403 si el pool no está en `AllowedPools`; admins exentos).
  - **Pre-flight del pool** (`poolExistsActive`): el pool debe existir Y estar
    `active` en libvirt → `400` limpio si no existe o está inactivo (`503` si
    libvirt falla). Sin jobs zombie.
  - La cuota de disco se evalúa contra el pool ELEGIDO (no solo el default),
    y el job recibe `poolName` (usado por `verifyDeployTargetFree`,
    `GetPoolPath` y el re-check de V13-DATA-01).
- **Backend — `ListPools` (`storage.go`)**: filtra por `AllowedPools` del
  llamante no-admin (defensa en profundidad) → el dropdown del frontend solo
  muestra pools elegibles.
- **Frontend — `VmList`**: selector de Storage Pool en el modal de deploy
  (`appPools`/`deployPools`), alimentado por `api.listPools()` (no-iso, ya
  filtrado por RBAC en el backend), preselección del primer pool disponible,
  y `body.pool` en el deploy. Claves i18n `vms.deployPool` /
  `vms.deployPoolDefault` en en/es/ca.
- **Gates VM** (`v1.3.0-data02`):
  - A) Usuario restringido (AllowedPools=[webkvm-disks]): `ListPools` →
    `['webkvm-disks']` ✔
  - B) Deploy a pool NO permitido (`ISOS`) → **403** ✔
  - C) Deploy a pool inexistente por usuario restringido → **403** (RBAC
    primero) ✔
  - D) Deploy a pool inexistente por admin → **400** ✔
  - E) Deploy al pool permitido → **202** → VM creada ✔
  - F3) Deploy a pool inactivo (`pool-destroy`) → **400** ✔
  - Artefactos de test limpiados (VM, usuario, pools); sin huérfanos.
- **Cierre del Sprint A (v1.3)**: SEC-01 (cookie HttpOnly + CSRF), SEC-02
  (rate-limit global), DATA-01 (cuota fina async) y DATA-02 (pool elegible)
  completos. Pasada final: `golangci-lint` 0 issues, `go build/vet` OK,
  `go test -race ./...` verde (solo los 2 `backupstore` root conocidos),
  `eslint`/`prettier --check`/`vitest`/`vite build`/`check-i18n` verdes.
  Binario desplegado en la VM: `v1.3.0-data02`.

## Sprint A (v1.3) — V13-DATA-01: Cuota fina async + re-check anti-TOCTOU (2026-09-06)

Cierra la deuda A-04/A-05: el pre-flight verificaba la cuota con un tamaño
*estimado*; ahora, al materializar el disco, se re-verifica contra el tamaño
**real** que el volumen ocupa en el pool.

- **`quota.go`** — helpers nuevos:
  - `realVolumeDiskGB(pool, vol)`: consulta libvirt (`GetStorageVolume`) y
    devuelve el tamaño REAL en disco (`Allocated`, con fallback a `Capacity`)
    redondeado al alza vía `bytesToGB` (que ya existía).
  - `recheckDiskQuota(owner, pool, vol)`: decisión anti-TOCTOU — lee el
    tamaño real + el uso actual del owner y aplica `enforceDiskQuota`.
    Thread-safe: lee snapshots vivos de libvirt, sin estado mutable
    compartido; fail-closed si no se puede verificar. Admin/roles exentos.
- **`appliances.go` — `deployApplianceJob`**: tras registrar el volumen (y
  tras decompresión + resize) y ANTES de `CreateDomain`, se ejecuta el
  re-check. Si excede: `removePoolImage` (V12-DATA-02) destruye el volumen
  inmediatamente, el job pasa a **`error`** con mensaje explícito
  ("Cuota excedida tras la materialización del disco. Recursos limpiados
  por seguridad.") y se registra `appliance.deploy_quota_rollback` en el
  audit (con el error detallado). Sin VM, sin huérfano.
- **`vms.go` — import**: tras asignar owner, se re-chequea el disco real del
  VM importado; si excede → `DeleteDomain` + `DeleteVMDiskFiles` + 409
  ("Cuota excedida tras la importación del disco. Recursos limpiados por
  seguridad.") + audit `vm.import_quota_rollback`.
- **Tests** (`quota_async_test.go`):
  - `TestRecheckDeployQuota_Race` (`-race`): 48 goroutines evalúan
    simultáneamente el escenario (usuario a 9/10 GB, disco real de 2 GB →
    exceso) y el caso límite (real exacto → permitido); todas coinciden.
  - `TestRecheckDiskQuota_FailClosedNoLibvirt`: sin libvirt → error cerrado
    (nunca concede ni paniquea).
  - `TestRecheckDiskQuota_AdminExempt`.
- **Gates VM** (`v1.3.0-data01`, libvirt real):
  - **A (sin falso positivo):** q2 (quota 6) despliega alpine-3.24 → job
    `completed`, VM creada.
  - **B (exceso → rollback):** q1 (quota 1) crea una VM de 1 GB (cur=1);
    appliance custom `gate-big` con DiskGB=0 (pre-flight 1+0=1 pasa);
    tras materializarse el disco real (~1 GB) → re-check 1+1=2 > 1 → job
    **`error`** con el mensaje exacto, **sin volumen huérfano** en el pool,
    **sin VM** creada, audit `appliance.deploy_quota_rollback`
    ("q1 uses 2/1 GB disk (global)").
  - Artefactos de test limpiados (usuarios q1/q2, VMs, appliance, volúmenes).
- `golangci-lint` 0 issues; `go test -race` verde (solo los 2 `backupstore`
  conocidos del entorno root).

## Sprint A (v1.3) — V13-SEC-02: Rate-limit global por IP (2026-09-06)

- **`internal/auth/ratelimit_global.go` (nuevo)** — token bucket por IP
  (`golang.org/x/time/rate`), dependencia directa añadida a `go.mod`:
  - Mapa `buckets map[string]*ipRateBucket` protegido por `sync.Mutex`; cada
    bucket guarda `rps/burst` de creación y se **reconstruye si el operador
    cambia los parámetros** (la nueva política aplica de inmediato a buckets
    existentes, no se queda con parámetros stale).
  - **Sweeper** en goroutine independiente (ticker 5 min) que purga buckets
    inactivos > TTL (15 min) → el mapa no crece sin límite bajo escaneos/DDoS.
    `sweepOnce(now, ttl)` testeable.
  - **Exenciones evaluadas ANTES de consumir tokens**: peticiones Bearer
    (tokens API/sesión válidos — el middleware de JWT corre antes y 401 los
    inválidos) y CIDRs de confianza (loopback + env `WEBKVM_TRUSTED_RATELIMIT_CIDRS`
    + `server.trusted_cidrs` vivo del configstore). Montado en `router.go`
    DESPUÉS de `authMgr.Middleware`, excluyendo no-`/api/` y `/api/health`.
  - **Estándares HTTP**: `429 Too Many Requests` con `Retry-After` +
    `X-RateLimit-Limit/Remaining/Reset` en cada respuesta.
  - Config en configstore (hot-reload): `server.rate_limit_enabled` (true),
    `server.rate_limit_rps` (50), `server.rate_limit_burst` (100) — generosos
    para cargas de página de flotas grandes, con rebuild de buckets.
  - Refactor DRY: helpers compartidos de IP/CIDR en `ratelimit.go`
    (`isTrustedPeer`, `clientIPOf`) usados por ambos limiters.
- **Tests** (`ratelimit_global_test.go` + `ratelimit_integration_test.go`):
  agotamiento+recuperación, headers estándar, exención CIDR (settings + env),
  exención Bearer, disabled, no-API/health exentos, sweep purga buckets,
  **concurrencia con `-race`** (32 workers × 50 requests, mapa coherente y
  vacío tras sweep), aislamiento por IP, **rebuild por cambio de params**,
  y wiring real configstore→limiter (X-RateLimit-Limit=1 con burst=1).
- **Gates VM** (`webvm`, binario `v1.3.0-sec02`):
  - Con burst=1/rps=1: cookie session → `200` luego **`429`** (Retry-After +
    X-RateLimit-*); IPs distintas aisladas ✔
  - **Exención Bearer**: token API real (`wvmb_…`) en ráfaga → **200 siempre**
    (cookie al mismo tiempo → 429) ✔
  - Cambio de settings (burst 100→1) re-aplica a buckets existentes ✔
  - Settings restaurados a 50/100; tokens de prueba eliminados ✔
  - Debug temporal (log de `params()`/del middleware) usado para diagnosticar
    el peer IP real y el comportamiento de buckets — **eliminado** del código.
- `golangci-lint` 0 issues; `go test -race` verde (solo los 2 `backupstore`
  conocidos del entorno root).

## Sprint A (v1.3) — V13-SEC-01: JWT → cookie HttpOnly (2026-09-06)

- **Backend — `internal/auth/cookie.go` (nuevo)**: helpers de cookies de
  sesión + CSRF. `webkvm_session` (HttpOnly, SameSite=Lax, Secure por
  config, Max-Age = TTL) y `webkvm_csrf` (no-HttpOnly, double-submit).
  `SessionToken`, `CSRFValue`, `Set/Clear SessionCookie`, `NewCSRFValue`,
  `CSRFValid` (constant-time), `IsUnsafeMethod`.
- **`jwt.go` — Middleware**: acepta el JWT de sesión desde la cookie
  (`viaCookie`) además del `Authorization: Bearer` (API tokens / scripting,
  sin cambios). CSRF obligatorio para métodos de mutación (POST/PUT/PATCH/
  DELETE) bajo `/api/*` autenticados por cookie: `X-CSRF-Token` debe
  coincidir con la cookie `webkvm_csrf`. `/api/auth/login` exento
  (pre-sesión); Bearer exento (no es atacable por CSRF). `Manager` gana
  `SetSecureCookies`/`SecureCookies`.
- **`api/auth.go`**: Login/Refresh emiten las cookies y **dejan de
  devolver el JWT en el JSON** (`LoginResponse` sin `token`, con `csrf`);
  Logout lee el token de header o cookie, revoca y borra ambas cookies.
- **`config`**: `SecureCookies` (default `true`, `WEBKVM_COOKIE_SECURE=0`
  para LAN sin TLS) + helper `envBoolFrom`. `main` lo aplica al Manager.
- **`router.go` CORS**: header `X-CSRF-Token` permitido; `AllowCredentials`
  true solo con origins explícitos (no wildcard).
- **Frontend**: `auth.svelte.js` re-arquitectado — el estado pasa de
  `tokenState` (localStorage) a `status ('checking'|'in'|'out')` + identidad
  no-secreta. `bootstrap()` re-valida la sesión vía `/auth/me`; `request()`
  usa `credentials: 'include'` + `X-CSRF-Token` en mutaciones; XHR de
  uploads/imports y fetches de export/logs usan cookies; `logout()`
  limpia cookie en servidor. `events.svelte.js`: SSE por cookie directo
  (`/api/events`), sin ticket dance. `App.svelte` (spinner de bootstrap +
  gating por status), `Login.svelte` (setSession + csrf), `Account.svelte`,
  `Layout.svelte`, `VmDetail.svelte` (download .rdp/.vv con cookie).
- **Tests**: `auth/middleware_cookie_test.go` (CSRF double-submit: sin/
  mal/correcto header, GET exento, Bearer exento, login público, `CSRFValid`,
  `NewCSRFValue`) y `api/login_cookie_test.go` (login → cookie HttpOnly +
  SameSite=Lax + sin token en body + csrf en cookie/cuerpo + JWT validable).
- **Gates VM** (`webvm` 192.168.1.121, binario `v1.3.0-sec01`):
  - Login HTTP 200 → `Set-Cookie: webkvm_session=…; HttpOnly; Secure;
    SameSite=Lax` y `webkvm_csrf`; body **sin** `token`, con `csrf` ✔
  - POST mutación con cookie SIN header CSRF → **403** ✔
  - POST con CSRF incorrecto → **403** ✔
  - POST con cookie `webkvm_csrf` + header correcto → **201** (grupo creado
    y limpiado) ✔
  - GET con cookie → **200** ✔
  - UI chromium headless: login page renderiza, bootstrap sin errores ✔
  - En la VM (HTTP puro) se aplicó el drop-in `WEBKVM_COOKIE_SECURE=0`;
    el default de producción sigue siendo `Secure=true`.

## Sprint C (roadmap v1.2) — Frontend robusto: V12-FE-01..04 (2026-09-06)

- **V12-FE-01 · Fugas de pollers/timers** — cada `setInterval`/`setTimeout`
  se cancela en `onDestroy`:
  - `VmList`: `stopAppPoller()` en `onDestroy`; el poller de jobs de
    appliance aborta tras **10 errores 404 consecutivos** (el job ya no
    existe en el backend, p.ej. purgado por el sweeper de 24 h) y **los
    fallos de red genéricos resetean el contador** (sin falsos positivos);
    toast + estado `error` con la clave `vms.applianceJobGone`.
  - `Backup`: `forceStopJobsPoller()` en `onDestroy` (la versión normal
    mantiene el poll con job activo, pero el unmount lo fuerza).
  - `Storage`: `downloadInterval` subido a scope de componente + limpieza
    en `onDestroy` (antes era local a `startDownload` y filtraba tras
    navegar).
  - `TerminalPanel`: registro `later()` para TODOS los timers (reconnect
    con backoff, restart 250 ms, fit ×3) cancelados en `onDestroy`.
  - `Status`: timers de feedback (restart/backup) con `later()` + cleanup.
  - `VmDetail`: `later()` para deep-link serial, reset de notas y cierre
    de modal de export + `onDestroy`/cleanup.
  - `CommandPalette`, `KeyboardShortcuts`, `SearchInput`, `PasswordModal`,
    `CredentialsModal`: timers de focus/copy/debounce cancelados.
  - `TaskCenter` ya limpiaba (`return () => clearInterval(iv)`).
- **V12-FE-02 · Doble-click guards** — mutaciones envueltas con flag
  booleano síncrono (`disabled` + spinner), con re-entry guard al inicio:
  - `VmList`: `quickAction` (start/shutdown/forceoff/clone/template) con
    `quickBusy` por clave + botones `disabled` + `Spinner`; `deployAppliance`
    con `deployBusy` (el diálogo de confirmación sigue abierto durante el
    await — antes un doble-click lanzaba dos deploys).
  - `Storage`: `poolCreating`/`volCreating`/`volResizing` en createPool,
    createVolume y resizeVolume.
  - `Users`: `userAdding`/`userSaving` en addUser y saveEdit.
  - `Backup`: `runBackup` con guard síncrono por target (`runningBackups`).
  - `VmCreate`/`VmDetail`/`Networks`/`Settings` ya disponían de flags
    (`loading`, `actionLoading`, `saving`, `restarting`, …) — verificados.
- **V12-FE-03 · i18n completo + check en CI** —
  - Claves faltantes añadidas a en/es/ca: `common.saving`,
    `storage.isoSectionTitle`, y `vmCreate.cloudInit{Label,Helper,Enable,
    User,Hostname,SSHKey}` (existían bajo `networks.*`, no bajo `vmCreate`),
    más `vms.applianceJobGone` (poller FE-01). Escapado correcto de
    apóstrofes en ca (`Activa l'aprovisionament…`) y comillas dobles para
    el texto con apóstrofe.
  - `scripts/check-i18n.mjs` (comparación por brace-matching de los tres
    bloques) + wrapper `scripts/check-i18n.sh` que falla con exit 1 en
    claves asimétricas (verificado rompiendo simetría). Integrado en
    `.github/workflows/ci.yml` (job frontend).
  - Verificado: las 876 claves literales usadas por los componentes
    resuelven en el diccionario.
- **V12-FE-04 · Null-guards + Vitest (utils only, sin jsdom)** —
  - `Settings`: `sections` defiende ante schema sin `section` (`|| 'general'`),
    `(s.name || '').toLowerCase()`, `schema?.fields?.find`, label con fallback.
  - `VmList`: `appDeleteTarget?.name || ''` en el confirm de borrado.
  - Vitest configurado **exclusivamente** para `src/lib/utils/**/*.test.js`
    con `environment: 'node'` (sin jsdom). Tests de `formatRate`,
    `stateDotClass`/`stateBadgeClass` (fallback neutro para estados
    desconocidos) y `browser` (false en node). `npm test` verde; integrado
    en CI.
- **Gates VM** (`webvm` 192.168.1.121): binario `v1.2.0-rc1` desplegado,
  servicio `active`, `<title>WebKVM</title>`, login 200 + token,
  `/api/system/status` con libvirt conectado. Tests frontend (`npm test`)
  y `check-i18n` verdes también en la VM.

## Sprint B (roadmap v1.2) — OPS: V12-OPS-01..06 (2026-09-06)

- **V12-OPS-01 · Release reproducible** — `Makefile` target `release`
  (verifica tag, arma `dist/webkvm-<tag>.tar.gz` + `SHA256SUMS`, publica
  con `gh` si `RELEASE_PUBLISH=1`) y `.github/workflows/release.yml` que
  en cada tag `v*` (o dispatch) construye el binario versionado
  (`-ldflags Version=tag`), el CLI, empaqueta el tarball con `smoke.sh` y
  crea la GitHub release con checksums. Validado el ensamblado en la VM:
  tarball + SHA256SUMS correctos.
- **V12-OPS-02 · CHANGELOG** — `CHANGELOG.md` en formato Keep a Changelog
  con la sección Unreleased (v1.2.0) documentando Sprint A + B y el
  histórico 1.1.0…1.1.10. La versión es visible en `webkvm version`, en
  `/api/system` y en la UI (Status).
- **V12-OPS-03 · `scripts/smoke.sh` a prueba de balas** — arranca una VM
  desechable (pool scratch + volumen + dominio + estado running) y usa un
  único `trap cleanup EXIT` + `trap 'exit 130' INT` / `'exit 143' TERM`.
  Todos los recursos llevan prefijo único `webkvm-smoke-$$-<ts>` y se
  destruyen de forma idempotente y con timeout por paso. Validado con
  harness (éxito, fallo, SIGTERM, SIGINT → siempre teardown) y **en la VM
  real**: PASS + cero residuos (0 VMs/pools/dirs `webkvm-smoke`).
- **V12-OPS-04 · Runbooks** — `docs/runbooks/`: `RUNBOOKS.md` (índice +
  reglas de oro), `UPGRADE-ROLLBACK.md`, `DISASTER-RECOVERY.md`,
  `SECURITY-MODEL.md`, `OPS-DAILY.md`. Enlazados desde el README.
- **V12-OPS-05 · Rotación de auditoría durable** — `audit.go`:
  `maxBackups=7` (límite ~80 MB), rotación con desplazamiento
  `.6→.7…1→2` y descarte del más antiguo, **`file.Sync()` tras cada
  `Flush` y antes del `Close`** durante la rotación, `Logger.Close()`
  idempotente (flush+sync+close) invocado en el apagado del servicio.
  `List()` lee los 7 backups. Tests nuevos (`rotation_test.go`) + gates
  VM: servicio con el binario nuevo y `audit.log` íntegro (0600, 211
  líneas) tras reinicio.
- **V12-OPS-06 · Job sweeper thread-safe** — `storage.go`:
  `DownloadJob.UpdatedAt` (unix, renovado en cada create/update);
  `pruneExpiredJobs(now, ttl)` bajo `jobsMu` que **solo purga estados
  terminales (`completed`/`error`) con TTL > 24 h** (nunca `queued`/
  `running`); `StartJobSweeper(ctx, 5m, 24h)` goroutine independiente
  arrancada en main. Tests con `-race` (`job_sweeper_test.go`). Gate VM:
  `job_sweeper_started interval=5m ttl=24h` en journal.

## Sprint B (roadmap v1.2) — CAT: V12-CAT-00..08 (2026-09-06)

- **V12-CAT-00 · Migración del store de appliances a layout v3** —
  `appliances/catalog.go`: `layoutVersion=3`; al cargar, los built-ins
  reciben los defaults nuevos (URL, SizeBytes, recursos, compression,
  notes) **solo si `!Customized && !BuiltinOverride`**; el `ProvisionScript`
  modificado por admin se respeta estrictamente (vía `GetProvision`).
  `Update`/`SetProvision` marcan `Customized`. `save()` ahora escribe 0600.
  Tests (`migration_test.go`): refresh de defaults, preservación de
  Customized/BuiltinOverride/no-builtin, persistencia de version 3,
  marcado de Customized.
- **V12-CAT-01..06 · URLs/SizeBytes corregidos** — `appliances/defaults.go`:
  debian-13→trixie (339214336), opensuse-leap→Build2.16 (341735936),
  openmediavault→iso 8.3.1 (1244774400), opnsense→pkg.opnsense.org,
  vyos→2026.09.01-0034-rolling (673185792), notas pfsense actualizadas.
- **V12-CAT-07 · Provisionamiento oficial Docker CE** —
  `appliances/provision.go`: script `docker-ce` con keyring
  `/etc/apt/keyrings/docker.asc` firmado (`signed-by`) vía
  `curl -fsSL --proto =https` (descarga del GPG key, **sin `curl|sh`**),
  fuente APT pinned y `docker compose version` como sanity check.
- **V12-CAT-08 · Validación pre-vuelo de deploys** — `api/appliances.go`:
  - La red se valida contra `ListNetworks` **antes** de crear el job →
    400 inmediato (`networkExists`/`networkInList`); libvirt caído → 503.
  - El payload cloud-init se valida antes de `storeJob` (sin jobs zombie
    ni entrada de auditoría para deploys que no arrancan).
  - Fallo de `applyCloudInit` → job `error` (antes `completed` con
    warning), **sin** escritura de `AppInfo` (la UI nunca muestra
    credenciales de un app sin semilla), audit
    `appliance.deploy_cloudinit_failed`, VM conservada (disco válido).
  - Tests `api/cat08_test.go` (membership de red, red vacía permitida).
  - Se movió `updateJob(...,"completed","warning"...)` → `"error"`.

## Sprint A (roadmap v1.2) — batch 3: V12-SEC-04 (cierre del Sprint A)

- **V12-SEC-04 · `POST|PUT /pools` admin-only + gate UI** —
  `api/router.go`: las mutaciones de pool (CreatePool/UpdatePool) salen del
  grupo `RequireAtLeast("operator")` y pasan al grupo `RequireRole(admin)`,
  junto a los borrados. Los operadores conservan `GET /pools` y `GET
  /volumes` (monitorización intacta) y las subidas de volúmenes/ISO.
  - `frontend/routes/Storage.svelte`: botón "+ Create Pool" con
    `disabled={!auth.isAdmin()}`, `title` y span explicativos
    (`storage.poolAdminOnly` en en/es/ca), guard en `createPool()` y
    catch de 403 forzado → toast claro + reset del diálogo (sin colapso).
  - Test `api/pools_rbac_test.go` con los middlewares reales
    (`auth.RequireRole`/`RequireAtLeast`) replicando el mapeo del grupo
    como canario de regresión: operator → GET 200, POST/PUT pools 403,
    POST volumes 200; admin → POST/PUT pools 200.
  - Gates VM: operator GET /pools=200, GET /volumes=200, POST /pools=403
    ("insufficient role for this action"); admin POST /pools=201.

## Sprint A (roadmap v1.2) — batch 2: V12-SEC-02 + V12-SEC-03 (2026-09-06)

- **V12-SEC-02 · Blacklist JWT persistente** — `auth/blacklist.go` reescrito
  con persistencia en `DATA_DIR/revoked.json`:
  - Escritura atómica: `revoked.json.tmp` (0600) → `fsync` → `rename` →
    `fsync` del directorio (sello POSIX contra panic/kernel crash).
  - Fail-open con custodia: archivo corrupto → `revoked.json.corrupt-<ts>`
    (jamás destruido) y arranque con lista vacía (nunca aborta).
  - `NewTokenBlacklistWithPath` / `NewManagerWithPath` (retrocompatible:
    `NewTokenBlacklist`/`NewManager` intactos para tests). `main.go` usa
    `cfg.RevokedFile()`. Save en `Revoke`, en GC (solo si dirty/purged) y en
    `Close`.
  - Gates VM: logout → `revoked.json` 0600 v1 → **restart → token viejo 401**.
  - Tests `-race`: persistencia entre instancias, drop de expirados,
    corrupción→custodia+recuperación, atomicidad (sin `.tmp`, 0600),
    100 revocaciones concurrentes sin pérdida, lectores durante writers.

- **V12-SEC-03 · Password policy + rotación del secreto inicial** —
  `user/store.go`:
  - `validatePasswordStrength`: mín 12, máx 128, 3/4 clases si <16, denylist
    embebida (`go:embed common_passwords.txt`, ~1.1k entradas + clásicos)
    con rechazo exacto (case-insensitive) y de "decoraciones" (cola de
    dígitos/símbolos, p.ej. `freedom123`). Grandfather: solo hashes nuevos.
  - Borrado transaccional de `admin-password.initial` / `.reset`: SOLO
    después de `save()` exitoso en `ChangePassword`/`Update` para el admin,
    sin depender del flag `must_change` (el secreto se agota al fijar
    cualquier contraseña nueva). Falla la persistencia → ficheros intactos.
  - Gates VM: débil → 400 `decorated version of a common password`;
    cambio fuerte → 200 + fichero borrado + login con la nueva contraseña.
  - Tests `-race`: matriz de policy (longitud/clases/denylist/decoración),
    denylist poblada, borrado en éxito, intacto en fallo (policy y old
    wrong), reset admin, no-admin no toca ficheros, policy aplicada a
    non-admins.

  > Credenciales staging VM `webvm` (192.168.1.121) tras gates: admin
  > `FinalAdminPass#2026` (el fichero inicial se eliminó correctamente).

## Repositorio (local main)

### Sprint A (roadmap v1.2) — reparaciones de datos/seguridad (2006-09-05
- **V12-DATA-01 (AP-C1)** · `Connector.DomainExists()/VolumeExists()`
  + `Handler.verifyDeployTargetFree()` (mira filesystem + libvirt, fail-closed)
  + lock por nombre (`acquireDeployLock`, purga propia) + pre-flight
  en handler (409 inmediato) y en el JOB bajo el lock (cierre TOCTOU),
  con audit `vm.appliance_deploy_blocked`. Verificado en VM staging:
  G1 409 por dominio, G2 409 por volumen, G3 deploy concurrente mismo
  nombre → 1 VM + 1 job bloqueado con audit (sin pérdida de datos).
- **V12-DATA-02 (AP-C2)**. `removePoolImage` con guard de propiedad por
  inodo (`fileInode`): cleanup en fallos de `qemu-img resize`,
  `GetStorageVolume`, y `CreateDomain` con `RefreshPool` post-delete +
  audit `appliance.deploy_cleanup`. NUNCA borra un artefacto ajeno que
  ganó la carrera por el mismo path (solo limpia lo que colocó el job).
- **V12-SEC-01 (AP-C3)** `install -m 600 /dev/null /etc/webkvm-app.txt`
  en los 16 scripts de provisión + (bashrc-mark conservado, root-only).
  Aceptación verificada: el script servido empieza con control 0600
  (`static -c %a` = 600 en el guest tras deploy real). NOTA: el store
  desplegado con las plantillas viejas necesita que el `layoutVersion`
  se refresque hacia v3 (debe ser `roadmap v1.2 V12-CAT-00`; en staging
  reseteamos el store manualmente).

# FIXES — Correcciones aplicadas (2026-09-05)

Correcciones derivadas de `ANALISIS-CODIGO.md` (auditoría en `51898b1`, v1.1.10).
Cada fix fue **verificado contra el código real** antes de aplicarse. La
documentación de referencia completa con evidencia está en
`ANALISIS-CODIGO.md` (secciones §4–§7).

## Verificación aplicada

- Backend: `go build ./...` OK · `go test -count=1 ./internal/api
  ./internal/libvirt ./internal/auth ./internal/cloudinit` OK.
  (Los 2 fails de `internal/backupstore` en esta máquina son preexistentes y
  provocados por ejecutar los tests como root: `TestValidateTargetPathDenyList`
  (*root* lee `/proc/1/root`) y `TestAllocateOutputPathRejectsBadDir` (root
  escribe en dir "read-only"). No son regresiones.)
- Frontend: `npm run build` OK (9.1 s, sin errores).
- Scripta: `bash -n packaging/standalone/install.sh` y `update.sh` OK.
- Frontend compilado y copiado a `backend/internal/frontend/dist`?: NO —
  ejecutar `make build` antes de recompilar el binario de producción.

## Críticos corregidos

### C-01 · `CreateStorageVolume` — formato sin whitelist
`backend/internal/libvirt/storage.go`. `req.Name` ya validaba regex
(`nameRE`), pero `req.Format` llegaba crudo al XML del volumen (inyección).
Fix: whitelist explícita `qcow2|raw` con rechazo (fail-closed, antes
silenceaba inválidos).

### C-02 · `createDiskInPool` — `volName` sin validar
`backend/internal/libvirt/domain.go:1209`. `volName` (deriva del nombre de
VM/req.Name) se interpolaba al XML y es nombre de fichero backing.
Fix: `nameRE` validación + whitelist de formato `qcow2|raw` (además de
eliminar el bloque redundante que silenciosamente volvía a `qcow2`).

### C-03 · Import OVA — XML de atacante al dominio
`backend/internal/libvirt/ova.go`. `doc.VirtualSystem.Name` al
`<name>` del dominio y `diskPath` al `<source file=…>` sin escapar.
Fix: `xmlEscape()` en ambos (y en `buildOVF` para name/osType del descriptor
de export).

### C-04 · Ticket VNC con poder de apagado
`backend/internal/auth/jwt.go` + `backend/internal/api/console.go`.
Confirmado: la consola embebida tenía botones Reboot/Shutdown/Force-off que
POSTeaban `?vt=` (ticket reutilizable 1 h), y el allowlist del middleware
aceptaba esas rutas.
Fix: eliminados `/reboot /shutdown /forceoff` del allowlist `vncTicketAllowedPath`
(solo quedan `/vnc` y `/clipboard`), eliminados los botones y el panel power
de la página de consola; ahora el power exige el Bearer JWT de la app
principal. Un `vt` filtrado ya no puede apagar la VM.

### C-05 · Update/install aceptaban binario sin checksum
- `packaging/standalone/update.sh`: el fallback a `raw.githubusercontent`
  (sin checksum posible) ahora exige `WEBKVM_ALLOW_UNVERIFIED=1`; sin
  SHA256SUMS en el release → `die` (fail-closed).
- `packaging/standalone/install.sh`: cuando el release no trae suma, antes
  solo logueaba WARNING e instalaba root igual; ahora `die`.

### C-06 · RCE root: `SystemUpdate` siempre activo + `eval` con input
- `backend/internal/api/system.go:SystemUpdate`: opt-in
  `WEBKVM_ALLOW_UPDATE=1` (env, apagado por defecto); sigue admin+root.
  Log `/var/log/webkvm/update.log` baja de 0644 a 0600.
- `packaging/standalone/install.sh` (líneas 95-147): los 6 `eval "${var}="${ans}""`
  con input interactivo reemplazados por `printf -v "${var}" '%s' "${ans}"`;
  un pegado malicioso ya no ejecuta shell como root. (Líneas 103, 106, 112,
  124, 144, 146.)

## Altos corregidos

- **A-01 · `api/templates.go`** — Instantiate/crear-desde-template cargaba
  el disco al pool equivocado: `checkDiskQuota` usaba `h.defaultPool()`
  aunque se computaba `tplPool` (y validaba ACL contra él). Fix: cuota
  evaluada sobre `tplPool` real.
- **A-02 · `api/storage.go` (UploadDisk)** — la cuota se cobraba con
  `ContentLength` del cliente (inexistente en chunked, contaminable).
  Fix: tras `io.Copy`, se re-cheque a la cuota con el tamaño real
  (`os.Stat`) y si excede se hace `os.Remove` del fichero + 409 → el
  espacio no queda ocupado por un disco fuera de cuota.
- **A-03 · `api/vms.go` (CreateVM cloud-init)** — la validación de
  cloud-init corría DESPUÉS de `CreateDomain` (contradecía su propio
  comentario): fallo de validación dejaba VM huérfana + 400 engañoso.
  Fix: validación movida antes de crear el dominio.
- **A-17 · `api/console_serial.go:generatePasswordString`** — si fallaba la
  lectura de urandom devolvía `"aaaaaaaa…"` (fail-open) y había sesgo de
  módulo. Fix: `crypto/rand` + muestreo por rechazo (unbiased), fail-closed
  (pánico si no hay CSPRNG).

## Frontend corregidos

- **A-20 · `routes/Snapshots.svelte`** — `s.vm_name.toLowerCase()` sin guard
  → página en blanco con snapshot huérfana (`null`). Fix `(x || '')` guards.
- **A-21 · `lib/utils/vmState.js`** — estado desconocido (libvirt
  `blocked/idle/pmsuspended`, etc.) caía al fallback `crashed` (rojo).
  Fix: fallback a `shutoff` (gris neutro) en `stateDotClass` y
  `stateBadgeClass`.
- **A-23 · `routes/VmList.svelte` + `routes/Users.svelte`** — mismo patrón
  null-guard en `v.name` / `u.username` de los filtros de búsqueda.
- **A-24 · `lib/components/Sidebar.svelte`** — rama muerta abriría enlaces
  externos con el JWT en la URL (`?token=`). Fix: eliminada la rama
  (nada definía `item.external=true`); navegación 100 % interna.

## Verificación funcional (VM `webvm` 192.168.1.121 — libvirt real)

Build `dev` (2026-09-05 20:47 UTC) desplegada en `/usr/local/bin/webkvm`,
servicio reiniciado, `curl /api/health` OK. Tests ejecutados vía API con
usuarios operator reales y quotas/pools cap limitados:

| Test | Fix | Resultado real |
|------|-----|----------------|
| POST volume `format:"qcow2'><svg…"` (t01 cap) | C-01 | **500** `unsupported volume format … (allowed: qcow2, raw)` ✔ |
| POST volume `name:"('><evil/>"` | C-02 | **500** `invalid volume name …` ✔ |
| POST volume sane (t02) | sanity | **201** ✔ |
| POST `/api/vms/{id}/shutdown?vt=<VT>` (t01 cap) | C-04 | **403** `ticket not valid for this endpoint` ✔ |
| POST `/api/vms/{id}/clipboard?vt=<VT>` | C-04 | llega al handler (500 guest-OS, esperado en VM sin agent) ✔ |
| GET `/console/{id}?vt=<VT>` | C-04 | **200** HTML consola ✔ |
| POST `/api/system/update` | C-06 | **403** `disabled` ✔ |
| Upload 2 GB preallocated **chunked** sin Content-Length (t03 cap pool 1) | A-02 | **409** `upload exceeded disk quota …; file deleted` + fichero fuera del pool ✔ |
| POST VM con cloud-init password 5 chars | A-03 | **400** `at least 6` + **ninguna VM huérfana** ✔ |
| Instantiate plantilla 2 GB → pool ISOS (cap **1**) | C-07/A-01 | **409** `t01 uses 2/1 GB disk on pool "ISOS"` ✔ |
| Instantiate plantilla 2 GB → pool webkvm-disks (cap 2) | control | **201** ✔ |
| `bash -n` install.sh / update.sh | C-05/C-06 | OK ✔ |

**Descubrimiento nuevo durante la verificación (C-07):** el cobro de cuota
en `InstantiateTemplate` usaba el owner de la **plantilla** — si la
plantilla era de admin (exento), cualquier operador instanciaba sin pasar
por su cuota ni sus pools (reproducido: 201 con t01 limitado, A-61 tras el
fix 409). Corregido cobrando al llamante + fail-closed en el miss de
`GetDomain` (también en CloneVM/UpdateVM/ResizeDomainDisk — M-04).

UI smoke con **chromium headless** instalado en la VM: `http://localhost:8080/` —
`<title>WebKVM</title>`, markup de login presente. Sistema limpio tras
tests (solo `admin`, VMs y volúmenes del dueño intactos).

## No corregido en esta pasada (para siguientes tandas)

Se mantienen abiertos en `ANALISIS-CODIGO.md`: C-04 extra (TTL del VT y
revocación — mitigado el power, falta acortar/rotar), A-04/A-05 (cuota TOCTOU
en appliance/import-restore, estimación con tamaño comprimido), A-06..A-16,
A-18..A-22, los M-01..M-29 y B-01..B-17. M-04 quedó resuelto en los 4 puntos
detectados (instantiate/clone/update/resize). La eliminación de discos al
borrar una VM sigue necesitando `?disks=true` (comportamiento por diseño,
documentado). Prioridad siguiente sugerida: A-04–A-08 (cuota/ACL en
vms.go/backup.go) — la resta de tamaños reales y re-cheques post-operación.
