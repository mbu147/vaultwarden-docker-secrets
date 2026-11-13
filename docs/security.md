# Security notes

`secret-sync` holds the credentials to your vault and can write to your
containers' secrets, so treat it as a high-value component.

## Vaultwarden side

- Create a **dedicated user** for secret-sync. Do not use a personal account.
- Share only the needed items through an **organization collection** with
  read-only access. The tool only ever reads.
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

- The Docker socket is only needed for restart/signal actions and the swarm
  backend, and is only used when configured.
- Access the Docker API through
  [socket-proxy](https://github.com/wollomatic/socket-proxy) and
  grant only what you need (see [examples/compose.yaml](../examples/compose.yaml)).
  Mounting the raw socket is equivalent to root on the host.

## Container hardening

- Run `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges`, non-root user.
- Put the shared secrets volume on tmpfs so values are never written to disk.
- Default file mode is `0400`. If consumers run as a different user, set
  `mode: "0444"` on a tmpfs volume only mounted by the consumers that need it.
- The webhook requires a bearer token. Bind it to an internal network or
  localhost and put TLS in front if it is reachable from outside.

## Reporting vulnerabilities

Please open a private security advisory on the GitHub repository instead of a
public issue.
