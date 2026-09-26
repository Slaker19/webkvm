# WebKVM — Preguntas Frecuentes (FAQ)

Respuestas exhaustivas a las preguntas arquitectónicas, técnicas, operativas y de resolución de problemas más comunes sobre WebKVM.

[**English**](FAQ.md) • [**Español**](FAQ.es.md)

---

## Tabla de Contenidos

1. [General y Arquitectura](#1-general-y-arquitectura)
2. [Máquinas Virtuales y QEMU/KVM](#2-máquinas-virtuales-y-qemukvm)
3. [Contenedores Incus / LXC](#3-contenedores-incus--lxc)
4. [Redes e IPs Dinámicas](#4-redes-e-ips-dinámicas)
5. [Almacenamiento e Image Hub](#5-almacenamiento-e-image-hub)
6. [Terminal WebGL y Consolas](#6-terminal-webgl-y-consolas)
7. [Seguridad y Control de Acceso](#7-seguridad-y-control-de-acceso)
8. [Resolución de Problemas (Troubleshooting)](#8-resolución-de-problemas-troubleshooting)

---

## 1. General y Arquitectura

### ¿Qué diferencia a WebKVM de Proxmox VE o Cockpit?
- **Proxmox VE** requiere formatear la máquina y usar su propia distribución Debian modificada, ejecutando múltiples daemons pesados en Perl que consumen entre 1 y 2 GB de RAM en reposo.
- **Cockpit** es un panel administrativo básico que carece de soporte nativo para contenedores de sistema Incus/LXD, catálogo Image Hub, inyección automática de cloud-init, auto-rollback en firewall y terminal WebGL acelerada.
- **WebKVM** ofrece una experiencia ágil tipo nube privada empaquetada en un **único binario en Go de ~14 MB** que consume **menos de 20 MB de RAM**, instalándose sobre tu distribución Linux existente sin modificarla.

### ¿Cuáles son los requisitos mínimos del sistema?
- **CPU**: 1 núcleo (x86_64 con virtualización VT-x/AMD-V activada en BIOS/UEFI, o ARM64).
- **RAM**: 2 GB de RAM mínimo (WebKVM utiliza ~15 MB; el resto queda disponible para tus VMs y contenedores).
- **Disco**: 5 GB de espacio libre para binarios del sistema e imágenes base.
- **SO**: Cualquier distribución Linux estándar (Debian 11+, Ubuntu 20.04+, Fedora 38+, Arch Linux, AlmaLinux 9+, Rocky Linux 9+).

### ¿Funciona WebKVM en Raspberry Pi o placas ARM64?
Sí. Al compilarse para `linux/arm64`, WebKVM gestiona máquinas virtuales KVM ARM64 (mediante firmware UEFI AAVMF) y contenedores de sistema nativos Incus con aceleración total por hardware.

### ¿Se puede ejecutar WebKVM dentro de otra máquina virtual (Virtualización Anidada)?
Sí. Si el hipervisor anfitrión soporta virtualización anidada (ej. `kvm_intel.nested=1` o `kvm_amd.nested=1`), WebKVM detectará `/dev/kvm` y ejecutará instancias aceleradas por hardware sin inconvenientes.

---

## 2. Máquinas Virtuales y QEMU/KVM

### ¿Debo elegir formato de disco QCOW2 o RAW?
- **QCOW2 (Recomendado)**: Aprovisionamiento dinámico (solo ocupa en disco lo que el sistema invitado escribe), permite snapshots instantáneos, clonado ultrarrápido Copy-On-Write (COW) y expansión de volumen en caliente.
- **RAW**: Formato de bloques directos con rendimiento ligeramente superior en I/O masivo, pero ocupa todo el espacio reservado desde el primer momento y no soporta archivos backing COW ni snapshots instantáneos.

### ¿Cómo funciona el aprovisionamiento con Cloud-Init?
WebKVM utiliza el estándar `NoCloud`. Al crear una instancia a partir de una plantilla cloud, WebKVM genera una imagen ISO efímera que contiene las directivas `#cloud-config` (usuarios, claves SSH públicas, red, hostname y scripts de post-instalación). El sistema operativo invitado lee este volumen en el primer arranque y se autoconfigura en segundos.

### ¿Los snapshots son consistentes a nivel de sistema de archivos?
Sí. Cuando la máquina virtual tiene instalado el agente invitado (`qemu-guest-agent`, preconfigurado en las plantillas de WebKVM), WebKVM congela el sistema de archivos (`fsfreeze`) antes de capturar el snapshot, garantizando consistencia absoluta en bases de datos y archivos.

### ¿Se puede redimensionar el disco de una VM en caliente?
Sí. Para discos QCOW2 conectados por VirtIO, WebKVM permite **aumentar el tamaño del disco sin apagar la máquina virtual**. El kernel de Linux detecta automáticamente el nuevo tamaño del disco.

---

## 3. Contenedores Incus / LXC

### ¿Cuál es la diferencia entre contenedores Incus y Docker?
- **Docker / Podman**: Contenedores de aplicación diseñados para ejecutar un único proceso o microservicio (ej. `nginx` o `redis`) con sistemas de archivos efímeros.
- **Incus / LXC**: **Contenedores de sistema** que ejecutan una distribución Linux completa con su propio init `systemd`, servidor SSH, gestor de paquetes (`apt`/`apk`/`dnf`), syslog y cron. Se comportan exactamente como máquinas virtuales, pero arrancan en 200 ms y consumen apenas 30–50 MB de RAM.

### ¿Pueden KVM e Incus compartir la misma red?
Sí. Tanto las máquinas virtuales KVM como los contenedores Incus se conectan a los mismos bridges de Linux (`vmbr0` para la LAN y `vmbr1` para NAT privado), comunicándose entre sí a velocidad nativa de interfaz de red.

---

## 4. Redes e IPs Dinámicas

### ¿Qué ocurre si la IP del servidor cambia por DHCP?
WebKVM gestiona cambios de IP de forma completamente transparente:
1. **Sockets del Servidor**: El backend escucha en `0.0.0.0:8080`, aceptando conexiones en cualquier interfaz activa.
2. **Endpoints Dinámicos en Frontend**: La SPA resuelve todas las rutas de API y WebSockets a partir del host utilizado en el navegador (`window.location.host`).
3. **Aislamiento de Red de Invitados**: Las VMs en modo Bridge (`vmbr0`) mantienen sus propias IPs asignadas por el router, mientras que las VMs en `vmbr1` utilizan el router NAT interno de WebKVM (`100.0.0.1`).

### ¿Cómo uso WebKVM en un portátil conectado por Wi-Fi?
Las tarjetas Wi-Fi convencionales (`wlan0`) bloquean el modo Bridge directo con múltiples direcciones MAC. Para portátiles en Wi-Fi:
- Selecciona la **Red NAT (`vmbr1`)** al crear las instancias.
- WebKVM enruta el tráfico de las máquinas virtuales a través de la Wi-Fi del portátil de forma transparente.

### ¿Cómo me conecto usando el nombre de host en lugar de la IP?
Puedes acceder usando mDNS: `https://<nombre-servidor>.local:8080` (ej. `https://webkvm.local:8080`), soportado de forma nativa en macOS, iOS, Android, Linux y Windows.

---

## 5. Almacenamiento e Image Hub

### ¿Qué es la separación por "Pool Purpose"?
Para evitar errores y sobreescrituras accidentales, WebKVM clasifica los pools en:
- **Pools de tipo `disk`**: Exclusivos para discos raíz de VMs, volúmenes de datos y contenedores.
- **Pools de tipo `iso`**: Exclusivos para medios de instalación de solo lectura.
El backend valida esta condición en modo *fail-closed*, rechazando escrituras cruzadas.

### ¿Cómo acelera el Image Hub los despliegues?
En lugar de descargar ISOs interactivas, el Image Hub permite almacenar imágenes cloud pre-construidas (`ubuntu-24.04.qcow2`, `debian-12.qcow2`, `alpine-3.24.qcow2`). Al crear una VM, se genera una capa COW en milisegundos sin duplicar el disco original.

---

## 6. Terminal WebGL y Consolas

### ¿Por qué la terminal del host solicita login del sistema?
Por seguridad y control de accesos, WebKVM ejecuta `/bin/login -p` en un pseudo-terminal PTY protegido en lugar de abrir una shell root directa sin autenticar. Es necesario ingresar credenciales válidas de un usuario del sistema Linux.

### ¿Por qué funcionan de forma fluida `btop`, `htop`, `mc` y `nano`?
1. **Entorno TrueColor y UTF-8**: El PTY define `TERM=xterm-256color`, `COLORTERM=truecolor` y `LANG=C.UTF-8`.
2. **Motor Unicode 11**: xterm.js calcula anchos de celda exactos para emojis, caracteres braille y marcos de cajas.
3. **WebSocket Binario**: Transmisión continua sin errores de validación UTF-8 fragmentada.
4. **Aceleración WebGL**: Renderizado a 60 FPS por hardware GPU que elimina el tearing y parpadeo.

### ¿Cambiar de pestaña en WebKVM desconecta la terminal?
No. `HostConsole` permanece montada en segundo plano en `App.svelte`. Puedes navegar a *Máquinas Virtuales*, *Almacenamiento* o *Ajustes* y regresar con tus sesiones de `btop`, `nano` o compilaciones intactas.

---

## 7. Seguridad y Control de Acceso

### ¿Cómo se protegen las conexiones WebSocket?
WebKVM utiliza **tickets efímeros de un solo uso** almacenados en memoria con un tiempo de vida (TTL) de 30 segundos. Al abrir una terminal o consola, el frontend pide un ticket autenticado y lo consume en el handshake. El ticket se destruye de inmediato, evitando tokens JWT en URLs o logs de proxies.

### ¿Cómo funciona el dialer seguro anti-SSRF?
Al descargar imágenes o ISOs desde URLs remotas, WebKVM resuelve el DNS y bloquea destinos en bucle local (`127.0.0.0/8`), rangos privados RFC 1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), CGNAT o IPv6 equivalentes, re-validando cada redirección HTTP.

### ¿Qué es el "Safe-Apply" del firewall?
Al modificar las reglas del firewall del host, WebKVM las aplica de forma
provisional con una ventana de confirmación de 30 segundos (configurable entre
10 y 300 con `firewall.confirm_window_secs`). Si una regla defectuosa te deja
sin conectividad y no llegas a confirmar, el firewall vuelve solo al último
conjunto estable, evitando que te quedes fuera del servidor.

---

## 8. Resolución de Problemas (Troubleshooting)

### Olvidé la contraseña de administrador. ¿Cómo la restablezco?
Si tienes acceso root al servidor anfitrión:
```bash
# Actualizar la contraseña del usuario admin
webkvm-cli users update admin --password "NuevaContraseñaSegura123!"
```
O elimina `/opt/webkvm/users.json` y reinicia el servicio para re-generar las credenciales iniciales.

### ¿Qué verificar si una VM no arranca?
1. Comprueba el dispositivo de aceleración: `ls -l /dev/kvm`.
2. Verifica la memoria disponible: `free -h`.
3. Consulta los logs del servicio: `journalctl -u webkvm -n 50 --no-pager`.
