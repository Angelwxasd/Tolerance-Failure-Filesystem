package node

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	pb "so-final/proto"

	"net/http" // Para el manejo de errores HTTP, debido a su facilidad

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	//"google.golang.org/protobuf/proto"
)

type Node struct {
	ID          int
	Address     string
	raft        *Raft
	server      *grpc.Server
	fileMgr     *FileManager
	snapshotter *SnapshotManager
	mu          sync.RWMutex
}

// Cleanup removes old snapshots or performs necessary cleanup tasks.
func (sm *SnapshotManager) Cleanup() {
	// Example cleanup logic: remove old snapshots
	files, err := os.ReadDir(sm.snapshotPath)
	if err != nil {
		log.Printf("Error reading snapshot directory: %v", err)
		return
	}

	for _, file := range files {
		filePath := filepath.Join(sm.snapshotPath, file.Name())
		if err := os.Remove(filePath); err != nil {
			log.Printf("Error removing snapshot file %s: %v", filePath, err)
		}
	}
}

func NewNode(id int, address string, peers []*Peer) *Node {
	// 1. Inicializar componentes base
	n := &Node{
		ID:      id,
		Address: address,
		raft:    NewRaft(id, peers),
		fileMgr: NewFileManager(),
	}

	// 2. Configurar gestor de snapshots
	n.snapshotter = NewSnapshotManager(n)

	// 3. Inicializar servicio de red con dependencias
	networkService := &NetworkService{
		node:    n,
		fileMgr: n.fileMgr,
	}

	// 4. Configurar servidor gRPC con interceptores
	n.server = grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			n.connectionStateInterceptor(),
			n.leaderCheckInterceptor(),
		),
	)
	pb.RegisterRaftServiceServer(n.server, networkService)

	// 5. Cargar estado persistente
	n.loadPersistentState()

	return n
}

func (n *Node) Start() error {
	// 1. Iniciar listener de red
	lis, err := net.Listen("tcp", n.Address)
	if err != nil {
		return fmt.Errorf("error al iniciar listener: %v", err)
	}

	// 2. Iniciar servicios en paralelo
	go n.server.Serve(lis)
	go n.raft.applyCommits()
	go n.monitorPeerConnections()
	go n.snapshotter.AutoSnapshot(30 * time.Minute)

	// ===== [NUEVO] Iniciar servidor HTTP para métricas =====
	go func() {
		http.HandleFunc("/raft-state", func(w http.ResponseWriter, r *http.Request) { // <-- Añadir paréntesis aquí
			n.raft.mu.Lock()
			defer n.raft.mu.Unlock()

			state := "Follower"
			switch n.raft.state {
			case Leader:
				state = "Leader"
			case Candidate:
				state = "Candidate"
			}

			response := fmt.Sprintf(
				"Nodo ID: %d\nEstado: %s\nTérmino: %d\nLíder: %d\nCommit Index: %d\nLast Applied: %d",
				n.ID, state, n.raft.currentTerm, n.raft.leaderId, n.raft.commitIndex, n.raft.lastApplied,
			)

			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(response))
		}) // <-- Paréntesis faltante aquí

		log.Printf("[Nodo %d] Métricas HTTP en :8080", n.ID)
		if err := http.ListenAndServe(":8080", nil); err != nil {
			log.Printf("[Nodo %d] Error en servidor HTTP: %v", n.ID, err)
		}
	}()

	// 3. Registrar nodo en el cluster
	n.bootstrapCluster()

	log.Printf("[Nodo %d] Operativo en %s", n.ID, n.Address)
	return nil
}

func (n *Node) Stop() {
	n.mu.Lock()
	defer n.mu.Unlock()

	// 1. Detener servicios en orden seguro
	n.server.GracefulStop()
	n.raft.persistState()
	n.fileMgr.Sync()

	// 2. Limpiar recursos
	n.snapshotter.Cleanup()

	log.Printf("[Nodo %d] Detenido correctamente", n.ID)
}

// ==================== Funciones internas ====================
func (n *Node) loadPersistentState() {
	// 1. Cargar snapshot más reciente
	if err := n.snapshotter.LoadLatest(); err != nil {
		log.Printf("Error cargando snapshot: %v", err)
	}

	// 2. Cargar logs no aplicados
	if err := n.raft.loadUnappliedLogs(); err != nil {
		log.Printf("Error cargando logs: %v", err)
	}
}

func (n *Node) monitorPeerConnections() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		for _, peer := range n.raft.peers {
			if !peer.IsActive() {
				go peer.Reconnect()
			}
		}
	}
}

func (n *Node) bootstrapCluster() {
	if len(n.raft.peers) == 0 {
		n.raft.becomeLeader()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, peer := range n.raft.peers {
		// Enviar JoinRequest con el LastIndex correcto
		req := &pb.JoinRequest{
			NodeId:    int64(n.ID),
			Address:   n.Address,
			LastIndex: int64(n.raft.lastApplied),
		}

		// Manejar errores de conexión
		if _, err := peer.client.JoinCluster(ctx, req); err != nil {
			log.Printf("Error uniendo al cluster: %v", err)
			go peer.Reconnect() // Reconexión automática
		}
	}
}

// ==================== Interceptores gRPC ====================
func (n *Node) connectionStateInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if n.raft.state == Leader && !n.raft.checkQuorum() {
			return nil, status.Error(codes.Unavailable, "cluster sin quórum")
		}
		return handler(ctx, req)
	}
}

func (n *Node) leaderCheckInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if info.FullMethod != "/proto.RaftService/JoinCluster" && n.raft.leaderId != n.ID {
			return nil, status.Error(codes.FailedPrecondition, "no soy líder")
		}
		return handler(ctx, req)
	}
}

// ==================== Manager de Snapshots ====================
type SnapshotManager struct {
	node         *Node
	snapshotPath string
}

func NewSnapshotManager(node *Node) *SnapshotManager {
	basePath := filepath.Join(os.Getenv("RAFT_DATA_DIR"), fmt.Sprintf("node%d", node.ID))
	return &SnapshotManager{
		node:         node,
		snapshotPath: filepath.Join(basePath, "snapshots"),
	}
}

func (sm *SnapshotManager) AutoSnapshot(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		if sm.node.raft.state == Leader {
			sm.node.raft.takeSnapshot()
		}
	}
}

func (sm *SnapshotManager) LoadLatest() error {
	return sm.node.raft.loadSnapshot()
}
