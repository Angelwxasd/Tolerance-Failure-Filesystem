package node

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "so-final/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/proto"
)

// ==================== Service Implementation ====================
type NetworkService struct {
	pb.UnimplementedRaftServiceServer
	node    *Node
	fileMgr *FileManager // Nueva estructura para manejo de archivos
}

// ==================== Peer Management ====================
type Peer struct {
	ID       int
	Address  string
	conn     *grpc.ClientConn
	client   pb.RaftServiceClient
	mu       sync.Mutex
	isActive atomic.Bool // Estado atómico de conexión
}

func NewPeer(id int, address string) (*Peer, error) {
	var (
		conn *grpc.ClientConn
		err  error
	)

	// Configuración robusta de conexión
	kp := keepalive.ClientParameters{
		Time:                30 * time.Second,
		Timeout:             10 * time.Second,
		PermitWithoutStream: true,
	}

	maxRetries := 5
	baseDelay := 1 * time.Second

	for i := 0; i < maxRetries; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		conn, err = grpc.DialContext(
			ctx,
			address,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithKeepaliveParams(kp),
			grpc.WithBlock(),
		)

		if err == nil && conn.GetState() == connectivity.Ready {
			break
		}

		if i < maxRetries-1 {
			retryDelay := baseDelay * time.Duration(1<<uint(i))
			log.Printf("Reintentando conexión a %s en %v", address, retryDelay)
			time.Sleep(retryDelay)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("fallo conexión a %s: %v", address, err)
	}

	// Monitoreo continuo de estado
	go func() {
		for {
			state := conn.GetState()
			if state == connectivity.TransientFailure || state == connectivity.Shutdown {
				p.isActive.Store(false)
			} else {
				p.isActive.Store(true)
			}
			time.Sleep(2 * time.Second)
		}
	}()

	return &Peer{
		ID:       id,
		Address:  address,
		conn:     conn,
		client:   pb.NewRaftServiceClient(conn),
		isActive: atomic.Bool{},
	}, nil
}

// ==================== Core RPC Handlers ====================
func (ns *NetworkService) TransferFile(ctx context.Context, req *pb.FileData) (*pb.TransferResponse, error) {
	// 1. Validar operación localmente
	if !ns.fileMgr.ValidatePath(req.Filename) {
		return &pb.TransferResponse{Success: false, Message: "Ruta inválida"}, nil
	}

	// 2. Crear comando Raft
	cmd := &pb.FileCommand{
		Op:         pb.FileCommand_TRANSFER,
		Path:       req.Filename,
		Content:    req.Content,
		Timestamp:  time.Now().UnixNano(),
		TargetNode: req.TargetNode,
	}

	// 3. Aplicar a través del consenso Raft
	if err := ns.node.raft.ProposeCommand(cmd); err != nil {
		return &pb.TransferResponse{Success: false, Message: err.Error()}, nil
	}

	// 4. Replicar a otros nodos
	go ns.replicateToPeers(cmd)

	return &pb.TransferResponse{Success: true, Message: "Transferencia iniciada"}, nil
}

func (ns *NetworkService) replicateToPeers(cmd *pb.FileCommand) {
	data, _ := proto.Marshal(&pb.FileData{
		Filename: cmd.Path,
		Content:  cmd.Content,
	})

	for _, peer := range ns.node.raft.peers {
		if peer.ID == ns.node.raft.id {
			continue // Saltar nodo local
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if _, err := peer.client.TransferFile(ctx, &pb.FileData{
			Filename: cmd.Path,
			Content:  cmd.Content,
		}); err != nil {
			log.Printf("Error replicando a nodo %d: %v", peer.ID, err)
		}
	}
}

// ==================== Raft Consensus Handlers ====================
func (ns *NetworkService) RequestVote(ctx context.Context, req *pb.VoteRequest) (*pb.VoteResponse, error) {
	ns.node.raft.mu.Lock()
	defer ns.node.raft.mu.Unlock()

	resp := &pb.VoteResponse{
		Term:        int64(ns.node.raft.currentTerm),
		VoteGranted: false,
	}

	// Lógica mejorada de votación
	if req.Term > int64(ns.node.raft.currentTerm) {
		ns.node.raft.stepDownToFollower(int(req.Term))
	}

	lastLogIndex, lastLogTerm := ns.node.raft.getLastLogInfo()
	upToDate := req.LastLogTerm > int64(lastLogTerm) ||
		(req.LastLogTerm == int64(lastLogTerm) && req.LastLogIndex >= int64(lastLogIndex))

	if upToDate && (ns.node.raft.votedFor == -1 || ns.node.raft.votedFor == int(req.CandidateId)) {
		ns.node.raft.votedFor = int(req.CandidateId)
		resp.VoteGranted = true
		ns.node.raft.resetElectionTimer()
	}

	return resp, nil
}

func (ns *NetworkService) AppendEntries(ctx context.Context, req *pb.AppendRequest) (*pb.AppendResponse, error) {
	ns.node.raft.mu.Lock()
	defer ns.node.raft.mu.Unlock()

	resp := &pb.AppendResponse{
		Term:    int64(ns.node.raft.currentTerm),
		Success: false,
	}

	// 1. Actualizar estado si el término es mayor
	if req.Term > int64(ns.node.raft.currentTerm) {
		ns.node.raft.stepDownToFollower(int(req.Term))
	}

	// 2. Verificar consistencia de logs
	if int(req.PrevLogIndex) < ns.node.raft.lastIncludedIndex {
		go ns.node.raft.sendSnapshotToLeader(int(req.LeaderId))
		return resp, nil
	}

	// 3. Aplicar entradas al log
	if success := ns.node.raft.applyEntries(req); success {
		resp.Success = true
		ns.node.raft.commitIndex = min(int(req.LeaderCommit), ns.node.raft.lastIncludedIndex+len(ns.node.raft.log))
		ns.node.raft.resetElectionTimer()
	}

	return resp, nil
}

// ==================== Snapshot Handling ====================
func (ns *NetworkService) InstallSnapshot(stream pb.RaftService_InstallSnapshotServer) error {
	var (
		snapshotData      bytes.Buffer
		lastIncludedIndex int64
		lastIncludedTerm  int64
	)

	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if chunk.IsLast {
			lastIncludedIndex = chunk.LastIncludedIndex
			lastIncludedTerm = chunk.LastIncludedTerm
		}

		snapshotData.Write(chunk.Data)
	}

	// Aplicar snapshot al estado local
	if err := ns.node.raft.applySnapshot(snapshotData.Bytes(), lastIncludedIndex, lastIncludedTerm); err != nil {
		return stream.SendAndClose(&pb.SnapshotAck{Success: false})
	}

	// Sincronizar sistema de archivos
	ns.fileMgr.SyncFromSnapshot(snapshotData.Bytes())

	return stream.SendAndClose(&pb.SnapshotAck{Success: true})
}

// ==================== Helper Functions ====================
func (p *Peer) IsActive() bool {
	return p.isActive.Load()
}

func (p *Peer) UpdateAddress(newAddr string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.Address == newAddr {
		return
	}

	if p.conn != nil {
		p.conn.Close()
	}

	conn, err := grpc.Dial(newAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("Error actualizando dirección %d: %v", p.ID, err)
		return
	}

	p.Address = newAddr
	p.conn = conn
	p.client = pb.NewRaftServiceClient(conn)
	p.isActive.Store(true)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ==================== File Management Integration ====================
type FileManager struct {
	baseDir string
	mu      sync.RWMutex
}

func NewFileManager() *FileManager {
	baseDir := os.Getenv("FILE_BASE_DIR")
	if baseDir == "" {
		baseDir = "/app/files"
	}
	return &FileManager{baseDir: baseDir}
}

func (fm *FileManager) SyncFromSnapshot(snapshot []byte) {
	// Lógica para aplicar snapshot al sistema de archivos
	// (Implementación detallada requerida)
}

func (fm *FileManager) ValidatePath(path string) bool {
	// Validar rutas seguras y dentro del directorio base
	cleanPath := filepath.Clean(path)
	return filepath.IsAbs(cleanPath) && strings.HasPrefix(cleanPath, fm.baseDir)
}
