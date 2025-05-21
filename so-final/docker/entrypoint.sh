#!/bin/sh
set -eu

# 1) back-end Raft
./server &          # deja el servidor en segundo plano
pid=$!

# 2) espera 20 s para que el clúster se estabilice
sleep 1

# 3) lanza la GUI en primer plano (el contenedor terminará cuando la GUI salga)
exec ./ui
