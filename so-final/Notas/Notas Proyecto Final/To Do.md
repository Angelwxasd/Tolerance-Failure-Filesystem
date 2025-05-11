
5. Lógica de reconexión de peers (network.go):

Problema:

    El código de Peer.Reconnect() no actualiza dinámicamente la dirección si un nodo cambia de IP.

Solución:
Implementar actualización de DNS en cada intento de reconexión:
go

func (p *Peer) Reconnect() {
    // Resolver dirección actualizada usando DNS interno de Docker
    updatedAddr, err := net.ResolveTCPAddr("tcp", p.Address)
    if err == nil {
        p.Address = updatedAddr.String() // Actualizar dirección
    }
    // Resto de la lógica de reconexión...
}


6. Corrección de deadlocks en Raft (raft.go):

Problema:

    En sendHeartbeats(), el uso de defer rf.mu.Unlock() dentro de goroutines puede bloquear el mutex principal.

Solución:
Liberar el mutex antes de lanzar goroutines y usar copias locales:
go

func (rf *Raft) sendHeartbeats() {
    // ...
    rf.mu.Lock()
    currentTerm := rf.currentTerm
    savedPeers := make([]*Peer, len(rf.peers))
    copy(savedPeers, rf.peers) // Copiar peers
    rf.mu.Unlock() // Liberar antes de goroutines

    for _, peer := range savedPeers {
        go func(p *Peer) {
            // Usar currentTerm y savedPeers aquí
        }(peer)
    }
}


