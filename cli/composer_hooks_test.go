package cli

import "testing"

func TestIsPostComposerCommandDetectsComposerLifecycleCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "install", args: []string{"install"}, want: true},
		{name: "update", args: []string{"update"}, want: true},
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

func TestShouldRunPostComposerHookSkipsOnlyComposerSolverFailures(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		want     bool
	}{
		{name: "success", exitCode: 0, want: true},
		{name: "generic failure", exitCode: 1, want: true},
		{name: "dependency solver failure", exitCode: 2, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldRunPostComposerHook(test.exitCode); got != test.want {
				t.Fatalf("shouldRunPostComposerHook(%d) = %v, want %v", test.exitCode, got, test.want)
			}
		})
	}
}
