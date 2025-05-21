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
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
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

/*Crea e inicializa un nodo Raft listo para arrancar:

  Instancia la estructura Node (ID, dirección y gestor de archivos).

  Construye la máquina de consenso (NewRaft), el snapshotter y el servicio gRPC.

  Configura la política keep-alive del lado servidor para detectar clientes colgados.

  Encadena dos interceptores (disponibilidad y liderazgo).

  Registra la implementación del servicio RaftService.

  Carga el estado persistente del nodo (logs y snapshot).*/

func NewNode(id int, addr string, peers []*Peer) *Node {
	n := &Node{
		ID:      id,
		Address: addr,
		fileMgr: NewFileManager(),
	}
	n.raft = NewRaft(id, peers)
	// ⬇️ Refuerza la recuperación de estado Raft
	n.raft.readPersist()
	n.snapshotter = NewSnapshotManager(n)
	// ─── keep-alive del lado servidor ────────────────────────────────
	kaep := keepalive.EnforcementPolicy{
		MinTime:             2 * time.Minute, // ≥ Time del cliente
		PermitWithoutStream: true,            // acepta pings sin RPC activas
	}

	svc := &NetworkService{node: n, fileMgr: n.fileMgr}
	n.server = grpc.NewServer(
		grpc.KeepaliveEnforcementPolicy(kaep),
		/* grpc.ChainUnaryInterceptor(
			n.connectionStateInterceptor(),
		), */
	)
	pb.RegisterRaftServiceServer(n.server, svc)
	reflection.Register(n.server)

	n.loadPersistentState()
	return n
}

/*
Intercepta cada RPC antes de ejecutarse:

	Si el nodo es líder pero no hay quórum, responde codes.Unavailable, bloqueando operaciones de escritura mientras el clúster está “partido”.

	De lo contrario, deja pasar la llamada al handler real.
*/
func (n *Node) connectionStateInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (interface{}, error) {

		n.raft.mu.RLock()
		leader := n.raft.state == Leader
		quorum := n.raft.checkQuorum()
		n.raft.mu.RUnlock()

		if leader && !quorum {
			return nil, status.Error(codes.Unavailable, "cluster sin quórum")
		}
		return handler(ctx, req)
	}
}

/* ---------- arranque y parada ---------- */
/*
Arranca todos los servicios que componen el nodo:

    Abre un net.Listener en el puerto 50051 dentro del contenedor.

    Lanza el servidor gRPC en una goroutine.

    Inicia snapshots automáticos cada 30 min.

    Expone métricas Raft simples vía HTTP :8080/raft.

    Espera activamente a que los peers definidos en PEER_ADDRS acepten TCP (sincroniza la puesta en marcha del clúster).

    Para cada peer arranca un bucle de reconexión en segundo plano.

    Registra en logs que el nodo está operativo. 	*/

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
	//waitForPeers(os.Getenv("PEER_ADDRS"), n.Address, 100, 500*time.Millisecond)

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

/*
Detiene el nodo de forma ordenada:

	Parada “graceful” del servidor gRPC (vacía las conexiones activas).

	Sincroniza a disco el gestor de archivos (fileMgr.Sync).

	Elimina snapshots temporales y persiste el estado Raft.

	Escribe en logs que el nodo ha sido detenido.
*/
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
/* Al reiniciar, aplica el snapshot más reciente y deja que NewRaft
cargue el log y el currentTerm. De esta forma, el nodo recupera su
último estado consistente antes de un fallo.*/

func (n *Node) loadPersistentState() {
	if err := n.snapshotter.LoadLatest(); err != nil {
		log.Printf("snapshot: %v", err)
	}
	// `raft.loadStateInternal()` ya se llama desde NewRaft
}

// ---------- interceptores ---------- //
/* Filtra solo las RPC del sistema de archivos que deben ejecutarse en el líder:

   Mantiene una lista requiresLeader (TransferFile, DeleteFile, MkDir, RemoveDir).

   Si llega otra RPC (AppendEntries, RequestVote…) la deja pasar.

   Si el nodo no es líder, responde codes.FailedPrecondition. */
/* func (n *Node) leaderInterceptor() grpc.UnaryServerInterceptor {
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
} */

/* ---------- métrica HTTP ---------- */
/* Servicio HTTP mínimo para observabilidad:

   Registra un handler /raft que muestra id, estado (Follower | Candidate | Leader), término vigente, índice commit y aplicado.

   Escucha en :8080 y reporta la URL en logs. */
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
/* Sincroniza el arranque del contenedor con el resto del clúster:

   Parsea PEER_ADDRS con el formato id@host:port.

   Ignora su propia dirección.

   Para cada peer intenta abrir un tcp con reintentos exponenciales (“back-off” aleatorio).

   Si un peer no responde tras N intentos, aborta todo el proceso (log.Fatalf) porque el clúster no puede formarse correctamente. */
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

// Calcula la ruta base (RAFT_DATA_DIR/node<id>/snapshots) y devuelve el gestor vinculado al nodo.
func NewSnapshotManager(n *Node) *SnapshotManager {
	base := filepath.Join(os.Getenv("RAFT_DATA_DIR"), fmt.Sprintf("node%d", n.ID))
	return &SnapshotManager{node: n, dir: filepath.Join(base, "snapshots")}
}

/*
	Cada interval (ticker) verifica si el nodo es líder;

si lo es, dispara raft.takeSnapshot(). Así solo el líder crea imágenes compactadas del log.
*/
func (sm *SnapshotManager) AutoSnapshot(interval time.Duration) {
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for range tk.C {
		if sm.node.raft.state == Leader {
			sm.node.raft.takeSnapshot()
		}
	}
}

/*
	Durante el arranque aplica el snapshot más reciente al estado Raft (applySnapshot),

usando el commitIndex y currentTerm actuales para mantener coherencia.
*/
func (sm *SnapshotManager) LoadLatest() error {
	return sm.node.raft.applySnapshot(nil, int64(sm.node.raft.commitIndex), int64(sm.node.raft.currentTerm))
}

/*
	Elimina archivos de snapshot del directorio cuando el nodo se detiene,

evitando basura en disco dentro del contenedor.
*/
func (sm *SnapshotManager) Cleanup() {
	if files, err := os.ReadDir(sm.dir); err == nil {
		for _, f := range files {
			_ = os.Remove(filepath.Join(sm.dir, f.Name()))
		}
	}
}

/* // node/node.go – coloca esto junto a otros helpers
func (n *Node) GetOrConnect(id int) (*Peer, error) {
	if id < 0 || id >= len(n.raft.peers) {
		return nil, fmt.Errorf("peer %d fuera de rango", id)
	}
	p := n.raft.peers[id]
	if p == nil {
		return nil, fmt.Errorf("peer %d desconocido", id)
	}
	if !p.IsActive() {
		if err := p.Reconnect(); err != nil {
			return nil, err
		}
	}
	return p, nil
}
*/
