package api

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/backup"
)

// downloadBackup отдаёт свежую копию базы файлом
func (s *Server) downloadBackup(c echo.Context) error {
	dir, err := os.MkdirTemp("", "kvn-backup-*")
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Не удалось создать временный файл")
	}
	defer os.RemoveAll(dir)

	name := backup.FileName(s.now())
	path := filepath.Join(dir, name)
	if err := backup.Snapshot(s.db, path); err != nil {
		return jsonError(c, http.StatusInternalServerError, "Не удалось сделать копию базы")
	}
	return c.Attachment(path, name)
}
