# RELEASING WebKVM

[**English**](RELEASING.md) • [**Español**](RELEASING.es.md)

Maintainer checklist for a public release. The current target is
**`v0.1.12-fix02`**; the release and Docker workflows trigger on `v*` tags.

> The commands below are documentation. Do not run the commit, tag or push
> steps until the release has been reviewed and approved.

## 1. Preflight

From a clean checkout:

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

Expected: lint 0 issues, Go race tests pass, frontend 391 unit + 368 DOM tests,
i18n symmetric in `en/es/ca`, build succeeds.

## 2. Public-content review

The repository is public. Before staging:

```bash
git status --porcelain
git diff --check
```

- Do not publish `tasks/`, `graphify-out/`, `.agents/`, `.anti-slop/`,
  `.claude/`, `.opencode/`, private lab scripts, `ROADMAP-*.md` or local `.env`
  files. `.gitignore` already excludes them.
- `AGENTS.md` must contain no real hostnames, lab IPs, private paths or
  credentials.
- `docs/runbooks/HA-CLUSTER*` must use documentation placeholders (RFC 5737
  addresses and `node-a/b/c`), not a real topology.
- `scripts/deploy-to-dev.sh` defaults to a documentation address.
- No private keys, tokens or passwords: `git grep` for `PRIVATE KEY`, `ghp_`,
  `github_pat_`, `sk-`, `AKIA`, `xox` must return nothing.

## 3. Version and changelog

All of these must say `0.1.12-fix02` (the git tag keeps the `v`):

- `CHANGELOG.md` / `CHANGELOG.es.md` — section `[0.1.12-fix02]`, dated and with the
  native-package naming/ordering, i18n and dependency fixes.
- `frontend/package.json` and `package-lock.json`.
- `packaging/arch/PKGBUILD` (`_pkgver`, source URL).
- `README`/docs examples that show a release version.
- `[Unreleased]` must not contain private lab reports or links to ignored
  scripts.

## 4. Stage and commit

Review the untracked release files: new backend packages/tests, new frontend
components/tests, `docs/runbooks/`, `scripts/install-ha-stack.sh`,
`packaging/ocf/`, `packaging/standalone/test-ha-stack.sh` and the HA firewall
unit. Then:

```bash
git add -A
git diff --cached --check
git diff --cached --stat
```

The staged set must not contain ignored internal artifacts. Commit with a
release message, for example:

```bash
git commit -m "chore(release): v0.1.12-fix02"
git tag -a v0.1.12-fix02 -m "WebKVM v0.1.12-fix02"
git push origin main --follow-tags
```

## 5. GitHub release and assets

Pushing the `v0.1.12-fix02` tag runs `.github/workflows/release.yml`, which builds:

- `webkvm-0.1.12-fix02-linux_amd64`
- `webkvm-cli-0.1.12-fix02-linux_amd64`
- `webkvm-0.1.12-fix02.tar.gz`
- `webkvm_0.1.12.fix02_amd64.deb`
- `webkvm-0.1.12-1.fix02.x86_64.rpm`
- `SHA256SUMS`

Mark the release as **latest**. The in-app updater and `install.sh` read
`/releases/latest`, so a draft or older release will not be offered.

## 6. Validate the in-app update path

On a 0.1.11 native install:

1. Open `/#/status` as admin; the Update card must offer `v0.1.12-fix02`.
2. Run the update. Release mode downloads `webkvm-0.1.12-fix02-linux_amd64`, verifies
   the entry in `SHA256SUMS`, keeps `webkvm.previous`, restarts the service and
   health-checks it. Source mode pulls the checkout and rebuilds.
3. Confirm `webkvm version` reports `0.1.12-fix02` and `/api/health` is OK.
4. Start or delete a snapshot on an existing VM: the AppArmor label is added
   lazily; no manual XML edit.
5. Rollback test: use `webkvm.previous` (or `make rollback`) and confirm the
   previous version starts.

Docker installs must pull the new image instead of using the button.

## 7. AUR package

After the GitHub release exists:

```bash
# from packaging/arch/PKGBUILD
updpkgsums
```

Replace `sha256sums=('SKIP')` with the SHA-256 of
`webkvm-0.1.12-fix02.tar.gz` from `SHA256SUMS`, then push the AUR package
separately. Do not publish a package with `SKIP` as its checksum.

## 8. HA clusters

The in-app button updates only the node serving the panel. For a real HA
cluster, follow [HA-CLUSTER.md](runbooks/HA-CLUSTER.md#1211-updating-webkvm-in-an-ha-cluster):
disable `webkvm-app`, update every node with the same binary, re-enable and
verify. Roll back the same way from `webkvm.previous`.
