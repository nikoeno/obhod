package proc

import (
	"context"
	"syscall"
	"unsafe"
)

// System returns a Lister backed by a Toolhelp32 snapshot.
func System() Lister { return toolhelp{} }

type toolhelp struct{}

func (toolhelp) List(ctx context.Context) ([]Process, error) {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(snap)
	var e syscall.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := syscall.Process32First(snap, &e); err != nil {
		return nil, err
	}
	var out []Process
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := syscall.UTF16ToString(e.ExeFile[:])
		out = append(out, Process{PID: int(e.ProcessID), PPID: int(e.ParentProcessID), Name: normalize(name)})
		if err := syscall.Process32Next(snap, &e); err != nil {
			if err == syscall.ERROR_NO_MORE_FILES {
				return out, nil
			}
			return nil, err
		}
	}
}
