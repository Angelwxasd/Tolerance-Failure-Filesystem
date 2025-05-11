FROM golang:1.24-alpine

WORKDIR /app

# Herramientas útiles para depuración
RUN apk add --no-cache curl netcat-openbsd

# Copiar dependencias primero para aprovechar cache de Docker
COPY go.mod go.sum ./
RUN go mod download

# Copiar código fuente
COPY . .

# Compilar para producción
RUN CGO_ENABLED=0 GOOS=linux go build -o /so-final

# Crear directorios con permisos adecuados
RUN mkdir -p /app/files /app/raft-data && \
    chmod -R 777 /app/files /app/raft-data

EXPOSE 50051 8080

ENTRYPOINT ["/so-final"]