ARG GO_VERSION=1.26

# When performing a multi-platform build, leverage Go's built-in support for
# cross-compilation instead of relying on emulation (which is much slower).
# See: https://docs.docker.com/build/building/multi-platform/#cross-compiling-a-go-application
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
ARG TARGETOS
ARG TARGETARCH

# Create /etc/passwd file with single non-root user for scratch image
RUN echo "nobody:x:65534:65534:nobody:/:" > /etc_passwd

WORKDIR /src

# Build static executable
RUN --mount=type=bind,target=. \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} CGO_ENABLED=0 go build -o /bin/ghostgres ./cmd/ghostgres

FROM scratch AS final

USER nobody
EXPOSE 5432

# Copy minimal /etc/passwd file, CA certificates, and binary to final image
COPY --from=build /etc_passwd /etc/passwd
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /bin/ghostgres ghostgres

ENTRYPOINT ["./ghostgres"]
