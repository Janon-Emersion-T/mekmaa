# mekmaa3 auth system

Server-rendered Go authentication starter with SQLite persistence, secure session cookies, and RBAC.

Target Go version: `1.26.5`

## Features

- bcrypt password hashing
- server-side session storage with SHA-256 token hashes
- 6-digit email verification OTP before first login
- CSRF protection on all POST forms
- role-based middleware for `customer`, `editor`, `admin`, and `superadmin`
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
- A verified seeded superadmin account is created or updated on startup for platform control.
- `superadmin` can do everything `admin` can do.

## Environment variables

- `ADDR` server bind address, default `:8080`
- `DB_PATH` SQLite database path, default `app.db`
- `COOKIE_SECURE` set to `true` behind HTTPS
- `SMTP_HOST` SMTP host, default `smtp.gmail.com`
- `SMTP_PORT` SMTP port, default `587`
- `SMTP_USER` SMTP username
- `SMTP_PASS` SMTP password or Gmail app password
- `SMTP_FROM` optional sender address, defaults to `SMTP_USER`

## Run

```bash
go run .
```

## Gmail SMTP example

```bash
export SMTP_HOST=smtp.gmail.com
export SMTP_PORT=587
export SMTP_USER='janonemersion2016@gmail.com'
export SMTP_PASS='your-gmail-app-password'
export SMTP_FROM='janonemersion2016@gmail.com'
go run .
```
