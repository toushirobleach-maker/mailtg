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
	"mailtg/internal/gmailclient"
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

	gmailSvc, err := gmailclient.New(ctx, cfg, store)
	if err != nil {
		log.Fatalf("init gmail client: %v", err)
	}

	ignoredAtStartup, err := snapshotUnreadIDs(ctx, gmailSvc)
	if err != nil {
		log.Fatalf("snapshot startup unread messages: %v", err)
	}

	log.Printf("mailtg started, polling every %s, ignoring %d unread messages present at startup", cfg.PollInterval, len(ignoredAtStartup))

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	if err := processOnce(ctx, gmailSvc, sender, store, ignoredAtStartup); err != nil {
		log.Printf("initial poll failed: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down")
			return
		case <-ticker.C:
			if err := processOnce(ctx, gmailSvc, sender, store, ignoredAtStartup); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("poll failed: %v", err)
			}
		}
	}
}

func processOnce(
	ctx context.Context,
	gm *gmailclient.Client,
	sender *tgsender.Sender,
	store *state.Store,
	ignoredAtStartup map[string]struct{},
) error {
	messages, err := gm.ListUnread(ctx)
	if err != nil {
		return err
	}

	for _, msg := range messages {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if _, ignored := ignoredAtStartup[msg.Id]; ignored {
			continue
		}

		if store.IsFailed(ctx, msg.Id, maxFailures) {
			if err := gm.MarkFailed(ctx, msg.Id); err != nil {
				log.Printf("mark previously failed message %s: %v", msg.Id, err)
			}
			continue
		}

		if store.IsProcessed(ctx, msg.Id) {
			if err := gm.MarkRead(ctx, msg.Id); err != nil {
				log.Printf("mark read skipped message %s: %v", msg.Id, err)
			}
			continue
		}

		parsed, err := gm.GetParsedMessage(ctx, msg.Id)
		if err != nil {
			recordFailure(ctx, gm, store, msg.Id, err)
			continue
		}
		log.Printf(
			"received gmail message %s to %s -> chat_id=%d thread_id=%d has_photo=%t text=%q",
			msg.Id,
			parsed.Recipient,
			parsed.ChatID,
			parsed.ThreadID,
			parsed.Photo != nil,
			singleLine(parsed.Text),
		)

		result, err := sender.Deliver(parsed)
		if err != nil {
			recordFailure(ctx, gm, store, msg.Id, err)
			continue
		}
		log.Printf(
			"telegram delivery ok for gmail message %s: mode=%s chat_id=%d thread_id=%d",
			msg.Id,
			result.Mode,
			parsed.ChatID,
			parsed.ThreadID,
		)

		if err := store.ClearFailure(ctx, msg.Id); err != nil {
			log.Printf("clear failures %s: %v", msg.Id, err)
		}
		if err := store.MarkProcessed(ctx, msg.Id); err != nil {
			log.Printf("persist processed %s: %v", msg.Id, err)
		}
		if err := gm.MarkRead(ctx, msg.Id); err != nil {
			log.Printf("mark read %s: %v", msg.Id, err)
		}
	}

	return nil
}

func recordFailure(ctx context.Context, gm *gmailclient.Client, store *state.Store, messageID string, reason error) {
	count, err := store.RecordFailure(ctx, messageID, reason.Error())
	if err != nil {
		log.Printf("record failure %s: %v", messageID, err)
		return
	}

	log.Printf("message %s failed attempt %d/%d: %v", messageID, count, maxFailures, reason)
	if count < maxFailures {
		return
	}

	if err := gm.MarkFailed(ctx, messageID); err != nil {
		log.Printf("mark failed message %s: %v", messageID, err)
		return
	}

	if _, lastError, ok, detailErr := store.FailureDetails(ctx, messageID); detailErr != nil {
		log.Printf("load failure details %s: %v", messageID, detailErr)
	} else if ok {
		log.Printf("message %s moved to Gmail label mailtg_failed after %d failures; last error: %s", messageID, count, lastError)
	}
}

func singleLine(text string) string {
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	return strings.Join(strings.Fields(text), " ")
}

func snapshotUnreadIDs(ctx context.Context, gm *gmailclient.Client) (map[string]struct{}, error) {
	messages, err := gm.ListUnread(ctx)
	if err != nil {
		return nil, err
	}

	ignored := make(map[string]struct{}, len(messages))
	for _, msg := range messages {
		ignored[msg.Id] = struct{}{}
	}
	return ignored, nil
}
