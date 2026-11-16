# Standalone credential proxy deployment

This example builds a local container image from release binaries and runs it with
explicit state and bootstrap mounts. For host-only setup, follow the user guide's
[local quick start](../../docs-site/content/guides/credential-proxy.md#quick-start-local-passkey-administration).
Use **Clients** in the administration UI to provision application/device access.

## Prepare a release image context

Choose a release containing `serve proxy`, built with `CGO_ENABLED=0`. From the
repository root, with GitHub CLI (`gh`), Go, `sha256sum` and `tar` available:

```sh
scripts/prepare-proxy-image.sh vVERSION ./tmp/proxy-image-VERSION
```

The script downloads the Linux amd64/arm64 archives and checksums from the
canonical GitHub release, verifies their hashes, extracts the binaries and checks
for `CGO_ENABLED=0` in Go build settings. Do not substitute an installed dynamically
linked binary. These checksums establish consistency with that release, not an
independent signature or proof of provenance.

Review the Dockerfile's pinned distroless base digest before production use. The
runtime includes CA certificates, has no shell and runs as UID/GID **65532**.
Preparation does not build or publish an image; Compose below builds the local
`term-llm-proxy:local` image. No published proxy image is assumed.

## Provision private bind mounts

In a new private deployment directory, prepare the state directory and bootstrap
file before starting Compose. Adjust paths and ownership for your runtime:

```sh
install -d -m 0700 state secrets
(umask 077; openssl rand -hex 32 > secrets/bootstrap)
sudo chown 65532:65532 state secrets/bootstrap
# Allow the container UID to traverse the parent without listing its contents.
chmod 0711 secrets
cp /path/to/checkout/examples/proxy/compose.yaml .
export PROXY_PUBLIC_URL=https://proxy.example.test
export PROXY_IMAGE_CONTEXT=/absolute/path/to/verified/context
```

The state directory must be writable and the mode-0600 bootstrap file readable by
the runtime UID. With rootless Docker or user-namespace remapping, provision the
**actual mapped host UID**, not necessarily host UID 65532. Verify readability
with the real Compose runtime. Compose secret `uid`/`mode` settings are not a
portable fix for bind ownership; this example uses an explicit read-only file
mount. Do not recursively chown mounts at startup.

## Configure HTTPS and start

Provide an independently managed TLS terminator for the exact, stable HTTPS
origin in `PROXY_PUBLIC_URL`. Compose publishes the HTTP backend only on
`127.0.0.1:8082`; it does not configure DNS, certificates or TLS. See the guide's
[HTTPS and hostname routing](../../docs-site/content/guides/credential-proxy.md#https-termination-and-hostname-routing)
section for a Caddy example.

The terminator must:

- Preserve the exact public **Host** and route all root paths unchanged:
  `/_proxy/`, client routes and `/healthz`. Subpath rewriting is unsupported.
- Keep the backend inaccessible to untrusted clients. `Forwarded` and
  `X-Forwarded-*` headers do not establish trust.
- Disable SSE buffering and align timeouts with `--request-timeout`.

A terminator in another container or on Docker Desktop may need an explicit
private network; its loopback is not necessarily the host's loopback. The proxy
also needs upstream egress: a private network alone does not isolate client egress.

After configuring the terminator:

```sh
docker compose up --build
```

Open `${PROXY_PUBLIC_URL}/_proxy/`, exchange the contents of `secrets/bootstrap`
and enroll a named passkey. **After enrollment, remove the bootstrap command
arguments and file mount from Compose, then securely remove the host file.** Keep
the public URL and `/state` mount for subsequent starts; browser sessions do not
survive restarts. Provision a spare passkey. For host-assisted recovery, see
[passkeys, sessions and recovery](../../docs-site/content/guides/credential-proxy.md#passkeys-sessions-and-recovery).

## Configuration and state

Routes are UI-managed by default and persisted under `/state`. For operator-managed
routes, mount a private YAML file read-only and add `--config /config/proxy.yaml`
to the Compose command, retaining its other arguments. Individually mount any
referenced secret files read-only and make them readable by the runtime UID only.
The UI cannot introduce file references.

Protect all of `/state`, including database WAL/SHM files and backups. Write-only
secrets are **plaintext at rest**, not encrypted vault contents. Never mount proxy
state or real upstream credentials into client/agent containers.

For intentional legacy token administration, replace the passkey bootstrap
arguments/mount with `--auth token --admin-token-file /run/secrets/proxy_admin`
and mount a fresh private admin file read-only at that path. There is no silent
token fallback. For passkeys, `http://localhost[:port]` is the only HTTP development
exception; use that exact Host, not an IP address.

Compose uses a read-only root filesystem, drops capabilities, disables privilege
escalation and bounds temporary storage. Its `serve proxy health` readiness check
contacts only the local store/listener, not an upstream provider. The 30-second
stop grace period allows a 20-second drain plus terminal bookkeeping.

## Contributor browser smoke

Run [`scripts/proxy_passkey_smoke.sh --help`](../../scripts/proxy_passkey_smoke.sh)
for the external-endpoint enrollment/restart/recovery smoke and optional synthetic
client request check. Use disposable fixtures only. The script requires existing
dependencies, Chromium, services and TLS trust; it does not provision them or
disable HTTPS validation. `mock_upstreams.py` supplies synthetic localhost test
servers without contacting a provider.
