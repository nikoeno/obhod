package paths

import (
	"fmt"
	"syscall"
	"unsafe"
)

// sddl grants full control to SYSTEM and Administrators only and blocks inheritance from the parent.
const sddl = "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

const (
	seFileObject         = 1
	daclSecurityInfo     = 0x4
	protectedDaclSecInfo = 0x80000000
	sddlRevision1        = 1
)

var (
	advapi32                      = syscall.NewLazyDLL("advapi32.dll")
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	procConvertSDDL               = advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	procGetSecurityDescriptorDacl = advapi32.NewProc("GetSecurityDescriptorDacl")
	procSetNamedSecurityInfo      = advapi32.NewProc("SetNamedSecurityInfoW")
	procLocalFree                 = kernel32.NewProc("LocalFree")
)

// restrictACL replaces the DACL of dir with SYSTEM + Administrators, full control.
func restrictACL(dir string) error {
	s, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return err
	}
	var sd uintptr
	if r, _, e := procConvertSDDL.Call(uintptr(unsafe.Pointer(s)), sddlRevision1, uintptr(unsafe.Pointer(&sd)), 0); r == 0 {
		return fmt.Errorf("build descriptor: %w", e)
	}
	defer procLocalFree.Call(sd)
	var present, defaulted uint32
	var acl uintptr
	if r, _, e := procGetSecurityDescriptorDacl.Call(sd, uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&acl)), uintptr(unsafe.Pointer(&defaulted))); r == 0 || present == 0 {
		return fmt.Errorf("read descriptor: %w", e)
	}
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	if r, _, _ := procSetNamedSecurityInfo.Call(uintptr(unsafe.Pointer(p)), seFileObject, daclSecurityInfo|protectedDaclSecInfo, 0, 0, acl, 0); r != 0 {
		return fmt.Errorf("set ACL: %w", syscall.Errno(r))
	}
	return nil
}
