// Package backup делает копии базы Мастера: по расписанию на диск и по запросу из панели.
package backup

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

const prefix = "kvn-backup-"

// Snapshot записывает согласованную копию базы в path. VACUUM INTO безопасен
// при работающем Мастере: пишущие запросы просто подождут.
func Snapshot(db *gorm.DB, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// VACUUM INTO не перезаписывает существующий файл
	_ = os.Remove(path)
	if err := db.Exec("VACUUM INTO ?", path).Error; err != nil {
		return fmt.Errorf("копия базы: %w", err)
	}
	return os.Chmod(path, 0o600)
}

// FileName — имя файла копии для момента t
func FileName(t time.Time) string {
	return prefix + t.UTC().Format("20060102-150405") + ".sqlite"
}

// Rotate оставляет в dir только keep самых свежих копий
func Rotate(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".sqlite") {
			files = append(files, e.Name())
		}
	}
	// Имена содержат время в сортируемом формате
	sort.Strings(files)
	for len(files) > keep {
		if err := os.Remove(filepath.Join(dir, files[0])); err != nil {
			return err
		}
		files = files[1:]
	}
	return nil
}

// Schedule раз в сутки кладёт копию в dir и хранит keep последних.
// Первая копия — сразу при старте Мастера.
func Schedule(db *gorm.DB, dir string, keep int) {
	run := func() {
		path := filepath.Join(dir, FileName(time.Now()))
		if err := Snapshot(db, path); err != nil {
			log.Printf("❌ Бэкап базы не удался: %v", err)
			return
		}
		if err := Rotate(dir, keep); err != nil {
			log.Printf("⚠️ Не удалось удалить старые бэкапы: %v", err)
		}
		log.Printf("💾 Бэкап базы: %s", path)
	}
	go func() {
		run()
		for range time.Tick(24 * time.Hour) {
			run()
		}
	}()
}
