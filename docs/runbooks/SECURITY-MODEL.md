# RUNBOOK: Modelo de Seguridad y Auditoría

Objetivo: documentar qué protege WebKVM, dónde se registra la actividad
y cómo un operador debe auditar el sistema. Complementa a
[SECURITY.md](../../SECURITY.md).

---

## 1. Autenticación y autorización

- **Sesiones**: JWT firmado (`JWT_SECRET`). Roles: `admin`, `operator`,
  `viewer`. Middleware `RequireRole`/`RequireAtLeast` en `internal/api`.
- **Admin exento** de cuotas y ACLs de pool (diseño explícito).
- **API tokens** largos (Bearer) para scripting; se revocan de inmediato.
- **Blacklist de tokens persistente** (`revoked.json`, 0600): una
  revocación sobrevive reinicios. Escritura atómica (tmp→fsync→rename) y
  *fail-open* ante corrupción (renombra a `.corrupt-<ts>`).
- **Rate limiting de login** por IP; CIDRs de confianza configurables
  (`WEBKVM_TRUSTED_RATELIMIT_CIDRS`).

## 2. Política de contraseñas (V12-SEC-03)

- Mínimo 12 caracteres; 3 de 4 clases de caracteres; sin contraseñas de
  una lista embebida de ~1.100 comunes; sin caracteres decorados
  (sustituciones tipo `p@ss`).
- Almacenamiento bcrypt (`password_hash`).
- Ficheros `admin-password.*` (0600) se eliminan **transaccionalmente**
  solo tras guardar el hash correctamente.

## 3. Controles de VM y appliance (V12-DATA/CAT)

- **Cuotas**: vCPU/RAM sobre VMs **en ejecución**; disco **global** por
  usuario (todos los pools). Un template instancia carga la cuota al
  **usuario que la instancia** (C-07).
- **ACL de pool por usuario** (`AllowedPools`): 403 para pools no
  permitidos; los admin siempre exentos.
- **Pools de almacenamiento**: creación/edición solo `admin` (V12-SEC-04).
- **Deploys de appliance (V12-CAT-08)**:
  - La red destino se valida contra `ListNetworks` → 400 antes de crear
    el job.
  - El payload cloud-init se valida antes de encolar.
  - Un fallo de `applyCloudInit` marca el job como `error`, **no** escribe
    `AppInfo` (la UI nunca muestra credenciales de un app sin semilla) y
    audita `appliance.deploy_cloudinit_failed`.
  - Limpieza de orfandades: `removePoolImage` en cada punto de fallo
    (V12-DATA-02), con guardia de propiedad por inode.
- **XML**: nombres y formatos validados con regex estrictas (C-01/C-02);
  descriptores OVA con escape XML (C-03).

## 4. Qué se registra en auditoría

Log JSONL append-only en `/opt/webkvm/audit.log` (0600). Cada entrada:
`time, user, role, action, resource, ip, detail, error`.

| Acción | Ejemplo |
|---|---|
| Login/logout | `auth.login`, `auth.logout` |
| Usuarios | `user.create`, `user.update`, `user.delete` |
| Ciclo de vida VM | `vm.create`, `vm.start`, `vm.stop`, `vm.delete` |
| Cambios de sistema | `system.update`, `settings.change` |
| Appliance | `vm.appliance_deploy`, `appliance.deploy_cleanup`, `appliance.deploy_cloudinit_failed` |
| ISO | `iso.download` |
| Pools | `storage.pool.create` |

## 5. Rotación y durabilidad de auditoría (V12-OPS-05)

- Rotación a 10 MB con **hasta 7 backups** (`audit.log.1` … `.7`),
  límite total ≈ 80 MB.
- **fsync** tras cada `Flush` y antes del `Close` durante la rotación —
  ningún registro reconocido se pierde por buffers.
- `Logger.Close()` invocado en el apagado del servicio (flush + fsync).
- `List()` lee todos los backups (`.1`…`.7` + actual) y devuelve
  newest-first con paginado.

### Consultas útiles de auditoría

```bash
AUDIT=/opt/webkvm/audit.log

# Últimos eventos
tail -20 "$AUDIT" | jq -r '.time + " " + .user + " " + .action'

# Inicios de sesión fallidos
grep '"action":"auth.login"' "$AUDIT" | jq -r 'select(.error) | .time + " " + .ip'

# Cambios de usuarios
grep -E '"action":"user\.' "$AUDIT" | jq -r '.time + " " + .user + " " + .action + " " + (.resource // "")'

# Ciclo de vida de un VM concreto
grep '"resource":"<vm-id-or-name>"' "$AUDIT" | jq -r '.time + " " + .action'

# Deploys de appliance y sus resultados
grep -E '"action":"(vm.appliance_deploy|appliance.deploy_cleanup|appliance.deploy_cloudinit_failed)"' \
  "$AUDIT" | jq -r '.time + " " + .action + " " + (.detail.error // .error // "")'
```

> Si `jq` no está instalado, `grep '"action":"..."' "$AUDIT"` también es
> legible (una línea JSON por evento).

## 6. Jobs en segundo plano (V12-OPS-06)

- Descargas ISO y deploys de appliance se registran como jobs en memoria.
- **Sweeper**: goroutine con ticker de 5 min purga jobs **terminales**
  (`completed`/`error`) con más de **24 h** de antigüedad. Los jobs
  `queued`/`running` **nunca** se purgan.
- Contienda resuelta con `sync.RWMutex`; `UpdatedAt` (unix) se renueva en
  cada creación/actualización.

## 7. Checklist de auditoría (mensual)

- [ ] Revisar logins fallidos y rate-limit trips.
- [ ] Revisar cambios de roles/usuarios.
- [ ] Verificar rotación: `ls -la /opt/webkvm/audit.log*`.
- [ ] Confirmar que no hay jobs `error` acumulados en la UI de ISO.
- [ ] Confirmar backups del datadir recientes.
- [ ] Revisar deploys de appliance fallidos y su limpieza.

## 8. Incidentes de seguridad

1. **Revoca el acceso**: cambia la contraseña admin, revoca API tokens,
   rota `JWT_SECRET` (al rotar, todos los tokens de sesión se invalidan).
2. **Extrae la auditoría** antes de borrar nada.
3. **Aísla** el host (firewall/red) si sospechas compromiso del host.
4. Consulta [DISASTER-RECOVERY.md](DISASTER-RECOVERY.md) para restaurar.