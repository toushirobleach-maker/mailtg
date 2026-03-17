package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mailtg/internal/config"
	"mailtg/internal/imapclient"
	"mailtg/internal/state"
	"mailtg/internal/tgsender"
)

const maxFailures = 5

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := state.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open state db: %v", err)
	}
	defer store.Close()

	sender, err := tgsender.New(cfg.BotToken)
	if err != nil {
		log.Fatalf("init telegram sender: %v", err)
	}

	mailSvc := imapclient.New(cfg)

	ignoredAtStartup, err := snapshotUnreadIDs(ctx, mailSvc)
	if err != nil {
		log.Fatalf("snapshot startup unread messages: %v", err)
	}

	log.Printf("mailtg started, polling every %s, ignoring %d unread messages present at startup", cfg.PollInterval, len(ignoredAtStartup))

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	if err := processOnce(ctx, mailSvc, sender, store, ignoredAtStartup); err != nil {
		log.Printf("initial poll failed: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down")
			return
		case <-ticker.C:
			if err := processOnce(ctx, mailSvc, sender, store, ignoredAtStartup); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("poll failed: %v", err)
			}
		}
	}
}

func processOnce(
	ctx context.Context,
	mailSvc *imapclient.Client,
	sender *tgsender.Sender,
	store *state.Store,
	ignoredAtStartup map[string]struct{},
) error {
	messages, err := mailSvc.ListUnread(ctx)
	if err != nil {
		return err
	}

	for _, msg := range messages {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if _, ignored := ignoredAtStartup[msg.ID]; ignored {
			log.Printf("mail message %s skipped: status=ignored_at_startup", msg.ID)
			continue
		}

		if store.IsFailed(ctx, msg.ID, maxFailures) {
			log.Printf("mail message %s skipped: status=failed_permanently", msg.ID)
			if err := mailSvc.MarkFailed(ctx, msg.ID); err != nil {
				log.Printf("mark previously failed message %s: %v", msg.ID, err)
			}
			continue
		}

		if store.IsProcessed(ctx, msg.ID) {
			log.Printf("mail message %s skipped: status=already_processed", msg.ID)
			if err := mailSvc.MarkRead(ctx, msg.ID); err != nil {
				log.Printf("mark read skipped message %s: %v", msg.ID, err)
			}
			continue
		}

		parsed, err := mailSvc.GetParsedMessage(ctx, msg.ID)
		if err != nil {
			recordFailure(ctx, mailSvc, store, msg.ID, err)
			continue
		}
		log.Printf(
			"mail message %s received: to=%s chat_id=%d thread_id=%d has_photo=%t has_button=%t text=%q",
			msg.ID,
			parsed.Recipient,
			parsed.ChatID,
			parsed.ThreadID,
			parsed.Photo != nil,
			parsed.URL != "",
			singleLine(parsed.Text),
		)
		log.Printf(
			"telegram delivery pending for mail message %s: chat_id=%d thread_id=%d has_photo=%t has_button=%t",
			msg.ID,
			parsed.ChatID,
			parsed.ThreadID,
			parsed.Photo != nil,
			parsed.URL != "",
		)

		result, err := sender.Deliver(parsed)
		if err != nil {
			log.Printf(
				"telegram delivery failed for mail message %s: chat_id=%d thread_id=%d has_photo=%t has_button=%t error=%q text=%q",
				msg.ID,
				parsed.ChatID,
				parsed.ThreadID,
				parsed.Photo != nil,
				parsed.URL != "",
				err.Error(),
				singleLine(parsed.Text),
			)
			recordFailure(ctx, mailSvc, store, msg.ID, err)
			continue
		}
		log.Printf(
			"telegram delivery ok for mail message %s: mode=%s chat_id=%d thread_id=%d has_photo=%t has_button=%t text=%q",
			msg.ID,
			result.Mode,
			parsed.ChatID,
			parsed.ThreadID,
			parsed.Photo != nil,
			parsed.URL != "",
			singleLine(parsed.Text),
		)

		if err := store.ClearFailure(ctx, msg.ID); err != nil {
			log.Printf("clear failures %s: %v", msg.ID, err)
		}
		if err := store.MarkProcessed(ctx, msg.ID); err != nil {
			log.Printf("persist processed %s: %v", msg.ID, err)
		}
		if err := mailSvc.MarkRead(ctx, msg.ID); err != nil {
			log.Printf("mark read %s: %v", msg.ID, err)
		}
	}

	return nil
}

func recordFailure(ctx context.Context, mailSvc *imapclient.Client, store *state.Store, messageID string, reason error) {
	count, err := store.RecordFailure(ctx, messageID, reason.Error())
	if err != nil {
		log.Printf("record failure %s: %v", messageID, err)
		return
	}

	log.Printf("message %s failed attempt %d/%d: %v", messageID, count, maxFailures, reason)
	if count < maxFailures {
		return
	}

	if err := mailSvc.MarkFailed(ctx, messageID); err != nil {
		log.Printf("mark failed message %s: %v", messageID, err)
		return
	}

	if _, lastError, ok, detailErr := store.FailureDetails(ctx, messageID); detailErr != nil {
		log.Printf("load failure details %s: %v", messageID, detailErr)
	} else if ok {
		log.Printf("message %s marked as seen after %d failures; last error: %s", messageID, count, lastError)
	}
}

func singleLine(text string) string {
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	return strings.Join(strings.Fields(text), " ")
}

func snapshotUnreadIDs(ctx context.Context, mailSvc *imapclient.Client) (map[string]struct{}, error) {
	messages, err := mailSvc.ListUnread(ctx)
	if err != nil {
		return nil, err
	}

	ignored := make(map[string]struct{}, len(messages))
	for _, msg := range messages {
		ignored[msg.ID] = struct{}{}
	}
	return ignored, nil
}
