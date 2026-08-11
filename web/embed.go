package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed index.html app.css app.js
var assets embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(assets, ".")
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || r.URL.Path == "/" {
			data, err := assets.ReadFile("index.html")
			if err != nil {
				http.Error(w, "admin UI unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		files.ServeHTTP(w, r)
	})
}
