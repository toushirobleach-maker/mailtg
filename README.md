# mailtg

MVP service that polls Gmail and forwards messages into Telegram groups.

## Recipient format

Use the Gmail plus address to encode the Telegram destination:

`test+-1001234567890+23@gmail.com`

- `-1001234567890` is the Telegram group chat ID.
- `23` is an optional Telegram forum topic thread ID.

## What it does

- polls Gmail for unread inbox messages
- reads the recipient address from `Delivered-To`, fallback to `X-Original-To`, then `To`
- sends the full mail text to Telegram
- if the email contains one image, sends that image and uses the mail text as the caption
- ignores all non-image attachments
- stores processed Gmail message IDs in sqlite to avoid duplicates
- marks processed messages as read in Gmail
- retries failed messages up to 5 times, then marks them as read and applies Gmail label `mailtg_failed`

## Setup

1. Create a Google Cloud project and enable Gmail API.
2. Configure the OAuth consent screen.
3. Create an OAuth client of type `Desktop app`.
4. Download the JSON credentials file and save it as `credentials.json`.
5. Copy `.env.example` to `.env` and set `BOT_TOKEN`.
6. Run the service.

## First run

On first startup the service prints a Google authorization URL.
Open it in a browser, grant access, and wait for the local callback to complete.
The app saves the Gmail OAuth token into the sqlite database.

## Run

```bash
go run .
```

## Docker

Build the image:

```bash
docker build -t mailtg .
```

Run the container:

```bash
docker run --rm \
  --env-file .env \
  -e GMAIL_CREDENTIALS_PATH=/app/credentials.json \
  -e DB_PATH=/data/mailtg.db \
  -v "$(pwd)/credentials.json:/app/credentials.json:ro" \
  -v "$(pwd)/data:/data" \
  mailtg
```

Notes:

- Create the `data` directory before first run: `mkdir -p data`
- The sqlite database is stored in `./data/mailtg.db`
- The recommended flow is: do the first OAuth login with `go run .` locally, then start the container and reuse the Gmail token already stored in sqlite
- First-time OAuth inside Docker is inconvenient right now because the app uses a temporary localhost callback port
