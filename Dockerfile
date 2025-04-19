# Используем официальный образ Golang
FROM golang:1.21

# Создаём рабочую директорию
WORKDIR /app

# Копируем все файлы
COPY . .

# Ставим зависимости и билдим
RUN go mod tidy
RUN go build -o bot

# Запускаем
CMD ["./main.go"]
