package app

import (
	"context"
	"time"

	pb "so-final/backend/proto" // ¡mismo proto que usa el nodo!

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type App struct {
	client pb.RaftServiceClient
}

func NewApp() *App { return &App{} }

// OnStartup se llama una vez al abrir la ventana.
func (a *App) Startup(ctx context.Context) {
	conn, err := grpc.DialContext(ctx,
		"localhost:50051", // ⚙️  se puede volver variable
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(), grpc.WithTimeout(3*time.Second),
	)
	if err != nil {
		panic(err)
	}
	a.client = pb.NewRaftServiceClient(conn)
}

// Ejemplo: listar un directorio del líder
func (a *App) ListDir(path string) ([]string, error) {
	reply, err := a.client.ListDir(context.TODO(), &pb.DirRequest{Path: path})
	if err != nil {
		return nil, err
	}
	return reply.Names, nil
}

// Ejemplo: transferir archivo
func (a *App) Transfer(dst, contentBase64 string) (bool, string) {
	resp, err := a.client.TransferFile(context.TODO(), &pb.FileData{
		Filename: dst, Content: []byte(contentBase64),
	})
	if err != nil {
		return false, err.Error()
	}
	return resp.Success, resp.Message
}
