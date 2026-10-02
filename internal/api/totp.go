package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"gorm.io/gorm"
)

// Двухфакторный вход: TOTP (RFC 6238) — Google Authenticator, Aegis, 2FAS, Bitwarden…
// Код считается в приложении по времени, интернет не нужен ни телефону, ни мастеру.

const (
	totpKey      = "totp"
	totpStep     = 30 * time.Second
	totpDigits   = 6
	totpSkew     = 1  // принимаем соседние интервалы: ±30 с
	totpDriftMax = 20 // ±10 минут — только чтобы подсказать про сбитые часы
	recoveryN    = 8
)

type totpState struct {
	Enabled  bool     `json:"enabled"`
	Secret   string   `json:"secret,omitempty"`   // base32, действующий
	Pending  string   `json:"pending,omitempty"`  // выдан при настройке, ещё не подтверждён
	Recovery []string `json:"recovery,omitempty"` // sha256 неиспользованных резервных кодов
}

// totpUsed — последний принятый интервал: один и тот же код дважды не принимаем
var (
	totpMu   sync.Mutex
	totpUsed int64
)

func loadTOTP(db *gorm.DB) totpState {
	var st totpState
	var s models.Setting
	if db.First(&s, "key = ?", totpKey).Error == nil {
		_ = json.Unmarshal([]byte(s.Value), &st)
	}
	return st
}

func saveTOTP(db *gorm.DB, st totpState) error {
	raw, _ := json.Marshal(st)
	return db.Save(&models.Setting{Key: totpKey, Value: string(raw)}).Error
}

// ResetTOTP выключает 2FA (аварийный сброс с сервера мастера: kvn-master reset-2fa)
func ResetTOTP(db *gorm.DB) error { return saveTOTP(db, totpState{}) }

func newTOTPSecret() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

func totpCode(secret string, counter int64) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return ""
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, v%1000000)
}

// totpMatch — интервал, для которого код верен, в пределах ±window от now
func totpMatch(secret, code string, now time.Time, window int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	cur := now.Unix() / int64(totpStep/time.Second)
	for d := int64(0); d <= window; d++ {
		for _, c := range []int64{cur + d, cur - d} {
			if secureEqual(code, totpCode(secret, c)) {
				return c - cur, true
			}
		}
	}
	return 0, false
}

func recoveryHash(code string) string {
	return hashToken(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", "")))
}

func newRecoveryCodes() (plain, hashed []string) {
	for i := 0; i < recoveryN; i++ {
		b := make([]byte, 5)
		_, _ = rand.Read(b)
		c := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
		plain = append(plain, c[:4]+"-"+c[4:])
		hashed = append(hashed, recoveryHash(c))
	}
	return
}

// checkSecondFactor проверяет код из приложения или резервный код.
// Ошибка — понятная человеку: в том числе про сбитые часы сервера.
func (s *Server) checkSecondFactor(st *totpState, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return errNeedCode
	}
	now := s.now()
	if off, ok := totpMatch(st.Secret, code, now, totpSkew); ok {
		totpMu.Lock()
		defer totpMu.Unlock()
		counter := now.Unix()/int64(totpStep/time.Second) + off
		if counter <= totpUsed {
			return fmt.Errorf("Этот код уже использован — дождитесь следующего")
		}
		totpUsed = counter
		return nil
	}
	if off, ok := totpMatch(st.Secret, code, now, totpDriftMax); ok {
		return fmt.Errorf("Код верный, но часы сервера расходятся с телефоном примерно на %d с — проверьте время на сервере (timedatectl)", off*int64(totpStep/time.Second))
	}
	// Резервный код — одноразовый
	h := recoveryHash(code)
	for i, r := range st.Recovery {
		if secureEqual(h, r) {
			st.Recovery = append(st.Recovery[:i], st.Recovery[i+1:]...)
			return saveTOTP(s.db, *st)
		}
	}
	return fmt.Errorf("Неверный код")
}

var errNeedCode = fmt.Errorf("Введите код из приложения-аутентификатора")

// ---------- API ----------

func (s *Server) getTOTP(c echo.Context) error {
	st := loadTOTP(s.db)
	return c.JSON(http.StatusOK, map[string]any{"enabled": st.Enabled, "recovery_left": len(st.Recovery)})
}

// setupTOTP выдаёт новый секрет; включится он только после подтверждения кодом
func (s *Server) setupTOTP(c echo.Context) error {
	st := loadTOTP(s.db)
	if st.Enabled {
		return jsonError(c, http.StatusBadRequest, "Двухфакторный вход уже включён")
	}
	st.Pending = newTOTPSecret()
	if err := saveTOTP(s.db, st); err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	label := "KVN"
	if u, err := url.Parse(s.cfg.MasterURL); err == nil && u.Hostname() != "" {
		label = "KVN:" + u.Hostname()
	}
	uri := fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=KVN&digits=%d&period=%d",
		url.PathEscape(label), st.Pending, totpDigits, int(totpStep/time.Second))
	return c.JSON(http.StatusOK, map[string]string{"secret": st.Pending, "uri": uri})
}

func (s *Server) enableTOTP(c echo.Context) error {
	var req struct {
		Code string `json:"code"`
	}
	_ = c.Bind(&req)
	st := loadTOTP(s.db)
	if st.Pending == "" {
		return jsonError(c, http.StatusBadRequest, "Сначала получите QR-код")
	}
	if _, ok := totpMatch(st.Pending, req.Code, s.now(), totpSkew); !ok {
		if off, ok := totpMatch(st.Pending, req.Code, s.now(), totpDriftMax); ok {
			return jsonError(c, http.StatusBadRequest, fmt.Sprintf("Часы сервера расходятся с телефоном примерно на %d с — проверьте время на сервере", off*int64(totpStep/time.Second)))
		}
		return jsonError(c, http.StatusBadRequest, "Неверный код — проверьте, что отсканировали QR-код этой панели")
	}
	plain, hashed := newRecoveryCodes()
	st = totpState{Enabled: true, Secret: st.Pending, Recovery: hashed}
	if err := saveTOTP(s.db, st); err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return c.JSON(http.StatusOK, map[string]any{"enabled": true, "recovery": plain})
}

func (s *Server) disableTOTP(c echo.Context) error {
	var req struct {
		Code string `json:"code"`
	}
	_ = c.Bind(&req)
	st := loadTOTP(s.db)
	if !st.Enabled {
		return c.JSON(http.StatusOK, map[string]any{"enabled": false})
	}
	if err := s.checkSecondFactor(&st, req.Code); err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	if err := ResetTOTP(s.db); err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return c.JSON(http.StatusOK, map[string]any{"enabled": false})
}
