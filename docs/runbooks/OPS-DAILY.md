# RUNBOOK: Operaciones Diarias (Day-2)

Objetivo: tareas habituales de un administrador sobre una instalación
operativa — monitorización, logs, auditoría, jobs, backups y limpieza.

---

## 1. Monitorización

### Health check

```bash
curl -sk https://localhost:8080/api/health
# respuesta esperada: {"status":"ok","version":"...","build_time":"..."}
```

> En instalaciones detrás de proxy o con BIND_ADDR=127.0.0.1, usa el
> endpoint en loopback. Se puede cronificar:
> `*/1 * * * * root curl -fsS -m 5 http://127.0.0.1:8080/api/health || logger -t webkvm "health failed"`

### Servicio y recursos

```bash
systemctl status webkvm --no-pager
journalctl -u webkvm -f
ss -ltnp | grep -E '8080|443'
```

### Dashboard de la UI

- **Status**: versión, estado del backend, actualizaciones disponibles.
- **Settings**: bind, puerto, logging, backups, certificados.
- **Alertas** (si notifier activado): downtime de VM, disco bajo, fallos
  de backup.

## 2. Logs de aplicación

- Estructurados (JSON por defecto): `journalctl -u webkvm -f` o
  `/var/log/webkvm/` si `WEBKVM_LOG_FILE` está activo.
- Nivel/forma configurables: `WEBKVM_LOG_LEVEL` (info/debug/warn),
  `WEBKVM_LOG_FORMAT` (json/console).

## 3. Auditoría (V12-OPS-05)

El log de auditoría es **la** fuente para forense y cumplimiento:

```bash
ls -la /opt/webkvm/audit.log*
# rotación: audit.log, .1 ... .7  (10 MB cada uno, ~80 MB total)
tail -f /opt/webkvm/audit.log
```

Ver [SECURITY-MODEL.md](SECURITY-MODEL.md) sección 5 para consultas jq.

## 4. Jobs de descarga y deploy

- Los jobs viven en memoria; el sweeper (V12-OPS-06) purga jobs
  terminales > 24 h cada 5 min. `queued`/`running` nunca se purgan.
- **Si un deploy se queda colgado** en `running` > 30 min:
  1. Revisa el estado del volumen: `virsh vol-list <pool>`.
  2. Revisa el job en la UI de ISO/deploys.
  3. Si procede, borra el volumen huérfano y reintenta.
  - La limpieza automática (`removePoolImage`, V12-DATA-02) ya cubre los
    fallos normales; un colgado manual es un caso de emergencia.

## 5. Backups (datadir)

El datadir es la única pieza irrecuperable sin backup.

```bash
# Manual:
sudo scripts/webkvm-backup.sh
# Configurado en la UI: Backup -> target + schedule + retention.
# Comprueba el resultado: Backup -> Jobs (último estado).
```

Retención sugerida: 7 diarios + 4 semanales + mensual.

## 6. Limpieza de disco / logs

- `audit.log*` auto-rotan a 7×10 MB — no requiere mantenimiento.
- `journald`: `journalctl --vacuum-size=200M` si crece.
- Pools de ISO/VDI: revisar volúmenes sin referencias
  (`virsh vol-list` contra `virsh list --all`).

## 7. Smoke test periódico (V12-OPS-03)

El script `scripts/smoke.sh` valida la pila entera (pool → volumen →
VM → running) y **se auto-limpia** incluso con Ctrl+C o fallo. Útil
tras actualizaciones del host o de libvirt:

```bash
sudo scripts/smoke.sh
```

Opciones: `SMOKE_TIMEOUT=180`, `WEBKVM_SMOKE_KEEP=1` (deja los recursos
para debug), `WEBKVM_URL=https://localhost:8080` (añade health check).

## 8. Rotación de secretos

- **Admin password**: cambia desde la UI (Users → editar) — los
  `admin-password.*` se eliminan transaccionalmente tras el guardado.
- **API tokens**: regenera y revoca los que ya no se usen (UI o API).
- **JWT_SECRET**: rota solo si sospechas compromiso; invalidará todas las
  sesiones.

## 9. Actualización

Ver [UPGRADE-ROLLBACK.md](UPGRADE-ROLLBACK.md): backup previo, verificar
checksum, `make install-systemd` (health check + auto-rollback), validar.

## 10. Release (solo mantenedores) — V12-OPS-01

```bash
git tag v1.2.0 && git push origin v1.2.0
# GitHub Actions construye y publica dist/webkvm-<tag>.tar.gz + SHA256SUMS
# Alternativa local:
make release                      # artefactos locales
make release RELEASE_PUBLISH=1    # además publica con gh
```

Comprueba la release publicada y su checksum antes de anunciarla.