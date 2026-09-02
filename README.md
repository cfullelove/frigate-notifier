# Frigate notifier

A fail-closed MQTT service for Frigate review events. It caches a Home Assistant alarm state over WebSocket, applies ordered rules, asks Gemini to inspect a Frigate snapshot, and sends a Telegram photo followed by one clip when the review ends.

## Configure and run

Copy `config.example.yml` to `config.yml`, set the referenced environment variables, then build/run:

```sh
docker build -t frigate-notifier .
docker run --rm -v "$PWD/config.yml:/app/config.yml:ro" --env-file .env frigate-notifier -config /app/config.yml
```

Or start it with Docker Compose after creating `config.yml` and `.env`:

```sh
docker compose up -d --build
docker compose logs -f frigate-notifier
```

Stop it with `docker compose down`.

The image is non-root and uses a writable temporary directory for clips. The MQTT broker must be reachable using the configured URI. Home Assistant needs a long-lived access token (Profile -> Security -> Long-Lived Access Tokens), and its URL is converted to `/api/websocket`. Create a Telegram bot with BotFather and obtain the chat ID through the Bot API. Create a Gemini API key in Google AI Studio and choose a supported configured model (the supplied example uses `gemini-2.5-flash`).

Rules are ordered, exact, and first-match-wins; conditions in a rule are ANDed. The supplied configuration allows `armed_away`, `armed_night`, and `armed_home` only for `Car Port`. Missing, unavailable, unknown, or stale alarm state always denies.

Frigate snapshot and clip endpoints use the first linked event; camera falls back to `cameras[0]`. Downloads are bounded to 50 MiB, retried only for transient HTTP failures, and clips are securely temporary files. Telegram captions are Unicode-safely limited to 1024 code points. State is in-memory only, so restarts intentionally lose correlation.

## Development

Use Docker for all Go commands:

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.23 go test ./...
docker run --rm -v "$PWD:/src" -w /src golang:1.23 go test -race ./...
docker run --rm -v "$PWD:/src" -w /src golang:1.23 go vet ./...
```

Secrets are never logged. Do not commit `.env` or `config.yml`.
