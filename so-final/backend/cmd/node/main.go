package main

import (
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"so-final/node"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// main arranca el servidor gRPC **antes** de intentar marcar a los peers.
// Así evitamos el ciclo de bloqueo que ocurría cuando cada contenedor
// intentaba conectarse mientras nadie escuchaba todavía.
func main() {
	rand.Seed(time.Now().UnixNano() + int64(os.Getpid())) // una semilla única por contenedor
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)

	/* ───── 1. Configuración básica ───── */
	idStr := os.Getenv("NODE_ID")
	if idStr == "" {
		log.Fatal("NODE_ID no está definido")
	}
	nodeID, err := strconv.Atoi(idStr)
	if err != nil {
		log.Fatalf("NODE_ID inválido: %v", err)
	}

	nodeAddr := os.Getenv("NODE_ADDR")
	if nodeAddr == "" {
		log.Fatal("NODE_ADDR no está definido")
	}

	raftDir := os.Getenv("RAFT_DATA_DIR")
	if raftDir == "" {
		raftDir = "/app/raft-data"
	}
	if err := os.MkdirAll(raftDir, 0o755); err != nil {
		log.Fatalf("Error creando RAFT_DATA_DIR: %v", err)
	}
	os.Setenv("RAFT_DATA_DIR", raftDir) // para el resto del código

	// 2. Construir lista de peers con IDs correctos y conexión gRPC lista
	raw := strings.Split(os.Getenv("PEER_ADDRS"), ",")
	seen := map[int]bool{}
	peers := make([]*node.Peer, 0, len(raw))

	nextFallbackID := 1
	for _, entry := range raw {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		var pid int
		var addr string

		if parts := strings.SplitN(entry, "@", 2); len(parts) == 2 {
			pid, err = strconv.Atoi(parts[0])
			if err != nil {
				log.Fatalf("PEER_ADDRS: id inválido en %q", entry)
			}
			addr = parts[1]
		} else {
			for seen[nextFallbackID] || nextFallbackID == nodeID {
				nextFallbackID++
			}
			pid = nextFallbackID
			addr = entry
		}

		if pid == nodeID || addr == nodeAddr { // evitar agregarse
			continue
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			log.Fatalf("PEER_ADDRS: host:port inválido en %q", entry)
		}
		if seen[pid] {
			log.Fatalf("PEER_ADDRS: id %d duplicado", pid)
		}
		seen[pid] = true

		/* ------------- CAMBIO FUNDAMENTAL ------------- */
		peer, err := node.NewPeer(pid, addr) // ← Esto llama dial()
		if err != nil {
			log.Printf("[Nodo %d] peer %s aún no disponible: %v", nodeID, addr, err)
		}
		peers = append(peers, peer)
	}

	/* ───── 3. Inicializar y arrancar nodo ───── */
	n := node.NewNode(nodeID, nodeAddr, peers)
	if err := n.Start(); err != nil {
		log.Fatalf("Error iniciando nodo %d: %v", nodeID, err)
	}
	log.Printf("Nodo %d operativo en %s", nodeID, nodeAddr)

	/* ───── 4. Esperar señal de terminación ───── */
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("Deteniendo nodo…")
	n.Stop()
	log.Println("Nodo detenido correctamente")
}
