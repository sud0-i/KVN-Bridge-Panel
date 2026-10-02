package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/xray"
	statscmd "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type agent struct {
	masterURL  string
	token      string
	privKey    string
	configPath string
	xrayBin    string
	http       *http.Client

	// Статистика, которую ещё не удалось отправить Мастеру.
	// Xray сбрасывает счётчики при чтении, поэтому без буфера трафик терялся бы.
	mu      sync.Mutex
	pending map[string]*protocol.UserTraffic

	// ssh — состояние SSH на ноде (ключи панели, вход по паролю)
	ssh *sshState

	// metrics — показатели сервера; actionResult — итог последнего действия из панели
	metrics      *metricsSampler
	actionResult string

	// links — замеры канала до выходных нод (только у моста)
	links *linkMonitor

	// mieru — состояние сервера mieru (mita); mitaTraffic подменяет его API в тестах
	mieru       *mieruState
	mitaTraffic func(uds string) (map[string][2]int64, error)

	// cdnPortOpen — порт входа для CDN уже открыт в ufw
	cdnPortOpen bool

	// notifyAcks — итоги пересылки уведомлений, ждут отправки мастеру
	notifyAcks []protocol.NotifyAck

	// hotAdd подменяет добавление пользователей через API Xray (тесты)
	hotAdd func([]hotUser) error

	// maint — обновления агента, Xray и геобаз
	maint *maintainer

	// hy2Cert — сертификат Hysteria2 (PEM), который агент сообщает Мастеру для пина в подписках
	hy2Cert []byte

	// lastErr — последняя ошибка применения конфига, уходит Мастеру при следующей синхронизации
	lastErr string
	// Состояние ноды, которое агент сообщает Мастеру
	sniErr  string
	warpErr string
	sni     sniChecker
	warp    warpDoctor
}

func main() {
	// 1. Загружаем переменные из .env файла
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ Файл .env не найден, используем системные переменные")
	}

	a := &agent{
		masterURL:  strings.TrimRight(os.Getenv("MASTER_URL"), "/"),
		token:      os.Getenv("NODE_TOKEN"),
		privKey:    os.Getenv("PRIVATE_KEY"),
		configPath: envOr("XRAY_CONFIG", "/usr/local/etc/xray/config.json"),
		xrayBin:    envOr("XRAY_BIN", "xray"),
		http:       &http.Client{Timeout: 15 * time.Second},
		pending:    map[string]*protocol.UserTraffic{},
		maint:      newMaintainer(),
		links:      newLinkMonitor(),
		metrics:    newMetricsSampler(),
	}
	if a.masterURL == "" || a.token == "" || a.privKey == "" {
		log.Fatal("❌ Ошибка: MASTER_URL, NODE_TOKEN или PRIVATE_KEY не заданы!")
	}

	log.Printf("🤖 Агент запущен. Мастер: %s", a.masterURL)
	// Уже выпущенный сертификат Hysteria2 сообщаем с первой же синхронизации
	if cert, err := os.ReadFile(filepath.Join(filepath.Dir(a.configPath), hy2CertName)); err == nil {
		a.hy2Cert = cert
	}

	a.sync()
	// Канал до выходных нод: замер раз в минуту, результаты уходят с синхронизацией
	go a.links.run()

	// Отправка статистики каждую минуту
	go func() {
		for range time.Tick(time.Minute) {
			a.collectAndSendStats()
		}
	}()

	// Обновление юзеров каждую минуту (запрос лёгкий, а блокировки применяются быстрее)
	for range time.Tick(time.Minute) {
		a.sync()
	}
}

// --- СИНХРОНИЗАЦИЯ И ОБНОВЛЕНИЕ КОНФИГА ---
func (a *agent) sync() {
	if a.maint == nil {
		a.maint = newMaintainer()
	}
	if a.mieru == nil {
		a.mieru = &mieruState{}
	}
	if a.links == nil {
		a.links = newLinkMonitor()
	}
	if a.metrics == nil {
		a.metrics = newMetricsSampler()
	}
	req, _ := http.NewRequest(http.MethodGet, a.masterURL+"/api/sync", nil)
	req.Header.Set(protocol.NodeTokenHeader, a.token)
	warp := warpAvailable()
	if warp {
		req.Header.Set(protocol.WarpHeader, "1")
	} else {
		req.Header.Set(protocol.WarpHeader, "0")
	}
	req.Header.Set(protocol.ConfigErrorHeader, url.QueryEscape(a.lastErr))
	req.Header.Set(protocol.SNIErrorHeader, url.QueryEscape(a.sniErr))
	req.Header.Set(protocol.WarpErrorHeader, url.QueryEscape(a.warpErr))
	req.Header.Set(protocol.AgentVersionHeader, a.maint.selfSHA)
	if v := installedXray(a.xrayBin); v != "" {
		req.Header.Set(protocol.XrayVersionHeader, v)
	}
	req.Header.Set(protocol.GeoUpdatedHeader, a.maint.geoUpdated())
	req.Header.Set(protocol.UpdateErrorHeader, url.QueryEscape(a.maint.lastErr()))
	req.Header.Set(protocol.MieruHeader, a.mieru.running)
	req.Header.Set(protocol.MieruErrorHeader, url.QueryEscape(a.mieru.err))
	req.Header.Set(protocol.MetricsHeader, a.metrics.header())
	if st := a.sshHeader(); st != "" {
		req.Header.Set(protocol.SSHHeader, url.QueryEscape(st))
	}
	actionSent := a.actionResult
	if actionSent != "" {
		req.Header.Set(protocol.ActionHeader, a.actionHeader())
	}
	links, linksTaken := a.links.header()
	if links != "" {
		req.Header.Set(protocol.LinksHeader, links)
	}
	acks := a.notifyAckHeader()
	if acks != "" {
		req.Header.Set(protocol.NotifyAckHeader, acks)
	}
	if len(a.hy2Cert) > 0 {
		req.Header.Set(protocol.HysteriaCertHeader, base64.StdEncoding.EncodeToString(a.hy2Cert))
	}

	resp, err := a.http.Do(req)
	if err != nil {
		log.Printf("❌ Ошибка связи с мастером: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("❌ Мастер отклонил запрос (код %d). Проверьте NODE_TOKEN.", resp.StatusCode)
		return
	}

	// Мастер получил итоги пересылки и замеры канала — больше их не шлём
	if acks != "" {
		a.notifyAcks = nil
	}
	a.links.sent(linksTaken)
	if actionSent != "" && a.actionResult == actionSent {
		a.actionResult = ""
	}

	var data protocol.SyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("❌ Ошибка парсинга JSON от мастера: %v", err)
		return
	}
	// Обновления — после применения конфига и даже если он не применился:
	// новая версия агента или Xray может как раз это исправить
	defer a.maintain(data.Maintenance)
	a.links.setExits(data.Exits)
	a.scheduleAction(data.Action)
	a.syncSSH(data.SSHKeys, data.SSHKeysOnly)
	// mita — после применения конфига Xray: его SOCKS-вход для mita должен уже работать
	if data.Mieru != nil {
		data.Mieru.SocksUser, data.Mieru.SocksPass = mieruSocksUser, mieruSocksPass(a.privKey)
	}
	defer a.syncMieru(data.Mieru)
	a.relayNotify(data.Notify)

	// Проверяем, что сайт для маскировки Reality открывается с ноды.
	// Если Dest задан (мост на сервере Мастера), маскировка идёт под свой Caddy — проверять нечего.
	if data.Reality.Dest == "" && data.Reality.SNI != "" {
		a.sniErr = a.sni.get(data.Reality.SNI, checkSNI)
	} else {
		a.sniErr = ""
	}
	// WARP нужен выходным нодам и мосту без выходных нод, если для него есть правила
	if data.Role == protocol.RoleExit || len(data.Warp) > 0 {
		a.warpErr = a.warp.diagnose(warp)
		warp = a.warpErr == ""
	} else {
		a.warpErr = ""
	}

	// Без работающего WARP правила «через WARP» не применяем: трафик пойдёт с IP ноды,
	// а не в никуда
	if len(data.Warp) > 0 && !warp {
		log.Printf("⚠️ WARP недоступен на 127.0.0.1:%d — %d правил пойдут напрямую", protocol.WarpProxyPort, len(data.Warp))
		data.Warp = nil
	}

	// Hysteria2: сертификат выпускается на SNI ноды и лежит рядом с конфигом Xray.
	// Если выпустить не удалось — поднимаем ноду без Hysteria, VLESS важнее.
	if data.Hysteria != nil {
		certFile, keyFile, certPEM, err := ensureHy2Cert(filepath.Dir(a.configPath), data.Reality.SNI, time.Now())
		if err != nil {
			log.Printf("⚠️ Hysteria2 выключен: %v", err)
			data.Hysteria = nil
		} else {
			data.Hysteria.CertFile, data.Hysteria.KeyFile = certFile, keyFile
			a.hy2Cert = certPEM
		}
	}

	// Вход для CDN: свой сертификат на CDN-домен и открытый порт. Не вышло — без CDN.
	if data.CDN != nil {
		certFile, keyFile, err := ensureCDNCert(filepath.Dir(a.configPath), data.CDN.Domain, time.Now())
		if err != nil {
			log.Printf("⚠️ Вход для CDN выключен: %v", err)
			data.CDN = nil
		} else {
			data.CDN.CertFile, data.CDN.KeyFile = certFile, keyFile
			a.openCDNPort()
		}
	}

	cfg, err := xray.Build(data, a.privKey)
	if err != nil {
		a.lastErr = err.Error()
		log.Printf("❌ Ошибка сборки конфига: %v", err)
		return
	}

	changed, err := a.applyConfig(cfg)
	if err != nil && (len(data.Direct) > 0 || len(data.Warp) > 0) {
		// Чаще всего Xray отвергает правило маршрутизации (например, несуществующий geosite).
		// Применяем конфиг без них: пользователи и выходные ноды должны обновляться всё равно.
		log.Printf("⚠️ Xray отверг конфиг, пробуем без правил маршрутизации: %v", err)
		routingErr := err
		data.Direct, data.Warp = nil, nil
		if cfg, err = xray.Build(data, a.privKey); err == nil {
			if changed, err = a.applyConfig(cfg); err == nil {
				a.lastErr = "Правила маршрутизации не применены: " + routingErr.Error()
				log.Printf("✅ Конфиг обновлён без правил маршрутизации")
				return
			}
		}
	}
	if err != nil {
		a.lastErr = err.Error()
		log.Printf("❌ Не удалось применить конфиг: %v", err)
		return
	}
	a.lastErr = ""
	if changed {
		log.Printf("✅ Конфиг обновлён: роль %s, клиентов %d, экзитов %d", data.Role, len(data.Clients), len(data.Exits))
	}
}

// applyConfig проверяет новый конфиг и перезапускает Xray, только если что-то поменялось
func (a *agent) applyConfig(cfg []byte) (bool, error) {
	if old, err := os.ReadFile(a.configPath); err == nil && bytes.Equal(old, cfg) {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(a.configPath), 0o755); err != nil {
		return false, err
	}
	// Пишем во временный файл рядом: так rename будет атомарным
	tmp := tempConfigPath(a.configPath)
	// 0644: служба Xray из официального установщика работает от пользователя nobody
	if err := os.WriteFile(tmp, cfg, 0o644); err != nil {
		return false, err
	}
	defer os.Remove(tmp)

	// Битый конфиг не должен уронить работающий Xray
	if out, err := exec.Command(a.xrayBin, "run", "-test", "-c", tmp).CombinedOutput(); err != nil {
		return false, fmt.Errorf("xray отверг конфиг: %s", xrayError(out))
	}
	old, _ := os.ReadFile(a.configPath)
	if err := os.Rename(tmp, a.configPath); err != nil {
		return false, err
	}
	// Только новые пользователи — добавляем на лету, остальные соединения не трогаем.
	// Не вышло (старый Xray без HandlerService, сбой API) — перезапуск с уже записанным конфигом.
	if adds, ok := hotAdds(old, cfg); ok && len(adds) > 0 {
		err := a.addUsers(adds)
		if err == nil {
			log.Printf("➕ Добавлено пользователей без перезапуска Xray: %d", len(adds))
			return true, nil
		}
		log.Printf("⚠️ Не удалось добавить пользователей на лету, перезапускаю Xray: %v", err)
	}
	// Перезапуск обнулит счётчики трафика в Xray — сначала забираем их
	if len(old) > 0 { // при первом конфиге Xray ещё не работал — считать нечего
		if err := a.collectStats(); err != nil {
			log.Printf("⚠️ Статистика перед перезапуском не собрана: %v", err)
		}
	}
	if out, err := exec.Command("systemctl", "restart", "xray").CombinedOutput(); err != nil {
		return true, fmt.Errorf("перезапуск xray: %v\n%s", err, out)
	}
	return true, nil
}

// tempConfigPath — имя временного файла рядом с конфигом. Оно обязано
// заканчиваться на .json: Xray определяет формат конфига по расширению.
func tempConfigPath(configPath string) string {
	return strings.TrimSuffix(configPath, ".json") + ".next.json"
}

// --- СБОР И ОТПРАВКА СТАТИСТИКИ ---
func (a *agent) collectAndSendStats() {
	if err := a.collectStats(); err != nil {
		log.Printf("⚠️ Не удалось получить статистику от Xray: %v", err)
	}
	if err := a.collectMieru(); err != nil {
		log.Printf("⚠️ Не удалось получить статистику от mieru: %v", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.pending) == 0 {
		return // Если трафика не было, не дергаем Мастер
	}

	report := make([]protocol.UserTraffic, 0, len(a.pending))
	for _, st := range a.pending {
		report = append(report, *st)
	}
	if err := a.sendStats(report); err != nil {
		log.Printf("❌ Статистика не отправлена, попробуем в следующий раз: %v", err)
		return
	}
	a.pending = map[string]*protocol.UserTraffic{}
}

func (a *agent) collectStats() error {
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", xray.APIPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Запрашиваем статистику с обнулением счётчиков в Xray
	resp, err := statscmd.NewStatsServiceClient(conn).QueryStats(ctx, &statscmd.QueryStatsRequest{
		Pattern: "user>>>",
		Reset_:  true,
	})
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, stat := range resp.Stat {
		// Xray отдает имена в формате: user>>>EMAIL>>>traffic>>>downlink
		parts := strings.Split(stat.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || stat.Value == 0 {
			continue
		}
		email := parts[1]
		if email == mieruSocksUser {
			continue // вход для mita: трафик mieru считает сам mita, по пользователям
		}
		st, ok := a.pending[email]
		if !ok {
			st = &protocol.UserTraffic{Email: email}
			a.pending[email] = st
		}
		switch parts[3] {
		case "downlink":
			st.Down += stat.Value
		case "uplink":
			st.Up += stat.Value
		}
	}
	return nil
}

func (a *agent) sendStats(report []protocol.UserTraffic) error {
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	req, _ := http.NewRequest(http.MethodPost, a.masterURL+"/api/stats", bytes.NewReader(body))
	req.Header.Set(protocol.NodeTokenHeader, a.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("мастер ответил кодом %d", resp.StatusCode)
	}
	return nil
}

// warpAvailable — слушает ли локальный прокси Cloudflare WARP
func warpAvailable() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", protocol.WarpProxyPort), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
