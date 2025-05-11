FROM golang:1.24-alpine

WORKDIR /app

# Instalar herramientas de depuración y red
RUN apk add --no-cache curl netcat-openbsd iproute2

# Copiar dependencias primero para aprovechar cache de Docker
COPY go.mod go.sum ./
RUN go mod download

# Copiar código fuente
COPY . .

# Compilar (agregamos flags para Alpine)
RUN CGO_ENABLED=0 GOOS=linux go build -gcflags="all=-N -l" -o /so-final

# Configurar permisos y directorios
RUN mkdir -p /app/files /app/raft-data && \
    chmod -R 777 /app/files /app/raft-data

EXPOSE 50051

# Volúmenes (opcional, ya que se definen en docker-compose)
VOLUME ["/app/files", "/app/raft-data"]

ENTRYPOINT ["/so-final"]