package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

// Бинарник агента, который мастер раздаёт нодам (тот же, что ставит Ansible).
// Хэш считаем один раз: файл внутри образа и меняется только вместе с мастером.
type agentBinary struct {
	once sync.Once
	path string
	sha  string
}

func (b *agentBinary) hash() string {
	b.once.Do(func() {
		f, err := os.Open(b.path)
		if err != nil {
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err == nil {
			b.sha = hex.EncodeToString(h.Sum(nil))
		}
	})
	return b.sha
}

// agentBinaryHandler отдаёт бинарник агента ноде (по её токену) с хэшем в заголовке
func (s *Server) agentBinaryHandler(c echo.Context) error {
	sha := s.agentBin.hash()
	if sha == "" {
		return notFound(c)
	}
	c.Response().Header().Set("X-Agent-SHA256", sha)
	return c.File(s.agentBin.path)
}

// xrayVersionRe — версия Xray: 26.3.27 (без «v»)
var xrayVersionRe = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)

// normalizeXrayVersion: "v26.3.27" → "26.3.27"; пусто — не управлять версией
func normalizeXrayVersion(v string) (string, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return "", true
	}
	return v, xrayVersionRe.MatchString(v)
}

// maintenance — что нода должна поддерживать в актуальном состоянии
func (s *Server) maintenance(r Routing) *protocol.Maintenance {
	return &protocol.Maintenance{
		AgentSHA256: s.agentBin.hash(),
		XrayVersion: r.XrayVersion,
		GeoUpdate:   r.GeoUpdate,
	}
}

// Последняя версия Xray на GitHub. Кэшируем на час: панель спрашивает при каждом открытии.
type latestCache struct {
	mu      sync.Mutex
	version string
	at      time.Time
}

// xrayReleasesURL — адрес API релизов (в тестах подменяется)
var xrayReleasesURL = "https://api.github.com/repos/XTLS/Xray-core/releases/latest"

func (s *Server) latestXray(c echo.Context) error {
	s.latest.mu.Lock()
	defer s.latest.mu.Unlock()
	if s.latest.version == "" || s.now().Sub(s.latest.at) > time.Hour {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(xrayReleasesURL)
		if err != nil {
			return jsonError(c, http.StatusBadGateway, "GitHub недоступен: "+err.Error())
		}
		defer resp.Body.Close()
		var rel struct {
			TagName string `json:"tag_name"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel) != nil {
			return jsonError(c, http.StatusBadGateway, "GitHub ответил не так, как ожидалось")
		}
		v, ok := normalizeXrayVersion(rel.TagName)
		if !ok || v == "" {
			return jsonError(c, http.StatusBadGateway, "Непонятная версия: "+rel.TagName)
		}
		s.latest.version, s.latest.at = v, s.now()
	}
	return c.JSON(http.StatusOK, map[string]string{"version": s.latest.version})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
