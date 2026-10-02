package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/xray"
)

// Куда отправлять трафик выбранной страны
const (
	RouteWarp   = "warp"   // выходная нода → Cloudflare WARP (IP ноды не светится)
	RouteBridge = "bridge" // напрямую с моста
	RouteExit   = "exit"   // обычным путём, с IP выходной ноды
)

// Regions — готовые наборы правил по странам
var Regions = map[string][]string{
	"ru": {"geosite:category-ru", "geoip:ru"},
	"ir": {"geosite:category-ir", "geoip:ir"},
	"cn": {"geosite:cn", "geoip:cn"},
}

// WarpTemplates — сервисы, которые часто не любят IP хостингов
var WarpTemplates = map[string][]string{
	"google":  {"geosite:google"},
	"openai":  {"geosite:openai"},
	"netflix": {"geosite:netflix"},
	"spotify": {"geosite:spotify"},
}

// Routing — настройки маршрутизации кластера, редактируются из панели
type Routing struct {
	Region      string `json:"region"`       // "", "ru", "ir", "cn"
	RegionRoute string `json:"region_route"` // RouteWarp / RouteBridge / RouteExit
	// BridgeDirect — свои правила «напрямую с моста»
	BridgeDirect []string `json:"bridge_direct"`
	// WarpRules — свои правила «через WARP на выходной ноде»
	WarpRules []string `json:"warp_rules"`
	// DirectExitLinks — добавлять в подписку прямые ссылки на выходные ноды
	DirectExitLinks bool `json:"direct_exit_links"`
	// XHTTP — запасной транспорт на том же порту 443: вход на нодах и ссылки в подписке
	XHTTP bool `json:"xhttp"`
	// Fingerprint — отпечаток TLS (uTLS) в подписках и на участке мост → выходная нода.
	// Когда DPI начинает душить один отпечаток (чаще всего chrome), помогает сменить его.
	Fingerprint string `json:"fingerprint"`
	// ExitLink — как мост подключается к выходным нодам: ExitLinkTCP или ExitLinkXHTTP
	ExitLink string `json:"exit_link"`
	// Hysteria — вход Hysteria2 (UDP 443) на всех нодах и серверы Hy2 в подписке
	Hysteria bool `json:"hysteria"`
	// HysteriaObfs — маскировка salamander: пакеты похожи на шум, а не на HTTP/3
	HysteriaObfs bool `json:"hysteria_obfs"`
	// HysteriaObfsPassword — общий пароль salamander; генерируется сам, в панели не правится
	HysteriaObfsPassword string `json:"-"`
	// XrayVersion — какая версия Xray должна стоять на нодах ("26.3.27"); пусто — не трогать
	XrayVersion string `json:"xray_version"`
	// GeoUpdate — ночью раз в сутки обновлять геобазы на нодах
	GeoUpdate bool `json:"geo_update"`
	// CDN — вход для CDN на нодах с CDN-доменом и варианты «через CDN» в подписке
	CDN bool `json:"cdn"`
	// Mieru — сервер mieru (mita) на нодах и серверы mieru в подписке для Karing
	Mieru bool `json:"mieru"`
	// MieruPorts — TCP-порты mieru: "40100-40109" (клиент прыгает по ним) или один порт
	MieruPorts string `json:"mieru_ports"`
}

// MieruVersion — версия mita, которую ставят ноды (обновляется вместе с панелью)
const MieruVersion = "3.38.0"

// DefaultMieruPorts — порты mieru по умолчанию
const DefaultMieruPorts = "40100-40109"

var mieruPortsRe = regexp.MustCompile(`^([0-9]{4,5})(?:-([0-9]{4,5}))?$`)

// validMieruPorts: порт или диапазон до 100 портов в 1025–65535, мимо занятых
func validMieruPorts(s string) bool {
	m := mieruPortsRe.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	from, _ := strconv.Atoi(m[1])
	to := from
	if m[2] != "" {
		to, _ = strconv.Atoi(m[2])
	}
	if from < 1025 || to > 65535 || to < from || to-from >= 100 {
		return false
	}
	for _, busy := range []int{protocol.CDNPort, xray.APIPort, protocol.MieruSocksPort, protocol.WarpProxyPort, 8080} {
		if busy >= from && busy <= to {
			return false
		}
	}
	return true
}

// routingStored — как настройки лежат в базе: вместе с паролем salamander, который
// не уходит в панель (json:"-" у Routing)
type routingStored struct {
	Routing
	HysteriaObfsPassword string `json:"hysteria_obfs_password,omitempty"`
}

// hysteria — настройки Hysteria2 для синхронизации нод (nil — выключено)
func (r Routing) hysteria() *protocol.Hysteria {
	if !r.Hysteria {
		return nil
	}
	h := &protocol.Hysteria{}
	if r.HysteriaObfs {
		h.Obfs = r.HysteriaObfsPassword
	}
	return h
}

// Транспорт связки мост → выходная нода
const (
	// ExitLinkTCP — TCP + XTLS Vision: новое соединение (TCP + Reality) на каждое соединение пользователя
	ExitLinkTCP = "tcp"
	// ExitLinkXHTTP — XHTTP: соединения к ноде переиспользуются, открытие сайтов быстрее
	// на время рукопожатия, а поток коротких TLS-соединений не бросается в глаза DPI
	ExitLinkXHTTP = "xhttp"
	// ExitLinkCDN — через CDN-домен экзита (если он задан), иначе как XHTTP
	ExitLinkCDN = "cdn"
)

// Fingerprints — отпечатки, которые понимают и Xray, и sing-box (Karing), и mihomo
var Fingerprints = []string{"chrome", "firefox", "safari", "ios", "android", "edge", "qq", "random"}

const routingKey = "routing"

func (s *Server) defaultRouting() Routing {
	return Routing{
		RegionRoute:     RouteWarp,
		BridgeDirect:    s.cfg.BridgeDirect,
		WarpRules:       []string{},
		DirectExitLinks: true,
		XHTTP:           true,
		Fingerprint:     xray.DefaultFingerprint,
		ExitLink:        ExitLinkTCP,
		GeoUpdate:       true,
		MieruPorts:      DefaultMieruPorts,
	}
}

func (s *Server) loadRouting() Routing {
	r := s.defaultRouting()
	var st models.Setting
	if err := s.db.First(&st, "key = ?", routingKey).Error; err == nil {
		stored := routingStored{Routing: r}
		if json.Unmarshal([]byte(st.Value), &stored) == nil {
			r = stored.Routing
			r.HysteriaObfsPassword = stored.HysteriaObfsPassword
		}
	}
	return r
}

// fingerprint — выбранный отпечаток TLS (для старых сохранённых настроек — по умолчанию)
func (r Routing) fingerprint() string {
	if slices.Contains(Fingerprints, r.Fingerprint) {
		return r.Fingerprint
	}
	return xray.DefaultFingerprint
}

// bridgeDirect — всё, что мост выпускает напрямую
func (r Routing) bridgeDirect() []string {
	out := append([]string{}, r.BridgeDirect...)
	if r.RegionRoute == RouteBridge {
		out = append(out, Regions[r.Region]...)
	}
	return out
}

// exitWarp — всё, что выходная нода выпускает через WARP
func (r Routing) exitWarp() []string {
	out := append([]string{}, r.WarpRules...)
	if r.RegionRoute == RouteWarp {
		out = append(out, Regions[r.Region]...)
	}
	return out
}

var ruleRe = regexp.MustCompile(`^(geosite|geoip|domain|full|keyword):[A-Za-z0-9._@!\-]+$`)

func validateRules(list []string) ([]string, error) {
	out := []string{}
	for _, v := range list {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if !ruleRe.MatchString(v) && net.ParseIP(v) == nil {
			if _, _, err := net.ParseCIDR(v); err != nil {
				return nil, fmt.Errorf("некорректное правило: %q", v)
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) getSettings(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{
		"routing":        s.loadRouting(),
		"regions":        Regions,
		"warp_templates": WarpTemplates,
		"fingerprints":   Fingerprints,
		"agent_sha":      s.agentBin.hash(), // сверяем с версиями агентов на нодах
	})
}

func (s *Server) saveSettings(c echo.Context) error {
	var r Routing
	if err := c.Bind(&r); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	if _, ok := Regions[r.Region]; !ok && r.Region != "" {
		return jsonError(c, http.StatusBadRequest, "Неизвестная страна")
	}
	switch r.RegionRoute {
	case RouteWarp, RouteBridge, RouteExit:
	default:
		return jsonError(c, http.StatusBadRequest, "Неизвестный маршрут")
	}
	if r.Fingerprint == "" {
		r.Fingerprint = xray.DefaultFingerprint
	}
	if !slices.Contains(Fingerprints, r.Fingerprint) {
		return jsonError(c, http.StatusBadRequest, "Неизвестный fingerprint")
	}
	var ok bool
	if r.XrayVersion, ok = normalizeXrayVersion(r.XrayVersion); !ok {
		return jsonError(c, http.StatusBadRequest, "Версия Xray в формате 26.3.27")
	}
	r.MieruPorts = strings.TrimSpace(r.MieruPorts)
	if r.MieruPorts == "" {
		r.MieruPorts = DefaultMieruPorts
	}
	if !validMieruPorts(r.MieruPorts) {
		return jsonError(c, http.StatusBadRequest, "Порты mieru: число или диапазон «40100-40109» (до 100 портов, 1025–65535)")
	}
	switch r.ExitLink {
	case "":
		r.ExitLink = ExitLinkTCP
	case ExitLinkTCP, ExitLinkXHTTP:
	case ExitLinkCDN:
		if !r.CDN {
			return jsonError(c, http.StatusBadRequest, "Связь через CDN: сначала включите CDN")
		}
	default:
		return jsonError(c, http.StatusBadRequest, "Связь с выходной нодой: tcp, xhttp или cdn")
	}
	var err error
	if r.BridgeDirect, err = validateRules(r.BridgeDirect); err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	if r.WarpRules, err = validateRules(r.WarpRules); err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}

	// Пароль salamander общий для всех нод и клиентов: сохраняем прежний, при первом сохранении создаём
	r.HysteriaObfsPassword = s.loadRouting().HysteriaObfsPassword
	if r.HysteriaObfsPassword == "" {
		pw, err := randomHex(16)
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, "Ошибка генерации пароля")
		}
		r.HysteriaObfsPassword = pw
	}
	raw, _ := json.Marshal(routingStored{Routing: r, HysteriaObfsPassword: r.HysteriaObfsPassword})
	if err := s.db.Save(&models.Setting{Key: routingKey, Value: string(raw)}).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return c.JSON(http.StatusOK, r)
}
