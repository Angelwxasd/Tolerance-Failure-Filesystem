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
	addresses := strings.Split(peerAddrs, ",")
	for _, addr := range addresses {
		// Extraer el último octeto de la IP para obtener el ID
		ipParts := strings.Split(addr, ":")
		ipOnly := ipParts[0] // "172.20.0.2"
		ipOctets := strings.Split(ipOnly, ".")
		idStr := ipOctets[len(ipOctets)-1] // "2"
		id, err := strconv.Atoi(idStr)
		if err != nil {
			log.Printf("Dirección inválida: %s", addr)
			continue // Saltar peer inválido
		}
		peer, err := node.NewPeer(id, addr)
		if err != nil {
			log.Printf("Error al conectar con peer %s: %v", addr, err)
			continue // Saltar peer inválido
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
