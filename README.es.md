# WebKVM

<div align="center">

**Gestor Nativo y Ultra-Ligero de Máquinas Virtuales y Contenedores para Linux (Binario Único)**

[![CI Status](https://github.com/Slaker19/webkvm/actions/workflows/ci.yml/badge.svg)](https://github.com/Slaker19/webkvm/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/Go-1.26-00ADD8?style=flat&logo=go)](https://golang.org)
[![Svelte Version](https://img.shields.io/badge/Svelte-5_Runes-FF3E00?style=flat&logo=svelte)](https://svelte.dev)
[![TailwindCSS](https://img.shields.io/badge/TailwindCSS-v4-38B2AC?style=flat&logo=tailwind-css)](https://tailwindcss.com)
[![Licencia: AGPL v3](https://img.shields.io/badge/Licencia-AGPLv3-blue.svg)](LICENSE)
[![Sin Dependencias](https://img.shields.io/badge/Dependencias_en_Runtime-0-success.svg)](#arquitectura)

[**English**](README.md) • [**Español**](README.es.md) • [**Manual de Uso**](docs/USAGE.es.md) • [**Preguntas Frecuentes**](docs/FAQ.es.md) • [**Guía CLI**](docs/CLI.es.md) • [**Referencia de API**](docs/API.es.md)

</div>

---

## ¿Qué es WebKVM?

**WebKVM** convierte cualquier servidor Linux existente (Ubuntu, Debian, Fedora, Arch, AlmaLinux, Rocky) en una plataforma de virtualización y nube privada **en 1 minuto, sin formatear ni secuestrar el sistema operativo**.

Desarrollado en **Go** con enlaces nativos CGO a `libvirt` y una interfaz web SPA en **Svelte 5** incrustada en el ejecutable (`//go:embed`), WebKVM compila en un **único binario estático de ~14 MB**. Consume **unos 15 MB de memoria RAM** en reposo y ofrece gestión unificada para **Máquinas Virtuales QEMU/KVM** y **Contenedores de Sistema Incus/LXC**.

```text
┌─────────────────────────────────────────────────────────────────────────┐
│                           Interfaz Web WebKVM                           │
│                 Svelte 5 • Tailwind v4 • Terminal WebGL                 │
└────────────────────────────────────┬────────────────────────────────────┘
                                     │ HTTPS / WSS / REST
┌────────────────────────────────────▼────────────────────────────────────┐
│                    Servidor WebKVM (Binario Único Go)                   │
│   Auth / JWT • WebSocket Proxy • Image Hub • Firewall • Storage Pools   │
└──────────────────┬──────────────────────────────────┬───────────────────┘
                   │ CGO / Socket Unix                │ REST / Socket Unix
┌──────────────────▼──────────────┐ ┌─────────────────▼───────────────────┐
│          QEMU / KVM             │ │            Incus / LXD              │
│    Virtualización Hardware      │ │       Contenedores de Sistema       │
│  (Ubuntu, Windows, FreeBSD...)  │ │     (Alpine, Debian, Ubuntu...)     │
└─────────────────────────────────┘ └─────────────────────────────────────┘
```

---

## ¿Por qué WebKVM? (El Punto Óptimo)

| Característica / Métrica | WebKVM | Proxmox VE | Cockpit (Machines) | virt-manager |
|---|---|---|---|---|
| **Formato de Distribución** | **Binario único (~14 MB)** | ISO completa / Secuestro del SO | Módulos del sistema y D-Bus | Aplicación de escritorio GTK |
| **Consumo de RAM en Reposo** | **~15 MB** | 1.2 GB – 2.5 GB | ~80 MB | N/A (Cliente local) |
| **Preservación del Host** | **100% intacto** (Distro estándar) | Requiere fork modificado Debian | Intacto | Intacto |
| **Máquinas Virtuales KVM** | Sí — soporte total | Sí — soporte total | Sí — soporte básico | Sí — soporte total |
| **Contenedores Incus / LXC** | **Sí — vista unificada nativa** | Parcial — scripts LXC propios | No | No |
| **Cloud-Init e Image Hub** | **Sí — despliegue en 5 segundos** | Parcial — plantillas manuales | No — manual | No — manual |
| **Terminal Web Integrada** | **WebGL + Unicode 11 + teclas TUI** | Parcial — noVNC / xterm básico | Parcial — terminal básica | No — solo terminal local |
| **Velocidad y ETA en Vivo** | **Sí — MB/s y tiempo restante real** | Parcial — solo porcentaje | No | No |
| **CLI Local Zero-Token** | **Sí — autenticación automática sin token** | Parcial — `pvesh` / `qm` | No — `cockpit-bridge` | Parcial — solo `virsh` |
| **Matriz Multi-Consola en Vivo**| **Sí — Multi-View interactivo** | No — 1 VM a la vez | No — 1 VM a la vez | Parcial — múltiples ventanas |

---

## Instalación Rápida (1 Minuto)

### Instalación Nativa (Recomendada)

Ejecuta el instalador desatendido como root en tu servidor:

```bash
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-webkvm.sh | sudo bash
```

El instalador detecta automáticamente el gestor de paquetes (`apt`, `dnf`, `pacman`), instala los componentes de virtualización necesarios (`qemu-kvm`, `libvirt-daemon-system`), configura la red dual (Bridge LAN + NAT Aislado), genera los certificados TLS y activa el servicio `webkvm.service` en systemd.

**Acceso al panel**:
- URL: `https://<IP-DEL-SERVIDOR>:8080` (o `https://localhost:8080` si es local).
- La contraseña inicial se muestra al terminar la instalación y queda guardada en `/opt/webkvm/admin-password.initial`.

### Instalación con Docker

Si prefieres ejecutar WebKVM en un contenedor frente al socket libvirt del host:

```bash
curl -fsSL https://raw.githubusercontent.com/Slaker19/webkvm/main/scripts/install-docker.sh -o /tmp/webkvm-docker-install.sh && sudo bash /tmp/webkvm-docker-install.sh
```
*(Consulta [docs/DOCKER.es.md](docs/DOCKER.es.md) para volúmenes y configuración detallada).*

---

## Características Principales

### 1. Cómputo Híbrido Unificado (KVM + Contenedores Incus)
- Gestiona máquinas virtuales pesadas (Windows, Linux, BSD) y contenedores de sistema ultraligeros LXC desde un único panel intuitivo.
- Métricas de CPU, RAM, Disco y Red en tiempo real vía Server-Sent Events (SSE).
- Redimensionado de discos en caliente sin reiniciar el sistema invitado.

### 2. Image Hub y Despliegues Cloud-Init en 5 Segundos
- Catálogo con imágenes oficiales en la nube (`.qcow2`) de Ubuntu, Debian, Alpine, Fedora, Rocky y AlmaLinux.
- Despliegues instantáneos mediante capas Copy-On-Write (COW) sobre discos base.
- Inyección automática de claves públicas SSH, usuarios, contraseñas y recetas `#cloud-config`.

### 3. Terminal WebGL del Host de Alto Rendimiento
- Motor `@xterm/xterm` moderno con **aceleración gráfica por GPU (WebGL)** y soporte **Unicode 11** para rendering perfecto de `btop`, `htop`, `mc`, `nano` y `yazi`.
- Barra de atajos para herramientas TUI con teclas de función (`F1`–`F12`), `Esc`, `Tab` y `Ctrl+C`.
- **Persistencia en Segundo Plano**: Cambiar de sección en WebKVM no interrumpe ni reinicia las sesiones de terminal activas.
- Autenticación segura mediante PAM del sistema (sin logins automáticos inseguros).

### 4. Métricas de Descarga y Velocidad en Tiempo Real
- Tasa de transferencia suavizada con media móvil (ej. `52.4 MB/s · 120 MB / 1.8 GB · ETA 25s (45%)`).
- Seguimiento global en el panel inferior desplegable **Task Drawer** (`Ctrl+J` / `⌘J`).

### 5. Matriz Multi-View Interactiva
- Monitoriza e interactúa con múltiples consolas gráficas (noVNC) en una cuadrícula en vivo simultánea.

### 6. Seguridad Pragmática y Defensiva
- **Tickets de WebSocket Efímeros**: Los WebSockets se autentican con tickets de un solo uso en RAM (TTL de 30s). Cero JWTs en URLs o historiales.
- **Dialer Seguro Anti-SSRF**: Las descargas bloquean rangos privados, loopback y CGNAT, re-validando cada salto HTTP.
- **Firewall con Auto-Rollback**: Las modificaciones del firewall incluyen un temporizador de confirmación de 30s para evitar bloqueos accidentales.

---

## CLI Local Zero-Token (`webkvm-cli`)

WebKVM incluye una herramienta de línea de comandos completa para administración por SSH y automatizaciones.

Al ejecutarse como root en el servidor local, **se auto-autentica sin requerir tokens ni contraseñas**:

```bash
# Listar todas las máquinas virtuales y contenedores
webkvm-cli vms list

# Crear una máquina virtual Debian 12 en 5 segundos
webkvm-cli vms create --name debian-prod --ram 2048 --vcpus 2 --disk 20 --image debian-12

# Crear un contenedor Incus Alpine Linux
webkvm-cli vms create --name alpine-ct --ram 512 --vcpus 1 --type container --image images:alpine/3.21

# Descargar una imagen cloud oficial
webkvm-cli images pull-cloud ubuntu-24.04

# Salida estructurada en JSON para scripts con jq
webkvm-cli --json vms list | jq '.[] | select(.state=="running") | .name'
```
*(Consulta el manual completo en [docs/CLI.es.md](docs/CLI.es.md)).*

---

## Suite de Documentación

| Documento | Descripción |
|---|---|
| [**Manual de Usuario (USAGE.es.md)**](docs/USAGE.es.md) | Creación de VMs, pools de almacenamiento, redes, cloud-init y backups. |
| [**Guía de Instalación (INSTALLATION.es.md)**](docs/INSTALLATION.es.md) | Instalación paso a paso, puertos, redes duales, certificados Let's Encrypt y systemd. |
| [**Preguntas Frecuentes (FAQ.es.md)**](docs/FAQ.es.md) | Más de 30 respuestas sobre arquitectura, Wi-Fi en portátiles, formatos y resolución de problemas. |
| [**Comparativa Técnica (COMPARISON.es.md)**](docs/COMPARISON.es.md) | Comparativa frente a Proxmox VE, Cockpit y virt-manager. |
| [**Manual de CLI (CLI.es.md)**](docs/CLI.es.md) | Guía completa de comandos y sintaxis de `webkvm-cli`. |
| [**Referencia de API (API.es.md)**](docs/API.es.md) | Especificación de endpoints REST y WebSockets con payloads JSON. |
| [**Arquitectura Interna (ARCHITECTURE.es.md)**](docs/ARCHITECTURE.es.md) | Detalles técnicos: bindings CGO, Incus driver y motor Svelte 5 embebido. |
| [**Despliegue con Docker (DOCKER.es.md)**](docs/DOCKER.es.md) | El mismo binario en un contenedor: requisitos del host, montajes y la excepción de la autoactualización. |
| [**Runbooks operativos**](docs/runbooks/RUNBOOKS.es.md) | Operación diaria, actualización y reversión, recuperación ante desastres y modelo de seguridad. |
| [**Política de Seguridad (SECURITY.es.md)**](SECURITY.es.md) | Modelo de seguridad, mitigaciones SSRF y reporte de vulnerabilidades. |

---

## Compilar desde el código fuente

Requisitos previos: Go ≥ 1.26, Node.js ≥ 20, `libvirt-dev`, `gcc`.

```bash
# Clona el repositorio
git clone https://github.com/Slaker19/webkvm.git
cd webkvm

# Compila el binario único completo (compila el frontend Svelte y lo
# incrusta en backend/webkvm)
make build

# Ejecuta los tests unitarios
go test ./...
cd frontend && npm test
```

---

## Licencia

WebKVM es software libre de código abierto bajo la licencia [GNU Affero General Public License v3.0 (AGPLv3)](LICENSE).
