package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"gorm.io/gorm"
)

const (
	defaultIPLimit = 5
	gigabyte       = 1 << 30
	maxImportLines = 500
)

// Токен подписки: наши — 32 hex-символа, но при переносе из других панелей
// принимаем любые безопасные для URL строки разумной длины
var subTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// EnsureSubTokens выдаёт токены подписки пользователям, созданным до их появления
func EnsureSubTokens(db *gorm.DB) error {
	var users []models.User
	if err := db.Where("sub_token = '' OR sub_token IS NULL").Find(&users).Error; err != nil {
		return err
	}
	for _, u := range users {
		if err := db.Model(&models.User{}).Where("id = ?", u.ID).Update("sub_token", models.NewSubToken()).Error; err != nil {
			return err
		}
	}
	return nil
}

func isUniqueErr(err error) bool {
	return errors.Is(err, gorm.ErrDuplicatedKey) || (err != nil && strings.Contains(err.Error(), "UNIQUE"))
}

func validateName(name string) error {
	if name == "" || len(name) > 64 {
		return errors.New("Имя должно быть от 1 до 64 символов")
	}
	return nil
}

func (s *Server) listUsers(c echo.Context) error {
	var users []models.User
	if err := s.db.Order("created_at").Find(&users).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return c.JSON(http.StatusOK, users)
}

func (s *Server) findUser(c echo.Context) (models.User, error) {
	var user models.User
	err := s.db.Where("id = ?", c.Param("id")).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return user, jsonError(c, http.StatusNotFound, "Пользователь не найден")
	}
	if err != nil {
		return user, jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return user, nil
}

func (s *Server) createUser(c echo.Context) error {
	var req struct {
		Name         string     `json:"name"`
		IPLimit      int        `json:"ip_limit"`
		TrafficQuota int64      `json:"traffic_quota"` // байты, 0 = без лимита
		ExpiresAt    *time.Time `json:"expires_at"`
		SubToken     string     `json:"sub_token"` // необязательно: перенос ссылки из старой панели
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат данных")
	}

	req.Name = strings.TrimSpace(req.Name)
	if err := validateName(req.Name); err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	if req.IPLimit < 0 || req.TrafficQuota < 0 {
		return jsonError(c, http.StatusBadRequest, "Лимиты не могут быть отрицательными")
	}
	req.SubToken = strings.TrimSpace(req.SubToken)
	if req.SubToken != "" && !subTokenRe.MatchString(req.SubToken) {
		return jsonError(c, http.StatusBadRequest, "Токен подписки: 8–64 символа, латиница, цифры, - и _")
	}

	newUser := models.User{
		ID:           uuid.NewString(),
		Name:         req.Name,
		SubToken:     req.SubToken,
		IPLimit:      req.IPLimit,
		TrafficQuota: req.TrafficQuota,
		ExpiresAt:    req.ExpiresAt,
		Status:       "active",
	}
	if newUser.IPLimit == 0 {
		newUser.IPLimit = defaultIPLimit
	}

	if err := s.db.Create(&newUser).Error; err != nil {
		if isUniqueErr(err) {
			return jsonError(c, http.StatusConflict, "Пользователь с таким именем или токеном подписки уже есть")
		}
		return jsonError(c, http.StatusInternalServerError, "Ошибка сохранения")
	}
	return c.JSON(http.StatusCreated, newUser)
}

// updateUser — правка имени и лимитов. Поля передаются целиком: expires_at = null снимает срок.
func (s *Server) updateUser(c echo.Context) error {
	var req struct {
		Name         string     `json:"name"`
		IPLimit      int        `json:"ip_limit"`
		TrafficQuota int64      `json:"traffic_quota"`
		ExpiresAt    *time.Time `json:"expires_at"`
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат данных")
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := validateName(req.Name); err != nil {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	if req.IPLimit < 0 || req.TrafficQuota < 0 {
		return jsonError(c, http.StatusBadRequest, "Лимиты не могут быть отрицательными")
	}

	user, err := s.findUser(c)
	if err != nil {
		return err
	}
	err = s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"name":          req.Name,
		"ip_limit":      req.IPLimit,
		"traffic_quota": req.TrafficQuota,
		"expires_at":    req.ExpiresAt,
	}).Error
	if isUniqueErr(err) {
		return jsonError(c, http.StatusConflict, "Пользователь с таким именем уже есть")
	}
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка сохранения")
	}
	return s.respondUser(c, user.ID)
}

func (s *Server) respondUser(c echo.Context, id string) error {
	var user models.User
	if err := s.db.Where("id = ?", id).First(&user).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка БД")
	}
	return c.JSON(http.StatusOK, user)
}

func (s *Server) resetUserTraffic(c echo.Context) error {
	user, err := s.findUser(c)
	if err != nil {
		return err
	}
	if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).
		Updates(map[string]any{"traffic_up": 0, "traffic_down": 0}).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка сохранения")
	}
	return s.respondUser(c, user.ID)
}

// rotateUser выдаёт новую ссылку подписки (what=link) или новый ключ VLESS (what=key).
// Новый ключ отключает все настроенные устройства, пока они не обновят подписку.
func (s *Server) rotateUser(c echo.Context) error {
	var req struct {
		What string `json:"what"`
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат данных")
	}
	user, err := s.findUser(c)
	if err != nil {
		return err
	}
	id := user.ID
	var update map[string]any
	switch req.What {
	case "link":
		update = map[string]any{"sub_token": models.NewSubToken()}
	case "key":
		id = uuid.NewString()
		update = map[string]any{"id": id}
	default:
		return jsonError(c, http.StatusBadRequest, "what должен быть link или key")
	}
	if err := s.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(update).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "Ошибка сохранения")
	}
	return s.respondUser(c, id)
}

// importLine разбирает строку «имя токен [квота_ГБ] [ГГГГ-ММ-ДД]».
// Вместо токена можно вставить старую ссылку целиком — берётся последний сегмент пути.
func importLine(line string) (models.User, error) {
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == ';'
	})
	if len(fields) < 2 || len(fields) > 4 {
		return models.User{}, errors.New("нужно: имя токен [квота_ГБ] [ГГГГ-ММ-ДД]")
	}
	user := models.User{Name: fields[0], IPLimit: defaultIPLimit, Status: "active"}
	if err := validateName(user.Name); err != nil {
		return user, err
	}

	token := strings.TrimRight(fields[1], "/")
	if i := strings.LastIndex(token, "/"); i >= 0 {
		token = token[i+1:]
	}
	if i := strings.IndexAny(token, "?#"); i >= 0 {
		token = token[:i]
	}
	if !subTokenRe.MatchString(token) {
		return user, fmt.Errorf("токен %q: 8–64 символа, латиница, цифры, - и _", token)
	}
	user.SubToken = token

	for _, f := range fields[2:] {
		if d, err := time.Parse("2006-01-02", f); err == nil {
			end := d.Add(24*time.Hour - time.Second) // до конца указанного дня
			user.ExpiresAt = &end
			continue
		}
		gb, err := strconv.ParseFloat(strings.TrimSuffix(strings.ToLower(f), "gb"), 64)
		if err != nil || gb < 0 || math.IsInf(gb, 0) || math.IsNaN(gb) || gb > 1e6 {
			return user, fmt.Errorf("%q — не квота в ГБ и не дата ГГГГ-ММ-ДД", f)
		}
		user.TrafficQuota = int64(gb * gigabyte)
	}
	return user, nil
}

// importUsers переносит пользователей из другой панели со старыми ссылками подписки.
// Всё или ничего: при любой ошибке не создаётся никто.
func (s *Server) importUsers(c echo.Context) error {
	var req struct {
		Text string `json:"text"`
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "Неверный формат данных")
	}

	var users []models.User
	var problems []string
	names, tokens := map[string]bool{}, map[string]bool{}
	for i, raw := range strings.Split(req.Text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(users)+len(problems) >= maxImportLines {
			return jsonError(c, http.StatusBadRequest, fmt.Sprintf("Не больше %d строк за раз", maxImportLines))
		}
		u, err := importLine(line)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("строка %d: %v", i+1, err))
			continue
		case names[u.Name]:
			problems = append(problems, fmt.Sprintf("строка %d: имя %s повторяется", i+1, u.Name))
			continue
		case tokens[u.SubToken]:
			problems = append(problems, fmt.Sprintf("строка %d: токен повторяется", i+1))
			continue
		}
		var taken int64
		s.db.Model(&models.User{}).Where("name = ? OR sub_token = ?", u.Name, u.SubToken).Count(&taken)
		if taken > 0 {
			problems = append(problems, fmt.Sprintf("строка %d: пользователь %s или его токен уже есть", i+1, u.Name))
			continue
		}
		names[u.Name], tokens[u.SubToken] = true, true
		u.ID = uuid.NewString()
		users = append(users, u)
	}

	if len(problems) > 0 {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "Ничего не импортировано", "problems": problems})
	}
	if len(users) == 0 {
		return jsonError(c, http.StatusBadRequest, "Список пуст")
	}
	if err := s.db.Create(&users).Error; err != nil {
		if isUniqueErr(err) {
			return jsonError(c, http.StatusConflict, "Имя или токен уже заняты")
		}
		return jsonError(c, http.StatusInternalServerError, "Ошибка сохранения")
	}
	return c.JSON(http.StatusCreated, map[string]any{"created": len(users)})
}

func (s *Server) setUserStatus(c echo.Context) error {
	var req struct {
		Status string `json:"status"`
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "bad format")
	}
	if req.Status != "active" && req.Status != "blocked" {
		return jsonError(c, http.StatusBadRequest, "status должен быть active или blocked")
	}

	res := s.db.Model(&models.User{}).Where("id = ?", c.Param("id")).Update("status", req.Status)
	if res.Error != nil {
		return jsonError(c, http.StatusInternalServerError, "DB error")
	}
	if res.RowsAffected == 0 {
		return jsonError(c, http.StatusNotFound, "Пользователь не найден")
	}
	return c.NoContent(http.StatusOK)
}

func (s *Server) deleteUser(c echo.Context) error {
	if err := s.db.Where("id = ?", c.Param("id")).Delete(&models.User{}).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "DB error")
	}
	return c.NoContent(http.StatusNoContent)
}
