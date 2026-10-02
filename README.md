# KVN-Bridge-Panel

**English** · [Русский](README.ru.md)

[![CI](https://github.com/sud0-i/KVN-Bridge-Panel/actions/workflows/ci.yml/badge.svg)](https://github.com/sud0-i/KVN-Bridge-Panel/actions/workflows/ci.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)

A panel for running your own VPN on your own servers — for yourself, family and friends. It installs
with one command; everything else happens in the browser: servers are added by IP and root password,
users get a subscription link and connect with the apps they already know.

The core idea is a **cascade**: users connect to a "bridge" (usually in their own country), and
traffic leaves for the internet through an "exit node" abroad. The connection to the bridge looks
like ordinary domestic traffic, and there can be several exit nodes. A single server without a
cascade works too.

```
device ──▶ bridge ──▶ exit node ──▶ internet
                          └──▶ Cloudflare WARP (by rules)
```

## Features

- **Protocols:** VLESS + Reality (primary), XHTTP, Hysteria2, mieru, and connecting through a CDN
  (Cloudflare). Each one is a toggle; users get every variant in a single subscription and switch
  if one of them gets blocked.
- **Cascade and failover:** the bridge spreads traffic across exit nodes and drops the ones that are
  down. If all of them are down, traffic is blocked rather than leaving from the bridge's IP.
- **Nodes installed from the panel:** IP and root password — Xray, the agent, firewall, network
  tuning and WARP are set up automatically. Nodes update the agent, Xray and geo databases themselves.
- **Routing:** local sites direct, selected services through Cloudflare WARP, your own rules.
- **Users:** traffic quotas, expiry dates, a subscription page with instructions and Download / Add
  buttons, import of users together with their old links from another panel.
- **Monitoring:** server load (CPU, memory, disk, network, steal, packet loss), bridge → exit link
  quality, charts for a day and a week.
- **Telegram notifications:** a node went down, the link degraded, a user is running out of traffic
  or subscription time. Works even where Telegram is blocked.
- **Server management:** service restarts and reboots, a terminal right in the browser, key-only SSH
  login on servers.
- **Security:** the panel lives at a secret path, two-factor sign-in, a separate token and key for
  every node, daily backups.
- English and Russian interface, works well on a phone.

## Requirements

- **A server for the panel** running Debian or Ubuntu (x86-64), 1 GB of RAM or more, and a domain
  (or subdomain) pointing to it with an A record. Ports 80 and 443.
- **Servers for nodes** (optional) — also Debian or Ubuntu x86-64 with root SSH access. Rent the
  bridge in your own country and the exit node abroad.
- The panel and a bridge can share a server, and a single server is enough to get started.

## Quick start

1. Point your domain at the server and run the installer:
   ```bash
   curl -fsSL https://raw.githubusercontent.com/sud0-i/KVN-Bridge-Panel/main/install.sh | sudo bash
   ```
   It installs Docker, asks for the domain, the admin password and whether this server should also
   be a bridge, and prints the panel address at the end.
2. Open the panel and turn on two-factor sign-in: Settings → Two-factor sign-in.
3. **Nodes** → Add a node: IP, role (bridge or exit node) and root password. The node shows up in
   2–3 minutes.
4. **Users** → Add. The 🔗 button shows the subscription link and a QR code — send them to the
   person. The link opens a page with apps for their device.

Manually: copy `.env.example` to `.env`, fill it in and run `docker compose up -d`.

### Setups

- **One server.** Answer "yes" to the bridge question — the server is both the panel and the VPN,
  and traffic leaves from its IP. Add an exit node later and the bridge switches to the cascade
  by itself.
- **Panel and bridge on one server, exit nodes elsewhere.** Xray takes port 443 and hands browsers
  and node agents over to the panel, so from outside the server looks like an ordinary website with
  a real certificate.
- **Panel on its own server.** The most robust option: the panel server holds every user, while
  bridge IPs are known to all clients.

## Apps

There is one subscription link; its format is picked for the app automatically.

| App | Subscription format | What it gets |
|---|---|---|
| [Karing](https://karing.app) | sing-box JSON | every variant, including mieru; picks the best server itself |
| [Happ](https://www.happ.su), [Hiddify](https://hiddify.com), v2rayNG, NekoBox and others | list of links | VLESS, XHTTP, Hysteria2, CDN — whatever the app supports |
| [mihomo](https://github.com/MetaCubeX/mihomo) (including on routers), Throne | list of links | the same plus mieru |

The apps shown on the subscription page, the profile name and the Help link are set in the
Subscription page section.

## Protocols

Toggled in the Protocols section; nodes pick up changes within a minute.

| Protocol | Port | When it helps |
|---|---|---|
| **VLESS + Reality** (TCP, XTLS Vision) | TCP 443 | primary: looks like HTTPS to a real website |
| **XHTTP** | TCP 443 | fallback on the same port if the primary gets throttled |
| **Hysteria2** | UDP 443 | fast on lossy links; some networks throttle UDP harder |
| **mieru** | TCP 40100–40109 | no TLS, traffic looks like noise — in case TLS and QUIC get throttled |
| **Through a CDN** | TCP 8443 | when a node's IP is blocked: the connection goes via Cloudflare |

Users, quotas, statistics and routing work the same for every protocol. Nodes installed from the
panel get the required ports opened in ufw automatically.

<details>
<summary><b>More about each protocol</b></summary>

**SNI (Reality camouflage).** Each node poses as a real website, which must open from the node
itself over TLS 1.3 and HTTP/2. The agent checks this; if the site does not fit, the node shows
`SNI ✗` with the reason. SNI is changed with the ✏️ button without a redeploy. The "Find" button next
to the SNI looks for suitable sites among the node's neighbours — in the same /24 at the same hoster,
which makes the camouflage most plausible. The node checks the 254 neighbouring addresses once (at
most 10 connections at a time) and lists sites with TLS 1.3, X25519, HTTP/2 and a real certificate
whose name points to that same address; any of them is applied with one click. To check a candidate
by hand from the node: `curl -so /dev/null --tlsv1.3 --http2 -w '%{http_version}\n' https://example.com`
(should print `2`).

**TLS fingerprint** (Routing) — which browser connections imitate: chrome (default), firefox,
safari, ios, android, edge, random. If connections suddenly stall everywhere at once, try another.

**Hysteria2.** The certificate is self-signed and issued by the node; apps check it by fingerprint,
so no domain is needed and the server cannot be spoofed. **Salamander** makes packets look like
noise but only works in apps that support it (Karing, Hiddify, v2rayNG, mihomo). On a "panel +
bridge" server, open UDP 443 yourself if a firewall is on.

**mieru.** The agent installs the mieru server (`mita`) from the official release, verifying its
checksum, and passes it the panel's users. New users are added without dropping connections.

**Through a CDN.** For each node that should be reachable via Cloudflare:
1. An A record (e.g. `cdn.example.com`) to the node's IP with proxying on (orange cloud).
2. SSL/TLS → **Full** mode (not "Flexible" and not "Full (strict)").
3. The domain goes into the node card, CDN row.

A CDN is slower than a direct connection — it is a fallback. If it does not connect, check the
"Full" mode and turn off Bot Fight Mode. The bridge → exit link can go through the CDN too, for when
the route to the foreign hoster gets throttled.

**Bridge → exit link** (Routing): TCP + Vision by default, or XHTTP, which carries many user
connections over one — sites open faster.
</details>

## Routing

The Routing section:

- **Country** (Russia, Iran, China) — ready-made rules for local sites. Their traffic can go through
  WARP, directly from the bridge, or through the exit node. Even better is when local traffic never
  enters the VPN at all: pick a region in the app for that (e.g. Hiddify → Region).
- **Through WARP** — services that dislike hosting IPs (templates: Google, OpenAI, Netflix,
  Spotify). WARP is installed on exit nodes automatically.
- **Direct from bridges** — what the bridge sends out itself, bypassing exit nodes.
- **Direct links to exit nodes** — connecting without a bridge (lower ping), but users see the exit
  node's IP.

Rule syntax: `geosite:`, `geoip:`, `domain:`, `full:`, `keyword:`, an IP or a subnet.

## Users and subscriptions

Every user has a quota, an expiry date and a subscription link `https://<domain>/sub/<token>`.
Apps show the remaining traffic and expiry themselves. If a link leaks, the ✏️ button issues a new
link or a new key. New users are added to nodes without restarts or dropped connections.

Opened in a browser, the link shows the user their own page: traffic, expiry, apps for their device
with Download and Add buttons (Add puts the subscription into the app) and a QR code.

Servers appear in apps as "KVN <address>"; set a friendlier label such as "🇳🇱 Amsterdam" with the
tag button on the node card.

<details>
<summary><b>Moving from another panel</b></summary>

**📥 Import** on the Users page — one line per person: name, token or the whole old subscription
link, optionally a quota in GB and an expiry date. If any line has an error, nothing is imported.
```
alice 57d2bfa5b82efcc2d127dd64f5dce8c7
bob   https://old.example.com/sub/0a1b2c3d4e5f60718293a4b5c6d7e8f9  50  2026-12-31
```
Old links of the form `https://<old-domain>/sub/<token>` keep working:
1. Install the panel on the new domain and import users.
2. Check the subscription `https://<new-domain>/sub/<old-token>` on your own phone.
3. Add the old domain as a second site: in `/opt/kvn-panel/Caddyfile` replace `{$DOMAIN} {` with
   `{$DOMAIN}, old.example.com {`, run `docker compose restart caddy` and point the old domain's DNS
   at the new server. Do not change `DOMAIN` in `.env`.
</details>

## Monitoring and notifications

- **Servers.** Each node card shows CPU, memory, disk, network, connection count and uptime, plus
  day and week charts. Two metrics tell whose problem it is when everything slows down: **steal**
  (neighbours take the CPU — the hoster overloaded the machine) and **TCP retransmits** (loss in the
  hoster's network).
- **Bridge → exit link.** Once a minute the bridge measures latency and loss to every exit node.
  The charts show at which hours the link sags — ready numbers for a ticket to the hoster.
- **Telegram.** Settings → Telegram notifications: a bot token from @BotFather and your chat ID
  (@userinfobot tells it). The bot reports node problems, failed updates, link degradation, server
  overload and users running out of traffic or time — and separately when things recover. Messages
  are sent by an exit node, so they arrive even if Telegram is blocked where the panel runs.

## Security

- **Hidden panel.** The panel opens at a secret path `https://<domain>/<ADMIN_PATH>/` generated by
  the installer. The home page is a neutral placeholder (or your own site from
  `/opt/kvn-panel/data/site`), everything else answers 404.
- **Two-factor sign-in.** Codes from Google Authenticator, Aegis, 2FAS, Bitwarden and the like, plus
  8 recovery codes. Lost both the phone and the codes:
  `cd /opt/kvn-panel && docker compose exec master ./kvn-master reset-2fa`.
- **Nodes.** Each has its own token (only a hash is stored) and its own Reality key that exists only
  on the node.
- **SSH.** The panel has its own SSH key; nodes add it to `authorized_keys` themselves (in a separate
  block, leaving the rest alone). Add your own keys in Settings → SSH keys. On the node card:
  - **terminal** `>_` — a root console in the browser. Works even if the agent is down. Requires
    two-factor sign-in and a fresh code per session; sessions are logged and reported to Telegram;
  - **Disable password login** — the panel first checks that its key works, then turns passwords off;
  - **redeploy** without a password, using the panel's key.
- **Actions** (⏻ button): restart Xray, mieru or the agent, or reboot the server.

This is not a Tor-grade anonymity tool: the bridge knows which addresses you connect to, and the exit
node knows where the bridge traffic came from. Servers, the database and logs are your responsibility.

## Updates

The panel: `cd /opt/kvn-panel && docker compose pull && docker compose up -d`.

Nodes update themselves, verifying every step — if something is off, the working version stays and
the panel shows why:
- **the agent** follows the panel's version within a minute of a panel update;
- **Xray** — to the version set in Settings → Updates (the "Check latest" button);
- **geo databases** ([Loyalsoldier](https://github.com/Loyalsoldier/v2ray-rules-dat)) — every night.

## Backup and restore

The panel saves a copy of the database to `/opt/kvn-panel/data/backups` every day (keeps 7). Download
a fresh copy with the "💾 Backup" button and keep it off the server, together with
`/opt/kvn-panel/.env`. Node keys are not stored in the database, so nodes keep working after
a restore without a redeploy.

```bash
cd /opt/kvn-panel
docker compose stop master
cp /path/to/kvn-backup-….sqlite data/db.sqlite
docker compose start master
```

## Troubleshooting

- **A node did not appear after install** — the reason is on its card ("Deploy failed: …"). Usual
  suspects: wrong password, SSH closed, not Debian/Ubuntu. The 🔄 button retries.
- **`SNI ✗` on a node** — the camouflage site does not open from it; pick another ("Find" or ✏️).
- **`WARP ✗`** — WARP did not start, "through WARP" traffic leaves from the node's IP. Often temporary.
- **The CDN does not connect** — check SSL mode "Full" and turn off Bot Fight Mode.
- **The two-factor code is rejected** — check the server time (`timedatectl`); the panel says so if
  the clock is off.

## Development

```bash
go test ./...
cd frontend && npm ci && npm run dev   # proxies /api to localhost:8080
```

| Part | What's inside |
|---|---|
| `cmd/master` | API and panel: Go, [Echo](https://echo.labstack.com/), [GORM](https://gorm.io/) + SQLite |
| `frontend` | [Vue 3](https://vuejs.org/), [Vite](https://vite.dev/), [Tailwind CSS](https://tailwindcss.com/) |
| `cmd/agent` | node agent: fetches its config from the panel every minute, builds and checks the Xray config, reports statistics |
| `internal/protocol` | the panel ↔ agent exchange format |
| `ansible/` | installing a node by IP and password |

VPN core — [Xray-core](https://github.com/XTLS/Xray-core), mieru server — [mita](https://github.com/enfein/mieru),
web server — [Caddy](https://caddyserver.com/). CI publishes `ghcr.io/sud0-i/kvn-bridge-panel:latest`
on every push to `main`.

Bugs and ideas — [Issues](https://github.com/sud0-i/KVN-Bridge-Panel/issues).

## License

[GNU AGPL-3.0](LICENSE). You are free to use, modify and redistribute it. If you modify the panel
and let others use it over a network (for example, by selling access), you must publish the source
of your changes under the same license.
