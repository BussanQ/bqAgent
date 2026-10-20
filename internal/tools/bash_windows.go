//go:build windows

package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The shell must join the job before it can execute or spawn children. Closing
// our non-inherited job handle also kills descendants if the agent exits.
func prepareBashProcess(cmd *exec.Cmd) (func() error, func() error, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create Windows job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(handle, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, nil, fmt.Errorf("configure Windows job: %w", err)
	}
	job := &windowsBashJob{handle: handle}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Run without the parent's console or a visible console window; stdin
	// remains connected to the null device.
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW
	cmd.SysProcAttr.HideWindow = true
	cmd.Cancel = func() error { return job.cancel(cmd) }
	return func() error { return job.assignAndResume(cmd) }, job.close, nil
}

// Start's context watcher can cancel while afterStart is still assigning the
// job. Serialize them: cancellation before assignment must kill the suspended
// shell too, rather than successfully terminating an empty job.
type windowsBashJob struct {
	mu        sync.Mutex
	handle    windows.Handle
	cancelled bool
}

func (job *windowsBashJob) cancel(cmd *exec.Cmd) error {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.cancelled = true
	if job.handle == 0 {
		return os.ErrProcessDone
	}
	jobErr := windows.TerminateJobObject(job.handle, 1)
	var processErr error
	if cmd.Process != nil {
		processErr = cmd.Process.Kill()
		if errors.Is(processErr, os.ErrProcessDone) {
			processErr = nil
		}
	}
	return errors.Join(jobErr, processErr)
}

func (job *windowsBashJob) assignAndResume(cmd *exec.Cmd) error {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.cancelled || job.handle == 0 {
		return fmt.Errorf("Windows job canceled before command started")
	}
	var assignErr error
	if err := cmd.Process.WithHandle(func(handle uintptr) {
		assignErr = windows.AssignProcessToJobObject(job.handle, windows.Handle(handle))
	}); err != nil {
		return fmt.Errorf("access command process: %w", err)
	}
	if assignErr != nil {
		return fmt.Errorf("assign command to Windows job: %w", assignErr)
	}
	return resumeBashThread(uint32(cmd.Process.Pid))
}

func (job *windowsBashJob) close() error {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.handle == 0 {
		return nil
	}
	defer func() {
		_ = windows.CloseHandle(job.handle)
		job.handle = 0
	}()
	if err := windows.TerminateJobObject(job.handle, 1); err != nil {
		return fmt.Errorf("terminate Windows job: %w", err)
	}
	// Termination is asynchronous. Even descendants with redirected output
	// must exit before a job is reported as reaped at the end of the turn.
	deadline := time.NewTimer(commandWaitDelay)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var info windowsJobAccounting
		if err := windows.QueryInformationJobObject(job.handle, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
			return fmt.Errorf("query Windows job cleanup: %w", err)
		}
		if info.ActiveProcesses == 0 {
			return nil
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("Windows job cleanup timed out with %d processes still exiting", info.ActiveProcesses)
		case <-ticker.C:
		}
	}
}

// JOBOBJECT_BASIC_ACCOUNTING_INFORMATION is not declared by x/sys/windows.
type windowsJobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// os/exec closes CreateProcess's primary thread handle. Before resumption a
// CREATE_SUSPENDED process has not run user code; find its suspended thread via
// Toolhelp and resume it only after assignment to the job succeeded.
func resumeBashThread(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("enumerate suspended command thread: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return fmt.Errorf("open suspended command thread: %w", err)
		}
		_, resumeErr := windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if resumeErr != nil {
			return fmt.Errorf("resume command thread: %w", resumeErr)
		}
		return nil
	}
	if err != nil && !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("enumerate command thread: %w", err)
	}
	return fmt.Errorf("no suspended thread found for command process %d", pid)
}
