// Command nsctl runs the local NeuronSphere: a control plane and the
// environment substrate its deployments sit on, with Docker as the only host
// prerequisite.
//
// See docs/proposals/NERD002_Go_CLI_Port.rst.
package main

import "github.com/neuronsphere/hmd-cli-neuronsphere/cmd"

// version is set at build time with -ldflags "-X main.version=<version>",
// read from meta-data/VERSION by the repository-root Makefile.
var version = "dev"

func main() {
	cmd.Execute(version)
}
