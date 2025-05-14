## PARA EJECUTAR EL SCRIPT sh
chmod +x ConfigNodo1

Ejecuta:

./ConfigNodo1


2 · ConfigNodo2, ConfigNodo3, ConfigNodo4

Copia el archivo y ajusta solo las líneas que cambian:
Script	NODE_ID	NODE_ADDR	RAFT_DATA_DIR	FILE_BASE_DIR
ConfigNodo2	2	192.168.1.11:50051	/srv/raft2	/srv/files2
ConfigNodo3	3	192.168.1.12:50051	/srv/raft3	/srv/files3
ConfigNodo4	4	192.168.1.13:50051	/srv/raft4	/srv/files4
La línea PEER_ADDRS= es la misma en los cuatro (porque siempre debes listar a los otros 3 nodos).


