package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Обновление панели кнопкой. Панель (мастер) работает в Docker на сервере моста, и
// обновить себя изнутри контейнера не может: `docker compose up -d` убил бы процесс,
// который его запустил. Поэтому обновляет агент моста — снаружи, через systemd-run:
// так обновление доходит до конца, что бы ни случилось с панелью и терминалом.
// Новая версия должна ответить за 90 секунд, иначе возвращаем прежний образ.
//
// Канал обновлений: IMAGE_TAG (latest — стабильный, beta — тестовый) агент ставит в
// docker-compose.yml вместо прежнего тега; при откате возвращается и файл, и образ.

var (
	panelDir       = envDefault("KVN_PANEL_DIR", "/opt/kvn-panel")
	panelUpdateLog = "/var/log/kvn-panel-update.log"
)

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

const panelUpdateScript = `set -eu
cd "$PANEL_DIR"
exec >>"$UPDATE_LOG" 2>&1
echo "=== $(date '+%F %T') обновление панели"
ref=$(docker inspect -f '{{.Config.Image}}' kvn-master)
old=$(docker inspect -f '{{.Image}}' kvn-master)
cp docker-compose.yml docker-compose.yml.kvn-prev
if [ -n "${IMAGE_TAG:-}" ]; then
  # Меняем только тег нашего образа; свой образ (KVN_IMAGE) не трогаем
  sed -i -E "s#^([[:space:]]*image:[[:space:]]*[^[:space:]]*/kvn-bridge-panel):[^[:space:]]+#\1:$IMAGE_TAG#" docker-compose.yml
  echo "образ: $(grep -E 'image:.*kvn-bridge-panel' docker-compose.yml | head -1 | tr -s ' ')"
fi
timeout 900 docker compose pull master
docker compose up -d master
new=$(docker inspect -f '{{.Image}}' kvn-master)
if [ "$new" = "$old" ]; then rm -f docker-compose.yml.kvn-prev; echo "RESULT: unchanged"; exit 0; fi
i=0
while [ $i -lt 45 ]; do
  code=$(curl -s -o /dev/null -m 3 -w '%{http_code}' http://127.0.0.1:8080/ || true)
  if [ "$code" != "000" ] && [ "$code" != "" ]; then rm -f docker-compose.yml.kvn-prev; echo "RESULT: updated"; exit 0; fi
  i=$((i+1)); sleep 2
done
echo "новая версия не ответила за 90 с — возвращаю прежнюю"
mv docker-compose.yml.kvn-prev docker-compose.yml
docker tag "$old" "$ref"
docker compose up -d master
echo "RESULT: rolled back"
exit 3
`

// panelUpdateScriptRun — что запускать (в тестах — с коротким ожиданием)
var panelUpdateScriptRun = panelUpdateScript

// updatePanel запускает обновление и ждёт его окончания; итог — для карточки в панели
// panelChannels — канал из панели → тег образа
var panelChannels = map[string]string{"stable": "latest", "beta": "beta"}

func updatePanel(channel string) (string, error) {
	if _, err := os.Stat(filepath.Join(panelDir, "docker-compose.yml")); err != nil {
		return "", fmt.Errorf("на этом сервере нет панели (%s)", panelDir)
	}
	out, err := runCmd("systemd-run", "--unit=kvn-panel-update", "--collect", "--wait", "--quiet",
		"--setenv=PANEL_DIR="+panelDir, "--setenv=UPDATE_LOG="+panelUpdateLog, "--setenv=IMAGE_TAG="+panelChannels[channel],
		"/bin/sh", "-c", panelUpdateScriptRun)
	last := lastResult()
	switch {
	case err == nil && last == "unchanged":
		return "уже последняя версия", nil
	case err == nil && channel == "beta":
		return "готово (тестовая версия)", nil
	case err == nil:
		return "готово", nil
	case last == "rolled back":
		return "", fmt.Errorf("новая версия не запустилась — вернул прежнюю (журнал: %s)", panelUpdateLog)
	case strings.Contains(string(out), "already"):
		return "", fmt.Errorf("обновление уже идёт")
	default:
		return "", fmt.Errorf("%s (журнал: %s)", tailLine(string(out), err), panelUpdateLog)
	}
}

// lastResult — что записал скрипт последним в «RESULT: …»
func lastResult() string {
	raw, err := os.ReadFile(panelUpdateLog)
	if err != nil {
		return ""
	}
	res := ""
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "RESULT: ") {
			res = strings.TrimPrefix(l, "RESULT: ")
		} else if strings.HasPrefix(l, "=== ") {
			res = ""
		}
	}
	return res
}

func tailLine(out string, err error) string {
	if s := strings.TrimSpace(out); s != "" {
		lines := strings.Split(s, "\n")
		return lines[len(lines)-1]
	}
	return err.Error()
}
