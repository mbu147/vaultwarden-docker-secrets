# Copilot instructions

`secret-sync` is a Go service that pulls items from Vaultwarden/Bitwarden via the `bw` CLI and writes them into Docker secrets (Compose files or Swarm secrets), optionally rotating them and restarting consumers. All code, comments and docs are in **English**.

## Build, test, lint

Go is usually not installed locally; run everything in a container (module root is the repo root, Go 1.23):

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.23-alpine sh -c "gofmt -l . && go vet ./... && go test ./..."
# single test
docker run --rm -v "$PWD:/src" -w /src golang:1.23-alpine go test ./internal/syncer -run TestRotation -v
# image
docker build -t secret-sync .
```

CI (`.github/workflows/ci.yml`) requires `gofmt -l .` to print nothing, plus `go vet` and `go test -race`. Files must use LF line endings (`.gitattributes`); on Windows the editor may write CRLF, which makes `gofmt -l` flag every file. Fix with `gofmt -w .`.

## Architecture

Flow: `cmd/secret-sync/main.go` wires everything → `internal/syncer` runs a loop (interval ticker + webhook trigger) → each run calls `bw.Client.Fetch` (one `bw sync` + `list items/folders/collections`, decrypted snapshot) → for every configured secret resolves the value (`Vault.Resolve`), compares the SHA-256 with the backend's current value, and writes/rotates.

- `internal/config`: YAML parsing with strict keys (`KnownFields`) plus defaults/validation. Rotation default resolution lives in `Config.RotateEnabled` (per-secret overrides global, default true).
- `internal/bw`: wraps the `bw` CLI as a subprocess. Credentials are passed only through the subprocess env; the app data dir is a temp dir.
- `internal/backend`: `Backend` interface (`Current` hash + `Write`). `File` for Compose, `Swarm` for Swarm.
- `internal/docker`: stdlib-only Docker Engine API client (unix socket or tcp, e.g. a socket proxy). Do not add the Docker SDK unless necessary.
- `internal/syncer`: orchestration, rotation actions (restart/signal), webhook (`POST /sync`, `GET /healthz`).

## Behavior that is easy to break

- **File backend rewrites in place** (truncate + write, same inode, chmod around it). Compose bind-mounts secret files directly, so rename-based atomic writes would leave containers on the old file. `TestFileWriteKeepsInode` guards this.
- **Swarm secrets are immutable**: a change creates `<name>_v<hash8>` with labels `secret-sync.name` / `secret-sync.hash`, rewrites `SecretID`/`SecretName` in every service spec that references older versions (the in-container `File.Name` is kept), then deletes old versions. The Swarm backend updates services itself, so `on_change` actions are skipped for it (`runAction`).
- **Rotation semantics**: a secret that does not exist yet is just created and never triggers actions. With `rotate: false`, an existing secret is kept and only a warning is logged.
- Rotation targets = `on_change.containers` plus running containers labeled `secret-sync.restart=<secret>[,<secret>]`.
- Label discovery (`internal/syncer/discovery.go`, opt-in via `discovery.labels`) turns `secret-sync.<name>.<key>` labels into file secrets on every run; the label's container becomes the rotation target. Config-file secrets win, conflicting declarations are rejected, and per-secret validation is shared via `config.NormalizeSecret`.
- Never log secret values; log only name and first 8 chars of the hash.
- The healthcheck (`-healthcheck`) only checks for the ready file, which is written after the first fully successful sync.
- The Docker client is created only if a secret uses the swarm backend or an `on_change` action (`needsDocker` in `main.go`).

## Conventions

- Tests use fakes for the vault source and Docker (`syncer_test.go`); there is no live Vaultwarden in CI. Keep new logic behind the existing interfaces (`VaultSource`, `Containers`, `Backend`) so it stays testable that way.
- User-facing behavior changes must update `README.md` (config table, semantics) and the files in `examples/` (`config.yaml`, `compose.yaml`, `compose.swarm.yaml`).
- Releases: pushing a tag `vX.Y.Z` triggers `.github/workflows/release.yml` (tests, multi-arch image to GHCR, GitHub Release). The binary version is injected via `-X main.version`.
- The runtime image is `node:alpine` with `@bitwarden/cli` pinned via the `BW_VERSION` build arg in the `Dockerfile`.
