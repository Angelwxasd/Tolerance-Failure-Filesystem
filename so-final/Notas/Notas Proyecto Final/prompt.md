Estoy ejecutando mi programa en 4 contenedores de docker, al momento de ejecutarlos se conectan correctamente los nodos, pero  al tratar de crear un archivo, no aparece en ninguno de ellos, me parece que el error está en la lógica de los commit, ya que no pasan de cero, pero por favor analízalo, esto me aparece en la terminal:
❯❯ for i in {1..4}; do curl -s localhost:$((8080+i))/raft; done
id=1 state=Leader term=2 commit=0 applied=0
id=2 state=Follower term=2 commit=0 applied=0
id=3 state=Follower term=2 commit=0 applied=0
id=4 state=Follower term=2 commit=0 applied=0

╭─angelxx   󰉖 ~/fault-tolerance-filesystem-raftGrpc/so-final                  ( master)  ~2
╰─ ❯❯ grpcurl -plaintext -import-path proto -proto proto/file.proto \
  -d '{"dirname":"/app/files/docs"}' \
  localhost:50051 proto.RaftService/MkDir
{
  "success": true,
  "message": "directorio creado"
}

╭─angelxx   󰉖 ~/fault-tolerance-filesystem-raftGrpc/so-final                  ( master)  ~2
╰─ ❯❯ DATA=$(echo -n "hola\n" | base64)
grpcurl -plaintext -import-path proto -proto proto/file.proto \
  -d '{"filename":"/app/files/docs/hola.txt","content":"'"$DATA"'"}' \
  localhost:50051 proto.RaftService/TransferFile
{
  "success": true,
  "message": "transferencia replicada"
}

╭─angelxx   󰉖 ~/fault-tolerance-filesystem-raftGrpc/so-final                  ( master)  ~2
╰─ ❯❯ for n in 1 2 3 4; do
  echo "Nodo $n:"; docker compose exec -T node$n ls -R /app/files/docs; echo
done
Nodo 1:
ls: /app/files/docs: No such file or directory


Nodo 2:
ls: /app/files/docs: No such file or directory


Nodo 3:
ls: /app/files/docs: No such file or directory


Nodo 4:
ls: /app/files/docs: No such file or directory



╭─angelxx   󰉖 ~/fault-tolerance-filesystem-raftGrpc/so-final                  ( master)  ~2
╰─ ❯❯ grpcurl -plaintext -import-path proto -proto proto/file.proto \
  -d '{"dirname":"/app/files/docs"}' \
  localhost:50051 proto.RaftService/MkDir
{
  "success": true,
  "message": "directorio creado"
}

╭─angelxx   󰉖 ~/fault-tolerance-filesystem-raftGrpc/so-final                  ( master)  ~2
╰─ ❯❯ DATA=$(echo -n "hola\n" | base64)
grpcurl -plaintext -import-path proto -proto proto/file.proto \
  -d '{"filename":"/app/files/docs/hola.txt","content":"'"$DATA"'"}' \
  localhost:50051 proto.RaftService/TransferFile
{
  "success": true,
  "message": "transferencia replicada"
}

╭─angelxx   󰉖 ~/fault-tolerance-filesystem-raftGrpc/so-final                  ( master)  ~2
╰─ ❯❯ for n in 1 2 3 4; do
  echo "Nodo $n:"; docker compose exec -T node$n ls -R /app/files/docs; echo
done
Nodo 1:
ls: /app/files/docs: No such file or directory


Nodo 2:
ls: /app/files/docs: No such file or directory


Nodo 3:
ls: /app/files/docs: No such file or directory


Nodo 4:
ls: /app/files/docs: No such file or directory



╭─angelxx   󰉖 ~/fault-tolerance-filesystem-raftGrpc/so-final                  ( master)  ~2
╰─ ❯❯ for i in {1..4}; do curl -s localhost:$((8080+i))/raft; done
id=1 state=Leader term=2 commit=0 applied=0
id=2 state=Follower term=2 commit=0 applied=0
id=3 state=Follower term=2 commit=0 applied=0
id=4 state=Follower term=2 commit=0 applied=0

╭─angelxx   󰉖 ~/fault-tolerance-filesystem-raftGrpc/so-final                  ( master)  ~2
╰─ ❯❯ grpcurl -plaintext -import-path proto -proto proto/file.proto \
  -d '{"dirname":"/app/files/docs"}' \
  localhost:50051 proto.RaftService/MkDir
{
  "success": true,
  "message": "directorio creado"
}

╭─angelxx   󰉖 ~/fault-tolerance-filesystem-raftGrpc/so-final                  ( master)  ~2
╰─ ❯❯ DATA=$(echo -n "hola\n" | base64)
grpcurl -plaintext -import-path proto -proto proto/file.proto \
  -d '{"filename":"/app/files/docs/hola.txt","content":"'"$DATA"'"}' \
  localhost:50051 proto.RaftService/TransferFile
{
  "success": true,
  "message": "transferencia replicada"
}

╭─angelxx   󰉖 ~/fault-tolerance-filesystem-raftGrpc/so-final                  ( master)  ~2
╰─ ❯❯ for n in 1 2 3 4; do
  echo "Nodo $n:"; docker compose exec -T node$n ls -R /app/files/docs; echo
done
Nodo 1:
ls: /app/files/docs: No such file or directory


Nodo 2:
ls: /app/files/docs: No such file or directory


Nodo 3:
ls: /app/files/docs: No such file or directory


Nodo 4:
ls: /app/files/docs: No such file or directory



╭─angelxx   󰉖 ~/fault-tolerance-filesystem-raftGrpc/so-final                  ( master)  ~2
╰─ ❯❯ for i in {1..4}; do curl -s localhost:$((8080+i))/raft; done
id=1 state=Leader term=2 commit=0 applied=0
id=2 state=Follower term=2 commit=0 applied=0
id=3 state=Follower term=2 commit=0 applied=0
id=4 state=Follower term=2 commit=0 applied=0

A qué se podrá deber?
Por favor analiza mis códigos a profundidad, tómate el tiempo necesario.




### RESOLVER 
Pasos para corregir el problema  

    Corrige el cálculo del quórum  en quorumSize para asegurar que el líder alcance el quórum.
    Aumenta el timeout  de AppendEntries a 2 segundos y agrega más reintentos.
    Verifica los logs de Raft  para asegurar que las entradas se replican correctamente.
    Monta un volumen compartido  para /app/files en Docker Compose.
    Agrega registros de depuración  en applyEntries y applyLogs para identificar errores silenciosos.
    Prueba con 3 nodos  para simplificar el clúster y verificar que el quórum se alcanza.
     

// ---------------------------------------------//

1. Dime cómo resolver el fallo 2, detalladamente, indicándome la ubicación exacta en la que hacer los cambios:2. Fallo en la alineación de logs (applyEntries)  

En la función applyEntries de raft.txt, si el índice o término previo no coincide, el log se trunca y se rechaza la entrada: 
go
⌄
if rf.log[prev].Term != int(req.PrevLogTerm) {
    rf.log = rf.log[:prev]
    return false
}
Problema: 
Si los nodos no tienen logs alineados (por ejemplo, porque no han replicado entradas anteriores), las nuevas entradas se rechazan, lo que impide que el líder avance el commitIndex. 

Solución: 
Agrega registros de depuración para verificar si los logs están alineados y si las entradas se rechazan. Además, asegúrate de que el líder envíe entradas válidas al inicializar nextIndex correctamente. 

2. Cómo puedo resolver este fallo también? Como lo anterior, mencioname detalladamente:
4. Errores en la aplicación de logs (applyLogs)  

En applyLogs de raft.txt, aunque se proponen comandos, si hay errores al crear directorios o archivos, estos no se registran: 
go
⌄
if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
    log.Printf("mkdir %s: %v", filepath.Dir(target), err)
    continue
}
Problema: 
Si hay errores (por ejemplo, permisos insuficientes), los cambios no se aplican, lo que da la falsa impresión de que no ocurrió nada. 

Solución: 
Valida los permisos de los directorios y asegúrate de que /app/files esté correctamente montado en cada contenedor (verifica el Docker Compose). 

3. El cambio en Docker compose no cambiaría la estructura de mi programa teniendo que hacer cambios en todo el código?
Configuración de Docker Compose  

En docker-compose.txt, los volúmenes de /app/files y /app/raft-data son específicos por contenedor, pero no se comparten entre ellos. Esto significa que los cambios en un nodo no se reflejan en otros. 

Problema: 
Aunque Raft replica los logs, los archivos físicos no se sincronizan porque cada nodo tiene su propia copia de /app/files. 

Solución: 
Usa un volumen compartido para /app/files (ejemplo con volumes externos): 

volumes:
  shared_files:
    driver: local
    type: volume
    name: shared_files

services:
  node1:
    volumes:
      - shared_files:/app/files


4. Cómo podría resolver este problema y qué repercusiones tendría en mi código?
6. Validación de rutas en FileManager  

En network.txt, la función ValidatePath permite rutas absolutas pero no verifica que estén dentro de /app/files: 
return filepath.IsAbs(clean) && strings.HasPrefix(clean, fm.baseDir)
Problema: 
Si fm.baseDir es /app/files, pero la ruta solicitada es /app/files/../.., podría escapar del directorio base. 

Solución: 
Agrega validación adicional para evitar path traversal: 
if !strings.HasPrefix(clean, fm.baseDir) {
    return false
}
