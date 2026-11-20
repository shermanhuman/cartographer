package main

import (
	"context"

	"example.com/fixture/app"
	"example.com/fixture/osdep"
)

func main() {
	var s app.Server
	_ = osdep.OnLinux()
	s.Routes(nil, "")
	_ = app.Keys(1)
	_ = context.Background()
}
