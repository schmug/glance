# Glance + Cloudflare Tunnel (Docker Compose)

One-command deployment of Glance behind a Cloudflare Tunnel with Cloudflare Access in front of `/admin`.

## Prerequisites

- Docker + Docker Compose
- A Cloudflare account with a zone you control
- A Cloudflare Tunnel created in Zero Trust → Networks → Tunnels (save the install token)
- A Cloudflare Access application protecting your hostname (save the AUD tag)

## First-time setup

1. Copy env template:
   ```
   cp .env.example .env
   ```
   Paste your tunnel token into `TUNNEL_TOKEN`.

2. Build the image (required for the `secret:make` / `password:hash` helpers below):
   ```
   docker compose build
   ```

3. Generate a session secret and a password hash, paste both into `config/glance.yml`:
   ```
   docker compose run --rm glance /app/glance secret:make
   docker compose run --rm glance /app/glance password:hash 'your-password-here'
   ```

4. Edit `config/glance.yml`:
   - `auth.secret-key` ← generated secret
   - `auth.users.admin.password-hash` ← generated hash
   - `admin.cloudflare-access.team-domain` ← `YOURTEAM.cloudflareaccess.com`
   - `admin.cloudflare-access.audience` ← AUD tag from the Access app
   - `admin.cloudflare-access.allowed-emails` ← your email(s)

5. In the Cloudflare Tunnel dashboard, add a public hostname routing your domain to `http://glance:8080`.

6. Bring it up:
   ```
   docker compose up -d
   docker compose logs -f
   ```

Visit your hostname → you'll be gated by Access, then Glance session login, then land on the dashboard. `/admin` works the same.

## Local bring-up without Cloudflare

For first-run smoke tests before the tunnel is wired up:

1. In `docker-compose.yml`, uncomment the `ports: 8080:8080` mapping on the `glance` service.
2. In `docker-compose.yml`, uncomment `GLANCE_ADMIN_DEV_BYPASS: "1"` to skip CF Access verification (session login still required).
3. Remove or comment out the `cloudflared` service (or `docker compose up glance`).
4. Browse `http://localhost:8080`.

Re-enable both before exposing publicly.

## Persistence

- `./config/glance.yml` is the live, editable config. The `/admin` UI writes here.
- `./config/.glance-history/` is a local-only git repo of every config change. Do not commit.

## Updating

```
docker compose pull cloudflared
docker compose build glance
docker compose up -d
```
