# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT

# checkov:skip=CKV_DOCKER_7:No free access to Chainguard versioned labels.
# hadolint global ignore=DL3007

FROM cgr.dev/chainguard/go:latest AS builder

ARG TARGETARCH
ENV CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o /go/bin/newsletter-api -trimpath -ldflags="-w -s" github.com/linuxfoundation/lfx-v2-newsletter-service/cmd/newsletter-api

FROM cgr.dev/chainguard/static:latest

USER nonroot

COPY --from=builder /go/bin/newsletter-api /cmd/newsletter-api

ENTRYPOINT ["/cmd/newsletter-api"]
