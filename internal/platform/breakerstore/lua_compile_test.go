package breakerstore

import (
	"context"
	"testing"
)

// TestAllLuaScriptsCompile 把每个组装后的 Lua 脚本交给 Redis 编译。
//
// 这道防线针对一类单元测试抓不到的故障：脚本只在真正被调用的那条代码路径上才加载，
// 语法错误因此能一路躲过 build 与单测，直到线上某个分支第一次触发时才炸——
// 而且错误会被包装成「Redis 不可用」，指向完全错误的方向。
// SCRIPT LOAD 只编译不执行，所以这个测试与业务状态无关，也不会留下副作用。
func TestAllLuaScriptsCompile(t *testing.T) {
	_, client, _ := newTestStore(t)
	ctx := context.Background()

	if len(assembledLuaScripts) == 0 {
		t.Fatal("no assembled Lua scripts found")
	}
	for name, source := range assembledLuaScripts {
		t.Run(name, func(t *testing.T) {
			if _, err := client.ScriptLoad(ctx, source).Result(); err != nil {
				t.Fatalf("Lua script %q failed to compile: %v", name, err)
			}
		})
	}
}
