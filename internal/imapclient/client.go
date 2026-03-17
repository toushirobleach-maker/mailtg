package imapclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
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
	ID      string
	Mailbox string
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

	mailboxes, err := c.listMailboxes(ctx, conn)
	if err != nil {
		return nil, err
	}

	refs := make([]MessageRef, 0)
	for _, mailbox := range mailboxes {
		uids, err := c.searchUnreadInMailbox(ctx, conn, mailbox)
		if err != nil {
			return nil, err
		}
		for _, uid := range uids {
			refs = append(refs, MessageRef{
				ID:      composeMessageID(mailbox, uid),
				Mailbox: mailbox,
			})
		}
	}

	return refs, nil
}

func (c *Client) GetParsedMessage(ctx context.Context, id string) (*mailparse.ParsedMessage, error) {
	mailbox, uid, err := parseMessageID(id)
	if err != nil {
		return nil, err
	}

	conn, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Logout()

	if err := c.selectMailbox(ctx, conn, mailbox); err != nil {
		return nil, err
	}

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

	return conn, nil
}

func (c *Client) listMailboxes(ctx context.Context, conn *client.Client) ([]string, error) {
	mailboxesCh := make(chan *imap.MailboxInfo, 100)
	errCh := make(chan error, 1)

	go func() {
		errCh <- conn.List("", "*", mailboxesCh)
	}()

	var mailboxes []string
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case mailbox, ok := <-mailboxesCh:
			if !ok {
				if err := <-errCh; err != nil {
					return nil, fmt.Errorf("list mailboxes: %w", err)
				}
				return mailboxes, nil
			}
			if mailbox == nil || hasNoSelect(mailbox.Attributes) {
				continue
			}
			mailboxes = append(mailboxes, mailbox.Name)
		}
	}
}

func (c *Client) searchUnreadInMailbox(ctx context.Context, conn *client.Client, mailbox string) ([]uint32, error) {
	if err := c.selectMailbox(ctx, conn, mailbox); err != nil {
		return nil, err
	}

	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}

	uids, err := conn.UidSearch(criteria)
	if err != nil {
		return nil, fmt.Errorf("search unread messages in %s: %w", mailbox, err)
	}

	return uids, nil
}

func (c *Client) selectMailbox(ctx context.Context, conn *client.Client, mailbox string) error {
	done := make(chan error, 1)
	go func() {
		_, err := conn.Select(mailbox, false)
		done <- err
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("select mailbox %s: %w", mailbox, err)
		}
		return nil
	}
}

func (c *Client) updateFlags(ctx context.Context, id string, item imap.StoreItem, flags []interface{}) error {
	mailbox, uid, err := parseMessageID(id)
	if err != nil {
		return err
	}

	conn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Logout()

	if err := c.selectMailbox(ctx, conn, mailbox); err != nil {
		return err
	}

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

func hasNoSelect(attrs []string) bool {
	for _, attr := range attrs {
		if strings.EqualFold(attr, imap.NoSelectAttr) {
			return true
		}
	}
	return false
}

func composeMessageID(mailbox string, uid uint32) string {
	return mailbox + "::" + strconv.FormatUint(uint64(uid), 10)
}

func parseMessageID(id string) (string, uint32, error) {
	mailbox, uidRaw, ok := strings.Cut(id, "::")
	if !ok || mailbox == "" || uidRaw == "" {
		return "", 0, fmt.Errorf("invalid imap message id %q", id)
	}

	value, err := strconv.ParseUint(uidRaw, 10, 32)
	if err != nil {
		return "", 0, fmt.Errorf("invalid imap uid %q: %w", id, err)
	}

	return mailbox, uint32(value), nil
}
