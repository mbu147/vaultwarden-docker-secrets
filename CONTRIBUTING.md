# Contributing

Contributions and forks are welcome.

1. Open an issue for larger changes before starting, so we can agree on the approach.
2. Fork the repository and create a branch from `main`.
3. Keep changes focused. Code, comments and documentation are written in English.
4. Before opening a pull request, make sure these pass (also run by CI):

   ```sh
   gofmt -l .        # must print nothing
   go vet ./...
   go test ./...
   ```

   Without a local Go installation:

   ```sh
   docker run --rm -v "$PWD:/src" -w /src golang:1.23-alpine \
     sh -c "gofmt -l . && go vet ./... && go test ./..."
   ```

5. Add or update tests and documentation (README, `examples/`) for behavior changes.
6. Never commit credentials or real secret values.

By contributing you agree that your contribution is licensed under the
[MIT License](LICENSE) of this project.

Security issues: see [docs/security.md](docs/security.md#reporting-vulnerabilities).
