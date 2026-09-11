# Security Policy

## Supported Versions

Solo se publican actualizaciones de seguridad para la **última release** del
proyecto y para `main` (estado de desarrollo). Las versiones anteriores quedan
sin soporte.

| Version        | Supported          |
| -------------- | ------------------ |
| latest release | :white_check_mark: |
| main           | :white_check_mark: |
| < latest       | :x:                |

## Reporting a Vulnerability

Si encuentras una vulnerabilidad, **no la publiques en issues públicos**.

- Reporta por email al autor: **alvinpp1908@gmail.com**
- Incluye: versión afectada, descripción, pasos para reproducir y, si la tienes,
  una propuesta de mitigación.
- Acusamos recibo en un plazo de **72 horas** y coordinamos el plazo de
  divulgación según la severidad.

## Prácticas del proyecto

- Secretos (JWT, webhooks, SMTP, SFTP, CIFS) almacenados con permisos `0600`,
  nunca devueltos por la API y excluidos de los backups de configuración.
- Backend con hardening de systemd (`NoNewPrivileges`, `ProtectSystem=full`,
  `CapabilityBoundingSet` restringido, `ReadWritePaths` acotado).
- Rate limiting de login con lockout y CIDRs de confianza configurables.
- Validación y escape de entradas de cloud-init; reglas de firewall aplicadas
  con argumentos separados (sin shell).

## Avisos conocidos (dependencias)

Dependabot revisa `backend/go.mod` y `frontend/package.json`. Estado a
2026-09-11:

| ID | Paquete | Rango vulnerable | Parche | Notas |
| -- | ------- | ---------------- | ------ | ----- |
| [GHSA-64f3-v33m-w89f](https://github.com/advisories/GHSA-64f3-v33m-w89f) / CVE-2026-55621 | `github.com/lxc/incus/v6` | `<= 6.23.0` | **ninguno aún** | Bypass de restricción de proyecto al copiar volúmenes custom entre proyectos. `v6.23.0` es la última publicada; se actualizará en cuanto salga `v6.23.1+`. Impacto práctico bajo: WebKVM habla con el daemon Incus local (socket unix, proyecto `default`) y no expone copia de volúmenes entre proyectos. |

Cuando upstream publique una versión parcheada: `go get
github.com/lxc/incus/v6@latest`, `go mod tidy`, rebuild y re-ejecutar el QA.
