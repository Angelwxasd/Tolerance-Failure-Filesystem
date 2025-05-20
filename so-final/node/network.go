package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/ioutil"
	"log"
	"net"
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
/* Referencia al nodo (*Node) para acceder a Raft y demás subsistemas.

fileMgr para validar rutas y acceder al directorio base.
Implementa todas las RPC declaradas en proto.RaftService. */
type NetworkService struct {
	pb.UnimplementedRaftServiceServer
	node    *Node
	fileMgr *FileManager
}

/* ---------- helpers comunes ---------- */

// crea un FileCommand y lo envía al líder ­(o devuelve error si este nodo no es líder)
/* Comprueba que el nodo sea líder; si no, devuelve error "no soy líder".

Construye un pb.FileCommand (op, path, content) y lo envía a Raft con ProposeCommand, iniciando la replicación.
Se reutiliza por las cuatro RPC de sistema de archivos. */
func (ns *NetworkService) buildAndPropose(
	op pb.FileCommand_Operation, path string, content []byte) error {

	if ns.node.raft.state != Leader {
		return errors.New("no soy líder")
	}
	cmd := &pb.FileCommand{Op: op, Path: path, Content: content}
	return ns.node.raft.ProposeCommand(cmd)
}

// ---------- Gestión de Peers ---------- //
/* Mantiene los metadatos y la conexión saliente a otro nodo:

   conn – *grpc.ClientConn reutilizable.

   client – stub generado para invocar RPC.

   isActive – atomic.Bool que indica si la conexión está lista o idle. */
type Peer struct {
	ID      int
	Address string

	conn   *grpc.ClientConn
	client pb.RaftServiceClient

	mu       sync.Mutex
	isActive atomic.Bool
}

// Crea la estructura y llama a dial() para establecer la primera conexión.
func NewPeer(id int, addr string) (*Peer, error) {
	p := &Peer{ID: id, Address: addr}
	return p, p.dial()
}

/*
	Configura parámetros keep-alive de lado cliente.

Usa grpc.DialContext con timeout = 5 s y credenciales “insecure” (sin TLS en el laboratorio).

Si la conexión se establece, guarda conn, crea el stub y marca el peer como activo.

Arranca watch() en una goroutine para vigilar la salud del canal.
*/
func (p *Peer) dial() error {
	kp := keepalive.ClientParameters{Time: 2 * time.Minute, Timeout: 20 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, p.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(kp),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			dialer := &net.Dialer{}
			return dialer.DialContext(ctx, "tcp", addr)
		}))

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
// peer.go – no desconectarse por un canal Idle
/* Bucle eterno que:

   Consulta conn.GetState().

   Marca isActive = Ready ∨ Idle.

   Si el estado pasa a TransientFailure o Shutdown, invoca Reconnect().

   Reintenta cada 2 s para no bloquear el scheduler. */
func (p *Peer) watch() {
	for {
		if p.conn == nil {
			p.Reconnect()
		} else {
			st := p.conn.GetState()
			active := st == connectivity.Ready || st == connectivity.Idle
			p.isActive.Store(active)

			// solo reconectar en fallos reales
			if st == connectivity.TransientFailure || st == connectivity.Shutdown {
				p.Reconnect()
			}
		}
		time.Sleep(2 * time.Second)
	}
}

// Lectura atómica de la bandera de salud.
func (p *Peer) IsActive() bool { return p.isActive.Load() }

/* ---------- RPC Sistema de archivos ---------- */

// 1. Transferir archivo
/* 	Valida ruta; construye comando TRANSFER; devuelve success=true solo si el líder replicó la entrada.*/
func (ns *NetworkService) TransferFile(
	ctx context.Context, req *pb.FileData) (*pb.GenericResponse, error) {

	if !ns.fileMgr.ValidatePath(req.Filename) {
		return &pb.GenericResponse{Success: false, Message: "ruta inválida"}, nil
	}
	err := ns.buildAndPropose(pb.FileCommand_TRANSFER, req.Filename, req.Content)
	return &pb.GenericResponse{Success: err == nil, Message: msg(err, "transferencia replicada")}, nil
}

//  2. Borrar archivo
//     Igual que arriba pero con operación DELETE.
func (ns *NetworkService) DeleteFile(
	ctx context.Context, req *pb.DeleteRequest) (*pb.GenericResponse, error) {

	if !ns.fileMgr.ValidatePath(req.Filename) {
		return &pb.GenericResponse{Success: false, Message: "ruta inválida"}, nil
	}
	err := ns.buildAndPropose(pb.FileCommand_DELETE, req.Filename, nil)
	return &pb.GenericResponse{Success: err == nil, Message: msg(err, "archivo borrado")}, nil
}

// 3. Crear directorio
// Crea directorio (MKDIR).
func (ns *NetworkService) MkDir(
	ctx context.Context, req *pb.MkDirRequest) (*pb.GenericResponse, error) {

	if !ns.fileMgr.ValidatePath(req.Dirname) {
		return &pb.GenericResponse{Success: false, Message: "ruta inválida"}, nil
	}
	err := ns.buildAndPropose(pb.FileCommand_MKDIR, req.Dirname, nil)
	return &pb.GenericResponse{Success: err == nil, Message: msg(err, "directorio creado")}, nil
}

// 4. Eliminar directorio
// Elimina directorio (RMDIR).
func (ns *NetworkService) RemoveDir(
	ctx context.Context, req *pb.RemoveDirRequest) (*pb.GenericResponse, error) {

	if !ns.fileMgr.ValidatePath(req.Dirname) {
		return &pb.GenericResponse{Success: false, Message: "ruta inválida"}, nil
	}
	err := ns.buildAndPropose(pb.FileCommand_RMDIR, req.Dirname, nil)
	return &pb.GenericResponse{Success: err == nil, Message: msg(err, "directorio eliminado")}, nil
}

// 5. Listar (read-only, sin consenso)
/* Read-only: lista nombres dentro de req.Path directamente desde disco, sin pasar por Raft.
Devuelve codes.InvalidArgument ante rutas sospechosas. */
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

// Cierra la conexión rota (si existe) y prueba hasta 3 rediales exponenciales (2 s, 4 s, 6 s).
// Si logra reconectar, escribe un log; si no, deja al peer como inactivo.
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
/* Implementa la fase de votación:

   Si el término entrante es mayor, el nodo se convierte en Follower.

   Comprueba que el candidato esté “up-to-date” (último índice/term).

   Concede el voto si cumple las reglas y no ha votado aún.

   Resetea el election timer al votar para evitar split-votes. */
func (ns *NetworkService) RequestVote(ctx context.Context, req *pb.VoteRequest) (*pb.VoteResponse, error) {
	rf := ns.node.raft
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// Preparo la respuesta con el término actual por defecto
	resp := &pb.VoteResponse{
		Term:        int64(rf.currentTerm),
		VoteGranted: false,
	}

	// Si el candidato viene con un término superior, bajo a follower
	if int(req.Term) > rf.currentTerm {
		rf.stepDownToFollower(int(req.Term))
	}

	// Compruebo si el log del candidato está "tan al día o más" que el mío
	lastIdx, lastTerm := rf.getLastLogInfo()
	upToDate := req.LastLogTerm > int64(lastTerm) ||
		(req.LastLogTerm == int64(lastTerm) && req.LastLogIndex >= int64(lastIdx))

	// Concedo el voto si no he votado aún en este término (o ya voté al mismo candidato)
	if upToDate && (rf.votedFor == -1 || rf.votedFor == int(req.CandidateId)) {
		rf.votedFor = int(req.CandidateId)
		rf.persistState()       // guarda votedFor y currentTerm en disco
		rf.resetElectionTimer() // reinicio el timeout de elección
		resp.VoteGranted = true
	}

	// Actualizo el término en la respuesta (por si stepDown cambió rf.currentTerm)
	resp.Term = int64(rf.currentTerm)
	return resp, nil
}

// ---------- RPC AppendEntries ---------- //
/* Lógica de replicación/heartbeat:

    Rechaza solicitudes con término obsoleto.

    Con un término nuevo, hace stepDownToFollower.

    Cualquier solicitud válida reinicia el election timer.

    Llama a applyEntries(req) para alinear logs; si hay éxito,
	avanza commitIndex al mínimo entre leaderCommit y log local, y lanza applyLogs() fuera del candado. */
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
/* Recibe un snapshot en chunks:

   Concatena datos en bytes.Buffer hasta encontrar el chunk con IsLast=true.

   Extrae LastIncludedIndex/Term y delega en raft.applySnapshot(buf, idx, term).

   Envía un SnapshotAck con Success verdadero o falso. */
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
/* Permite que un nodo nuevo se una dinámicamente:

   Solo el líder acepta la petición (codes.FailedPrecondition si no).

   Crea un Peer con NewPeer; si el dial falla, se responde con Success=false.

   Inserta el peer en leader.peers y confirma “bienvenido”. */
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
/* Abstrae el directorio base (FILE_BASE_DIR, default /app/files).
 */
type FileManager struct {
	baseDir string
}

func NewFileManager() *FileManager {
	dir := os.Getenv("FILE_BASE_DIR")
	if dir == "" {
		dir = "/srv/files"
	}
	return &FileManager{baseDir: dir}
}

// ValidatePath – Comprueba que la ruta sea absoluta, normalizada y dentro de baseDir (previene path-traversal).
func (fm *FileManager) ValidatePath(p string) bool {
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(fm.baseDir, clean)
	}
	return strings.HasPrefix(clean, fm.baseDir)
}

// Sync / SyncFromSnapshot – Place-holders para fsync o restaurar desde snapshot (aún vacíos en este fragmento).
func (fm *FileManager) Sync()                     {}
func (fm *FileManager) SyncFromSnapshot(_ []byte) {}

// ---------- Helpers ---------- //
// Devuelve el menor de dos enteros (se usa para recortar commitIndex).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

/* ---------- utils ---------- */
// 	Si err != nil devuelve err.Error(), de lo contrario un mensaje de éxito. Evita repetir lógica en cada RPC.
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

// llama a persistState justo después de cambiar rf.currentTerm o rf.votedFor
func (rf *Raft) persistState() {
	// 1) Estructura mínima a serializar
	state := struct {
		Term     int `json:"currentTerm"`
		VotedFor int `json:"votedFor"`
	}{
		Term:     rf.currentTerm,
		VotedFor: rf.votedFor,
	}

	// 2) Serializo a JSON
	data, err := json.Marshal(state)
	if err != nil {
		log.Fatalf("Raft.persistState: error al serializar estado: %v", err)
	}

	// 3) Escribo atómicamente al disco
	file := filepath.Join(rf.dataDir, "state.json")
	if err := ioutil.WriteFile(file, data, 0644); err != nil {
		log.Fatalf("Raft.persistState: error al escribir %s: %v", file, err)
	}
}

func (rf *Raft) readPersist() {
	file := filepath.Join(rf.dataDir, "state.json")
	data, err := ioutil.ReadFile(file)
	if err != nil {
		// si no existe, es la primera vez: currentTerm=0, votedFor=-1
		rf.currentTerm = 0
		rf.votedFor = -1
		return
	}
	var state struct {
		Term     int `json:"currentTerm"`
		VotedFor int `json:"votedFor"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		log.Fatalf("Raft.readPersist: error al deserializar %s: %v", file, err)
	}
	rf.currentTerm = state.Term
	rf.votedFor = state.VotedFor
}
