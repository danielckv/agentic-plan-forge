package sample

import (
	"fmt"
	"net/http"
)

// MaxRetries bounds retries.
const MaxRetries = 3

var defaultName = "sample"

// Greeter greets people.
type Greeter interface {
	Greet(name string) string
}

// Server serves requests.
type Server struct {
	Port int
}

// Foo adds its operands.
func Foo(a, b int) int {
	return a + b
}

func helper(x string) string {
	return x
}

// Start registers routes and serves.
func (s *Server) Start(port int) error {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	return nil
}
