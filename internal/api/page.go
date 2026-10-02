package api

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
)

// Платформы на странице подписки, в порядке вкладок
var Platforms = []string{"ios", "android", "windows", "macos", "linux"}

// AppClient — VPN-приложение на странице подписки
type AppClient struct {
	Name string `json:"name"`
	// Deeplink — шаблон ссылки «добавить в приложение»: {url} — ссылка подписки как есть,
	// {url_enc} — она же в URL-кодировке, {name} — название профиля в URL-кодировке.
	// Пусто — только кнопка «Скачать».
	Deeplink string `json:"deeplink"`
	// Downloads — где скачать, по платформам. Нет платформы — клиент на ней не показывается.
	Downloads map[string]string `json:"downloads"`
}

// PageSettings — страница подписки и то, что видят клиенты
type PageSettings struct {
	Title      string      `json:"title"`       // название профиля в приложении и заголовок страницы
	SupportURL string      `json:"support_url"` // куда писать за помощью (https:// или tg://), необязательно
	Clients    []AppClient `json:"clients"`
}

const pageKey = "page"

// DefaultClients — проверенные клиенты с deeplink-импортом подписки
func DefaultClients() []AppClient {
	return []AppClient{
		{
			Name:     "Karing",
			Deeplink: "karing://install-config?url={url_enc}&name={name}",
			Downloads: map[string]string{
				"ios":     "https://apps.apple.com/app/karing/id6472431552",
				"android": "https://github.com/KaringX/karing/releases/latest",
				"windows": "https://github.com/KaringX/karing/releases/latest",
				"macos":   "https://github.com/KaringX/karing/releases/latest",
				"linux":   "https://github.com/KaringX/karing/releases/latest",
			},
		},
		{
			Name:     "Happ",
			Deeplink: "happ://add/{url}",
			Downloads: map[string]string{
				"ios":     "https://apps.apple.com/app/happ-proxy-utility/id6504287215",
				"android": "https://play.google.com/store/apps/details?id=com.happproxy",
				"windows": "https://github.com/Happ-proxy/happ-desktop/releases/latest",
				"macos":   "https://github.com/Happ-proxy/happ-desktop/releases/latest",
				"linux":   "https://github.com/Happ-proxy/happ-desktop/releases/latest",
			},
		},
		{
			Name:     "Hiddify",
			Deeplink: "hiddify://import/{url}#{name}",
			Downloads: map[string]string{
				"ios":     "https://apps.apple.com/app/hiddify-proxy-vpn/id6596777532",
				"android": "https://play.google.com/store/apps/details?id=app.hiddify.com",
				"windows": "https://github.com/hiddify/hiddify-app/releases/latest",
				"macos":   "https://github.com/hiddify/hiddify-app/releases/latest",
				"linux":   "https://github.com/hiddify/hiddify-app/releases/latest",
			},
		},
	}
}

func defaultPage() PageSettings {
	return PageSettings{Title: "KVN", Clients: DefaultClients()}
}

func (s *Server) loadPage() PageSettings {
	p := defaultPage()
	var st models.Setting
	if err := s.db.First(&st, "key = ?", pageKey).Error; err == nil {
		p.Clients = nil // сохранённый список заменяет встроенный целиком
		_ = json.Unmarshal([]byte(st.Value), &p)
	}
	return p
}

var (
	schemeRe = regexp.MustCompile(`^([a-z][a-z0-9+.-]{1,31})://`)
	// Схемы, через которые можно выполнить код в браузере
	badSchemes = map[string]bool{"javascript": true, "data": true, "vbscript": true, "file": true, "blob": true}
)

func isHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

func validDeeplink(raw string) bool {
	m := schemeRe.FindStringSubmatch(raw)
	return m != nil && !badSchemes[m[1]] && m[1] != "http" && m[1] != "https" && len(raw) <= 300
}

func validatePage(p *PageSettings) error {
	p.Title = strings.TrimSpace(p.Title)
	if p.Title == "" || len([]rune(p.Title)) > 32 {
		return fmt.Errorf("Название: от 1 до 32 символов")
	}
	p.SupportURL = strings.TrimSpace(p.SupportURL)
	if p.SupportURL != "" && !isHTTPS(p.SupportURL) && !strings.HasPrefix(p.SupportURL, "tg://") {
		return fmt.Errorf("Ссылка на поддержку должна начинаться с https:// или tg://")
	}
	if len(p.Clients) > 20 {
		return fmt.Errorf("Не больше 20 приложений")
	}
	known := map[string]bool{}
	for _, pl := range Platforms {
		known[pl] = true
	}
	for i := range p.Clients {
		c := &p.Clients[i]
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" || len([]rune(c.Name)) > 40 {
			return fmt.Errorf("Название приложения: от 1 до 40 символов")
		}
		c.Deeplink = strings.TrimSpace(c.Deeplink)
		if c.Deeplink != "" && !validDeeplink(c.Deeplink) {
			return fmt.Errorf("%s: deeplink должен иметь вид схема://… (не http, javascript, data)", c.Name)
		}
		downloads := map[string]string{}
		for pl, link := range c.Downloads {
			link = strings.TrimSpace(link)
			if link == "" {
				continue
			}
			if !known[pl] {
				return fmt.Errorf("%s: неизвестная платформа %q", c.Name, pl)
			}
			if !isHTTPS(link) {
				return fmt.Errorf("%s: ссылка на скачивание должна начинаться с https://", c.Name)
			}
			downloads[pl] = link
		}
		c.Downloads = downloads
	}
	if p.Clients == nil {
		p.Clients = []AppClient{}
	}
	return nil
}

func (s *Server) getPageSettings(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"page":      s.loadPage(),
		"defaults":  defaultPage(),
		"platforms": Platforms,
	})
}

func (s *Server) savePageSettings(c echo.Context) error {
	var p PageSettings
	if err := c.Bind(&p); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	if err := validatePage(&p); err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	raw, _ := json.Marshal(p)
	if err := s.db.Save(&models.Setting{Key: pageKey, Value: string(raw)}).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return c.JSON(http.StatusOK, p)
}

// expandDeeplink подставляет ссылку подписки в шаблон приложения
func expandDeeplink(tmpl, subURL, title string) string {
	return strings.NewReplacer(
		"{url_enc}", url.QueryEscape(subURL),
		"{url}", subURL,
		"{name}", url.QueryEscape(title),
	).Replace(tmpl)
}

// detectPlatform — платформа по User-Agent, чтобы сразу открыть нужную вкладку
func detectPlatform(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"), strings.Contains(ua, "iPod"):
		return "ios"
	case strings.Contains(ua, "Android"):
		return "android"
	case strings.Contains(ua, "Windows"):
		return "windows"
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS X"):
		return "macos"
	case strings.Contains(ua, "Linux"), strings.Contains(ua, "X11"):
		return "linux"
	}
	return "ios"
}

// pageApp — приложение на вкладке платформы. Ссылки уже проверены при сохранении,
// поэтому помечаем их как безопасные: иначе html/template заменит нестандартные схемы на #ZgotmplZ.
type pageApp struct {
	Name     string
	Download template.URL
	Deeplink template.URL
}

type pagePlatform struct {
	ID   string
	Apps []pageApp
}

func pagePlatforms(p PageSettings, subURL string) []pagePlatform {
	var out []pagePlatform
	for _, pl := range Platforms {
		var apps []pageApp
		for _, c := range p.Clients {
			link, ok := c.Downloads[pl]
			if !ok || !isHTTPS(link) {
				continue
			}
			app := pageApp{Name: c.Name, Download: template.URL(link)}
			if validDeeplink(c.Deeplink) {
				app.Deeplink = template.URL(expandDeeplink(c.Deeplink, subURL, p.Title))
			}
			apps = append(apps, app)
		}
		if len(apps) > 0 {
			out = append(out, pagePlatform{ID: pl, Apps: apps})
		}
	}
	return out
}
