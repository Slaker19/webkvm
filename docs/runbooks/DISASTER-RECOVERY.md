# RUNBOOK: Recuperación ante Desastres (Disaster Recovery)

Objetivo: restaurar WebKVM tras escenarios de pérdida — host caído,
datadir borrado/corrupto, pérdida de libvirt, o ficheros clave dañados.
El sistema está diseñado para que **el datadir sea la única verdad**;
todo lo demás (binario, servicio, pools) es reproducible.

---

## 0. Mapa de dependencias

```
WebKVM operativo =
  binario (/usr/local/bin/webkvm)
  + datadir (/opt/webkvm)      <-- IRRECUPERABLE si no hay backup
  + libvirt (pools, volúmenes, VMs en QEMU)
  + (opcional) Caddy / certs / .env
```

- **Los VMs viven en libvirt** (pools), no en el datadir. Un backup de
  WebKVM no incluye los discos de los VMs.
- El datadir contiene: `users.json`, `api-tokens.json`, `revoked.json`,
  `audit.log*`, `backup/`, `nodes.json`, `certs/`, settings, etc.

---

## 1. El host arranca pero el servicio no está

```bash
systemctl status webkvm
journalctl -u webkvm -n 100 --no-pager

# Causas comunes:
#  - libvirtd caído        -> systemctl start libvirtd
#  - puerto 8080 ocupado   -> ss -ltnp | grep 8080
#  - datadir movido/corrupto -> comprueba /opt/webkvm
#  - binario borrado       -> reinstala (ver sección 2)
```

La webkvm conecta a libvirt en background (backoff 10s→60s) si `libvirtd`
está caído; no es necesario reiniciar el servicio cuando libvirtd vuelve.

---

## 2. Binario perdido o corrupto

1. Descarga la versión que quieras de las releases de GitHub.
2. **Verifica el checksum** contra `SHA256SUMS` (ver UPGRADE-ROLLBACK).
3. Instala:
   ```bash
   sudo install -m 0755 webkvm /usr/local/bin/webkvm
   sudo systemctl restart webkvm
   ```
4. Valida con `/api/health`.

> El datadir no cambia entre versiones menores; actualizar el binario
> solo NO pierde usuarios ni auditoría.

---

## 3. Datadir perdido/corrupto (el escenario crítico)

Solo recuperable si tienes un backup. Dos caminos:

### 3a. Backups de la propia UI (recomendados)

El datadir se auto-backupea (si está configurado) en
`/opt/webkvm/backup/`. Restaurar:

```bash
sudo systemctl stop webkvm
ls -lt /opt/webkvm/backup/            # elige el más reciente
# Restaura el contenido completo del datadir a /opt/webkvm:
sudo rm -rf /opt/webkvm.broken        # mueve el datadir corrupto aparte
sudo mv /opt/webkvm /opt/webkvm.broken
sudo mkdir -p /opt/webkvm
# (extrae el backup dentro de /opt/webkvm)
sudo systemctl start webkvm
curl -sk https://localhost:8080/api/health
```

### 3b. Backup manual (webkvm-backup.sh o copia)

```bash
sudo systemctl stop webkvm
sudo rm -rf /opt/webkvm
sudo cp -a /opt/webkvm.bak.<fecha> /opt/webkvm
# Permisos correctos:
sudo chown -R root:root /opt/webkvm
sudo chmod 600 /opt/webkvm/*.json /opt/webkvm/revoked.json 2>/dev/null || true
sudo chmod 700 /opt/webkvm/certs 2>/dev/null || true
sudo systemctl start webkvm
```

**Sin backup, el estado recuperable es:**
- VMs intactas en libvirt (siguen visibles con `virsh list --all`).
- **Usuarios, tokens y auditoría perdidos.** Habrá que reinstalar y
  crear el admin de nuevo.

---

## 4. Corrupción parcial de ficheros sensibles

| Fichero | Síntoma | Recuperación |
|---|---|---|
| `users.json` | login falla para todos | Restaurar del backup. Sin backup: reinstalar y crear admin. |
| `revoked.json` (V12-SEC-02) | `Error loading revoked file` | **Fail-open**: el sistema renombra el corrupto a `revoked.json.corrupt-<ts>` y sigue. Los tokens revocados antes se pierden: revoca de nuevo o rota `JWT_SECRET`. |
| `audit.log` | lista vacía o parseo raro | El logger ignora líneas corruptas; restaura del backup si quieres el histórico. |
| `api-tokens.json` | 401 con tokens válidos | Restaurar del backup o recrear tokens. |
| `certs/` | TLS inválido | `make regen-cert` (self-signed) o reconfigurar certificado. |

---

## 5. Pérdida de VMs en libvirt (pero discos intactos)

Los discos suelen sobrevivir en el pool. Recrea el dominio con
`virt-install`/`virsh define` apuntando al volumen existente:

```bash
virsh vol-list <pool>                    # localiza el volumen
# Recrea el VM reutilizando el disco; los datos están en el volumen.
```

Si el VM existe pero no arranca (kernel panic en cada boot) y usaba
cloud-init con el script de provisión, la semilla NoCloud se inyectó en
el arranque del primer boot — el reaprovisionamiento no es automático.

---

## 6. Recuperación total del host (bootstrap)

1. Instala dependencias: `make install-deps` (libvirt, qemu, etc.).
2. `make install-all` (backend + Caddy + HTTPS).
3. Restaura el datadir (sección 3).
4. Verifica pools: `virsh pool-list --all`; reactiva con `pool-start`.
5. Ejecuta `sudo scripts/smoke.sh` para validar el stack.
6. Login como admin y comprueba VMs, usuarios, auditoría y jobs.

---

## Checklist rápido de incidente

```bash
# 1. El servicio responde?
systemctl is-active webkvm
curl -sk https://localhost:8080/api/health

# 2. Libvirt OK?
virsh -c qemu:///system list --all

# 3. Datadir intacto?
ls -la /opt/webkvm | head

# 4. Auditoría intacta?
wc -l /opt/webkvm/audit.log

# 5. Último backup disponible?
ls -lt /opt/webkvm/backup/ | head
```