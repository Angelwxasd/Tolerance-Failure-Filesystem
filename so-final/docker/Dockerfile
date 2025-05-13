FROM golang:1.24-alpine

WORKDIR /app

# Instalar dependencias necesarias para los health checks y conexiones
RUN apk add --no-cache curl netcat-openbsd gawk

# Copiar dependencias primero para aprovechar caché de Docker
COPY go.mod go.sum ./
RUN go mod download

# Copiar todo el código fuente
COPY . .

# Compilar binario final
RUN CGO_ENABLED=0 GOOS=linux go build -o /so-final

# Crear directorios con permisos no privilegiados (Alpine usa uid 1000 por defecto)
RUN mkdir -p /app/files /app/raft-data && \
    chown -R 1000:1000 /app/files /app/raft-data && \
    chmod -R 777 /app/files /app/raft-data

# Exponer puertos gRPC y HTTP
EXPOSE 50051 8080

# Usar el script como entrypoint
ENTRYPOINT ["/so-final"]