# ==========================================
# ЭТАП 1: Сборка фронтенда (Vue)
# ==========================================
FROM node:alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

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
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist
COPY ansible/ ./ansible/

EXPOSE 8080

CMD ["./kvn-master"]
