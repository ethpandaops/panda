package cli

import "testing"

func TestValidateBuildMethod(t *testing.T) {
	tests := []struct {
		name    string
		client  string
		method  string
		wantErr bool
	}{
		{name: "unset", client: "geth", method: ""},
		{name: "prysm go", client: "prysm", method: "go"},
		{name: "prysm bazel", client: "prysm", method: "bazel"},
		{name: "prysm unknown method", client: "prysm", method: "make", wantErr: true},
		{name: "other client", client: "geth", method: "go", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBuildMethod(tt.client, tt.method)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateBuildMethod(%q, %q) error = %v, wantErr %v", tt.client, tt.method, err, tt.wantErr)
			}
		})
	}
}
