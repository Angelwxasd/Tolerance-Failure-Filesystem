FROM golang:1.24-alpine

WORKDIR /app

# Instalar herramientas de depuración 
# Instalar herramientas (con apk)
RUN apk add --no-cache curl netcat-openbsd

# Instalar herramientas de depuración
RUN apk add --no-cache curl netcat-openbsd iproute2  # ← Agregamos iproute2 para ss

# Copiar dependencias primero para cache
COPY go.mod go.sum ./
RUN go mod download

# Copiar código
COPY . .

# Compilar con símbolos de depuración
RUN go build -gcflags="all=-N -l" -o /so-final

EXPOSE 50051

# Crear directorios para archivos y datos de Raft
RUN mkdir -p /app/files
RUN mkdir -p /app/raft-data
VOLUME /app/files
VOLUME /app/raft-data



ENTRYPOINT ["/so-final"]
