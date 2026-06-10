package cli

import "testing"

func TestIsPostComposerCommandDetectsComposerLifecycleCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "install", args: []string{"install"}, want: true},
		{name: "create project", args: []string{"create-project", "laravel/laravel", "site"}, want: true},
		{name: "global working directory option", args: []string{"--working-dir", "site", "install"}, want: true},
		{name: "non lifecycle command", args: []string{"require", "vendor/install"}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isPostComposerCommand(test.args); got != test.want {
				t.Fatalf("isPostComposerCommand(%#v) = %v, want %v", test.args, got, test.want)
			}
		})
	}
}
