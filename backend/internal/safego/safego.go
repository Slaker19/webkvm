// Package safego guards background goroutines against panics. Go's
// runtime terminates the whole process on an unhandled panic anywhere,
// so every goroutine we spawn that can touch foreign code (libvirt,
// qemu, sockets, I/O) should defer a Recover to log and swallow it
// instead of taking the server down with it.
package safego

import (
	"log/slog"
	"runtime/debug"
)

// Recover logs the panic (with a stack trace) and swallows it. It must
// be called as `defer safego.Recover("name")` as the first statement
// inside a goroutine so recover() can observe the panic from this
// goroutine's stack.
func Recover(name string) {
	if r := recover(); r != nil {
		slog.Error("goroutine_panic",
			"name", name,
			"panic", r,
			"stack", string(debug.Stack()),
		)
	}
}
