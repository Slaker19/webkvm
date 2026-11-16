# PUBLICAR WebKVM

[**English**](RELEASING.md) • [**Español**](RELEASING.es.md)

Checklist para mantenedores. El objetivo actual es **`v0.1.13`**; los workflows
de release y Docker se disparan con tags `v*`.

> Los comandos de abajo son documentación. No ejecutes el commit, el tag ni el
> push hasta que la release esté revisada y aprobada.

## 1. Comprobaciones previas

Desde un checkout limpio:

```bash
cd backend
go test -race ./...
go run github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.6 run --timeout=5m

cd ../frontend
npm ci
npm test
npm run test:dom
npm run lint
npx prettier --check .
npm run build

cd ..
bash scripts/check-i18n.sh
bash scripts/check-undefined-components.sh
```

Esperado: lint 0, tests Go con race OK, 391 unitarios + 368 DOM de frontend,
i18n simétrico en `en/es/ca` y build correcto.

## 2. Revisión de contenido público

El repositorio es público. Antes de preparar el índice:

```bash
git status --porcelain
git diff --check
```

- No publiques `tasks/`, `graphify-out/`, `.agents/`, `anti-slop/`, `.claude/`,
  `.opencode/`, scripts privados de laboratorio, `ROADMAP-*.md` ni `.env` locales. `.gitignore`
  ya los excluye.
- `AGENTS.md` no debe contener hostnames reales, IPs de laboratorio, rutas
  privadas ni credenciales.
- `docs/runbooks/HA-CLUSTER*` debe usar placeholders (direcciones RFC 5737 y
  `node-a/b/c`), no una topología real.
- `scripts/deploy-to-dev.sh` usa una dirección de documentación por defecto.
- Sin claves privadas, tokens ni contraseñas: `git grep` de `PRIVATE KEY`,
  `ghp_`, `github_pat_`, `sk-`, `AKIA`, `xox` no debe devolver nada.

## 3. Versión y changelog

Todo debe decir `0.1.13` (el tag de git conserva la `v`):

- `CHANGELOG.md` / `CHANGELOG.es.md` — sección `[0.1.13]`, fechada y con los
  cambios de drain, Web Push, OIDC, i18n y CI.
- `frontend/package.json` y `package-lock.json`.
- `packaging/arch/PKGBUILD` (`_pkgver`, URL de origen).
- Ejemplos de README/docs que muestren una versión de release.
- `[Unreleased]` no debe contener informes privados de laboratorio ni enlaces a
  scripts ignorados.

## 4. Preparar índice y commit

Revisa los ficheros nuevos de la release: paquetes/tests nuevos de backend,
componentes/tests nuevos de frontend, `docs/runbooks/`,
`scripts/install-ha-stack.sh`, `packaging/ocf/`,
`packaging/standalone/test-ha-stack.sh` y la unidad de firewall HA. Después:

```bash
git add -A
git diff --cached --check
git diff --cached --stat
```

El índice no debe contener artefactos internos ignorados. Commit de release:

```bash
git commit -m "chore(release): v0.1.13"
git tag -a v0.1.13 -m "WebKVM v0.1.13"
git push origin main --follow-tags
```

## 5. Release de GitHub y assets

Al empujar el tag `v0.1.13`, `.github/workflows/release.yml` genera:

- `webkvm-0.1.13-linux_amd64`
- `webkvm-cli-0.1.13-linux_amd64`
- `webkvm-0.1.13.tar.gz`
- `webkvm_0.1.13_amd64.deb`
- `webkvm-0.1.13-1.x86_64.rpm`
- `SHA256SUMS`

Marca la release como **latest**. El updater de la UI e `install.sh` leen
`/releases/latest`; un draft o una release antigua no se ofrecerá.

## 6. Validar la actualización desde la UI

En una instalación nativa 0.1.11:

1. Abre `/#/status` como admin; la tarjeta de actualización debe ofrecer
   `v0.1.13`.
2. Ejecuta la actualización. El modo release descarga
   `webkvm-0.1.13-linux_amd64`, verifica su entrada en `SHA256SUMS`, conserva
   `webkvm.previous`, reinicia el servicio y hace health check. El modo source
   hace pull y recompila.
3. Confirma que `webkvm version` dice `0.1.13` y que `/api/health` responde OK.
4. Arranca o borra un snapshot de una VM existente: la etiqueta AppArmor se
   añade de forma perezosa; no hay que editar el XML a mano.
5. Prueba el rollback con `webkvm.previous` (o `make rollback`) y confirma que
   arranca la versión anterior.

Las instalaciones Docker deben descargar la imagen nueva, no usar el botón.

## 7. Paquete AUR de terceros

El paquete `webkvm-bin` de AUR lo mantiene un tercero, no los
mantenedores de WebKVM. No lo publicamos y no podemos responder por él; no lo
presentes como oficial ni recomendado.

El `packaging/arch/PKGBUILD` de este repositorio es una referencia para
compilar en local con `makepkg` desde el tarball oficial. Si lo usas, actualiza
`sha256sums` con `updpkgsums` o copia el valor del `SHA256SUMS` de la release;
nunca compiles con `SKIP`.

## 8. Clústeres HA

El botón de la UI solo actualiza el nodo que sirve el panel. Para un clúster HA
real, sigue [HA-CLUSTER.es.md](runbooks/HA-CLUSTER.es.md#1211-actualizar-webkvm-en-un-clúster-ha):
deshabilita `webkvm-app`, actualiza todos los nodos con el mismo binario,
reactiva y verifica. El rollback se hace igual desde `webkvm.previous`.
