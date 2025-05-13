package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	pb "so-final/proto"

	"google.golang.org/protobuf/proto"
)

/* ---------- tipos y utilidades ---------- */

type RaftState int

// RaftState es el estado del nodo en el algoritmo de consenso
const (
	Follower RaftState = iota
	Candidate
	Leader
)

//	Entrada de log Raft → {Term, Command};
//
// el comando es genérico (interface{}) pero en este proyecto siempre es un *pb.FileCommand.
type LogEntry struct {
	Term    int
	Command interface{}
}

// Utilidades: max entre dos enteros;
// toProto serializa un slice de LogEntry a []*pb.LogEntry (protobuf) para AppendEntries.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func toProto(entries []LogEntry) []*pb.LogEntry {
	out := make([]*pb.LogEntry, 0, len(entries))
	for _, le := range entries {
		if fc, ok := le.Command.(*pb.FileCommand); ok {
			b, _ := proto.Marshal(fc)
			out = append(out, &pb.LogEntry{Term: int64(le.Term), Command: b})
		}
	}
	return out
}

/* ---------- estructura principal ---------- */
/* Contiene todo el estado duradero y volátil que describe el algoritmo Raft:

   Persistente: currentTerm, votedFor, log, snapshot (lastIncluded*, snapshot).

   Volátil (en cada nodo): commitIndex, lastApplied.

   Solo en el líder: nextIndex[], matchIndex[].

   Infra: state, electionTimer, lista de *Peer.  */
type Raft struct {
	mu sync.RWMutex

	id          int
	currentTerm int
	votedFor    int
	log         []LogEntry

	commitIndex int
	lastApplied int

	nextIndex  []int
	matchIndex []int

	state         RaftState
	electionTimer *time.Timer

	/* snapshot mínimo */
	lastIncludedIndex int
	lastIncludedTerm  int
	snapshot          []byte
	snapLock          sync.Mutex

	peers []*Peer
}

/* ---------- constructor ---------- */
/* Detecta el ID máximo de todos los peers para dimensionar los arrays.

Crea el Raft con:

    Entrada sentinela en el índice 0 (Term = 0),

    votedFor = -1,

    estado inicial Follower.

Carga snapshot y meta-datos si existen, ajusta nextIndex[], commitIndex, lastApplied.

Persiste el estado y enciende el election timer con resetElectionTimer.

Log “follower listo”. */
func NewRaft(id int, peers []*Peer) *Raft {
	// 1. Calcular ID máx. ----------------------------------------------
	maxID := id
	for _, p := range peers {
		if p != nil && p.ID > maxID {
			maxID = p.ID
		}
	}

	size := maxID + 1 // índices válidos 0…maxID
	rf := &Raft{
		id:       id,
		votedFor: -1,
		state:    Follower,
		// ⬇️ Entrada sentinela: índice 0 siempre existe y tiene Term 0
		log:        []LogEntry{{Term: 0}},
		peers:      peers,
		nextIndex:  make([]int, size),
		matchIndex: make([]int, size),
	}

	// 2. Inicializar con snapshot --------------------------------------
	rf.mu.Lock()
	rf.loadSnapshot()
	rf.loadStateInternal()

	if rf.lastIncludedIndex > 0 {
		for i := range rf.nextIndex {
			rf.nextIndex[i] = rf.lastIncludedIndex + 1
		}
		rf.commitIndex = rf.lastIncludedIndex
		rf.lastApplied = rf.lastIncludedIndex
	}
	rf.mu.Unlock()

	// 3. Arranque -------------------------------------------------------
	rf.saveState()
	rf.resetElectionTimer()
	log.Printf("[Nodo %d] follower listo", id)
	return rf
}

/* ---------- persistencia básica ---------- */
// Construye la ruta …/RAFT_DATA_DIR/node<id>
func (rf *Raft) dir() string {
	return filepath.Join(os.Getenv("RAFT_DATA_DIR"), fmt.Sprintf("node%d", rf.id))
}

// Serializa meta.json y log.bin (uno por línea, formato protobuf). Versión Locked se usa cuando ya está tomado el mu.
func (rf *Raft) saveState() {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	rf.saveStateLocked()
}

func (rf *Raft) saveStateLocked() {
	os.MkdirAll(rf.dir(), 0o755)
	meta := struct {
		CurrentTerm int
		VotedFor    int
		CommitIndex int
		LastApplied int
	}{rf.currentTerm, rf.votedFor, rf.commitIndex, rf.lastApplied}
	b, _ := json.Marshal(meta)
	os.WriteFile(path.Join(rf.dir(), "meta.json"), b, 0o644)

	var buf bytes.Buffer
	for i, le := range rf.log {
		if fc, ok := le.Command.(*pb.FileCommand); ok {
			cb, _ := proto.Marshal(fc)
			lb, _ := proto.Marshal(&pb.LogEntry{Term: int64(le.Term), Command: cb})
			buf.Write(lb)
			if i < len(rf.log)-1 {
				buf.WriteByte('\n')
			}
		}
	}
	os.WriteFile(path.Join(rf.dir(), "log.bin"), buf.Bytes(), 0o644)
}

// Deserializa lo anterior: restaura meta-datos y reconstruye el slice log.
// Asegura que commitIndex y lastApplied nunca queden antes del último snapshot.
func (rf *Raft) loadStateInternal() {
	meta := path.Join(rf.dir(), "meta.json")
	if b, err := os.ReadFile(meta); err == nil {
		var m struct {
			CurrentTerm int
			VotedFor    int
			CommitIndex int
			LastApplied int
		}
		if json.Unmarshal(b, &m) == nil {
			rf.currentTerm, rf.votedFor, rf.commitIndex, rf.lastApplied = m.CurrentTerm, m.VotedFor, m.CommitIndex, m.LastApplied
		}
	}
	logFile := path.Join(rf.dir(), "log.bin")
	if content, err := os.ReadFile(logFile); err == nil {
		lines := bytes.Split(content, []byte("\n"))
		for _, l := range lines {
			if len(l) == 0 {
				continue
			}
			var pe pb.LogEntry
			if proto.Unmarshal(l, &pe) != nil {
				continue
			}
			var fc pb.FileCommand
			if proto.Unmarshal(pe.Command, &fc) != nil {
				continue
			}
			rf.log = append(rf.log, LogEntry{Term: int(pe.Term), Command: &fc})
		}
	}
	rf.commitIndex = max(rf.commitIndex, rf.lastIncludedIndex)
	rf.lastApplied = max(rf.lastApplied, rf.lastIncludedIndex)
}

/* ---------- snapshot mínimo ---------- */
// Si existe snapshot.bin, delega en applySnapshot.
func (rf *Raft) loadSnapshot() {
	snap := path.Join(rf.dir(), "snapshot.bin")
	if data, err := os.ReadFile(snap); err == nil {
		rf.applySnapshot(data, 0, 0)
	}
}

//	Líder guarda {LastIncludedIndex, LastIncludedTerm} como JSON.
//
// No almacena el contenido de archivos (simplificado).
func (rf *Raft) takeSnapshot() {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	info := struct{ LastIncludedIndex, LastIncludedTerm int }{rf.commitIndex, rf.currentTerm}
	b, _ := json.Marshal(info)
	os.WriteFile(path.Join(rf.dir(), "snapshot.bin"), b, 0o644)
}

//	Actualiza índices (lastIncluded*), descarta log anterior y sobrescribe snapshot en memoria.
//
// Se sincroniza con snapLock para evitar carreras con streaming.
func (rf *Raft) applySnapshot(data []byte, idx, term int64) error {
	rf.snapLock.Lock()
	defer rf.snapLock.Unlock()
	rf.lastIncludedIndex = int(idx)
	rf.lastIncludedTerm = int(term)
	rf.log = nil
	rf.commitIndex = rf.lastIncludedIndex
	rf.lastApplied = rf.lastIncludedIndex
	rf.snapshot = data
	return nil
}

/* ---------- timers ---------- */
// Devuelve un random 400-800 ms (previene colisiones de elecciones).
func timeout() time.Duration { return time.Duration(rand.Intn(400)+400) * time.Millisecond }

// Cancela el anterior, programa un nuevo time.AfterFunc. Si expira y no somos líder ⇒ startElection().
func (rf *Raft) resetElectionTimer() {
	if rf.electionTimer != nil {
		rf.electionTimer.Stop()
	}
	rf.electionTimer = time.AfterFunc(timeout(), func() {
		rf.mu.Lock()
		defer rf.mu.Unlock()
		if rf.state != Leader {
			rf.startElection()
		}
	})
}

/* ---------- elecciones y liderazgo ---------- */
/* Cambia a Candidate, incrementa currentTerm, vota por sí mismo.

Construye VoteRequest con la info de su último log.

Envía la petición a cada peer en goroutines (timeout 300 ms).

Usa atomic.AddInt32 sobre votes para contar réponses.

Si logra mayoría → becomeLeader; si termina sin mayoría → vuelve a Follower y reinicia el timer. */
func (rf *Raft) startElection() {
	rf.state = Candidate
	rf.currentTerm++
	rf.votedFor = rf.id

	lastIdx, lastTerm := rf.getLastLogInfo()
	args := &pb.VoteRequest{
		Term:         int64(rf.currentTerm),
		CandidateId:  int64(rf.id),
		LastLogIndex: int64(lastIdx),
		LastLogTerm:  int64(lastTerm),
	}

	var votes int32 = 1
	var wg sync.WaitGroup
	for _, p := range rf.peers {
		if p.ID == rf.id || p.client == nil {
			continue
		}
		wg.Add(1)
		go func(pr *Peer) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			if resp, err := pr.client.RequestVote(ctx, args); err == nil {
				rf.mu.Lock()
				if resp.Term > int64(rf.currentTerm) {
					rf.stepDownToFollower(int(resp.Term))
				} else if resp.VoteGranted && rf.state == Candidate {
					// ahora (N nodos = len(peers)+1; mayoría = N/2 redondeado ↑)
					majority := (len(rf.peers)+1)/2 + 1 // 3 en un clúster de 4
					if atomic.AddInt32(&votes, 1) >= int32(majority) {
						rf.becomeLeader()
					}
				}
				rf.mu.Unlock()
			}
		}(p)
	}
	go func() {
		wg.Wait()
		rf.mu.Lock()
		if rf.state == Candidate {
			rf.state = Follower
			rf.resetElectionTimer()
		}
		rf.mu.Unlock()
	}()
}

/* Se llama cuando recibe un término mayor: actualiza term, pasa a Follower, borra votedFor, reinicia timer y persiste. */
func (rf *Raft) stepDownToFollower(term int) {
	if term <= rf.currentTerm {
		return
	}
	rf.currentTerm = term
	rf.state = Follower
	rf.votedFor = -1
	rf.resetElectionTimer()
	rf.saveStateLocked()
}

/*
	Confirma que seguía en estado Candidate.

Marca state = Leader.

Inicializa nextIndex[i] = lastLogIndex + 1 para todos.

Persiste y lanza goroutine sendHeartbeats() para empezar replicación.
*/
func (rf *Raft) becomeLeader() {
	if rf.state != Candidate {
		return
	}
	rf.state = Leader
	lastIndex := rf.lastIncludedIndex + len(rf.log) - 1
	for i := range rf.nextIndex {
		rf.nextIndex[i] = lastIndex + 1
	}
	rf.saveStateLocked()
	go rf.sendHeartbeats()
	log.Printf("[Nodo %d] líder en término %d", rf.id, rf.currentTerm)
}

/* ---------- replicación ---------- */
/* Ticker de 50 ms.

Mientras sigamos siendo líder: copia peers y llama a syncNode(p) en paralelo. */
func (rf *Raft) sendHeartbeats() {
	tk := time.NewTicker(50 * time.Millisecond)
	defer tk.Stop()
	for range tk.C {
		rf.mu.Lock()
		if rf.state != Leader {
			rf.mu.Unlock()
			return
		}
		peers := append([]*Peer(nil), rf.peers...)
		rf.mu.Unlock()

		for _, p := range peers {
			if p.ID == rf.id || p.client == nil {
				continue
			}
			go rf.syncNode(p)
		}
	}
}

/*
	Calcula nextIndex y prevLog (incluyendo casos tras snapshot).

Envía AppendRequest con las entradas pendientes.

Según la AppendResponse:

	Term mayor ⇒ stepDownToFollower.

	Success==true ⇒ avanza nextIndex, actualiza matchIndex, llama a updateCommitIndex.

	Failure ⇒ retrocede nextIndex (back-off exponencial simplificado: -1).
*/
func (rf *Raft) syncNode(peer *Peer) error {
	rf.mu.Lock()
	if rf.state != Leader {
		rf.mu.Unlock()
		return errors.New("no líder")
	}
	next := rf.nextIndex[peer.ID]
	prev := next - 1
	var prevTerm int64
	if prev > rf.lastIncludedIndex {
		if rel := prev - rf.lastIncludedIndex; rel >= 0 && rel < len(rf.log) {
			prevTerm = int64(rf.log[rel].Term)
		}
	}
	entries := rf.logSliceFrom(next)
	args := &pb.AppendRequest{
		Term:         int64(rf.currentTerm),
		LeaderId:     int64(rf.id),
		PrevLogIndex: int64(prev),
		PrevLogTerm:  prevTerm,
		Entries:      toProto(entries),
		LeaderCommit: int64(rf.commitIndex),
	}
	rf.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	resp, err := peer.client.AppendEntries(ctx, args)
	if err != nil {
		return err
	}

	rf.mu.Lock()
	defer rf.mu.Unlock()
	if resp.Term > int64(rf.currentTerm) {
		rf.stepDownToFollower(int(resp.Term))
		return nil
	}
	if resp.Success {
		rf.nextIndex[peer.ID] = next + len(entries)
		rf.matchIndex[peer.ID] = rf.nextIndex[peer.ID] - 1
		rf.updateCommitIndex()
		rf.saveStateLocked()
	} else {
		rf.nextIndex[peer.ID] = max(1, rf.nextIndex[peer.ID]-1)
	}
	return nil
}

func (rf *Raft) logSliceFrom(idx int) []LogEntry {
	rel := idx - rf.lastIncludedIndex
	if rel < 0 || rel >= len(rf.log) {
		return nil
	}
	return rf.log[rel:]
}

/* ---------- aplicar entradas ---------- */
// Se ejecuta al recibir AppendEntries. Valida prevLog y, si encaja, recorta/añade las nuevas entradas.
func (rf *Raft) applyEntries(req *pb.AppendRequest) bool {
	prev := int(req.PrevLogIndex) - rf.lastIncludedIndex
	if prev < 0 || prev >= len(rf.log) {
		return false
	}
	if rf.log[prev].Term != int(req.PrevLogTerm) {
		rf.log = rf.log[:prev]
		return false
	}

	// agregar nuevas
	for _, pe := range req.Entries {
		var fc pb.FileCommand
		if proto.Unmarshal(pe.Command, &fc) != nil {
			continue
		}
		rf.log = append(rf.log, LogEntry{Term: int(pe.Term), Command: &fc})
	}
	return true
}

/* ---------- commit helpers ---------- */

func (rf *Raft) getLastLogInfo() (idx, term int) {
	if len(rf.log) == 0 {
		return rf.lastIncludedIndex, rf.lastIncludedTerm
	}
	return rf.lastIncludedIndex + len(rf.log) - 1, rf.log[len(rf.log)-1].Term
}

/* ---------- proposición de comandos ---------- */

func (rf *Raft) ProposeCommand(cmd *pb.FileCommand) error {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.state != Leader {
		return errors.New("no soy líder")
	}
	rf.log = append(rf.log, LogEntry{Term: rf.currentTerm, Command: cmd})
	rf.matchIndex[rf.id] = rf.lastIncludedIndex + len(rf.log) - 1
	rf.saveStateLocked()
	go rf.sendHeartbeats()
	return nil
}

// Aplica todas las entradas del log que ya alcanzaron commitIndex
func (rf *Raft) applyLogs() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	base := os.Getenv("FILE_BASE_DIR")
	if base == "" {
		base = "/app/files"
	}

	for rf.lastApplied < rf.commitIndex {
		rf.lastApplied++

		// Índice relativo dentro del slice rf.log
		rel := rf.lastApplied - rf.lastIncludedIndex
		if rel < 0 || rel >= len(rf.log) {
			continue // puede ocurrir justo después de un snapshot
		}

		entry := rf.log[rel]
		cmd, ok := entry.Command.(*pb.FileCommand)
		if !ok {
			continue // entrada no reconocida
		}

		target := filepath.Join(base, filepath.Clean(cmd.Path))

		switch cmd.Op {
		case pb.FileCommand_TRANSFER:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				log.Printf("mkdir %s: %v", filepath.Dir(target), err)
				continue
			}
			if err := os.WriteFile(target, cmd.Content, 0o644); err != nil {
				log.Printf("write %s: %v", target, err)
			}

		case pb.FileCommand_DELETE:
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				log.Printf("remove %s: %v", target, err)
			}

		case pb.FileCommand_MKDIR:
			if err := os.MkdirAll(target, 0o755); err != nil {
				log.Printf("mkdir %s: %v", target, err)
			}

		case pb.FileCommand_RMDIR:
			if err := os.RemoveAll(target); err != nil && !os.IsNotExist(err) {
				log.Printf("rmdir %s: %v", target, err)
			}
		}
	}

	// Persistimos el nuevo lastApplied para asegurar consistencia tras reinicio
	rf.saveStateLocked()
}

// must be called with rf.mu locked (only by the leader)
func (rf *Raft) updateCommitIndex() {
	upper := rf.lastIncludedIndex + len(rf.log) - 1 // índice real máximo
	for n := rf.commitIndex + 1; n <= upper; n++ {
		votes := 1
		for _, p := range rf.peers {
			if p.ID != rf.id && rf.matchIndex[p.ID] >= n {
				votes++
			}
		}
		if votes >= rf.quorumSize() &&
			rf.log[n-rf.lastIncludedIndex].Term == rf.currentTerm {
			rf.commitIndex = n
		}
	}
	if rf.commitIndex > rf.lastApplied {
		go rf.applyLogs()
	}
}

// ---------- quorum helper ----------

// Devuelve true si el número de peers con conexión READY
// + el propio nodo alcanza la mayoría necesaria.
func (rf *Raft) checkQuorum() bool {
	active := 1 // nos contamos a nosotros mismos
	for _, p := range rf.peers {
		if p != nil && p.IsActive() {
			active++
		}
	}
	return active >= rf.quorumSize()
}

// quorumSize devuelve N/2 redondeado ↑ (mayoría estricta)
func (rf *Raft) quorumSize() int {
	return (len(rf.peers)+1)/2 + 1
}
