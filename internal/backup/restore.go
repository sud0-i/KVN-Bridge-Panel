package backup

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInvalidBackup = errors.New("файл не является читаемым бэкапом KVN")

type restoreTable struct {
	name     string
	required []string
}

var restoreTables = []restoreTable{
	{"users", []string{"id", "name"}},
	{"nodes", []string{"ip", "type", "token_hash"}},
	{"settings", []string{"key", "value"}},
	{"notify_messages", nil},
	{"link_samples", nil},
	{"node_metrics", nil},
	{"terminal_sessions", nil},
}

// Restore checks the incoming SQLite file, saves the current database, then
// replaces the data in one transaction while keeping the local bridge and login.
func Restore(ctx context.Context, database *gorm.DB, path, safetyPath string, normalize func(*gorm.DB) error) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" && filepath.VolumeName(absolute) != "" {
		absolute = "/" + filepath.ToSlash(absolute)
	}
	source := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: "mode=ro"}).String()
	return database.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		// Use fresh statements on the pinned connection. A raw PRAGMA scan can
		// otherwise leave its destination model attached to the following query.
		conn = conn.Session(&gorm.Session{NewDB: true})
		if err := conn.Exec("ATTACH DATABASE ? AS incoming", source).Error; err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidBackup, err)
		}
		defer conn.WithContext(context.Background()).Exec("DETACH DATABASE incoming")

		var integrity string
		if err := conn.Raw("PRAGMA incoming.integrity_check(1)").Scan(&integrity).Error; err != nil || integrity != "ok" {
			return ErrInvalidBackup
		}
		columns := make(map[string]string, len(restoreTables))
		for _, table := range restoreTables {
			list, err := sharedColumns(conn, table)
			if err != nil {
				return fmt.Errorf("проверка таблицы %s: %w", table.name, err)
			}
			columns[table.name] = list
		}

		if err := Snapshot(conn, safetyPath); err != nil {
			return err
		}
		return conn.Transaction(func(tx *gorm.DB) error {
			var existing []models.Node
			if err := tx.Find(&existing).Error; err != nil {
				return fmt.Errorf("чтение текущих нод: %w", err)
			}
			var local []models.Node
			for _, node := range existing {
				if node.IsLocalBridge() {
					local = append(local, node)
				}
			}
			var loginSettings []models.Setting
			if err := tx.Where("key IN ?", []string{"totp", "panel_channel"}).Find(&loginSettings).Error; err != nil {
				return fmt.Errorf("чтение параметров входа: %w", err)
			}

			for _, table := range restoreTables {
				name := quotedIdentifier(table.name)
				if err := tx.Exec("DELETE FROM main." + name).Error; err != nil {
					return err
				}
				if list := columns[table.name]; list != "" {
					query := "INSERT INTO main." + name + " (" + list + ") SELECT " + list + " FROM incoming." + name
					if err := tx.Exec(query).Error; err != nil {
						return err
					}
				}
			}

			if len(local) > 0 {
				if err := tx.Where("local_bridge = ? OR (reality_dest <> '' AND COALESCE(self_steal_domain, '') = '')", true).Delete(&models.Node{}).Error; err != nil {
					return err
				}
				if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&local).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("key IN ?", []string{"totp", "panel_channel"}).Delete(&models.Setting{}).Error; err != nil {
				return err
			}
			if len(loginSettings) > 0 {
				if err := tx.Create(&loginSettings).Error; err != nil {
					return err
				}
			}
			// A queued reboot or notification from the backup should not run again.
			if err := tx.Exec("UPDATE nodes SET pending_action = '', pending_at = NULL").Error; err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM notify_messages WHERE sent_at IS NULL AND COALESCE(failed, 0) = 0").Error; err != nil {
				return err
			}
			return normalize(tx)
		})
	})
}

func sharedColumns(db *gorm.DB, table restoreTable) (string, error) {
	var kind string
	if err := db.Raw("SELECT type FROM pragma_table_list WHERE schema = 'incoming' AND name = ?", table.name).Scan(&kind).Error; err != nil {
		return "", err
	}
	if kind == "" && len(table.required) == 0 {
		return "", nil
	}
	if kind != "table" {
		return "", fmt.Errorf("%w: таблица %s", ErrInvalidBackup, table.name)
	}
	var incoming, current []string
	if err := db.Raw("SELECT name FROM pragma_table_info(?, 'incoming')", table.name).Scan(&incoming).Error; err != nil {
		return "", err
	}
	if err := db.Raw("SELECT name FROM pragma_table_info(?, 'main')", table.name).Scan(&current).Error; err != nil {
		return "", err
	}
	set := make(map[string]bool, len(incoming))
	for _, column := range incoming {
		set[column] = true
	}
	for _, required := range table.required {
		if !set[required] {
			return "", fmt.Errorf("%w: столбец %s.%s", ErrInvalidBackup, table.name, required)
		}
	}
	var shared []string
	for _, column := range current {
		if set[column] {
			shared = append(shared, quotedIdentifier(column))
		}
	}
	return strings.Join(shared, ", "), nil
}

func quotedIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
