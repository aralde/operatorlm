//go:build windows

package providers

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	globalJobHandle windows.Handle
	globalJobOnce   sync.Once
	globalJobErr    error
)

func initGlobalJobObject() {
	globalJobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			globalJobErr = fmt.Errorf("create job object: %w", err)
			return
		}

		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}

		_, err = windows.SetInformationJobObject(
			h,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		)
		if err != nil {
			windows.CloseHandle(h)
			globalJobErr = fmt.Errorf("set job object information: %w", err)
			return
		}

		globalJobHandle = h
	})
}

func startCommand(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}

	initGlobalJobObject()
	if globalJobErr != nil {
		log.Printf("windows job object: initialization failed: %v", globalJobErr)
		return nil
	}

	hProcess, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		log.Printf("windows job object: failed to open child process handle for PID %d: %v", cmd.Process.Pid, err)
		return nil
	}
	defer windows.CloseHandle(hProcess)

	if err := windows.AssignProcessToJobObject(globalJobHandle, hProcess); err != nil {
		log.Printf("windows job object: failed to assign process %d to job: %v", cmd.Process.Pid, err)
	}

	return nil
}

func prepareCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".bat" || ext == ".cmd" {
		fullArgs := append([]string{"/c", name}, args...)
		cmd := exec.CommandContext(ctx, "cmd.exe", fullArgs...)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x08000000, // CREATE_NO_WINDOW
		}
		return cmd
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	return cmd
}

func getActiveLSInfo() (string, string, string) {
	addr := os.Getenv("ANTIGRAVITY_LS_ADDRESS")
	token := os.Getenv("ANTIGRAVITY_CSRF_TOKEN")

	if addr == "" || token == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		if addr == "" {
			cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command",
				`Get-NetTCPConnection -State Listen | Where-Object { $_.OwningProcess -ne 0 } | ForEach-Object { $p = Get-Process -Id $_.OwningProcess -ErrorAction SilentlyContinue; if ($p -and $p.Name -like '*language_server*') { $_.LocalPort } }`)
			cmd.SysProcAttr = &syscall.SysProcAttr{
				HideWindow:    true,
				CreationFlags: 0x08000000,
			}
			output, err := cmd.Output()
			if err == nil {
				lines := strings.Split(string(output), "\n")
				for _, line := range lines {
					port := strings.TrimSpace(line)
					if port != "" {
						testAddr := "localhost:" + port
						conn, err := net.DialTimeout("tcp", testAddr, 100*time.Millisecond)
						if err == nil {
							conn.Close()
							addr = testAddr
							break
						}
					}
				}
			}
		}

		if token == "" {
			cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command",
				`$cmd = Get-CimInstance Win32_Process -Filter "Name = 'language_server.exe'" | Select-Object -ExpandProperty CommandLine -First 1; if ($cmd -match '--csrf_token\s+([^\s]+)') { $Matches[1] }`)
			cmd.SysProcAttr = &syscall.SysProcAttr{
				HideWindow:    true,
				CreationFlags: 0x08000000,
			}
			output, err := cmd.Output()
			if err == nil {
				token = strings.TrimSpace(string(output))
			}
		}
	}

	if addr == "" {
		addr = "localhost:53235"
	}

	projectID := getProjectIDFromPB()

	return addr, token, projectID
}
