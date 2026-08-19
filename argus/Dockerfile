# syntax=docker/dockerfile:1

# ---- Stage 1: build the React frontend ----
FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install
COPY web/ ./
RUN npm run build          # outputs to /web/dist

# ---- Stage 2: build the Go binary (embeds the frontend) ----
FROM golang:1.25-alpine AS build
WORKDIR /src
# allow go to fetch a newer toolchain if a dependency requires one (default images pin 'local')
ENV GOTOOLCHAIN=auto
# running version (git describe), stamped into the binary so the UI can show it + flag updates
ARG ARGUS_VERSION=""
COPY go.mod ./
COPY . .
# bring in the freshly built assets so go:embed bakes them in
COPY --from=web /web/dist ./web/dist
# resolve modules (writes go.sum) from actual imports, then build a static binary
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X 'argus/internal/buildinfo.Version=${ARGUS_VERSION}'" -o /argus .

# ---- Stage 3: minimal runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /argus /argus
ENV ARGUS_LISTEN=":8080" ARGUS_DATA_DIR="/data"
EXPOSE 8080
VOLUME ["/data"]
USER nonroot:nonroot
ENTRYPOINT ["/argus"]
