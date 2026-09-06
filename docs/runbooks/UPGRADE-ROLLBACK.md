# RUNBOOK: Actualización y Rollback

Objetivo: subir WebKVM de versión de forma segura y reproducible, y
volver a la versión anterior si algo falla, **sin perder datos ni
registros de auditoría**.

> Aplica a instalaciones nativas (systemd). Para Docker consulta
> `docs/DOCKER.md`.

---

## 1. Preparación

1. **Comprueba la versión actual:**
   ```bash
   webkvm version
   ```

2. **Revisa el changelog** (`CHANGELOG.md` de la release) para cambios
   de configuración o pasos extra.

3. **Backup completo del datadir** antes de tocar nada:
   ```bash
   sudo scripts/webkvm-backup.sh
   # o, manualmente:
   sudo systemctl stop webkvm
   sudo cp -a /opt/webkvm /opt/webkvm.bak.$(date +%F-%H%M)
   sudo systemctl start webkvm
   ```

4. **Descarga el artefacto y verifica su checksum** (V12-OPS-01):
   ```bash
   VERSION=v1.2.0
   curl -fsSL -o /tmp/webkvm-$VERSION.tar.gz \
     https://github.com/Slaker19/webkvm/releases/download/$VERSION/webkvm-$VERSION.tar.gz
   curl -fsSL -o /tmp/SHA256SUMS \
     https://github.com/Slaker19/webkvm/releases/download/$VERSION/SHA256SUMS
   cd /tmp
   sha256sum -c SHA256SUMS --ignore-missing webkvm-$VERSION.tar.gz
   ```
   Debe imprimir `OK`. Si no, **aborta** — el artefacto no es íntegro.

5. **Smoke test del stack** (opcional pero recomendado, V12-OPS-03):
   ```bash
   sudo scripts/smoke.sh
   ```

---

## 2. Actualización (flujo automático con health check)

El target `make install-systemd` guarda el binario actual en
`webkvm.previous` **antes** de instalar el nuevo y hace un health check
con auto-rollback si el servicio no responde en 20 s.

```bash
cd <repo-o-dir-con-Makefile>
# Extrae el tarball descargado en un dir con el Makefile, o apunta
# WEBKVM_BINARY al binario del tarball.
make install-systemd
```

Si prefieres hacerlo manualmente (recomendado en hosts con menos
privilegios):

```bash
sudo systemctl stop webkvm
sudo cp -a /usr/local/bin/webkvm /usr/local/bin/webkvm.previous
sudo install -m 0755 /tmp/tarball/webkvm /usr/local/bin/webkvm
sudo systemctl daemon-reload
sudo systemctl start webkvm
```

## 3. Validación post-update

1. **Servicio arriba:**
   ```bash
   systemctl is-active webkvm        # -> active
   systemctl status webkvm --no-pager
   ```

2. **Health check:**
   ```bash
   curl -sk https://localhost:8080/api/health | head -c 200
   ```

3. **Versión nueva activa:**
   ```bash
   webkvm version                     # debe mostrar la versión nueva
   ```

4. **Interfaz**: carga `https://<host>` y confirma login + una vista de
   VMs.

5. **Auditoría sigue íntegra** (no se debe haber reseteado):
   ```bash
   wc -l /opt/webkvm/audit.log
   tail -3 /opt/webkvm/audit.log
   ```

---

## 4. Rollback (si algo falla)

El binario anterior siempre queda en `webkvm.previous` tras
`make install-systemd` (o del paso manual).

```bash
# Con el makefile:
sudo make rollback
# Hace: mv webkvm.previous -> webkvm; restart; health check con 20s.

# Manualmente:
sudo systemctl stop webkvm
sudo test -f /usr/local/bin/webkvm.previous || { echo "no hay binario anterior"; exit 1; }
sudo mv /usr/local/bin/webkvm.previous /usr/local/bin/webkvm
sudo systemctl start webkvm
curl -sk https://localhost:8080/api/health
```

> **Importante**: el rollback NO restaura el datadir. Si el problema
> afecta a datos, restaura el backup completo:
> ```bash
> sudo systemctl stop webkvm
> sudo rm -rf /opt/webkvm
> sudo cp -a /opt/webkvm.bak.<fecha> /opt/webkvm
> sudo systemctl start webkvm
> ```

---

## 5. Desinstalación

```bash
make uninstall
# Detiene y elimina el servicio + binario. El datadir se conserva
# a propósito (borrado manual si se desea).
```

## Exit criteria

- [ ] `webkvm version` muestra la versión objetivo.
- [ ] `systemctl is-active webkvm` = `active`.
- [ ] `/api/health` responde `ok`.
- [ ] `audit.log` intacto y creciendo.
- [ ] En caso de fallo, rollback completado y servicio operativo.