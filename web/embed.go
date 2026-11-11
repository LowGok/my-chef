package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var assets embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	mux := http.NewServeMux()
	mux.Handle("GET /app/", files)
	for _, route := range []string{"/app"} {
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/app/", http.StatusSeeOther) })
	}
	for _, route := range []string{"/landing", "/landing/"} {
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/", http.StatusSeeOther) })
	}
	mux.Handle("GET /", files)
	return mux
}
