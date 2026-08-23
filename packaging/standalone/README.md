# Instalador standalone

Este directorio instala `webkvm` como gestor independiente en **Debian/Ubuntu (apt)**,
**Fedora/RHEL (dnf)** y **Arch (pacman)**, sin requerir OpenMediaVault ni Cockpit.

## Instalación desde cero (one-liner)

```bash
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-webkvm.sh | sudo bash
```

El script clona el repositorio, detecta la distro, instala las dependencias **runtime**
(libvirt, QEMU, OVMF, swtpm), despliega el **binario precompilado** (que embebe el
frontend) e instala el servicio systemd. El servidor **nunca compila**: no necesita Go,
Node ni toolchain. Genera un certificado TLS auto-firmado, configura las redes NAT/bridge
y verifica el servicio.

Interactivo cuando se ejecuta en una terminal; usa valores por defecto cuando viene por
pipeline. Cada valor se puede forzar con variables de entorno (ver más abajo).

## Cómo se obtiene el binario

El binario embebe el frontend: es el único artefacto que el servidor necesita. Se
compila **una sola vez** en una máquina de desarrollo (o en el CI) y se publica:

```bash
make binary   # backend/webkvm
make dist     # dist/webkvm-<version>.tar.gz con binario + instalador + scripts + SHA256SUMS
```

En el servidor:

```bash
sudo WEBKVM_BINARY=backend/webkvm ./install.sh
# o con URL + checksum:
sudo WEBKVM_BINARY_URL=https://host/webkvm WEBKVM_BINARY_SHA256=<sha256> ./install.sh
```

El instalador **no instala Go/Node ni compila** en el servidor: requiere que le pases el
binario (`WEBKVM_BINARY`), una URL+checksum (`WEBKVM_BINARY_URL`), o un binario ya
presente en `backend/webkvm` dentro del checkout.

## Modo interactivo

En una terminal, el instalador pregunta:

1. **Puerto** (8080) y **bind address** (0.0.0.0).
2. **HTTPS** con certificado auto-firmado (recomendado) — el backend sirve TLS
   directamente; el certificado se puede descargar desde `https://IP:PUERTO/api/system/cert`.
3. **Dominio público** (opcional): si resuelve al servidor, se obtiene un certificado
   **Let's Encrypt automático** (autocert, con renovación); si no puede validarse,
   se cae al auto-firmado con ese dominio en el SAN.
4. **Redes**: `NAT` / `Bridge` / `Both` (recomendado). NAT = salida a Internet por el
   host (192.168.122.x). Bridge = bridge macvlan `br0` sobre la NIC física para que las
   VMs tengan IP propia en la LAN real.

## Variables

- `WEBKVM_DATA_DIR`: `/opt/webkvm`.
- `WEBKVM_PREFIX`: `/usr/local`.
- `WEBKVM_BIND_ADDR`: `0.0.0.0` en instalación nueva.
- `WEBKVM_PORT`: `8080` por defecto.
- `WEBKVM_BINARY`: ruta a binario local.
- `WEBKVM_BINARY_URL`: URL HTTPS del binario (con `WEBKVM_BINARY_SHA256` obligatorio).
- `WEBKVM_HTTPS=yes|no`: habilita HTTPS auto-firmado.
- `WEBKVM_TLS_DOMAIN`: dominio público opcional (Let's Encrypt automático).
- `NETWORK_MODE=nat|bridge|both|none`: redes que se configuran.
- `BRIDGE_DHCP`, `BRIDGE_STATIC_IP`, `BRIDGE_STATIC_GW`, `BRIDGE_STATIC_DNS`:
  configuración del bridge (por defecto DHCP).
- `WEBKVM_NONINTERACTIVE=1`: no pregunta, usa defaults.
- `WEBKVM_SERVICE`: nombre del unit systemd (por defecto `webkvm.service`).
- `WEBKVM_INSTALL_REPO` / `WEBKVM_INSTALL_BRANCH`: en `scripts/install-webkvm.sh`.

## Comportamiento

- Preflight accionable: KVM (`/dev/kvm` + cómo activarlo), amd64, RAM ≥2 GiB, disco
  ≥5 GiB, red y systemd.
- Instala libvirt, QEMU, OVMF y swtpm vía apt/dnf/pacman.
- Despliega el binario precompilado (nunca compila en el servidor).
- Escribe `server.bind_addr`, `server.port` y `server.tls_*` en `config.json` **antes** del
  primer arranque para que el servicio use el puerto/TLS pedidos desde el inicio.
- Conserva la configuración persistente en actualizaciones.
- Copia binario y unit anterior; restaura si el health-check falla.
- Comprueba `/api/health` (también por HTTPS) y libvirt al final.

## Rutas

- Binario: `/usr/local/bin/webkvm`.
- Datos: `/opt/webkvm`.
- Certificado TLS: `/opt/webkvm/certs/webkvm.crt` (+ `.key`).
- Unit: `/etc/systemd/system/webkvm.service`.
- Logs: `/opt/webkvm/logs/backend.log` y journald.
- Contraseña inicial: `/opt/webkvm/admin-password.initial`.

## Desinstalar

```bash
sudo ./uninstall.sh
```

Conserva los datos por defecto. Para borrar todo:

```bash
sudo PURGE_DATA=1 ./uninstall.sh
```

## Validación

```bash
systemctl is-active webkvm
curl -kfsS https://127.0.0.1:8080/api/health
virsh -c qemu:///system pool-list --all
```

## Seguridad

Cambia inmediatamente la contraseña inicial (`/opt/webkvm/admin-password.initial`). Con
HTTPS auto-firmado, descarga el certificado desde `/api/system/cert` e instálalo como
confiable para eliminar el aviso del navegador. En producción conviene `WEBKVM_BIND_ADDR=127.0.0.1`
detrás de un reverse proxy si ya tienes uno.

El instalador no acepta URLs HTTP para binarios y no compila código remoto implícito.

## Actualizaciones

Ejecuta de nuevo el instalador con un binario nuevo. No se sobrescribe `config.json`
existente, por lo que los ajustes de la UI se mantienen.

Si falla el arranque:

```bash
systemctl status webkvm
journalctl -u webkvm -n 100 --no-pager
```

## Limitaciones conocidas

- El binario debe compilarse antes (en una máquina de desarrollo o en el CI) y pasarse al
  servidor con `WEBKVM_BINARY`, `WEBKVM_BINARY_URL`+`WEBKVM_BINARY_SHA256`, o dejarse en
  `backend/webkvm`. El servidor no compila por diseño (para minimizar fallos).
- La publicación de binarios y sus checksums debe gestionarse por el pipeline de releases
  del proyecto.

## Licencia

Doble licencia **AGPLv3 (uso no comercial/comunidad) o Licencia Comercial (pago)**.
Ver `LICENSE.md` en la raíz del repositorio.