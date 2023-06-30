//go:build linux
// +build linux

package libpod

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strconv"
	"strings"

	"github.com/containers/podman/v4/libpod/define"
	"github.com/containers/podman/v4/pkg/rootless"
	"github.com/containers/psgo"
	"github.com/google/shlex"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

// Top gathers statistics about the running processes in a container. It returns a
// []string for output
func (c *Container) Top(descriptors []string) ([]string, error) {
	if c.config.NoCgroups {
		return nil, fmt.Errorf("cannot run top on container %s as it did not create a cgroup: %w", c.ID(), define.ErrNoCgroups)
	}

	conStat, err := c.State()
	if err != nil {
		return nil, fmt.Errorf("unable to look up state for %s: %w", c.ID(), err)
	}
	if conStat != define.ContainerStateRunning {
		return nil, errors.New("top can only be used on running containers")
	}

	// Also support comma-separated input.
	psgoDescriptors := []string{}
	for _, d := range descriptors {
		for _, s := range strings.Split(d, ",") {
			if s != "" {
				psgoDescriptors = append(psgoDescriptors, s)
			}
		}
	}

	// If we encountered an ErrUnknownDescriptor error, fallback to executing
	// ps(1). This ensures backwards compatibility to users depending on ps(1)
	// and makes sure we're ~compatible with docker.
	output, psgoErr := c.GetContainerPidInformation(psgoDescriptors)
	if psgoErr == nil {
		return output, nil
	}
	if !errors.Is(psgoErr, psgo.ErrUnknownDescriptor) {
		return nil, psgoErr
	}

	// Note that the descriptors to ps(1) must be shlexed (see #12452).
	psDescriptors := []string{}
	for _, d := range descriptors {
		shSplit, err := shlex.Split(d)
		if err != nil {
			return nil, fmt.Errorf("parsing ps args: %w", err)
		}
		for _, s := range shSplit {
			if s != "" {
				psDescriptors = append(psDescriptors, s)
			}
		}
	}

	output, err = c.execPS(psDescriptors)
	if err != nil {
		return nil, fmt.Errorf("executing ps(1) in the container: %w", err)
	}

	// Trick: filter the ps command from the output instead of
	// checking/requiring PIDs in the output.
	filtered := []string{}
	cmd := strings.Join(descriptors, " ")
	for _, line := range output {
		if !strings.Contains(line, cmd) {
			filtered = append(filtered, line)
		}
	}

	return filtered, nil
}

// GetContainerPidInformation returns process-related data of all processes in
// the container.  The output data can be controlled via the `descriptors`
// argument which expects format descriptors and supports all AIXformat
// descriptors of ps (1) plus some additional ones to for instance inspect the
// set of effective capabilities.  Each element in the returned string slice
// is a tab-separated string.
//
// For more details, please refer to github.com/containers/psgo.
func (c *Container) GetContainerPidInformation(descriptors []string) ([]string, error) {
	pid := strconv.Itoa(c.state.PID)
	// NOTE: psgo returns a [][]string to give users the ability to apply
	//       filters on the data.  We need to change the API here
	//       to return a [][]string if we want to make use of
	//       filtering.
	opts := psgo.JoinNamespaceOpts{FillMappings: rootless.IsRootless()}

	psgoOutput, err := psgo.JoinNamespaceAndProcessInfoWithOptions(pid, descriptors, &opts)
	if err != nil {
		return nil, err
	}
	res := []string{}
	for _, out := range psgoOutput {
		res = append(res, strings.Join(out, "\t"))
	}
	return res, nil
}

// execute ps(1) from the host within the container mountns
// This is done by first lookup the ps host path and open a fd for it,
// then read all linked libs from it then open them as well, (including the linker).
// Then we can join the pid and mountns, lastly execute the linker directly via
// /proc/self/fd/X and use --preload to set all shared libs as well.
// That way we open everything on the host and do not depend on any container libs.
func (c *Container) execPS(psArgs []string) ([]string, error) {
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer wPipe.Close()
	defer rPipe.Close()

	stdout := []string{}
	go func() {
		scanner := bufio.NewScanner(rPipe)
		for scanner.Scan() {
			stdout = append(stdout, scanner.Text())
		}
	}()

	psPath, err := exec.LookPath("ps")
	if err != nil {
		return nil, err
	}
	psFD, err := unix.Open(psPath, unix.O_PATH, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(psFD)
	logrus.Debugf("Trying to execute %q from the host in the container", psPath)
	psPath = fmt.Sprintf("/proc/self/fd/%d", psFD)

	args := append([]string{psPath}, psArgs...)

	// Now get all shared libs from ps(1), if this fails it is likely a static
	// binary so no further actin required.
	cmd := exec.Command("ldd", psPath)
	output, err := cmd.Output()
	if err == nil {
		logrus.Debug("ps is dynamically linked, open linker and shared libraries for it")
		var preload []string
		var linkerPath string
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 3 {
				// open the shared lib on the host as it will most likely not be in the container
				logrus.Debugf("Open shared library for ps: %s", fields[2])
				fd, err := unix.Open(fields[2], unix.O_PATH, 0)
				if err == nil {
					defer unix.Close(fd)
					preload = append(preload, fmt.Sprintf("/proc/self/fd/%d", fd))
				}
			} else if len(fields) == 2 {
				if path.IsAbs(fields[0]) {
					// this should be the dynamic linker
					logrus.Debugf("Using linker for ps: %s", fields[0])
					linkFD, err := unix.Open(fields[0], unix.O_PATH|unix.O_CLOEXEC, 0)
					if err != nil {
						return nil, err
					}
					defer unix.Close(linkFD)
					linkerPath = fmt.Sprintf("/proc/self/fd/%d", linkFD)
				}
			}
		}
		// Ok, set linker args. First overwrite argv[0] because busybox for example needs it to know
		// which program to execute as everything is in one binary and they need to proper name.
		// Second now preload all linked shared libs. This is to prevent the executable from loading
		// any libs in the container and thus very likely failing.
		args = append([]string{linkerPath, "--argv0", "ps", "--preload", strings.Join(preload, " ")}, args...)
	}

	pid := c.state.PID
	errChan := make(chan error)
	go func() {
		defer close(errChan)

		// DO NOT UNLOCK THIS THREAD!!!
		// We are joining a different pid and mount ns, go must destroy the
		// thread when we are done and not reuse it.
		runtime.LockOSThread()

		// join the mount namespace of pid
		mntFD, err := os.Open(fmt.Sprintf("/proc/%d/ns/mnt", pid))
		if err != nil {
			errChan <- err
			return
		}
		defer mntFD.Close()

		// join the pid namespace of pid
		pidFD, err := os.Open(fmt.Sprintf("/proc/%d/ns/pid", pid))
		if err != nil {
			errChan <- err
			return
		}
		defer pidFD.Close()

		// create a new mountns on the current thread
		if err = unix.Unshare(unix.CLONE_NEWNS); err != nil {
			errChan <- fmt.Errorf("unshare NEWNS: %w", err)
			return
		}
		if err := unix.Setns(int(mntFD.Fd()), unix.CLONE_NEWNS); err != nil {
			errChan <- fmt.Errorf("setns NEWNS: %w", err)
			return
		}

		if err := unix.Setns(int(pidFD.Fd()), unix.CLONE_NEWPID); err != nil {
			errChan <- fmt.Errorf("setns NEWPID: %w", err)
			return
		}

		logrus.Debugf("Executing ps in the containers mnt+pid namespace, final command: %v", args)
		var errBuf bytes.Buffer
		path := args[0]
		args[0] = "ps"
		cmd := exec.Cmd{
			Path:   path,
			Args:   args,
			Stdout: wPipe,
			Stderr: &errBuf,
		}

		err = cmd.Run()
		if err != nil {
			exitError := &exec.ExitError{}
			if errors.As(err, &exitError) && errBuf.Len() > 0 {
				// when error printed on stderr include it in error
				err = fmt.Errorf("ps failed with exit code %d: %s", exitError.ExitCode(), errBuf.String())
			} else {
				err = fmt.Errorf("could not execute ps in the container: %w", err)
			}
		}
		errChan <- err
	}()

	// the channel blocks and waits for command completion
	err = <-errChan
	return stdout, err
}
