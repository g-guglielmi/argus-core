# syntax=docker/dockerfile:1

# Base images are pinned by digest as well as by tag: a tag can be re-pointed upstream without any
# change here, a digest can't. Dependabot (.github/dependabot.yml) moves the digests (and the Go
# patch) with a reviewed pull request; the version-gate in CI reads the Go version from this file.
# ---- Stage 1: build the React frontend ----
FROM node:25-alpine@sha256:bdf2cca6fe3dabd014ea60163eca3f0f7015fbd5c7ee1b0e9ccb4ced6eb02ef4 AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts # reproducible install from the committed lockfile; no package runs code in the build
COPY web/ ./
RUN npm run build          # tsc --noEmit && vite build -> /web/dist

# ---- Stage 2: build the Go binary (embeds the frontend) ----
FROM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
WORKDIR /src
# The image's toolchain is the one that builds: a dependency that needs a newer Go fails loudly
# here instead of downloading one at build time. Keep the tag on a supported Go (the two newest
# majors get security fixes) and bump it with patch releases.
ENV GOTOOLCHAIN=local
# running version (git describe), stamped into the binary so the UI can show it + flag updates
ARG ARGUS_VERSION=""
# download pinned modules first (cached layer) from the committed go.mod + go.sum
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# bring in the freshly built assets so go:embed bakes them in
COPY --from=web /web/dist ./web/dist
# build a static binary (readonly module mode: fails if go.mod/go.sum drift from imports)
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X 'argus/internal/buildinfo.Version=${ARGUS_VERSION}'" -o /argus .

# ---- Stage 3: minimal runtime ----
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /argus /argus
# The core's collectors (deploy/core/externalscripts), from the "collectors" build context CI passes
# in (a local build: docker build --build-context collectors=deploy/core/externalscripts argus). The
# core's Zabbix server runs them for the hosts it monitors, but it is a host package, not a
# container: after each core update the argus-updater runs this image once with the host's Zabbix
# ExternalScripts folder bound in and `/argus install-collectors` copies them there (collectors.go).
# The label tells the updater the image carries them.
COPY --from=collectors *.py /collectors/
LABEL io.argus.collectors="/collectors"
ENV ARGUS_LISTEN=":8080" ARGUS_DATA_DIR="/data"
EXPOSE 8080
VOLUME ["/data"]
USER nonroot:nonroot
# Container health for Docker, the Unraid GUI or Dockhand: the binary asks its own /healthz over the
# loopback (healthcheck.go; the image has no shell to run anything else). A slow first start
# (template import, migrations) is covered by the start period.
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 CMD ["/argus", "healthcheck"]
ENTRYPOINT ["/argus"]
