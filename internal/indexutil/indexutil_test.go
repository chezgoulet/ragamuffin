package indexutil

import "testing"

func TestIsIndexable_Markdown(t *testing.T) {
	if !IsIndexable("notes.md") {
		t.Error("expected .md to be indexable")
	}
}

func TestIsIndexable_Txt(t *testing.T) {
	if !IsIndexable("readme.txt") {
		t.Error("expected .txt to be indexable")
	}
}

func TestIsIndexable_Org(t *testing.T) {
	if !IsIndexable("todo.org") {
		t.Error("expected .org to be indexable")
	}
}

func TestIsIndexable_Rst(t *testing.T) {
	if !IsIndexable("guide.rst") {
		t.Error("expected .rst to be indexable")
	}
}

func TestIsIndexable_NoExtension(t *testing.T) {
	if !IsIndexable("Makefile") {
		t.Error("expected files with no extension to be indexable")
	}
}

func TestIsIndexable_Uppercase(t *testing.T) {
	if !IsIndexable("README.MD") {
		t.Error("expected .MD (uppercase) to be indexable")
	}
}

func TestIsIndexable_UnknownExtension(t *testing.T) {
	if IsIndexable("image.png") {
		t.Error("expected .png to be excluded")
	}
}

func TestIsIndexable_GoFile(t *testing.T) {
	if IsIndexable("main.go") {
		t.Error("expected .go to be excluded")
	}
}

func TestIsIndexable_DotDirectory(t *testing.T) {
	if IsIndexable(".git/config") {
		t.Error("expected .git directory to be excluded")
	}
}

func TestIsIndexable_DotPrefix(t *testing.T) {
	if IsIndexable(".hidden") {
		t.Error("expected .hidden to be excluded")
	}
}

func TestIsIndexable_DotInPath(t *testing.T) {
	if IsIndexable("vendor/.cache/notes.md") {
		t.Error("expected path containing /. to be excluded")
	}
}

func TestIsIndexable_SubdirWithDot(t *testing.T) {
	if IsIndexable("src/.docusaurus/notes.md") {
		t.Error("expected path with /. to be excluded")
	}
}

func TestIsIndexable_ValidSubdir(t *testing.T) {
	if !IsIndexable("src/docs/architecture.md") {
		t.Error("expected valid subdir path to be indexable")
	}
}
