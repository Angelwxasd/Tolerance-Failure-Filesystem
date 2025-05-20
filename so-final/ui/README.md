Esta es la interfaz gráfica, estamos usando Fyne.

Para ejecutar el programa con interfaz gráfica debemos de ejecutar los siguientes comandos:
    cd ui
    env WLR_NO_HARDWARE_CURSORS=1 XDG_SESSION_TYPE=x11 go run .

Eso en cuanto a arch linux, en cuanto a otra distro (ubuntu, mint, etc), podemos usar:

    cd ui
    go run .

Esto ejecuta la interfaz, si es que no aparece por favor investigar si utiliza Gnome, X11 o Wayland, para conseguir el comando apropiado.