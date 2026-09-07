# The DAG-runner service image.
#
# It is the nsctl binary with a different entrypoint, not a second program.
# Node execution lives in internal/runner, and Go scopes internal/ per module,
# so a separate module could not import it -- one binary that can also serve
# costs a hidden subcommand and avoids either duplicating node execution or
# making it public API.
FROM golang:1.25-alpine AS build
WORKDIR /src
# CGO off so the result runs on scratch.
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN go build -ldflags "-X main.version=${VERSION} -s -w" -o /nsctl .

# scratch, not alpine: the runner shells out to nothing. It speaks to the
# Docker daemon over the mounted socket, and every command it "runs" is a
# container the daemon starts, so the image needs no shell, no docker CLI and
# no kubectl.
FROM scratch
# The daemon is addressed over a unix socket, but resolving anything by name --
# hmd_proxy, ms-deployment -- needs the netgo resolver to have somewhere to
# start, and TLS to a registry needs roots.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /nsctl /nsctl
EXPOSE 8080
ENTRYPOINT ["/nsctl", "runner", "serve", "--addr", ":8080"]
