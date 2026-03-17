package imapclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"mailtg/internal/config"
	"mailtg/internal/mailparse"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

type Client struct {
	address string
	timeout time.Duration
	config  *config.Config
}

type MessageRef struct {
	ID string
}

func New(cfg *config.Config) *Client {
	return &Client{
		address: net.JoinHostPort(cfg.IMAPHost, strconv.Itoa(cfg.IMAPPort)),
		timeout: 30 * time.Second,
		config:  cfg,
	}
}

func (c *Client) ListUnread(ctx context.Context) ([]MessageRef, error) {
	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Logout()

	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}

	uids, err := conn.UidSearch(criteria)
	if err != nil {
		return nil, fmt.Errorf("search unread messages: %w", err)
	}

	refs := make([]MessageRef, 0, len(uids))
	for _, uid := range uids {
		refs = append(refs, MessageRef{ID: uidToID(uid)})
	}
	return refs, nil
}

func (c *Client) GetParsedMessage(ctx context.Context, id string) (*mailparse.ParsedMessage, error) {
	uid, err := idToUID(id)
	if err != nil {
		return nil, err
	}

	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Logout()

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

	section := &imap.BodySectionName{}
	messages := make(chan *imap.Message, 1)
	errCh := make(chan error, 1)

	go func() {
		errCh <- conn.UidFetch(seqset, []imap.FetchItem{section.FetchItem()}, messages)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case msg := <-messages:
			if msg == nil {
				if err := <-errCh; err != nil {
					return nil, fmt.Errorf("fetch message %s: %w", id, err)
				}
				return nil, fmt.Errorf("message %s not found", id)
			}

			body := msg.GetBody(section)
			if body == nil {
				return nil, fmt.Errorf("message %s has empty body", id)
			}

			raw, err := io.ReadAll(body)
			if err != nil {
				return nil, fmt.Errorf("read message body %s: %w", id, err)
			}
			return mailparse.ParseRaw(id, bytes.NewReader(raw))
		}
	}
}

func (c *Client) MarkRead(ctx context.Context, id string) error {
	return c.updateFlags(ctx, id, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.SeenFlag})
}

func (c *Client) MarkFailed(ctx context.Context, id string) error {
	return c.MarkRead(ctx, id)
}

func (c *Client) connect(ctx context.Context) (*client.Client, error) {
	dialer := &net.Dialer{Timeout: c.timeout}
	conn, err := client.DialWithDialerTLS(dialer, c.address, &tls.Config{
		ServerName: c.config.IMAPHost,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return nil, fmt.Errorf("dial imap: %w", err)
	}

	if err := conn.Login(c.config.IMAPUsername, c.config.IMAPPassword); err != nil {
		conn.Logout()
		return nil, fmt.Errorf("imap login: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		_, selectErr := conn.Select(c.config.IMAPMailbox, false)
		done <- selectErr
	}()

	select {
	case <-ctx.Done():
		conn.Logout()
		return nil, ctx.Err()
	case err := <-done:
		if err != nil {
			conn.Logout()
			return nil, fmt.Errorf("select mailbox %s: %w", c.config.IMAPMailbox, err)
		}
	}

	return conn, nil
}

func (c *Client) updateFlags(ctx context.Context, id string, item imap.StoreItem, flags []interface{}) error {
	uid, err := idToUID(id)
	if err != nil {
		return err
	}

	conn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Logout()

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

	done := make(chan error, 1)
	go func() {
		done <- conn.UidStore(seqset, item, flags, nil)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("update flags for %s: %w", id, err)
		}
		return nil
	}
}

func uidToID(uid uint32) string {
	return strconv.FormatUint(uint64(uid), 10)
}

func idToUID(id string) (uint32, error) {
	value, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid imap uid %q: %w", id, err)
	}
	return uint32(value), nil
}
