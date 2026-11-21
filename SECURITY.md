# Security Policy

## Reporting a Vulnerability

If you discover a security issue in NetDisk (authentication bypass, path traversal, XSS in preview/share pages, privilege escalation, etc.), please **do not** open a public issue.

Instead, report it privately via GitHub's [Private Vulnerability Reporting](https://github.com/MiraiNoShiori/netdisk/security/advisories/new) tab on this repository. Include:

- A clear description of the issue and affected endpoint/component
- Steps to reproduce or a minimal proof of concept
- Any suggested mitigation if you have one

## Deployment Hardening Notes

Before exposing NetDisk beyond localhost or a trusted LAN:

1. Replace the default `NETDISK_JWT_SECRET` with a random 32+ byte secret (`openssl rand -hex 32`).
2. Set `NETDISK_ALLOW_REGISTER=false` once initial accounts are created.
3. Terminate TLS at a reverse proxy and set `NETDISK_COOKIE_SECURE=true` (or configure `NETDISK_TRUSTED_PROXY_CIDRS` with `auto`).
4. Keep `NETDISK_ALLOW_GLOBAL_INSTANT_UPLOAD=false` unless cross-account hash deduplication is explicitly desired.
5. Never expose the NFSv3 gateway (`NETDISK_NFS_ADDR`) to the public internet.
