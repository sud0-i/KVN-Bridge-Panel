package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

// Обновления на ноде: сам агент, Xray и геобазы. Общий принцип — ничего не ставим,
// пока не проверили: хэш скачанного файла и `xray run -test` с новой версией.
// Ошибка любого шага оставляет всё как было и уходит в панель (X-Node-Update-Error).

const (
	// retryAfter — после ошибки не пытаемся снова каждую минуту
	retryAfter = 30 * time.Minute
	// maxDownload — защита от бесконечного ответа
	maxDownload = 200 << 20
)

var (
	xrayReleaseURL = "https://github.com/XTLS/Xray-core/releases/download"
	geoBaseURL     = "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download"
	geoFiles       = []string{"geoip.dat", "geosite.dat"}
	xrayVersionOut = regexp.MustCompile(`Xray ([0-9]+\.[0-9]+\.[0-9]+)`)
)

type maintainer struct {
	selfSHA    string // хэш собственного бинарника
	assetDir   string // где лежат geoip.dat / geosite.dat
	download   *http.Client
	errs       map[string]string    // последняя ошибка по виду обновления
	failedAt   map[string]time.Time // когда последний раз провалилось
	now        func() time.Time
	execSelf   func(path string) error // перезапуск в новом бинарнике (в тестах — заглушка)
	selfPath   string
	selfUpdate bool
}

func newMaintainer() *maintainer {
	m := &maintainer{
		assetDir: envOr("XRAY_LOCATION_ASSET", "/usr/local/share/xray"),
		download: &http.Client{Timeout: 5 * time.Minute},
		errs:     map[string]string{},
		failedAt: map[string]time.Time{},
		now:      time.Now,
		execSelf: func(path string) error { return syscall.Exec(path, os.Args, os.Environ()) },
		// AGENT_SELF_UPDATE=0 — отключить самообновление (например, при разработке)
		selfUpdate: os.Getenv("AGENT_SELF_UPDATE") != "0",
	}
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			m.selfPath = exe
			m.selfSHA, _ = fileSHA256(exe)
		}
	}
	return m
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (m *maintainer) fail(kind string, err error) {
	m.failedAt[kind] = m.now()
	m.errs[kind] = err.Error()
	log.Printf("⚠️ Обновление (%s) не удалось: %v", kind, err)
}

func (m *maintainer) ok(kind string) {
	delete(m.errs, kind)
	delete(m.failedAt, kind)
}

// lastErr — ошибки обновлений для панели, пусто — всё в порядке
func (m *maintainer) lastErr() string {
	var parts []string
	for _, kind := range []string{"агент", "xray", "геобазы"} {
		if e := m.errs[kind]; e != "" {
			parts = append(parts, kind+": "+e)
		}
	}
	return strings.Join(parts, "; ")
}

func (m *maintainer) mayTry(kind string) bool {
	t, ok := m.failedAt[kind]
	return !ok || m.now().Sub(t) > retryAfter
}

func (m *maintainer) get(url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := m.download.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: код %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownload))
}

// ---------- 1. Сам агент ----------

// updateSelf: у мастера другой бинарник — скачиваем, сверяем хэш, подменяем себя и
// перезапускаемся в новом (тот же PID для systemd). Возвращается, только если не вышло.
func (a *agent) updateSelf(want string) {
	m := a.maint
	if want == m.selfSHA {
		m.ok("агент")
	}
	if want == "" || want == m.selfSHA || !m.selfUpdate || m.selfPath == "" || !m.mayTry("агент") {
		return
	}
	// Мастер собирает агент под linux/amd64
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return
	}
	body, err := m.get(a.masterURL+"/api/agent/binary", map[string]string{protocol.NodeTokenHeader: a.token})
	if err != nil {
		m.fail("агент", err)
		return
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != want {
		m.fail("агент", fmt.Errorf("хэш скачанного бинарника %s… не совпал с ожидаемым %s…", got[:12], want[:12]))
		return
	}
	tmp := m.selfPath + ".new"
	if err := os.WriteFile(tmp, body, 0o755); err != nil {
		m.fail("агент", err)
		return
	}
	if err := os.Rename(tmp, m.selfPath); err != nil {
		os.Remove(tmp)
		m.fail("агент", err)
		return
	}
	log.Printf("⬆️ Агент обновлён (%s… → %s…), перезапускаюсь", short(m.selfSHA), short(want))
	a.collectAndSendStats() // не теряем накопленный трафик
	if err := m.execSelf(m.selfPath); err != nil {
		m.fail("агент", fmt.Errorf("перезапуск: %w", err))
	}
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// ---------- 2. Xray ----------

// installedXray — версия установленного Xray ("26.3.27"), пусто — не удалось узнать
func installedXray(bin string) string {
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return ""
	}
	if m := xrayVersionOut.FindSubmatch(out); m != nil {
		return string(m[1])
	}
	return ""
}

func xrayAsset() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "Xray-linux-64.zip", nil
	case "arm64":
		return "Xray-linux-arm64-v8a.zip", nil
	}
	return "", fmt.Errorf("архитектура %s не поддерживается", runtime.GOARCH)
}

// parseDgst достаёт SHA-256 из файла .dgst релиза Xray ("SHA2-256= <hex>")
func parseDgst(dgst []byte) string {
	for _, line := range strings.Split(string(dgst), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "SHA2-256" {
			return strings.ToLower(strings.TrimSpace(v))
		}
	}
	return ""
}

// updateXray ставит нужную версию Xray: скачать → сверить хэш → проверить текущий конфиг
// новым бинарником → атомарно подменить → перезапустить. Старая версия продолжает работать,
// пока новая не прошла проверку.
func (a *agent) updateXray(want string) {
	m := a.maint
	if want == "" || !m.mayTry("xray") {
		return
	}
	bin, err := exec.LookPath(a.xrayBin)
	if err != nil {
		return
	}
	if installedXray(bin) == want {
		m.ok("xray")
		return
	}
	if err := a.installXray(bin, want); err != nil {
		m.fail("xray", err)
		return
	}
	m.ok("xray")
	log.Printf("⬆️ Xray обновлён до %s", want)
}

func (a *agent) installXray(bin, want string) error {
	m := a.maint
	asset, err := xrayAsset()
	if err != nil {
		return err
	}
	base := fmt.Sprintf("%s/v%s/%s", xrayReleaseURL, want, asset)
	zipBytes, err := m.get(base, nil)
	if err != nil {
		return err
	}
	dgst, err := m.get(base+".dgst", nil)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(zipBytes)
	if wantSum := parseDgst(dgst); wantSum == "" || wantSum != hex.EncodeToString(sum[:]) {
		return errors.New("хэш архива не совпал с .dgst релиза")
	}
	newBin, err := extractFromZip(zipBytes, "xray")
	if err != nil {
		return err
	}
	tmp := bin + ".new"
	if err := os.WriteFile(tmp, newBin, 0o755); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if got := installedXray(tmp); got != want {
		return fmt.Errorf("скачанный Xray сообщает версию %q вместо %q", got, want)
	}
	if out, err := exec.Command(tmp, "run", "-test", "-c", a.configPath).CombinedOutput(); err != nil {
		return fmt.Errorf("новая версия отвергла текущий конфиг: %s", xrayError(out))
	}
	if err := os.Rename(tmp, bin); err != nil {
		return err
	}
	if out, err := exec.Command("systemctl", "restart", "xray").CombinedOutput(); err != nil {
		return fmt.Errorf("перезапуск xray: %v %s", err, out)
	}
	return nil
}

func extractFromZip(data []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, maxDownload))
	}
	return nil, fmt.Errorf("в архиве нет %s", name)
}

// ---------- 3. Геобазы ----------

// geoDue — пора ли обновлять: базы старше суток и сейчас ночь (3–6 по времени сервера),
// чтобы перезапуск Xray пришёлся на время, когда им почти не пользуются.
// Базы старше 3 суток обновляем в любое время.
func (m *maintainer) geoDue() bool {
	st, err := os.Stat(filepath.Join(m.assetDir, "geosite.dat"))
	if err != nil {
		return true
	}
	age := m.now().Sub(st.ModTime())
	h := m.now().Hour()
	return age > 72*time.Hour || (age > 20*time.Hour && h >= 3 && h < 6)
}

func (m *maintainer) geoUpdated() string {
	st, err := os.Stat(filepath.Join(m.assetDir, "geosite.dat"))
	if err != nil {
		return ""
	}
	return st.ModTime().UTC().Format(time.RFC3339)
}

// updateGeo скачивает свежие geoip/geosite, сверяет хэши, проверяет с ними текущий конфиг
// и только потом подменяет и перезапускает Xray. Если базы не изменились — просто
// отмечаем проверку (обновляем дату файла), без перезапуска.
func (a *agent) updateGeo() {
	m := a.maint
	if !m.mayTry("геобазы") || !m.geoDue() {
		return
	}
	tmpDir, err := os.MkdirTemp(m.assetDir, ".geo-")
	if err != nil {
		m.fail("геобазы", err)
		return
	}
	defer os.RemoveAll(tmpDir)

	changed := false
	for _, name := range geoFiles {
		data, err := m.get(geoBaseURL+"/"+name, nil)
		if err != nil {
			m.fail("геобазы", err)
			return
		}
		sumFile, err := m.get(geoBaseURL+"/"+name+".sha256sum", nil)
		if err != nil {
			m.fail("геобазы", err)
			return
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if want, _, _ := strings.Cut(strings.TrimSpace(string(sumFile)), " "); !strings.EqualFold(want, got) {
			m.fail("геобазы", fmt.Errorf("хэш %s не совпал", name))
			return
		}
		if old, err := fileSHA256(filepath.Join(m.assetDir, name)); err != nil || old != got {
			changed = true
		}
		if err := os.WriteFile(filepath.Join(tmpDir, name), data, 0o644); err != nil {
			m.fail("геобазы", err)
			return
		}
	}

	if changed {
		test := exec.Command(a.xrayBin, "run", "-test", "-c", a.configPath)
		test.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+tmpDir)
		if out, err := test.CombinedOutput(); err != nil {
			m.fail("геобазы", fmt.Errorf("с новыми базами конфиг не проходит: %s", xrayError(out)))
			return
		}
		for _, name := range geoFiles {
			if err := os.Rename(filepath.Join(tmpDir, name), filepath.Join(m.assetDir, name)); err != nil {
				m.fail("геобазы", err)
				return
			}
		}
		if out, err := exec.Command("systemctl", "restart", "xray").CombinedOutput(); err != nil {
			m.fail("геобазы", fmt.Errorf("перезапуск xray: %v %s", err, out))
			return
		}
		log.Printf("🗺️ Геобазы обновлены")
	} else {
		now := m.now()
		for _, name := range geoFiles {
			_ = os.Chtimes(filepath.Join(m.assetDir, name), now, now)
		}
	}
	m.ok("геобазы")
}

// maintain — все обновления после синхронизации. Агент — первым: новый агент
// сам доделает остальное.
func (a *agent) maintain(mt *protocol.Maintenance) {
	if mt == nil {
		return
	}
	a.updateSelf(mt.AgentSHA256)
	a.updateXray(mt.XrayVersion)
	if mt.GeoUpdate {
		a.updateGeo()
	}
}
