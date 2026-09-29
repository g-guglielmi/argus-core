# syntax=docker/dockerfile:1

# ---- Stage 1: build the React frontend ----
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci                 # reproducible install from the committed lockfile
COPY web/ ./
RUN npm run build          # tsc --noEmit && vite build -> /web/dist

# ---- Stage 2: build the Go binary (embeds the frontend) ----
FROM golang:1.27.1-alpine AS build
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
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X 'argus/internal/buildinfo.Version=${ARGUS_VERSION}'" -o /argus .

# ---- Stage 3: minimal runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /argus /argus
ENV ARGUS_LISTEN=":8080" ARGUS_DATA_DIR="/data"
EXPOSE 8080
VOLUME ["/data"]
USER nonroot:nonroot
# Container health for Docker, the Unraid GUI or Dockhand: the binary asks its own /healthz over the
# loopback (healthcheck.go; the image has no shell to run anything else). A slow first start
# (template import, migrations) is covered by the start period.
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 CMD ["/argus", "healthcheck"]
ENTRYPOINT ["/argus"]
