package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
)

const ctxNode = "node"

func (s *Server) login(c echo.Context) error {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат")
	}

	if !secureEqual(req.Password, s.cfg.AdminPassword) {
		log.Printf("🔐 Неудачная попытка входа с %s", c.RealIP())
		return jsonError(c, http.StatusUnauthorized, "Неверный пароль")
	}
	// Второй фактор: код из приложения или резервный код
	if st := loadTOTP(s.db); st.Enabled {
		if err := s.checkSecondFactor(&st, req.Code); err != nil {
			if err != errNeedCode {
				log.Printf("🔐 Неверный код 2FA с %s", c.RealIP())
			}
			return c.JSON(http.StatusUnauthorized, map[string]any{"error": err.Error(), "need_code": true})
		}
	}

	// Пароль верный -> Выдаем JWT токен на 24 часа
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"admin": true,
		"exp":   s.now().Add(24 * time.Hour).Unix(),
	})
	tokenString, err := token.SignedString(s.cfg.JWTSecret)
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка генерации токена")
	}
	return c.JSON(http.StatusOK, map[string]string{"token": tokenString})
}

// requireAdmin пропускает запрос только с валидным JWT администратора
func (s *Server) requireAdmin(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		tokenString, ok := strings.CutPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
		if !ok || tokenString == "" {
			return jsonError(c, http.StatusUnauthorized, "Токен не предоставлен")
		}

		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method")
			}
			return s.cfg.JWTSecret, nil
		}, jwt.WithExpirationRequired(), jwt.WithTimeFunc(s.now))
		if err != nil || !token.Valid {
			return jsonError(c, http.StatusUnauthorized, "Недействительный токен")
		}
		if claims, ok := token.Claims.(jwt.MapClaims); !ok || claims["admin"] != true {
			return jsonError(c, http.StatusUnauthorized, "Недействительный токен")
		}
		return next(c)
	}
}

// requireNode находит ноду по её персональному токену. Без токена — 404, как будто тут ничего нет.
func (s *Server) requireNode(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		token := c.Request().Header.Get(protocol.NodeTokenHeader)
		if token == "" {
			return notFound(c)
		}
		var node models.Node
		if err := s.db.Where("token_hash = ?", hashToken(token)).First(&node).Error; err != nil {
			return notFound(c)
		}
		c.Set(ctxNode, &node)
		return next(c)
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// secureEqual сравнивает строки за постоянное время (защита от timing-атак)
func secureEqual(a, b string) bool {
	if b == "" {
		return false
	}
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
