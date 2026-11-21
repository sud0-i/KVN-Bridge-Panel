package api

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/labstack/echo/v4"
	"github.com/sud0-i/KVN-Bridge-Panel/internal/backup"
	"gorm.io/gorm"
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

const maxBackupUpload = 512 << 20

// uploadBackup accepts the same SQLite file as downloadBackup.
func (s *Server) uploadBackup(c echo.Context) error {
	if !s.restoreMu.TryLock() {
		return jsonError(c, http.StatusConflict, "Восстановление уже запущено")
	}
	defer s.restoreMu.Unlock()

	file, err := os.CreateTemp("", "kvn-restore-*.sqlite")
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, "Не удалось создать временный файл")
	}
	defer os.Remove(file.Name())
	_, copyErr := io.Copy(file, http.MaxBytesReader(c.Response(), c.Request().Body, maxBackupUpload))
	closeErr := file.Close()
	var limit *http.MaxBytesError
	if errors.As(copyErr, &limit) {
		return jsonError(c, http.StatusRequestEntityTooLarge, "Бэкап больше 512 МиБ")
	}
	if copyErr != nil || closeErr != nil {
		return jsonError(c, http.StatusBadRequest, "Не удалось прочитать файл бэкапа")
	}

	name := "kvn-before-restore-" + s.now().UTC().Format("20060102-150405.000000000") + ".sqlite"
	err = backup.Restore(c.Request().Context(), s.db, file.Name(), filepath.Join(s.cfg.BackupDir, name), func(tx *gorm.DB) error {
		if err := EnsureSubTokens(tx); err != nil {
			return err
		}
		return EnsureXHTTPPaths(tx)
	})
	if errors.Is(err, backup.ErrInvalidBackup) {
		return jsonError(c, http.StatusBadRequest, err.Error())
	}
	if err != nil {
		log.Printf("Ошибка восстановления базы: %v", err)
		return jsonError(c, http.StatusInternalServerError, "Не удалось восстановить базу. Проверьте журнал Мастера")
	}
	return c.JSON(http.StatusOK, map[string]string{"previous_backup": name})
}
