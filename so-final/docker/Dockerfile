############################
# 1 ▸ BUILD STAGE (Alpine) #
############################
FROM golang:1.24.2-alpine AS builder

WORKDIR /src

# ─── dependencias de sistema para compilar GLFW/Fyne ───
# build-base      → gcc, musl-dev, make…
# pkgconfig       → pc files para cgo
# xorgproto       → headers de X11
# libx*-dev       → headers de Xlib, Xrandr, etc.
# mesa-dev        → headers+pkg-config de OpenGL
# libxi-dev       → requerido por GLFW
RUN apk add --no-cache \
        build-base pkgconfig \
        xorgproto \
        libx11-dev libxinerama-dev libxrandr-dev libxcursor-dev \
        libxi-dev mesa-dev

# ─── módulos Go en caché ───
COPY go.mod go.sum ./
RUN go mod download

# ─── código fuente ───
COPY . .

# ⚙️  construimos los dos binarios
RUN go build -o /out/server ./main.go
RUN go build -o /out/ui     ./ui

############################
# 2 ▸ RUNTIME STAGE (Alpine)#
############################
FROM alpine:3.20

# Librerías en tiempo de ejecución (sin headers)
RUN apk add --no-cache \
        libx11 libxinerama libxrandr libxcursor \
        mesa-gl libxkbcommon

WORKDIR /app

# Binarios desde la etapa builder
COPY --from=builder /out/server .
COPY --from=builder /out/ui     .

# Script de orquestación
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Directorios de datos (se montan como volúmenes en docker-compose)
RUN mkdir -p /app/files /app/raft-data
ENV FILE_BASE_DIR=/app/files \
    RAFT_DATA_DIR=/app/raft-data

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
