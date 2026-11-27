package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/runner"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/website"
)

func makeSiteZIP(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, content := range files {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestPublishAndPreviewSiteArchive(t *testing.T) {
	ev := hiddenEnv(t, "")
	const path = "/s3cr3t-path/api/public-site"
	login := ev.do("POST", "/s3cr3t-path/api/login", `{"password":"secret"}`, nil)
	var session map[string]string
	if login.Code != http.StatusOK || json.Unmarshal(login.Body.Bytes(), &session) != nil || session["token"] == "" {
		t.Fatalf("login: %d", login.Code)
	}
	auth := map[string]string{"Authorization": "Bearer " + session["token"]}
	files := map[string]string{
		"index.html":      `<link href="assets/site.css" rel="stylesheet"><h1>Archived site</h1>`,
		"assets/site.css": "h1 { color: rebeccapurple; }",
		"404.html":        "<h1>Missing</h1>",
	}
	archive := makeSiteZIP(t, files)
	payload, _ := json.Marshal(website.Settings{Archive: archive})
	rec := ev.do("POST", path+"/preview", string(payload), auth)
	var url map[string]string
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &url) != nil || url["url"] == "" {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	for suffix, source := range map[string]string{"": "index.html", "assets/site.css": "assets/site.css"} {
		if rec := ev.do("GET", url["url"]+suffix, "", nil); rec.Code != http.StatusOK || rec.Body.String() != files[source] {
			t.Fatalf("preview %s: %d %s", suffix, rec.Code, rec.Body)
		}
	}
	if rec := ev.do("GET", "/assets/site.css", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("preview changed published content: %d", rec.Code)
	}
	publish := func(value string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]website.Settings{"page": {Archive: value}})
		return ev.do("PUT", path, string(body), auth)
	}
	if rec := publish(makeSiteZIP(t, map[string]string{"index.html": "OK", "../escape.html": "invalid"})); rec.Code != http.StatusBadRequest {
		t.Fatalf("accepted ZIP with parent path: %d", rec.Code)
	}
	if rec := publish(archive); rec.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	if rec := ev.do("GET", "/assets/site.css", "", nil); rec.Code != http.StatusOK || rec.Body.String() != files["assets/site.css"] {
		t.Fatalf("asset: %d %s", rec.Code, rec.Body)
	}
	if rec := ev.do("GET", "/missing", "", nil); rec.Code != http.StatusNotFound || rec.Body.String() != files["404.html"] {
		t.Fatalf("custom 404: %d %s", rec.Code, rec.Body)
	}
	if rec := ev.do("PUT", path, `{"rollback":true}`, auth); rec.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body)
	}
	if rec := ev.do("GET", "/assets/site.css", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("rolled back asset is still public: %d", rec.Code)
	}
}

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
	ev.s.provisionSelfSteal = func(_ context.Context, target, name, alias string, page website.Settings, _ string, _ runner.SSHRoute) error {
		called = true
		if target != ip || name != domain || alias != cdn || website.Render(page) != markup {
			t.Fatalf("published %q to %s (%s, %s)", website.Render(page), target, name, alias)
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
