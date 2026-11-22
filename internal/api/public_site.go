package api

import (
	"context"
	"encoding/json"
	"net/http"

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

func (s *Server) publicSiteHTML(node models.Node) string {
	if node.SelfStealDomain == "" {
		return ""
	}
	_, state := s.publicSiteFor(node.IP)
	return website.Render(state.Current)
}

func (s *Server) getPublicSite(c echo.Context) error {
	_, state := s.publicSiteFor(c.QueryParam("node"))
	return c.JSON(http.StatusOK, state)
}

func previewPublicSite(c echo.Context) error {
	var page website.Settings
	if err := c.Bind(&page); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	if err := page.Validate(); err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	c.Response().Header().Set("Content-Security-Policy", website.Policy)
	return c.HTML(http.StatusOK, website.Preview(page))
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
			node.CDNDomain, website.Render(input.Page), s.sshKey.path, route)
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
