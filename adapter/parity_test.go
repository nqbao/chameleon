package adapter

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// Fixture generated from the original adapter implementations before port
// changes, covering each runtime in host, Docker, sandbox, and interactive modes.
func TestCommandParity(t *testing.T) {
	var fixtures []struct {
		Runtime, Environment string
		Context              AgentCommandContext
		Name                 string
		Args, Env, UnsetEnv  []string
	}
	data, err := os.ReadFile("testdata/commands.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Runtime+"/"+fixture.Environment+"/interactive="+map[bool]string{true: "true", false: "false"}[fixture.Context.Interactive], func(t *testing.T) {
			rt, err := AgentRuntimeFor(fixture.Runtime)
			if err != nil {
				t.Fatal(err)
			}
			cmd, err := rt.BuildCommand(fixture.Context)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				for _, p := range cmd.TempFiles {
					_ = os.Remove(p)
				}
			}()
			if fixture.Runtime == "pi" {
				if len(cmd.TempFiles) != 1 || len(cmd.TempMounts) != 1 {
					t.Fatal("Pi script must have command-owned cleanup and Docker mount")
				}
				for i, arg := range cmd.Args {
					if arg == cmd.TempFiles[0] {
						cmd.Args[i] = "$PI_GATE"
					}
				}
			}
			if cmd.Name != fixture.Name || !reflect.DeepEqual(cmd.Args, fixture.Args) || !reflect.DeepEqual(cmd.Env, fixture.Env) || !reflect.DeepEqual(cmd.UnsetEnv, fixture.UnsetEnv) {
				t.Fatalf("command changed: %+v; expected %+v", cmd, fixture)
			}
		})
	}
}

func TestPiExtensionReadableByOtherUsers(t *testing.T) {
	cmd, err := (&PiRuntime{}).BuildCommand(AgentCommandContext{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, p := range cmd.TempFiles {
			_ = os.Remove(p)
		}
	}()
	info, err := os.Stat(cmd.TempFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o044 != 0o044 {
		t.Fatalf("mode = %v, want world-readable", info.Mode().Perm())
	}
}
