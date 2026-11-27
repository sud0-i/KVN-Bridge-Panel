package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/website"
)

const publicSiteKey = "public_site"

type publicSiteState struct {
	Current  website.Settings  `json:"current"`
	Previous *website.Settings `json:"previous"`
}

func (s *Server) publicSiteFor(ip string) (string, publicSiteState) {
	state := publicSiteState{Current: website.Default()}
	key := publicSiteKey
	if ip != "" {
		_, global := s.publicSiteFor("")
		state.Current = global.Current
		key += ":" + ip
	}
	var stored models.Setting
	if s.db.First(&stored, "key = ?", key).Error == nil {
		var decoded publicSiteState
		if json.Unmarshal([]byte(stored.Value), &decoded) == nil && decoded.Current.Validate() == nil {
			state = decoded
		}
	}
	return key, state
}

func (s *Server) publicSiteSettings(node models.Node) website.Settings {
	if node.SelfStealDomain == "" {
		return website.Settings{}
	}
	_, state := s.publicSiteFor(node.IP)
	return state.Current
}

func (s *Server) getPublicSite(c echo.Context) error {
	_, state := s.publicSiteFor(c.QueryParam("node"))
	return c.JSON(http.StatusOK, state)
}

func (s *Server) previewPublicSite(c echo.Context) error {
	var page website.Settings
	if err := c.Bind(&page); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	if err := page.Validate(); err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	if page.Archive != "" {
		files, err := website.ReadArchive(page.Archive)
		if err != nil {
			return jsonError(c, http.StatusBadRequest, err.Error())
		}
		token := models.NewSubToken()
		s.siteFilesMu.Lock()
		s.archivePreview = siteArchivePreview{token: token, expires: s.now().Add(5 * time.Minute), files: files}
		s.siteFilesMu.Unlock()
		c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
		return c.JSON(http.StatusOK, map[string]string{"url": s.adminAPI() + "/site-preview/" + token + "/"})
	}
	c.Response().Header().Set("Content-Security-Policy", website.Policy)
	return c.HTML(http.StatusOK, website.Preview(page))
}

type siteArchivePreview struct {
	token   string
	expires time.Time
	files   website.Files
}

func (s *Server) previewSiteFile(c echo.Context) error {
	s.siteFilesMu.Lock()
	preview := s.archivePreview
	if !s.now().Before(preview.expires) {
		s.archivePreview = siteArchivePreview{}
		preview = siteArchivePreview{}
	}
	s.siteFilesMu.Unlock()
	if preview.token == "" || preview.token != c.Param("token") {
		return notFound(c)
	}
	return serveSiteFile(c, preview.files, c.Param("*"))
}

func (s *Server) publishedArchive(encoded string) website.Files {
	s.siteFilesMu.Lock()
	defer s.siteFilesMu.Unlock()
	if s.cachedSiteArchive != encoded {
		s.cachedSiteArchive = encoded
		s.cachedSiteFiles, _ = website.ReadArchive(encoded)
	}
	return s.cachedSiteFiles
}

func serveSiteFile(c echo.Context, files website.Files, name string) error {
	h := c.Response().Header()
	h.Set("Content-Security-Policy", website.ArchivePolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set(echo.HeaderCacheControl, "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	if data, mime, ok := files.Lookup(name); ok {
		return c.Blob(http.StatusOK, mime, data)
	}
	if data, mime, ok := files.Lookup("404.html"); ok {
		return c.Blob(http.StatusNotFound, mime, data)
	}
	return notFound(c)
}

func (s *Server) savePublicSite(c echo.Context) error {
	ip := c.QueryParam("node")
	if _, busy := s.selfStealJobs.LoadOrStore(ip, struct{}{}); busy {
		return jsonError(c, http.StatusConflict, "Сайт уже обновляется")
	}
	defer s.selfStealJobs.Delete(ip)
	key, state := s.publicSiteFor(ip)

	var input struct {
		Page     website.Settings `json:"page"`
		Rollback bool             `json:"rollback"`
	}
	if err := c.Bind(&input); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	if input.Rollback {
		if state.Previous == nil {
			return jsonError(c, http.StatusConflict, "Предыдущей версии нет")
		}
		input.Page = *state.Previous
	}
	if err := input.Page.Validate(); err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	if _, err := website.ReadArchive(input.Page.Archive); err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	if ip != "" {
		var node models.Node
		if err := s.db.First(&node, "ip = ?", ip).Error; err != nil {
			return jsonError(c, http.StatusNotFound, "Нода не найдена")
		}
		if node.SelfStealDomain == "" || node.IsLocalBridge() || !hasPanelKey(node) {
			return jsonError(c, http.StatusBadRequest, "Нужна удалённая нода со своим сайтом и SSH-ключом панели")
		}
		route, err := s.nodeSSHRoute(node.IP, node.SSHPort, node.SSHViaBridge)
		if err != nil {
			return jsonError(c, http.StatusBadRequest, err.Error())
		}
		if _, _, err := s.sshKey.load(); err != nil {
			return jsonError(c, http.StatusInternalServerError, "Ключ панели: "+err.Error())
		}
		err = s.provisionSelfSteal(context.WithoutCancel(c.Request().Context()), ip, node.SelfStealDomain,
			node.CDNDomain, input.Page, s.sshKey.path, route)
		if err != nil {
			return jsonError(c, http.StatusBadGateway, "Не удалось обновить сайт: "+err.Error())
		}
	}
	if input.Page != state.Current {
		old := state.Current
		state = publicSiteState{Current: input.Page, Previous: &old}
	}
	raw, _ := json.Marshal(state)
	if err := s.db.Save(&models.Setting{Key: key, Value: string(raw)}).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return c.JSON(http.StatusOK, state)
}
