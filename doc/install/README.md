# Production Installation Guides

This folder contains OS-specific production install guides for the API host:
- `ubuntu.md`
- `debian.md`
- `rocky.md`

PostgreSQL deployment note:

- PostgreSQL Patroni/etcd can be deployed as a single primary node or as a primary with one or more standby nodes.
- `standby_ips` may be empty for a small single-node deployment.

MySQL deployment note:

- MySQL InnoDB Cluster can be deployed as a single primary node or as a primary with one or more secondary nodes.
- This deployment does not use MySQL Router; every node runs a lightweight primary-check HTTP endpoint (`:9200` by default) that HAProxy uses to find the current Group Replication primary.

SSH access note:

- Recommended default is SSH key authentication with a dedicated sudo-capable user.
- Set `CLUSTER_SSH_USER` and `CLUSTER_SSH_PRIVATE_KEY_PATH` in `/etc/erawan-cluster/.env`.
- Place the matching private key on the API host before restarting the service.
- These guides assume the cloud image or template already trusts the corresponding public key on each DB node.

All guides use the same production layout:

```text
/usr/local/bin/erawan-cluster                 # API binary
/opt/erawan-cluster/cluster/                  # Ansible playbooks
/etc/erawan-cluster/.env                      # API config (root-owned, group-readable)
/var/lib/erawan-cluster/cluster/jobs/         # MySQL job state
/var/lib/erawan-cluster/cluster/jobs/pgsql/   # PostgreSQL job state (default)
/var/lib/erawan-cluster/haproxy/tenants/      # Generated HAProxy tenant configs
/etc/systemd/system/erawan-cluster.service    # API systemd unit
```

## Security baseline
1. Run API as non-root (`erawan` user from installer scripts).
2. Set strong `API_KEY` in `/etc/erawan-cluster/.env`.
3. Restrict API network exposure (private subnet or firewall allowlist only).
4. Keep MySQL and SSH credentials out of shell history and Postman exports.
5. Keep HAProxy reload permission minimal:
   - `erawan ALL=(root) NOPASSWD: /bin/systemctl reload haproxy`
6. Keep file permissions strict:
   - `/etc/erawan-cluster/.env` as `0640` and owned by `root:erawan`
   - `/var/lib/erawan-cluster` as `0750`
7. For internet-exposed environments, terminate TLS in front of API.

## HAProxy rollout behavior
- Installers configure HAProxy for hot reload.
- Runtime updates should use `reload` (no restart) to avoid dropping active connections.

## HTTPS / TLS termination

The API's HTTP server never terminates TLS itself (see [security.md](../security.md#network-exposure)).
Keep `API_HOST=127.0.0.1` and put a TLS-terminating reverse proxy in front. Since
HAProxy is already installed by these guides, terminate TLS there rather than adding
a second HTTP daemon (leaner footprint, no extra process to patch on a host that
already runs Ansible jobs and DB TCP-proxying).

### Public domain (Let's Encrypt via certbot)

Requires a DNS A-record pointing a domain at this host's public IP with ports 80/443
reachable from the internet.

1. `sudo apt install -y certbot`
2. Issue the cert without taking HAProxy's `:80` away: run certbot standalone on an
   alternate port and route only the ACME challenge path to it from HAProxy.
   ```bash
   sudo certbot certonly --standalone --http-01-port 8888 -d api.yourdomain.com
   ```
   ```
   frontend erawan_http
       bind *:80
       acl is_acme path_beg /.well-known/acme-challenge/
       use_backend acme_challenge if is_acme
       redirect scheme https code 301 unless is_acme

   backend acme_challenge
       server certbot 127.0.0.1:8888
   ```
3. Combine cert + key into the single PEM HAProxy expects:
   ```bash
   cat /etc/letsencrypt/live/api.yourdomain.com/fullchain.pem \
       /etc/letsencrypt/live/api.yourdomain.com/privkey.pem \
       | sudo tee /etc/haproxy/certs/erawan-api.pem
   sudo chmod 600 /etc/haproxy/certs/erawan-api.pem
   ```
4. Add the HTTPS frontend, pointed at the API's loopback listener:
   ```
   frontend erawan_api_https
       bind *:443 ssl crt /etc/haproxy/certs/erawan-api.pem
       http-response set-header Strict-Transport-Security "max-age=31536000; includeSubDomains"
       default_backend erawan_api

   backend erawan_api
       server api1 127.0.0.1:8080 check
   ```
5. Validate and reload:
   ```bash
   sudo haproxy -c -f /etc/haproxy/haproxy.cfg -f <tenants_dir>
   sudo systemctl reload haproxy
   ```
6. Automate renewal — certbot's package already installs a systemd timer
   (`systemctl list-timers | grep certbot`). Add a deploy-hook so the combined PEM
   and HAProxy reload happen automatically on every renewal:
   ```bash
   sudo tee /etc/letsencrypt/renewal-hooks/deploy/haproxy-reload.sh <<'EOF'
   #!/bin/bash
   cat /etc/letsencrypt/live/api.yourdomain.com/fullchain.pem \
       /etc/letsencrypt/live/api.yourdomain.com/privkey.pem \
       > /etc/haproxy/certs/erawan-api.pem
   chmod 600 /etc/haproxy/certs/erawan-api.pem
   systemctl reload haproxy
   EOF
   sudo chmod +x /etc/letsencrypt/renewal-hooks/deploy/haproxy-reload.sh
   ```

### Private/internal only (no public domain)

Let's Encrypt requires public reachability. Use an internal CA (preferred if more
than one internal client must trust the cert long-term) or a self-signed cert for
test environments:

```bash
openssl req -x509 -nodes -newkey rsa:2048 -days 825 \
  -keyout /etc/haproxy/certs/erawan-api.key \
  -out /etc/haproxy/certs/erawan-api.crt \
  -subj "/CN=<internal-ip-or-hostname>" \
  -addext "subjectAltName=IP:<internal-ip>"
cat /etc/haproxy/certs/erawan-api.crt /etc/haproxy/certs/erawan-api.key \
  | sudo tee /etc/haproxy/certs/erawan-api.pem
sudo chmod 600 /etc/haproxy/certs/erawan-api.pem
```

Add the same HTTPS frontend/backend block as step 4 above. Distribute
`erawan-api.crt` to internal clients that need to trust it; otherwise they must skip
certificate verification, which weakens MITM protection on the private network —
document that tradeoff if you accept it. No Let's-Encrypt-style renewal automation
applies here; track the cert's expiry manually (or shorten `-days` and put a reminder
on the calendar).
