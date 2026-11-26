package updater

import "syscall"

func init() {
	// Release the identity lock even if OpenRC loses the supervisor to SIGKILL.
	workerAttributes.Pdeathsig = syscall.SIGKILL
}
