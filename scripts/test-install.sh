#!/bin/bash
# Прогоняет install.sh целиком в песочнице: внешние команды (docker, systemctl,
# curl, getent) заменены заглушками, ответы подаются на stdin, файлы пишутся
# во временный каталог. Ловит ошибки, из-за которых скрипт молча выходит.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
mkdir -p "$T/bin" "$T/etc/systemd" "$T/etc/modules-load.d" "$T/etc/sysctl.d" "$T/keyrings" "$T/apt"

cat > "$T/bin/docker" <<'STUB'
#!/bin/bash
case "$*" in
  *"exec -T master"*) echo "NODE_TOKEN=tok"; echo "PRIVATE_KEY=priv" ;;
  *"compose cp"*) : > "${@: -1}" ;;
esac
STUB
cat > "$T/bin/curl" <<STUB
#!/bin/bash
prev=""; for a in "\$@"; do [ "\$prev" = "-o" ] && cp "$ROOT/Caddyfile.combined" "\$a"; prev=\$a; done
STUB
printf '#!/bin/bash\n' > "$T/bin/systemctl"
printf '#!/bin/bash\n' > "$T/bin/xray"
printf '#!/bin/bash\n' > "$T/bin/modprobe"
printf '#!/bin/bash\n' > "$T/bin/apt-get"
printf '#!/bin/bash\necho jammy\n' > "$T/bin/lsb_release"
printf '#!/bin/bash\ncat >/dev/null; prev=""; for a in "$@"; do [ "$prev" = -o ] && : > "$a"; prev=$a; done\n' > "$T/bin/gpg"
printf '#!/bin/bash\necho "$*" >> "%s/warp.log"\n' "$T" > "$T/bin/warp-cli"
printf '#!/bin/bash\n' > "$T/bin/sysctl"
printf '#!/bin/bash\necho "203.0.113.7 STREAM $2"\n' > "$T/bin/getent"
chmod +x "$T"/bin/*

# Проверку root отключаем: в CI скрипт идёт от обычного пользователя, а пишет только во временный каталог.
# $1 — чем заменить </dev/tty у вопросов установщика.
make_copy() {
    sed -e "s|</dev/tty|$1|" \
        -e 's|\[ "\$EUID" -eq 0 \]|true|' \
        -e "s|/opt/kvn-panel|$T/opt|" \
        -e "s|/etc/vpn-agent|$T/etc/vpn-agent|g" \
        -e "s|/etc/systemd/system|$T/etc/systemd|" \
        -e "s|/etc/modules-load.d|$T/etc/modules-load.d|g" \
        -e "s|/etc/sysctl.d|$T/etc/sysctl.d|g" \
        -e "s|/usr/share/keyrings|$T/keyrings|g" \
        -e "s|/etc/apt/sources.list.d|$T/apt|g" \
        -e "s|/var/lib/cloudflare-warp|$T/warp|g" \
        -e "s|/usr/local/bin/vpn-agent|$T/vpn-agent|g" \
        "$ROOT/install.sh"
}
make_copy "" > "$T/install.sh"            # ответы — через stdin
make_copy "<\\&3" > "$T/install-pipe.sh"  # ответы — через дескриптор 3, stdin — сам скрипт

run() {
    rm -rf "$T/opt" "$T/etc/vpn-agent"
    printf '%b' "$1" | PATH="$T/bin:$PATH" bash "$T/install.sh" > "$T/out.log" 2>&1 \
        || { cat "$T/out.log"; echo "FAIL: $2"; exit 1; }
    grep -q "Установка полностью завершена" "$T/out.log" || { cat "$T/out.log"; echo "FAIL: $2"; exit 1; }
}

# 1. Только мастер, пароль сгенерирован (Enter)
run 'panel.example.com\n\nn\n' "мастер, автопароль"
[ "$(stat -c %a "$T/opt/.env")" = 600 ] || { echo "FAIL: права .env"; exit 1; }
grep -Eq '^ADMIN_PASSWORD=[A-Za-z0-9]{16}$' "$T/opt/.env" || { echo "FAIL: пароль"; exit 1; }
grep -Eq '^ADMIN_PATH=[A-Za-z0-9]{24}$' "$T/opt/.env" || { echo "FAIL: ADMIN_PATH"; exit 1; }
grep -Eq '^JWT_SECRET=[A-Za-z0-9]{48}$' "$T/opt/.env" || { echo "FAIL: JWT_SECRET"; exit 1; }

# 2. Мастер + мост, свой пароль, без WARP
run 'panel.example.com\nmypass\ny\nn\n' "мастер + мост"
[ ! -f "$T/warp.log" ] || { echo "FAIL: WARP ставился, хотя отказались"; exit 1; }
grep -q '^NODE_TOKEN=tok$' "$T/etc/vpn-agent/.env" || { echo "FAIL: .env агента"; exit 1; }
[ -s "$T/etc/sysctl.d/99-kvn-network.conf" ] || { echo "FAIL: сетевые настройки"; exit 1; }
grep -q '127.0.0.1:8443:8443' "$T/opt/docker-compose.yml" || { echo "FAIL: compose моста"; exit 1; }

# 3. Одиночный сервер с WARP (Enter = да)
run 'panel.example.com\nmypass\ny\n\n' "мастер + мост + WARP"
grep -q "registration new" "$T/warp.log" && grep -q "proxy port 40000" "$T/warp.log" \
    || { cat "$T/warp.log"; echo "FAIL: WARP не настроен"; exit 1; }
grep -q "WARP работает" "$T/out.log" || { echo "FAIL: нет сообщения про WARP"; exit 1; }

# 4. WARP не поставился — установка всё равно завершается, с предупреждением
printf '#!/bin/bash\nexit 1\n' > "$T/bin/warp-cli"
run 'panel.example.com\nmypass\ny\ny\n' "WARP с ошибкой"
grep -q "WARP не поставился" "$T/out.log" || { cat "$T/out.log"; echo "FAIL: нет предупреждения"; exit 1; }
! grep -q "Установка прервана" "$T/out.log" || { cat "$T/out.log"; echo "FAIL: сбой WARP не должен выглядеть как обрыв установки"; exit 1; }

# 5. Как в жизни: curl … | bash. Скрипт приходит через stdin (ответы — отдельно, как с терминала),
#    а docker compose exec, apt-get и прочие могут читать stdin. Они не должны «съесть» остаток
#    скрипта — иначе финальное сообщение с адресом панели молча не выводится.
cat > "$T/bin/docker" <<'STUB'
#!/bin/bash
case "$*" in
  *"exec -T master"*) cat >/dev/null; echo "NODE_TOKEN=tok"; echo "PRIVATE_KEY=priv" ;;
  *"compose cp"*) : > "${@: -1}" ;;
esac
STUB
printf '#!/bin/bash\ncat >/dev/null\n' > "$T/bin/apt-get"
chmod +x "$T/bin/docker" "$T/bin/apt-get"
rm -rf "$T/opt" "$T/etc/vpn-agent"
printf 'panel.example.com\nmypass\ny\nn\n' > "$T/answers"
PATH="$T/bin:$PATH" bash < "$T/install-pipe.sh" 3< "$T/answers" > "$T/out.log" 2>&1 \
    || { cat "$T/out.log"; echo "FAIL: curl | bash"; exit 1; }
grep -q "Установка полностью завершена" "$T/out.log" \
    || { tail -5 "$T/out.log"; echo "FAIL: curl | bash — нет финального сообщения с адресом панели"; exit 1; }

# 5. Упавшая команда должна называться в выводе, а не завершать скрипт молча
printf '#!/bin/bash\n[ "$1" = restart ] && exit 1\nexit 0\n' > "$T/bin/systemctl"
printf 'panel.example.com\nmypass\ny\nn\n' | PATH="$T/bin:$PATH" bash "$T/install.sh" > "$T/out.log" 2>&1 \
    && { echo "FAIL: ошибка systemctl не остановила установку"; exit 1; }
grep -q "Установка прервана: строка [0-9]*, команда: systemctl restart" "$T/out.log" \
    || { cat "$T/out.log"; echo "FAIL: нет сообщения об ошибке"; exit 1; }

echo "install.sh: все режимы прошли (мастер, мост, мост + WARP, curl | bash), ошибки видны"
