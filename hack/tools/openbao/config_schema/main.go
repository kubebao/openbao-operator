package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kubebao/openbao-operator/hack/tools/openbao/internal/configschema"
)

func main() {
	var moduleDir string
	flag.StringVar(&moduleDir, "module-dir", "", "Upstream OpenBao module directory")
	flag.Parse()
	if moduleDir == "" {
		fmt.Fprintln(os.Stderr, "error: -module-dir is required")
		os.Exit(2)
	}
	keys, err := configschema.Upstream(moduleDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, key := range keys.Sorted() {
		fmt.Println(key)
	}
}
