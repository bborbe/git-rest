FROM golang:1.26.2 AS build
COPY . /workspace
WORKDIR /workspace
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -mod=vendor -ldflags "-s" -a -installsuffix cgo -o /main

FROM alpine:3.23
RUN apk --no-cache add \
    ca-certificates \
    git \
    gnupg \
    openssh-client \
    tzdata \
    && rm -rf /var/cache/apk/*
COPY --from=build /main /main
ENTRYPOINT ["/main"]
