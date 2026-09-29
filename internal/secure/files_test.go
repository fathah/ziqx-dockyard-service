package secure

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStrictJSON(t *testing.T) {
	for _, raw := range []string{`{"name":"a","name":"b"}`, `{"name":"a","extra":1}`, `{"name":"a"} {}`, `{"name":"a","nested":{"x":1,"x":2}}`} {
		var v struct {
			Name string `json:"name"`
		}
		if Decode([]byte(raw), &v) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	var v struct {
		Name string `json:"name"`
	}
	if e := Decode([]byte(`{"name":"ok"}`), &v); e != nil || v.Name != "ok" {
		t.Fatal(e)
	}
}
func TestAtomicMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "secret")
	if e := Atomic(p, []byte("one"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := Atomic(p, []byte("two"), 0600); e != nil {
		t.Fatal(e)
	}
	st, _ := os.Stat(p)
	b, _ := os.ReadFile(p)
	if st.Mode().Perm() != 0600 || string(b) != "two" {
		t.Fatal("bad durable file")
	}
}
func TestPrivateDirRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	os.Symlink(t.TempDir(), link)
	if PrivateDir(link) == nil {
		t.Fatal("accepted symlink")
	}
}
