package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"golang.org/x/crypto/ssh"
)

// SSH-ключ мастера: им панель заходит на ноды — терминал и переустановка без пароля.
// Создаётся при первом запуске рядом с базой (в Docker — ./data/ssh на хосте, переживает
// обновления). Публичную часть агенты сами прописывают в authorized_keys своих серверов.

const adminKeysKey = "ssh_admin_keys"

type masterKey struct {
	once   sync.Once
	path   string
	signer ssh.Signer
	pub    string // строка для authorized_keys
	err    error
}

func (k *masterKey) load() (ssh.Signer, string, error) {
	k.once.Do(func() {
		if raw, err := os.ReadFile(k.path); err == nil {
			k.signer, k.err = ssh.ParsePrivateKey(raw)
		} else {
			k.signer, k.err = generateMasterKey(k.path)
		}
		if k.err == nil {
			k.pub = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k.signer.PublicKey()))) + " kvn-master"
		}
	})
	return k.signer, k.pub, k.err
}

func generateMasterKey(path string) (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "kvn-master")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

// adminKeys — личные ключи администратора, которые ставятся на все ноды
func (s *Server) adminKeys() []string {
	var keys []string
	var st models.Setting
	if s.db.First(&st, "key = ?", adminKeysKey).Error == nil {
		_ = json.Unmarshal([]byte(st.Value), &keys)
	}
	return keys
}

// authorizedKeys — что агент держит в authorized_keys (ключ мастера + личные)
func (s *Server) authorizedKeys() []string {
	_, pub, err := s.sshKey.load()
	if err != nil {
		return nil
	}
	return append([]string{pub}, s.adminKeys()...)
}

func (s *Server) getSSHKeys(c echo.Context) error {
	_, pub, err := s.sshKey.load()
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ключ мастера: "+err.Error())
	}
	return c.JSON(http.StatusOK, map[string]any{"master": pub, "admin": s.adminKeys()})
}

func (s *Server) saveSSHKeys(c echo.Context) error {
	var req struct {
		Admin string `json:"admin"` // по ключу на строку
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}
	keys := []string{}
	for _, line := range strings.Split(req.Admin, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pk, comment, opts, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil || len(opts) > 0 {
			return jsonError(c, http.StatusBadRequest, fmt.Sprintf("Не похоже на публичный SSH-ключ: %.40s…", line))
		}
		// Пересобираем строку сами: только тип, ключ и комментарий, без опций
		k := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
		if comment = strings.Map(func(r rune) rune {
			if r < ' ' {
				return -1
			}
			return r
		}, comment); comment != "" {
			k += " " + comment
		}
		keys = append(keys, k)
	}
	if len(keys) > 20 {
		return jsonError(c, http.StatusBadRequest, "Не больше 20 ключей")
	}
	raw, _ := json.Marshal(keys)
	if err := s.db.Save(&models.Setting{Key: adminKeysKey, Value: string(raw)}).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return s.getSSHKeys(c)
}
