FROM golang:1.22.4-bullseye AS builder

ENV CGO_CFLAGS="-O -D__BLST_PORTABLE__"
ENV CGO_CFLAGS_ALLOW="-O -D__BLST_PORTABLE__"

ENV GOPRIVATE=github.com/mocachain

ARG GITHUB_TOKEN
RUN git config --global url."https://${GITHUB_TOKEN}:@github.com/".insteadOf "https://github.com/"

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN make build


FROM golang:1.22.4-bullseye

WORKDIR /app

RUN apt-get update && apt-get install -y jq mariadb-client

COPY --from=builder /workspace/build/moca-sp /usr/bin/moca-sp

CMD ["moca-sp"]