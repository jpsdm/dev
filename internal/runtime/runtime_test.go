package runtime

import (
	"context"
	"sync"
	"testing"
)

type stubRuntime struct {
	name string
}

func (s *stubRuntime) Name() string { return s.name }
func (s *stubRuntime) ListRemoteVersions(ctx context.Context) ([]Version, error) {
	return nil, nil
}
func (s *stubRuntime) ListInstalledVersions() ([]Version, error) { return nil, nil }
func (s *stubRuntime) CurrentVersion() (*Version, error)         { return nil, nil }
func (s *stubRuntime) Install(ctx context.Context, name string) error {
	return nil
}
func (s *stubRuntime) Uninstall(name string) error { return nil }
func (s *stubRuntime) Activate(name string) error  { return nil }
func (s *stubRuntime) BinaryPath(versionDir, binName string) (string, error) {
	return "", nil
}
func (s *stubRuntime) BinDir(versionDir string) (string, error) {
	return "", nil
}

func TestValidVersionName_RejectsEmptyDotDotAndSeparators(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", ".", "..", "a/b", "a\\b", "../escape"} {
		if err := ValidVersionName(name); err == nil {
			t.Errorf("ValidVersionName(%q) returned nil error, want an error", name)
		}
	}
}

func TestValidVersionName_AcceptsOrdinaryNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"22", "3.12", "1.24", "lts"} {
		if err := ValidVersionName(name); err != nil {
			t.Errorf("ValidVersionName(%q) returned error: %v, want nil", name, err)
		}
	}
}

func TestManager_RegisterAndGet(t *testing.T) {
	t.Parallel()
	m := NewManager()
	m.Register(&stubRuntime{name: "node"})

	got, ok := m.Get("node")
	if !ok {
		t.Fatal(`Get("node") ok = false, want true`)
	}
	if got.Name() != "node" {
		t.Errorf(`Get("node").Name() = %q, want "node"`, got.Name())
	}
}

func TestManager_GetUnknown(t *testing.T) {
	t.Parallel()
	m := NewManager()

	_, ok := m.Get("ruby")
	if ok {
		t.Error(`Get("ruby") ok = true, want false (nothing registered)`)
	}
}

func TestManager_ConcurrentRegisterGetNamesIsRaceFree(t *testing.T) {
	m := NewManager()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			m.Register(&stubRuntime{name: "lang"})
		}(i)
		go func() {
			defer wg.Done()
			m.Get("lang")
		}()
		go func() {
			defer wg.Done()
			m.Names()
		}()
	}
	wg.Wait()
}

func TestManager_NamesSorted(t *testing.T) {
	t.Parallel()
	m := NewManager()
	m.Register(&stubRuntime{name: "python"})
	m.Register(&stubRuntime{name: "node"})
	m.Register(&stubRuntime{name: "go"})

	got := m.Names()
	want := []string{"go", "node", "python"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
