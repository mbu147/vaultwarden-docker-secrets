# vaultwarden-docker-secrets

`secret-sync` is a small container that downloads items from
[Vaultwarden](https://github.com/dani-garcia/vaultwarden) / Bitwarden with the
`bw` CLI and writes them as files into a shared volume that your Docker Compose
containers mount as secrets. It can **rotate** secrets automatically when they
change in the vault and restart the affected containers.

> This is a sync sidecar, not a Kubernetes operator. On Kubernetes use the
> [External Secrets Operator](https://external-secrets.io/) instead.

## Features

- **Opt-in per item**: only items with the custom field `sync_enabled = true`
  are synced. No per-secret configuration, no labels.
- **Directory per item**: `password`, `username`, `notes`, `uri` and every
  custom field become files.
- **Optional groups** (custom field `group`) place items in a subdirectory, so
  a consumer can mount only its own part of the volume.
- **Rotation** (`rotate: true|false`): changed values are rewritten *in place*
  (bind-mounted files stay valid); labeled containers are restarted.
- **Pruning**: files disappear again when `sync_enabled` is removed, the item is
  deleted or its group changes.
- **Sync triggers**: polling interval plus an authenticated webhook
  (`POST /sync`) for instant updates.
- Configuration entirely through environment variables (config file optional);
  credentials from files (Docker secrets) or environment variables.
- Multi-arch images (amd64/arm64) published to GHCR on every version tag.

## How it works

```
Vaultwarden --(bw CLI: sync + list items)--> secret-sync --> /secrets/[<group>/]<item>/<file>
                                                       \--> restart labeled containers on rotation
```

1. Logs in with a Bitwarden **API key** and unlocks the vault with the master
   password. Session data lives only in memory and a temporary directory.
2. On every run it syncs the vault and selects all items with
   `sync_enabled = true`.
3. It compares the SHA-256 of every value with the stored file. A new file is
   created; a changed file is rewritten (if `rotate` is on) and the containers
   labeled for it are restarted. Files that are no longer produced are removed.

## Marking items in Vaultwarden

Add these **custom fields** to an item (type text or boolean):

| Field | Required | Meaning |
|---|---|---|
| `sync_enabled` | yes | `true` (also `1`, `yes`, `on`) opts the item in. Missing or anything else: the item is ignored (and its files are removed). |
| `group` | no | Subdirectory for the item. Missing or empty: no subdirectory. No `/`, `\`, `.` or `..`. |

Resulting layout (item names are sanitized: characters other than
`A-Z a-z 0-9 . _ -` become `_`, leading dots are replaced):

```
/secrets/
  mariadb/                 <- group (optional)
    mariadb-root/          <- item name
      password
      username             <- only if not empty
      notes
      uri                  <- first URI
      <custom field>       <- one file per custom field with a value
  standalone-item/         <- item without group
    password
```

`sync_enabled` and `group` themselves are never written. Empty values are
skipped. Two items that map to the same directory, or a custom field that
collides with another file name, are reported as errors and skipped; pruning is
suspended until the conflict is resolved.

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
4. Add `sync_enabled = true` (and optionally `group`) to your items.
5. Mount the volume in the consumer and wait for `service_healthy`:

```yaml
services:
  mariadb:
    image: mariadb:11
    depends_on:
      secret-sync:
        condition: service_healthy
    volumes:
      - type: volume
        source: secrets
        target: /run/secrets
        read_only: true
        volume:
          subpath: mariadb         # the item's group
    environment:
      MARIADB_ROOT_PASSWORD_FILE: /run/secrets/mariadb-root/password
    labels:
      secret-sync.restart: "mariadb"
```

`subpath` needs Compose >= 2.26 and Docker >= 26. Without a group (or without
`subpath`) the files are at `/run/secrets/<item>/<file>`, resp.
`/run/secrets/<group>/<item>/<file>`.

## Restricting secrets to a group

A shared volume exposes every file to every container that mounts it. Mounting
`subpath: <group>` shows a consumer only its group. Several containers may share
a group. Limits: this separates files, it is not a hard barrier. A container that
mounts the whole volume, or anyone who can create containers or reach the Docker
socket, can still read everything.

## Sharing the volume across Compose projects

The example sets `name: secrets` on the volume. Without it, Compose names the
volume `<project>_secrets` and other projects cannot reach it. With the fixed
name, `secret-sync` creates the volume, and consumers in any other Compose
project attach to it:

```yaml
volumes:
  secrets:
    external: true
```

The top-level `volumes:` declaration is required in every consuming project,
also when mounting with `subpath`; otherwise Compose fails with `refers to
undefined volume secrets`. Start `secret-sync` first (there is no `depends_on`
across projects); consumers that start earlier should use
`restart: unless-stopped`.

## Rotation and restarts

| `rotate` | File missing | Value changed in vault |
|---|---|---|
| `true` (default) | created | rewritten, labeled containers restarted |
| `false` | created | **kept**, a warning is logged |

A newly created file never triggers a restart. Containers opt in with the label
`secret-sync.restart`, a comma-separated list of targets:

- `<group>`: any item of that group changed
- `<group>/<item>`: only that item changed (sanitized item name)
- `<item>`: an item without group changed

```yaml
labels:
  secret-sync.restart: "mariadb, redis/cache"
```

Restarting needs Docker API access (`DOCKER_HOST` or the mounted socket), see
below. Without it, rotations are still written and a warning is logged.

## Pruning stale files

With `prune: true` (default; `SECRET_SYNC_PRUNE=false` disables it) secret-sync
keeps a list of the files it wrote (`.secret-sync-managed` in `secret_dir`) and
removes those that a run no longer produces, plus emptied directories. This
happens when

- `sync_enabled` is removed or set to something other than true,
- the item was deleted in Vaultwarden (or the service user lost access),
- the item moved to another `group` or was renamed,
- a custom field was removed or emptied.

Files it did not write are never touched. Nothing is removed when the vault
fetch fails or when the run reports any error. Consumers keep an open file until
restarted, but new reads fail once it is gone. Files left over from older
versions are not in the manifest; delete them by hand (for example by
recreating the volume: `docker compose down && docker volume rm secrets`).

## Docker socket access

Only needed for restarts. Use a socket proxy as in the example. The proxy runs
unprivileged, so it must be a member of the group that owns
`/var/run/docker.sock` on the host (for example `999` or `992`). The example uses
`992` by default via `${DOCKER_GID:-992}`. Override it in the environment or in
an optional `.env` file next to `compose.yaml`:

```sh
echo "DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)" > .env
```

Without it the proxy logs `socket not available ... permission denied`. See
[examples/.env.example](examples/.env.example).

## Using secrets as environment variables

Environment variables are fixed when a container is created, so secret-sync
cannot inject them. Prefer the `*_FILE` variables that many images support. For
images without `*_FILE` support, read the file in the entrypoint (the image
needs a shell; replace `/original-entrypoint` with the image's own entrypoint).
Rotation restarts the container, so the new value is picked up:

```yaml
entrypoint: ["/bin/sh", "-c", "export DB_PASSWORD=$$(cat /run/secrets/app/password) && exec /original-entrypoint \"$$@\"", "--"]
```

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
| `SECRET_SYNC_ROTATE` | `rotate` | `true` | Update existing files on change |
| `SECRET_SYNC_PRUNE` | `prune` | `true` | Remove stale files |
| `SECRET_SYNC_SECRET_DIR` | `secret_dir` | `/secrets` | Output directory |
| `SECRET_SYNC_FILE_MODE` | `file_mode` | `0444` | Octal file mode |
| `SECRET_SYNC_READY_FILE` | `ready_file` | `/tmp/secret-sync.ready` | Written after the first fully successful sync |
| `SECRET_SYNC_WEBHOOK_LISTEN` | `webhook.listen` | off | e.g. `:8080` |
| `SECRET_SYNC_WEBHOOK_TOKEN_FILE` | `webhook.token_file` | – | Required with the webhook |
| `DOCKER_HOST` | `docker.host` | default socket | `tcp://host:port` or `unix:///path` |
| `SECRET_SYNC_LOG_LEVEL` | – | `info` | `debug`, `info`, `warn`, `error` |
| `SECRET_SYNC_LOG_FORMAT` | – | `text` | `text` or `json` |

Secret values are never logged; only file names and the first 8 characters of
the hash.

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
