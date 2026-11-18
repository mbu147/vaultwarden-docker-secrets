# Security notes

`secret-sync` holds the credentials to your vault and can write secrets into
your Compose projects and Arcane, so treat it as a high-value component.

## Vaultwarden side

- Create a **dedicated user** for secret-sync. Do not use a personal account.
- Only items with the custom field `sync_enabled = true` are synced, so the
  service user may see more items than are exposed. Still share only the needed
  items through an **organization collection** with read-only access. The tool
  only ever reads.
- Protect the master password and API key like any other root credential.

## Credentials

- Pass credentials as files (Docker secrets) via `*_file` options. Environment
  variables (`BW_CLIENTID`, `BW_CLIENTSECRET`, `BW_PASSWORD`) are supported but
  are visible through `docker inspect`.
- Credentials are passed to the `bw` subprocess through its environment only
  and are never logged. Secret values are never logged; only the name and the
  first 8 characters of the SHA-256 hash appear.
- The `bw` data directory is a temporary directory (put `/tmp` on tmpfs) and the
  session is logged out on shutdown.

## Docker access

- The Docker socket is only used to list containers and read their Compose
  project directory. Containers are never started, stopped or restarted.
- Access the Docker API through
  [socket-proxy](https://github.com/wollomatic/socket-proxy) and
  grant only what you need (see [examples/compose.yaml](../examples/compose.yaml)).
  Mounting the raw socket is equivalent to root on the host.
- The env file feature and the label `secret-sync.env` need container listing
  including stopped containers (`GET /containers/json?all=1`), which the
  example proxy rule already permits.

## Env files and Arcane

- The env file feature needs a **read-write** mount of the directory with all
  Compose projects. A compromised secret-sync could then modify project files,
  so only writes below `env_file.dir` are made, to one fixed file name, never
  through symlinks. The directory taken from the container label is validated;
  anyone who can create containers can still cause that one file to be written
  in any project below `env_file.dir`.
- `.secrets` holds plaintext values (mode `0600`) and environment variables are
  visible via `docker inspect`. Prefer `*_FILE` secrets where images support it.
- Arcane variables flagged secret are encrypted in Arcane's database, but Arcane
  writes them in plaintext to `.env.global` on its agents. The Arcane API key can
  manage all variables; store it as a Docker secret, not as an environment
  variable. Variables are never deleted by secret-sync.

## Container hardening

- Run `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges`, non-root user.
- The webhook requires a bearer token. Bind it to an internal network or
  localhost and put TLS in front if it is reachable from outside.

## Reporting vulnerabilities

Please open a private security advisory on the GitHub repository instead of a
public issue.
