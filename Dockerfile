FROM golang:1.23.6-bullseye AS builder

ENV CGO_CFLAGS="-O -D__BLST_PORTABLE__"
ENV CGO_CFLAGS_ALLOW="-O -D__BLST_PORTABLE__"

ENV GOPRIVATE=github.com/MocaFoundation
ENV GOPROXY=https://proxy.golang.org,direct
ENV GONOSUMDB=github.com/MocaFoundation/*
ENV GONOSUMCHECK=github.com/MocaFoundation/*

ARG GITHUB_TOKEN
RUN git config --global url."https://${GITHUB_TOKEN}:@github.com/".insteadOf "https://github.com/"

WORKDIR /workspace

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod tidy && make build


FROM golang:1.23.6-bullseye

WORKDIR /app

RUN apt-get update && apt-get install -y jq mariadb-client

COPY --from=builder /workspace/build/moca-sp /usr/bin/moca-sp

CMD ["moca-sp"]