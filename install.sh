#!/bin/bash

# Цветовая палитра
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}=========================================${NC}"
echo -e "${GREEN}🚀 Установка KVN Smart VPN Cluster${NC}"
echo -e "${BLUE}=========================================${NC}"

# 1. Проверка прав Root
if [ "$EUID" -ne 0 ]; then
  echo -e "${RED}❌ Ошибка: Скрипт необходимо запускать от имени root (sudo).${NC}"
  exit 1
fi

# 2. Установка Docker, если его нет
if ! command -v docker &> /dev/null; then
    echo -e "🐳 Docker не найден. Начинаем автоматическую установку..."
    curl -fsSL https://get.docker.com | sh
    echo -e "${GREEN}✅ Docker успешно установлен!${NC}"
else
    echo -e "🐳 Docker уже установлен. Пропускаем..."
fi

# 3. Диалог с пользователем
echo ""
read -p "🌐 Введите ваш домен (например, panel.mysite.com): " DOMAIN
if [ -z "$DOMAIN" ]; then
    echo -e "${RED}❌ Ошибка: Домен обязателен!${NC}"
    exit 1
fi

read -p "🔑 Придумайте пароль администратора (Enter = автогенерация): " ADMIN_PASS
if [ -z "$ADMIN_PASS" ]; then
    ADMIN_PASS=$(tr -dc A-Za-z0-9 </dev/urandom | head -c 12)
    echo -e "Сгенерирован пароль: ${GREEN}$ADMIN_PASS${NC}"
fi

# 4. Генерация ключей
echo "⚙️ Генерация ключей безопасности..."
JWT_SECRET=$(tr -dc A-Za-z0-9 </dev/urandom | head -c 32)
CLUSTER_API_KEY=$(tr -dc A-Za-z0-9 </dev/urandom | head -c 32)

# 5. Создание рабочей директории
INSTALL_DIR="/opt/kvn-panel"
echo "📂 Создание директории $INSTALL_DIR..."
mkdir -p $INSTALL_DIR/data
cd $INSTALL_DIR

# 6. Запись файла .env
cat > .env <<EOF
DOMAIN=$DOMAIN
ADMIN_PASSWORD=$ADMIN_PASS
JWT_SECRET=$JWT_SECRET
CLUSTER_API_KEY=$CLUSTER_API_KEY
EOF

# 7. Генерация Caddyfile
cat > Caddyfile <<EOF
{\$DOMAIN} {
    reverse_proxy master:8080
}
EOF

# 8. Генерация docker-compose.yml
# Замени 'tvoy-login/kvn-panel:latest' на свое будущее имя в Docker Hub!
cat > docker-compose.yml <<EOF
services:
  master:
    image: sud0i/kvn-panel:latest
    container_name: kvn-master
    restart: always
    volumes:
      - ./data:/app/data
      - ./.env:/app/.env

  caddy:
    image: caddy:2-alpine
    container_name: kvn-caddy
    restart: always
    ports:
      - "80:80"
      - "443:443"
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

# 9. Финальный запуск
echo -e "🚀 Скачивание и запуск кластера..."
docker compose up -d

echo -e "\n${BLUE}=========================================${NC}"
echo -e "${GREEN}✅ Установка полностью завершена!${NC}"
echo -e "🌐 Панель доступна по адресу: ${GREEN}https://$DOMAIN${NC}"
echo -e "👤 Логин: (не требуется)"
echo -e "🔑 Пароль: ${GREEN}$ADMIN_PASS${NC}"
echo -e "⚙️ Директория с данными: $INSTALL_DIR"
echo -e "${BLUE}=========================================${NC}"