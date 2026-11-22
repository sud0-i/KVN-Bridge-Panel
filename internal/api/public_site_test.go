package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/runner"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/website"
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
	const markup = "<!doctype html><title>Example</title><h1>My public site</h1>"
	htmlPayload, _ := json.Marshal(map[string]any{"page": map[string]string{"html": markup}})
	previewPayload, _ := json.Marshal(map[string]string{"html": markup})
	preview := ev.do("POST", path+"/preview", string(previewPayload), auth)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), "Content-Security-Policy") ||
		!strings.Contains(preview.Body.String(), "<h1>My public site</h1>") {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body)
	}
	if rec := ev.do("PUT", path, string(htmlPayload), auth); rec.Code != http.StatusOK {
		t.Fatalf("publish HTML: %d %s", rec.Code, rec.Body)
	}
	rec = ev.do("GET", "/", "", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != markup || rec.Header().Get("Content-Security-Policy") != website.Policy {
		t.Fatalf("published HTML: %d %s %v", rec.Code, rec.Body, rec.Header())
	}
}

func TestPublishHTMLToNode(t *testing.T) {
	ev := newEnv(t)
	const ip = "203.0.113.43"
	const domain = "site.example.com"
	const cdn = "edge.example.com"
	const markup = "<!doctype html><h1>Example &amp; notes</h1>"
	ev.addNode(ip, "exit")
	if err := ev.db.Model(&models.Node{}).Where("ip = ?", ip).
		Updates(map[string]any{"ssh_state": "keys", "self_steal_domain": domain, "cdn_domain": cdn}).Error; err != nil {
		t.Fatal(err)
	}
	called := false
	ev.s.provisionSelfSteal = func(_ context.Context, target, name, alias, page, _ string, _ runner.SSHRoute) error {
		called = true
		if target != ip || name != domain || alias != cdn || page != markup {
			t.Fatalf("published %q to %s (%s, %s)", page, target, name, alias)
		}
		return nil
	}
	data, _ := json.Marshal(map[string]any{"page": map[string]string{"html": markup}})
	path := "/api/public-site?node=" + ip
	rec := ev.do("PUT", path, string(data), ev.adminToken())
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("node publication: %d %s", rec.Code, rec.Body)
	}
	_, state := ev.s.publicSiteFor(ip)
	if state.Current.HTML != markup || state.Previous == nil || state.Previous.HTML != "" {
		t.Fatalf("unexpected saved state: %+v", state)
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
