# ==========================================
# ЭТАП 1: Сборка фронтенда (Vue)
# ==========================================
FROM node:alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:alpine AS awg-builder
RUN apk add --no-cache git make gcc musl-dev linux-headers
WORKDIR /src
RUN set -eux; \
    mkdir -p /out/bin /out/source; \
    git init amneziawg-go; \
    git -C amneziawg-go fetch --depth=1 https://github.com/amnezia-vpn/amneziawg-go.git 08fe3d33bbb116be09a6183d02609311b8b0c9e8; \
    git -C amneziawg-go checkout --detach FETCH_HEAD; \
    git -C amneziawg-go archive --format=tar.gz --output=/out/source/amneziawg-go.tar.gz HEAD; \
    cd amneziawg-go; \
    CGO_ENABLED=0 go build -trimpath -o /out/bin/amneziawg-go .
RUN set -eux; \
    git init amneziawg-tools; \
    git -C amneziawg-tools fetch --depth=1 https://github.com/amnezia-vpn/amneziawg-tools.git ee0f0a9aa34ff0a0da4b3433b9512781cfe02843; \
    git -C amneziawg-tools checkout --detach FETCH_HEAD; \
    git -C amneziawg-tools archive --format=tar.gz --output=/out/source/amneziawg-tools.tar.gz HEAD; \
    make -C amneziawg-tools/src LDFLAGS=-static; \
    cp amneziawg-tools/src/wg /out/bin/awg

# ==========================================
# ЭТАП 2: Сборка бэкенда (Go)
# ==========================================
FROM golang:alpine AS backend-builder
WORKDIR /app

# C-компилятор нужен драйверу SQLite (CGO)
RUN apk add --no-cache gcc musl-dev

# Кэшируем зависимости Go
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Мастер (с CGO для SQLite); VERSION — коммит, его видно в настройках панели
ARG VERSION=dev
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-X github.com/sud0-i/KVN-Bridge-Panel/internal/api.Version=${VERSION}" -o kvn-master ./cmd/master
# Агент — статический бинарник для нод (Ansible копирует его на сервер)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o build/agent_linux_amd64 ./cmd/agent

# ==========================================
# ЭТАП 3: Финальный образ
# ==========================================
FROM alpine:latest
WORKDIR /app

# ansible + sshpass нужны для авто-деплоя нод по root-паролю
RUN apk --no-cache add ca-certificates tzdata ansible openssh-client sshpass

COPY --from=backend-builder /app/kvn-master .
COPY --from=backend-builder /app/build/agent_linux_amd64 ./build/agent_linux_amd64
COPY --from=awg-builder /out/bin/ ./build/awg_linux_amd64/
COPY --from=awg-builder /out/source/ ./licenses/awg/
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist
COPY ansible/ ./ansible/
COPY internal/website/index.html ./internal/website/index.html

EXPOSE 8080

CMD ["./kvn-master"]
