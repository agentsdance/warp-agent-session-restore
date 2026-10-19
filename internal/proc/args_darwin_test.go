package proc

import (
	"encoding/binary"
	"os"
	"reflect"
	"testing"
)

func TestArgumentsPreserveBoundariesAndDiscardSecrets(t *testing.T) {
	args := []string{"codex", "-c", "key=\"with spaces\"", "", "a'b"}
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(len(args)))
	b = append(b, []byte("/path/to/codex\x00\x00\x00")...)
	for _, a := range args {
		b = append(b, []byte(a)...)
		b = append(b, 0)
	}
	b = append(b, []byte("SECRET=do-not-retain\x00WARP_TERMINAL_SESSION_UUID=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\x00CODEX_HOME=/custom\x00")...)
	got, env, err := parseProcArgs(b)
	if err != nil || !reflect.DeepEqual(got, args) {
		t.Fatalf("%q %v", got, err)
	}
	if env["SECRET"] != "" || env["CODEX_HOME"] != "/custom" || len(env) != 2 {
		t.Fatal("environment filtering failed")
	}
}

func TestReadOwnOriginalArguments(t *testing.T) {
	args, _, err := Arguments(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, os.Args) {
		t.Fatalf("sysctl argv mismatch: %q vs %q", args, os.Args)
	}
}
