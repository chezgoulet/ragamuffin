package fileutil

import "testing"

func TestIsIndexable_Markdown(t *testing.T) {
	if !IsIndexable("README.md") {
		t.Error("expected .md to be indexable")
	}
}

func TestIsIndexable_HiddenFile(t *testing.T) {
	if IsIndexable(".gitignore") {
		t.Error("expected hidden files to be excluded")
	}
}

func TestIsIndexable_HiddenDotFile(t *testing.T) {
	if IsIndexable(".dotfile.md") {
		t.Error("expected .dotfile.md to be excluded")
	}
}

func TestIsIndexable_UnknownExtension(t *testing.T) {
	if IsIndexable("image.png") {
		t.Error("expected .png to be excluded")
	}
}

func TestIsIndexable_NoExtension(t *testing.T) {
	if IsIndexable("Makefile") {
		t.Error("expected files with no extension to be excluded")
	}
}

func TestIsIndexable_AllKnownExtensions(t *testing.T) {
	exts := []string{".txt", ".md", ".json", ".csv", ".xml", ".yaml", ".yml", ".html", ".htm", ".rst", ".adoc", ".log"}
	for _, ext := range exts {
		name := "file" + ext
		if !IsIndexable(name) {
			t.Errorf("expected %s to be indexable", name)
		}
	}
}

func TestIsIndexable_UppercaseExtension(t *testing.T) {
	if !IsIndexable("README.MD") {
		t.Error("expected .MD (uppercase) to be indexable")
	}
}

func TestIsIndexable_PathWithDir(t *testing.T) {
	if !IsIndexable("subdir/notes.md") {
		t.Error("expected subdir/notes.md to be indexable")
	}
}

func TestIsIndexable_HiddenFileInSubdir(t *testing.T) {
	if IsIndexable("subdir/.hidden.txt") {
		t.Error("expected hidden file in subdir to be excluded")
	}
}

func TestIsIndexable_EmptyPath(t *testing.T) {
	if IsIndexable("") {
		t.Error("expected empty path to be excluded")
	}
}
