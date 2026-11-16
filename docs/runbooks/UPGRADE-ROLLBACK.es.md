# RUNBOOK: Actualización y Rollback

[**English**](UPGRADE-ROLLBACK.md) • [**Español**](UPGRADE-ROLLBACK.es.md)

Objetivo: subir WebKVM de versión de forma segura y reproducible, y
volver a la versión anterior si algo falla, **sin perder datos ni
registros de auditoría**.

> Aplica a instalaciones nativas (systemd). Para Docker consulta
> [docs/DOCKER.es.md](../DOCKER.es.md).

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

4. **Descarga el artefacto y verifica su checksum**:

   Artefactos oficiales de la release **v0.1.13** (tag `v0.1.13`):
   - `webkvm-0.1.13-linux_amd64` (binario del servidor independiente)
   - `webkvm-cli-0.1.13-linux_amd64` (binario de CLI independiente)
   - `webkvm-0.1.13.tar.gz` (tarball de fuentes y precompilados)
   - Paquetes `.deb` (Debian/Ubuntu) y `.rpm` (RHEL/Fedora)
   - `SHA256SUMS` (sumas de verificación criptográfica)

   ```bash
   VERSION=v0.1.13
   curl -fsSL -o /tmp/webkvm-$VERSION.tar.gz \
     https://github.com/Slaker19/webkvm/releases/download/$VERSION/webkvm-$VERSION.tar.gz
   curl -fsSL -o /tmp/SHA256SUMS \
     https://github.com/Slaker19/webkvm/releases/download/$VERSION/SHA256SUMS
   cd /tmp
   sha256sum -c SHA256SUMS --ignore-missing webkvm-$VERSION.tar.gz
   ```
   Debe imprimir `OK`. Si no, **aborta** — el artefacto no es íntegro.

5. **Smoke test del stack** (opcional pero recomendado):
   ```bash
   sudo scripts/smoke.sh
   ```

---

## 2. Realizar la actualización

### Método 1: Actualización desde la Web UI (recomendado para instalaciones nativas independientes)

Los administradores pueden iniciar la actualización directamente desde el panel:

1. Abre la pestaña **Estado** (`/#/status`) y pulsa en **Actualizar** (**Update**).
2. **Requisito de entorno**: El servicio requiere `WEBKVM_ALLOW_UPDATE=1` en sus variables de entorno (configurado por defecto en `webkvm.service` en instalaciones nativas).
3. **Ejecución y registro**: La actualización se ejecuta en segundo plano (detached) de modo que la detención del servicio no mate al actualizador. La salida se escribe en `/var/log/webkvm/update.log`.
4. **Selección automática del modo**:
   - **Modo `release`**: Se usa cuando `REPO_DIR` no es un clon de git. Consulta la última release de GitHub, descarga `webkvm-<version>-linux_amd64`, verifica su checksum contra `SHA256SUMS` y lo instala.
   - **Modo `source`**: Se usa cuando `REPO_DIR` es un clon de git. Ejecuta `git pull --ff-only` y recompila el frontend y backend (requiere Go ≥ 1.26 y Node.js ≥ 20 en el host).
5. **Copia de seguridad y rollback automático**: El binario anterior se respalda en `/usr/local/bin/webkvm.previous`. El actualizador reinicia el servicio y sondea `/api/health` durante 30 segundos. Si falla, restaura automáticamente `webkvm.previous` y reinicia el servicio.
6. **Restricciones de despliegue**:
   - **Docker**: NO utilices el actualizador integrado dentro de contenedores Docker. Actualiza descargando la nueva imagen (`docker compose pull && docker compose up -d`). Consulta [docs/DOCKER.es.md](../DOCKER.es.md).
   - **Clústeres HA**: En un clúster gestionado por Pacemaker, el botón solo actualiza el nodo local que sirve el panel y reinicia su servicio local; NO lo uses como actualizador de clúster. Las actualizaciones deben coordinarse nodo a nodo a través de Pacemaker. Consulta [HA-CLUSTER.es.md](HA-CLUSTER.es.md).

### Método 2: Despliegue automático con el instalador (con health check y rollback)

El tarball trae el **binario ya compilado** y el instalador, así que el
servidor de producción no necesita Go ni Node.js. El instalador guarda el
binario actual en `webkvm.previous` **antes** de instalar el nuevo y hace
un health check con auto-rollback si el servicio no responde.

```bash
cd /tmp && tar -xzf webkvm-$VERSION.tar.gz
sudo WEBKVM_BINARY=backend/webkvm bash packaging/standalone/install.sh --yes
```

### Método 3: Actualización manual

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
   # 8080/http es el valor por defecto; si la instalación usa otro
   # puerto o HTTPS (server.port / server.tls_cert en config.json),
   # averígualo con:
   #   sudo bash scripts/detect-webkvm-endpoint.sh /opt/webkvm
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

El binario anterior siempre queda en `/usr/local/bin/webkvm.previous` tras el
actualizador integrado, `make install-systemd` o el paso manual.

```bash
# Con el makefile:
sudo make rollback
# Hace: mv webkvm.previous -> webkvm; restart; health check con 20s.

# Manualmente:
sudo systemctl stop webkvm
sudo test -f /usr/local/bin/webkvm.previous || { echo "no hay binario anterior"; exit 1; }
sudo mv /usr/local/bin/webkvm.previous /usr/local/bin/webkvm
sudo systemctl start webkvm
curl -sk https://localhost:8080/api/health   # ajusta puerto/proto si no es el default; ver nota arriba
```

### Rollback en clústeres HA (Pacemaker)

En un clúster de alta disponibilidad, no reviertas los binarios en un nodo activo sin coordinarlo a través de Pacemaker.
1. Desactiva el recurso antes de sustituir el binario:
   ```bash
   sudo pcs resource disable webkvm-app --wait
   ```
2. Restaura el binario anterior en cada nodo:
   ```bash
   sudo mv /usr/local/bin/webkvm.previous /usr/local/bin/webkvm
   ```
3. Reactiva el recurso:
   ```bash
   sudo pcs resource enable webkvm-app --wait
   sudo pcs status
   ```
Consulta [HA-CLUSTER.es.md](HA-CLUSTER.es.md) para más detalles operativos del clúster.

> **Importante**: el rollback NO restaura el datadir. Si el problema
> afecta a datos, restaura el backup completo:
> ```bash
> sudo systemctl stop webkvm
> sudo rm -rf /opt/webkvm
> sudo cp -a /opt/webkvm.bak.<fecha> /opt/webkvm
> sudo systemctl start webkvm
> ```

---

## 5. Migración de AppArmor tras la actualización

En hosts con AppArmor, WebKVM asigna `<seclabel type='none' model='apparmor'/>` a los dominios definidos para que las operaciones con snapshots externos no queden bloqueadas por la regla de denegación de libvirt.

- **Migración diferida (lazy)**: Las máquinas virtuales existentes se migran automáticamente en su siguiente arranque, reversión o eliminación de snapshot. **No se requieren ediciones manuales del XML.**
- **Confinamiento**: Para mantener el confinamiento completo de AppArmor, define `WEBKVM_DISABLE_APPARMOR=0` en el entorno del servicio (ten en cuenta que la eliminación de snapshots externos offline puede fallar hasta que libvirt publique la corrección upstream).
- **SELinux**: Los hosts con SELinux no se ven afectados (no se añade la seclabel).
- Consulta [SECURITY.es.md](../../SECURITY.es.md) para los detalles del modelo de seguridad.

---

## 6. Desinstalación

```bash
make uninstall
# Detiene y elimina el servicio + binario. El datadir se conserva
# a propósito (borrado manual si se desea).
```

## Exit criteria

- [ ] `webkvm version` muestra la versión objetivo (ej. `0.1.13`).
- [ ] `systemctl is-active webkvm` = `active`.
- [ ] `/api/health` responde `ok`.
- [ ] `audit.log` intacto y creciendo.
- [ ] En caso de fallo, rollback completado y servicio operativo.