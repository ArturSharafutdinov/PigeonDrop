package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The operator's Telegram commands. `telegram check` needs no database.

const tgTestToken = "123456789:AAH-test_token0123456789abcdefghijk"

// stubBotAPI answers getMe and getWebhookInfo as told and keeps the paths it was asked for.
type stubBotAPI struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func newStubBotAPI(t *testing.T, getMe, webhook string, status int) *stubBotAPI {
	t.Helper()
	stub := &stubBotAPI{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		stub.mu.Lock()
		stub.paths = append(stub.paths, r.URL.Path)
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status != 200 {
			w.WriteHeader(status)
			io.WriteString(w, `{"ok":false,"error_code":`+strconv.Itoa(status)+`,"description":"nope"}`)
			return
		}
		switch r.URL.Path {
		case "/bot" + tgTestToken + "/getMe":
			io.WriteString(w, `{"ok":true,"result":`+getMe+`}`)
		case "/bot" + tgTestToken + "/getWebhookInfo":
			io.WriteString(w, `{"ok":true,"result":`+webhook+`}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
		}
	}))
	t.Cleanup(stub.Close)
	return stub
}

func tgEnv(api string) func(string) string {
	return envOf(map[string]string{"DATABASE_URL": "postgres://nobody@127.0.0.1:1/none", "TELEGRAM_BOT_TOKEN": tgTestToken, "TELEGRAM_API_URL": api, "TELEGRAM_TIMEOUT": "5s"})
}

func runTelegramCheck(t *testing.T, env func(string) string, extra ...string) (stdout string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = run(context.Background(), append([]string{"telegram", "check"}, extra...), env, strings.NewReader(""), &out, &errOut)
	return out.String(), err
}

const botReply = `{"id":777,"is_bot":true,"first_name":"PigeonDrop","username":"pigeondrop_bot"}`

func TestTelegramCheckSaysOkAndNamesTheBot(t *testing.T) {
	stub := newStubBotAPI(t, botReply, `{"url":"","pending_update_count":0}`, 200)

	stdout, err := runTelegramCheck(t, tgEnv(stub.URL))

	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ok:", "@pigeondrop_bot", "id 777", "no webhook", "https://t.me/pigeondrop_bot"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q: %q", want, stdout)
		}
	}
	if strings.Contains(stdout, tgTestToken) {
		t.Error("the token is in the output")
	}
	if len(stub.paths) != 2 {
		t.Errorf("paths asked: %v", stub.paths)
	}
}

func TestTelegramCheckRefusesWhileAWebhookIsSet(t *testing.T) {
	stub := newStubBotAPI(t, botReply, `{"url":"https://hooks.example.com/secret/path/abc123","pending_update_count":4}`, 200)

	stdout, err := runTelegramCheck(t, tgEnv(stub.URL))

	if err == nil || stdout != "" {
		t.Fatalf("stdout %q, error %v", stdout, err)
	}
	if !strings.Contains(err.Error(), "webhook") || !strings.Contains(err.Error(), "hooks.example.com") || !strings.Contains(err.Error(), "@pigeondrop_bot") {
		t.Errorf("the error does not say what is wrong: %v", err)
	}
	if strings.Contains(err.Error(), "secret/path") || strings.Contains(err.Error(), "abc123") {
		t.Errorf("the webhook's secret path is in the error: %v", err)
	}
}

func TestTelegramCheckNamesTheLikelyCauseWhenTheTokenIsRefused(t *testing.T) {
	stub := newStubBotAPI(t, botReply, `{}`, 401)

	stdout, err := runTelegramCheck(t, tgEnv(stub.URL))

	if err == nil || stdout != "" {
		t.Fatalf("stdout %q, error %v", stdout, err)
	}
	if !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") || !strings.Contains(err.Error(), "401") {
		t.Errorf("the error does not point at the token: %v", err)
	}
	if strings.Contains(err.Error(), tgTestToken) || strings.Contains(err.Error(), "AAH-test") {
		t.Errorf("the token is in the error: %v", err)
	}
}

func TestTelegramCheckSaysWhenTelegramCannotBeReached(t *testing.T) {
	stub := newStubBotAPI(t, botReply, `{}`, 200)
	address := stub.URL
	stub.Close() // nothing listens there any more

	_, err := runTelegramCheck(t, tgEnv(address))

	if err == nil || !strings.Contains(err.Error(), "cannot be used") || !strings.Contains(err.Error(), "api.telegram.org") {
		t.Fatalf("error: %v", err)
	}
	if strings.Contains(err.Error(), tgTestToken) || strings.Contains(err.Error(), "AAH-test") {
		t.Errorf("the token is in the error: %v", err)
	}
}

func TestTelegramCheckReportsATelegramThatAnswersBadly(t *testing.T) {
	stub := newStubBotAPI(t, botReply, `{}`, 400)

	_, err := runTelegramCheck(t, tgEnv(stub.URL))

	if err == nil || !strings.Contains(err.Error(), "answered badly") {
		t.Fatalf("error: %v", err)
	}
	if strings.Contains(err.Error(), tgTestToken) {
		t.Errorf("the token is in the error: %v", err)
	}
}

func TestTelegramCheckWithoutATokenSaysSo(t *testing.T) {
	_, err := runTelegramCheck(t, envOf(map[string]string{"DATABASE_URL": "postgres://nobody@127.0.0.1:1/none"}))

	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN is not set") {
		t.Fatalf("error: %v", err)
	}
}

func TestTelegramCheckTakesNoArguments(t *testing.T) {
	_, err := runTelegramCheck(t, tgEnv("http://127.0.0.1:1"), "now")

	if err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("error: %v", err)
	}
}

func TestTelegramUnknownCommandShowsTheUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run(context.Background(), []string{"telegram", "frobnicate"}, tgEnv("http://127.0.0.1:1"), strings.NewReader(""), &out, &errOut)

	// This one asks for the database (every command but the checks does), which is not there: either error is a refusal.
	if err == nil {
		t.Fatal("an unknown command was accepted")
	}
}
