package shell

import (
	"reflect"
	"strings"
	"testing"
)

func TestMergeUserPath_EmptyExistingGetsWantedInOrder(t *testing.T) {
	t.Parallel()
	got := mergeUserPath("", []string{`C:\Users\foo\.dev`, `C:\Users\foo\.dev\bin`})
	want := `C:\Users\foo\.dev;C:\Users\foo\.dev\bin`
	if got != want {
		t.Errorf("mergeUserPath() = %q, want %q", got, want)
	}
}

func TestMergeUserPath_PrependsMissingEntriesBeforeExisting(t *testing.T) {
	t.Parallel()
	got := mergeUserPath(`C:\other`, []string{`C:\Users\foo\.dev`})
	want := `C:\Users\foo\.dev;C:\other`
	if got != want {
		t.Errorf("mergeUserPath() = %q, want %q", got, want)
	}
}

func TestMergeUserPath_DoesNotDuplicateAlreadyPresentEntry(t *testing.T) {
	t.Parallel()
	got := mergeUserPath(`C:\Users\foo\.dev;C:\other`, []string{`C:\Users\foo\.dev`, `C:\Users\foo\.dev\bin`})
	want := `C:\Users\foo\.dev\bin;C:\Users\foo\.dev;C:\other`
	if got != want {
		t.Errorf("mergeUserPath() = %q, want %q (only the missing entry should be prepended)", got, want)
	}
}

func TestMergeUserPath_MatchIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	got := mergeUserPath(`c:\users\foo\.dev`, []string{`C:\Users\Foo\.dev`})
	want := `c:\users\foo\.dev`
	if got != want {
		t.Errorf("mergeUserPath() = %q, want %q (an equivalent differently-cased entry must not be duplicated)", got, want)
	}
}

func TestMergeUserPath_IgnoresEmptySegmentsInExisting(t *testing.T) {
	t.Parallel()
	got := mergeUserPath(`C:\other;;`, []string{`C:\Users\foo\.dev`})
	want := `C:\Users\foo\.dev;C:\other`
	if got != want {
		t.Errorf("mergeUserPath() = %q, want %q (a stray empty segment from a trailing ';' must not survive)", got, want)
	}
}

func TestPathEntriesFor_UsesDevHomeReferenceWhenExpandable(t *testing.T) {
	t.Parallel()
	got := pathEntriesFor(`C:\Users\foo\.dev`, true)
	want := []string{`%DEV_HOME%`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pathEntriesFor(devHome, true) = %v, want %v (a %%DEV_HOME%% reference, so PATH tracks DEV_HOME automatically if it's ever changed)", got, want)
	}
}

func TestPathEntriesFor_UsesLiteralPathWhenNotExpandable(t *testing.T) {
	t.Parallel()
	got := pathEntriesFor(`C:\Users\foo\.dev`, false)
	want := []string{`C:\Users\foo\.dev`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pathEntriesFor(devHome, false) = %v, want %v (a %%VAR%% reference never expands in a REG_SZ value, so it must fall back to the literal path)", got, want)
	}
}

// TestPathEntriesFor_NoDevHomeBinEntry pins the removal of
// $DEV_HOME\bin from the Windows registry PATH. That directory existed
// solely for the shim mechanism the shell-function PATH redesign
// deleted; nothing creates or references it any more, so writing it
// into a user's persistent registry PATH would leave a permanent
// reference to a directory dev never makes. These files predate the
// redesign and were missed by it — this test exists so the next
// redesign can't miss them the same way.
func TestPathEntriesFor_NoDevHomeBinEntry(t *testing.T) {
	t.Parallel()
	for _, useExpand := range []bool{true, false} {
		for _, entry := range pathEntriesFor(`C:\Users\foo\.dev`, useExpand) {
			if strings.HasSuffix(strings.ToLower(entry), `\bin`) {
				t.Errorf("pathEntriesFor(devHome, %v) still returns a \\bin entry %q; $DEV_HOME\\bin no longer exists", useExpand, entry)
			}
		}
	}
}
