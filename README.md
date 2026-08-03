# Mekmaa

Server-rendered Go application for Mekmaa, with SQLite persistence, secure session cookies, RBAC, booking operations, and finance tooling.

Target Go version: `1.26.5`

## Features

- bcrypt password hashing
- server-side session storage with SHA-256 token hashes
- 6-digit email verification OTP before first login
- CSRF protection on all POST forms
- role-based middleware for `customer`, `editor`, `coach`, `admin`, and `superadmin`
- admin UI for assigning roles to users
- seeded verified superadmin account

## Routes

- `GET /` public landing page
- `GET|POST /register` registration
- `GET|POST /login` login
- `GET|POST /verify-email` email verification
- `POST /verify-email/resend` resend verification code
- `POST /logout` logout
- `GET /dashboard` authenticated users
- `GET /editor` `editor`, `admin`, or `superadmin`
- `GET /admin` `admin` or `superadmin`
- `POST /admin/users/roles` `admin` or `superadmin`

## Bootstrap behavior

- Self-service registrations receive the `customer` role by default and must verify their email before signing in.
- `coach` is a seeded system role with access to the dashboard and student attendance only.
- A verified seeded superadmin account is created or updated on startup for platform control.
- `superadmin` can do everything `admin` can do.

## Environment variables

Use `.env.example` as the starting point.

Core:

- `APP_ENV`
- `ADDR`
- `DB_PATH`
- `UPLOAD_DIR`
- `COOKIE_SECURE`
- `MEKMAA_PUBLIC_BASE_URL`
- `BOOKING_ACCESS_TOKEN_SECRET`

Communications:

- `BOOKING_EMAIL_ENABLED`
- `SMTP_HOST`
- `SMTP_PORT`
- `SMTP_USER`
- `SMTP_PASS`
- `SMTP_FROM`
- `BOOKING_SMS_ENABLED`
- `SMS_USER_ID`
- `SMS_API_KEY`
- `SMS_SENDER_ID`

## Run

```bash
go run .
```

## Production Deployment

```bash
go build .
```

See `docs/production-deployment.md` for:

- production startup validation
- HTTPS and cookie requirements
- booking pricing readiness
- health and readiness endpoints
- backup and restore procedures
