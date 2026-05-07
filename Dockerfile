# ==========================================
# ЭТАП 1: Сборка фронтенда (Vue)
# ==========================================
FROM node:alpine AS frontend-builder
WORKDIR /app
# Копируем package.json и ставим зависимости
COPY frontend/package*.json ./frontend/
WORKDIR /app/frontend
RUN npm install
# Копируем исходники и собираем
COPY frontend/ ./
RUN npm run build

# ==========================================
# ЭТАП 2: Сборка бэкенда (Go)
# ==========================================
FROM golang:alpine AS backend-builder
WORKDIR /app

# ДОБАВЛЕНО: Устанавливаем C-компиляторы, необходимые для SQLite
RUN apk add --no-cache gcc musl-dev

# Кэшируем зависимости Go
COPY go.mod go.sum ./
RUN go mod download

# Копируем весь код проекта
COPY . .

# ИЗМЕНЕНО: Включаем CGO (CGO_ENABLED=1)
RUN CGO_ENABLED=1 GOOS=linux go build -o kvn-master ./cmd/master

# ==========================================
# ЭТАП 3: Финальный образ (Только самое нужное)
# ==========================================
FROM alpine:latest
WORKDIR /app

# Устанавливаем часовые пояса и корневые сертификаты (нужно для HTTPS запросов)
RUN apk --no-cache add ca-certificates tzdata

# Копируем бинарник от Go
COPY --from=backend-builder /app/kvn-master .
# Копируем собранный фронтенд от Node.js
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist

# Порт, который слушает Мастер
EXPOSE 8080

# Запуск
CMD ["./kvn-master"]