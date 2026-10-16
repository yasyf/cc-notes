package gitguard

// x/sys/unix defines no FIONREAD for darwin; this is <sys/filio.h>'s _IOR('f', 127, int).
const fionread = 0x4004667f
