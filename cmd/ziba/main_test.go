package main

import (
	"errors"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr error // nil means the command is expected to succeed
	}{
		{name: "no arguments asks for usage", args: nil, wantErr: errUsage},
		{name: "unknown command asks for usage", args: []string{"bogus"}, wantErr: errUsage},
		{name: "version succeeds", args: []string{"version"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(tt.args)

			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("run(%q) = %v, want no error", tt.args, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("run(%q) = %v, want %v", tt.args, err, tt.wantErr)
			}
		})
	}
}

// Where to listen has to be settable without changing the command, because in a
// container the command is baked into the image.
//
// ZIBA_PORT is tested because it already existed and already looked like it
// worked: compose read it to publish a host port and the program never did, so
// setting it to 9000 moved the outside of the container and left the inside on
// 8080.
func TestListenAddress(t *testing.T) {
	tests := []struct {
		name       string
		addr, port string
		want       string
	}{
		{"nothing set", "", "", ":8080"},
		{"a port alone", "", "9000", ":9000"},
		{"a port written with its colon", "", ":9000", ":9000"},
		{"a whole address, for a reverse proxy on the same host", "127.0.0.1:9000", "", "127.0.0.1:9000"},
		{"the whole address wins over the port", "127.0.0.1:9000", "7000", "127.0.0.1:9000"},
		{"blank is not set", "  ", "", ":8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ZIBA_ADDR", tt.addr)
			t.Setenv("ZIBA_PORT", tt.port)
			if got := listenAddress(); got != tt.want {
				t.Errorf("listenAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}
