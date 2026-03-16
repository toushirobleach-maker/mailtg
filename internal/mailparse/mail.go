package mailparse

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	gmail "google.golang.org/api/gmail/v1"
)

var tagStripper = regexp.MustCompile(`\+(-?\d+)(?:\+(\d+))?$`)
var firstURLPattern = regexp.MustCompile(`https?://[^\s<>()]+`)

type ParsedMessage struct {
	GmailID   string
	Recipient string
	ChatID    int64
	ThreadID  int64
	Text      string
	URL       string
	Photo     *Photo
}

type Photo struct {
	Filename string
	MIMEType string
	Data     []byte
}

type AttachmentFetcher func(messageID, attachmentID string) ([]byte, error)

func Parse(message *gmail.Message, fetch AttachmentFetcher) (*ParsedMessage, error) {
	if message == nil || message.Payload == nil {
		return nil, errors.New("empty message payload")
	}

	recipient, err := findRecipient(message.Payload.Headers)
	if err != nil {
		return nil, err
	}

	chatID, threadID, err := parseAddressTarget(recipient)
	if err != nil {
		return nil, err
	}

	text, photo, err := walkPart(message.Id, message.Payload, fetch)
	if err != nil {
		return nil, err
	}
	if photo != nil {
		text = stripPhotoFilenameArtifacts(text, photo.Filename)
	}
	text, extractedURL := extractFirstURL(text)
	safeURL, err := normalizeSafeURL(extractedURL)
	if err != nil {
		return nil, err
	}
	if text == "" {
		text = "(empty message)"
	}

	return &ParsedMessage{
		GmailID:   message.Id,
		Recipient: recipient,
		ChatID:    chatID,
		ThreadID:  threadID,
		Text:      text,
		URL:       safeURL,
		Photo:     photo,
	}, nil
}

func (p *ParsedMessage) ChatIDString() string {
	return strconv.FormatInt(p.ChatID, 10)
}

func (p *ParsedMessage) ThreadIDString() string {
	return strconv.FormatInt(p.ThreadID, 10)
}

func findRecipient(headers []*gmail.MessagePartHeader) (string, error) {
	for _, key := range []string{"Delivered-To", "X-Original-To", "To"} {
		for _, header := range headers {
			if strings.EqualFold(header.Name, key) && header.Value != "" {
				addresses, err := mailAddressList(header.Value)
				if err == nil && len(addresses) > 0 {
					return addresses[0], nil
				}
			}
		}
	}
	return "", errors.New("recipient address not found")
}

func parseAddressTarget(address string) (int64, int64, error) {
	at := strings.LastIndex(address, "@")
	if at == -1 {
		return 0, 0, fmt.Errorf("invalid recipient address: %q", address)
	}

	localPart := address[:at]
	match := tagStripper.FindStringSubmatch(localPart)
	if match == nil {
		return 0, 0, fmt.Errorf("recipient does not contain +chat[+thread] suffix: %q", address)
	}

	chatID, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid chat id %q: %w", match[1], err)
	}

	var threadID int64
	if match[2] != "" {
		threadID, err = strconv.ParseInt(match[2], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid thread id %q: %w", match[2], err)
		}
	}

	return chatID, threadID, nil
}

func walkPart(messageID string, part *gmail.MessagePart, fetch AttachmentFetcher) (string, *Photo, error) {
	var plainText string
	var htmlText string
	var photo *Photo

	var visit func(current *gmail.MessagePart) error
	visit = func(current *gmail.MessagePart) error {
		if current == nil {
			return nil
		}

		if len(current.Parts) > 0 {
			for _, child := range current.Parts {
				if err := visit(child); err != nil {
					return err
				}
			}
			return nil
		}

		data, err := readBody(messageID, current, fetch)
		if err != nil {
			return err
		}

		switch {
		case current.MimeType == "text/plain" && plainText == "":
			plainText = strings.TrimSpace(string(data))
		case current.MimeType == "text/html" && htmlText == "":
			htmlText = strings.TrimSpace(htmlToText(string(data)))
		case strings.HasPrefix(current.MimeType, "image/") && photo == nil:
			photo = &Photo{
				Filename: filenameOrDefault(current.Filename, current.MimeType),
				MIMEType: current.MimeType,
				Data:     data,
			}
		}

		return nil
	}

	if err := visit(part); err != nil {
		return "", nil, err
	}

	if plainText != "" {
		return plainText, photo, nil
	}

	return htmlText, photo, nil
}

func readBody(messageID string, part *gmail.MessagePart, fetch AttachmentFetcher) ([]byte, error) {
	if part.Body == nil {
		return nil, nil
	}
	if part.Body.AttachmentId != "" {
		return fetch(messageID, part.Body.AttachmentId)
	}
	if part.Body.Data == "" {
		return nil, nil
	}

	decoded, err := base64.URLEncoding.DecodeString(part.Body.Data)
	if err == nil {
		return decoded, nil
	}

	return base64.RawURLEncoding.DecodeString(part.Body.Data)
}

func filenameOrDefault(name, mimeType string) string {
	if name != "" {
		return name
	}
	extensions, _ := mime.ExtensionsByType(mimeType)
	if len(extensions) > 0 {
		return "image" + extensions[0]
	}
	if slash := strings.IndexByte(mimeType, '/'); slash != -1 && slash+1 < len(mimeType) {
		return "image." + mimeType[slash+1:]
	}
	return "image" + filepath.Ext(mimeType)
}

func htmlToText(source string) string {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return source
	}

	var parts []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			text := strings.TrimSpace(html.UnescapeString(n.Data))
			if text != "" {
				parts = append(parts, text)
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)

	return strings.Join(parts, "\n")
}

func stripPhotoFilenameArtifacts(text, filename string) string {
	text = strings.TrimSpace(text)
	filename = strings.TrimSpace(filename)
	if text == "" || filename == "" {
		return text
	}

	replacements := []string{
		"[" + filename + "]",
		"[ " + filename + " ]",
	}

	for _, artifact := range replacements {
		text = strings.ReplaceAll(text, artifact, "")
	}

	lines := strings.Split(text, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		clean := strings.TrimSpace(line)
		if clean == "" {
			if len(filtered) > 0 && filtered[len(filtered)-1] != "" {
				filtered = append(filtered, "")
			}
			continue
		}
		filtered = append(filtered, clean)
	}

	return strings.TrimSpace(strings.Join(filtered, "\n"))
}

func extractFirstURL(text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return text, ""
	}

	match := firstURLPattern.FindString(text)
	if match == "" {
		return text, ""
	}

	cleaned := strings.Replace(text, match, "", 1)
	lines := strings.Split(cleaned, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(filtered) > 0 && filtered[len(filtered)-1] != "" {
				filtered = append(filtered, "")
			}
			continue
		}
		filtered = append(filtered, line)
	}

	return strings.TrimSpace(strings.Join(filtered, "\n")), match
}

func normalizeSafeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("url contains control characters")
		}
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported url scheme: %s", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", errors.New("url host is empty")
	}

	return parsed.String(), nil
}
