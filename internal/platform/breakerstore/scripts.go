package breakerstore

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed lua
var luaFiles embed.FS

type luaScriptSpec struct {
	name    string
	helpers []string
	main    string
}

var assembledLuaScripts = loadLuaScripts(luaScriptManifest)

func luaScript(name string) string {
	script, ok := assembledLuaScripts[name]
	if !ok {
		panic("breakerstore: unknown Lua script " + name)
	}
	return script
}

func loadLuaScripts(specs []luaScriptSpec) map[string]string {
	loaded := make(map[string]string, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.name) == "" || strings.TrimSpace(spec.main) == "" {
			panic("breakerstore: Lua script manifest contains an empty name or main file")
		}
		if _, exists := loaded[spec.name]; exists {
			panic("breakerstore: duplicate Lua script name " + spec.name)
		}
		paths := append(append([]string(nil), spec.helpers...), spec.main)
		var source strings.Builder
		for index, path := range paths {
			content, err := luaFiles.ReadFile(path)
			if err != nil {
				panic(fmt.Sprintf("breakerstore: read Lua source %s for %s: %v", path, spec.name, err))
			}
			if index == len(paths)-1 && strings.TrimSpace(string(content)) == "" {
				panic(fmt.Sprintf("breakerstore: Lua main source %s for %s is empty", path, spec.name))
			}
			source.WriteString("-- source: ")
			source.WriteString(path)
			source.WriteByte('\n')
			source.Write(content)
			if len(content) == 0 || content[len(content)-1] != '\n' {
				source.WriteByte('\n')
			}
		}
		loaded[spec.name] = source.String()
	}
	return loaded
}
