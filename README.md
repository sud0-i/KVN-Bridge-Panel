# KVN-Bridge-Panel

**English** · [Русский](README.ru.md)

A self-hosted panel for a personal VPN on your own servers — for yourself and a few
people close to you. Traffic is encrypted from the device to your servers, so neither
public Wi-Fi nor your ISP can see which sites you open. Entry and exit are split
across different servers.

```
device ──VLESS+Reality──▶ bridge ──VLESS+Reality──▶ exit node ──▶ internet
   └──────────── direct link (optional) ────────────────┘   └─▶ Cloudflare WARP (by rules)
```

- **Bridge** — the entry point that users' devices connect to.
- **Exit node** — the server traffic leaves to the internet from. With several exit
  nodes, the bridge spreads traffic across them and keeps checking that each one actually
  reaches the internet. A node counts as dead only if every check over the last ~2 minutes
  failed, so a single dropped connection doesn't take it out, and a dead one drops out of
  rotation on its own. If all exit
  nodes are down, the bridge blocks traffic instead of sending it out from its own IP. To go back
  to the [single-server](#single-server) mode, delete the exit nodes in the panel.
- **Master** — the control panel: users, servers, routing, subscriptions, statistics.

The panel and the subscription page are available in English and Russian.

## Routing
Configured in the **Routing** section; changes reach the nodes within a minute.

- **Country** (Russia, Iran, China) — fills in ready-made `geosite:`/`geoip:` rules.
  If that country's traffic enters the tunnel, it can go:
  - **through WARP on the exit node** — sites see a Cloudflare IP, not your node's;
  - **directly from the bridge** — sites see the bridge's IP;
  - **through the exit node**, like all other traffic.

  Best of all, local traffic shouldn't enter the VPN at all: users just pick their
  region in the client app (e.g. Hiddify → Region).
- **Through WARP** — your own rules and templates (Google, OpenAI, Netflix, Spotify)
  for services that dislike hosting IPs.
- **Directly from bridges** — what bridges send out themselves, bypassing exit nodes.
- **Direct links to exit nodes** — subscriptions get "KVN direct" servers that skip
  the bridge (lower ping). Users will see exit node IPs.

- **Bridge → exit node link** — TCP + Vision (default) opens a new connection with a Reality
  handshake to the node for every user connection. **XHTTP** carries dozens of streams over one
  connection: sites open faster by roughly two bridge ↔ node round trips, and there is no stream
  of short TLS connections for DPI to notice. Requires the XHTTP setting.
- **TLS fingerprint** — which browser connections imitate (chrome by default; also firefox, safari,
  ios, android, edge, qq, random). It applies to users' subscriptions and to the bridge → exit link.
  If connections start stalling everywhere at once, DPI may have started throttling one fingerprint:
  switch to another one.

Rules: `geosite:`, `geoip:`, `domain:`, `full:`, `keyword:`, an IP or a subnet.

WARP (the official Cloudflare client in proxy mode on `127.0.0.1:40000`) is installed
on exit nodes during deployment, and on a single server if you choose it in the installer. If it
didn't start on a node, "WARP traffic" leaves from the node's own IP, and the panel shows a
`WARP ✗` badge for that node.

### Fallback transport XHTTP
Besides the main connection (VLESS + Reality + XTLS Vision over TCP), every node accepts
XHTTP on **the same port 443**: the connection passes Reality, then Xray hands it to an
internal XHTTP inbound. Subscriptions carry an "XHTTP" variant of every server — if the
main one gets blocked, just switch to it in the client. Turn it off in the Protocols section.

### Hysteria2
A second protocol next to VLESS, turned on with a switch in the **Protocols** section. Every node gets a
Hysteria2 inbound on **UDP 443** (next to TCP 443 Reality) inside the same Xray, so users, quotas,
traffic statistics, the cascade and routing all work the same. Subscriptions get "Hy2" servers
(`hysteria2://` links, and outbounds in the sing-box JSON for Karing).

- The certificate is self-signed and issued by the agent itself for the node's SNI. Apps check it by
  its fingerprint (`pinSHA256`, or the whole certificate for sing-box), so nodes need no domain and
  the server can't be spoofed.
- **Salamander** (optional) makes packets look like random noise instead of HTTP/3. It helps where
  QUIC is blocked, but works only in apps that support it (Karing, Hiddify, v2rayNG, mihomo).
- It runs over UDP: sometimes faster than TCP, but some ISPs throttle UDP harder, so compare both.
- Nodes deployed from the panel open UDP 443 in ufw. On the master + bridge server, open it yourself
  if you run a firewall there.

### mieru
A fallback protocol without TLS: traffic looks like noise, not HTTPS. Useful if TLS and QUIC, which
the other protocols rely on, start getting blocked. Turn it on in the Protocols section.

The agent installs the mieru server (`mita`) from its GitHub release with checksum verification, opens
the ports (TCP 40100–40109 by default; the client hops across them) and keeps the same users as the
panel. New users are added without dropping connections; blocking and removal restart mita. mita's
traffic exits through a local Xray inbound, so the bridge cascade, WARP, routing rules, stats and quotas
work exactly as for the other protocols.

The "… mieru" server appears once the node reports that mita is running: in Karing's JSON and as a
`mierus://` link in the plain subscription, which mihomo (including on routers), Throne and the mieru
client understand. Apps without mieru (v2rayNG, Hiddify etc.) skip that line. Install errors show on
the node's card.

### Via CDN
A fallback for when a node's IP is blocked or the path to it is throttled: connections go through a CDN
(Cloudflare etc.) instead of straight to the node. Turn it on in the Protocols section, then for each node:
1. In Cloudflare, an A record (e.g. `cdn.example.com`) to the node's IP with the orange cloud (proxied).
2. SSL/TLS → **Full** mode. Not "Flexible" (the node expects TLS) and not "Full (strict)" (the node's
   certificate is self-signed).
3. Put the domain on the node's card, CDN row.

The node serves XHTTP over TLS on port **8443** (Cloudflare proxies it; 443 is taken by Reality) and
opens the port in ufw itself. Subscriptions get "… CDN" servers: TLS to the CDN with its real
certificate and XHTTP in `packet-up` mode, which passes through any CDN. For exit nodes the CDN variant
appears even without direct links, since the client never sees the node's IP. The bridge → exit link
can go through the CDN too ("Bridge → exit link" → "XHTTP via CDN"), for when the path from Russia to
foreign hosting gets throttled. Exits without a CDN domain are reached over XHTTP.

The CDN path is slower and has more latency than a direct one — it is a fallback, not the main route.
If it doesn't connect, check that SSL mode is "Full" and turn off Bot Fight Mode for the domain.

### SNI
Reality makes each node mimic a real website (the SNI): the node contacts that site on
every connection, so it must be **reachable from the node itself** over TLS 1.3 and HTTP/2.
The agent checks this on its own — if not, the node shows `SNI ✗` with the reason.
Set the SNI when deploying and change it with the ✏️ button without redeploying;
subscriptions update automatically. To check a candidate by hand, run on the node:
`curl -so /dev/null --tlsv1.3 --http2 -w '%{http_version}\n' https://example.com` (expect `2`).

### What this is not
It is not a Tor-level anonymity tool: the bridge sees which addresses you connect to,
and the exit node sees where the bridge's traffic comes from. The servers, database
and logs are your responsibility — protect access to them.

## Tech stack
| Part | Technologies |
|---|---|
| VPN core | [Xray-core](https://github.com/XTLS/Xray-core): VLESS, Reality, XTLS Vision, XHTTP; geosite/geoip from [Loyalsoldier](https://github.com/Loyalsoldier/v2ray-rules-dat) |
| Master (backend) | Go, [Echo](https://echo.labstack.com/), [GORM](https://gorm.io/) + SQLite, JWT, [go-qrcode](https://github.com/skip2/go-qrcode) |
| Panel (frontend) | [Vue 3](https://vuejs.org/), [Vite](https://vite.dev/), [Tailwind CSS](https://tailwindcss.com/) |
| Node agent | Go, Xray gRPC StatsService for per-user traffic, systemd |
| WARP | Official [Cloudflare WARP](https://developers.cloudflare.com/warp-client/) client in proxy mode |
| Web server and TLS | [Caddy](https://caddyserver.com/): automatic HTTPS, PROXY protocol in the master + bridge mode |
| Deployment | Docker Compose, [Ansible](https://www.ansible.com/) (node deployment by IP and password), bash installer |
| CI | GitHub Actions: `go vet`, tests with `-race`, frontend build, shellcheck, installer test, image published to GHCR |

## Components
- `cmd/master` — API and panel (Echo + SQLite); serves the Vue frontend and `/sub/<token>` subscriptions.
- `cmd/agent` — an agent on every node: once a minute it fetches `/api/sync` from the master,
  builds `config.json` for Xray (`internal/xray`), validates it with `xray run -test`,
  restarts Xray only when something changed, reports traffic statistics and the node's
  state (WARP, config errors).
- `internal/protocol` — the master ↔ agent exchange format, not tied to a specific core.
- `ansible/deploy_node.yml` — automatic node deployment by IP and root password.

Every node gets its own token (only its sha256 is stored in the database) and its own
Reality private key, which exists only on the node itself.

## Installation
```bash
curl -fsSL https://raw.githubusercontent.com/sud0-i/KVN-Bridge-Panel/main/install.sh | sudo bash
```
Point your domain's A record at the server first. The installer asks for the domain,
a password, and whether this same server should also be a bridge.

Or manually: copy `.env.example` to `.env`, fill it in and run `docker compose up -d`.
To deploy nodes, the master must be reachable at `https://$DOMAIN` (or set `MASTER_URL`).

### Hidden panel
The panel lives at a secret path: `https://<domain>/<ADMIN_PATH>/`. The installer generates the path
and prints the address, and it is stored in `ADMIN_PATH` in `/opt/kvn-panel/.env`. At
`https://<domain>` there is a neutral placeholder, and everything else answers with an ordinary 404,
including `/api/login`, unknown subscription tokens and agent requests without a node token. To show
your own site instead of the placeholder, put it (with `index.html`) into `/opt/kvn-panel/data/site`.

For an existing install, add `ADMIN_PATH=<8–64 letters, digits, - or _>` to `.env` and run
`docker compose up -d`. Without `ADMIN_PATH` the panel stays at the site root, as before.

### Master and bridge on one server
Xray takes port 443. Connections that aren't VPN clients (browsers, other nodes' agents,
scanners) are forwarded by Reality to a local Caddy (`127.0.0.1:8443`, see
`Caddyfile.combined`) together with the real IP via PROXY protocol. From the outside the
server looks like an ordinary website with a real certificate, and the panel is served on
the same domain.

This mode is handy to get started. Long-term, keep the master on a separate server: it
holds the database of all users, and the bridge's IP is known to every client.

### Single server
The simplest setup is one server with nothing else: run the installer and answer "yes" to the
bridge question. With no exit nodes, the bridge sends traffic to the internet itself:
```
device ──VLESS+Reality──▶ server (master + bridge) ──▶ internet
```
Everything else works as usual: subscriptions, the user page, XHTTP, quotas, backups and the
hidden panel. The installer also offers Cloudflare WARP. If it's installed, the "through WARP"
rules and the country setting are handled by the server itself; without WARP that traffic leaves
from the server's IP. Add an exit node later in the **Nodes** section: once it comes online, the
bridge switches to the cascade on its own, with no reinstall. Until the node has come online
(deployment in progress or failed), single-server mode keeps working.

### Network tuning
During deployment every node gets BBR, the `fq` queue, larger TCP buffers and MTU probing
(`/etc/sysctl.d/99-kvn-network.conf`). This noticeably speeds up long routes with packet loss.
In containers (OpenVZ, LXC) some settings may not apply, and deployment carries on anyway.

## Users and subscriptions
Each user has a subscription link `https://<domain>/sub/<token>`. The 🔗 button shows it with a QR code
for the phone. The ✏️ button changes the quota, expiry date and IP limit. It also resets traffic and
issues a new link or a new key (for example, if a link leaked). Clients get the profile name, the update
interval (12 h), and the traffic and expiry through the standard `subscription-userinfo` headers.

New users appear on the nodes without restarting Xray: the agent adds them through the Xray API, so
nobody else's connections drop. Blocking, deleting, key changes and settings changes still apply with a
restart (about a second). Otherwise a blocked user's open connections would keep working. Before a
restart the agent collects pending traffic, so no stats are lost.

### Subscription page
Opened in a browser, the link shows the user's personal page: traffic and expiry, apps for their
device (detected automatically) with **Download** and **Add** buttons, a QR code for another device,
and a help link. **Add** is a deeplink: the app opens with the subscription already filled in.
Karing, Happ and Hiddify are there by default. Change the list, the profile name and the help link
in the **Subscription page** section.

VPN apps get the subscription in a format they understand. Karing and the official sing-box apps
get sing-box JSON: the old RIXX panel also served Karing JSON, so migrated profiles keep their
format. The XHTTP variants go only to Karing, because regular sing-box can't do XHTTP. For automatic
server choice in Karing, use its built-in auto-select. All other apps (Happ,
Hiddify, v2rayNG, NekoBox, Throne and others) get the standard base64 list of `vless://` links.
Force a format with `?format=singbox` or `?format=base64`.

Servers are named "KVN <address>". To show something clearer in the apps, such as
"🇳🇱 Amsterdam", set a label with the tag button on the node's card in the **Nodes** section.

### Moving from another panel
Users can keep their old links. Click **📥 Import** and paste one line per user:
```
alice 57d2bfa5b82efcc2d127dd64f5dce8c7
bob   https://old.example.com/sub/0a1b2c3d4e5f60718293a4b5c6d7e8f9  50  2026-12-31
```
Each line holds the name, the token or the whole old link, and optionally a quota in GB and an expiry
date. If any line has an error, nobody is imported.

Old links keep working if they look like `https://<old-domain>/sub/<token>`:
1. Set up the new master on its own domain and import users.
2. Check on your own phone: add `https://<new-domain>/sub/<old-token>` as a subscription.
   This is exactly what users will get after the switch.
3. At the switch, don't touch `DOMAIN`: nodes and clients are tied to it. Just add the old domain as
   a second site: in `/opt/kvn-panel/Caddyfile` change the `{$DOMAIN} {` line to
   `{$DOMAIN}, old.example.com {`, run `docker compose restart caddy`, and point the old domain's DNS
   at the new server. Caddy gets a certificate within a minute. The next time a client updates, it
   gets the new servers.

## Telegram notifications
Settings → Telegram notifications: a bot token from @BotFather and a chat ID (@userinfobot tells you
yours; send /start to your bot first). The bot reports:
- **nodes**: down and back, config not applied, camouflage site or WARP not working;
- **updates**: agent, Xray or geo database update failed;
- **users**: 90% and 100% of traffic, subscription ending in 3 days and ended.

Every minute the master compares the current state with what it already reported: the bot writes about
a problem once, and again when it is fixed. Telegram is blocked from Russian bridges, so the exit node
sends the messages when it syncs and confirms delivery to the master. Unconfirmed messages are retried;
after five attempts a message counts as undelivered. Without exit nodes (single server) the master sends
them itself. The token is never shown back in the panel. "Send test" checks the whole chain and shows
the result. A custom Bot API (a local server or a mirror) is set with `TELEGRAM_API` on the master and
the agents.

## Bridge → exit link quality
Every minute the bridge makes 10 TCP connections to port 443 of each exit node — the same place user
traffic goes. Latency is the reply time; loss is connections with no reply within a second. ICMP
(`ping`) doesn't work for this: it is often dropped or deprioritized along the way.

The Nodes section shows latency and loss charts for the last day (10-minute buckets) and week
(hourly). You can see which hours the link degrades, and the numbers are ready for a ticket to the
hoster. The Overview shows the exit's current latency and loss. When loss over 10 minutes exceeds 10%,
it shows in "Needs attention" and the Telegram bot reports it, and again when it recovers. Measurements
are kept for 8 days.

## Server metrics and control
Agents report server metrics every minute, and the Nodes section shows them on each node's card: CPU,
memory, disk, network, TCP connections and uptime, with day and week charts. Two of them tell whose
problem it is when everything slows down at once:
- **steal** — CPU taken by neighbours on the physical host. High steal means the hoster has
  overloaded the machine;
- **TCP retransmits** — the share of packets sent again. This is network loss as the server sees it.

Telegram alerts fire when the disk is 90% full, memory is 95% used, CPU stays above 90% or steal above
20% for ten minutes, or retransmits exceed 5%. Measurements are kept for 8 days.

The ⏻ button on a node card restarts Xray, mieru or the agent, or reboots the server. The command goes
out on the next sync (within a minute), and the result shows on the card. If the node hasn't checked in
for 5 minutes, the command is cancelled, so a reboot never happens out of the blue an hour later.

## Two-factor sign-in
Settings → Two-factor sign-in: a QR code for Google Authenticator, Aegis, 2FAS, Bitwarden and the like.
The code is computed on the phone from the time, so no internet is needed. When you turn it on, the
panel gives 8 one-time recovery codes in case you lose the phone. If both the phone and the codes are
gone, turn 2FA off on the master server:
```bash
cd /opt/kvn-panel && docker compose exec master ./kvn-master reset-2fa
```
Codes depend on the time, so the server clock must be accurate (`timedatectl`). If it drifts, the panel
says so instead of just "wrong code".

## Terminal and SSH
The master has its own SSH key (`/opt/kvn-panel/data/ssh/id_ed25519`, created on first start).
Agents install its public half into `/root/.ssh/authorized_keys` themselves — in a separate
`# kvn-panel` block; other lines are left alone. Settings → SSH keys lets you add your own keys:
they go to every node.

- **Terminal.** The `>_` button on a node card opens a root console right in the panel:
  browser → WebSocket → master → SSH to the node. It does not depend on the agent, so it works
  even if the agent or Xray are down. Requires two-factor sign-in and a fresh code per session;
  a session closes after 15 minutes without input. Sessions are logged (Settings → SSH keys)
  and, with notifications on, reported to Telegram.
- **Copy and paste.** Selecting text copies it; paste with Ctrl+V, Ctrl+Shift+V or right click.
  On a phone there is a Paste button, Esc, Tab, Ctrl and arrow keys, and an input line with Send.
  Browsers only allow clipboard access over HTTPS.
- **Key-only login.** The SSH row on a node card has "Disable password login". The master first
  checks that its key is accepted; then the agent writes `/etc/ssh/sshd_config.d/00-kvn.conf`
  (`PasswordAuthentication no`), validates it with `sshd -t` and reloads sshd. The same button
  turns passwords back on.
- **Redeploy without a password.** With the master key in place, the password field may be left
  empty when redeploying. A password is only needed after the server OS was reinstalled.
- The node's SSH host key is remembered on first login. If it changes (OS reinstalled), the
  terminal refuses — redeploy the node with a password to reset the remembered key.

## Updates
The master updates via its image: `cd /opt/kvn-panel && docker compose pull && docker compose up -d`.
Nodes update everything else themselves, and each step is verified first. If a check fails, the
working version stays and the node shows "Update failed" with the reason in the panel.

- **Agent.** Nodes compare their binary with the one in the master image. If it differs, the node
  downloads the new one (using its token), checks its SHA-256 and re-executes into it. Nodes catch up
  within a minute of a master update.
- **Xray.** Set the version in the panel (Settings → Updates). "Check latest" asks GitHub. A node
  downloads the release, checks it against the `.dgst` file and tests the current config with the new
  binary (`xray run -test`). Only then does it replace the binary and restart Xray. Leave the field
  empty to not manage the version. Hysteria2 needs Xray 26.3.27 or newer: a node will not install an
  older version while Hysteria2 is on.
- **Geo databases.** Loyalsoldier's geoip.dat and geosite.dat are refreshed nightly, between 3 and
  6 am server time. Databases older than three days are refreshed right away. Checksums are verified,
  and Xray restarts only if the files changed.

The nodes table shows the Xray version, the agent build (yellow means an update is pending) and the
geo date. To turn off agent self-update on a node, set `AGENT_SELF_UPDATE=0` in `/etc/vpn-agent/.env`.

**Once, when moving to this version**, agents cannot update themselves yet. After updating the master:
- bridge on the master server:
  `cd /opt/kvn-panel && docker compose cp master:/app/build/agent_linux_amd64 /usr/local/bin/vpn-agent && systemctl restart vpn-agent`;
- other nodes: the 🔄 button in the panel.

## Backup and restore
The master puts a copy of its database into `/opt/kvn-panel/data/backups` once a day and
keeps the last 7. Download a fresh copy with the "💾 Backup" button in the panel — keep it
somewhere other than the same server.

The database holds users, nodes (token hashes and public keys) and settings. Node private
keys live only on the nodes, so after a restore the nodes keep working without redeploying.
Save `/opt/kvn-panel/.env` (admin password and JWT secret) separately.

Restore:
```bash
cd /opt/kvn-panel
docker compose stop master
cp /path/to/kvn-backup-….sqlite data/db.sqlite
docker compose start master
```

## Image
CI publishes `ghcr.io/sud0-i/kvn-bridge-panel:latest` on every push to `main`.

## Development
```bash
go test ./...
cd frontend && npm ci && npm run dev   # proxies /api to localhost:8080
```

## License
[GNU AGPL-3.0](LICENSE). You are free to use, modify and redistribute it. If you modify the
panel and let others use it over a network (for example, by selling access), you must publish
the source of your changes under the same license.
