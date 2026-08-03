# Mekmaa Production Deployment

## Go Version

Use Go `1.26.5`, matching `go.mod`.

## Build

```bash
go build .
```

## Required Environment Variables

Start from [.env.example](/home/janon-emersion-t/Projects/mekmaa%20web/.env.example).

Core:

- `APP_ENV=production`
- `ADDR=:8080`
- `DB_PATH=/srv/mekmaa/data/mekmaa.db`
- `UPLOAD_DIR=/srv/mekmaa/uploads`
- `COOKIE_SECURE=true`
- `MEKMAA_PUBLIC_BASE_URL=https://mekmaa.com`
- `BOOKING_ACCESS_TOKEN_SECRET=<long-random-secret>`
- `BOOKING_ACCESS_TOKEN_TTL_DAYS=180`

Booking communications:

- `BOOKING_EMAIL_ENABLED=true|false`
- `SMTP_HOST`
- `SMTP_PORT`
- `SMTP_USER`
- `SMTP_PASS`
- `SMTP_FROM`
- `BOOKING_SMS_ENABLED=true|false`
- `SMS_USER_ID`
- `SMS_API_KEY`
- `SMS_SENDER_ID`

Public venue details:

- `MEKMAA_CONTACT_PHONE`
- `MEKMAA_CONTACT_EMAIL`
- `MEKMAA_VENUE_NAME`
- `MEKMAA_VENUE_ADDRESS`

## Production Validation

On startup, production mode rejects unsafe configuration such as:

- missing or weak `BOOKING_ACCESS_TOKEN_SECRET`
- development booking token secret
- non-HTTPS or localhost `MEKMAA_PUBLIC_BASE_URL`
- `COOKIE_SECURE=false`
- missing `DB_PATH`
- production DB paths using memory or temporary locations
- production upload paths using temporary locations

The app also checks:

- database connectivity and writability
- upload directory writability
- SQLite foreign keys
- required booking communication credentials when a channel is enabled

## Booking Pricing Before Launch

Mekmaa does not auto-fill commercial prices.

Before exposing `/book` publicly:

1. Open `/admin/pricing`
2. Configure positive weekday/weekend and peak/off-peak prices for every active public booking option
3. Confirm the dashboard no longer shows the pricing setup warning

If pricing is incomplete:

- startup logs a warning listing unpriced active booking options
- staff see a setup warning on the dashboard and pricing page
- customers cannot submit those unpriced options

## Database Location

Recommended:

```text
/srv/mekmaa/data/mekmaa.db
```

Avoid:

- temporary directories
- in-memory SQLite paths
- locations managed by OS cleanup jobs

Migrations run automatically at startup.

## Upload Location

Recommended:

```text
/srv/mekmaa/uploads
```

This path must persist across restarts and deployments.

## Reverse Proxy Expectations

Place Mekmaa behind an HTTPS reverse proxy such as Nginx or Caddy.

Requirements:

- terminate TLS at the proxy
- forward traffic to the Mekmaa process
- keep the public origin aligned with `MEKMAA_PUBLIC_BASE_URL`
- expose `/health` and `/ready` to the load balancer or supervisor as needed

## HTTPS And Cookies

Production requires:

- `MEKMAA_PUBLIC_BASE_URL` with `https://`
- `COOKIE_SECURE=true`

Session and CSRF cookies keep their existing `HttpOnly` and `SameSite=Lax` protections.

## Booking Token Secret Generation

Generate a strong secret, for example:

```bash
openssl rand -base64 48
```

Do not reuse the development default.

## SMTP Configuration

If `BOOKING_EMAIL_ENABLED=true`, all of the following must be valid:

- `SMTP_HOST`
- `SMTP_PORT`
- `SMTP_USER`
- `SMTP_PASS`
- `SMTP_FROM`

If email delivery is disabled, those credentials are not required for readiness.

## SMS Configuration

If `BOOKING_SMS_ENABLED=true`, all of the following must be valid:

- `SMS_USER_ID`
- `SMS_API_KEY`
- `SMS_SENDER_ID`

If SMS delivery is disabled, those credentials are not required for readiness.

## Health Endpoints

- `GET /health`
  - liveness only
  - returns `200` when the process is up
- `GET /ready`
  - checks config, database, uploads, migrations, and pricing-readiness state
  - returns `200` when ready
  - returns `503` when not ready

Neither endpoint exposes secrets.

## Backups

Create backups with:

```bash
scripts/backup.sh
```

Validate a backup with:

```bash
scripts/restore-check.sh /path/to/backup-directory
```

The backup includes:

- SQLite database
- uploaded files

## Restore Procedure

1. Stop Mekmaa
2. Copy the verified backup database into the production data path
3. Restore uploads into the production upload path
4. Start Mekmaa
5. Check `/ready`
6. Run the smoke tests below

Do not overwrite the live database before the backup passes `scripts/restore-check.sh`.

## Recommended Restart Steps

1. Deploy the new binary
2. Confirm environment variables are present
3. Restart the service
4. Watch startup logs for validation failures or pricing warnings
5. Check `/health`
6. Check `/ready`

## Post-Deployment Smoke Tests

1. Open `/login`
2. Open `/dashboard`
3. Open `/admin/pricing`
4. Open `/book`
5. Submit one safe test booking for a priced activity
6. Open the generated booking status link
7. Confirm `/ready` returns HTTP `200`
