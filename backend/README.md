# Mid Station Backend

Go + Gin backend for the personal upstream monitor described in `../docs`.

## Run

```powershell
cd backend
go mod tidy
go run ./cmd/server -hash-password "your-password"
Copy-Item config.example.yaml config.yaml
```

Put the generated bcrypt hash, PostgreSQL DSN, session secret, and stable 32-byte encryption key in `config.yaml`, then run:

```powershell
go run ./cmd/server -config config.yaml
```

The server creates the PostgreSQL tables on startup.

## Environment Overrides

- `MID_SERVER_ADDR`
- `MID_SESSION_SECRET`
- `MID_ENCRYPTION_KEY`
- `MID_ADMIN_USERNAME`
- `MID_ADMIN_PASSWORD_HASH`
- `MID_DATABASE_DSN`
- `MID_POLLER_INTERVAL_SECONDS`
