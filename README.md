# vaultwarden-docker-secrets

`secret-sync` is a small container that downloads items from
[Vaultwarden](https://github.com/dani-garcia/vaultwarden) / Bitwarden with the
`bw` CLI and writes them into Docker secrets. It supports **Docker Compose**
(files) and **Docker Swarm** (versioned Swarm secrets) and can **rotate**
secrets automatically when they change in the vault.

> This is a sync sidecar, not a Kubernetes operator. On Kubernetes use the
> [External Secrets Operator](https://external-secrets.io/) instead.

## Features

- Maps vault items (by name or ID, optionally narrowed by folder/collection) to
  secrets. Fields: `password`, `username`, `notes`, `uri`, `custom:<name>`.
- **Backends**
  - `file`: writes files for Compose. Files are rewritten *in place*, so
    bind-mounted secret files stay valid for running containers.
  - `swarm`: creates immutable, versioned secrets (`<name>_v<hash8>`), points
    all services at the new version and removes old ones.
- **Rotation switch** (`rotate: true|false`), globally and per secret.
- **Rotation actions**: restart or signal containers, configured in YAML or via
  the container label `secret-sync.restart=<secret>[,<secret>...]`.
- **Label-based definitions** (optional): containers declare the secrets they
  need with `secret-sync.<name>.item` labels, see
  [Label-based secret definitions](#label-based-secret-definitions).
- **Sync triggers**: polling interval plus an authenticated webhook
  (`POST /sync`) for instant updates.
- Credentials from files (Docker secrets) or environment variables.
- Multi-arch images (amd64/arm64) published to GHCR on every version tag.

## How it works

```
Vaultwarden --(bw CLI: sync + list items)--> secret-sync --> file backend  (Compose)
                                                       \--> swarm backend (Swarm)
                                                       \--> restart / signal containers
```

1. Logs in with a Bitwarden **API key** and unlocks the vault with the master
   password. Session data lives only in memory and a temporary directory.
2. On every run it syncs the vault, resolves each configured secret and
   compares the SHA-256 of the value with the stored one.
3. If different and rotation is enabled, it writes the new value and runs the
   configured action. A secret that did not exist before is just created; no
   consumers are restarted for it.

## Quick start (Compose)

1. In Vaultwarden create a **dedicated user** (ideally in an organization with
   access to only the needed collections) and get its API key
   (*Settings → Security → Keys → View API key*).
2. Put the credentials into files outside of git, e.g. `bootstrap/bw_client_id`,
   `bw_client_secret`, `bw_password`, `sync_webhook_token`. Compose bind-mounts
   these files with their host permissions and ignores `uid`/`gid`/`mode`, while
   the container runs as UID 1000 (`node`). Make them readable for that user:
   `sudo chown 1000:1000 bootstrap/* && chmod 400 bootstrap/*` (or `chmod 444`).
3. Configure `secret-sync` with environment variables (see below), as in
   [examples/compose.yaml](examples/compose.yaml). For secrets that must exist
   at container startup or need the Swarm backend, also copy
   [examples/config.yaml](examples/config.yaml) to `config/config.yaml` and
   adapt it.
4. Start with [examples/compose.yaml](examples/compose.yaml). Consumers mount the
   shared `secrets` volume read-only and wait for `service_healthy`.

Alternatively bind-mount a host directory as `secret_dir` and reference the
files with Compose's top-level `secrets: <name>: file: ./secrets/<name>`. The
file must exist before the consumer starts, so keep the `depends_on` health
condition.

## Sharing the volume across Compose projects

The example sets `name: secrets` on the volume. Without it, Compose names the
volume `<project>_secrets` and other projects cannot reach it. With the fixed
name, `secret-sync` creates the volume, and consumers in any other Compose
project attach to it:

```yaml
services:
  app:
    volumes:
      - secrets:/run/secrets:ro

volumes:
  secrets:
    external: true
```

Start `secret-sync` first (there is no `depends_on` across projects). Consumers
that start before the files exist should use `restart: unless-stopped`.

## Docker socket access

The socket proxy runs unprivileged, so it must be a member of the group that
owns `/var/run/docker.sock` on the host. That group ID differs between systems
(for example `999` or `992`) and cannot be detected from inside Compose. The
example uses `992` by default via `${DOCKER_GID:-992}`. If your host differs,
set `DOCKER_GID` in the environment or in an optional `.env` file next to
`compose.yaml`:

```sh
echo "DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)" > .env
```

Without it the proxy logs `socket not available ... permission denied`. See
[examples/.env.example](examples/.env.example).

## Using secrets as environment variables

Environment variables are fixed when a container is created, so secret-sync
cannot inject them. Prefer the `*_FILE` variables that many images support:

```yaml
environment:
  POSTGRES_PASSWORD_FILE: /run/secrets/db_password
```

For images without `*_FILE` support, read the file in the entrypoint (the image
needs a shell; replace `/original-entrypoint` with the image's own entrypoint).
Rotation restarts the container, so the new value is picked up:

```yaml
entrypoint: ["/bin/sh", "-c", "export DB_PASSWORD=$$(cat /run/secrets/db_password) && exec /original-entrypoint \"$$@\"", "--"]
```

## Swarm

See [examples/compose.swarm.yaml](examples/compose.swarm.yaml). Swarm secrets
cannot be modified, so every new value becomes a new secret named
`<name>_v<hash8>` labeled `secret-sync.name=<name>`. After creating a version,
secret-sync updates all services that referenced an older version (the target
file name inside the container is preserved) and deletes the old versions.
Swarm secrets are always rotated through service updates; `on_change` is not
used for them. With `rotate: false`, the first version is created and later
vault changes are ignored.

## Configuration

See [examples/config.yaml](examples/config.yaml) for a fully commented file.
The config file is **optional**: every general setting can be set with an
environment variable, and environment variables override the file. Only the
`secrets:` list needs the file (or container labels).

| Environment variable | Config key |
|---|---|
| `BW_SERVER` | `bitwarden.server` |
| `BW_CLIENTID_FILE`, `BW_CLIENTSECRET_FILE`, `BW_PASSWORD_FILE` | `bitwarden.*_file` |
| `BW_CLIENTID`, `BW_CLIENTSECRET`, `BW_PASSWORD` | credential values (used when no file is set) |
| `SECRET_SYNC_INTERVAL` | `interval` |
| `SECRET_SYNC_ROTATE` | `rotate` |
| `SECRET_SYNC_SECRET_DIR` | `secret_dir` |
| `SECRET_SYNC_READY_FILE` | `ready_file` |
| `SECRET_SYNC_WEBHOOK_LISTEN`, `SECRET_SYNC_WEBHOOK_TOKEN_FILE` | `webhook.*` |
| `SECRET_SYNC_DISCOVERY_LABELS` | `discovery.labels` |
| `SECRET_SYNC_LOG_LEVEL` | `debug`, `info` (default), `warn`, `error`. Not a config key |
| `SECRET_SYNC_LOG_FORMAT` | `text` (default) or `json`. Not a config key |
| `DOCKER_HOST` | `docker.host` |

| Key | Default | Description |
|---|---|---|
| `bitwarden.server` | – | Vaultwarden URL |
| `bitwarden.client_id_file` / `client_secret_file` / `password_file` | – | Credential files; fall back to `BW_CLIENTID`, `BW_CLIENTSECRET`, `BW_PASSWORD` |
| `interval` | `5m` | Poll interval, minimum `10s` |
| `rotate` | `true` | Default rotation behavior |
| `secret_dir` | `/secrets` | Target directory of the `file` backend |
| `ready_file` | `/tmp/secret-sync.ready` | Created after the first fully successful sync; used by `-healthcheck` |
| `webhook.listen` / `webhook.token_file` | off | Enables `POST /sync` and `GET /healthz` |
| `docker.host` | `$DOCKER_HOST`, else default socket | `unix:///path` or `tcp://host:port` |
| `discovery.labels` | `false` | Read secret definitions from container labels |
| `secrets[].name` | – | Secret name (`[A-Za-z0-9._-]`) |
| `secrets[].source.item` | – | Item name or ID (must match exactly one item) |
| `secrets[].source.folder` / `collection` | – | Narrow the lookup by name |
| `secrets[].source.field` | `password` | `password`, `username`, `notes`, `uri`, `custom:<name>` |
| `secrets[].backend` | `file` | `file` or `swarm` |
| `secrets[].path` | `<name>` | File path below `secret_dir` (`file` backend) |
| `secrets[].mode` | `0444` | Octal file mode. Consumers usually run as a different user, so files are world-readable by default; use `0400` for stricter access |
| `secrets[].rotate` | global | Per-secret override |
| `secrets[].on_change.action` | `none` | `none`, `restart`, `signal` |
| `secrets[].on_change.containers` | – | Container names/IDs to act on, in addition to labeled ones |
| `secrets[].on_change.signal` | `SIGHUP` | Signal for the `signal` action |

### Label-based secret definitions

With `discovery.labels: true`, containers can declare the secrets they need
instead of listing them in `config.yaml`:

```yaml
services:
  app:
    image: your/app
    volumes:
      - secrets:/run/secrets:ro
    labels:
      secret-sync.db_password.item: postgres
      secret-sync.api_token.item: my-api
      secret-sync.api_token.field: custom:token
      secret-sync.api_token.action: signal      # restart (default) | signal | none
```

Shorthand: one label per Vaultwarden item creates one secret per listed field,
named `<item>_<field>`:

```yaml
labels:
  secret-sync.item.test1: user,password   # secrets test1_user (username) and test1_password
  secret-sync.item.myapi: custom:token    # secret myapi_token (custom field "token")
```

Fields: `user`, `password`, `notes`, `uri`, `custom:<name>`. The item name must
only contain `[A-Za-z0-9._-]`; otherwise use the long form. Long-form labels for
a generated name (e.g. `secret-sync.test1_user.mode: "0400"`) refine or override
the shorthand.

Long form: `secret-sync.<name>.<key>`; the name may contain dots, the key is the
last segment. Keys: `item` (required), `folder`, `collection`, `field`, `path`,
`mode`, `rotate`, `action`, `signal`, with the same meaning as in the config file.
Rules:

- The declaring container is restarted (or signaled) on rotation, `action`
  defaults to `restart`. Several containers may declare the same secret if they
  use identical labels; conflicting declarations are rejected.
- Secrets in `config.yaml` win over labels with the same name.
- Labels only create `file` secrets. Use `config.yaml` for the Swarm backend.
- Discovery runs on every sync (interval or webhook) and only sees **running**
  containers. A container that waits for `secret-sync` with
  `depends_on: condition: service_healthy` is not running yet, so its secret does
  not exist on first start. Define secrets that must exist at startup in
  `config.yaml`, and use labels for services that can start without them or
  re-read the file.
- Secrets whose containers stopped are not removed from the secrets directory.
- Only enable this where everyone who can create containers is trusted: any
  container with such labels can request items the service user can read.

### Rotation semantics

| `rotate` | Secret missing | Value changed in vault |
|---|---|---|
| `true` | created | updated, action executed (swarm: services updated) |
| `false` | created | **kept**, a warning is logged |

### Label-based restarts

```yaml
services:
  app:
    labels:
      secret-sync.restart: "db_password,api_token"
```

### Instant updates

Polling is always active. To sync immediately after a change, call the webhook:

```sh
curl -X POST -H "Authorization: Bearer $TOKEN" http://secret-sync:8080/sync
```

You can call it from a Vaultwarden-side script, cron, or an automation tool.
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
go vet ./... && go test ./...
docker build -t secret-sync .
```

Go is not required locally; you can run the tests in a container:

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.23-alpine go test ./...
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
