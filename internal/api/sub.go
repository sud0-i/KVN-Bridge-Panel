package api

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/skip2/go-qrcode"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/xray"
)

// SubUpdateHours — как часто (в часах) клиенту обновлять подписку
const SubUpdateHours = 12

// subServer — один сервер в подписке: нода, отображаемое имя и транспорт
type subServer struct {
	Node      models.Node
	Name      string
	XHTTPPath string // пусто — основной вариант (TCP + Vision)
	FP        string // отпечаток TLS для клиента
	// Hy2 — сервер Hysteria2 (UDP 443) вместо VLESS; Obfs — пароль salamander (пусто — без маскировки)
	Hy2  bool
	Obfs string
	// CDN — через CDN-домен ноды (XHTTP поверх TLS на CDNPort) вместо прямого подключения
	CDN bool
	// MieruPorts — сервер mieru на этих портах
	MieruPorts string
}

func nodeAddress(n models.Node) string {
	if n.Domain != "" {
		return n.Domain
	}
	return n.IP
}

// serverName — подпись ноды, если задана («🇳🇱 Амстердам»), иначе «KVN <адрес>»
func serverName(n models.Node, suffix string) string {
	if n.Label != "" {
		return n.Label + suffix
	}
	return "KVN" + suffix + " " + nodeAddress(n)
}

func (srv subServer) fingerprint() string {
	if srv.FP == "" {
		return xray.DefaultFingerprint
	}
	return srv.FP
}

// vlessLink собирает ссылку VLESS + Reality — ровно под конфиг, который генерирует агент:
// TCP с XTLS Vision или, если задан путь, запасной транспорт XHTTP на том же порту
func vlessLink(userID string, srv subServer) string {
	n := srv.Node
	if srv.CDN {
		return cdnLink(userID, srv)
	}
	host := nodeAddress(n)
	if strings.Contains(host, ":") { // IPv6
		host = "[" + host + "]"
	}

	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("security", "reality")
	q.Set("sni", n.SNI)
	q.Set("fp", srv.fingerprint())
	q.Set("pbk", n.PubKey)
	q.Set("sid", n.SID)
	if srv.XHTTPPath != "" {
		q.Set("type", "xhttp")
		q.Set("path", srv.XHTTPPath)
		q.Set("mode", "auto")
	} else {
		q.Set("type", "tcp")
		q.Set("flow", xray.Flow)
	}

	return "vless://" + userID + "@" + host + ":443?" + q.Encode() + "#" + url.PathEscape(srv.Name)
}

// cdnLink — VLESS через CDN: обычный TLS до CDN (сертификат CDN настоящий),
// XHTTP в режиме packet-up — его пропускает любой CDN
func cdnLink(userID string, srv subServer) string {
	n := srv.Node
	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("security", "tls")
	q.Set("sni", n.CDNDomain)
	q.Set("fp", srv.fingerprint())
	q.Set("alpn", "h2,http/1.1")
	q.Set("type", "xhttp")
	q.Set("host", n.CDNDomain)
	q.Set("path", srv.XHTTPPath)
	q.Set("mode", "packet-up")
	return fmt.Sprintf("vless://%s@%s:%d?%s#%s", userID, n.CDNDomain, protocol.CDNPort, q.Encode(), url.PathEscape(srv.Name))
}

// mieruLink — простая ссылка mieru (mierus://): её понимают mihomo, Throne и клиент mieru.
// Порты и протоколы — парами: port=40100-40109&protocol=TCP.
func mieruLink(userID string, srv subServer) string {
	q := url.Values{}
	q.Set("profile", "default")
	q.Set("port", srv.MieruPorts)
	q.Set("protocol", "TCP")
	q.Set("multiplexing", "MULTIPLEXING_LOW")
	u := url.URL{Scheme: "mierus", User: url.UserPassword(userID, userID), Host: nodeAddress(srv.Node), RawQuery: q.Encode(), Fragment: srv.Name}
	return u.String()
}

// hy2Link — ссылка hysteria2:// в стандартном формате (Hiddify, v2rayNG, Happ, mihomo…).
// Сертификат самоподписанный: insecure=1 отключает проверку по CA, а pinSHA256 — проверяет
// именно наш сертификат, так что подменить сервер нельзя.
func hy2Link(userID string, srv subServer) string {
	host := nodeAddress(srv.Node)
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	q := url.Values{}
	q.Set("sni", hy2ServerName(srv.Node.Hy2Cert))
	q.Set("insecure", "1")
	q.Set("pinSHA256", hy2Pin(srv.Node.Hy2Cert))
	if srv.Obfs != "" {
		q.Set("obfs", "salamander")
		q.Set("obfs-password", srv.Obfs)
	}
	return fmt.Sprintf("hysteria2://%s@%s:%d/?%s#%s", url.PathEscape(userID), host, protocol.HysteriaPort, q.Encode(), url.PathEscape(srv.Name))
}

// setSubHeaders — заголовки, которые понимают клиенты (Hiddify, v2rayNG, Streisand, Happ…):
// название профиля, интервал обновления, трафик и срок действия
func setSubHeaders(c echo.Context, u models.User, title string) {
	h := c.Response().Header()
	h.Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(title)))
	h.Set("Profile-Update-Interval", strconv.Itoa(SubUpdateHours))
	var expire int64
	if u.ExpiresAt != nil {
		expire = u.ExpiresAt.Unix()
	}
	h.Set("Subscription-Userinfo", fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d",
		u.TrafficUp, u.TrafficDown, u.TrafficQuota, expire))
}

// subServers — серверы через мосты и, если включено, прямые на выходные ноды.
// Для каждой ноды основной вариант (TCP + Vision) и запасной (XHTTP), если он включён.
func (s *Server) subServers() ([]subServer, error) {
	bridges, err := s.aliveNodes(protocol.RoleBridge)
	if err != nil {
		return nil, err
	}
	routing := s.loadRouting()
	var out []subServer
	taken := map[string]int{}
	push := func(v subServer) {
		// Одинаковые подписи у разных нод: клиентам (и тегам sing-box) нужны уникальные имена
		if taken[v.Name]++; taken[v.Name] > 1 {
			v.Name = fmt.Sprintf("%s %d", v.Name, taken[v.Name])
		}
		out = append(out, v)
	}
	// Через CDN — для любой ноды с CDN-доменом: IP ноды клиент не видит
	addCDN := func(n models.Node) {
		if routing.CDN && n.CDNDomain != "" && n.XHTTPPath != "" {
			push(subServer{Node: n, Name: serverName(n, " CDN"), XHTTPPath: n.XHTTPPath, FP: routing.fingerprint(), CDN: true})
		}
	}
	// mieru — у нод, где mita уже работает (агент сообщил версию)
	addMieru := func(n models.Node, suffix string) {
		if routing.Mieru && n.MieruVersion != "" {
			push(subServer{Node: n, Name: serverName(n, suffix+" mieru"), MieruPorts: routing.MieruPorts})
		}
	}
	add := func(n models.Node, suffix string) {
		fp := routing.fingerprint()
		variants := []subServer{{Node: n, Name: serverName(n, suffix), FP: fp}}
		if routing.XHTTP && n.XHTTPPath != "" {
			variants = append(variants, subServer{Node: n, Name: serverName(n, suffix+" XHTTP"), XHTTPPath: n.XHTTPPath, FP: fp})
		}
		// Hysteria2 — только когда нода уже прислала свой сертификат (иначе клиенту нечем её проверить)
		if h := routing.hysteria(); h != nil && n.Hy2Cert != "" && hy2ServerName(n.Hy2Cert) != "" {
			variants = append(variants, subServer{Node: n, Name: serverName(n, suffix+" Hy2"), Hy2: true, Obfs: h.Obfs})
		}
		for _, v := range variants {
			push(v)
		}
	}
	for _, b := range bridges {
		add(b, "")
		addMieru(b, "")
		addCDN(b)
	}
	if routing.DirectExitLinks || routing.CDN {
		exits, err := s.aliveNodes(protocol.RoleExit)
		if err != nil {
			return nil, err
		}
		for _, e := range exits {
			if routing.DirectExitLinks {
				add(e, " direct")
				addMieru(e, " direct")
			}
			addCDN(e)
		}
	}
	return out, nil
}

// subscriptionLinks — ссылки vless:// для base64-подписки и страницы
func (s *Server) subscriptionLinks(userID string) ([]string, error) {
	servers, err := s.subServers()
	if err != nil {
		return nil, err
	}
	links := make([]string, 0, len(servers))
	for _, srv := range servers {
		if srv.MieruPorts != "" {
			links = append(links, mieruLink(userID, srv))
			continue
		}
		if srv.Hy2 {
			links = append(links, hy2Link(userID, srv))
			continue
		}
		links = append(links, vlessLink(userID, srv))
	}
	return links, nil
}

func (s *Server) subscription(c echo.Context) error {
	lang := pickLang(c.Request().Header.Get("Accept-Language"), c.QueryParam("lang"))
	tr := subTexts[lang]

	if isPreviewBot(c.Request().Header.Get("User-Agent")) {
		return notFound(c)
	}

	// Сначала по токену подписки; по UUID — чтобы работали ссылки, выданные до появления токенов
	var user models.User
	token := c.Param("token")
	if err := s.db.Where("sub_token = ?", token).First(&user).Error; err != nil {
		if err := s.db.Where("id = ?", token).First(&user).Error; err != nil {
			return notFound(c) // не подсказываем, что здесь раздают подписки
		}
	}
	page := s.loadPage()
	setSubHeaders(c, user, page.Title)

	// Кто пришёл: браузер или VPN-клиент. Некоторые клиенты пишут в User-Agent «Mozilla»,
	// но text/html просят только браузеры.
	req := c.Request()
	isBrowser := strings.Contains(req.Header.Get("User-Agent"), "Mozilla") &&
		strings.Contains(req.Header.Get("Accept"), "text/html") && c.QueryParam("format") != "raw"

	active := user.CanConnect(s.now())
	if !isBrowser {
		if !active {
			return c.String(http.StatusForbidden, tr["inactive"])
		}
		ua := req.Header.Get("User-Agent")
		if subFormat(ua, c.QueryParam("format")) == "singbox" {
			servers, err := s.subServers()
			if err != nil {
				return c.String(http.StatusInternalServerError, tr["error"])
			}
			cfg, err := singboxConfig(user.ID, page.Title, servers, supportsXHTTP(ua))
			if err != nil {
				return c.String(http.StatusInternalServerError, tr["error"])
			}
			return c.JSONBlob(http.StatusOK, cfg)
		}
		links, err := s.subscriptionLinks(user.ID)
		if err != nil {
			return c.String(http.StatusInternalServerError, tr["error"])
		}
		return c.String(http.StatusOK, base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n"))))
	}

	var links []string
	if active {
		var err error
		if links, err = s.subscriptionLinks(user.ID); err != nil {
			return c.HTML(http.StatusInternalServerError, "<h1>"+tr["error"]+"</h1>")
		}
	}
	subURL := c.Scheme() + "://" + req.Host + req.URL.Path
	qr, err := qrcode.Encode(subURL, qrcode.Medium, 256)
	if err != nil {
		return c.HTML(http.StatusInternalServerError, "<h1>"+tr["error"]+"</h1>")
	}

	// Вкладка платформы посетителя; если для неё приложений нет — первая доступная
	platforms := pagePlatforms(page, subURL)
	current := detectPlatform(req.Header.Get("User-Agent"))
	if len(platforms) > 0 && !slices.ContainsFunc(platforms, func(p pagePlatform) bool { return p.ID == current }) {
		current = platforms[0].ID
	}

	used := user.TrafficUp + user.TrafficDown
	data := map[string]any{
		"T":         tr,
		"Title":     page.Title,
		"Name":      user.Name,
		"Active":    active,
		"Reason":    tr[inactiveReason(user, s.now())],
		"Used":      fmt.Sprintf("%.2f", float64(used)/gigabyte),
		"Quota":     "",
		"Expires":   "",
		"Links":     strings.Join(links, "\n"),
		"HasAny":    len(links) > 0,
		"SubURL":    subURL,
		"QR":        template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(qr)),
		"Platforms": platforms,
		"Current":   current,
		"Support":   supportURL(page.SupportURL),
	}
	if user.TrafficQuota > 0 {
		data["Quota"] = fmt.Sprintf("%.2f", float64(user.TrafficQuota)/gigabyte)
	}
	if user.ExpiresAt != nil {
		data["Expires"] = user.ExpiresAt.Format("02.01.2006")
	}

	var buf bytes.Buffer
	if err := subTemplate.Execute(&buf, data); err != nil {
		return c.HTML(http.StatusInternalServerError, "<h1>"+tr["error"]+"</h1>")
	}
	status := http.StatusOK
	if !active {
		status = http.StatusForbidden
	}
	return c.HTMLBlob(status, buf.Bytes())
}

// inactiveReason — ключ текста с причиной, почему подписка не работает
func inactiveReason(u models.User, now time.Time) string {
	switch {
	case u.Status != "active":
		return "reasonBlocked"
	case u.ExpiresAt != nil && now.After(*u.ExpiresAt):
		return "reasonExpired"
	case u.TrafficQuota > 0 && u.TrafficUp+u.TrafficDown >= u.TrafficQuota:
		return "reasonQuota"
	}
	return ""
}

// supportURL — ссылка на поддержку (проверена при сохранении: https:// или tg://)
func supportURL(raw string) template.URL {
	if isHTTPS(raw) || strings.HasPrefix(raw, "tg://") {
		return template.URL(raw)
	}
	return ""
}

// pickLang — "ru", если браузер его предпочитает (или явно ?lang=ru), иначе "en"
func pickLang(acceptLanguage, override string) string {
	if override == "ru" || override == "en" {
		return override
	}
	for _, part := range strings.Split(acceptLanguage, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		if strings.HasPrefix(tag, "ru") {
			return "ru"
		}
		if strings.HasPrefix(tag, "en") {
			return "en"
		}
	}
	return "en"
}

var subTexts = map[string]map[string]string{
	"en": {
		"lang":          "en",
		"notFound":      "User not found or deleted",
		"inactive":      "Subscription is inactive",
		"error":         "Server error",
		"hello":         "Hi, ",
		"intro":         "This is your personal VPN page.",
		"traffic":       "Traffic",
		"of":            "of",
		"unlimited":     "no limit",
		"expires":       "Valid until",
		"noExpiry":      "no end date",
		"reasonBlocked": "Access is suspended. Contact the administrator.",
		"reasonExpired": "The subscription has expired. Contact the administrator to extend it.",
		"reasonQuota":   "The traffic limit is used up. Contact the administrator.",
		"step1":         "1. Install an app",
		"step2":         "2. Add the subscription",
		"step2Hint":     "Tap “Add” next to the app you installed. If nothing happens, copy the link and add it in the app manually.",
		"step3":         "3. Connect",
		"step3Hint":     "Turn on the connection in the app. The server list updates by itself.",
		"download":      "Download",
		"add":           "Add",
		"copySub":       "Copy subscription link",
		"copied":        "Copied!",
		"qr":            "Or scan the QR code in the app on another device",
		"manual":        "Manual setup",
		"copyConfig":    "Copy configuration",
		"noServers":     "Servers are unavailable right now, try again later.",
		"support":       "Need help? Write to us",
		"direct":        "“direct” servers connect without the bridge: lower ping, but may be less stable on some networks.",
		"ios":           "iPhone / iPad",
		"android":       "Android",
		"windows":       "Windows",
		"macos":         "macOS",
		"linux":         "Linux",
	},
	"ru": {
		"lang":          "ru",
		"notFound":      "Пользователь не найден или удалён",
		"inactive":      "Подписка неактивна",
		"error":         "Ошибка сервера",
		"hello":         "Привет, ",
		"intro":         "Это твоя персональная страница VPN.",
		"traffic":       "Трафик",
		"of":            "из",
		"unlimited":     "без лимита",
		"expires":       "Действует до",
		"noExpiry":      "бессрочно",
		"reasonBlocked": "Доступ приостановлен. Напиши администратору.",
		"reasonExpired": "Срок подписки закончился. Напиши администратору, чтобы продлить.",
		"reasonQuota":   "Лимит трафика исчерпан. Напиши администратору.",
		"step1":         "1. Установи приложение",
		"step2":         "2. Добавь подписку",
		"step2Hint":     "Нажми «Добавить» рядом с установленным приложением. Если ничего не произошло — скопируй ссылку и добавь её в приложении вручную.",
		"step3":         "3. Подключись",
		"step3Hint":     "Включи подключение в приложении. Список серверов обновляется сам.",
		"download":      "Скачать",
		"add":           "Добавить",
		"copySub":       "Скопировать ссылку подписки",
		"copied":        "Скопировано!",
		"qr":            "Или отсканируй QR-код в приложении на другом устройстве",
		"manual":        "Ручная настройка",
		"copyConfig":    "Скопировать конфигурацию",
		"noServers":     "Серверы сейчас недоступны, попробуй позже.",
		"support":       "Нужна помощь? Напиши нам",
		"direct":        "Серверы «direct» подключаются без моста: пинг ниже, но в некоторых сетях может работать хуже.",
		"ios":           "iPhone / iPad",
		"android":       "Android",
		"windows":       "Windows",
		"macos":         "macOS",
		"linux":         "Linux",
	},
}

// html/template сам экранирует все подставляемые значения (защита от XSS).
// Внешних CDN нет: они могут не открываться у пользователя. QR рисуется на сервере.
var subTemplate = template.Must(template.New("sub").Parse(`<!DOCTYPE html>
<html lang="{{.T.lang}}">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta name="robots" content="noindex">
<title>{{.Title}}</title>
<style>
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:flex;justify-content:center;padding:16px;background:#111827;color:#f9fafb;font-family:system-ui,sans-serif}
.card{background:#1f2937;border:1px solid #374151;border-radius:16px;padding:24px;max-width:520px;width:100%}
h1{color:#60a5fa;font-size:24px;margin:0 0 4px;text-align:center}
h2{font-size:17px;margin:24px 0 8px}
p{color:#9ca3af;margin:4px 0}
.center{text-align:center}
.stats{display:flex;gap:8px;margin-top:16px}
.stat{flex:1;background:#111827;border:1px solid #374151;border-radius:12px;padding:10px;text-align:center}
.stat b{display:block;font-size:17px;color:#f9fafb}
.stat span{font-size:12px;color:#9ca3af}
.alert{background:#7f1d1d55;border:1px solid #b91c1c;color:#fecaca;border-radius:12px;padding:12px;margin-top:16px;text-align:center}
.tabs{display:flex;flex-wrap:wrap;gap:6px}
.tab{background:#111827;border:1px solid #374151;color:#d1d5db;border-radius:999px;padding:6px 12px;font-size:14px;cursor:pointer}
.tab.on{background:#2563eb;border-color:#2563eb;color:#fff}
.apps{display:none;margin-top:10px}
.apps.on{display:block}
.app{display:flex;align-items:center;gap:8px;background:#111827;border:1px solid #374151;border-radius:12px;padding:10px 12px;margin-top:8px}
.app b{flex:1}
a.btn,button{display:inline-block;background:#2563eb;color:#fff;border:0;border-radius:10px;padding:8px 14px;font-size:14px;font-weight:600;text-decoration:none;cursor:pointer}
a.btn.gray{background:#374151}
button.wide{width:100%;padding:12px;font-size:16px;margin-top:10px}
a.btn:hover,button:hover{filter:brightness(1.15)}
.qr{display:block;margin:12px auto 0;width:200px;height:200px;border-radius:12px;background:#fff}
details{margin-top:20px;color:#9ca3af}
summary{cursor:pointer}
pre{background:#111827;border:1px solid #374151;border-radius:12px;padding:12px;white-space:pre-wrap;word-break:break-all;font-size:11px;color:#d1d5db}
small{color:#6b7280;display:block;margin-top:6px}
a{color:#60a5fa}
</style>
</head>
<body>
<div class="card">
  <h1>{{.T.hello}}{{.Name}}!</h1>
  <p class="center">{{.T.intro}}</p>

  <div class="stats">
    <div class="stat"><span>{{.T.traffic}}</span><b>{{.Used}} GB</b><span>{{if .Quota}}{{.T.of}} {{.Quota}} GB{{else}}{{.T.unlimited}}{{end}}</span></div>
    <div class="stat"><span>{{.T.expires}}</span><b>{{if .Expires}}{{.Expires}}{{else}}∞{{end}}</b><span>{{if not .Expires}}{{.T.noExpiry}}{{end}}</span></div>
  </div>

  {{if not .Active}}
  <div class="alert">{{.Reason}}</div>
  {{else}}
  {{if .Platforms}}
  <h2>{{.T.step1}}</h2>
  <div class="tabs">
    {{range .Platforms}}<button type="button" class="tab{{if eq .ID $.Current}} on{{end}}" data-tab="{{.ID}}">{{index $.T .ID}}</button>{{end}}
  </div>
  {{range .Platforms}}
  <div class="apps{{if eq .ID $.Current}} on{{end}}" id="apps-{{.ID}}">
    {{range .Apps}}
    <div class="app"><b>{{.Name}}</b>
      <a class="btn gray" href="{{.Download}}" target="_blank" rel="noopener">{{$.T.download}}</a>
      {{if .Deeplink}}<a class="btn" href="{{.Deeplink}}">{{$.T.add}}</a>{{end}}
    </div>
    {{end}}
  </div>
  {{end}}
  <h2>{{.T.step2}}</h2>
  <p>{{.T.step2Hint}}</p>
  {{end}}
  <button type="button" class="wide" data-copy="{{.SubURL}}">{{.T.copySub}}</button>
  <img class="qr" src="{{.QR}}" alt="QR">
  <p class="center"><small>{{.T.qr}}</small></p>
  {{if .Platforms}}
  <h2>{{.T.step3}}</h2>
  <p>{{.T.step3Hint}}</p>
  {{end}}
  {{if not .HasAny}}<div class="alert">{{.T.noServers}}</div>{{end}}
  {{end}}

  {{if .Support}}<p class="center" style="margin-top:20px"><a href="{{.Support}}" target="_blank" rel="noopener">{{.T.support}}</a></p>{{end}}

  {{if .HasAny}}
  <details>
    <summary>{{.T.manual}}</summary>
    <pre id="links">{{.Links}}</pre>
    <button type="button" class="wide" data-copy-from="links">{{.T.copyConfig}}</button>
    <small>{{.T.direct}}</small>
  </details>
  {{end}}
</div>
<script>
document.querySelectorAll('[data-tab]').forEach(function (t) {
  t.addEventListener('click', function () {
    document.querySelectorAll('[data-tab]').forEach(function (x) { x.classList.toggle('on', x === t); });
    document.querySelectorAll('.apps').forEach(function (a) { a.classList.toggle('on', a.id === 'apps-' + t.dataset.tab); });
  });
});
document.querySelectorAll('[data-copy],[data-copy-from]').forEach(function (b) {
  b.addEventListener('click', function () {
    var text = b.dataset.copy || document.getElementById(b.dataset.copyFrom).textContent;
    var done = function () { b.textContent = {{.T.copied}}; };
    if (navigator.clipboard) { navigator.clipboard.writeText(text).then(done, function () { prompt('', text); }); }
    else { prompt('', text); }
  });
});
</script>
</body>
</html>`))

// subPreview — какие серверы получат пользователи при сохранённых настройках (для панели)
func (s *Server) subPreview(c echo.Context) error {
	servers, err := s.subServers()
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	type item struct {
		Name   string `json:"name"`
		Kind   string `json:"kind"` // vless | xhttp | hy2 | cdn
		Direct bool   `json:"direct"`
	}
	out := make([]item, 0, len(servers))
	for _, srv := range servers {
		kind := "vless"
		switch {
		case srv.Hy2:
			kind = "hy2"
		case srv.CDN:
			kind = "cdn"
		case srv.MieruPorts != "":
			kind = "mieru"
		case srv.XHTTPPath != "":
			kind = "xhttp"
		}
		out = append(out, item{Name: srv.Name, Kind: kind, Direct: srv.Node.Type == protocol.RoleExit})
	}
	return c.JSON(http.StatusOK, out)
}
