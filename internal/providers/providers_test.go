package providers

import (
	"testing"

	"github.com/jpsdm/dev/internal/runtime"
)

func TestRegister_RegistersNode(t *testing.T) {
	t.Parallel()
	m := runtime.NewManager()
	Register(m)

	r, ok := m.Get("node")
	if !ok {
		t.Fatal(`Get("node") ok = false after Register(), want true`)
	}
	if r.Name() != "node" {
		t.Errorf(`Get("node").Name() = %q, want "node"`, r.Name())
	}
}

func TestRegister_RegistersJava(t *testing.T) {
	t.Parallel()
	m := runtime.NewManager()
	Register(m)

	r, ok := m.Get("java")
	if !ok {
		t.Fatal(`Get("java") ok = false after Register(), want true`)
	}
	if r.Name() != "java" {
		t.Errorf(`Get("java").Name() = %q, want "java"`, r.Name())
	}
}

func TestRegister_RegistersGo(t *testing.T) {
	t.Parallel()
	m := runtime.NewManager()
	Register(m)

	r, ok := m.Get("go")
	if !ok {
		t.Fatal(`Get("go") ok = false after Register(), want true`)
	}
	if r.Name() != "go" {
		t.Errorf(`Get("go").Name() = %q, want "go"`, r.Name())
	}
}

func TestRegister_RegistersPython(t *testing.T) {
	t.Parallel()
	m := runtime.NewManager()
	Register(m)

	r, ok := m.Get("python")
	if !ok {
		t.Fatal(`Get("python") ok = false after Register(), want true`)
	}
	if r.Name() != "python" {
		t.Errorf(`Get("python").Name() = %q, want "python"`, r.Name())
	}
}
