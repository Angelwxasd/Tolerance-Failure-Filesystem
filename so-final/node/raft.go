package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	pb "so-final/proto"
	"strings"

	"sync"
	"time"

	"google.golang.org/protobuf/proto"
)

type RaftState int

const (
	Follower RaftState = iota
	Candidate
	Leader
)

type LogEntry struct {
	Term    int
	Command interface{}
}

// Helper function to convert []LogEntry to []*pb.LogEntry
func convertToProtoLogEntries(entries []LogEntry) []*pb.LogEntry {
	protoEntries := make([]*pb.LogEntry, len(entries))
	for i, entry := range entries {
		var cmd *pb.FileCommand
		if fileCmd, ok := entry.Command.(*pb.FileCommand); ok {
			cmd = fileCmd
		} else {
			log.Printf("Comando no es FileCommand")
			continue
		}
		cmdBytes, _ := proto.Marshal(cmd)
		protoEntries[i] = &pb.LogEntry{
			Term:    int64(entry.Term),
			Command: cmdBytes,
		}
	}
	return protoEntries
}

type Raft struct {
	mu          sync.Mutex
	id          int
	currentTerm int
	votedFor    int
	log         []LogEntry

	commitIndex int
	lastApplied int

	nextIndex  []int
	matchIndex []int

	state RaftState
	//lastHeartbeat time.Time
	electionTimer *time.Timer

	// Snapshots
	lastIncludedIndex int
	lastIncludedTerm  int
	leaderId          int // Debes mantener actualizado este valor
	snapshot          []byte
	snapshotLock      sync.Mutex

	peers []*Peer
}

func NewRaft(id int, peers []*Peer) *Raft {
	if peers == nil {
		log.Fatal("La lista de peers no puede ser nil")
	}
	rf := &Raft{
		id:                id,
		currentTerm:       0,
		votedFor:          -1,
		state:             Follower,
		log:               make([]LogEntry, 0),
		peers:             peers, // Inicializar con peers reales
		nextIndex:         make([]int, len(peers)),
		matchIndex:        make([]int, len(peers)),
		lastIncludedIndex: 0,
		lastIncludedTerm:  0,
		snapshot:          make([]byte, 0),
	}

	// 🔄 CAMBIO: Cargar snapshot antes que el estado
	if err := rf.loadSnapshot(); err != nil {
		log.Printf("Error cargando snapshot: %v", err)
	}

	// 🔄 CAMBIO: Ajustar índices del log después de cargar snapshot
	rf.commitIndex = rf.lastIncludedIndex
	rf.lastApplied = rf.lastIncludedIndex

	// ✅ Inicializar nextIndex con la longitud del log
	for i := range rf.nextIndex {
		rf.nextIndex[i] = rf.lastIncludedIndex + 1
	}

	if err := rf.loadState(); err != nil {
		log.Printf("Error al cargar estado Raft: %v", err)
	}

	rf.resetElectionTimer()

	// Forzar guardado inicial
	go func() {
		time.Sleep(1 * time.Second)
		rf.saveState()
	}()

	return rf
}

// En raft.go
func (rf *Raft) saveState() error {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	dataDir := filepath.Join(os.Getenv("RAFT_DATA_DIR"), fmt.Sprintf("node%d", rf.id))

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Printf("Error al crear directorio %s: %v", dataDir, err)
		return err
	}

	metaPath := path.Join(dataDir, "meta.json")
	logPath := path.Join(dataDir, "log.bin")

	// Guardar meta datos
	meta := struct {
		CurrentTerm int
		VotedFor    int
		CommitIndex int
		LastApplied int
	}{
		rf.currentTerm,
		rf.votedFor,
		rf.commitIndex,
		rf.lastApplied,
	}

	metaJSON, _ := json.Marshal(meta)
	if err := os.WriteFile(metaPath, metaJSON, 0644); err != nil {
		return err
	}

	// Guardar logs
	logEntries := make([][]byte, len(rf.log))
	for i, entry := range rf.log {
		cmdBytes, _ := proto.Marshal(entry.Command.(*pb.FileCommand))
		entryBytes, err := proto.Marshal(&pb.LogEntry{
			Term:    int64(entry.Term),
			Command: cmdBytes,
		})
		if err != nil {
			return err
		}
		logEntries[i] = entryBytes
	}

	logStrings := make([]string, len(logEntries))
	for i, entry := range logEntries {
		logStrings[i] = string(entry)
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(logStrings, "\n")), 0644); err != nil {
		return err
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Printf("Error al crear directorio %s: %v", dataDir, err)
		return err
	}

	return nil
}

func (rf *Raft) loadState() error {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	dataDir := filepath.Join(os.Getenv("RAFT_DATA_DIR"), fmt.Sprintf("node%d", rf.id))
	metaPath := path.Join(dataDir, "meta.json")
	logPath := path.Join(dataDir, "log.bin")

	// Cargar meta datos
	if _, err := os.Stat(metaPath); err == nil {
		metaJSON, err := os.ReadFile(metaPath)
		if err != nil {
			return fmt.Errorf("error al leer meta.json: %v", err)
		}
		var meta struct {
			CurrentTerm int
			VotedFor    int
			CommitIndex int
			LastApplied int
		}
		if err := json.Unmarshal(metaJSON, &meta); err != nil {
			return fmt.Errorf("error al deserializar meta.json: %v", err)
		}
		rf.currentTerm = meta.CurrentTerm
		rf.votedFor = meta.VotedFor
		rf.commitIndex = meta.CommitIndex
		rf.lastApplied = meta.LastApplied
	} else {
		log.Printf("No se encontró meta.json para node %d", rf.id)
	}

	// Cargar logs
	if _, err := os.Stat(logPath); err == nil {
		logContent, err := os.ReadFile(logPath)
		if err != nil {
			return fmt.Errorf("error al leer log.bin: %v", err)
		}
		lines := strings.Split(string(logContent), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			var entry pb.LogEntry
			if err := proto.Unmarshal([]byte(line), &entry); err != nil {
				log.Printf("Error al deserializar log: %v", err)
				continue
			}
			cmd := &pb.FileCommand{}
			if err := proto.Unmarshal(entry.Command, cmd); err != nil {
				log.Printf("Error al deserializar comando: %v", err)
				continue
			}
			rf.log = append(rf.log, LogEntry{
				Term:    int(entry.Term),
				Command: cmd,
			})
		}
	} else {
		log.Printf("No se encontró log.bin para node %d", rf.id)
		rf.log = make([]LogEntry, 0) // Inicializar log vacío si no hay datos
	}

	// 🔄 CAMBIO: Ajustar índices relativos al snapshot
	if rf.lastApplied < rf.lastIncludedIndex {
		rf.lastApplied = rf.lastIncludedIndex
	}
	if rf.commitIndex < rf.lastIncludedIndex {
		rf.commitIndex = rf.lastIncludedIndex
	}

	return nil
}

func (rf *Raft) resetElectionTimer() {
	if rf.electionTimer != nil {
		rf.electionTimer.Stop()
	}
	rf.electionTimer = time.AfterFunc(randomElectionTimeout(), rf.startElection)
}

func randomElectionTimeout() time.Duration {
	return time.Duration(150+rand.Intn(150)) * time.Millisecond
}

func (rf *Raft) startElection() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// Validar peers ANTES de la elección
	for i, peer := range rf.peers {
		if peer == nil || peer.client == nil {
			log.Printf("ERROR: Peer %d (ID %d) no está inicializado", i, peer.ID)
			return
		}
	}

	rf.state = Candidate
	rf.currentTerm++
	rf.votedFor = rf.id
	rf.saveState()

	args := &pb.VoteRequest{
		Term:         int64(rf.currentTerm),
		CandidateId:  int64(rf.id),
		LastLogIndex: int64(len(rf.log) - 1),
		LastLogTerm:  int64(rf.log[len(rf.log)-1].Term),
	}

	var (
		votes int
		mu    sync.Mutex // Mutex local para proteger votes
		wg    sync.WaitGroup
	)

	for _, peer := range rf.peers {
		wg.Add(1)
		go func(p *Peer) {
			defer wg.Done()

			resp, err := p.client.RequestVote(context.Background(), args)
			if err != nil || resp == nil {
				log.Printf("Error en RequestVote a %d: %v", p.ID, err)
				return
			}

			rf.mu.Lock()
			defer rf.mu.Unlock()

			if resp.Term > int64(rf.currentTerm) {
				rf.currentTerm = int(resp.Term)
				rf.state = Follower
				rf.votedFor = -1
				rf.saveState()
				return
			}

			mu.Lock()
			defer mu.Unlock()
			if resp.VoteGranted {
				votes++
				if votes > len(rf.peers)/2 && rf.state == Candidate {
					rf.becomeLeader()
				}
			}
		}(peer)
	}

	go func() {
		wg.Wait()
		rf.saveState() // Guardar estado final después de todas las respuestas
	}()
}

func (rf *Raft) becomeLeader() {
	rf.state = Leader
	// Inicializar nextIndex y matchIndex para cada peer
	for i := range rf.nextIndex {
		rf.nextIndex[i] = len(rf.log)
		rf.matchIndex[i] = 0
	}
	rf.saveState()         // Persistir el nuevo estado
	go rf.sendHeartbeats() // Enviar heartbeats periódicos
}

func (rf *Raft) sendHeartbeats() {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()

	for rf.state == Leader {
		rf.mu.Lock()
		currentTerm := rf.currentTerm
		lastIncludedIndex := rf.lastIncludedIndex
		savedPeers := make([]*Peer, len(rf.peers))
		copy(savedPeers, rf.peers)
		rf.mu.Unlock()

		for _, peer := range savedPeers {
			if peer == nil || peer.client == nil {
				log.Printf("Peer %s no está inicializado", peer.Address)
				continue
			}

			go func(p *Peer) {
				rf.mu.Lock()

				// 1. Obtener y validar nextIndex
				nextIdx := rf.nextIndex[p.ID]
				if nextIdx <= lastIncludedIndex {
					// 2. Enviar snapshot si está detrás del punto de truncamiento
					snapshot := rf.snapshot
					rf.mu.Unlock()

					rf.sendSnapshot(p, snapshot)
					return
				}

				// 3. Preparar argumentos para AppendEntries
				adjustedPrevIndex := nextIdx - 1 - lastIncludedIndex
				var prevLogTerm int
				entries := make([]LogEntry, 0)

				// 4. Calcular término del log anterior (si existe)
				if adjustedPrevIndex >= 0 && adjustedPrevIndex < len(rf.log) {
					prevLogTerm = rf.log[adjustedPrevIndex].Term
				}

				// 5. Obtener entradas a enviar (relativas al snapshot)
				if nextIdx-lastIncludedIndex < len(rf.log) {
					entries = rf.log[nextIdx-lastIncludedIndex:]
				}

				args := &pb.AppendRequest{
					Term:         int64(currentTerm),
					LeaderId:     int64(rf.id),
					PrevLogIndex: int64(nextIdx - 1), // Índice absoluto
					PrevLogTerm:  int64(prevLogTerm),
					Entries:      convertToProtoLogEntries(entries),
					LeaderCommit: int64(rf.commitIndex),
				}

				if len(entries) == 0 {
					args.Entries = nil // Heartbeat vacío
				}
				rf.mu.Unlock()

				// 6. Enviar RPC con timeout
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()

				resp, err := p.client.AppendEntries(ctx, args)
				if err != nil {
					return
				}

				// 7. Procesar respuesta
				rf.mu.Lock()
				defer rf.mu.Unlock()

				if resp.Term > int64(rf.currentTerm) {
					rf.stepDownToFollower(int(resp.Term))
					return
				}

				if resp.Success {
					newNextIdx := nextIdx + len(entries)
					rf.nextIndex[p.ID] = newNextIdx
					rf.matchIndex[p.ID] = newNextIdx - 1
					rf.updateCommitIndex()
				} else {
					// Retroceder nextIndex sin pasar el último snapshot
					rf.nextIndex[p.ID] = max(lastIncludedIndex+1, rf.nextIndex[p.ID]-1)
					log.Printf("Ajustando nextIndex para %s a %d", p.Address, rf.nextIndex[p.ID])
				}
			}(peer)
		}
		<-ticker.C
	}
}

// Función auxiliar para transición a Follower
func (rf *Raft) stepDownToFollower(term int) {
	rf.currentTerm = term
	rf.state = Follower
	rf.votedFor = -1
	rf.resetElectionTimer()
	rf.saveState()
	log.Printf("Nodo %d convertido a Follower en término %d", rf.id, term)
}

func (rf *Raft) applyLogs() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// 🔄 Aplicar desde el último índice aplicado +1 hasta commitIndex
	for i := rf.lastApplied + 1; i <= rf.commitIndex; i++ {
		adjustedIndex := i - rf.lastIncludedIndex

		// 🔄 Validar índice dentro del rango del log
		if adjustedIndex < 0 || adjustedIndex >= len(rf.log) {
			log.Printf("Índice inválido: %d (Ajustado: %d)", i, adjustedIndex)
			continue
		}

		entry := rf.log[adjustedIndex]
		switch cmd := entry.Command.(type) {
		case *pb.FileCommand:
			// 🔄 Usar ruta relativa al directorio base
			baseDir := os.Getenv("FILE_BASE_DIR")
			if baseDir == "" {
				baseDir = "/app/files"
			}
			fullPath := filepath.Join(baseDir, filepath.Base(cmd.Path))

			switch cmd.Op {
			case pb.FileCommand_TRANSFER:
				if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
					log.Printf("Error creando directorio: %v", err)
					continue
				}
				if err := os.WriteFile(fullPath, cmd.Content, 0644); err != nil {
					log.Printf("Error escribiendo archivo: %v", err)
				}
			case pb.FileCommand_DELETE:
				if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
					log.Printf("Error eliminando archivo: %v", err)
				}
			}
		}
	}
	rf.lastApplied = rf.commitIndex
	rf.saveState()
}

func (rf *Raft) transferFile(filePath string, targetNode string) error {
	// 🔄 Validar y limpiar la ruta del archivo
	cleanPath := filepath.Clean(filePath)
	if !filepath.IsAbs(cleanPath) {
		return fmt.Errorf("ruta debe ser absoluta: %s", cleanPath)
	}

	// 🔄 Leer archivo con permisos adecuados
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return fmt.Errorf("error leyendo archivo: %v", err)
	}

	// 🔄 Buscar peer por dirección completa
	targetAddr := strings.TrimSpace(targetNode)
	for _, peer := range rf.peers {
		if strings.TrimSpace(peer.Address) == targetAddr {
			_, err := peer.client.TransferFile(context.Background(), &pb.FileData{
				Content:  data,
				Filename: filepath.Base(cleanPath), // 🔄 Enviar solo nombre del archivo
			})
			return err
		}
	}
	return fmt.Errorf("nodo destino no encontrado: %s", targetAddr)
}

func (rf *Raft) deleteFile(path string) error {
	err := os.Remove(path)
	if err != nil {
		return err
	}
	log.Printf("Archivo eliminado: %s", path)
	return nil
}

// Función para sincronizar un nodo
func (rf *Raft) syncNode(peer *Peer) error {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// Validación de estado
	if rf.state != Leader {
		return errors.New("solo el líder puede sincronizar nodos")
	}

	// Usar nextIndex del peer como punto de partida
	nextIdx := rf.nextIndex[peer.ID]
	prevLogIndex := nextIdx - 1
	var prevLogTerm int64 = 0

	// Obtener término del índice anterior si existe
	if prevLogIndex >= 0 && prevLogIndex < len(rf.log) {
		prevLogTerm = int64(rf.log[prevLogIndex].Term)
	}

	// Preparar entradas a enviar
	entries := make([]LogEntry, 0)
	if nextIdx < len(rf.log) {
		entries = rf.log[nextIdx:]
	}

	// Construir AppendRequest
	args := &pb.AppendRequest{
		Term:         int64(rf.currentTerm),
		LeaderId:     int64(rf.id),
		PrevLogIndex: int64(prevLogIndex),
		PrevLogTerm:  prevLogTerm,
		Entries:      convertToProtoLogEntries(entries),
		LeaderCommit: int64(rf.commitIndex),
	}

	// Lógica de reintentos con backoff exponencial
	maxRetries := 3
	baseDelay := 100 * time.Millisecond
	for i := 0; i < maxRetries; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		resp, err := peer.client.AppendEntries(ctx, args)
		cancel()

		if err != nil {
			log.Printf("Fallo en AppendEntries a %s (intento %d): %v", peer.Address, i+1, err)
			time.Sleep(baseDelay * time.Duration(1<<i)) // Backoff exponencial
			continue
		}

		// Manejar término mayor en respuesta
		if resp.Term > int64(rf.currentTerm) {
			rf.currentTerm = int(resp.Term)
			rf.state = Follower
			rf.votedFor = -1
			rf.saveState()
			return fmt.Errorf("actualización de término a %d, nodo convertido a follower", rf.currentTerm)
		}

		// Actualizar indices según respuesta
		if resp.Success {
			rf.nextIndex[peer.ID] = nextIdx + len(entries)
			rf.matchIndex[peer.ID] = rf.nextIndex[peer.ID] - 1
			rf.saveState()
			log.Printf("Sincronización exitosa con %s. nextIndex: %d, matchIndex: %d",
				peer.Address, rf.nextIndex[peer.ID], rf.matchIndex[peer.ID])
			return nil
		} else {
			// Retroceder nextIndex y reintentar
			rf.nextIndex[peer.ID] = max(1, rf.nextIndex[peer.ID]-1)
			log.Printf("Sincronización fallida con %s. nextIndex retrocedido a %d",
				peer.Address, rf.nextIndex[peer.ID])
			break // Salir del bucle para permitir nueva llamada
		}
	}

	// Persistir cambios incluso en fallo
	rf.saveState()
	return fmt.Errorf("no se pudo sincronizar con %s después de %d intentos", peer.Address, maxRetries)
}

func (rf *Raft) updateCommitIndex() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	newCommitIndex := rf.commitIndex
	start := rf.lastIncludedIndex + 1
	if start < 0 {
		start = 0
	}

	// 🔄 CAMBIO: Iterar sobre el rango absoluto del log
	for n := start; n <= rf.lastIncludedIndex+len(rf.log); n++ {
		adjustedIndex := n - rf.lastIncludedIndex

		if adjustedIndex < 0 || adjustedIndex >= len(rf.log) {
			continue
		}

		if rf.log[adjustedIndex].Term != rf.currentTerm {
			continue
		}

		count := 1
		for _, peer := range rf.peers {
			if rf.matchIndex[peer.ID] >= n {
				count++
			}
		}

		if count > len(rf.peers)/2 && n > newCommitIndex {
			newCommitIndex = n
		}
	}

	if newCommitIndex > rf.commitIndex {
		rf.commitIndex = newCommitIndex
		go func() {
			rf.applyLogs()
			rf.saveState() // Persistir cambios tras operación exitosa
		}()
	}
}

func (rf *Raft) takeSnapshot() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.commitIndex <= rf.lastIncludedIndex {
		return
	}

	// 1. Actualizar índices del snapshot
	rf.lastIncludedIndex = rf.commitIndex
	rf.lastIncludedTerm = rf.log[rf.commitIndex].Term

	// 2. Crear snapshot con los datos correctos
	snapshot := rf.createSnapshot()

	// 3. Truncar log (conservar solo la entrada dummy)
	rf.log = []LogEntry{{Term: rf.lastIncludedTerm}}

	// 4. Guardar y enviar
	rf.saveSnapshot(snapshot)
	for _, peer := range rf.peers {
		go rf.sendSnapshot(peer, snapshot)
	}
}

func (rf *Raft) createSnapshot() []byte {
	// Serializar estado (ej: sistema de archivos)
	state := map[string]interface{}{
		"lastIncludedIndex": rf.lastIncludedIndex,
		"lastIncludedTerm":  rf.lastIncludedTerm,
		"data":              rf.snapshot,
	}
	snapshot, _ := json.Marshal(state)
	return snapshot
}

func (rf *Raft) sendSnapshot(peer *Peer, snapshot []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := peer.client.InstallSnapshot(ctx)
	if err != nil {
		log.Printf("Error al crear stream para snapshot: %v", err)
		return
	}

	chunkSize := 512 * 1024 // 512 KB por chunk
	totalChunks := (len(snapshot) + chunkSize - 1) / chunkSize

	for i := 0; i < totalChunks; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(snapshot) {
			end = len(snapshot)
		}

		chunk := &pb.SnapshotChunk{
			Data:              snapshot[start:end],
			ChunkIndex:        int64(i),
			IsLast:            i == totalChunks-1,
			Term:              int64(rf.currentTerm),
			LeaderId:          int64(rf.id),
			LastIncludedIndex: int64(rf.lastIncludedIndex),
			LastIncludedTerm:  int64(rf.lastIncludedTerm),
		}

		if err := stream.Send(chunk); err != nil {
			log.Printf("Error enviando chunk %d: %v", i, err)
			return
		}
	}

	// Recibir ACK
	ack, err := stream.CloseAndRecv()
	if err != nil || !ack.Success {
		log.Printf("Snapshot fallido en peer %d", peer.ID)
	}
}

func (ns *NetworkService) InstallSnapshot(stream pb.RaftService_InstallSnapshotServer) error {
	var (
		buffer      bytes.Buffer
		lastIndex   int64
		lastTerm    int64
		currentTerm int64
	)

	// 1. Recibir y ensamblar chunks del snapshot
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("Error recibiendo chunk: %v", err)
			return err
		}

		// Capturar metadatos del primer chunk
		if chunk.ChunkIndex == 0 {
			lastIndex = chunk.LastIncludedIndex
			lastTerm = chunk.LastIncludedTerm
			currentTerm = chunk.Term
		}

		buffer.Write(chunk.Data)
	}

	ns.node.raft.mu.Lock()
	defer ns.node.raft.mu.Unlock()

	// 2. Validar término del líder
	if currentTerm < int64(ns.node.raft.currentTerm) {
		return stream.SendAndClose(&pb.SnapshotAck{Success: false})
	}

	// 3. Aplicar snapshot y persistir estado
	if err := ns.node.raft.applySnapshot(buffer.Bytes(), lastIndex, lastTerm); err != nil {
		log.Printf("Error aplicando snapshot: %v", err)
		return stream.SendAndClose(&pb.SnapshotAck{Success: false})
	}

	// 4. Sincronizar índices
	ns.node.raft.commitIndex = int(lastIndex)
	ns.node.raft.lastApplied = int(lastIndex)

	// 5. Persistir estado completo
	if err := ns.node.raft.saveState(); err != nil {
		log.Printf("Error guardando estado: %v", err)
	}

	return stream.SendAndClose(&pb.SnapshotAck{
		Success: true,
		Term:    int64(ns.node.raft.currentTerm),
	})
}

func (rf *Raft) applySnapshot(data []byte, index, term int64) error {
	rf.snapshotLock.Lock()
	defer rf.snapshotLock.Unlock()

	// 1. Validar versión del snapshot
	if index <= int64(rf.lastIncludedIndex) {
		return errors.New("snapshot obsoleto")
	}

	// 2. Estructura de datos del snapshot
	type SnapshotData struct {
		LastIncludedIndex int               `json:"lastIncludedIndex"`
		LastIncludedTerm  int               `json:"lastIncludedTerm"`
		Log               []LogEntry        `json:"log"`
		Files             map[string][]byte `json:"files"`
	}

	var snapshotData SnapshotData
	if err := json.Unmarshal(data, &snapshotData); err != nil {
		return fmt.Errorf("error deserializando snapshot: %v", err)
	}

	// 3. Actualizar estado de Raft
	rf.lastIncludedIndex = snapshotData.LastIncludedIndex
	rf.lastIncludedTerm = snapshotData.LastIncludedTerm
	rf.log = snapshotData.Log
	rf.commitIndex = rf.lastIncludedIndex
	rf.lastApplied = rf.lastIncludedIndex

	// 4. Restaurar sistema de archivos
	baseDir := os.Getenv("FILE_BASE_DIR")
	if baseDir == "" {
		baseDir = "/app/files"
	}

	for relPath, content := range snapshotData.Files {
		fullPath := filepath.Join(baseDir, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			continue
		}
		if err := os.WriteFile(fullPath, content, 0644); err != nil {
			log.Printf("Error restaurando archivo %s: %v", fullPath, err)
		}
	}

	// 5. Guardar snapshot en disco
	if err := rf.saveSnapshot(data); err != nil {
		return fmt.Errorf("error guardando snapshot: %v", err)
	}

	return nil
}

func (rf *Raft) saveSnapshot(data []byte) error {
	snapshotPath := filepath.Join(os.Getenv("RAFT_DATA_DIR"), fmt.Sprintf("node%d/snapshot.bin", rf.id))
	return os.WriteFile(snapshotPath, data, 0644)
}

func (rf *Raft) loadSnapshot() error {
	snapshotPath := filepath.Join(os.Getenv("RAFT_DATA_DIR"), fmt.Sprintf("node%d/snapshot.bin", rf.id))
	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		return err
	}

	// Extraer metadatos del snapshot
	type SnapshotMeta struct {
		LastIncludedIndex int `json:"lastIncludedIndex"`
		LastIncludedTerm  int `json:"lastIncludedTerm"`
	}

	var meta SnapshotMeta
	if err := json.Unmarshal(data, &meta); err == nil {
		// Aplicar snapshot con sus propios metadatos
		return rf.applySnapshot(data, int64(meta.LastIncludedIndex), int64(meta.LastIncludedTerm))
	}

	return fmt.Errorf("snapshot corrupto")
}
