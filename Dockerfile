FROM golang:1.27-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/secret-sync ./cmd/secret-sync

FROM node:26-alpine
# The Bitwarden CLI is only distributed via npm (or a glibc binary), so the
# runtime image is Node based. Renovate/Dependabot can bump this version.
ARG BW_VERSION=2026.10.1
RUN npm install -g "@bitwarden/cli@${BW_VERSION}" \
    && npm cache clean --force
COPY --from=build /out/secret-sync /usr/local/bin/secret-sync

LABEL org.opencontainers.image.source="https://github.com/mbu147/vaultwarden-docker-secrets" \
      org.opencontainers.image.description="Syncs Vaultwarden/Bitwarden items into Docker secrets"

USER node
ENTRYPOINT ["secret-sync"]
