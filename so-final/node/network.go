package node

import (
	"bytes"
	"context"
	"errors"
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// ---------- Servicio Raft ---------- //

type NetworkService struct {
	pb.UnimplementedRaftServiceServer
	node    *Node
	fileMgr *FileManager
}

/* ---------- helpers comunes ---------- */

// crea un FileCommand y lo envía al líder ­(o devuelve error si este nodo no es líder)
func (ns *NetworkService) buildAndPropose(
	op pb.FileCommand_Operation, path string, content []byte) error {

	if ns.node.raft.state != Leader {
		return errors.New("no soy líder")
	}
	cmd := &pb.FileCommand{Op: op, Path: path, Content: content}
	return ns.node.raft.ProposeCommand(cmd)
}

// ---------- Gestión de Peers ---------- //

type Peer struct {
	ID      int
	Address string

	conn   *grpc.ClientConn
	client pb.RaftServiceClient

	mu       sync.Mutex
	isActive atomic.Bool
}

func NewPeer(id int, addr string) (*Peer, error) {
	p := &Peer{ID: id, Address: addr}
	return p, p.dial()
}

func (p *Peer) dial() error {
	kp := keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 15 * time.Second, PermitWithoutStream: true}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, p.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(kp),
		grpc.WithBlock())

	if err != nil {
		p.isActive.Store(false)
		return err
	}
	p.conn = conn
	p.client = pb.NewRaftServiceClient(conn)
	p.isActive.Store(true)
	go p.watch()
	return nil
}

// node/network.go
func (p *Peer) watch() {
	for {
		if p.conn == nil || p.conn.GetState() != connectivity.Ready {
			p.Reconnect() // ← intenta marcar de nuevo
		}
		p.isActive.Store(p.conn != nil && p.conn.GetState() == connectivity.Ready)
		time.Sleep(2 * time.Second)
	}
}

func (p *Peer) IsActive() bool { return p.isActive.Load() }

/* ---------- RPC Sistema de archivos ---------- */

// 1. Transferir archivo
func (ns *NetworkService) TransferFile(
	ctx context.Context, req *pb.FileData) (*pb.GenericResponse, error) {

	if !ns.fileMgr.ValidatePath(req.Filename) {
		return &pb.GenericResponse{Success: false, Message: "ruta inválida"}, nil
	}
	err := ns.buildAndPropose(pb.FileCommand_TRANSFER, req.Filename, req.Content)
	return &pb.GenericResponse{Success: err == nil, Message: msg(err, "transferencia replicada")}, nil
}

// 2. Borrar archivo
func (ns *NetworkService) DeleteFile(
	ctx context.Context, req *pb.DeleteRequest) (*pb.GenericResponse, error) {

	if !ns.fileMgr.ValidatePath(req.Filename) {
		return &pb.GenericResponse{Success: false, Message: "ruta inválida"}, nil
	}
	err := ns.buildAndPropose(pb.FileCommand_DELETE, req.Filename, nil)
	return &pb.GenericResponse{Success: err == nil, Message: msg(err, "archivo borrado")}, nil
}

// 3. Crear directorio
func (ns *NetworkService) MkDir(
	ctx context.Context, req *pb.MkDirRequest) (*pb.GenericResponse, error) {

	if !ns.fileMgr.ValidatePath(req.Dirname) {
		return &pb.GenericResponse{Success: false, Message: "ruta inválida"}, nil
	}
	err := ns.buildAndPropose(pb.FileCommand_MKDIR, req.Dirname, nil)
	return &pb.GenericResponse{Success: err == nil, Message: msg(err, "directorio creado")}, nil
}

// 4. Eliminar directorio
func (ns *NetworkService) RemoveDir(
	ctx context.Context, req *pb.RemoveDirRequest) (*pb.GenericResponse, error) {

	if !ns.fileMgr.ValidatePath(req.Dirname) {
		return &pb.GenericResponse{Success: false, Message: "ruta inválida"}, nil
	}
	err := ns.buildAndPropose(pb.FileCommand_RMDIR, req.Dirname, nil)
	return &pb.GenericResponse{Success: err == nil, Message: msg(err, "directorio eliminado")}, nil
}

// 5. Listar (read-only, sin consenso)
func (ns *NetworkService) ListDir(
	ctx context.Context, req *pb.DirRequest) (*pb.DirReply, error) {

	target := filepath.Join(ns.fileMgr.baseDir, filepath.Clean(req.Path))
	if !ns.fileMgr.ValidatePath(target) {
		return nil, status.Error(codes.InvalidArgument, "ruta inválida")
	}
	ents, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return &pb.DirReply{Names: names}, nil
}

func (p *Peer) Reconnect() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn != nil && p.conn.GetState() == connectivity.Ready {
		return
	}
	if p.conn != nil {
		p.conn.Close()
	}
	for i := 0; i < 3; i++ {
		if err := p.dial(); err == nil {
			log.Printf("[Peer %d] reconectado", p.ID)
			return
		}
		time.Sleep(time.Duration(i+1) * 2 * time.Second)
	}
	log.Printf("[Peer %d] reconexión fallida", p.ID)
}

// ---------- RPC Votación ---------- //

func (ns *NetworkService) RequestVote(ctx context.Context, req *pb.VoteRequest) (*pb.VoteResponse, error) {
	rf := ns.node.raft
	rf.mu.Lock()
	defer rf.mu.Unlock()

	resp := &pb.VoteResponse{Term: int64(rf.currentTerm), VoteGranted: false}

	if int(req.Term) > rf.currentTerm {
		rf.stepDownToFollower(int(req.Term))
	}

	lastIdx, lastTerm := rf.getLastLogInfo()
	upToDate := req.LastLogTerm > int64(lastTerm) || (req.LastLogTerm == int64(lastTerm) && req.LastLogIndex >= int64(lastIdx))

	if upToDate && (rf.votedFor == -1 || rf.votedFor == int(req.CandidateId)) {
		rf.votedFor = int(req.CandidateId)
		rf.resetElectionTimer()
		resp.VoteGranted = true
	}
	resp.Term = int64(rf.currentTerm)
	return resp, nil
}

// ---------- RPC AppendEntries ---------- //
func (ns *NetworkService) AppendEntries(
	ctx context.Context, req *pb.AppendRequest,
) (*pb.AppendResponse, error) {

	rf := ns.node.raft
	rf.mu.Lock()
	defer rf.mu.Unlock()

	resp := &pb.AppendResponse{Term: int64(rf.currentTerm), Success: false}

	// 1. Mandato desfasado → rechazamos sin tocar el timer
	if int(req.Term) < rf.currentTerm {
		return resp, nil
	}

	// 2. Término nuevo → nos volvemos follower
	if int(req.Term) > rf.currentTerm {
		rf.stepDownToFollower(int(req.Term))
	}

	// 🔑 3. CUALQUIER AppendEntries válido reinicia el timer
	rf.resetElectionTimer()

	/* ------------------------------------------------------------------ */
	/* 4. Intentamos emparejar logs y, si procede, adelantamos commit.    */
	/* ------------------------------------------------------------------ */

	if rf.applyEntries(req) {
		newCommit := min(
			int(req.LeaderCommit),
			rf.lastIncludedIndex+len(rf.log),
		)
		if newCommit > rf.commitIndex {
			rf.commitIndex = newCommit
			go rf.applyLogs() // fuera del lock
		}
		resp.Success = true
	}

	resp.Term = int64(rf.currentTerm)
	return resp, nil
}

// ---------- RPC InstallSnapshot (stream) ---------- //

func (ns *NetworkService) InstallSnapshot(stream pb.RaftService_InstallSnapshotServer) error {
	var buf bytes.Buffer
	var idx, term int64

	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		buf.Write(chunk.Data)
		if chunk.IsLast {
			idx = chunk.LastIncludedIndex
			term = chunk.LastIncludedTerm
		}
	}

	if err := ns.node.raft.applySnapshot(buf.Bytes(), idx, term); err != nil {
		return stream.SendAndClose(&pb.SnapshotAck{Success: false})
	}
	return stream.SendAndClose(&pb.SnapshotAck{Success: true})
}

// ---------- RPC JoinCluster ---------- //

func (ns *NetworkService) JoinCluster(ctx context.Context, req *pb.JoinRequest) (*pb.JoinResponse, error) {
	leader := ns.node.raft
	leader.mu.Lock()
	defer leader.mu.Unlock()

	if leader.state != Leader {
		return nil, status.Error(codes.FailedPrecondition, "no soy líder")
	}

	newPeer, err := NewPeer(int(req.NodeId), req.Address)
	if err != nil {
		return &pb.JoinResponse{Success: false, Message: err.Error()}, nil
	}

	leader.peers = append(leader.peers, newPeer)
	log.Printf("[Líder] nodo %d añadido", newPeer.ID)
	return &pb.JoinResponse{Success: true, Message: "bienvenido"}, nil
}

/* ---------- FileManager ---------- */

type FileManager struct {
	baseDir string
}

func NewFileManager() *FileManager {
	dir := os.Getenv("FILE_BASE_DIR")
	if dir == "" {
		dir = "/app/files"
	}
	return &FileManager{baseDir: dir}
}

func (fm *FileManager) ValidatePath(p string) bool {
	clean := filepath.Clean(p)
	return filepath.IsAbs(clean) && strings.HasPrefix(clean, fm.baseDir)
}
func (fm *FileManager) Sync()                     {}
func (fm *FileManager) SyncFromSnapshot(_ []byte) {}

// ---------- Helpers ---------- //

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

/* ---------- utils ---------- */
func respMsg(err error, okMsg string) string {
	if err != nil {
		return err.Error()
	}
	return okMsg
}

func msg(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}
