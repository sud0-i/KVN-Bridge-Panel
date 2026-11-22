package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPublishPublicSiteAndRollback(t *testing.T) {
	ev := hiddenEnv(t, "")
	const path = "/s3cr3t-path/api/public-site"
	const input = `{"page":{"title":"<script>bad</script>","intro":"Notes","theme":"slate"}}`
	login := ev.do("POST", "/s3cr3t-path/api/login", `{"password":"secret"}`, nil)
	var session map[string]string
	if login.Code != http.StatusOK || json.Unmarshal(login.Body.Bytes(), &session) != nil || session["token"] == "" {
		t.Fatalf("login: %d %s", login.Code, login.Body)
	}
	auth := map[string]string{"Authorization": "Bearer " + session["token"]}
	if rec := ev.do("PUT", path, input, auth); rec.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	rec := ev.do("GET", "/", "", nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(body, "<script>") ||
		!strings.Contains(body, "&lt;script&gt;") || !strings.Contains(body, `class="slate"`) {
		t.Fatalf("public page: %d %s", rec.Code, body)
	}
	if rec := ev.do("PUT", path, `{"rollback":true}`, auth); rec.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body)
	}
	if rec := ev.do("GET", "/", "", nil); !strings.Contains(rec.Body.String(), "Plain Web Notes") {
		t.Fatalf("default page was not restored: %s", rec.Body)
	}
}

func TestDiagnosticTextRedactsCredentials(t *testing.T) {
	const message = "test https://panel.test/sub/subtoken failed\nNODE_TOKEN=agenttoken\nPassword: badpass\nvless://example\nuser 12345678-1234-1234-1234-123456789012"
	got := diagnosticText(message)
	for _, secret := range []string{"subtoken", "agenttoken", "badpass", "vless://", "12345678"} {
		if strings.Contains(got, secret) {
			t.Fatalf("credential %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, "failed") {
		t.Fatalf("lost error context: %q", got)
	}
}
