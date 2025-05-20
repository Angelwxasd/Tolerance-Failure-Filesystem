# 6.1 Crear un directorio "testdir"
grpcurl -plaintext -d '{"dirname":"testdir"}' localhost:50051 proto.RaftService/MkDir

# 6.2 Transferir un fichero
grpcurl -plaintext -d '{"filename":"testdir/hello.txt","content":"SGVsbG8gUmFm*dA=="}' localhost:50051 proto.RaftService/TransferFile

# 6.3 Listar ficheros (read-only, no pasa por Raft)
grpcurl -plaintext -d '{"path":"testdir"}' localhost:50051 proto.RaftService/ListDir

# 6.4 Borrar fichero
grpcurl -plaintext -d '{"filename":"testdir/hello.txt"}' localhost:50051 proto.RaftService/DeleteFile

# 6.5 Eliminar directorio
grpcurl -plaintext -d '{"dirname":"testdir"}' localhost:50051 proto.RaftService/RemoveDir
