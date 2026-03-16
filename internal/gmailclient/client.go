package gmailclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"mailtg/internal/config"
	"mailtg/internal/mailparse"
	"mailtg/internal/state"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	gmail "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

type Client struct {
	service      *gmail.Service
	query        string
	failureLabel string
}

func New(ctx context.Context, cfg *config.Config, store *state.Store) (*Client, error) {
	credentials, err := os.ReadFile(cfg.GmailCredentialsPath)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}

	oauthConfig, err := google.ConfigFromJSON(credentials, gmail.GmailModifyScope)
	if err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}

	httpClient, err := authorizedHTTPClient(ctx, oauthConfig, store)
	if err != nil {
		return nil, err
	}

	service, err := gmail.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create gmail service: %w", err)
	}

	client := &Client{
		service:      service,
		query:        cfg.GmailQuery,
		failureLabel: "mailtg_failed",
	}
	if err := client.ensureLabel(ctx, client.failureLabel); err != nil {
		return nil, err
	}

	return client, nil
}

func (c *Client) ListUnread(ctx context.Context) ([]*gmail.Message, error) {
	resp, err := c.service.Users.Messages.List("me").
		Q(c.query).
		MaxResults(20).
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("list unread messages: %w", err)
	}
	return resp.Messages, nil
}

func (c *Client) GetParsedMessage(ctx context.Context, messageID string) (*mailparse.ParsedMessage, error) {
	message, err := c.service.Users.Messages.Get("me", messageID).
		Format("full").
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}

	return mailparse.Parse(message, func(msgID, attachmentID string) ([]byte, error) {
		resp, attErr := c.service.Users.Messages.Attachments.Get("me", msgID, attachmentID).
			Context(ctx).
			Do()
		if attErr != nil {
			return nil, fmt.Errorf("get attachment: %w", attErr)
		}
		return decodeAttachment(resp.Data)
	})
}

func (c *Client) MarkRead(ctx context.Context, messageID string) error {
	_, err := c.service.Users.Messages.Modify("me", messageID, &gmail.ModifyMessageRequest{
		RemoveLabelIds: []string{"UNREAD"},
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("modify message: %w", err)
	}
	return nil
}

func (c *Client) MarkFailed(ctx context.Context, messageID string) error {
	labelID, err := c.findLabelID(ctx, c.failureLabel)
	if err != nil {
		return err
	}

	req := &gmail.ModifyMessageRequest{
		RemoveLabelIds: []string{"UNREAD"},
	}
	if labelID != "" {
		req.AddLabelIds = []string{labelID}
	}

	_, err = c.service.Users.Messages.Modify("me", messageID, req).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("mark failed message: %w", err)
	}
	return nil
}

func decodeAttachment(data string) ([]byte, error) {
	if data == "" {
		return nil, nil
	}
	decoded, err := base64.URLEncoding.DecodeString(data)
	if err == nil {
		return decoded, nil
	}
	return base64.RawURLEncoding.DecodeString(data)
}

func authorizedHTTPClient(ctx context.Context, cfg *oauth2.Config, store *state.Store) (*http.Client, error) {
	token, err := readToken(ctx, store)
	if err != nil {
		token, err = readLegacyToken("token.json")
		if err == nil {
			if saveErr := saveToken(ctx, store, token); saveErr != nil {
				return nil, saveErr
			}
			return cfg.Client(ctx, token), nil
		}

		token, err = getTokenFromWeb(ctx, cfg)
		if err != nil {
			return nil, err
		}
		if err := saveToken(ctx, store, token); err != nil {
			return nil, err
		}
	}

	return cfg.Client(ctx, token), nil
}

func getTokenFromWeb(ctx context.Context, cfg *oauth2.Config) (*oauth2.Token, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start oauth callback listener: %w", err)
	}
	defer listener.Close()

	redirectURL := fmt.Sprintf("http://%s/callback", listener.Addr().String())
	cfg.RedirectURL = redirectURL

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if gotErr := r.URL.Query().Get("error"); gotErr != "" {
			http.Error(w, gotErr, http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("oauth callback error: %s", gotErr):
			default:
			}
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			select {
			case errCh <- errors.New("oauth callback missing code"):
			default:
			}
			return
		}

		_, _ = w.Write([]byte("Authorization complete. You can return to the terminal."))
		select {
		case codeCh <- code:
		default:
		}
	})

	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			select {
			case errCh <- serveErr:
			default:
			}
		}
	}()
	defer server.Shutdown(ctx)

	authURL := cfg.AuthCodeURL(
		"mailtg-state",
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)

	fmt.Println("Open this URL in your browser and authorize Gmail access:")
	fmt.Println(authURL)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errCh:
		return nil, err
	case code := <-codeCh:
		token, tokenErr := cfg.Exchange(ctx, code)
		if tokenErr != nil {
			return nil, fmt.Errorf("exchange auth code: %w", tokenErr)
		}
		return token, nil
	}
}

func readToken(ctx context.Context, store *state.Store) (*oauth2.Token, error) {
	tokenJSON, ok, err := store.ReadOAuthToken(ctx, "gmail")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("gmail oauth token not found")
	}
	token := &oauth2.Token{}
	if err := json.Unmarshal([]byte(tokenJSON), token); err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}
	return token, nil
}

func readLegacyToken(path string) (*oauth2.Token, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	token := &oauth2.Token{}
	if err := json.NewDecoder(file).Decode(token); err != nil {
		return nil, fmt.Errorf("decode legacy token: %w", err)
	}
	return token, nil
}

func saveToken(ctx context.Context, store *state.Store, token *oauth2.Token) error {
	payload, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("encode token: %w", err)
	}
	if err := store.SaveOAuthToken(ctx, "gmail", string(payload)); err != nil {
		return err
	}
	return nil
}

func (c *Client) ensureLabel(ctx context.Context, name string) error {
	labelID, err := c.findLabelID(ctx, name)
	if err != nil {
		return err
	}
	if labelID != "" {
		return nil
	}

	_, err = c.service.Users.Labels.Create("me", &gmail.Label{
		Name:                  name,
		LabelListVisibility:   "labelShow",
		MessageListVisibility: "show",
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("create label %q: %w", name, err)
	}
	return nil
}

func (c *Client) findLabelID(ctx context.Context, name string) (string, error) {
	labels, err := c.service.Users.Labels.List("me").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("list labels: %w", err)
	}
	for _, label := range labels.Labels {
		if label.Name == name {
			return label.Id, nil
		}
	}
	return "", nil
}
