package script

import (
	"os/exec"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

func ExecuteScript(script string, envs []string) (string, error) {
	// Command to execute the Bash script
	logger.Debug(script)
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(cmd.Env, envs...)
	// Capture the standard output and standard error
	output, err := cmd.CombinedOutput()

	if err != nil {
		// Command execution failed
		exitError, ok := err.(*exec.ExitError)
		if ok {
			logger.Warn("Script execution failed with exit code:", exitError.ExitCode(), string(output), cmd)
			return string(output), err
		}
		return "", err
	}

	return string(output), nil
}
