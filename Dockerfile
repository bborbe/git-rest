FROM golang:1.27.3 AS build
COPY . /workspace
WORKDIR /workspace
# No explicit -mod flag on purpose. vendor/ is gitignored, so a bare clone (the
# cluster Job clones the repo and has none) would fail a -mod=vendor build
# outright; with no flag Go uses vendor/ when it is present and the module cache
# otherwise, which keeps the local arm on vendor and lets the cluster arm build.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s" -a -installsuffix cgo -o /main

FROM alpine:3.23
RUN apk --no-cache add \
    ca-certificates \
    git \
    gnupg \
    openssh-client \
    tini \
    tzdata \
    && rm -rf /var/cache/apk/*
ARG BUILD_GIT_VERSION=dev
ARG BUILD_GIT_COMMIT=none
ARG BUILD_DATE=unknown
COPY --from=build /main /main
ENV BUILD_GIT_VERSION=${BUILD_GIT_VERSION}
ENV BUILD_GIT_COMMIT=${BUILD_GIT_COMMIT}
ENV BUILD_DATE=${BUILD_DATE}
ENTRYPOINT ["/sbin/tini", "--", "/main"]
