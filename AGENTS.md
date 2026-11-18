# Agent instructions

`secret-sync` is a Go service that pulls Vaultwarden/Bitwarden items that have the custom field `sync_enabled` via the `bw` CLI and writes them as `.secrets` dotenv files into Compose projects and/or pushes them to Arcane as global variables. All code, comments and docs are in **English**.

## Build, test, lint

Go is usually not installed locally; run everything in a container (module root is the repo root, Go 1.23):

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.23-alpine sh -c "gofmt -l . && go vet ./... && go test ./..."
# single test
docker run --rm -v "$PWD:/src" -w /src golang:1.23-alpine go test ./internal/syncer -run TestEnvFile -v
# image
docker build -t secret-sync .
```

CI (`.github/workflows/ci.yml`) requires `gofmt -l .` to print nothing, plus `go vet` and `go test -race`. Files must use LF line endings (`.gitattributes`); on Windows the editor may write CRLF, which makes `gofmt -l` flag every file. Fix with `gofmt -w .`.

## Architecture

Flow: `cmd/secret-sync/main.go` wires everything → `internal/syncer` runs a loop (interval ticker + webhook trigger) → each run calls `bw.Client.Fetch` (`bw sync` + `list items`, decrypted snapshot) → `Vault.Synced` selects items whose custom field `sync_enabled` is truthy → `syncEnvFiles` and `syncArcane` push them (unchanged values are skipped by SHA-256 comparison).

- `internal/config`: YAML (optional, strict keys) plus env overrides (`applyEnv`) and defaults/validation.
- `internal/bw`: wraps the `bw` CLI as a subprocess (credentials only via the subprocess env; temp app data dir) and maps items to `SyncedItem`s (layout, sanitizing, collisions).
- `internal/backend`: generic in-place `File` writer and `Hash`, used by the env file and Arcane change detection.
- `internal/docker`: stdlib-only Docker Engine API client (unix socket or tcp). Used only to list containers. Do not add the Docker SDK unless necessary.
- `internal/syncer`: orchestration, env files, Arcane sync, webhook (`POST /sync`, `GET /healthz`).

## Behavior that is easy to break

- **Values**: `password`, `username`, `notes`, `uri` plus one per custom field per item. `sync_enabled`, `sync_group`, `sync_env_file`, `sync_arcane` and both `sync_arcane_*` fields are control fields and never synced. Empty values are skipped; name collisions are errors and skip the item.
- **Writes are in place** (truncate + write, same inode, chmod around it) via `backend.File`; `TestFileWriteKeepsInode` guards this.
- **Targets**: `SyncedItem.Targets()` is the single source for label targets: `group`, `group/item` (or just `item` without group).
- **Env files** (`internal/syncer/envfile.go`): containers labeled `secret-sync.env=<targets>` get `<com.docker.compose.project.working_dir>/<env_file.name>` (dotenv, one file per project, union of matched items). The working dir must stay below `env_file.dir`; non-regular files are refused. Variable names come from `SyncedItem.EnvName` (`<ITEM>_<FILE>` upper-case). No restart (env needs a recreate).
- **Arcane** (`internal/arcane`, `syncer/arcanesync.go`): items with `sync_enabled` are created/updated as secret global variables via `/api/variables` (header `X-Api-Key`) when `ARCANE_URL` is set and `ARCANE_ENABLED` is not false; `sync_arcane_agents` scopes them, `sync_arcane_inject_all` sets inject. Secret values are not returned by Arcane, so unchanged detection uses an in-memory hash. Variables are deleted when they vanish from the vault, but only those listed in the manifest variable `SECRET_SYNC_MANAGED` (IDs `KEY|agentIDs` joined by `;`); no deletion after item errors. Both `sync_arcane_*` fields are control fields too.
- Never log secret values; log only names and the first 8 chars of the hash.
- Credentials (`config.ReadCredential`): the `*_file` option wins, otherwise the plain env var (`BW_CLIENTID`, `BW_CLIENTSECRET`, `BW_PASSWORD`, `ARCANE_API_KEY`, `SECRET_SYNC_WEBHOOK_TOKEN`).
- Config keys are parsed strictly: removing or renaming a YAML key is a breaking change. Security expectations live in [docs/security.md](docs/security.md).
- The healthcheck (`-healthcheck`) only checks for the ready file, written after a fully successful sync.
- The Docker client is created only if `DOCKER_HOST`/`docker.host` is set or the default socket exists (`dockerAvailable` in `main.go`).

## Conventions

- Tests use fakes for the vault source and Docker (`syncer_test.go`); there is no live Vaultwarden in CI. Keep new logic behind the existing interfaces (`VaultSource`, `Containers`) so it stays testable that way.
- User-facing behavior changes must update `README.md` (config table, semantics) and the files in `examples/` (`config.yaml`, `compose.yaml`).


