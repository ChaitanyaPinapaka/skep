package main

import "fmt"

// Server handles HTTP requests.
type Server struct {
	Port int
	Host string
}

// NewServer creates a new server instance.
func NewServer(host string, port int) *Server {
	return &Server{Host: host, Port: port}
}

// Start starts the server.
func (s *Server) Start() error {
	fmt.Printf("listening on %s:%d\n", s.Host, s.Port)
	return nil
}

// Handler is the HTTP handler interface.
type Handler interface {
	ServeHTTP(w ResponseWriter, r *Request)
}

func main() {
	s := NewServer("localhost", 8080)
	s.Start()
}
