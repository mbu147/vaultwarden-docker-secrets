# vaultwarden-docker-secrets

`secret-sync` is a small container that downloads items from
[Vaultwarden](https://github.com/dani-garcia/vaultwarden) / Bitwarden with the
`bw` CLI and hands them to your Docker setup: as a `.secrets` dotenv file next
to the Compose file of each consumer project, and/or as secret global variables
in [Arcane](https://getarcane.app).

> This is a sync sidecar, not a Kubernetes operator. On Kubernetes use the
> [External Secrets Operator](https://external-secrets.io/) instead.

## Features

- **Opt-in per item**: only items with the custom field `sync_enabled = true`
  are synced. No per-secret configuration.
- **`.secrets` env file** per Compose project, found through the Compose
  working-dir label of labeled containers ([details](#env-file-per-compose-project)).
- **Arcane integration**: items are pushed as secret global variables,
  optionally restricted to agents ([details](#arcane-global-variables)).
- **Sync triggers**: polling interval plus an authenticated webhook
  (`POST /sync`) for instant updates.
- Configuration entirely through environment variables (config file optional);
  credentials from files (Docker secrets) or environment variables.
- Multi-arch images (amd64/arm64) published to GHCR on every version tag.

## How it works

```
Vaultwarden --(bw CLI: sync + list items)--> secret-sync --> <project>/.secrets
                                                       \--> Arcane global variables
```

1. Logs in with a Bitwarden **API key** and unlocks the vault with the master
   password. Session data lives only in memory and a temporary directory.
2. On every run it syncs the vault and selects all items with
   `sync_enabled = true`.
3. It writes the env files and pushes the Arcane variables, but only when a
   value changed (SHA-256 comparison).

## Marking items in Vaultwarden

Add these **custom fields** to an item (type text or boolean):

| Field | Required | Meaning |
|---|---|---|
| `sync_enabled` | yes | `true` (also `1`, `yes`, `on`) opts the item in. Missing or anything else: the item is ignored. |
| `sync_group` | no | Group used as a label target. Comma-separated list (`mariadb, caddy`) puts the item into every listed group. No `/`, `\`, `.` or `..`. |
| `sync_arcane_agents` | no | Comma-separated Arcane environment names or IDs the item's variables are limited to. Missing, empty or `all`: every agent. Only used with the [Arcane integration](#arcane-global-variables). |
| `sync_arcane_inject_all` | no | `true` injects the item's Arcane variables into every container's environment (default: only `${VAR}` interpolation). |
| `sync_env_file` | no | `false` (also `0`, `no`, `off`) excludes the item from the `.secrets` env files, even if a container label requests it. Default: `true`. |
| `sync_arcane` | no | `false` (also `0`, `no`, `off`) excludes the item from Arcane. Default: `true`, i.e. once `ARCANE_URL` and the API key are set, every synced item is pushed unless it opts out. |

Each item yields the values `password`, `username`, `notes`, `uri` (first URI)
and one per custom field. Empty values are skipped. Item names are sanitized:
characters other than `A-Z a-z 0-9 . _ -` become `_`. `sync_enabled`,
`sync_group`, `sync_arcane_agents`, `sync_arcane_inject_all`, `sync_env_file` and
`sync_arcane` themselves are
never synced. Two items that map to the same name, or a custom field that
collides with another value name, are reported as errors and skipped.

## Quick start (Compose)

1. In Vaultwarden create a **dedicated user** (ideally in an organization with
   access to only the needed items) and get its API key
   (*Settings → Security → Keys → View API key*).
2. Put the credentials into files outside of git, e.g. `bootstrap/bw_client_id`,
   `bw_client_secret`, `bw_password`, `sync_webhook_token`. Compose bind-mounts
   these files with their host permissions and ignores `uid`/`gid`/`mode`, while
   the container runs as UID 1000 (`node`). Make them readable for that user:
   `sudo chown 1000:1000 bootstrap/* && chmod 400 bootstrap/*` (or `chmod 444`).
3. Start [examples/compose.yaml](examples/compose.yaml). Configuration is done
   with environment variables (see below); [examples/config.yaml](examples/config.yaml)
   is an optional alternative.
4. Add `sync_enabled = true` (and optionally `sync_group`) to your items.
5. Label the consumers, see the next section.

## Env file per Compose project

For images without `*_FILE` support, secret-sync writes a dotenv file next to
the Compose file of the consumer. Compose labels every container with
`com.docker.compose.project.working_dir`; secret-sync reads it from containers
that carry the label `secret-sync.env` and writes `<working_dir>/.secrets`. The
label is a comma-separated list of targets:

- `<group>`: every item of that group
- `<group>/<item>`: only that item (sanitized item name)
- `<item>`: an item without group

```yaml
services:
  caddy:
    env_file:
      - path: .secrets
        required: false      # lets the very first `up` succeed
    labels:
      secret-sync.env: "caddy"
```

Enable it by mounting the directory that contains all Compose projects
**read-write at the same path Compose reports** (for example
`/app/data/projects`) and setting `SECRET_SYNC_ENV_FILE_DIR` to it.

- Variable names are `<ITEM>_<FILE>` in upper case with every non-alphanumeric
  character replaced by `_` (item `my db`, file `password` becomes
  `MY_DB_PASSWORD`). All containers of a project share one file, which holds the
  union of the items matched by their labels. Conflicting values for the same
  name are errors.
- Values are single-quoted (literal, no `$` interpolation); a value that contains
  `'` is double-quoted with escapes (`$` becomes `$$`).
- The file is written in place with mode `0600` (`SECRET_SYNC_ENV_FILE_MODE`).
  Only directories below `SECRET_SYNC_ENV_FILE_DIR` are written; anything else
  in a label is rejected, and symlinks or other non-regular files are never
  followed.
- A changed value updates the file but **does not restart** containers.
  Environment variables are fixed at container creation and `docker restart`
  does not re-read `env_file`; recreate the project (`docker compose up -d`, or
  a redeploy in Arcane) to apply the change.
- Containers must exist (running or created) for secret-sync to find the
  project. With `required: false` the first `up` creates them without the file,
  secret-sync then writes it, and the next recreate picks it up.
- **Ownership**: secret-sync runs as UID 1000 and must be allowed to create the
  file in the project directory. If another user owns the projects (Arcane, for
  example, uses `65532:65532`), the log shows `permission denied`. Run
  secret-sync as that user instead: set `user: "65532:65532"` and the same
  uid/gid on the `/tmp` tmpfs in the Compose file, and make the credential files
  readable for it (`chown 65532:65532 bootstrap/*`).
- The file is plaintext on disk and is never deleted automatically. Its
  readers are root (Docker/Compose) and the owner (UID 1000); make sure the
  project directory is not readable by others. Add `.secrets` to `.gitignore`.
  Arcane does not touch it (it only manages `.env`/`.env.global`).

## Arcane global variables

With `ARCANE_URL` set, secret-sync pushes **every synced item** (`sync_enabled`)
to [Arcane](https://getarcane.app) as **global variables with the secret flag**
(values are encrypted at rest in Arcane, but written in plaintext to Arcane's
`.env.global` on its agents). Create an API key in Arcane and give it to
secret-sync with `ARCANE_API_KEY_FILE` (preferred) or `ARCANE_API_KEY`.

- `ARCANE_ENABLED=false` switches the integration off centrally, even with
  `ARCANE_URL` set. Per item, `sync_arcane = false` opts out; everything else
  is pushed to all agents unless `sync_arcane_agents` limits it.
- Restrict an item to agents with `sync_arcane_agents = agent-a, agent-b`
  (environment names as shown in Arcane, or IDs; unknown names are reported
  with the list of known ones);
  without it (or with `all`) the variables apply to every agent.
- Variable names follow the same scheme as the env file (`<ITEM>_<FILE>`).
- Variables are created, or updated when the value changes. Arcane never returns
  secret values, so secret-sync remembers a hash per variable in memory; after a
  restart every variable is written once again.
- `sync_arcane_inject_all = true` on an item additionally injects its variables
  into every container's environment; by default they are only available for
  `${VAR}` interpolation in compose files.
- Variables that disappear from the vault (item deleted, `sync_enabled` or
  `sync_arcane` switched off, scope changed) are **deleted** in Arcane. Only
  variables created by secret-sync are touched: it lists them in the plain
  variable `SECRET_SYNC_MANAGED` (do not edit it). Variables that existed before
  this list was created, or were created by hand, are never deleted; remove
  such leftovers once manually. Nothing is deleted if the run had item errors.
- Arcane's API paths (`/api/variables`, header `X-Api-Key`) were taken from its
  source; verify against your Arcane version.

## Docker socket access

Only needed to list containers and read their Compose project directory (env
file feature). Use a socket proxy as in the example; it only needs
`GET /containers/json`. The proxy runs unprivileged, so it must be a member of
the group that owns `/var/run/docker.sock` on the host (for example `999` or
`992`). The example uses `992` by default via `${DOCKER_GID:-992}`. Override it
in the environment or in an optional `.env` file next to `compose.yaml`:

```sh
echo "DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)" > .env
```

Without it the proxy logs `socket not available ... permission denied`. See
[examples/.env.example](examples/.env.example).

## Configuration

Every setting can be given as an environment variable or in the optional YAML
file (`-config`, default `/etc/secret-sync/config.yaml`). Environment variables
win over the file.

| Environment variable | YAML key | Default | Description |
|---|---|---|---|
| `BW_SERVER` | `bitwarden.server` | Bitwarden cloud | Vaultwarden URL |
| `BW_CLIENTID_FILE` / `BW_CLIENTID` | `bitwarden.client_id_file` | – | API key client ID (file preferred) |
| `BW_CLIENTSECRET_FILE` / `BW_CLIENTSECRET` | `bitwarden.client_secret_file` | – | API key secret |
| `BW_PASSWORD_FILE` / `BW_PASSWORD` | `bitwarden.password_file` | – | Master password |
| `SECRET_SYNC_INTERVAL` | `interval` | `5m` | Polling interval (min `10s`) |
| `SECRET_SYNC_READY_FILE` | `ready_file` | `/tmp/secret-sync.ready` | Written after a fully successful sync |
| `SECRET_SYNC_WEBHOOK_LISTEN` | `webhook.listen` | off | e.g. `:8080` |
| `SECRET_SYNC_WEBHOOK_TOKEN_FILE` / `SECRET_SYNC_WEBHOOK_TOKEN` | `webhook.token_file` | – | Required with the webhook (file preferred) |
| `DOCKER_HOST` | `docker.host` | default socket | `tcp://host:port` or `unix:///path` |
| `SECRET_SYNC_ENV_FILE_DIR` | `env_file.dir` | off | Base directory of the Compose projects; enables the [env file](#env-file-per-compose-project) |
| `SECRET_SYNC_ENV_FILE_ENABLED` | `env_file.enabled` | `true` | Central switch; `false` disables the env files even with a dir set |
| `SECRET_SYNC_ENV_FILE_NAME` | `env_file.name` | `.secrets` | File name inside each project directory |
| `SECRET_SYNC_ENV_FILE_MODE` | `env_file.mode` | `0600` | Octal file mode of the env file |
| `ARCANE_URL` | `arcane.url` | off | Arcane base URL; enables the [Arcane integration](#arcane-global-variables) |
| `ARCANE_API_KEY_FILE` / `ARCANE_API_KEY` | `arcane.api_key_file` | – | Arcane API key (file preferred) |
| `ARCANE_ENABLED` | `arcane.enabled` | `true` | Central on/off switch; only effective with `ARCANE_URL` |
| `SECRET_SYNC_LOG_LEVEL` | – | `info` | `debug`, `info`, `warn`, `error` |
| `SECRET_SYNC_LOG_FORMAT` | – | `text` | `text` or `json` |

Secret values are never logged; only names and the first 8 characters of the
hash.

### Instant updates

Polling is always active. To sync immediately after a change, call the webhook:

```sh
curl -X POST -H "Authorization: Bearer <token>" http://secret-sync:8080/sync
```

Listening to Vaultwarden's WebSocket notifications for real push is on the
[roadmap](#roadmap).

## Command line

```
secret-sync [-config /etc/secret-sync/config.yaml] [-once] [-healthcheck] [-version]
```

## Security

See [docs/security.md](docs/security.md). Key points: use a dedicated
least-privilege Vaultwarden user, never pass credentials as plain environment
variables in production, access Docker only through a socket proxy, and keep
the container read-only without capabilities.

## Development

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.23-alpine sh -c "gofmt -l . && go vet ./... && go test ./..."
docker build -t secret-sync .
```

## Releases

Pushing a tag like `v1.2.3` runs [release.yml](.github/workflows/release.yml):
tests, multi-arch image build, push to `ghcr.io/mbu147/vaultwarden-docker-secrets`
with tags `1.2.3`, `1.2`, `1` (not for `v0.x`) and `latest` (not for
pre-releases such as `v1.2.3-rc.1`), a build provenance attestation, and an
automatically generated GitHub Release. Creating a Release in the GitHub UI
works too, since it pushes the tag. [ci.yml](.github/workflows/ci.yml) checks
formatting, vets, tests and builds the image on every pull request and push to
`main`.

## Contributing and license

Forks and pull requests are welcome, see [CONTRIBUTING.md](CONTRIBUTING.md).
Licensed under the [MIT License](LICENSE).

## Roadmap

- Listen to Vaultwarden WebSocket notifications for push-based sync (optional).
- Prometheus metrics.
- Attachments and TOTP as sources.

## Limitations

- The runtime image is Node based because the `bw` CLI is distributed via npm.
- Bitwarden Secrets Manager (`bws`) is not supported by Vaultwarden, so the
  regular vault is used via the `bw` CLI.
- Every run syncs the whole vault; the interval should not be very small on
  large vaults.
- Not tested against a live Vaultwarden in CI yet; unit tests use fakes.
