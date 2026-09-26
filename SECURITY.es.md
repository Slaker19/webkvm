# Política de Seguridad y Reporte de Vulnerabilidades

[**English**](SECURITY.md) • [**Español**](SECURITY.es.md)

---

## Versiones Soportadas

Solo se publican parches de seguridad para la última versión publicada y para la rama `main` (desarrollo activo).

| Versión | Estado de Soporte |
|---|---|
| Última Release | :white_check_mark: |
| Rama `main` | :white_check_mark: |
| Versiones Anteriores | :x: |

---

## Prácticas de Seguridad y Defensa en Profundidad

WebKVM incorpora controles de seguridad defensiva en múltiples capas:

### 1. Autenticación de WebSockets por Tickets de Un Solo Uso
- WebKVM descarta el paso de tokens JWT persistentes por query parameters, evitando que queden registrados en logs de acceso o servidores proxy intermedios.
- Utiliza **tickets efímeros de un solo uso** generados en RAM con un tiempo de vida (TTL) de 30 segundos. El ticket se destruye inmediatamente tras el handshake de conexión.

### 2. Protección Anti-SSRF y Anti-DNS-Rebinding
- Todas las descargas desde URLs externas (Image Hub, descarga de ISOs, imágenes base) pasan por un **dialer seguro** (`newSafeDownloadClient`).
- El dialer resuelve el DNS previamente y bloquea conexiones hacia bucle local (`127.0.0.0/8`), rangos privados RFC 1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), enlace local, CGNAT e IPv6 equivalentes.
- Cada redirección HTTP es re-validada de forma independiente antes de seguirla.

### 3. Aislamiento Estricto por Propósito de Pool (*Pool Purpose*)
- Los pools de almacenamiento se clasifican en `disk` o `iso`.
- Se bloquea la escritura de discos virtuales en pools de ISOs y viceversa, impidiendo la corrupción de volúmenes o sustituciones no autorizadas de imágenes.

### 4. Rollback Automático en Firewall (*Safe-Apply*)
- La aplicación de reglas de firewall en el host activa una ventana de confirmación de 30 segundos. Si el administrador pierde el acceso o no confirma los cambios, las reglas revierten automáticamente al estado anterior.

### 5. Terminal del Host con Aislamiento PAM
- La terminal interactiva invoca `/bin/login -p` en un PTY protegido, requiriendo credenciales válidas del sistema anfitrión y evitando aperturas automáticas inseguras de consolas root.

### 6. Almacenamiento Seguro de Secretos
- Los secretos (claves JWT, credenciales CIFS, tokens de API) se almacenan con permisos estrictos `0600` y quedan excluidos de las copias de seguridad de configuración y de las respuestas de la API.

---

## Reporte de Vulnerabilidades

Si encuentras una vulnerabilidad de seguridad, por favor **no la publiques en issues públicos de GitHub**.

1. Abre un reporte privado desde **[GitHub Security Advisories](https://github.com/Slaker19/webkvm/security/advisories/new)** (pestaña Security -> Report a vulnerability). El reporte solo es visible para ti y para el equipo mantenedor hasta que exista una corrección publicada.
2. Incluye:
   - Versión o commit afectado.
   - Descripción detallada y pasos para reproducir el fallo.
   - Impacto potencial o Prueba de Concepto (PoC).
   - Propuesta de mitigación (si la tienes).
3. Confirmaremos la recepción en un plazo máximo de **72 horas** y coordinaremos la divulgación responsable.
