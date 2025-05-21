#!/usr/bin/env bash
# -------------------------------------------------------------
# ConfigNodo1  – LANZA el nodo 1 de tu clúster
# -------------------------------------------------------------

## 1. Variables de identidad
export NODE_ID=1
export NODE_ADDR=localhost:50051

## 2. Direcciones de los peers (TODOS los demás)
# Javier NODO 2
# Moisés NODO 3
export PEER_ADDRS=2@172.26.165.149:50051,3@172.31.0.21:50051,4@192.168.1.13:50051

## 3. Carpetas locales
export RAFT_DATA_DIR=/srv/raft1
export FILE_BASE_DIR=/srv/files1

# Crea directorios si no existen
mkdir -p "$RAFT_DATA_DIR" "$FILE_BASE_DIR"

## 4. Arranca el nodo:
#   - Si ya tienes el ejecutable compilado ("nodo"), usa exec para
#     reemplazar el script por el proceso Go (mejor gestión de señales).
#   - Si prefieres compilar al vuelo, descomenta la línea go run .
exec ./nodo "$@"
# go run .   # ← alternativa sin compilar previamente
