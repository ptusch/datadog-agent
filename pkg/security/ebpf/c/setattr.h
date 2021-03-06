#ifndef _SETATTR_H_
#define _SETATTR_H_

#include "syscalls.h"

SEC("kprobe/security_inode_setattr")
int kprobe__security_inode_setattr(struct pt_regs *ctx) {
    struct syscall_cache_t *syscall = peek_syscall();
    if (!syscall)
        return 0;

    struct iattr *iattr = (struct iattr *)PT_REGS_PARM2(ctx);
    if (iattr != NULL) {
        int valid;
        bpf_probe_read(&valid, sizeof(valid), &iattr->ia_valid);
        if (valid & ATTR_GID) {
            bpf_probe_read(&syscall->setattr.group, sizeof(syscall->setattr.group), &iattr->ia_gid);
        }

        if (valid & (ATTR_TOUCH | ATTR_ATIME_SET | ATTR_MTIME_SET)) {
            if (syscall->setattr.dentry)
                return 0;
            bpf_probe_read(&syscall->setattr.atime, sizeof(syscall->setattr.atime), &iattr->ia_atime);
            bpf_probe_read(&syscall->setattr.mtime, sizeof(syscall->setattr.mtime), &iattr->ia_mtime);
        }
    }

    if (syscall->type == EVENT_UTIME || syscall->type == EVENT_CHMOD || syscall->type == EVENT_CHOWN) {
        if (syscall->setattr.dentry)
            return 0;
        syscall->setattr.dentry = (struct dentry *)PT_REGS_PARM1(ctx);
        syscall->setattr.path_key.ino = get_dentry_ino(syscall->setattr.dentry);
        // the mount id of path_key is resolved by kprobe/mnt_want_write. It is already set by the time we reach this probe.
        resolve_dentry(syscall->setattr.dentry, syscall->setattr.path_key, NULL);
    }

    return 0;
}

#endif
