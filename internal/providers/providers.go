// Package providers is the single place that lists every Runtime
// provider dev knows about, so command wiring (cmd/lang.go) can never
// register a different set.
package providers

import (
	"github.com/jpsdm/dev/internal/runtime"
	golang "github.com/jpsdm/dev/internal/runtime/go"
	"github.com/jpsdm/dev/internal/runtime/java"
	"github.com/jpsdm/dev/internal/runtime/node"
	"github.com/jpsdm/dev/internal/runtime/python"
)

// Register adds every known Runtime provider to m.
func Register(m *runtime.Manager) {
	m.Register(node.New())
	m.Register(java.New())
	m.Register(golang.New())
	m.Register(python.New())
}
