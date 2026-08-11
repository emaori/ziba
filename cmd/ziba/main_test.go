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
