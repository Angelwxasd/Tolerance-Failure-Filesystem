# DOCKER
docker-compose down -v        # limpia datos previos
docker-compose build --no-cache
docker-compose up



## COMANDO PROTO
Para compilar nuevamente el file.proto:
protoc --go_out=. --go-grpc_out=.     --go_opt=paths=source_relative     --go-grpc_opt=paths=source_relative     file.proto



https://github.com/debajyotidasgupta/raft-consensus


## COMANDOS DEBUG CON HTTP
#### Verifica que el servidor HTTP inicia:
    docker-compose logs node1 | grep "Métricas HTTP"
// Debe mostrar: "[Nodo 1] Métricas HTTP en :8080"

#### Pueba de Métricas:
    curl http://localhost:8081/raft-state


## COMANDOS DOCKER
### Iniciar y Detener Contenedores
#### Reconstruir y Ejecutar todos los contenedores
    docker-compose down -v && docker-compose up --build

#### Iniciar todos los servicios en segundo plano
    docker-compose up -d

#### Ejecutar los 4 nodos
    docker-compose up --build

#### Detener y eliminar contenedores, redes y volúmenes
    docker-compose down -v

#### Reiniciar un servicio específico (ej: node1)
    docker-compose restart node1


### Construir Imágenes
#### Reconstruir todas las imágenes (ignorando caché)
    docker-compose build --no-cache

#### Reconstruir una imagen específica (ej: node1)
    docker-compose build node1

### Ver Logs

#### Ver logs en tiempo real de todos los servicios
    docker-compose logs -f

#### Ver logs de un servicio específico (ej: node1)
    docker-compose logs -f node1

#### Ver últimos 100 logs de node1 con timestamps
    docker-compose logs --tail=100 -t node1

### Acceder a Contenedores

#### Abrir una terminal en el contenedor node1
    docker exec -it so-final-node1-1 sh

#### Ejecutar un comando específico dentro del contenedor (ej: listar archivos)
    docker exec so-final-node1-1 ls /app/files

### Gestionar Red y Conectividad

#### Listar todos los volúmenes
    docker volume ls

#### Inspeccionar un volumen específico (ej: node1_data)
    docker volume inspect so-final_node1_data

#### Eliminar todos los volúmenes no utilizados
    docker volume prune

### Monitorear Recursos

##### Ver contenedores en ejecución
    docker ps

##### Ver consumo de recursos (CPU, memoria, red)
    docker stats

#### Ver estado de los servicios en Docker Compose
    docker-compose ps

### Depurar Conexiones gRPC

#### Usar grpcurl para probar servicios gRPC desde el host
    grpcurl -plaintext localhost:50051 list  # Listar servicios
    grpcurl -plaintext localhost:50051 proto.RaftService.GetMetrics

### Limpiar Recursos

#### Eliminar todos los contenedores, redes, volúmenes e imágenes
    docker-compose down --volumes --rmi all --remove-orphans

#### Eliminar imágenes huérfanas
    docker image prune -a