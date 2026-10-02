#!/bin/bash
set -Eeuo pipefail  # -E: ловушка ERR срабатывает и внутри функций (всё тело — в main)

# Цветовая палитра
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
NC='\033[0m'

IMAGE="${KVN_IMAGE:-ghcr.io/sud0-i/kvn-bridge-panel:latest}"
REPO_RAW="https://raw.githubusercontent.com/sud0-i/KVN-Bridge-Panel/main"
INSTALL_DIR="/opt/kvn-panel"

fail() { echo -e "${RED}❌ $*${NC}"; exit 1; }

# Под set -e любая упавшая команда завершает скрипт — пусть он хотя бы скажет, какая
trap 'echo -e "${RED}❌ Установка прервана: строка $LINENO, команда: $BASH_COMMAND${NC}" >&2' ERR

# Cloudflare WARP в режиме прокси на 127.0.0.1:40000 — те же шаги, что в ansible/deploy_node.yml.
# Ошибка не прерывает установку: без WARP сервер просто выпускает всё со своего IP.
install_warp() {
    # Скрипт часто приходит через curl | bash: stdin — это сам скрипт, поэтому команды
    # его не читают (</dev/null). Таймауты — чтобы при блокировке Cloudflare (например,
    # из России) установщик сдавался за минуту, а не висел.
    local apt_opts=(-o Acquire::http::Timeout=20 -o Acquire::https::Timeout=20 -o Acquire::Retries=1)
    export DEBIAN_FRONTEND=noninteractive
    echo "   Пакеты и репозиторий Cloudflare..."
    timeout 300 apt-get install -y -qq gnupg lsb-release </dev/null >/dev/null || return 1
    curl -fsSL --max-time 30 https://pkg.cloudflareclient.com/pubkey.gpg \
        | gpg --yes --dearmor -o /usr/share/keyrings/cloudflare-warp-archive-keyring.gpg || return 1
    echo "deb [signed-by=/usr/share/keyrings/cloudflare-warp-archive-keyring.gpg] https://pkg.cloudflareclient.com/ $(lsb_release -cs) main" \
        > /etc/apt/sources.list.d/cloudflare-client.list || return 1
    timeout 300 apt-get "${apt_opts[@]}" update -qq </dev/null >/dev/null || return 1
    timeout 600 apt-get "${apt_opts[@]}" install -y -qq cloudflare-warp </dev/null >/dev/null || return 1
    echo "   Регистрация в Cloudflare (до минуты)..."
    if [ ! -f /var/lib/cloudflare-warp/reg.json ]; then
        timeout 60 warp-cli --accept-tos registration new </dev/null || return 1
    fi
    timeout 30 warp-cli --accept-tos mode proxy </dev/null \
        && timeout 30 warp-cli --accept-tos proxy port 40000 </dev/null \
        && timeout 30 warp-cli --accept-tos connect </dev/null
}

# Случайная строка из букв и цифр. Сначала читаем конечный кусок /dev/urandom,
# потом фильтруем: вариант «tr </dev/urandom | head» под pipefail падает с SIGPIPE.
random_string() {
    local s
    s=$(head -c 1024 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9')
    printf '%s' "${s:0:$1}"
}

# Всё тело — в функции: при «curl … | bash» bash читает скрипт из stdin по ходу выполнения,
# и команда, которая тоже читает stdin (docker compose exec, apt-get…), «съедала» бы остаток
# скрипта — установка молча обрывалась. Функцию bash читает целиком до запуска.
main() {
echo -e "${BLUE}=========================================${NC}"
echo -e "${GREEN}🚀 Установка KVN Smart VPN Cluster${NC}"
echo -e "${BLUE}=========================================${NC}"

# 1. Проверка прав Root
[ "$EUID" -eq 0 ] || fail "Скрипт необходимо запускать от имени root (sudo)."

# 2. Установка Docker, если его нет
if ! command -v docker &> /dev/null; then
    echo -e "🐳 Docker не найден. Начинаем автоматическую установку..."
    curl -fsSL https://get.docker.com | sh
    echo -e "${GREEN}✅ Docker успешно установлен!${NC}"
else
    echo -e "🐳 Docker уже установлен. Пропускаем..."
fi

# 3. Диалог с пользователем (читаем с терминала: скрипт может прийти через curl | bash)
echo ""
read -p "🌐 Введите ваш домен (например, panel.mysite.com): " DOMAIN </dev/tty
[ -n "$DOMAIN" ] || fail "Домен обязателен!"

read -p "🔑 Придумайте пароль администратора (Enter = автогенерация): " ADMIN_PASS </dev/tty
if [ -z "$ADMIN_PASS" ]; then
    ADMIN_PASS=$(random_string 16)
    echo -e "Сгенерирован пароль: ${GREEN}$ADMIN_PASS${NC}"
fi

echo ""
echo "Можно сразу сделать этот сервер ещё и мостом — точкой входа (VPN на порту 443)."
echo "Удобно для старта, но для постоянной работы Мастер лучше держать отдельно."
read -p "🌉 Установить мост на этот сервер? [y/N]: " WITH_BRIDGE </dev/tty
case "$WITH_BRIDGE" in [yYдД]*) WITH_BRIDGE=1 ;; *) WITH_BRIDGE=0 ;; esac

WITH_WARP=0
if [ "$WITH_BRIDGE" = 1 ]; then
    echo ""
    echo "Если выходных нод не будет, этот сервер сам выпускает трафик в интернет (одиночный режим)."
    echo "Cloudflare WARP позволит отправлять часть сайтов через Cloudflare (вкладка «Маршрутизация»)."
    read -p "☁️ Поставить Cloudflare WARP? [Y/n]: " WITH_WARP </dev/tty
    case "$WITH_WARP" in [nNнН]*) WITH_WARP=0 ;; *) WITH_WARP=1 ;; esac
fi

SERVER_IP=""
if [ "$WITH_BRIDGE" = 1 ]; then
    # Домен должен смотреть на этот сервер — иначе не выдастся сертификат
    SERVER_IP=$(getent ahostsv4 "$DOMAIN" | awk 'NR==1{print $1}')
    [ -n "$SERVER_IP" ] || fail "Домен $DOMAIN не резолвится. Создайте A-запись на IP этого сервера."
    echo -e "IP сервера (по DNS): ${GREEN}$SERVER_IP${NC}"
fi

# 4. Генерация ключей
echo "⚙️ Генерация ключей безопасности..."
JWT_SECRET=$(random_string 48)
# Секретный путь панели: по адресу домена открывается заглушка, панель — только здесь
ADMIN_PATH=$(random_string 24)

# 5. Создание рабочей директории
echo "📂 Создание директории $INSTALL_DIR..."
mkdir -p "$INSTALL_DIR/data"
cd "$INSTALL_DIR"

# 6. Запись файла .env (читать может только root)
(umask 077; cat > .env <<EOF
DOMAIN=$DOMAIN
ADMIN_PASSWORD=$ADMIN_PASS
JWT_SECRET=$JWT_SECRET
ADMIN_PATH=$ADMIN_PATH
EOF
)

# 7. Caddyfile и docker-compose.yml
if [ "$WITH_BRIDGE" = 1 ]; then
    # Порт 443 займёт Xray; Caddy слушает только 127.0.0.1:8443
    curl -fsSL "$REPO_RAW/Caddyfile.combined" -o Caddyfile
    MASTER_PORTS='    ports:
      - "127.0.0.1:8080:8080"'
    CADDY_PORTS='      - "80:80"
      - "127.0.0.1:8443:8443"'
else
    cat > Caddyfile <<'EOF'
{$DOMAIN} {
	reverse_proxy master:8080
}
EOF
    MASTER_PORTS=''
    CADDY_PORTS='      - "80:80"
      - "443:443"'
fi

cat > docker-compose.yml <<EOF
services:
  master:
    image: $IMAGE
    container_name: kvn-master
    restart: always
$MASTER_PORTS
    volumes:
      - ./data:/app/data
      - ./.env:/app/.env

  caddy:
    image: caddy:2-alpine
    container_name: kvn-caddy
    restart: always
    ports:
$CADDY_PORTS
    environment:
      - DOMAIN=\${DOMAIN}
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile
      - caddy_data:/data
      - caddy_config:/config
    depends_on:
      - master

volumes:
  caddy_data:
  caddy_config:
EOF

# 8. Запуск
echo -e "🚀 Скачивание и запуск Мастера..."
docker compose --env-file .env up -d

# 9. Мост на этом же сервере
if [ "$WITH_BRIDGE" = 1 ]; then
    echo -e "🌉 Установка моста..."

    # Сетевой тюнинг, как на остальных нодах (BBR, fq, буферы TCP); в контейнерах часть не применится
    echo tcp_bbr > /etc/modules-load.d/kvn-bbr.conf
    modprobe tcp_bbr 2>/dev/null || true
    curl -fsSL "$REPO_RAW/ansible/files/99-kvn-network.conf" -o /etc/sysctl.d/99-kvn-network.conf \
        && { sysctl --system >/dev/null 2>&1 || true; } \
        || echo -e "${YELLOW}⚠️ Не удалось скачать сетевые настройки — пропускаю${NC}"

    if ! command -v xray &> /dev/null; then
        bash -c "$(curl -fsSL https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ install
    fi

    if [ "$WITH_WARP" = 1 ]; then
        echo -e "☁️ Установка Cloudflare WARP..."
        if install_warp; then
            echo -e "${GREEN}✅ WARP работает в режиме прокси на 127.0.0.1:40000${NC}"
        else
            echo -e "${YELLOW}⚠️ WARP не поставился — трафик «через WARP» пойдёт с IP сервера. Панель покажет причину у моста.${NC}"
        fi
    fi

    # Ждём, пока Мастер поднимется
    for _ in $(seq 1 30); do
        curl -fs -o /dev/null http://127.0.0.1:8080/ && break
        sleep 2
    done

    # Мастер регистрирует ноду и отдаёт её токен и приватный ключ
    NODE_ENV=$(docker compose exec -T master ./kvn-master local-bridge -ip "$SERVER_IP" -domain "$DOMAIN" </dev/null 2>/dev/null) \
        || fail "Не удалось зарегистрировать мост (docker compose logs master)"

    mkdir -p /etc/vpn-agent
    chmod 700 /etc/vpn-agent
    (umask 077; {
        echo "MASTER_URL=http://127.0.0.1:8080"
        echo "$NODE_ENV"
    } > /etc/vpn-agent/.env)

    docker compose cp master:/app/build/agent_linux_amd64 /usr/local/bin/vpn-agent
    chmod 755 /usr/local/bin/vpn-agent

    cat > /etc/systemd/system/vpn-agent.service <<'EOF'
[Unit]
Description=VPN Cluster Agent
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/etc/vpn-agent
ExecStart=/usr/local/bin/vpn-agent
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable --now xray
    systemctl enable vpn-agent
    systemctl restart vpn-agent
fi

echo -e "\n${BLUE}=========================================${NC}"
echo -e "${GREEN}✅ Установка полностью завершена!${NC}"
echo -e "🌐 Панель доступна по адресу: ${GREEN}https://$DOMAIN/$ADMIN_PATH/${NC}"
echo -e "${YELLOW}   Сохраните этот адрес: по https://$DOMAIN открывается заглушка. Путь — ADMIN_PATH в $INSTALL_DIR/.env${NC}"
echo -e "🔑 Пароль: ${GREEN}$ADMIN_PASS${NC}"
echo -e "⚙️ Директория с данными: $INSTALL_DIR"
if [ "$WITH_BRIDGE" = 1 ]; then
    echo -e "🌉 Мост: этот сервер ($SERVER_IP). Логи агента: journalctl -u vpn-agent -f"
    echo -e "ℹ️ Сейчас это одиночный сервер: VPN уже работает, трафик выходит в интернет с IP $SERVER_IP."
    echo -e "   Чтобы трафик выходил с другого сервера, добавьте выходную ноду в панели (вкладка «Серверы»)."
fi
echo -e "${BLUE}=========================================${NC}"
}

main "$@"
