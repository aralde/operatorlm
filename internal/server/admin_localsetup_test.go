package server

import (
	"net"
	"testing"
)

func TestFindFreePortSkipsPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port

	if portFree(busy) {
		t.Fatalf("port %d is in use but reported free", busy)
	}
	got, err := findFreePort(busy)
	if err != nil {
		t.Fatal(err)
	}
	if got == busy || !portFree(got) {
		t.Fatalf("findFreePort(%d) = %d, want a different free port", busy, got)
	}
}
