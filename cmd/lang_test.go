// lang's tests mutate the shared package-level langManager, so none of
// them use t.Parallel() against each other.
package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	devruntime "github.com/jpsdm/dev/internal/runtime"
)

type stubRuntime struct {
	name             string
	remote           []devruntime.Version
	installed        []devruntime.Version
	current          *devruntime.Version
	installErr       error
	uninstallErr     error
	activateErr      error
	listRemoteErr    error
	listInstalledErr error
	currentErr       error

	installCalled     bool
	installCalledWith string
	uninstallCalled   bool
	activateCalled    bool
}

func (s *stubRuntime) Name() string { return s.name }
func (s *stubRuntime) ListRemoteVersions(ctx context.Context) ([]devruntime.Version, error) {
	return s.remote, s.listRemoteErr
}
func (s *stubRuntime) ListInstalledVersions() ([]devruntime.Version, error) {
	return s.installed, s.listInstalledErr
}
func (s *stubRuntime) CurrentVersion() (*devruntime.Version, error) { return s.current, s.currentErr }
func (s *stubRuntime) Install(ctx context.Context, name string) error {
	s.installCalled = true
	s.installCalledWith = name
	return s.installErr
}
func (s *stubRuntime) Uninstall(name string) error {
	s.uninstallCalled = true
	return s.uninstallErr
}
func (s *stubRuntime) Activate(name string) error {
	s.activateCalled = true
	return s.activateErr
}
func (s *stubRuntime) BinaryPath(versionDir, binName string) (string, error) {
	return "", nil
}
func (s *stubRuntime) BinDir(versionDir string) (string, error) {
	return "", nil
}

func withStubManager(t *testing.T, stubs ...*stubRuntime) {
	t.Helper()
	orig := langManager
	m := devruntime.NewManager()
	for _, s := range stubs {
		m.Register(s)
	}
	langManager = m
	t.Cleanup(func() { langManager = orig })
}

func TestLangInstall_UnknownLanguageErrors(t *testing.T) {
	withStubManager(t, &stubRuntime{name: "node"})

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang", "install", "ruby", "3"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected an error installing an unregistered language")
	}
	if !strings.Contains(err.Error(), "unknown language: ruby") {
		t.Errorf(`error = %v, want it to mention "unknown language: ruby"`, err)
	}
}

func TestLangInstall_RoutesToTheNamedRuntime(t *testing.T) {
	nodeStub := &stubRuntime{name: "node"}
	pythonStub := &stubRuntime{name: "python"}
	withStubManager(t, nodeStub, pythonStub)

	rootCmd.SetOut(new(bytes.Buffer))
	rootCmd.SetErr(new(bytes.Buffer))
	rootCmd.SetArgs([]string{"lang", "install", "python", "3.12"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute() returned error: %v", err)
	}

	if !pythonStub.installCalled || pythonStub.installCalledWith != "3.12" {
		t.Errorf("python stub: installCalled=%v installCalledWith=%q, want called with \"3.12\"", pythonStub.installCalled, pythonStub.installCalledWith)
	}
	if nodeStub.installCalled {
		t.Error("node stub's Install() was called for a command targeting python")
	}
}

func TestLangCurrent_NoArgsListsEveryRegisteredLanguage(t *testing.T) {
	withStubManager(t,
		&stubRuntime{name: "node", current: &devruntime.Version{Name: "22"}},
		&stubRuntime{name: "python", current: nil},
	)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute() returned error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "node: 22") {
		t.Errorf(`output = %q, want it to mention "node: 22"`, got)
	}
	if !strings.Contains(got, "python: (none active)") {
		t.Errorf(`output = %q, want it to mention "python: (none active)"`, got)
	}
}

func TestLangInstall_RejectsPathTraversalVersionName(t *testing.T) {
	nodeStub := &stubRuntime{name: "node"}
	withStubManager(t, nodeStub)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang", "install", "node", ".."})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal(`expected an error installing version ".."`)
	}
	if nodeStub.installCalled {
		t.Error("Install() was called despite an invalid version name")
	}
}

func TestLangUninstall_RejectsPathTraversalVersionName(t *testing.T) {
	nodeStub := &stubRuntime{name: "node"}
	withStubManager(t, nodeStub)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang", "uninstall", "node", "../escape"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal(`expected an error uninstalling version "../escape"`)
	}
	if nodeStub.uninstallCalled {
		t.Error("Uninstall() was called despite an invalid version name")
	}
}

func TestLangUse_RejectsEmptyVersionName(t *testing.T) {
	nodeStub := &stubRuntime{name: "node"}
	withStubManager(t, nodeStub)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang", "use", "node", ""})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected an error activating an empty version name")
	}
	if nodeStub.activateCalled {
		t.Error("Activate() was called despite an invalid version name")
	}
}

func TestLangInstall_WrapsRuntimeErrorWithLanguageName(t *testing.T) {
	nodeStub := &stubRuntime{name: "node", installErr: errors.New("boom")}
	withStubManager(t, nodeStub)

	rootCmd.SetOut(new(bytes.Buffer))
	rootCmd.SetErr(new(bytes.Buffer))
	rootCmd.SetArgs([]string{"lang", "install", "node", "22"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected an error when Install() fails")
	}
	if !strings.Contains(err.Error(), "node") || !strings.Contains(err.Error(), "boom") {
		t.Errorf(`error = %q, want it to mention both "node" and the underlying "boom" error`, err.Error())
	}
}

func TestLangUninstall_WrapsRuntimeErrorWithLanguageName(t *testing.T) {
	nodeStub := &stubRuntime{name: "node", uninstallErr: errors.New("boom")}
	withStubManager(t, nodeStub)

	rootCmd.SetOut(new(bytes.Buffer))
	rootCmd.SetErr(new(bytes.Buffer))
	rootCmd.SetArgs([]string{"lang", "uninstall", "node", "22"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected an error when Uninstall() fails")
	}
	if !strings.Contains(err.Error(), "node") || !strings.Contains(err.Error(), "boom") {
		t.Errorf(`error = %q, want it to mention both "node" and the underlying "boom" error`, err.Error())
	}
}

func TestLangUse_WrapsRuntimeErrorWithLanguageName(t *testing.T) {
	nodeStub := &stubRuntime{name: "node", activateErr: errors.New("boom")}
	withStubManager(t, nodeStub)

	rootCmd.SetOut(new(bytes.Buffer))
	rootCmd.SetErr(new(bytes.Buffer))
	rootCmd.SetArgs([]string{"lang", "use", "node", "22"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected an error when Activate() fails")
	}
	if !strings.Contains(err.Error(), "node") || !strings.Contains(err.Error(), "boom") {
		t.Errorf(`error = %q, want it to mention both "node" and the underlying "boom" error`, err.Error())
	}
}

func TestLangList_OneProviderErrorDoesNotSuppressTheOthers(t *testing.T) {
	withStubManager(t,
		&stubRuntime{name: "node", listRemoteErr: errors.New("network down")},
		&stubRuntime{name: "python", remote: []devruntime.Version{{Name: "3.12"}}},
	)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang", "list"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected a non-nil aggregated error since node's provider failed")
	}
	if !strings.Contains(err.Error(), "node") {
		t.Errorf(`error = %q, want it to mention "node"`, err.Error())
	}
	got := out.String()
	if !strings.Contains(got, "python: 3.12 (latest)") {
		t.Errorf(`output = %q, want python's line despite node's provider erroring`, got)
	}
}

func TestLangInstalled_OneProviderErrorDoesNotSuppressTheOthers(t *testing.T) {
	withStubManager(t,
		&stubRuntime{name: "node", listInstalledErr: errors.New("disk error")},
		&stubRuntime{name: "python", installed: []devruntime.Version{{Name: "3.12"}}},
	)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang", "installed"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected a non-nil aggregated error since node's provider failed")
	}
	got := out.String()
	if !strings.Contains(got, "python: 3.12") {
		t.Errorf(`output = %q, want python's line despite node's provider erroring`, got)
	}
}

func TestLangCurrent_OneProviderErrorDoesNotSuppressTheOthers(t *testing.T) {
	withStubManager(t,
		&stubRuntime{name: "node", currentErr: errors.New("marker unreadable")},
		&stubRuntime{name: "python", current: &devruntime.Version{Name: "3.12"}},
	)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang", "current"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected a non-nil aggregated error since node's provider failed")
	}
	got := out.String()
	if !strings.Contains(got, "python: 3.12") {
		t.Errorf(`output = %q, want python's line despite node's provider erroring`, got)
	}
}

func TestLang_BareCommandPrintsHelpAndRegisteredLanguages(t *testing.T) {
	withStubManager(t, &stubRuntime{name: "node"}, &stubRuntime{name: "python"})

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"lang"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev lang` returned error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "Registered languages:") {
		t.Errorf(`output = %q, want it to contain "Registered languages:"`, got)
	}
	if !strings.Contains(got, "node") || !strings.Contains(got, "python") {
		t.Errorf(`output = %q, want it to list both "node" and "python"`, got)
	}
}

func TestLangAliases_ShortFormsWork(t *testing.T) {
	withStubManager(t, &stubRuntime{name: "node", current: &devruntime.Version{Name: "22"}})

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"l", "c"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("`dev l c` returned error: %v", err)
	}
	if !strings.Contains(out.String(), "node: 22") {
		t.Errorf(`"dev l c" output = %q, want it to mention "node: 22"`, out.String())
	}
}
