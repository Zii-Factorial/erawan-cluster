# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/erawan-cluster ./cmd/api

# ---- runtime stage ----
# Embeds the Go API + HAProxy + Ansible in one image so a single pod runs the
# whole proxy node (API control plane + SQL data plane), per doc/ha-architecture.md.
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        haproxy \
        ansible \
        openssh-client \
        ca-certificates \
        tini \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 10001 erawan \
    && useradd -u 10001 -g 10001 -M -s /usr/sbin/nologin erawan

# Run as a non-root user. HOME and the Ansible temp dirs point at /tmp so the
# container works with a read-only root filesystem (writable dirs are mounted in k8s).
ENV HOME=/tmp \
    ANSIBLE_LOCAL_TEMP=/tmp/.ansible/tmp \
    ANSIBLE_SSH_CONTROL_PATH_DIR=/tmp/.ansible/cp

# Application binary + assets the API serves / runs at runtime.
COPY --from=build /out/erawan-cluster /usr/local/bin/erawan-cluster
COPY cluster /app/cluster
COPY index.html /app/index.html
COPY deploy/docker/haproxy.cfg /etc/haproxy/haproxy.cfg
COPY deploy/docker/haproxy-reload.sh /usr/local/bin/haproxy-reload.sh
COPY deploy/docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/haproxy-reload.sh /usr/local/bin/entrypoint.sh

WORKDIR /app
EXPOSE 8080
USER 10001
# tini reaps the backgrounded HAProxy child cleanly.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
