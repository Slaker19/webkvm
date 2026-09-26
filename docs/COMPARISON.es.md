# WebKVM frente a Plataformas Alternativas de Virtualización

Comparativa técnica detallada y objetiva entre **WebKVM**, **Proxmox VE**, **Cockpit (Machines)**, **virt-manager** y **Portainer**.

[**English**](COMPARISON.md) • [**Español**](COMPARISON.es.md)

---

## Matriz Comparativa de Características

| Categoría Arquitectónica | WebKVM | Proxmox VE | Cockpit (Machines) | virt-manager | Portainer |
|---|---|---|---|---|---|
| **Arquitectura de Binario** | Binario único Go (~14 MB) + Svelte 5 embebido | Múltiples daemons en Perl/C (PVE proxy, daemons) | Módulos Python / D-Bus del sistema | Cliente de escritorio GTK | Backend Go + Daemon de Docker |
| **Consumo de RAM en Reposo** | **~15 MB RAM** | 1.2 GB – 2.5 GB RAM | ~80 MB RAM | N/A (Cliente local) | ~40 MB RAM |
| **Preservación del Host** | **100% intacto** (Distro estándar) | Requiere fork modificado de Debian | Intacto | Intacto | Requiere Docker |
| **Máquinas Virtuales KVM** | Sí — soporte completo | Sí — soporte completo | Sí — soporte básico | Sí — soporte completo | No (solo contenedores) |
| **Contenedores LXC / Incus** | **Sí — vista nativa Incus/LXD** | Parcial — scripts propios de PVE | No | No | No (solo apps Docker) |
| **Cloud-Init e Image Hub** | **Sí — despliegue en 5 s con Image Hub** | Parcial — clonado manual de plantillas | No — manual | No — manual | Parcial — plantillas de apps |
| **Calidad de Terminal Web** | **WebGL + Unicode 11 + teclas TUI** | Parcial — xterm.js básico | Parcial — terminal básica | No — solo terminal de escritorio | Parcial — exec shell básico |
| **Tasa de Descarga y ETA** | **Sí — MB/s y tiempo restante en vivo** | Parcial — solo porcentaje | No | No | Parcial — pull de capas básico |
| **CLI Local Zero-Token** | **Sí — autenticación automática sin token** | Parcial — `pvesh` / `qm` / `pct` | No | Parcial — solo `virsh` | No |
| **Matriz Multi-View Web** | **Sí — multiconsola interactiva** | No — 1 consola a la vez | No — 1 consola a la vez | Parcial — múltiples ventanas locales | No |
| **Rollback Seguro de Firewall**| **Sí — auto-rollback en 30 s** | No — firewall manual de PVE | No — módulo firewalld | No | No |
| **Tickets Efímeros WebSocket** | **Sí — 30 s de un solo uso en RAM** | Parcial — tickets de sesión / cookies | Parcial — auth por cookie | No — socket local | Parcial — JWT en cabeceras |

---

## Análisis Comparativo Profundo

### 1. WebKVM frente a Proxmox VE

**Cuándo elegir Proxmox VE:**
- Necesitas un clúster de centro de datos empresarial de más de 3 hipervisores dedicados con quorum Corosync, migración de máquinas virtuales en caliente por red 10GbE y almacenamiento Ceph distribuido.
- Vas a dedicar el servidor físico de forma exclusiva a la virtualización y no planeas ejecutar otros servicios en el anfitrión.

**Cuándo elegir WebKVM:**
- Quieres convertir un servidor Linux existente (Ubuntu, Debian, Fedora, Arch, AlmaLinux) en una plataforma de virtualización sin formatear ni sustituir el sistema operativo.
- Valoras un consumo mínimo de recursos (ahorrando 1–2 GB de RAM para tus máquinas reales en homelabs, mini-PCs o servidores dedicados).
- Buscas una interfaz unificada y estética para máquinas virtuales KVM y contenedores de sistema Incus.
- Prefieres la simplicidad de un único binario ejecutable con actualizaciones instantáneas sin riesgo de romper dependencias del sistema.

---

### 2. WebKVM frente a Cockpit (Módulo Machines)

**Cuándo elegir Cockpit:**
- Solo requieres supervisión básica del sistema (ver logs de systemd, discos montados y cuentas de usuario) y levantas máquinas virtuales de forma muy ocasional.

**Cuándo elegir WebKVM:**
- Necesitas un flujo de virtualización completo y ágil:
  - **Contenedores Incus**: Cockpit solo soporta KVM vía libvirt y carece de soporte para contenedores de sistema.
  - **Image Hub y Cloud-Init**: Cockpit obliga a descargar ISOs y pasar por instaladores interactivos; WebKVM despliega sistemas listos en 5 segundos.
  - **Almacenamiento y Redes**: WebKVM gestiona pools dedicados con validación de tipo, redes duales LAN/NAT y reglas de firewall con rollback seguro.
  - **Terminal de Alto Rendimiento**: Terminal WebGL con Unicode 11 y barra de atajos (`F1`–`F12`), permitiendo usar `btop`, `htop` o `mc` con total fluidez.

---

### 3. WebKVM frente a virt-manager

**Cuándo elegir virt-manager:**
- Estás sentado frente a una estación de trabajo Linux con sesión gráfica de escritorio (X11/Wayland) y prefieres una ventana GTK tradicional.

**Cuándo elegir WebKVM:**
- Quieres acceso 100% web desde cualquier dispositivo (portátil, tablet, móvil o Chromebook) sin lidiar con redirección de puertos X11 ni clientes VNC locales.
- Administras servidores remotos, VPS en la nube o equipos headless.
- Necesitas gestión multiusuario con roles y control de acceso (Admin, Operador, Visor).

---

## Conclusión

WebKVM ocupa el **espacio idóneo** en la infraestructura moderna:
1. Elimina la pesadez y rigidez de los hipervisores dedicados tradicionales.
2. Supera ampliamente las capacidades de los paneles genéricos de administración de servidores.
3. Brinda una experiencia de usuario rápida, estética y moderna manteniendo el control absoluto sobre el servidor Linux anfitrión.
