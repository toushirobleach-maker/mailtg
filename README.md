# mailtg

MVP service that polls an IMAP mailbox and forwards messages into Telegram groups.

## Recipient format

Use the mailbox plus address to encode the Telegram destination:

`test+-1001234567890+23@yandex.ru`

- `-1001234567890` is the Telegram group chat ID.
- `23` is an optional Telegram forum topic thread ID.

## What it does

- polls all IMAP folders for unread messages, including Spam/Junk if exposed by the server
- reads the recipient address from `Delivered-To`, fallback to `X-Original-To`, then `To`
- sends the full mail text to Telegram
- if the email contains one image, sends that image and uses the mail text as the caption
- ignores all non-image attachments
- stores processed message IDs in sqlite to avoid duplicates
- marks processed messages as read in IMAP
- retries failed messages up to 5 times, then marks them as read and stops retrying

## Setup

1. Create a Yandex mailbox with plus-addressing enabled.
2. Enable IMAP in the mailbox settings.
3. Create an app password if your account uses 2FA.
4. Copy `.env.example` to `.env`.
5. Fill in `BOT_TOKEN`, `IMAP_USERNAME`, and `IMAP_PASSWORD`.
6. Run the service.

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
  -e DB_PATH=/data/mailtg.db \
  -v "$(pwd)/data:/data" \
  mailtg
```

Notes:

- Create the `data` directory before first run: `mkdir -p data`
- The sqlite database is stored in `./data/mailtg.db`
- No OAuth flow is required anymore; the container only needs IMAP credentials from `.env`
