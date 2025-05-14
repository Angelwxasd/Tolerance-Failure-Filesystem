FROM golang:1.24-alpine

WORKDIR /app

# Instalar dependencias necesarias para los health checks y conexiones
RUN apk add --no-cache curl netcat-openbsd gawk

# Copiar dependencias primero para aprovechar caché de Docker
COPY go.mod go.sum ./
RUN go mod download

# Copiar todo el código fuente
COPY . .

# Compilar binario final (como root)
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/so-final

# Crear directorios y ajustar permisos
RUN mkdir -p /app/files /app/raft-data && \
    chown -R 1000:1000 /app/files /app/raft-data && \
    chmod -R 777 /app/files /app/raft-data

# Crear usuario no privilegiado
RUN adduser -D -u 1000 appuser

# Cambiar propietario del binario y directorios
RUN chown -R appuser:appuser /app

# Cambiar a usuario no privilegiado
USER appuser

# Exponer puertos gRPC y HTTP
EXPOSE 50051 8080

# Usar el script como entrypoint
ENTRYPOINT ["/app/so-final"]