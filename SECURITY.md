# Security Policy & Vulnerability Reporting

[**English**](SECURITY.md) • [**Español**](SECURITY.es.md)

---

## Supported Versions

Security updates are actively maintained for the latest release and the `main` branch.

| Version | Supported |
|---|---|
| Latest Release | :white_check_mark: |
| `main` Branch | :white_check_mark: |
| Older Versions | :x: |

---

## Security Practices & Defense-in-Depth

WebKVM implements multi-layered defensive security controls:

### 1. Ephemeral Single-Use WebSocket Authentication
- Traditional WebSocket implementations pass persistent JWT tokens in query parameters, risking credential leakage in server access logs and intermediate reverse proxies.
- WebKVM uses **single-use ephemeral tickets** generated in RAM with a 30-second TTL. The ticket is immediately destroyed upon the initial WebSocket upgrade.

### 2. Anti-SSRF & DNS-Rebinding Protection
- All server-side URL downloads (Image Hub, ISO downloads, cloud base disks) go through a **safe dialer** (`newSafeDownloadClient`).
- The dialer resolves DNS upfront and rejects all private, loopback, link-local, carrier-grade NAT, or IPv6 multicast ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `127.0.0.0/8`, `169.254.0.0/16`, `100.64.0.0/10`, `::1`, `fc00::/7`, `fe80::/10`).
- Every HTTP redirect hop is independently re-validated.

### 3. Fail-Closed Storage Pool Purpose Isolation
- Storage pools are classified as `disk` or `iso`.
- Disks cannot be written to ISO pools, and ISO downloads cannot target disk pools, preventing volume corruption and untrusted image replacement.

### 4. Firewall Safe-Apply Auto-Rollback
- Host firewall rule applications trigger a 30-second confirmation timer. If connectivity is lost or unconfirmed by the operator, rules automatically revert to the previous working configuration.

### 5. Host Terminal PAM Isolation
- The WebGL terminal connects to `/bin/login -p` over a secure PTY. It requires valid Linux system user authentication, preventing unauthorized unauthenticated root access from the browser.

### 6. Secrets & Key Storage
- Secrets (JWT keys, CIFS credentials, API tokens) are written with strict `0600` file permissions and excluded from configuration backups and API responses.

---

## Reporting a Vulnerability

If you discover a security vulnerability, please **do not report it in public issues**.

1. Open a private report via **[GitHub Security Advisories](https://github.com/Slaker19/webkvm/security/advisories/new)** (Security tab -> Report a vulnerability). This keeps the report visible only to you and the maintainers until a fix is published.
2. Include:
   - Affected version or commit.
   - Detailed description and steps to reproduce.
   - Potential impact or Proof of Concept (PoC).
   - Suggested mitigation (if available).
3. We will acknowledge receipt within **72 hours** and coordinate responsible disclosure.
