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