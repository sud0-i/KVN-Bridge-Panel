package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protowire"
)

// Mieru: сервер mita рядом с Xray. Агент ставит пакет нужной версии (со сверкой
// контрольной суммы), держит в mita тех же пользователей, что в панели, выпускает
// трафик mita в локальный SOCKS-вход Xray (каскад, WARP и правила — как у всех)
// и забирает трафик по пользователям из mita.

var (
	mieruReleaseURL = "https://github.com/enfein/mieru/releases/download"
	mitaBin         = "mita"
)

const (
	mieruConfigName   = "kvn-mieru.json"
	mieruCountersName = "kvn-mieru-counters.json"
	mieruSocksUser    = "kvn"
	// mitaGetUsers — метод API mita (pkg/appctl/proto/rpc.proto)
	mitaGetUsers = "/mieru.appctl.ServerManagementService/GetUsers"
)

type mieruState struct {
	applied  string // последний применённый конфиг
	running  string // версия работающего mita, пусто — не работает
	err      string
	failedAt time.Time
	// counters — последние значения счётчиков mita по пользователям: [скачано, отдано]
	counters map[string][2]int64
	portOpen string
}

func mitaUDS() string { return envOr("MITA_UDS_PATH", "/var/run/mita/mita.sock") }

// mieruSocksPass — пароль локального SOCKS-входа Xray для mita. Выводится из
// приватного ключа ноды: постоянный и на диск отдельно не пишется.
func mieruSocksPass(privKey string) string {
	sum := sha256.Sum256([]byte("kvn-mieru-socks:" + privKey))
	return hex.EncodeToString(sum[:12])
}

func (a *agent) mieruDir() string { return filepath.Dir(a.configPath) }

// syncMieru приводит mita в нужное состояние; nil — mieru выключен
func (a *agent) syncMieru(m *protocol.Mieru) {
	st := a.mieru
	if m == nil {
		if st.applied != "" || st.running != "" {
			_ = exec.Command(mitaBin, "stop").Run()
			log.Printf("⏹ mieru выключен")
		}
		st.applied, st.running, st.err = "", "", ""
		return
	}
	if err := a.applyMieru(m); err != nil {
		st.running, st.err = "", err.Error()
		st.failedAt = time.Now()
		log.Printf("⚠️ mieru: %v", err)
		return
	}
	st.err = ""
}

func (a *agent) applyMieru(m *protocol.Mieru) error {
	st := a.mieru
	if err := a.ensureMita(m.Version); err != nil {
		return err
	}
	cfg, err := mitaConfig(m, a.privKey)
	if err != nil {
		return err
	}
	running := mitaRunning()
	if string(cfg) != st.applied || !running {
		path := filepath.Join(a.mieruDir(), mieruConfigName)
		if err := writeFileAtomic(path, cfg, 0o600); err != nil {
			return err
		}
		out, err := exec.Command(mitaBin, "replace", "config", path).CombinedOutput()
		if err != nil && strings.Contains(string(out), "daemon is not running") {
			// Служба mita остановлена (или упала при установке) — поднимаем и пробуем ещё раз
			_ = exec.Command("systemctl", "restart", "mita").Run()
			time.Sleep(2 * time.Second)
			out, err = exec.Command(mitaBin, "replace", "config", path).CombinedOutput()
		}
		if err != nil {
			return fmt.Errorf("mita отверг конфиг: %s", strings.TrimSpace(string(out)))
		}
		switch {
		case running && st.applied != "" && sameExceptUsers(st.applied, string(cfg)) && !usersRemoved(st.applied, string(cfg)):
			// Только добавились пользователи — без обрыва соединений. Удаление и блокировку
			// применяем перезапуском: reload не закрывает уже открытые соединения.
			if out, err := exec.Command(mitaBin, "reload").CombinedOutput(); err != nil {
				return fmt.Errorf("mita reload: %s", strings.TrimSpace(string(out)))
			}
		default:
			if running {
				_ = exec.Command(mitaBin, "stop").Run()
			}
			if out, err := exec.Command(mitaBin, "start").CombinedOutput(); err != nil {
				return fmt.Errorf("mita start: %s", strings.TrimSpace(string(out)))
			}
			if !mitaRunning() {
				return fmt.Errorf("mita не запустился (mita status)")
			}
			log.Printf("▶️ mieru запущен на портах %s", m.Ports)
		}
		st.applied = string(cfg)
	}
	a.openMieruPorts(m.Ports)
	st.running = m.Version
	return nil
}

// ensureMita ставит mita нужной версии из релиза mieru, сверив SHA-256
func (a *agent) ensureMita(version string) error {
	if installed := mitaVersion(); installed == version {
		return nil
	}
	if !a.mieru.failedAt.IsZero() && time.Since(a.mieru.failedAt) < retryAfter && a.mieru.err != "" {
		return fmt.Errorf("%s", a.mieru.err) // недавно не вышло — ждём, а не качаем каждую минуту
	}
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH]
	if arch == "" {
		return fmt.Errorf("архитектура %s не поддерживается", runtime.GOARCH)
	}
	name := fmt.Sprintf("mita_%s_%s.deb", version, arch)
	base := fmt.Sprintf("%s/v%s/%s", mieruReleaseURL, version, name)
	deb, err := a.maint.get(base, nil)
	if err != nil {
		return err
	}
	sumFile, err := a.maint.get(base+".sha256.txt", nil)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(deb)
	if want, _, _ := strings.Cut(strings.TrimSpace(string(sumFile)), " "); !strings.EqualFold(want, hex.EncodeToString(sum[:])) {
		return fmt.Errorf("контрольная сумма %s не совпала", name)
	}
	tmp, err := os.CreateTemp("", "mita-*.deb")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(deb); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if out, err := exec.Command("dpkg", "-i", tmp.Name()).CombinedOutput(); err != nil {
		return fmt.Errorf("установка mita: %s", strings.TrimSpace(string(out)))
	}
	if got := mitaVersion(); got != version {
		return fmt.Errorf("после установки mita сообщает версию %q", got)
	}
	log.Printf("⬆️ mita %s установлен", version)
	// Новый пакет — конфиг применим заново
	a.mieru.applied = ""
	return nil
}

func mitaVersion() string {
	out, err := exec.Command(mitaBin, "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
}

func mitaRunning() bool {
	out, _ := exec.Command(mitaBin, "status").CombinedOutput()
	return strings.Contains(string(out), "RUNNING")
}

// mitaConfig — полный конфиг mita (JSON в формате protojson, как ждёт mita replace config)
func mitaConfig(m *protocol.Mieru, privKey string) ([]byte, error) {
	type obj = map[string]any
	binding := obj{"protocol": "TCP"}
	if strings.Contains(m.Ports, "-") {
		binding["portRange"] = m.Ports
	} else {
		var port int
		if _, err := fmt.Sscan(m.Ports, &port); err != nil {
			return nil, fmt.Errorf("порты mieru: %q", m.Ports)
		}
		binding["port"] = port
	}
	users := make([]obj, 0, len(m.Users))
	for _, u := range m.Users {
		users = append(users, obj{"name": u.Name, "password": u.Password})
	}
	return json.MarshalIndent(obj{
		"portBindings": []obj{binding},
		"users":        users,
		"loggingLevel": "WARN",
		// Всё — в Xray: оттуда каскад, WARP, правила и запрет локальных адресов
		"egress": obj{
			"proxies": []obj{{
				"name":                 "xray",
				"protocol":             "SOCKS5_PROXY_PROTOCOL",
				"host":                 "127.0.0.1",
				"port":                 protocol.MieruSocksPort,
				"socks5Authentication": obj{"user": mieruSocksUser, "password": mieruSocksPass(privKey)},
			}},
			"rules": []obj{{"ipRanges": []string{"*"}, "domainNames": []string{"*"}, "action": "PROXY", "proxyNames": []string{"xray"}}},
		},
	}, "", "  ")
}

// sameExceptUsers — конфиги отличаются только пользователями (тогда хватит mita reload)
func sameExceptUsers(a, b string) bool {
	strip := func(s string) string {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) != nil {
			return s
		}
		delete(m, "users")
		raw, _ := json.Marshal(m)
		return string(raw)
	}
	return strip(a) == strip(b)
}

// usersRemoved — в новом конфиге нет кого-то из прежних пользователей (или сменился пароль)
func usersRemoved(oldCfg, newCfg string) bool {
	users := func(s string) map[string]string {
		var c struct {
			Users []struct{ Name, Password string }
		}
		_ = json.Unmarshal([]byte(s), &c)
		out := map[string]string{}
		for _, u := range c.Users {
			out[u.Name] = u.Password
		}
		return out
	}
	now := users(newCfg)
	for name, pass := range users(oldCfg) {
		if p, ok := now[name]; !ok || p != pass {
			return true
		}
	}
	return false
}

// openMieruPorts открывает порты mieru в ufw (один раз на набор портов)
func (a *agent) openMieruPorts(ports string) {
	if a.mieru.portOpen == ports {
		return
	}
	if _, err := exec.LookPath("ufw"); err != nil {
		a.mieru.portOpen = ports
		return
	}
	rule := strings.Replace(ports, "-", ":", 1) + "/tcp" // ufw пишет диапазон через двоеточие
	if out, err := exec.Command("ufw", "allow", rule).CombinedOutput(); err != nil {
		log.Printf("⚠️ ufw allow %s: %v %s", rule, err, out)
		return
	}
	a.mieru.portOpen = ports
}

// ---------- Статистика ----------

// collectMieru добавляет к a.pending трафик пользователей mieru с прошлого раза.
// mita отдаёт накопительные счётчики; последние значения храним на диске, чтобы
// перезапуск агента (например, самообновление) не терял и не удваивал трафик.
func (a *agent) collectMieru() error {
	if a.mieru == nil || a.mieru.running == "" {
		return nil
	}
	read := mitaUserTraffic
	if a.mitaTraffic != nil {
		read = a.mitaTraffic
	}
	totals, err := read(mitaUDS())
	if err != nil {
		return err
	}
	st := a.mieru
	if st.counters == nil {
		st.counters = map[string][2]int64{}
		if raw, err := os.ReadFile(filepath.Join(a.mieruDir(), mieruCountersName)); err == nil {
			_ = json.Unmarshal(raw, &st.counters)
		}
	}
	a.mu.Lock()
	for name, cur := range totals {
		prev, seen := st.counters[name]
		var d [2]int64
		for i := range cur {
			switch {
			case !seen:
				d[i] = 0 // впервые видим — точка отсчёта
			case cur[i] >= prev[i]:
				d[i] = cur[i] - prev[i]
			default:
				d[i] = cur[i] // mita перезапустился и обнулил счётчик
			}
		}
		if seen && (d[0] > 0 || d[1] > 0) {
			p, ok := a.pending[name]
			if !ok {
				p = &protocol.UserTraffic{Email: name}
				a.pending[name] = p
			}
			p.Down += d[0]
			p.Up += d[1]
		}
		st.counters[name] = cur
	}
	a.mu.Unlock()
	raw, _ := json.Marshal(st.counters)
	return writeFileAtomic(filepath.Join(a.mieruDir(), mieruCountersName), raw, 0o600)
}

// mitaUserTraffic — накопленный трафик по пользователям из API mita: имя → [скачано, отдано].
// Ответ разбираем сами (UserWithMetricsList в misc.proto), чтобы не тянуть mieru в агента:
//
//	UserWithMetricsList { repeated UserWithMetrics items = 1; }
//	UserWithMetrics     { User user = 1; repeated Metric metrics = 2; }
//	User                { string name = 1; … }
//	Metric              { string name = 1; MetricType type = 2; int64 value = 3; … }
func mitaUserTraffic(uds string) (map[string][2]int64, error) {
	conn, err := grpc.NewClient("unix://"+uds,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(rawReplyCodec{})))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := []byte{}
	var reply []byte
	if err := conn.Invoke(ctx, mitaGetUsers, &req, &reply); err != nil {
		return nil, err
	}
	return parseMitaUsers(reply)
}

func parseMitaUsers(b []byte) (map[string][2]int64, error) {
	out := map[string][2]int64{}
	err := eachField(b, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
		if num != 1 || typ != protowire.BytesType {
			return nil
		}
		var name string
		var traffic [2]int64
		err := eachField(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
			switch {
			case num == 1 && typ == protowire.BytesType: // User
				return eachField(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
					if num == 1 && typ == protowire.BytesType {
						name = string(v)
					}
					return nil
				})
			case num == 2 && typ == protowire.BytesType: // Metric
				var mname string
				var value int64
				err := eachField(v, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
					switch {
					case num == 1 && typ == protowire.BytesType:
						mname = string(v)
					case num == 3 && typ == protowire.VarintType:
						value = int64(n)
					}
					return nil
				})
				switch mname {
				case "DownloadBytes":
					traffic[0] = value
				case "UploadBytes":
					traffic[1] = value
				}
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
		if name != "" {
			out[name] = traffic
		}
		return nil
	})
	return out, err
}

// eachField перебирает поля protobuf-сообщения: для вложенных/строк — v, для чисел — n
func eachField(b []byte, fn func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error) error {
	for len(b) > 0 {
		num, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return protowire.ParseError(l)
		}
		b = b[l:]
		switch typ {
		case protowire.BytesType:
			v, l := protowire.ConsumeBytes(b)
			if l < 0 {
				return protowire.ParseError(l)
			}
			if err := fn(num, typ, v, 0); err != nil {
				return err
			}
			b = b[l:]
		case protowire.VarintType:
			n, l := protowire.ConsumeVarint(b)
			if l < 0 {
				return protowire.ParseError(l)
			}
			if err := fn(num, typ, nil, n); err != nil {
				return err
			}
			b = b[l:]
		default:
			l := protowire.ConsumeFieldValue(num, typ, b)
			if l < 0 {
				return protowire.ParseError(l)
			}
			b = b[l:]
		}
	}
	return nil
}

// rawReplyCodec — запрос уже закодирован, ответ отдаём байтами
type rawReplyCodec struct{}

func (rawReplyCodec) Marshal(v any) ([]byte, error) { return *(v.(*[]byte)), nil }
func (rawReplyCodec) Unmarshal(data []byte, v any) error {
	*(v.(*[]byte)) = bytes.Clone(data)
	return nil
}
func (rawReplyCodec) Name() string { return "proto" }
