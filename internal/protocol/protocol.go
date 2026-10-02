// Package protocol описывает обмен данными между Мастером и Агентами.
// Структуры не привязаны к конкретному ядру (Xray / sing-box):
// агент сам превращает их в конфиг своего ядра.
package protocol

const (
	RoleBridge = "bridge" // точка входа: к ней подключаются пользователи
	RoleExit   = "exit"   // выходная нода: через неё мост выпускает трафик

	// NodeTokenHeader — заголовок, которым агент подтверждает, что он нода кластера
	NodeTokenHeader = "X-Node-Token"
	// WarpHeader — агент сообщает, доступен ли на ноде локальный прокси WARP ("1"/"0")
	WarpHeader = "X-Node-Warp"
	// ConfigErrorHeader — последняя ошибка применения конфига на ноде (пусто — всё хорошо)
	ConfigErrorHeader = "X-Node-Config-Error"
	// SNIErrorHeader — сайт для маскировки Reality недоступен с ноды (пусто — всё хорошо)
	SNIErrorHeader = "X-Node-SNI-Error"
	// WarpErrorHeader — почему WARP недоступен (пусто — работает или не нужен)
	WarpErrorHeader = "X-Node-Warp-Error"
	// HysteriaCertHeader — самоподписанный сертификат Hysteria2 ноды (PEM в base64).
	// Секрета в нём нет: по нему клиенты «пинят» сервер. Ключ остаётся на ноде.
	HysteriaCertHeader = "X-Node-Hy2-Cert"
	// AgentVersionHeader — SHA-256 бинарника агента (по нему видно, обновился ли он)
	AgentVersionHeader = "X-Node-Agent-Version"
	// XrayVersionHeader — установленная версия Xray, например "26.3.27"
	XrayVersionHeader = "X-Node-Xray-Version"
	// GeoUpdatedHeader — когда обновлялись геобазы (RFC 3339)
	GeoUpdatedHeader = "X-Node-Geo-Updated"
	// UpdateErrorHeader — последняя ошибка обновления агента, Xray или геобаз
	UpdateErrorHeader = "X-Node-Update-Error"
	// NotifyAckHeader — итоги отправки уведомлений из прошлой синхронизации (JSON []NotifyAck)
	NotifyAckHeader = "X-Node-Notify-Ack"
	// MieruHeader — версия работающего mita ("" — не запущен); MieruErrorHeader — почему
	MieruHeader      = "X-Node-Mieru"
	MieruErrorHeader = "X-Node-Mieru-Error"
	// LinksHeader — замеры канала до выходных нод с прошлой синхронизации (JSON []LinkSample)
	LinksHeader = "X-Node-Links"
	// MetricsHeader — показатели сервера (JSON NodeMetrics)
	MetricsHeader = "X-Node-Metrics"
	// ActionHeader — итог действия, выданного в прошлой синхронизации ("restart-xray: ok")
	ActionHeader = "X-Node-Action"
	// SSHHeader — состояние SSH на ноде: "keys" (ключ мастера стоит), "keys-only" (и пароль
	// выключен) или "error: …"
	SSHHeader = "X-Node-SSH"

	// WarpProxyPort — локальный SOCKS-порт клиента Cloudflare WARP на выходной ноде
	WarpProxyPort = 40000
)

// SyncResponse — всё, что нужно ноде, чтобы собрать конфиг
type SyncResponse struct {
	Role    string   `json:"role"`
	Reality Reality  `json:"reality"`
	Clients []Client `json:"clients"`
	Exits   []Exit   `json:"exits,omitempty"` // только для моста
	// Direct — что мост выпускает напрямую, минуя выходные ноды
	// (например "geosite:private", "domain:example.com", "geoip:xx", "10.0.0.0/8")
	Direct []string `json:"direct,omitempty"`
	// Warp — что выходная нода выпускает через Cloudflare WARP, а не со своего IP
	Warp []string `json:"warp,omitempty"`
	// XHTTPPath — путь запасного транспорта XHTTP на том же порту 443 (пусто — XHTTP выключен)
	XHTTPPath string `json:"xhttp_path,omitempty"`
	// Fingerprint — отпечаток TLS (uTLS), с которым мост подключается к выходным нодам.
	// Пусто — "chrome".
	Fingerprint string `json:"fingerprint,omitempty"`
	// Hysteria — вход Hysteria2 (UDP 443). nil — выключен.
	Hysteria *Hysteria `json:"hysteria,omitempty"`
	// CDN — вход для CDN (XHTTP поверх TLS на CDNPort): нода доступна через Cloudflare и т.п.
	CDN *CDN `json:"cdn,omitempty"`
	// Mieru — сервер mieru (mita) рядом с Xray: пользователи, порты, версия
	Mieru *Mieru `json:"mieru,omitempty"`
	// Maintenance — что агент должен поддерживать в актуальном состоянии
	Maintenance *Maintenance `json:"maintenance,omitempty"`
	// Notify — уведомления, которые выходная нода должна переслать в Telegram
	Notify *Notify `json:"notify,omitempty"`
	// Action — что сделать с сервером: ActionRestartXray, ActionRestartMieru, ActionRestartAgent, ActionReboot
	Action string `json:"action,omitempty"`
	// SSHKeys — ключи, которые агент держит в /root/.ssh/authorized_keys (ключ мастера и
	// личные ключи администратора); SSHKeysOnly — запретить вход по паролю
	SSHKeys     []string `json:"ssh_keys,omitempty"`
	SSHKeysOnly bool     `json:"ssh_keys_only,omitempty"`
}

// Maintenance — обновления на ноде
type Maintenance struct {
	// AgentSHA256 — SHA-256 бинарника агента, который раздаёт мастер (/api/agent/binary).
	// Отличается от своего — агент скачивает новый, сверяет хэш и перезапускается.
	AgentSHA256 string `json:"agent_sha256,omitempty"`
	// XrayVersion — какая версия Xray должна стоять ("26.3.27"). Пусто — не трогать.
	XrayVersion string `json:"xray_version,omitempty"`
	// GeoUpdate — раз в сутки (ночью) обновлять geoip.dat / geosite.dat
	GeoUpdate bool `json:"geo_update,omitempty"`
}

// Hysteria — настройки входа Hysteria2 на ноде
type Hysteria struct {
	// Obfs — пароль маскировки salamander (пусто — без маскировки, выглядит как HTTP/3)
	Obfs string `json:"obfs,omitempty"`
	// CertFile/KeyFile заполняет агент: сертификат и ключ лежат только на ноде
	CertFile string `json:"-"`
	KeyFile  string `json:"-"`
}

// HysteriaPort — Hysteria2 слушает UDP 443: снаружи похоже на HTTP/3 того же сайта
const HysteriaPort = 443

// Reality — параметры маскировки входящего подключения ноды.
// Приватный ключ хранится только на самой ноде и сюда не передаётся.
type Reality struct {
	SNI     string `json:"sni"`
	ShortID string `json:"short_id"`
	// Dest — куда отправлять подключения не-клиентов (пусто = SNI:443)
	Dest string `json:"dest,omitempty"`
	// Xver — передавать ли Dest реальный IP через PROXY protocol (0 — нет, 1 или 2 — версия)
	Xver int `json:"xver,omitempty"`
}

// Client — кому разрешено подключаться к ноде.
// Email используется Xray как ключ статистики.
type Client struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// Exit — выходная нода, через которую мост выпускает трафик
type Exit struct {
	Address   string `json:"address"`
	Port      int    `json:"port"`
	UUID      string `json:"uuid"` // UUID, под которым мост авторизуется на экзите
	PublicKey string `json:"public_key"`
	SNI       string `json:"sni"`
	ShortID   string `json:"short_id"`
	// XHTTPPath — если задан, мост подключается к экзиту по XHTTP (с переиспользованием
	// соединений), а не по TCP + Vision с новым рукопожатием на каждое соединение
	XHTTPPath string `json:"xhttp_path,omitempty"`
	// CDN — домен экзита за CDN: мост подключается к нему через CDN (XHTTP поверх TLS),
	// а не напрямую по IP. Работает, когда прямой канал до экзита режут.
	CDN string `json:"cdn,omitempty"`
}

// UserTraffic — прирост трафика пользователя с момента прошлого отчёта
type UserTraffic struct {
	Email string `json:"email"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
}

// Notify — сообщения для пересылки в Telegram и куда их слать
type Notify struct {
	Token    string          `json:"token"`
	Chats    []string        `json:"chats"`
	Messages []NotifyMessage `json:"messages"`
}

type NotifyMessage struct {
	ID   uint   `json:"id"`
	Text string `json:"text"`
}

// NotifyAck — итог отправки сообщения: пустая Error — доставлено во все чаты
type NotifyAck struct {
	ID    uint   `json:"id"`
	Error string `json:"error,omitempty"`
}

// CDNPort — порт входа для CDN. Cloudflare проксирует HTTPS только на 443, 2053, 2083,
// 2087, 2096 и 8443; 443 занят Reality, поэтому 8443.
const CDNPort = 8443

// CDN — вход для CDN на ноде. TLS с самоподписанным сертификатом: CDN в режиме «Full»
// его принимает, а снаружи сертификат CDN настоящий.
type CDN struct {
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	CertFile string `json:"-"` // заполняет агент
	KeyFile  string `json:"-"`
}

// MieruSocksPort — локальный вход Xray для трафика из mita: так mieru получает
// каскад, WARP и правила маршрутизации наравне с остальными протоколами
const MieruSocksPort = 10086

// Mieru — что нужно ноде для сервера mieru
type Mieru struct {
	Version string      `json:"version"` // версия mita, которую ставит агент ("3.38.0")
	Ports   string      `json:"ports"`   // TCP-порты: "40100-40109" или "40100"
	Users   []MieruUser `json:"users"`
	// Логин и пароль локального SOCKS-входа Xray — их задаёт агент
	SocksUser string `json:"-"`
	SocksPass string `json:"-"`
}

type MieruUser struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// LinkSample — замер канала мост → выходная нода: Sent TCP-подключений к её 443,
// Lost — без ответа за секунду; задержка — по ответившим
type LinkSample struct {
	To    string  `json:"to"` // IP выходной ноды
	Sent  int     `json:"sent"`
	Lost  int     `json:"lost"`
	AvgMs float64 `json:"avg_ms"`
	MaxMs float64 `json:"max_ms"`
}

// Действия с сервером из панели
const (
	ActionRestartXray  = "restart-xray"
	ActionRestartMieru = "restart-mieru"
	ActionRestartAgent = "restart-agent"
	ActionReboot       = "reboot"
)

// NodeMetrics — показатели сервера. Скорости и доли считаются с прошлого замера
// (раз в минуту); в первом замере после запуска агента их нет (nil).
type NodeMetrics struct {
	CPU         *float64 `json:"cpu,omitempty"`   // загрузка процессора, %
	Steal       *float64 `json:"steal,omitempty"` // процессор, отнятый соседями по физическому серверу, %
	Mem         float64  `json:"mem"`             // занятая память (без кеша), %
	MemTotalMB  int      `json:"mem_total_mb"`
	Disk        float64  `json:"disk"` // занято на /, %
	DiskTotalGB float64  `json:"disk_total_gb"`
	Load1       float64  `json:"load1"`
	Cores       int      `json:"cores"`
	RxBps       *float64 `json:"rx_bps,omitempty"`  // входящий трафик, бит/с
	TxBps       *float64 `json:"tx_bps,omitempty"`  // исходящий, бит/с
	Retrans     *float64 `json:"retrans,omitempty"` // доля переотправленных TCP-сегментов, %
	Conns       int      `json:"conns"`             // открытых TCP-соединений
	UptimeSec   int64    `json:"uptime_sec"`
}
