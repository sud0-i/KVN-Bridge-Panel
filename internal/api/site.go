package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
)

// Секретный путь панели: /<AdminPath>/ — и интерфейс, и его API.
// Всё остальное снаружи выглядит как обычный сайт.
var adminPathRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// NormalizeAdminPath превращает «abc», «/abc/» в «/abc». Пусто — панель в корне, как раньше.
func NormalizeAdminPath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", nil
	}
	if !adminPathRe.MatchString(p) {
		return "", fmt.Errorf("ADMIN_PATH: 8–64 символа, латиница, цифры, - и _")
	}
	return "/" + p, nil
}

// stubPage — заглушка вместо панели. Своя — положите сайт в SiteDir (index.html и всё нужное).
const stubPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Coming soon</title>
<style>
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#fafafa;color:#333;font-family:Georgia,serif}
main{text-align:center;padding:24px}
h1{font-weight:normal;font-size:32px;margin:0 0 8px}
p{color:#888;margin:0}
</style>
</head>
<body><main><h1>Coming soon</h1><p>This site is under construction.</p></main></body>
</html>`

const notFoundPage = `<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>404 Not Found</title></head>
<body><h1>Not Found</h1><p>The requested URL was not found on this server.</p></body></html>`

// headAsGet отвечает на HEAD так же, как на GET, только без тела — как обычный веб-сервер.
// Karing, например, перед обновлением подписки шлёт HEAD и на 404/405 сдаётся.
func headAsGet(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if c.Request().Method == http.MethodHead {
			c.Request().Method = http.MethodGet
			c.Response().Writer = discardBody{c.Response().Writer}
		}
		return next(c)
	}
}

// discardBody пропускает заголовки, но выбрасывает тело ответа
type discardBody struct{ http.ResponseWriter }

func (d discardBody) Write(p []byte) (int, error) { return len(p), nil }

// Боты, которые скачивают ссылку для превью в мессенджерах, и поисковики.
// Подписку им не отдаём: иначе конфиги пользователя осядут на их серверах.
var previewBots = []string{
	"telegrambot", "whatsapp", "facebookexternalhit", "facebot", "slackbot", "discordbot",
	"twitterbot", "linkedinbot", "vkshare", "skypeuripreview", "viber", "googlebot", "bingbot",
	"yandexbot", "applebot", "duckduckbot", "petalbot", "bot.html",
}

func isPreviewBot(ua string) bool {
	ua = strings.ToLower(ua)
	for _, b := range previewBots {
		if strings.Contains(ua, b) {
			return true
		}
	}
	return false
}

func notFound(c echo.Context) error {
	return c.HTML(http.StatusNotFound, notFoundPage)
}

// adminAPI — префикс API панели
func (s *Server) adminAPI() string {
	return s.cfg.AdminPath + "/api"
}

// registerSite вешает интерфейс панели, заглушку и обработчик ошибок
func (s *Server) registerSite(e *echo.Echo) {
	e.Pre(headAsGet)
	defaultHandler := e.DefaultHTTPErrorHandler
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		// API панели отвечает как раньше (JSON), остальное — как обычный веб-сервер
		if strings.HasPrefix(c.Request().URL.Path, s.adminAPI()+"/") {
			defaultHandler(err, c)
			return
		}
		if c.Response().Committed {
			return
		}
		code := http.StatusInternalServerError
		var he *echo.HTTPError
		if errors.As(err, &he) {
			code = he.Code
		}
		if code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
			_ = notFound(c)
			return
		}
		_ = c.HTML(code, "<h1>"+http.StatusText(code)+"</h1>")
	}

	if s.cfg.FrontendDir == "" {
		return
	}
	if s.cfg.AdminPath == "" {
		e.Static("/", s.cfg.FrontendDir)
		return
	}

	// Без слеша относительные пути в панели ломаются
	e.GET(s.cfg.AdminPath, func(c echo.Context) error {
		return c.Redirect(http.StatusFound, s.cfg.AdminPath+"/")
	})
	e.Group(s.cfg.AdminPath).Static("/", s.cfg.FrontendDir)

	site := s.cfg.SiteDir
	if site != "" {
		if _, err := os.Stat(filepath.Join(site, "index.html")); err != nil {
			site = ""
		}
	}
	if site != "" {
		e.Static("/", site)
		return
	}
	e.GET("/", func(c echo.Context) error { return c.HTML(http.StatusOK, stubPage) })
}
