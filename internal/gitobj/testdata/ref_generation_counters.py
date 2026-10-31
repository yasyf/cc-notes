import ctypes
import errno
import json
import os
import struct
import sys


class Attrlist(ctypes.Structure):
    _fields_ = [
        ("bitmapcount", ctypes.c_ushort),
        ("reserved", ctypes.c_ushort),
        ("common", ctypes.c_uint32),
        ("volume", ctypes.c_uint32),
        ("directory", ctypes.c_uint32),
        ("file", ctypes.c_uint32),
        ("fork", ctypes.c_uint32),
    ]


libc = ctypes.CDLL("/usr/lib/libSystem.B.dylib", use_errno=True)
libc.fgetattrlist.argtypes = [
    ctypes.c_int,
    ctypes.POINTER(Attrlist),
    ctypes.c_void_p,
    ctypes.c_size_t,
    ctypes.c_ulong,
]
libc.fgetattrlist.restype = ctypes.c_int
returned_attrs = 0x80000000
generation_count = 0x00080000
recursive_generation_count = 0x00000400
pack_invalid = 0x00000008
common_extended = 0x00000020


def counter(fd, recursive):
    common = returned_attrs | (0 if recursive else generation_count)
    fork = recursive_generation_count if recursive else 0
    request = Attrlist(5, 0, common, 0, 0, 0, fork)
    result = ctypes.create_string_buffer(32)
    ctypes.set_errno(0)
    code = libc.fgetattrlist(
        fd,
        ctypes.byref(request),
        result,
        len(result),
        pack_invalid | common_extended,
    )
    if code != 0:
        failure = ctypes.get_errno()
        if failure not in (errno.EINVAL, errno.ENOTSUP):
            raise OSError(failure, os.strerror(failure))
        return {"supported": False, "value": None, "returned": None, "errno": failure}
    size, = struct.unpack_from("<I", result.raw)
    if size != (32 if recursive else 28):
        raise ValueError(f"unexpected attribute buffer size {size}")
    returned = struct.unpack_from("<5I", result.raw, 4)
    if returned[0] & returned_attrs == 0:
        raise ValueError("returned attribute mask is absent")
    if returned[0] & ~common or any(returned[1:4]) or returned[4] & ~fork:
        raise ValueError(f"unexpected returned attributes {returned}")
    supported = bool(returned[4] & fork if recursive else returned[0] & generation_count)
    value, = struct.unpack_from("<Q" if recursive else "<I", result.raw, 24)
    return {
        "supported": supported,
        "value": value if supported else None,
        "packed_value": value,
        "returned": returned,
        "errno": 0,
    }


fd = os.open(sys.argv[1], os.O_RDONLY | os.O_DIRECTORY)
try:
    identity = os.fstat(fd)
    for count, line in enumerate(sys.stdin, 1):
        if count > 104 or line != "snapshot\n":
            raise ValueError("unexpected counter request")
        print(json.dumps({
            "device": identity.st_dev,
            "inode": identity.st_ino,
            "common": counter(fd, False),
            "recursive": counter(fd, True),
        }), flush=True)
finally:
    os.close(fd)
