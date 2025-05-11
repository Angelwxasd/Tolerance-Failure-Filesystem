package main

import (
	"log"
	"os"
	"os/signal"
	"so-final/node"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)

	// 1. Validar NODE_ID
	nodeIDStr := os.Getenv("NODE_ID")
	if nodeIDStr == "" {
		log.Fatal("NODE_ID no está definido")
	}
	nodeID, err := strconv.Atoi(nodeIDStr)
	if err != nil {
		log.Fatalf("NODE_ID inválido: %v", err)
	}

	nodeAddr := os.Getenv("NODE_ADDR")
	if nodeAddr == "" {
		log.Fatal("NODE_ADDR no está definido")
	}

	// 2. Configurar directorio RAFT_DATA_DIR
	raftDataDir := os.Getenv("RAFT_DATA_DIR")
	if raftDataDir == "" {
		raftDataDir = "/app/raft-data"
	}
	if err := os.MkdirAll(raftDataDir, 0755); err != nil {
		log.Fatalf("Error creando RAFT_DATA_DIR: %v", err)
	}
	os.Setenv("RAFT_DATA_DIR", raftDataDir)

	// 3. Procesar PEER_ADDRS
	peers := make([]*node.Peer, 0)
	peerAddrs := os.Getenv("PEER_ADDRS")
	if peerAddrs != "" {
		addresses := strings.Split(peerAddrs, ",")
		existingIDs := make(map[int]bool)

		for _, addr := range addresses {
			parts := strings.Split(addr, "@")
			if len(parts) != 2 {
				log.Printf("Formato inválido en peer: %s. Se esperaba 'ID@host:puerto'", addr)
				continue
			}

			id, err := strconv.Atoi(parts[0])
			if err != nil {
				log.Printf("ID no numérico en %s: %v", addr, err)
				continue
			}

			// Validar ID único
			if existingIDs[id] {
				log.Printf("ID duplicado en PEER_ADDRS: %d", id)
				continue
			}
			existingIDs[id] = true

			peerAddress := parts[1]
			log.Printf("Conectando a peer %d en %s", id, peerAddress)

			peer, err := node.NewPeer(id, peerAddress)
			if err != nil {
				log.Printf("Error creando peer %d (%s): %v", id, peerAddress, err)
				continue
			}

			peers = append(peers, peer)
		}
	}

	// 4. Ordenar peers por ID
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].ID < peers[j].ID
	})

	// 5. Iniciar nodo
	time.Sleep(5 * time.Second) // Espera inicial para que los servicios estén listos
	n := node.NewNode(nodeID, nodeAddr, peers)
	if err := n.Start(); err != nil {
		log.Fatalf("Error al iniciar nodo %d: %v", nodeID, err)
	}
	log.Printf("Nodo %d iniciado en %s", nodeID, nodeAddr)

	// 6. Manejo de señales para cierre controlado
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// 7. Mantener el programa en ejecución hasta señal
	<-stop
	log.Println("Recibida señal de terminación. Deteniendo nodo...")
	n.Stop()
	log.Println("Nodo detenido correctamente")
}
