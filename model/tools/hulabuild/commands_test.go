package main

import "testing"

func TestHasMkdocsSiteDir(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"empty", nil, false},
		{"build only", []string{"build"}, false},
		{"strict only", []string{"build", "--strict"}, false},
		{"--site-dir space-separated", []string{"build", "--site-dir", "out"}, true},
		{"-d space-separated", []string{"build", "-d", "out"}, true},
		{"--site-dir=value", []string{"build", "--site-dir=out"}, true},
		{"-d=value", []string{"build", "-d=out"}, true},
		{"flag prefix only", []string{"build", "--site"}, false}, // --site is not --site-dir
		{"unrelated -d-prefix", []string{"build", "--debug"}, false},
		{"mixed flags", []string{"build", "--strict", "--site-dir", "out", "--clean"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasMkdocsSiteDir(tc.args)
			if got != tc.want {
				t.Errorf("args=%v: got %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
