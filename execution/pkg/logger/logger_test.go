package logger

import (
	"os"
	"testing"
)

func TestLogToFile(t *testing.T) {
	f, _ := os.OpenFile("./test_log.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	config.Outputs = append(config.Outputs, f)
	Info("Test Log To File", "Oke")
}

func TestLogErrWithTelegram(t *testing.T) {
	config.TelegramChatId = -4102931211
	config.TelegramToken = "6301510657:AAG6llK5jeEzLKg-ZUD87pD_dc2IP1m16Ls"
	config.TelegramNotifyError = true
	Error("Test Log Error With Telegram", 123, "oke")
}

func TestLogTelegram(t *testing.T) {
	config.Identifier = "TEST"
	config.Flag = FLAG_TELEGRAM
	config.TelegramChatId = -4102931211
	config.TelegramToken = "6301510657:AAG6llK5jeEzLKg-ZUD87pD_dc2IP1m16Ls"
	Telegram("Test Log Telegram", "xxx", "oke")
}

func TestLogTopicTelegram(t *testing.T) {
	config.Identifier = "TEST"
	config.Flag = FLAG_TELEGRAM
	config.TelegramThreadId = 18
	config.TelegramChatId = -1002134567419
	config.TelegramToken = "1445784583:AAGIJpLzxGsB7csutW2fj7MVIOQlvQNa1XY"
	Telegram("Test Log Telegram", "xxx", "oke")
}

func TestGetTelegramConfig(t *testing.T) {
	cfgMu.Lock()
	origTok := config.TelegramToken
	origChat := config.TelegramChatId
	cfgMu.Unlock()
	defer func() {
		cfgMu.Lock()
		config.TelegramToken = origTok
		config.TelegramChatId = origChat
		cfgMu.Unlock()
	}()

	cfgMu.Lock()
	config.TelegramToken = "8230176859:AAG2MuF6RI3hRPm9H8_TctSSANkwwrEEdIc"
	config.TelegramChatId = -1003867050625
	cfgMu.Unlock()

	tok, cid, ok := GetTelegramConfig()
	if !ok || cid != -1003867050625 || tok != "8230...EdIc" {
		t.Fatalf("unexpected GetTelegramConfig result: ok=%v, cid=%d, tok=%s", ok, cid, tok)
	}

	cfgMu.Lock()
	config.TelegramToken = ""
	config.TelegramChatId = 0
	cfgMu.Unlock()

	tok, cid, ok = GetTelegramConfig()
	if ok || cid != 0 || tok != "" {
		t.Fatalf("expected unconfigured, got: ok=%v, cid=%d, tok=%s", ok, cid, tok)
	}
}
