package gitobj

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func nativeReferenceCounter(name string) (uint32, bool, error) {
	path, err := syscall.BytePtrFromString(name)
	if err != nil {
		return 0, false, err
	}
	attributes := unix.Attrlist{Bitmapcount: 5, Commonattr: unix.ATTR_CMN_RETURNED_ATTRS | unix.ATTR_CMN_GEN_COUNT}
	var data [28]byte
	_, _, errno := syscall.Syscall6(unix.SYS_GETATTRLIST, uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), unix.FSOPT_ATTR_CMN_EXTENDED|unix.FSOPT_PACK_INVAL_ATTRS, 0)
	runtime.KeepAlive(path)
	runtime.KeepAlive(attributes)
	if errno == syscall.ENOTSUP || errno == syscall.ENOSYS {
		return 0, false, nil
	}
	if errno != 0 {
		return 0, false, &os.PathError{Op: "getattrlist", Path: name, Err: errno}
	}
	return decodeReferenceCounter(data[:])
}

func decodeReferenceCounter(data []byte) (uint32, bool, error) {
	if len(data) != 28 {
		return 0, false, fmt.Errorf("reference generation attributes have %d bytes", len(data))
	}
	length := binary.LittleEndian.Uint32(data[:4])
	if length < 24 || length > 28 {
		return 0, false, fmt.Errorf("reference generation attributes report %d bytes", length)
	}
	if binary.LittleEndian.Uint32(data[4:8])&unix.ATTR_CMN_GEN_COUNT == 0 {
		return 0, false, nil
	}
	if length != 28 {
		return 0, false, fmt.Errorf("reference generation counter is truncated to %d bytes", length)
	}
	return binary.LittleEndian.Uint32(data[24:28]), true, nil
}
