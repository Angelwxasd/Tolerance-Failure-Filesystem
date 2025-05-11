package main

import (
	"log"
	"os"
	"so-final/node"
	"sort"
	"strconv"
	"strings"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	nodeIDStr := os.Getenv("NODE_ID")
	nodeID, _ := strconv.Atoi(nodeIDStr)
	nodeAddr := os.Getenv("NODE_ADDR")
	peerAddrs := os.Getenv("PEER_ADDRS")

	raftDataDir := os.Getenv("RAFT_DATA_DIR")
	if raftDataDir == "" {
		raftDataDir = "/app/raft-data"
	}
	os.Setenv("RAFT_DATA_DIR", raftDataDir)

	peers := make([]*node.Peer, 0) // <- Cambiar a slice de punteros
	addresses := strings.SplitSeq(peerAddrs, ",")
	for addr := range addresses {
		// Nuevo formato esperado: "ID@host:puerto" (ej: "2@node2:50051")
		parts := strings.Split(addr, "@")

		// Validar formato
		if len(parts) != 2 {
			log.Printf("Formato inválido en dirección peer: %s. Se esperaba 'ID@host:puerto'", addr)
			continue
		}

		// Obtener ID desde la primera parte
		id, err := strconv.Atoi(parts[0])
		if err != nil {
			log.Printf("ID no numérico en peer %s: %v", addr, err)
			continue
		}

		// Obtener dirección real (segunda parte)
		peerAddress := parts[1]

		// Crear peer con ID explícito y dirección
		peer, err := node.NewPeer(id, peerAddress)
		if err != nil {
			log.Printf("Error creando peer %d (%s): %v", id, peerAddress, err)
			continue
		}

		peers = append(peers, peer)
	}

	// Ordenar peers por ID
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].ID < peers[j].ID
	})

	n := node.NewNode(nodeID, nodeAddr, peers) // <- Pasar []*Peer
	if err := n.Start(); err != nil {
		log.Fatalf("Error al iniciar nodo %d: %v", nodeID, err)
	}
	log.Printf("Nodo %d iniciado en %s", nodeID, nodeAddr)

	select {}
}
