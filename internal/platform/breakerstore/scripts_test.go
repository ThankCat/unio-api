package breakerstore

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestLuaScriptsLoadInRedis(t *testing.T) {
	_, client, _ := newTestStore(t)
	names := make([]string, 0, len(assembledLuaScripts))
	for name := range assembledLuaScripts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := client.ScriptLoad(context.Background(), assembledLuaScripts[name]).Result(); err != nil {
			t.Fatalf("SCRIPT LOAD %q: %v", name, err)
		}
	}
}

func TestLuaScriptManifestAssemblesDeterministically(t *testing.T) {
	first := loadLuaScripts(luaScriptManifest)
	second := loadLuaScripts(luaScriptManifest)
	if len(first) != len(luaScriptManifest) || len(second) != len(first) {
		t.Fatalf("assembled script count = %d/%d, manifest = %d", len(first), len(second), len(luaScriptManifest))
	}
	for name, source := range first {
		if source != second[name] {
			t.Fatalf("script %q changed between identical assemblies", name)
		}
		if !strings.Contains(source, "-- source: ") {
			t.Fatalf("script %q has no source marker", name)
		}
		sum := sha1.Sum([]byte(source))
		if got, want := redis.NewScript(source).Hash(), hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("script %q SHA = %s, want %s", name, got, want)
		}
	}
}

func TestLuaScriptManifestPreservesHelperOrder(t *testing.T) {
	source := luaScript("attempt.finish")
	authoritative := strings.Index(source, "-- source: "+luaAuthoritativePath)
	guard := strings.Index(source, "-- source: "+luaAttemptPermitGuardPath)
	main := strings.Index(source, "-- source: lua/ops/finish.lua")
	if authoritative < 0 || guard <= authoritative || main <= guard {
		t.Fatalf("attempt.finish source order is invalid: authoritative=%d guard=%d main=%d", authoritative, guard, main)
	}
}

func TestAssembledLuaScriptsPassLuacheck(t *testing.T) {
	if os.Getenv("UNIO_LUA_STATIC_CHECK") != "1" {
		t.Skip("set UNIO_LUA_STATIC_CHECK=1 to run the external luacheck tool")
	}

	outputDir := t.TempDir()
	names := make([]string, 0, len(assembledLuaScripts))
	for name := range assembledLuaScripts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(outputDir, strings.ReplaceAll(name, ".", "_")+".lua")
		if err := os.WriteFile(path, []byte(assembledLuaScripts[name]), 0o600); err != nil {
			t.Fatalf("write assembled Lua script %q: %v", name, err)
		}
	}

	configPath, err := filepath.Abs(filepath.Join("..", "..", "..", ".luacheckrc"))
	if err != nil {
		t.Fatalf("resolve luacheck config: %v", err)
	}
	cmd := exec.Command("luacheck", "--config", configPath, outputDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("luacheck assembled scripts: %v\n%s", err, output)
	}
}

func TestLuaScriptManifestRejectsDuplicateName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Lua script name did not panic")
		}
	}()
	loadLuaScripts([]luaScriptSpec{
		{name: "duplicate", main: "lua/ops/reset.lua"},
		{name: "duplicate", main: "lua/ops/snapshot.lua"},
	})
}

func TestLuaScriptManifestRejectsMissingMain(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("missing Lua main file did not panic")
		}
	}()
	loadLuaScripts([]luaScriptSpec{{name: "missing", main: "lua/ops/does_not_exist.lua"}})
}
