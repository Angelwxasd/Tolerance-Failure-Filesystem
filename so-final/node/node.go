package node

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pb "so-final/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

/* ---------- nodo ---------- */

type Node struct {
	ID      int
	Address string

	raft        *Raft
	server      *grpc.Server
	fileMgr     *FileManager
	snapshotter *SnapshotManager

	mu sync.RWMutex
}

/* ---------- constructor ---------- */

func NewNode(id int, addr string, peers []*Peer) *Node {
	n := &Node{
		ID:      id,
		Address: addr,
		fileMgr: NewFileManager(),
	}
	n.raft = NewRaft(id, peers)
	n.snapshotter = NewSnapshotManager(n)

	svc := &NetworkService{node: n, fileMgr: n.fileMgr}
	n.server = grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			n.connectionStateInterceptor(),
			n.leaderInterceptor(), // versión filtrada solo para RPC de FS
		),
	)
	pb.RegisterRaftServiceServer(n.server, svc)

	n.loadPersistentState()
	return n
}

func (n *Node) connectionStateInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Si somos líder pero el clúster no tiene quórum, rechaza peticiones de cliente.
		if n.raft.state == Leader && !n.raft.checkQuorum() {
			return nil, status.Error(codes.Unavailable, "cluster sin quórum")
		}
		return handler(ctx, req)
	}
}

/* ---------- arranque y parada ---------- */

func (n *Node) Start() error {
	lis, err := net.Listen("tcp", ":50051") // dentro del contenedor
	if err != nil {
		return fmt.Errorf("listener: %w", err)
	}

	// gRPC
	go n.server.Serve(lis)

	// snapshots automáticos (cada 30 min)
	go n.snapshotter.AutoSnapshot(30 * time.Minute)

	// métrica HTTP sencilla
	go n.startMetricsHTTP()

	// esperar peers antes de operar
	waitForPeers(os.Getenv("PEER_ADDRS"), n.Address, 10, 500*time.Millisecond)

	// 2️⃣ – ahora sí, marcar/reconectar a cada peer en segundo plano
	for _, p := range n.raft.peers {
		if p == nil {
			continue
		}
		go func(pr *Peer) { // ← sin node.
			for !pr.IsActive() {
				pr.Reconnect()
				time.Sleep(2 * time.Second)
			}
		}(p)
	}

	log.Printf("[Nodo %d] operativo en %s", n.ID, n.Address)
	return nil
}

func (n *Node) Stop() {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.server.GracefulStop()
	n.fileMgr.Sync()
	n.snapshotter.Cleanup()
	n.raft.saveState()

	log.Printf("[Nodo %d] detenido", n.ID)
}

/* ---------- estado persistente ---------- */

func (n *Node) loadPersistentState() {
	if err := n.snapshotter.LoadLatest(); err != nil {
		log.Printf("snapshot: %v", err)
	}
	// `raft.loadStateInternal()` ya se llama desde NewRaft
}

// ---------- interceptores ---------- //
func (n *Node) leaderInterceptor() grpc.UnaryServerInterceptor {
	requiresLeader := map[string]bool{
		"/proto.RaftService/TransferFile": true,
		"/proto.RaftService/DeleteFile":   true,
		"/proto.RaftService/MkDir":        true,
		"/proto.RaftService/RemoveDir":    true,
	}
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !requiresLeader[info.FullMethod] { // ← Deja pasar RequestVote, AppendEntries…
			return handler(ctx, req)
		}
		n.raft.mu.Lock()
		isLeader := n.raft.state == Leader && n.raft.id == n.ID
		n.raft.mu.Unlock()
		if !isLeader {
			return nil, status.Error(codes.FailedPrecondition, "no soy líder")
		}
		return handler(ctx, req)
	}
}

/* ---------- métrica HTTP ---------- */

func (n *Node) startMetricsHTTP() {
	http.HandleFunc("/raft", func(w http.ResponseWriter, _ *http.Request) {
		n.raft.mu.Lock()
		defer n.raft.mu.Unlock()
		state := [...]string{"Follower", "Candidate", "Leader"}[n.raft.state]
		fmt.Fprintf(w, "id=%d state=%s term=%d commit=%d applied=%d\n",
			n.ID, state, n.raft.currentTerm, n.raft.commitIndex, n.raft.lastApplied)
	})
	log.Printf("[Nodo %d] métrica en :8080/raft", n.ID)
	_ = http.ListenAndServe(":8080", nil)
}

/* ---------- utilidades ---------- */

// LÓGICA PARA NODOS:
// waitForPeers lee PEER_ADDRS="id@host:port,..." y espera a que cada host:port acepte TCP.
func waitForPeers(peersEnv, selfAddr string, retries int, backoff time.Duration) {
	addrs := strings.Split(peersEnv, ",")
	for _, raw := range addrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		// ── Quitar el prefijo "id@" si existe ──
		parts := strings.SplitN(raw, "@", 2)
		addr := parts[len(parts)-1] // siempre host:port

		if addr == selfAddr {
			continue // saltar a sí mismo
		}

		var ok bool
		for i := 1; i <= retries; i++ {
			conn, err := net.DialTimeout("tcp", addr, 1*time.Second)
			if err == nil {
				conn.Close()
				log.Printf("✅ peer %s escuchando", addr)
				ok = true
				break
			}
			wait := backoff + time.Duration(rand.Int63n(int64(backoff)))
			log.Printf("⏳ esperando %s (intento %d/%d), reintento en %v", addr, i, retries, wait)
			time.Sleep(wait)
		}
		if !ok {
			log.Fatalf("❌ peer %s no respondió tras %d intentos", addr, retries)
		}
	}
}

/* ---------- SnapshotManager ---------- */

type SnapshotManager struct {
	node *Node
	dir  string
}

func NewSnapshotManager(n *Node) *SnapshotManager {
	base := filepath.Join(os.Getenv("RAFT_DATA_DIR"), fmt.Sprintf("node%d", n.ID))
	return &SnapshotManager{node: n, dir: filepath.Join(base, "snapshots")}
}

func (sm *SnapshotManager) AutoSnapshot(interval time.Duration) {
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for range tk.C {
		if sm.node.raft.state == Leader {
			sm.node.raft.takeSnapshot()
		}
	}
}

func (sm *SnapshotManager) LoadLatest() error {
	return sm.node.raft.applySnapshot(nil, int64(sm.node.raft.commitIndex), int64(sm.node.raft.currentTerm))
}

func (sm *SnapshotManager) Cleanup() {
	if files, err := os.ReadDir(sm.dir); err == nil {
		for _, f := range files {
			_ = os.Remove(filepath.Join(sm.dir, f.Name()))
		}
	}
}
