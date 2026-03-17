########## BASE IMAGE ##########
# Setup the base image for dependencies
FROM golang:1.26.1-bookworm AS builder

# Set the working directory
WORKDIR /src

ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOPROXY=https://proxy.golang.org,direct \
    GOMODCACHE=/go/pkg/mod \
    GOCACHE=/root/.cache/go-build

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    git \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

#####################################
##        PRODUCTION IMAGE        ##
#####################################
FROM builder AS prod

COPY . .

ARG MAIN=./cmd/server
ARG BIN=app

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath \
    -ldflags="-w -s" \
    -o /out/${BIN} \
    ${MAIN}

RUN mkdir -p /data

#####################################
##        DEVELOPMENT IMAGE        ##
#####################################
FROM builder AS dev

ARG AIR_VERSION=v1.64.5
ARG SQLC_VERSION=v1.30.0

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go install github.com/air-verse/air@${AIR_VERSION} && \
    go install github.com/sqlc-dev/sqlc/cmd/sqlc@${SQLC_VERSION}

COPY . .

EXPOSE 1323

CMD ["air" , "-c", ".air.toml"]

#####################################
##        PRODUCTION RUNTIME IMAGE ##
#####################################

FROM gcr.io/distroless/base-debian11:nonroot AS runtime

WORKDIR /app

ARG BIN=app
COPY --from=prod /out/${BIN} /app/${BIN}
COPY --from=prod --chown=65532:65532 /data /data

EXPOSE 1323

ENTRYPOINT ["/app/app"]

