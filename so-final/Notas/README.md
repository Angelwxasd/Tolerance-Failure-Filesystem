# COMANDOS Y AJUSTES

## 1. Preparar el entorno
Primero debemos de revisar si tenemos goland:
    go version

En la carpeta donde se encuentra "go.mod", ejecuta el siguiente comando:
    go mod tidy

## 2. Compilar el binario (Linux)
En donde se encuentre nuestro archivo main.go, ejecutamos lo siguiente:
    go build -o nodo main.go
Es importante ejecutarlo de la forma anterior, ya que generará un archivo "nodo".

## 3. Configurar y lanzar el nodo
Le damos permisos de ejecución (+X)
    chmod +x configNodo1.sh
Después de eso lo cargamos en el shell:
    source ./configNodo1.sh


# Terminal 2 (misma máquina)
Antes que nada, debe de reconocer la variable: 
    export FILE_BASE_DIR=/home/angelxx/srv/files1


//Después de escribir lo anterior en la terminal, podrás realizar operaciones con archivos!!!!
Algunos ejemplos son los siguientes (para más información visita OperacionesArchivos.md):

    grpcurl -plaintext \
    -d '{"dirname":"testdir"}' \
    localhost:50051 proto.RaftService/MkDir

    grpcurl -plaintext \
    -d '{"filename":"testdir/hello.txt","content":"SGVsbG8gd29ybGQ="}' \
    localhost:50051 proto.RaftService/TransferFile

