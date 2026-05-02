package server

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"go-graph-doc/internal/data"
)

//go:embed index.html
var indexHTML string

const dataPlaceholder = "/*__GRAPH_DATA__*/"

type Server struct {
	addr       string
	data       *data.GraphData
	rendered   string
	dataDir    string
	userHeader string
}

func New(addr string, graphData *data.GraphData, dataDir, userHeader string) *Server {
	return &Server{addr: addr, data: graphData, dataDir: dataDir, userHeader: userHeader}
}

func (s *Server) ListenAndServe() error {
	rendered, err := renderHTML(s.data)
	if err != nil {
		return fmt.Errorf("rendering template: %w", err)
	}
	s.rendered = rendered

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/data.json", s.handleJSON)
	if s.userHeader != "" {
		mux.HandleFunc("/api/config", s.makeStateHandler("config"))
		mux.HandleFunc("/api/graph", s.makeStateHandler("graph"))
	}

	return http.ListenAndServe(s.addr, mux)
}

func (s *Server) makeStateHandler(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.Header.Get(s.userHeader)
		if username == "" {
			http.Error(w, "forbidden: missing user header", http.StatusForbidden)
			return
		}
		var stateFile string
		if kind == "config" {
			// config is global across projects
			stateFile = filepath.Join(s.dataDir, sanitizePath(username)+".config.json")
		} else {
			project := s.data.ModulePath
			if project == "" {
				project = "default"
			}
			stateFile = filepath.Join(s.dataDir, sanitizePath(username), sanitizePath(project)+".graph.json")
		}

		switch r.Method {
		case http.MethodGet:
			body, err := os.ReadFile(stateFile)
			if os.IsNotExist(err) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte("{}"))
				return
			}
			if err != nil {
				http.Error(w, "error reading state", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(body)
		case http.MethodPut:
			if err := os.MkdirAll(filepath.Dir(stateFile), 0755); err != nil {
				http.Error(w, "error creating directory", http.StatusInternalServerError)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
			if err != nil {
				http.Error(w, "error reading body", http.StatusInternalServerError)
				return
			}
			if err := os.WriteFile(stateFile, body, 0644); err != nil {
				http.Error(w, "error writing state", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func sanitizePath(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "..", "_")
	if s == "" || s == "." {
		return "_"
	}
	return s
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, s.rendered)
}

func (s *Server) handleJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(s.data)
}

func RenderHTML(w io.Writer, graphData *data.GraphData) error {
	rendered, err := renderHTML(graphData)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, rendered)
	return err
}

func renderHTML(graphData *data.GraphData) (string, error) {
	jsonBytes, err := json.Marshal(graphData)
	if err != nil {
		return "", fmt.Errorf("marshaling data: %w", err)
	}
	if !strings.Contains(indexHTML, dataPlaceholder) {
		return "", fmt.Errorf("template missing placeholder %q", dataPlaceholder)
	}
	return strings.Replace(indexHTML, dataPlaceholder, string(jsonBytes), 1), nil
}
