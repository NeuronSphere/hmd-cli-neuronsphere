# The DAG-runner service image.
#
# Nothing publishes this. `nsctl control-plane start` builds it from sources
# carried inside the binary (internal/bundled, tools/runnersrc) when the runner
# is enabled and the image is absent, so it is reachable from a Homebrew
# install and not only from a working tree. `make image` does the same build
# from the tree.
#
# It is the nsctl binary with a different entrypoint, not a second program.
# Node execution lives in internal/runner, and Go scopes internal/ per module,
# so a separate module could not import it -- one binary that can also serve
# costs a hidden subcommand and avoids either duplicating node execution or
# making it public API.
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
# It also supplies the DNS resolution and CA roots the runner needs to reach
# hmd_proxy and pull images.
FROM docker:cli
COPY --from=build /nsctl /nsctl
EXPOSE 8080
# Split so the same image is also the CI image SPEC013 asks for: run with no
# arguments and it serves, `docker run <image> env list` and it is the CLI.
# The compose service sets no `command:`, so serving stays the default.
ENTRYPOINT ["/nsctl"]
CMD ["runner", "serve", "--addr", ":8080"]
