package main

import (
	"flag"
	"fmt"
	"os"
)

const (
	// configFileMode is the file mode used for rendered configuration files.
	// Configuration is not secret material, so 0644 is appropriate.
	configFileMode    = 0o644
	envPodIP          = "POD_IP"
	envHostname       = "HOSTNAME"
	pathWrapperBinary = "/utils/bao-wrapper"
	pathProbeBinary   = "/utils/bao-probe"
)

func main() {
	var selfInitSecretFiles []string
	flag.Func("self-init-secret-file", "required bootstrap input file; repeat for each input", func(value string) error {
		selfInitSecretFiles = append(selfInitSecretFiles, value)
		return nil
	})
	templatePath := flag.String("template", "", "path to the config template file")
	outputPath := flag.String("output", "", "path to write the rendered config file")
	selfInitPath := flag.String("self-init", "", "optional path to self-init config file (only used for pod-0)")
	wrapperSource := flag.String("copy-wrapper", "", "optional path to wrapper binary to copy to /utils/bao-wrapper")
	probeSource := flag.String("copy-probe", "", "optional path to probe binary to copy to /utils/bao-probe")
	flag.Parse()

	// Copy wrapper binary if specified (before rendering config)
	if *wrapperSource != "" {
		if err := copyWrapper(*wrapperSource); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "bao-config-init error: failed to copy wrapper: %v\n", err)
			os.Exit(1)
		}
	}

	if *probeSource != "" {
		if err := copyProbe(*probeSource); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "bao-config-init error: failed to copy probe: %v\n", err)
			os.Exit(1)
		}
	}

	if err := renderConfig(
		*templatePath,
		*outputPath,
		os.Getenv(envHostname),
		os.Getenv(envPodIP),
		*selfInitPath,
		selfInitSecretFiles...,
	); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "bao-config-init error: %v\n", err)
		os.Exit(1)
	}
}
