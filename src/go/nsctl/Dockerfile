# The nsctl service image: the local identity provider (authd) runs from it.
#
# Nothing publishes this. `nsctl control-plane start` builds it from sources
# carried inside the binary (internal/bundled, tools/nsctlsrc) when the
# identity provider is enabled and the image is absent, so it is reachable from
# a Homebrew install and not only from a working tree. `make image` does the
# same build from the tree.
#
# It is the nsctl binary with a different entrypoint, not a second program.
FROM golang:1.25-alpine AS build
WORKDIR /src
# CGO off so the binary carries no libc dependency of its own.
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN go build -ldflags "-X main.version=${VERSION} -s -w" -o /nsctl .

# docker:cli, not scratch. internal/container drives Docker by shelling out to
# the CLI rather than through the Engine API, so the binary has to be on PATH:
# on scratch every deploy node dies with `exec: "docker": executable file not
# found in $PATH`, which is a runtime failure no unit test sees, because the
# Docker seam is an interface every test fakes.
#
# It also supplies the DNS resolution and CA roots the service needs to reach
# hmd_proxy and pull images.
FROM docker:cli
COPY --from=build /nsctl /nsctl
EXPOSE 8080
# Split so the same image is also the CI image SPEC013 asks for: run with no
# arguments and it serves, `docker run <image> env list` and it is the CLI.
# The compose service sets no `command:`, so serving stays the default.
ENTRYPOINT ["/nsctl"]
CMD ["authd", "serve", "--addr", ":8080"]
