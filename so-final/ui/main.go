package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	pb "so-final/proto" // ← importa tu stub

	"google.golang.org/grpc"
)

const addr = "localhost:50051"

func main() {
	/* --- gRPC dial --- */
	conn, err := grpc.Dial(addr, grpc.WithInsecure())
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	cli := pb.NewRaftServiceClient(conn)

	/* --- Fyne --- */
	a := app.New()
	w := a.NewWindow("Raft FS")
	w.Resize(fyne.NewSize(460, 360))

	pathEntry := widget.NewEntry()
	pathEntry.SetPlaceHolder("/abs/path/o/relativo")

	logBox := widget.NewMultiLineEntry()
	logBox.SetPlaceHolder("Salida…")
	logBox.Wrapping = fyne.TextWrapWord
	logBox.Disable()

	/* --- botones --- */
	btn := func(label string, fn func(string)) *widget.Button {
		return widget.NewButton(label, func() {
			p := pathEntry.Text
			if p == "" {
				dialog.ShowError(fmt.Errorf("ruta vacía"), w)
				return
			}
			fn(p)
		})
	}
	mkdir := btn("MkDir", func(p string) {
		resp, err := cli.MkDir(context.Background(), &pb.MkDirRequest{Dirname: p})
		logBox.SetText(respMsg(resp.GetMessage(), err))
	})
	rmdir := btn("RmDir", func(p string) {
		resp, err := cli.RemoveDir(context.Background(), &pb.RemoveDirRequest{Dirname: p})
		logBox.SetText(respMsg(resp.GetMessage(), err))
	})
	delbtn := btn("Delete File", func(p string) {
		resp, err := cli.DeleteFile(context.Background(), &pb.DeleteRequest{Filename: p})
		logBox.SetText(respMsg(resp.GetMessage(), err))
	})
	// 🔍 Nuevo botón “List Dir”
	listBtn := btn("List Dir", func(p string) {
		resp, err := cli.ListDir(context.Background(), &pb.DirRequest{Path: p})
		if err != nil {
			logBox.SetText(respMsg("", err))
			return
		}
		logBox.SetText("✅\n" + strings.Join(resp.Names, "\n"))
	})

	/* --- subir archivo (abre file-chooser) --- */
	upload := widget.NewButton("Transfer File", func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, e error) {
			if e != nil || r == nil {
				return
			}
			defer r.Close()

			// lee el archivo completo → bytes crudos
			b, _ := ioutil.ReadAll(r)

			// ruta destino dentro del directorio elegido en la GUI
			name := filepath.Base(r.URI().Path())
			target := filepath.Join(pathEntry.Text, name)

			// gRPC: envía los bytes directamente (sin codificar)
			resp, err := cli.TransferFile(context.Background(),
				&pb.FileData{Filename: target, Content: b})

			logBox.SetText(respMsg(resp.GetMessage(), err))
		}, w)

		fd.Show()
	})

	/* --- layout --- */
	btnRow := container.NewGridWithColumns(2, mkdir, rmdir, upload, delbtn, listBtn)
	w.SetContent(container.NewVBox(
		widget.NewLabel("Ruta destino / fichero:"),
		pathEntry,
		btnRow,
		widget.NewLabel("Log:"),
		logBox,
	))
	w.ShowAndRun()
}

func respMsg(ok string, err error) string {
	if err != nil {
		return "⛔ " + err.Error()
	}
	return "✅ " + ok
}
