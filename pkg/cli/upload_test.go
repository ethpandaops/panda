package cli

import (
	"bufio"
	"strings"
	"testing"
)

func TestConfirmDeleteDefaultsToKeep(t *testing.T) {
	cases := map[string]bool{"y\n": true, "YES\n": true, "yes\n": true, "\n": false, "n\n": false, "sure\n": false, "": false}

	for in, want := range cases {
		if got := confirmDelete(bufio.NewScanner(strings.NewReader(in)), "panda/uploads/ab12cd/report.html"); got != want {
			t.Errorf("confirmDelete(%q) = %v, want %v", in, got, want)
		}
	}
}
