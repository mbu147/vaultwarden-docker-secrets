# Copilot instructions

`secret-sync` is a Go service that pulls Vaultwarden/Bitwarden items that have the custom field `sync_enabled` via the `bw` CLI and writes them as files into a shared secrets volume for Docker Compose consumers, optionally rotating them and restarting consumers. All code, comments and docs are in **English**.

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

Flow: `cmd/secret-sync/main.go` wires everything → `internal/syncer` runs a loop (interval ticker + webhook trigger) → each run calls `bw.Client.Fetch` (`bw sync` + `list items`, decrypted snapshot) → `Vault.Synced` selects items whose custom field `sync_enabled` is truthy → every file of every item is compared by SHA-256 with the file on disk and written/rotated.

- `internal/config`: YAML (optional, strict keys) plus env overrides (`applyEnv`) and defaults/validation.
- `internal/bw`: wraps the `bw` CLI as a subprocess (credentials only via the subprocess env; temp app data dir) and maps items to `SyncedItem`s (layout, sanitizing, collisions).
- `internal/backend`: `File` writes files below `secret_dir`.
- `internal/docker`: stdlib-only Docker Engine API client (unix socket or tcp). Used only to list/restart containers. Do not add the Docker SDK unless necessary.
- `internal/syncer`: orchestration, restarts, pruning (`prune.go`), webhook (`POST /sync`, `GET /healthz`).

## Behavior that is easy to break

- **Layout**: `<secret_dir>/[<group>/]<sanitized item name>/<file>`; files `password`, `username`, `notes`, `uri` plus one per custom field. `sync_enabled` and `group` are control fields and never written. Empty values are skipped; name collisions are errors and skip the item.
- **File backend rewrites in place** (truncate + write, same inode, chmod around it). Compose bind-mounts secret files directly, so rename-based atomic writes would leave containers on the old file. `TestFileWriteKeepsInode` guards this.
- **Rotation**: a file that does not exist yet is just created and never triggers a restart. With `rotate: false`, an existing file is kept and only a warning is logged.
- **Restart targets**: containers labeled `secret-sync.restart=<group>|<group>/<item>|<item>[,...]`; a changed file produces the targets `group`, `group/item` (or just `item` without group).
- **Pruning**: `.secret-sync-managed` in `secret_dir` lists written files. Stale files are removed only on a run without errors; foreign files are never touched.
- Never log secret values; log only the file name and first 8 chars of the hash.
- The healthcheck (`-healthcheck`) only checks for the ready file, written after a fully successful sync.
- The Docker client is created only if `DOCKER_HOST`/`docker.host` is set or the default socket exists (`dockerAvailable` in `main.go`).

## Conventions

- Tests use fakes for the vault source and Docker (`syncer_test.go`); there is no live Vaultwarden in CI. Keep new logic behind the existing interfaces (`VaultSource`, `Containers`) so it stays testable that way.
- User-facing behavior changes must update `README.md` (config table, semantics) and the files in `examples/` (`config.yaml`, `compose.yaml`).


