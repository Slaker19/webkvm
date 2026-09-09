# Hallazgos de búsqueda (errores / fallos / panic / excepciones) — rama findings/errors-main

He escaneado la rama main de Slaker19/webkvm buscando términos relacionados con errores: error, fail, bug, crash, panic, exception, segfault, fmt.Errorf, log.Fatal, log.Panicf, panic(), etc. He incluido absolutamente todo lo que encontré durante las búsquedas automáticas.

IMPORTANTE: los resultados de búsqueda del motor tienen un límite; esta lista puede ser incompleta. Para ver más resultados en la interfaz de GitHub: https://github.com/Slaker19/webkvm/search?q=error+OR+panic+OR+fail+OR+bug+OR+crash+OR+exception&type=code

---

Resumen de hallazgos (por archivo)

- backend/internal/cloudinit/cloudinit.go
  - Uso de panic() en cryptSHA512 cuando c.Generate falla: `panic("sha512_crypt failed: " + err.Error())`.
  - Uso de panic() en GeneratePassword si crypto/rand falla: `panic("crypto/rand failed: " + err.Error())`.
  - Enlace: https://github.com/Slaker19/webkvm/blob/main/backend/internal/cloudinit/cloudinit.go

- backend/internal/api/console_serial.go
  - Uso de panic()-like en generatePasswordString: la función `generatePasswordString` panica si crypto/rand no está disponible: `panic("crypto/rand unavailable: " + err.Error())`.
  - Enlace: https://github.com/Slaker19/webkvm/blob/main/backend/internal/api/console_serial.go

- backend/internal/backupstore/runner.go
  - NewRunnerWithConfig panica si config == nil: `panic("backupstore: NewRunnerWithConfig requires a non-nil ConfigProvider")`.
  - addSchedule registra un error en logger si cron.AddFunc falla (comportamiento correcto); revisar si se necesita propagar.
  - Enlace: https://github.com/Slaker19/webkvm/blob/main/backend/internal/backupstore/runner.go

- backend/internal/user/store.go
  - mustReadDenylist usa panic si la lectura embebida falla: `panic(fmt.Sprintf("embedded denylist missing: %v", err))`.
  - Varias funciones retornan errores descriptivos con errors.New/ fmt.Errorf — normal.
  - Enlace: https://github.com/Slaker19/webkvm/blob/main/backend/internal/user/store.go

- backend/internal/logging/logging_test.go
  - Tests que asumen comportamiento; no son fallos en runtime, pero indicados por la búsqueda (ej. asserts que esperan no panic). (archivo de tests)
  - Enlace: https://github.com/Slaker19/webkvm/blob/main/backend/internal/logging/logging_test.go

- backend/internal/auth/blacklist.go
  - NewTokenBlacklistWithPath llama a bl.loadLocked y si devuelve error se usa `slog.Error(...)` en lugar de abortar — es correcto por diseño (fail-open); sin embargo, revisar manejo de errores catastróficos.
  - Enlace: https://github.com/Slaker19/webkvm/blob/main/backend/internal/auth/blacklist.go

- backend/internal/notify/notify.go
  - loadSecrets devuelve error parseando JSON: `fmt.Errorf("parse notify-secrets.json: %w", err)`; saveSecrets escribe atómicamente y renombra — patrón seguro.
  - Enlace: https://github.com/Slaker19/webkvm/blob/main/backend/internal/notify/notify.go

- backend/internal/api/vms.go
  - Varias llamadas a funciones que pueden devolver errores durante import; se registran y se retornan JSON con jsonErr. Ejemplos: `h.compute.GetPoolPath` y creación de archivos temporales.
  - Enlace: https://github.com/Slaker19/webkvm/blob/main/backend/internal/api/vms.go

- backend/internal/backupstore/s3.go
  - Manejo de errores en List/Read/Delete con fmt.Errorf wrappers: `return nil, fmt.Errorf("s3 list: %w", obj.Err)` — correcto pero útil de revisar mensajes para trazabilidad.
  - Enlace: https://github.com/Slaker19/webkvm/blob/main/backend/internal/backupstore/s3.go

- backend/internal/libvirt/console.go
  - OpenSerialConsole devuelve errores mapeados (ErrDomainNotRunning) y libera stream si falla — manejo cuidadoso.
  - SetUserPassword envuelve error con mensaje: `fmt.Errorf("set user password (is qemu-guest-agent running in the VM?): %w", err)`.
  - Enlace: https://github.com/Slaker19/webkvm/blob/main/backend/internal/libvirt/console.go

- backend/internal/appliances/source.go
  - ValidateSourceURL devuelve errores en entradas inválidas; saneamiento de hosts en whitelist.
  - Enlace: https://github.com/Slaker19/webkvm/blob/main/backend/internal/appliances/source.go

- Varios archivos relacionados con persistencia atómica y manejo de errores (config store, token blacklist, notifier, backupstore, logging, packaging scripts). Ejemplos: backend/internal/configstore/store.go, backend/internal/config/config.go, packaging/standalone/update.sh, packaging/standalone/install.sh.

Observaciones generales y recomendaciones

- panic() está usado en unos pocos puntos concretos (cryptSHA512, GeneratePassword, NewRunnerWithConfig, mustReadDenylist, generatePasswordString). Revisar si esos panics son aceptables (casos de fallo catastrófico o corrupción interna) o si deberían transformarse en errores retornados para un fallo más controlado y trazable.
  - Ejemplo: `panic("crypto/rand failed: ...")` en GeneratePassword dejará caer todo el proceso; quizá prefieras propagar error y devolver 500 al cliente en endpoints.

- Muchas funciones siguen patrones seguros (escrituras atómicas con tmp+rename, logging, envoltorios de error con %w). Esto es positivo.

- Hay usos de fallback no-criptográfico en randHex: si cryptorand.Read falla, el código retorna una time-based pseudo-rand string. Esto evita devolver error pero es menos seguro. Revisar si el uso es estrictamente no-crítico.

Siguientes pasos que puedo hacer por ti

- Crear un Pull Request (yo puedo preparar rama + commit con este archivo FOUND_ERRORS.md; tú deberás abrir el PR en GitHub). — YA he creado la rama findings/errors-main.
- Intentar convertir los panics detectados a retornos de error y dejarlo en una rama (requiere confirmación porque puede necesitar cambios lógicos).
- Buscar más a fondo (escanear PRs, títulos, issues, o ejecutar búsquedas adicionales con otros patrones).

---

Enlaces de búsqueda adicional (ver en GitHub):
- Búsqueda general en código: https://github.com/Slaker19/webkvm/search?q=error+OR+panic+OR+fail+OR+bug+OR+crash+OR+exception&type=code

(Nota: los resultados mostrados en este archivo provienen de búsquedas automáticas y pueden ser incompletos. Recomendado revisar manualmente los archivos listados y la búsqueda en la UI de GitHub.)
