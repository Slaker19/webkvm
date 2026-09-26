# Changelog

Todos los cambios notables de este proyecto se documentan en este
fichero, siguiendo [Keep a Changelog](https://keepachangelog.com/es/1.1.0/)
y [Semantic Versioning](https://semver.org/lang/es/).

Versión en inglés: [CHANGELOG.md](CHANGELOG.md).

## [0.0.1] — 2026-09-26

Primera versión pública de WebKVM: un panel web autoalojado (un único
binario Go con el frontend Svelte embebido) para gestionar un host de
virtualización Linux.

### Añadido

- **Máquinas virtuales KVM/QEMU vía libvirt:** creación, clonado, edición de
  hardware, consola VNC/serie en el navegador, snapshots, discos y
  migración de almacenamiento.
- **Contenedores Incus/LXC** (módulo opcional) gestionados desde la misma
  interfaz que las VMs, incluido el catálogo de imágenes Incus. Los
  contenedores eligen su pool de almacenamiento al crearse y pueden
  moverse después entre pools de propósito `container`: el rootfs y sus
  snapshots se reubican con la migración nativa de Incus, con la
  instancia parada.
- **Almacenamiento:** pools locales y de red (NFS/SMB) con propósitos
  (`disk`, `container`, `iso`, `backup`, `template`), inspección de discos
  físicos y un almacén multimedia único para ISOs e imágenes.
- **Redes:** puentes, redes NAT/aisladas y redes L2 unificadas estilo
  Proxmox, con cortafuegos por VM.
- **Copias de seguridad** programadas y bajo demanda, con restauración e
  importación/exportación en formato vzdump de Proxmox para contenedores.
- **Plantillas** de VM y contenedor, con un interruptor por plantilla
  «compartir con todos los usuarios» reservado a administradores: una
  plantilla compartida la puede listar e instanciar cualquier usuario
  sin importar propietario, grupo ni etiquetas, y cada instanciación
  copia su disco completo.
- **Usuarios, roles y cuotas:** RBAC con ACL por recurso, cuotas de
  recursos, 2FA/TOTP, tokens de API y registro de auditoría.
- **Cloud-init** para la configuración inicial de invitados (usuarios,
  claves SSH, red, paquetes).
- **Tienda de aplicaciones:** appliances y *helper scripts* listos para
  desplegar, y catálogo de imágenes cloud oficiales.
- **Passthrough PCI y USB** con comprobación previa de IOMMU/VFIO.
- **Interfaz en tres idiomas:** inglés, español y catalán.
- **Instalación:** instalador standalone multidistro (apt/dnf/pacman) con
  HTTPS autofirmado y *rollback*, imagen Docker, paquetes deb/rpm y un
  cliente de línea de comandos (`webkvm-cli`).


---

[0.0.1]: https://github.com/Slaker19/webkvm/releases/tag/v0.0.1
