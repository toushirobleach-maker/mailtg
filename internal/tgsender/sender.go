package tgsender

import (
	"bytes"
	"encoding/json"
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"mailtg/internal/mailparse"
)

type Sender struct {
	bot *tgbotapi.BotAPI
}

type DeliveryResult struct {
	Mode string
}

func New(token string) (*Sender, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}

	return &Sender{bot: bot}, nil
}

func (s *Sender) Deliver(message *mailparse.ParsedMessage) (*DeliveryResult, error) {
	if message.Photo != nil {
		params := tgbotapi.Params{
			"chat_id": message.ChatIDString(),
			"caption": message.Text,
		}
		if message.ThreadID != 0 {
			params["message_thread_id"] = message.ThreadIDString()
		}
		if err := addDashboardButton(params, message.URL); err != nil {
			return nil, err
		}

		resp, err := s.bot.UploadFiles("sendPhoto", params, []tgbotapi.RequestFile{{
			Name: "photo",
			Data: tgbotapi.FileReader{
				Name:   message.Photo.Filename,
				Reader: bytes.NewReader(message.Photo.Data),
			},
		}})
		if err != nil {
			return nil, fmt.Errorf("send photo: %w", err)
		}
		if !resp.Ok {
			return nil, fmt.Errorf("send photo: telegram api error: %s", resp.Description)
		}
		return &DeliveryResult{Mode: "photo"}, nil
	}

	params := tgbotapi.Params{
		"chat_id": message.ChatIDString(),
		"text":    message.Text,
	}
	if message.ThreadID != 0 {
		params["message_thread_id"] = message.ThreadIDString()
	}
	if err := addDashboardButton(params, message.URL); err != nil {
		return nil, err
	}
	resp, err := s.bot.MakeRequest("sendMessage", params)
	if err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}
	if !resp.Ok {
		return nil, fmt.Errorf("send message: telegram api error: %s", resp.Description)
	}
	return &DeliveryResult{Mode: "text"}, nil
}

func addDashboardButton(params tgbotapi.Params, url string) error {
	if url == "" {
		return nil
	}

	markup := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL("Dashboard", url),
		),
	)

	payload, err := json.Marshal(markup)
	if err != nil {
		return fmt.Errorf("marshal inline keyboard: %w", err)
	}
	params["reply_markup"] = string(payload)
	return nil
}
