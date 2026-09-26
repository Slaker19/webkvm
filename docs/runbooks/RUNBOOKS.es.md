# RUNBOOKS — WebKVM

[**English**](RUNBOOKS.md) • [**Español**](RUNBOOKS.es.md)

Runbooks operativos para administradores de sistemas (sysadmins). El
objetivo es que cualquier cambio, incidente o recuperación se resuelva
siguiendo pasos probados, con comandos copiables y *exit criteria*
explícitos.

## Índice

| Runbook | Cuándo usarlo |
|---|---|
| [INSTALLATION.es.md](../INSTALLATION.es.md) | Primera instalación (binario, systemd, Docker, Caddy) |
| [UPGRADE-ROLLBACK.es.md](UPGRADE-ROLLBACK.es.md) | Subir de versión o volver a la anterior con seguridad |
| [DISASTER-RECOVERY.es.md](DISASTER-RECOVERY.es.md) | Pérdida del host, del datadir, corrupción de libvirt |
| [SECURITY-MODEL.es.md](SECURITY-MODEL.es.md) | Entender qué protege el sistema y cómo auditar |
| [OPS-DAILY.es.md](OPS-DAILY.es.md) | Tareas diarias: health, logs, auditoría, jobs, backups |

## Reglas de oro (aplican a todos los runbooks)

1. **Nunca** edites `/opt/webkvm/users.json` ni los ficheros del datadir
   mientras el servicio corre sin antes hacer copia.
2. Antes de cualquier operación que modifique el datadir, usa
   `scripts/webkvm-backup.sh` o la UI (Backup → Crear copia).
3. Cambia el datadir, no edites binarios a mano sin dejar el binario
   anterior (`webkvm.previous`).
4. El binario se distribuye precompilado: **no** hace falta Go/Node en el
   servidor de producción para actualizar.
5. Verifica siempre el checksum del artefacto contra `SHA256SUMS` antes de
   instalarlo.
6. Los scripts del repo tienen modo `-euo pipefail`; si ves un error,
   no lo "suprimas" — léelo.

## Estado y localización de ficheros clave

| Elemento | Ruta |
|---|---|
| Binario | `/usr/local/bin/webkvm` (o `/opt/webkvm/webkvm`) |
| Binario anterior (rollback) | `/usr/local/bin/webkvm.previous` |
| Datadir | `/opt/webkvm` |
| Usuarios | `/opt/webkvm/users.json` (0600) |
| Credenciales iniciales admin | `/opt/webkvm/admin-password.initial` (0600) |
| Blacklist de tokens | `/opt/webkvm/revoked.json` (0600) |
| Tokens API | `/opt/webkvm/api-tokens.json` (0600) |
| Log de auditoría | `/opt/webkvm/audit.log` (+ `.1` … `.7` rotados, 10 MB cada uno) |
| Log de aplicación | `/var/log/webkvm/` (si `WEBKVM_LOG_FILE` activo) |
| Servicio systemd | `/etc/systemd/system/webkvm.service` |
| Caddy (HTTPS) | `/etc/caddy/Caddyfile`, servicio `caddy` |
| Health check | `curl -sk https://localhost:8080/api/health` |

> La ruta del binario depende del método de instalación. Consulta
> [INSTALLATION.es.md](../INSTALLATION.es.md) para el flujo exacto de tu
> despliegue.

## Índice de comandos rápidos

```bash
# Estado del servicio y logs
systemctl status webkvm
journalctl -u webkvm -f
make status / make logs          # en el repo

# Health check
curl -sk https://localhost:8080/api/health
curl -s  http://127.0.0.1:8080/api/health

# Versión instalada
webkvm version

# Auditoría reciente
grep '"action"' /opt/webkvm/audit.log | tail

# Smoke test de la pila de virtualización
sudo scripts/smoke.sh
```

Siguiente: [UPGRADE-ROLLBACK.es.md](UPGRADE-ROLLBACK.es.md).