package mailparse

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	mail "github.com/emersion/go-message/mail"
	"golang.org/x/net/html"
)

var tagStripper = regexp.MustCompile(`\+(-?\d+)(?:\+(\d+))?$`)
var firstURLPattern = regexp.MustCompile(`https?://[^\s<>()]+`)

type ParsedMessage struct {
	MessageID string
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

func ParseRaw(messageID string, raw io.Reader) (*ParsedMessage, error) {
	message.CharsetReader = charset.Reader

	msg, err := mail.CreateReader(raw)
	if err != nil {
		return nil, fmt.Errorf("read raw message: %w", err)
	}

	recipient, err := findRecipient(&msg.Header)
	if err != nil {
		return nil, err
	}

	chatID, threadID, err := parseAddressTarget(recipient)
	if err != nil {
		return nil, err
	}

	text, photo, err := extractContent(msg)
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
		MessageID: messageID,
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

type headerGetter interface {
	Get(string) string
}

func findRecipient(headers headerGetter) (string, error) {
	for _, key := range []string{"Delivered-To", "X-Original-To", "To"} {
		if value := headers.Get(key); value != "" {
			addresses, err := mailAddressList(value)
			if err == nil && len(addresses) > 0 {
				return addresses[0], nil
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

func extractContent(reader *mail.Reader) (string, *Photo, error) {
	var plainText string
	var htmlText string
	var photo *Photo

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if !message.IsUnknownCharset(err) {
				return "", nil, fmt.Errorf("read message part: %w", err)
			}
		}
		if part == nil {
			continue
		}

		data, err := io.ReadAll(part.Body)
		if err != nil {
			return "", nil, fmt.Errorf("read part body: %w", err)
		}

		switch header := part.Header.(type) {
		case *mail.InlineHeader:
			mediaType, _, _ := header.ContentType()
			text, partPhoto, err := pickSinglePart(mediaType, header.Get("Content-Disposition"), "", "", data)
			if err != nil {
				return "", nil, err
			}
			if plainText == "" && text != "" && mediaType == "text/plain" {
				plainText = text
			}
			if htmlText == "" && text != "" && mediaType == "text/html" {
				htmlText = text
			}
			if photo == nil && partPhoto != nil {
				photo = partPhoto
			}
		case *mail.AttachmentHeader:
			filename, _ := header.Filename()
			mediaType, _, _ := header.ContentType()
			_, partPhoto, err := pickSinglePart(mediaType, header.Get("Content-Disposition"), "", filename, data)
			if err != nil {
				return "", nil, err
			}
			if photo == nil && partPhoto != nil {
				photo = partPhoto
			}
		}
	}

	if plainText != "" {
		return plainText, photo, nil
	}

	return htmlText, photo, nil
}

func pickSinglePart(mediaType, disposition, _ string, fileName string, data []byte) (string, *Photo, error) {
	switch {
	case mediaType == "text/plain":
		return strings.TrimSpace(string(data)), nil, nil
	case mediaType == "text/html":
		return strings.TrimSpace(htmlToText(string(data))), nil, nil
	case strings.HasPrefix(mediaType, "image/"):
		if strings.HasPrefix(strings.ToLower(disposition), "attachment") || strings.HasPrefix(strings.ToLower(disposition), "inline") || disposition == "" {
			return "", &Photo{
				Filename: filenameOrDefault(fileName, mediaType),
				MIMEType: mediaType,
				Data:     data,
			}, nil
		}
	}
	return "", nil, nil
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
