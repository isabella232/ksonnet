// Copyright 2017 The kubecfg authors
//
//
//    Licensed under the Apache License, Version 2.0 (the "License");
//    you may not use this file except in compliance with the License.
//    You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS,
//    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//    See the License for the specific language governing permissions and
//    limitations under the License.

package cmd

import (
	"fmt"
	"os"
	"strings"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/ksonnet/ksonnet/metadata"
	"github.com/ksonnet/ksonnet/pkg/kubecfg"
)

const (
	flagDiffStrategy = "diff-strategy"
	diffShortDesc    = "Compare manifests, based on environment or location (local or remote)"
)

func init() {
	addEnvCmdFlags(diffCmd)
	bindJsonnetFlags(diffCmd)
	bindClientGoFlags(diffCmd)
	diffCmd.PersistentFlags().String(flagDiffStrategy, "subset", "Diff strategy, all or subset.")
	RootCmd.AddCommand(diffCmd)
}

var diffCmd = &cobra.Command{
	Use:   "diff <location1:env1> [location2:env2] [-c <component-name>]",
	Short: diffShortDesc,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("'diff' requires at least one argument, that is the name of the environment\n\n%s", cmd.UsageString())
		}
		if len(args) > 2 {
			return fmt.Errorf("'diff' takes at most two arguments, that are the name of the environments\n\n%s", cmd.UsageString())
		}

		flags := cmd.Flags()

		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		wd := metadata.AbsPath(cwd)

		componentNames, err := flags.GetStringArray(flagComponent)
		if err != nil {
			return err
		}

		var env1 *string
		if len(args) > 0 {
			env1 = &args[0]
		}

		var env2 *string
		if len(args) > 1 {
			env2 = &args[1]
		}

		diffStrategy, err := flags.GetString(flagDiffStrategy)
		if err != nil {
			return err
		}

		c, err := initDiffCmd(cmd, wd, env1, env2, componentNames, diffStrategy)
		if err != nil {
			return err
		}

		return c.Run(cmd.OutOrStdout())
	},
	Long: `
The ` + "`diff`" + ` command displays standard file diffs, and can be used to compare manifests
based on *environment* or location ('local' ksonnet app manifests or what's running
on a 'remote' server).

Using this command, you can compare:

1. *Remote* and *local* manifests for a single environment
2. *Remote* manifests for two separate environments
3. *Local* manifests for two separate environments
4. A *remote* manifest in one environment and a *local* manifest in another environment

To see the official syntax, see the examples below. Make sure that your $KUBECONFIG
matches what you've defined in environments.

When NO component is specified (no ` + "`-c`" + ` flag), this command diffs all of
the files in the ` + "`components/`" + ` directory.

When a component IS specified via the ` + "`-c`" + ` flag, this command only checks
the manifest for that particular component.

### Related Commands

* ` + "`ks param diff` " + `— ` + paramShortDesc["diff"] + `

### Syntax
`,
	Example: `
# Show diff between remote and local manifests for a single 'dev' environment.
# This command diffs *all* components in the ksonnet app, and can be used in any
# of that app's subdirectories.
ks diff remote:dev local:dev

# Shorthand for the previous command (remote 'dev' and local 'dev')
ks diff dev

# Show diff between the remote resources running in two different ksonnet environments
# 'us-west/dev' and 'us-west/prod'. This command diffs all resources defined in
# the ksonnet app.
ks diff remote:us-west/dev remote:us-west/prod

# Show diff between local manifests in the 'us-west/dev' environment and remote
# resources in the 'us-west/prod' environment, for an entire ksonnet app
ks diff local:us-west/dev remote:us-west/prod

# Show diff between what's in the local manifest and what's actually running in the
# 'dev' environment, but for the Redis component ONLY
ks diff dev -c redis
`,
}

func initDiffCmd(cmd *cobra.Command, wd metadata.AbsPath, envFq1, envFq2 *string, files []string, diffStrategy string) (kubecfg.DiffCmd, error) {
	const (
		remote = "remote"
		local  = "local"
	)

	if envFq2 == nil {
		return initDiffSingleEnv(*envFq1, diffStrategy, files, cmd, wd)
	}

	// expect envs to be of the format local:myenv or remote:myenv
	env1 := strings.SplitN(*envFq1, ":", 2)
	env2 := strings.SplitN(*envFq2, ":", 2)

	// validation
	if len(env1) < 2 || len(env2) < 2 || (env1[0] != local && env1[0] != remote) || (env2[0] != local && env2[0] != remote) {
		return nil, fmt.Errorf("<env> must be prefaced by %s: or %s:, ex: %s:us-west/prod", local, remote, remote)
	}
	if len(files) > 0 {
		return nil, fmt.Errorf("'-f' is not currently supported for multiple environments")
	}

	manager, err := metadata.Find(wd)
	if err != nil {
		return nil, err
	}

	if env1[0] == local && env2[0] == local {
		return initDiffLocalCmd(env1[1], env2[1], diffStrategy, cmd, manager)
	}

	if env1[0] == remote && env2[0] == remote {
		return initDiffRemotesCmd(env1[1], env2[1], diffStrategy, cmd, manager)
	}

	localEnv := env1[1]
	remoteEnv := env2[1]
	if env1[0] == remote {
		localEnv = env2[1]
		remoteEnv = env1[1]
	}
	return initDiffRemoteCmd(localEnv, remoteEnv, diffStrategy, cmd, manager)
}

// initDiffSingleEnv sets up configurations for diffing using one environment
func initDiffSingleEnv(env, diffStrategy string, files []string, cmd *cobra.Command, wd metadata.AbsPath) (kubecfg.DiffCmd, error) {
	c := kubecfg.DiffRemoteCmd{}
	c.DiffStrategy = diffStrategy
	c.Client = &kubecfg.Client{}
	var err error

	if strings.HasPrefix(env, "remote:") || strings.HasPrefix(env, "local:") {
		return nil, fmt.Errorf("single <env> argument with prefix 'local:' or 'remote:' not allowed")
	}

	c.Client.APIObjects, err = expandEnvCmdObjs(cmd, env, files, wd)
	if err != nil {
		return nil, err
	}

	c.Client.ClientPool, c.Client.Discovery, err = restClientPool(cmd, &env)
	if err != nil {
		return nil, err
	}

	c.Client.Namespace, err = namespace()
	if err != nil {
		return nil, err
	}

	return &c, nil
}

// initDiffLocalCmd sets up configurations for diffing between two sets of expanded Kubernetes objects locally
func initDiffLocalCmd(env1, env2, diffStrategy string, cmd *cobra.Command, m metadata.Manager) (kubecfg.DiffCmd, error) {
	c := kubecfg.DiffLocalCmd{}
	c.DiffStrategy = diffStrategy
	var err error

	c.Env1 = &kubecfg.LocalEnv{}
	c.Env1.Name = env1
	c.Env1.APIObjects, err = expandEnvObjs(cmd, c.Env1.Name, m)
	if err != nil {
		return nil, err
	}

	c.Env2 = &kubecfg.LocalEnv{}
	c.Env2.Name = env2
	c.Env2.APIObjects, err = expandEnvObjs(cmd, c.Env2.Name, m)
	if err != nil {
		return nil, err
	}

	return &c, nil
}

// initDiffRemotesCmd sets up configurations for diffing between objects on two remote clusters
func initDiffRemotesCmd(env1, env2, diffStrategy string, cmd *cobra.Command, m metadata.Manager) (kubecfg.DiffCmd, error) {
	c := kubecfg.DiffRemotesCmd{}
	c.DiffStrategy = diffStrategy

	c.ClientA = &kubecfg.Client{}
	c.ClientB = &kubecfg.Client{}

	c.ClientA.Name = env1
	c.ClientB.Name = env2

	var err error
	c.ClientA.APIObjects, err = expandEnvObjs(cmd, c.ClientA.Name, m)
	if err != nil {
		return nil, err
	}
	c.ClientB.APIObjects, err = expandEnvObjs(cmd, c.ClientB.Name, m)
	if err != nil {
		return nil, err
	}

	c.ClientA.ClientPool, c.ClientA.Discovery, c.ClientA.Namespace, err = setupClientConfig(&c.ClientA.Name, cmd)
	if err != nil {
		return nil, err
	}
	c.ClientB.ClientPool, c.ClientB.Discovery, c.ClientB.Namespace, err = setupClientConfig(&c.ClientB.Name, cmd)
	if err != nil {
		return nil, err
	}

	return &c, nil
}

// initDiffRemoteCmd sets up configurations for diffing between local objects and objects on a remote cluster
func initDiffRemoteCmd(localEnv, remoteEnv, diffStrategy string, cmd *cobra.Command, m metadata.Manager) (kubecfg.DiffCmd, error) {
	c := kubecfg.DiffRemoteCmd{}
	c.DiffStrategy = diffStrategy
	c.Client = &kubecfg.Client{}

	var err error
	c.Client.APIObjects, err = expandEnvObjs(cmd, localEnv, m)
	if err != nil {
		return nil, err
	}

	c.Client.ClientPool, c.Client.Discovery, c.Client.Namespace, err = setupClientConfig(&remoteEnv, cmd)
	if err != nil {
		return nil, err
	}

	return &c, nil
}

func setupClientConfig(env *string, cmd *cobra.Command) (dynamic.ClientPool, discovery.DiscoveryInterface, string, error) {
	overrides := &clientcmd.ConfigOverrides{}
	loadingRules := *clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.DefaultClientConfig = &clientcmd.DefaultClientConfig
	config := clientcmd.NewInteractiveDeferredLoadingClientConfig(&loadingRules, overrides, os.Stdin)

	clientPool, discovery, err := restClient(cmd, env, config, overrides)
	if err != nil {
		return nil, nil, "", err
	}

	namespace, err := namespaceFor(config, overrides)
	if err != nil {
		return nil, nil, "", err
	}

	return clientPool, discovery, namespace, nil
}

// expandEnvObjs finds and expands templates for an environment
func expandEnvObjs(cmd *cobra.Command, env string, manager metadata.Manager) ([]*unstructured.Unstructured, error) {
	expander, err := newExpander(cmd)
	if err != nil {
		return nil, err
	}

	libPath, vendorPath, envLibPath, envComponentPath, envParamsPath := manager.LibPaths(env)
	componentPaths, err := manager.ComponentPaths()
	if err != nil {
		return nil, err
	}

	baseObj, err := constructBaseObj(componentPaths, nil)
	if err != nil {
		return nil, err
	}
	params := importParams(string(envParamsPath))

	expander.FlagJpath = append([]string{string(libPath), string(vendorPath), string(envLibPath)}, expander.FlagJpath...)
	expander.ExtCodes = append([]string{baseObj, params}, expander.ExtCodes...)

	envFiles := []string{string(envComponentPath)}

	return expander.Expand(envFiles)
}
