# runtime/linux_amd64/sys.s — zero-libc, zero-GC Linux x86_64 runtime core.
# Entry point (_start), error sentinels, bump scratch arena, string/byte
# builtins, Stdio, Env, Clock, FS, and Exec directly over Linux syscalls.
#
# Calling convention for runtime helpers (_fc_rt_*):
#   System V integer args: %rdi, %rsi, %rdx, %rcx, %r8, %r9
#   Returns: %rax (and %rdx, %rcx when returning 2-3 scalar values)
#   Callee-saved: %rbx, %rbp, %r12-%r15

.text

# ---------------------------------------------------------------------------
# Linux x86_64 syscall numbers
.equ SYS_read,         0
.equ SYS_write,        1
.equ SYS_close,        3
.equ SYS_poll,         7
.equ SYS_mmap,         9
.equ SYS_munmap,       11
.equ SYS_rt_sigaction, 13
.equ SYS_rt_sigreturn, 15
.equ SYS_ioctl,        16
.equ SYS_pipe,         22
.equ SYS_dup2,         33
.equ SYS_getpid,       39
.equ SYS_clone,        56
.equ SYS_fork,         57
.equ SYS_execve,       59
.equ SYS_exit,         60
.equ SYS_wait4,        61
.equ SYS_chdir,        80
.equ SYS_arch_prctl,   158
.equ SYS_getdents64,   217
.equ ARCH_SET_FS,      0x1002
.equ SYS_clock_gettime,228
.equ SYS_exit_group,   231
.equ SYS_openat,       257
.equ SYS_mkdirat,      258
.equ SYS_newfstatat,   262
.equ SYS_unlinkat,     263
.equ SYS_pipe2,        293

# Error sentinel IDs (must match bootstrap/internal/codegen/amd64)
.equ ERR_NIL,       0
.equ ERR_EOF,       1
.equ ERR_TIMEOUT,   2
.equ ERR_CANCELLED, 3
.equ ERR_ZEROWRITE, 4
.equ ERR_CLOSED,    5
.equ ERR_IO,        6
.equ ERR_NOTFOUND,  7
.equ ERR_MAXDEPTH,  8
.equ ERR_ARENAFULL, 9
.equ ERR_EXEC,      10
.equ ERR_PARSE,     11

.equ AT_FDCWD,      -100
.equ AT_REMOVEDIR,  0x200
.equ O_RDONLY,      0
.equ O_WRONLY,      1
.equ O_CREAT,       0x40
.equ O_TRUNC,       0x200
.equ O_DIRECTORY,   0x10000
.equ O_CLOEXEC,     0x80000
.equ O_NONBLOCK,    0x800
.equ EINTR,         4
.equ ENOENT,        2
.equ ENOTDIR,       20
.equ CLOCK_REALTIME,  0
.equ CLOCK_MONOTONIC, 1
.equ TCGETS,          0x5401
.equ TCSETS,          0x5402
.equ TIOCGWINSZ,      0x5413
.equ POLLIN_STDIN,    1

# ---------------------------------------------------------------------------
# Entry point: kernel enters _start with:
#   (%rsp)    = argc
#   8(%rsp)   = argv[0] ... NULL
#   ...       = envp[0] ... NULL
.weak _start
_start:
    xorq    %rbp, %rbp
    movq    (%rsp), %rax
    movq    %rax, _fc_rt_argc(%rip)
    leaq    8(%rsp), %rbx
    movq    %rbx, _fc_rt_argv(%rip)
    # envp = argv + (argc + 1) * 8
    leaq    8(%rbx, %rax, 8), %rcx
    movq    %rcx, _fc_rt_envp(%rip)
    # Initialize %fs base to _fc_main_tls (cur_fiber = 0) so %fs:0 is always safe
    movq    $SYS_arch_prctl, %rax
    movq    $ARCH_SET_FS, %rdi
    leaq    _fc_main_tls(%rip), %rsi
    syscall
    # Align stack to 16 bytes before calling _fc_entry
    andq    $-16, %rsp
    call    _fc_entry
    # %rax holds error sentinel (0 = nil)
    testq   %rax, %rax
    jz      .L_exit_ok
    cmpq    $0, _fc_rt_stderr_written(%rip)
    jne     .L_exit_err_quiet
    movq    %rax, %rdi
    call    _fc_rt_print_err
.L_exit_err_quiet:
    movq    $1, %rdi
    movq    $SYS_exit_group, %rax
    syscall
.L_exit_ok:
    xorq    %rdi, %rdi
    movq    $SYS_exit_group, %rax
    syscall

# _fc_rt_print_err(%rdi = err_id)
.global _fc_rt_print_err
_fc_rt_print_err:
    pushq   %rbp
    movq    %rsp, %rbp
    pushq   %rbx
    movq    %rdi, %rbx
    # Write "error: "
    movq    $2, %rdi
    leaq    .L_err_prefix(%rip), %rsi
    movq    $7, %rdx
    call    _fc_rt_write_all
    # Lookup message
    cmpq    $11, %rbx
    ja      .L_err_unknown
    leaq    .L_err_table(%rip), %rax
    shlq    $4, %rbx
    movq    (%rax, %rbx), %rsi
    movq    8(%rax, %rbx), %rdx
    jmp     .L_err_write
.L_err_unknown:
    leaq    .L_err_msg_unknown(%rip), %rsi
    movq    $15, %rdx
.L_err_write:
    movq    $2, %rdi
    call    _fc_rt_write_all
    movq    $2, %rdi
    leaq    .L_nl(%rip), %rsi
    movq    $1, %rdx
    call    _fc_rt_write_all
    popq    %rbx
    popq    %rbp
    ret

# ---------------------------------------------------------------------------
# Panic traps
.global _fc_rt_panic_bounds
_fc_rt_panic_bounds:
    movq    $2, %rdi
    leaq    .L_panic_bounds(%rip), %rsi
    movq    $31, %rdx
    call    _fc_rt_write_all
    movq    $2, %rdi
    movq    $SYS_exit_group, %rax
    syscall

.global _fc_rt_panic_div0
_fc_rt_panic_div0:
    movq    $2, %rdi
    leaq    .L_panic_div0(%rip), %rsi
    movq    $28, %rdx
    call    _fc_rt_write_all
    movq    $2, %rdi
    movq    $SYS_exit_group, %rax
    syscall

.global _fc_rt_panic
# _fc_rt_panic(%rdi = msg_ptr, %rsi = msg_len)
_fc_rt_panic:
    pushq   %rdi
    pushq   %rsi
    movq    $2, %rdi
    leaq    .L_panic_prefix(%rip), %rsi
    movq    $7, %rdx
    call    _fc_rt_write_all
    popq    %rdx
    popq    %rsi
    movq    $2, %rdi
    call    _fc_rt_write_all
    movq    $2, %rdi
    leaq    .L_nl(%rip), %rsi
    movq    $1, %rdx
    call    _fc_rt_write_all
    movq    $2, %rdi
    movq    $SYS_exit_group, %rax
    syscall

# ---------------------------------------------------------------------------
# Bounded static bump arena (1 MiB in .bss, lazily paged, thread-safe)
# _fc_rt_bump_alloc(%rdi = nbytes) -> %rax = ptr
.equ STR_ARENA_CAP, (1024 * 1024)
.global _fc_rt_bump_alloc
_fc_rt_bump_alloc:
    testq   %rdi, %rdi
    jnz     .L_ba_nonzero
    leaq    _fc_str_arena(%rip), %rax
    ret
.L_ba_nonzero:
    # Align size up to 8 bytes
    movq    %rdi, %rcx
    addq    $7, %rcx
    andq    $-8, %rcx
    movq    %rcx, %rax
    lock xaddq %rax, _fc_str_arena_pos(%rip)
    movq    %rax, %rdx
    addq    %rcx, %rdx
    cmpq    $STR_ARENA_CAP, %rdx
    ja      .L_ba_full
    leaq    _fc_str_arena(%rip), %rcx
    addq    %rcx, %rax
    ret
.L_ba_full:
    leaq    .L_panic_arena(%rip), %rdi
    movq    $31, %rsi
    call    _fc_rt_panic

# ---------------------------------------------------------------------------
# Memory / slice / string builtins

# _fc_rt_memcpy(%rdi = dst, %rsi = src, %rdx = n)
.global _fc_rt_memcpy
_fc_rt_memcpy:
    testq   %rdx, %rdx
    jz      .L_mc_done
    movq    %rdx, %rcx
    cld
    rep movsb
.L_mc_done:
    ret

# _fc_rt_memmove(%rdi = dst, %rsi = src, %rdx = n) -> safe for overlap (copy builtin)
.global _fc_rt_memmove
_fc_rt_memmove:
    testq   %rdx, %rdx
    jz      .L_mm_done
    cmpq    %rsi, %rdi
    je      .L_mm_done
    jb      .L_mm_fwd
    # Check overlap: dst > src && dst < src + n
    movq    %rsi, %rax
    addq    %rdx, %rax
    cmpq    %rax, %rdi
    jae     .L_mm_fwd
    # Copy backwards
    leaq    -1(%rdi, %rdx), %rdi
    leaq    -1(%rsi, %rdx), %rsi
    movq    %rdx, %rcx
    std
    rep movsb
    cld
    ret
.L_mm_fwd:
    movq    %rdx, %rcx
    cld
    rep movsb
.L_mm_done:
    ret

# _fc_rt_str_bytes(%rdi = ptr, %rsi = len) -> (%rax = copy_ptr, %rdx = len)
.global _fc_rt_str_bytes
_fc_rt_str_bytes:
    testq   %rsi, %rsi
    jnz     .L_sb_copy
    xorq    %rax, %rax
    xorq    %rdx, %rdx
    ret
.L_sb_copy:
    pushq   %rbx
    pushq   %r12
    movq    %rdi, %rbx
    movq    %rsi, %r12
    movq    %r12, %rdi
    call    _fc_rt_bump_alloc
    movq    %rax, %rdi
    movq    %rbx, %rsi
    movq    %r12, %rdx
    pushq   %rax
    call    _fc_rt_memcpy
    popq    %rax
    movq    %r12, %rdx
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_bytes_to_str(%rdi = ptr, %rsi = len) -> (%rax = copy_ptr, %rdx = len)
.global _fc_rt_bytes_to_str
_fc_rt_bytes_to_str:
    jmp     _fc_rt_str_bytes

# _fc_rt_bytes_eq(%rdi = p1, %rsi = l1, %rdx = p2, %rcx = l2) -> %rax (0/1)
.global _fc_rt_bytes_eq
_fc_rt_bytes_eq:
    cmpq    %rcx, %rsi
    jne     .L_beq_false
    testq   %rsi, %rsi
    jz      .L_beq_true
    xorq    %r8, %r8
.L_beq_loop:
    movzbl  (%rdi, %r8), %eax
    movzbl  (%rdx, %r8), %r9d
    cmpb    %r9b, %al
    jne     .L_beq_false
    incq    %r8
    cmpq    %rsi, %r8
    jb      .L_beq_loop
.L_beq_true:
    movq    $1, %rax
    ret
.L_beq_false:
    xorq    %rax, %rax
    ret

# _fc_rt_str_cmp(%rdi = p1, %rsi = l1, %rdx = p2, %rcx = l2) -> %rax (-1, 0, +1)
.global _fc_rt_str_cmp
_fc_rt_str_cmp:
    movq    %rsi, %r8
    cmpq    %rcx, %r8
    jbe     .L_sc_min_ok
    movq    %rcx, %r8
.L_sc_min_ok:
    xorq    %r9, %r9
.L_sc_loop:
    cmpq    %r8, %r9
    jae     .L_sc_lens
    movzbl  (%rdi, %r9), %eax
    movzbl  (%rdx, %r9), %r10d
    cmpb    %r10b, %al
    jb      .L_sc_lt
    ja      .L_sc_gt
    incq    %r9
    jmp     .L_sc_loop
.L_sc_lens:
    cmpq    %rcx, %rsi
    jb      .L_sc_lt
    ja      .L_sc_gt
    xorq    %rax, %rax
    ret
.L_sc_lt:
    movq    $-1, %rax
    ret
.L_sc_gt:
    movq    $1, %rax
    ret

# _fc_rt_format_uint(%rdi = val, %rsi = dst_ptr, %rdx = dst_len) -> (%rax = n, %rdx = err)
.global _fc_rt_format_uint
_fc_rt_format_uint:
    pushq   %rbp
    movq    %rsp, %rbp
    subq    $32, %rsp
    movq    %rdx, %r10        # r10 = dst_len
    movq    %rdi, %rax        # rax = val
    leaq    24(%rsp), %r8
    movq    %r8, %r9
    movq    $10, %rcx
.L_fu2_loop:
    xorq    %rdx, %rdx
    divq    %rcx
    addb    $'0', %dl
    decq    %r9
    movb    %dl, (%r9)
    testq   %rax, %rax
    jnz     .L_fu2_loop
    subq    %r9, %r8          # r8 = digit count
    cmpq    %r10, %r8
    ja      .L_fu2_full
    xorq    %rcx, %rcx
.L_fu2_cp:
    movb    (%r9, %rcx), %al
    movb    %al, (%rsi, %rcx)
    incq    %rcx
    cmpq    %r8, %rcx
    jb      .L_fu2_cp
    movq    %r8, %rax
    xorq    %rdx, %rdx
    movq    %rbp, %rsp
    popq    %rbp
    ret
.L_fu2_full:
    xorq    %rax, %rax
    movq    $ERR_ARENAFULL, %rdx
    movq    %rbp, %rsp
    popq    %rbp
    ret

# _fc_rt_parse_uint(%rdi = src_ptr, %rsi = src_len) -> (%rax = val, %rdx = err)
.global _fc_rt_parse_uint
_fc_rt_parse_uint:
    testq   %rsi, %rsi
    jz      .L_pu_err
    xorq    %rax, %rax        # val = 0
    xorq    %rcx, %rcx        # i = 0
    movq    $10, %r9
.L_pu_loop:
    cmpq    %rsi, %rcx
    jae     .L_pu_ok
    movzbl  (%rdi, %rcx), %r8d
    subl    $'0', %r8d
    cmpl    $9, %r8d
    ja      .L_pu_err
    # Check overflow: val * 10 + d
    mulq    %r9               # rdx:rax = rax * 10
    testq   %rdx, %rdx
    jnz     .L_pu_err
    addq    %r8, %rax
    jc      .L_pu_err
    incq    %rcx
    jmp     .L_pu_loop
.L_pu_ok:
    xorq    %rdx, %rdx
    ret
.L_pu_err:
    xorq    %rax, %rax
    movq    $ERR_PARSE, %rdx
    ret

# ---------------------------------------------------------------------------
# Stdio

# _fc_rt_write_all(%rdi = fd, %rsi = ptr, %rdx = len) -> %rax = err
.global _fc_rt_write_all
_fc_rt_write_all:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    movq    %rdi, %rbx        # fd
    movq    %rsi, %r12        # ptr
    movq    %rdx, %r13        # rem
.L_wa_loop:
    testq   %r13, %r13
    jz      .L_wa_ok
    movq    $SYS_write, %rax
    movq    %rbx, %rdi
    movq    %r12, %rsi
    movq    %r13, %rdx
    syscall
    cmpq    $-EINTR, %rax
    je      .L_wa_loop
    testq   %rax, %rax
    jle     .L_wa_err
    addq    %rax, %r12
    subq    %rax, %r13
    jmp     .L_wa_loop
.L_wa_ok:
    xorq    %rax, %rax
    popq    %r13
    popq    %r12
    popq    %rbx
    ret
.L_wa_err:
    movq    $ERR_IO, %rax
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_write_stdout(%rdi = ptr, %rsi = len) -> %rax = err
.global _fc_rt_write_stdout
_fc_rt_write_stdout:
    movq    %rsi, %rdx
    movq    %rdi, %rsi
    movq    $1, %rdi
    jmp     _fc_rt_write_all

# _fc_rt_write_stderr(%rdi = ptr, %rsi = len) -> %rax = err
.global _fc_rt_write_stderr
_fc_rt_write_stderr:
    movq    $1, _fc_rt_stderr_written(%rip)
    movq    %rsi, %rdx
    movq    %rdi, %rsi
    movq    $2, %rdi
    jmp     _fc_rt_write_all

# _fc_rt_read_stdin(%rdi = dst_ptr, %rsi = dst_len) -> (%rax = n, %rdx = err)
.global _fc_rt_read_stdin
_fc_rt_read_stdin:
    pushq   %rbx
    pushq   %r12
    movq    %rdi, %rbx
    movq    %rsi, %r12
.L_rs_retry:
    movq    $SYS_read, %rax
    xorq    %rdi, %rdi
    movq    %rbx, %rsi
    movq    %r12, %rdx
    syscall
    cmpq    $-EINTR, %rax
    je      .L_rs_retry
    testq   %rax, %rax
    jl      .L_rs_err
    jz      .L_rs_eof
    xorq    %rdx, %rdx
    popq    %r12
    popq    %rbx
    ret
.L_rs_eof:
    xorq    %rax, %rax
    movq    $ERR_EOF, %rdx
    popq    %r12
    popq    %rbx
    ret
.L_rs_err:
    xorq    %rax, %rax
    movq    $ERR_IO, %rdx
    popq    %r12
    popq    %rbx
    ret

# _fc_rt_term_raw() -> %rax = err
.global _fc_rt_term_raw
_fc_rt_term_raw:
    subq    $72, %rsp
    movq    $SYS_ioctl, %rax
    xorq    %rdi, %rdi
    movq    $TCGETS, %rsi
    leaq    _fc_term_orig(%rip), %rdx
    syscall
    testq   %rax, %rax
    jl      .L_tr_err
    movq    $1, _fc_term_saved(%rip)
    # Copy 64 bytes from _fc_term_orig to stack raw copy
    leaq    _fc_term_orig(%rip), %rsi
    movq    %rsp, %rdi
    movq    $8, %rcx
    cld
    rep     movsq
    # c_iflag &= ~(BRKINT|INPCK|ISTRIP|ICRNL|IXON) = ~0x532
    andl    $~0x532, 0(%rsp)
    # c_lflag &= ~(ISIG|ICANON|ECHO|IEXTEN) = ~0x800b
    andl    $~0x800b, 12(%rsp)
    # c_cc[VTIME]=0 (off 22), c_cc[VMIN]=1 (off 23)
    movb    $0, 22(%rsp)
    movb    $1, 23(%rsp)
    movq    $SYS_ioctl, %rax
    xorq    %rdi, %rdi
    movq    $TCSETS, %rsi
    movq    %rsp, %rdx
    syscall
    testq   %rax, %rax
    jl      .L_tr_err
    xorq    %rax, %rax
    addq    $72, %rsp
    ret
.L_tr_err:
    movq    $ERR_IO, %rax
    addq    $72, %rsp
    ret

# _fc_rt_term_restore() -> %rax = err
.global _fc_rt_term_restore
_fc_rt_term_restore:
    cmpq    $0, _fc_term_saved(%rip)
    je      .L_trest_ok
    movq    $SYS_ioctl, %rax
    xorq    %rdi, %rdi
    movq    $TCSETS, %rsi
    leaq    _fc_term_orig(%rip), %rdx
    syscall
    movq    $0, _fc_term_saved(%rip)
.L_trest_ok:
    xorq    %rax, %rax
    ret

# _fc_rt_term_size() -> (%rax = cols, %rdx = rows, %rcx = ok)
.global _fc_rt_term_size
_fc_rt_term_size:
    subq    $16, %rsp
    movq    $0, 0(%rsp)
    movq    $SYS_ioctl, %rax
    movq    $1, %rdi
    movq    $TIOCGWINSZ, %rsi
    movq    %rsp, %rdx
    syscall
    testq   %rax, %rax
    jl      .L_ts_fail
    movzwq  2(%rsp), %rax     # ws_col
    movzwq  0(%rsp), %rdx     # ws_row
    testq   %rax, %rax
    jz      .L_ts_fail
    testq   %rdx, %rdx
    jz      .L_ts_fail
    movq    $1, %rcx
    addq    $16, %rsp
    ret
.L_ts_fail:
    xorq    %rax, %rax
    xorq    %rdx, %rdx
    xorq    %rcx, %rcx
    addq    $16, %rsp
    ret

# _fc_rt_read_stdin_timeout(%rdi = dst_ptr, %rsi = dst_len, %rdx = timeout_ms) -> (%rax = n, %rdx = err)
.global _fc_rt_read_stdin_timeout
_fc_rt_read_stdin_timeout:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    subq    $16, %rsp
    movq    %rdi, %rbx
    movq    %rsi, %r12
    movq    %rdx, %r13
.L_rst_poll:
    # struct pollfd { int fd=0; short events=POLLIN; short revents=0; }
    movl    $0, 0(%rsp)
    movw    $POLLIN_STDIN, 4(%rsp)
    movw    $0, 6(%rsp)
    movq    $SYS_poll, %rax
    movq    %rsp, %rdi
    movq    $1, %rsi
    movq    %r13, %rdx
    syscall
    cmpq    $-EINTR, %rax
    je      .L_rst_poll
    testq   %rax, %rax
    jl      .L_rst_io
    jz      .L_rst_timeout
.L_rst_read:
    movq    $SYS_read, %rax
    xorq    %rdi, %rdi
    movq    %rbx, %rsi
    movq    %r12, %rdx
    syscall
    cmpq    $-EINTR, %rax
    je      .L_rst_read
    testq   %rax, %rax
    jl      .L_rst_io
    jz      .L_rst_eof
    xorq    %rdx, %rdx
    addq    $16, %rsp
    popq    %r13
    popq    %r12
    popq    %rbx
    ret
.L_rst_timeout:
    xorq    %rax, %rax
    movq    $ERR_TIMEOUT, %rdx
    addq    $16, %rsp
    popq    %r13
    popq    %r12
    popq    %rbx
    ret
.L_rst_eof:
    xorq    %rax, %rax
    movq    $ERR_EOF, %rdx
    addq    $16, %rsp
    popq    %r13
    popq    %r12
    popq    %rbx
    ret
.L_rst_io:
    xorq    %rax, %rax
    movq    $ERR_IO, %rdx
    addq    $16, %rsp
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# ---------------------------------------------------------------------------
# Env

# _fc_rt_strlen(%rdi = cstr) -> %rax
.global _fc_rt_strlen
_fc_rt_strlen:
    xorq    %rax, %rax
.L_sl_loop:
    cmpb    $0, (%rdi, %rax)
    je      .L_sl_done
    incq    %rax
    jmp     .L_sl_loop
.L_sl_done:
    ret

# _fc_rt_args_count() -> %rax
.global _fc_rt_args_count
_fc_rt_args_count:
    movq    _fc_rt_argc(%rip), %rax
    ret

# _fc_rt_arg(%rdi = idx) -> (%rax = ptr, %rdx = len)
.global _fc_rt_arg
_fc_rt_arg:
    cmpq    _fc_rt_argc(%rip), %rdi
    jae     .L_arg_empty
    movq    _fc_rt_argv(%rip), %rax
    movq    (%rax, %rdi, 8), %rdi
    pushq   %rdi
    call    _fc_rt_strlen
    movq    %rax, %rdx
    popq    %rax
    ret
.L_arg_empty:
    xorq    %rax, %rax
    xorq    %rdx, %rdx
    ret

# _fc_rt_getenv(%rdi = key_ptr, %rsi = key_len) -> (%rax = ptr, %rdx = len, %rcx = ok)
.global _fc_rt_getenv
_fc_rt_getenv:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    movq    %rdi, %rbx        # key_ptr
    movq    %rsi, %r12        # key_len
    movq    _fc_rt_envp(%rip), %r13
.L_ge_next:
    movq    (%r13), %rdi
    testq   %rdi, %rdi
    jz      .L_ge_none
    # Compare key_len bytes then '='
    xorq    %rcx, %rcx
.L_ge_cmp:
    cmpq    %r12, %rcx
    jae     .L_ge_eq
    movb    (%rdi, %rcx), %al
    cmpb    (%rbx, %rcx), %al
    jne     .L_ge_step
    incq    %rcx
    jmp     .L_ge_cmp
.L_ge_eq:
    cmpb    $'=', (%rdi, %r12)
    jne     .L_ge_step
    leaq    1(%rdi, %r12), %rdi
    pushq   %rdi
    call    _fc_rt_strlen
    movq    %rax, %rdx
    popq    %rax
    movq    $1, %rcx
    popq    %r13
    popq    %r12
    popq    %rbx
    ret
.L_ge_step:
    addq    $8, %r13
    jmp     .L_ge_next
.L_ge_none:
    xorq    %rax, %rax
    xorq    %rdx, %rdx
    xorq    %rcx, %rcx
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# ---------------------------------------------------------------------------
# Clock

# _fc_rt_clock_now() -> %rax (monotonic ns)
.global _fc_rt_clock_now
_fc_rt_clock_now:
    subq    $24, %rsp
    movq    $SYS_clock_gettime, %rax
    movq    $CLOCK_MONOTONIC, %rdi
    movq    %rsp, %rsi
    syscall
    movq    (%rsp), %rax      # tv_sec
    imulq   $1000000000, %rax, %rax
    addq    8(%rsp), %rax     # tv_nsec
    addq    $24, %rsp
    ret

# _fc_rt_clock_unix_ns() -> %rax (wall-clock CLOCK_REALTIME ns since 1970-01-01T00:00:00Z)
.global _fc_rt_clock_unix_ns
_fc_rt_clock_unix_ns:
    subq    $24, %rsp
    movq    $SYS_clock_gettime, %rax
    movq    $CLOCK_REALTIME, %rdi
    movq    %rsp, %rsi
    syscall
    movq    (%rsp), %rax      # tv_sec
    imulq   $1000000000, %rax, %rax
    addq    8(%rsp), %rax     # tv_nsec
    addq    $24, %rsp
    ret

# _fc_rt_clock_sleep(%rdi = ctx_state, %rsi = ctx_deadline, %rdx = dur_ns) -> %rax = err
.weak _fc_rt_fiber_sleep
.global _fc_rt_clock_sleep
_fc_rt_clock_sleep:
    cmpq    $0, _fc_fiber_runtime_active(%rip)
    je      .L_cs_os_thread
    movq    %fs:0, %rax
    testq   %rax, %rax
    jz      .L_cs_os_thread
    leaq    _fc_rt_fiber_sleep(%rip), %rax
    testq   %rax, %rax
    jz      .L_cs_os_thread
    jmp     *%rax
.L_cs_os_thread:
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    subq    $16, %rsp
    movq    %rdi, %rbx        # ctx_state
    movq    %rsi, %r12        # ctx_deadline
    movq    %rdx, %r13        # dur_ns
    # Check ctx err first
    movq    %rbx, %rdi
    movq    %r12, %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_cs_ret
    # Compute target = now + dur_ns
    call    _fc_rt_clock_now
    addq    %r13, %rax
    movq    %rax, %r13        # r13 = sleep target ns
.L_cs_loop:
    movq    %rbx, %rdi
    movq    %r12, %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_cs_ret
    call    _fc_rt_clock_now
    cmpq    %r13, %rax
    jae     .L_cs_ok
    # rem_ns = min(r13, ctx_deadline if nonzero) - now
    movq    %r13, %rcx
    testq   %r12, %r12
    jz      .L_cs_no_dl
    cmpq    %r12, %rcx
    jbe     .L_cs_no_dl
    movq    %r12, %rcx
.L_cs_no_dl:
    subq    %rax, %rcx
    jle     .L_cs_ok
    # timeout_ms = (rcx + 999999) / 1000000
    leaq    999999(%rcx), %rax
    xorq    %rdx, %rdx
    movq    $1000000, %r8
    divq    %r8
    movq    %rax, %rdx        # poll timeout_ms
    # pollfd on cancel rfd if ctx_state != 0
    testq   %rbx, %rbx
    jz      .L_cs_poll_none
    movl    4(%rbx), %eax     # rfd
    movl    %eax, (%rsp)
    movw    $1, 4(%rsp)       # POLLIN
    movw    $0, 6(%rsp)
    movq    $SYS_poll, %rax
    movq    %rsp, %rdi
    movq    $1, %rsi
    syscall
    jmp     .L_cs_loop
.L_cs_poll_none:
    movq    $SYS_poll, %rax
    xorq    %rdi, %rdi
    xorq    %rsi, %rsi
    syscall
    jmp     .L_cs_loop
.L_cs_ok:
    xorq    %rax, %rax
.L_cs_ret:
    addq    $16, %rsp
    popq    %r13
    popq    %r12
    popq    %rbx
    ret

# ---------------------------------------------------------------------------
# FS helpers (NUL-terminating path on stack: max 4096 bytes)

# _fc_rt_fs_read_file_into(%rdi = path_ptr, %rsi = path_len, %rdx = dst_ptr, %rcx = dst_len)
# -> (%rax = n, %rdx = err)
.global _fc_rt_fs_read_file_into
_fc_rt_fs_read_file_into:
    pushq   %rbp
    movq    %rsp, %rbp
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    subq    $4112, %rsp
    movq    %rdx, %r12        # dst_ptr
    movq    %rcx, %r13        # dst_len
    cmpq    $4095, %rsi
    ja      .L_fr_err_io
    leaq    16(%rsp), %rbx    # cpath
    # copy path_ptr(%rdi) len(%rsi) -> cpath(%rbx)
    movq    %rsi, %rcx
    movq    %rdi, %rsi
    movq    %rbx, %rdi
    cld
    rep movsb
    movb    $0, (%rdi)
    # openat(AT_FDCWD, cpath, O_RDONLY|O_CLOEXEC, 0)
    movq    $SYS_openat, %rax
    movq    $AT_FDCWD, %rdi
    movq    %rbx, %rsi
    movq    $(O_RDONLY | O_CLOEXEC), %rdx
    xorq    %r10, %r10
    syscall
    testq   %rax, %rax
    jl      .L_fr_open_err
    movq    %rax, %r14        # fd
    xorq    %rbx, %rbx        # total read = 0
.L_fr_loop:
    movq    %r13, %rdx
    subq    %rbx, %rdx        # rem = dst_len - total
    jz      .L_fr_check_eof
    movq    $SYS_read, %rax
    movq    %r14, %rdi
    leaq    (%r12, %rbx), %rsi
    syscall
    cmpq    $-EINTR, %rax
    je      .L_fr_loop
    testq   %rax, %rax
    jl      .L_fr_read_err
    jz      .L_fr_done_ok
    addq    %rax, %rbx
    jmp     .L_fr_loop
.L_fr_check_eof:
    movq    $SYS_read, %rax
    movq    %r14, %rdi
    leaq    8(%rsp), %rsi
    movq    $1, %rdx
    syscall
    cmpq    $-EINTR, %rax
    je      .L_fr_check_eof
    testq   %rax, %rax
    jl      .L_fr_read_err
    jg      .L_fr_full
.L_fr_done_ok:
    movq    $SYS_close, %rax
    movq    %r14, %rdi
    syscall
    movq    %rbx, %rax
    xorq    %rdx, %rdx
    jmp     .L_fr_ret
.L_fr_full:
    movq    $SYS_close, %rax
    movq    %r14, %rdi
    syscall
    movq    %rbx, %rax
    movq    $ERR_ARENAFULL, %rdx
    jmp     .L_fr_ret
.L_fr_read_err:
    movq    $SYS_close, %rax
    movq    %r14, %rdi
    syscall
    movq    %rbx, %rax
    movq    $ERR_IO, %rdx
    jmp     .L_fr_ret
.L_fr_open_err:
    cmpq    $-ENOENT, %rax
    je      .L_fr_notfound
.L_fr_err_io:
    xorq    %rax, %rax
    movq    $ERR_IO, %rdx
    jmp     .L_fr_ret
.L_fr_notfound:
    xorq    %rax, %rax
    movq    $ERR_NOTFOUND, %rdx
.L_fr_ret:
    addq    $4112, %rsp
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    popq    %rbp
    ret

# _fc_rt_fs_write_file(%rdi = path_ptr, %rsi = path_len, %rdx = data_ptr, %rcx = data_len) -> %rax = err
.global _fc_rt_fs_write_file
_fc_rt_fs_write_file:
    pushq   %rbp
    movq    %rsp, %rbp
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    subq    $4112, %rsp
    movq    %rdx, %r12
    movq    %rcx, %r13
    cmpq    $4095, %rsi
    ja      .L_fw_err_io
    leaq    16(%rsp), %rbx
    movq    %rsi, %rcx
    movq    %rdi, %rsi
    movq    %rbx, %rdi
    cld
    rep movsb
    movb    $0, (%rdi)
    movq    $SYS_openat, %rax
    movq    $AT_FDCWD, %rdi
    movq    %rbx, %rsi
    movq    $(O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC), %rdx
    movq    $0644, %r10
    syscall
    testq   %rax, %rax
    jl      .L_fw_open_err
    movq    %rax, %r14
    movq    %r14, %rdi
    movq    %r12, %rsi
    movq    %r13, %rdx
    call    _fc_rt_write_all
    movq    %rax, %rbx
    movq    $SYS_close, %rax
    movq    %r14, %rdi
    syscall
    movq    %rbx, %rax
    jmp     .L_fw_ret
.L_fw_open_err:
    cmpq    $-ENOENT, %rax
    je      .L_fw_notfound
.L_fw_err_io:
    movq    $ERR_IO, %rax
    jmp     .L_fw_ret
.L_fw_notfound:
    movq    $ERR_NOTFOUND, %rax
.L_fw_ret:
    addq    $4112, %rsp
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    popq    %rbp
    ret

# _fc_rt_fs_is_dir(%rdi = path_ptr, %rsi = path_len) -> %rax (0/1)
.global _fc_rt_fs_is_dir
_fc_rt_fs_is_dir:
    pushq   %rbp
    movq    %rsp, %rbp
    pushq   %rbx
    subq    $4256, %rsp       # cpath[4096] + statbuf[160]
    cmpq    $4095, %rsi
    ja      .L_id_no
    leaq    160(%rsp), %rbx
    movq    %rsi, %rcx
    movq    %rdi, %rsi
    movq    %rbx, %rdi
    cld
    rep movsb
    movb    $0, (%rdi)
    movq    $SYS_newfstatat, %rax
    movq    $AT_FDCWD, %rdi
    movq    %rbx, %rsi
    movq    %rsp, %rdx        # struct stat
    xorq    %r10, %r10
    syscall
    testq   %rax, %rax
    jl      .L_id_no
    # Linux x86_64 struct stat: st_mode is uint32 at offset 24
    movl    24(%rsp), %eax
    andl    $0170000, %eax
    cmpl    $0040000, %eax
    jne     .L_id_no
    movq    $1, %rax
    jmp     .L_id_ret
.L_id_no:
    xorq    %rax, %rax
.L_id_ret:
    addq    $4256, %rsp
    popq    %rbx
    popq    %rbp
    ret

# _fc_rt_fs_mkdir_temp(%rdi = pref_ptr, %rsi = pref_len) -> (%rax = ptr, %rdx = len, %rcx = err)
.global _fc_rt_fs_mkdir_temp
_fc_rt_fs_mkdir_temp:
    pushq   %rbp
    movq    %rsp, %rbp
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    subq    $256, %rsp
    movq    %rdi, %r12
    movq    %rsi, %r13
    # Build "/tmp/<pref><pid>_<seq>" on stack
    movq    %rsp, %rbx
    movb    $'/', 0(%rbx)
    movb    $'t', 1(%rbx)
    movb    $'m', 2(%rbx)
    movb    $'p', 3(%rbx)
    movb    $'/', 4(%rbx)
    addq    $5, %rbx
    cmpq    $180, %r13
    ja      .L_mt_err
    movq    %r13, %rcx
    movq    %r12, %rsi
    movq    %rbx, %rdi
    cld
    rep movsb
    movq    %rdi, %rbx
    # Append pid
    movq    $SYS_getpid, %rax
    syscall
    movq    %rax, %rdi
    movq    %rbx, %rsi
    movq    $24, %rdx
    call    _fc_rt_format_uint
    addq    %rax, %rbx
    movb    $'_', (%rbx)
    incq    %rbx
    movq    $1, %rax
    lock xaddq %rax, _fc_tmp_seq(%rip)
    movq    %rax, %rdi
    movq    %rbx, %rsi
    movq    $24, %rdx
    call    _fc_rt_format_uint
    addq    %rax, %rbx
    movb    $0, (%rbx)
    # mkdirat(AT_FDCWD, path, 0700)
    movq    $SYS_mkdirat, %rax
    movq    $AT_FDCWD, %rdi
    movq    %rsp, %rsi
    movq    $0700, %rdx
    syscall
    testq   %rax, %rax
    jl      .L_mt_err
    # Copy into bump arena and return string
    movq    %rbx, %rsi
    subq    %rsp, %rsi
    movq    %rsp, %rdi
    call    _fc_rt_str_bytes
    xorq    %rcx, %rcx
    jmp     .L_mt_ret
.L_mt_err:
    xorq    %rax, %rax
    xorq    %rdx, %rdx
    movq    $ERR_IO, %rcx
.L_mt_ret:
    addq    $256, %rsp
    popq    %r13
    popq    %r12
    popq    %rbx
    popq    %rbp
    ret

# _fc_rt_fs_remove_all(%rdi = path_ptr, %rsi = path_len) -> %rax = err
.global _fc_rt_fs_remove_all
_fc_rt_fs_remove_all:
    pushq   %rbp
    movq    %rsp, %rbp
    pushq   %rbx
    subq    $4112, %rsp
    cmpq    $4095, %rsi
    ja      .L_ra_err
    leaq    16(%rsp), %rbx
    movq    %rsi, %rcx
    movq    %rdi, %rsi
    movq    %rbx, %rdi
    cld
    rep movsb
    movb    $0, (%rdi)
    # Try unlinkat(file)
    movq    $SYS_unlinkat, %rax
    movq    $AT_FDCWD, %rdi
    movq    %rbx, %rsi
    xorq    %rdx, %rdx
    syscall
    testq   %rax, %rax
    jge     .L_ra_ok
    cmpq    $-ENOENT, %rax
    je      .L_ra_ok
    # Try unlinkat(AT_REMOVEDIR)
    movq    $SYS_unlinkat, %rax
    movq    $AT_FDCWD, %rdi
    movq    %rbx, %rsi
    movq    $AT_REMOVEDIR, %rdx
    syscall
    testq   %rax, %rax
    jge     .L_ra_ok
    cmpq    $-ENOENT, %rax
    je      .L_ra_ok
.L_ra_err:
    movq    $ERR_IO, %rax
    jmp     .L_ra_ret
.L_ra_ok:
    xorq    %rax, %rax
.L_ra_ret:
    addq    $4112, %rsp
    popq    %rbx
    popq    %rbp
    ret

# _fc_rt_fs_list_fc(%rdi = dir_ptr, %rsi = dir_len, %rdx = dst_ptr, %rcx = dst_len)
# -> (%rax = count, %rdx = err)
# dst_ptr points to an array of 16-byte strings ({ptr, len}).
.global _fc_rt_fs_list_fc
_fc_rt_fs_list_fc:
    pushq   %rbp
    movq    %rsp, %rbp
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    subq    $8208, %rsp       # cpath[4096] + dirents[4096]
    movq    %rdx, %r12        # dst_ptr
    movq    %rcx, %r13        # dst_len
    cmpq    $4095, %rsi
    ja      .L_lf_err_io
    leaq    4112(%rsp), %rbx
    movq    %rsi, %rcx
    movq    %rdi, %rsi
    movq    %rbx, %rdi
    cld
    rep movsb
    movb    $0, (%rdi)
    movq    $SYS_openat, %rax
    movq    $AT_FDCWD, %rdi
    movq    %rbx, %rsi
    movq    $(O_RDONLY | O_DIRECTORY | O_CLOEXEC), %rdx
    xorq    %r10, %r10
    syscall
    testq   %rax, %rax
    jl      .L_lf_open_err
    movq    %rax, %r14        # dirfd
    xorq    %r15, %r15        # count = 0
.L_lf_read:
    movq    $SYS_getdents64, %rax
    movq    %r14, %rdi
    movq    %rsp, %rsi
    movq    $4096, %rdx
    syscall
    testq   %rax, %rax
    jl      .L_lf_read_err
    jz      .L_lf_close_sort
    movq    %rax, %rbx        # bytes read
    xorq    %rcx, %rcx        # offset in dirents
.L_lf_ent:
    cmpq    %rbx, %rcx
    jae     .L_lf_read
    leaq    (%rsp, %rcx), %rdi
    movzwl  16(%rdi), %eax    # d_reclen
    movzbl  18(%rdi), %edx    # d_type
    pushq   %rax
    pushq   %rcx
    cmpb    $4, %dl           # DT_DIR == 4
    je      .L_lf_skip
    leaq    19(%rdi), %rdi    # d_name
    pushq   %rdi
    call    _fc_rt_strlen
    popq    %rdi
    cmpq    $3, %rax
    jb      .L_lf_skip
    # Check suffix ".fc"
    cmpb    $'.', -3(%rdi, %rax)
    jne     .L_lf_skip
    cmpb    $'f', -2(%rdi, %rax)
    jne     .L_lf_skip
    cmpb    $'c', -1(%rdi, %rax)
    jne     .L_lf_skip
    cmpq    %r13, %r15
    jae     .L_lf_full
    movq    %rax, %rsi
    call    _fc_rt_str_bytes  # copy name into bump arena -> (rax, rdx)
    movq    %r15, %rcx
    shlq    $4, %rcx
    movq    %rax, (%r12, %rcx)
    movq    %rdx, 8(%r12, %rcx)
    incq    %r15
.L_lf_skip:
    popq    %rcx
    popq    %rax
    addq    %rax, %rcx
    jmp     .L_lf_ent
.L_lf_full:
    popq    %rcx
    popq    %rax
    movq    $SYS_close, %rax
    movq    %r14, %rdi
    syscall
    movq    %r15, %rax
    movq    $ERR_ARENAFULL, %rdx
    jmp     .L_lf_ret
.L_lf_close_sort:
    movq    $SYS_close, %rax
    movq    %r14, %rdi
    syscall
    # Insertion sort r15 strings at r12
    movq    $1, %rbx
.L_lf_si:
    cmpq    %r15, %rbx
    jae     .L_lf_ok
    movq    %rbx, %r14
.L_lf_sj:
    testq   %r14, %r14
    jz      .L_lf_si_next
    movq    %r14, %rax
    decq    %rax
    shlq    $4, %rax
    movq    %r14, %rcx
    shlq    $4, %rcx
    movq    (%r12, %rax), %rdi
    movq    8(%r12, %rax), %rsi
    movq    (%r12, %rcx), %rdx
    movq    8(%r12, %rcx), %rcx
    pushq   %rax
    call    _fc_rt_str_cmp
    popq    %rcx              # prev byte off
    cmpq    $0, %rax
    jle     .L_lf_si_next
    # swap (r14-1) and r14
    movq    %r14, %rdx
    shlq    $4, %rdx
    movq    (%r12, %rcx), %rax
    movq    (%r12, %rdx), %rdi
    movq    %rdi, (%r12, %rcx)
    movq    %rax, (%r12, %rdx)
    movq    8(%r12, %rcx), %rax
    movq    8(%r12, %rdx), %rdi
    movq    %rdi, 8(%r12, %rcx)
    movq    %rax, 8(%r12, %rdx)
    decq    %r14
    jmp     .L_lf_sj
.L_lf_si_next:
    incq    %rbx
    jmp     .L_lf_si
.L_lf_ok:
    movq    %r15, %rax
    xorq    %rdx, %rdx
    jmp     .L_lf_ret
.L_lf_read_err:
    movq    $SYS_close, %rax
    movq    %r14, %rdi
    syscall
.L_lf_err_io:
    xorq    %rax, %rax
    movq    $ERR_IO, %rdx
    jmp     .L_lf_ret
.L_lf_open_err:
    cmpq    $-ENOENT, %rax
    je      .L_lf_nf
    jmp     .L_lf_err_io
.L_lf_nf:
    xorq    %rax, %rax
    movq    $ERR_NOTFOUND, %rdx
.L_lf_ret:
    addq    $8208, %rsp
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    popq    %rbp
    ret

# ---------------------------------------------------------------------------
# Exec.Run minimal syscall stub (fork + execve + stderr pipe + wait4)
# Takes pointer to arg struct in %rdi:
#   +0: ctx_state, +8: ctx_deadline
#   +16: bin_ptr, +24: bin_len
#   +32: args_ptr, +40: args_len (slice of 16-byte strings)
#   +48: env_ptr, +56: env_len
#   +64: workdir_ptr, +72: workdir_len
#   +80: stderr_ptr, +88: stderr_len
# Returns: (%rax = exit_code, %rdx = stderr_n, %rcx = err)
.global _fc_rt_exec_run
_fc_rt_exec_run:
    pushq   %rbp
    movq    %rsp, %rbp
    pushq   %rbx
    pushq   %r12
    pushq   %r13
    pushq   %r14
    pushq   %r15
    subq    $1056, %rsp       # argv[64] (512B), pipe fds (8B), wstatus (8B), bin cstr (512B)
    movq    %rdi, %r12        # args struct
    # Check ctx err
    movq    0(%r12), %rdi
    movq    8(%r12), %rsi
    call    _fc_rt_ctx_err
    testq   %rax, %rax
    jnz     .L_ex_ctx_err
    # Copy bin into NUL-terminated buffer at 528(%rsp)
    movq    24(%r12), %rcx
    cmpq    $500, %rcx
    ja      .L_ex_err
    movq    16(%r12), %rsi
    leaq    528(%rsp), %rdi
    cld
    rep movsb
    movb    $0, (%rdi)
    # Build argv array at (%rsp): argv[0] = bin, argv[1..n] = NUL-terminated copies
    leaq    528(%rsp), %rax
    movq    %rax, (%rsp)
    movq    40(%r12), %r13    # nargs
    cmpq    $62, %r13
    ja      .L_ex_err
    xorq    %rbx, %rbx
.L_ex_arg_loop:
    cmpq    %r13, %rbx
    jae     .L_ex_arg_done
    movq    32(%r12), %rax
    movq    %rbx, %rcx
    shlq    $4, %rcx
    movq    (%rax, %rcx), %r14
    movq    8(%rax, %rcx), %r15
    leaq    1(%r15), %rdi
    call    _fc_rt_bump_alloc
    movq    %rax, %rdi
    movq    %r14, %rsi
    movq    %r15, %rdx
    pushq   %rax
    call    _fc_rt_memcpy
    popq    %rax
    movb    $0, (%rax, %r15)
    movq    %rax, 8(%rsp, %rbx, 8)
    incq    %rbx
    jmp     .L_ex_arg_loop
.L_ex_arg_done:
    movq    $0, 8(%rsp, %r13, 8)
    # Create stderr pipe at 512(%rsp)
    movq    $SYS_pipe2, %rax
    leaq    512(%rsp), %rdi
    movq    $O_CLOEXEC, %rsi
    syscall
    testq   %rax, %rax
    jl      .L_ex_err
    # fork()
    movq    $SYS_fork, %rax
    syscall
    testq   %rax, %rax
    jl      .L_ex_fork_fail
    jnz     .L_ex_parent
    # --- CHILD ---
    movl    516(%rsp), %edi   # write end -> fd 2
    movq    $2, %rsi
    movq    $SYS_dup2, %rax
    syscall
    # chdir if workdir_len > 0
    movq    72(%r12), %rcx
    testq   %rcx, %rcx
    jz      .L_ex_do_exec
    leaq    1(%rcx), %rdi
    call    _fc_rt_bump_alloc
    movq    %rax, %rdi
    movq    64(%r12), %rsi
    movq    72(%r12), %rdx
    pushq   %rax
    call    _fc_rt_memcpy
    popq    %rdi
    movq    72(%r12), %rcx
    movb    $0, (%rdi, %rcx)
    movq    $SYS_chdir, %rax
    syscall
.L_ex_do_exec:
    movq    $SYS_execve, %rax
    leaq    528(%rsp), %rdi
    movq    %rsp, %rsi
    movq    _fc_rt_envp(%rip), %rdx
    syscall
    movq    $127, %rdi
    movq    $SYS_exit_group, %rax
    syscall
.L_ex_parent:
    movq    %rax, %r14        # child pid
    movl    516(%rsp), %edi   # close write end
    movq    $SYS_close, %rax
    syscall
    # Read child stderr into stderr_buf (80(%r12), 88(%r12))
    xorq    %r15, %r15        # captured = 0
.L_ex_drain:
    movq    88(%r12), %rdx
    subq    %r15, %rdx
    jz      .L_ex_drain_drop
    movq    $SYS_read, %rax
    movl    512(%rsp), %edi
    movq    80(%r12), %rsi
    addq    %r15, %rsi
    syscall
    cmpq    $-EINTR, %rax
    je      .L_ex_drain
    testq   %rax, %rax
    jle     .L_ex_wait
    addq    %rax, %r15
    jmp     .L_ex_drain
.L_ex_drain_drop:
    movq    $SYS_read, %rax
    movl    512(%rsp), %edi
    leaq    520(%rsp), %rsi
    movq    $8, %rdx
    syscall
    cmpq    $-EINTR, %rax
    je      .L_ex_drain_drop
    testq   %rax, %rax
    jg      .L_ex_drain_drop
.L_ex_wait:
    movl    512(%rsp), %edi
    movq    $SYS_close, %rax
    syscall
.L_ex_wait_retry:
    movq    $SYS_wait4, %rax
    movq    %r14, %rdi
    leaq    520(%rsp), %rsi
    xorq    %rdx, %rdx
    xorq    %r10, %r10
    syscall
    cmpq    $-EINTR, %rax
    je      .L_ex_wait_retry
    movl    520(%rsp), %eax
    movl    %eax, %edx
    andl    $0x7f, %edx
    jnz     .L_ex_signaled
    shrl    $8, %eax
    andl    $0xff, %eax
    movslq  %eax, %rax
    movq    %r15, %rdx
    xorq    %rcx, %rcx
    jmp     .L_ex_ret
.L_ex_signaled:
    movq    $-1, %rax
    movq    %r15, %rdx
    movq    $ERR_EXEC, %rcx
    jmp     .L_ex_ret
.L_ex_fork_fail:
    movl    512(%rsp), %edi
    movq    $SYS_close, %rax
    syscall
    movl    516(%rsp), %edi
    movq    $SYS_close, %rax
    syscall
.L_ex_err:
    movq    $-1, %rax
    xorq    %rdx, %rdx
    movq    $ERR_EXEC, %rcx
    jmp     .L_ex_ret
.L_ex_ctx_err:
    movq    %rax, %rcx
    movq    $-1, %rax
    xorq    %rdx, %rdx
.L_ex_ret:
    addq    $1056, %rsp
    popq    %r15
    popq    %r14
    popq    %r13
    popq    %r12
    popq    %rbx
    popq    %rbp
    ret

# ---------------------------------------------------------------------------
# Read-only data
.section .rodata
.L_err_prefix:    .ascii "error: "
.L_nl:            .ascii "\n"
.L_panic_prefix:  .ascii "panic: "
.L_panic_bounds:  .ascii "panic: index/slice out of range\n"
.L_panic_div0:    .ascii "panic: integer divide by zero\n"
.L_panic_arena:   .ascii "panic: runtime str arena full\n"

.L_err_msg_0:     .ascii "nil"
.L_err_msg_1:     .ascii "fincore: EOF"
.L_err_msg_2:     .ascii "fincore: timeout"
.L_err_msg_3:     .ascii "fincore: cancelled"
.L_err_msg_4:     .ascii "fincore: zero write"
.L_err_msg_5:     .ascii "fincore: closed"
.L_err_msg_6:     .ascii "fincore: I/O error"
.L_err_msg_7:     .ascii "fincore: not found"
.L_err_msg_8:     .ascii "fincore: max depth exceeded"
.L_err_msg_9:     .ascii "fincore: arena full"
.L_err_msg_10:    .ascii "fincore: exec failed"
.L_err_msg_11:    .ascii "fincore: invalid number"
.L_err_msg_unknown: .ascii "fincore: error?"

.balign 8
.L_err_table:
    .quad .L_err_msg_0,  3
    .quad .L_err_msg_1,  12
    .quad .L_err_msg_2,  16
    .quad .L_err_msg_3,  18
    .quad .L_err_msg_4,  19
    .quad .L_err_msg_5,  15
    .quad .L_err_msg_6,  18
    .quad .L_err_msg_7,  18
    .quad .L_err_msg_8,  27
    .quad .L_err_msg_9,  19
    .quad .L_err_msg_10, 20
    .quad .L_err_msg_11, 23

# ---------------------------------------------------------------------------
# Zero-initialized static runtime storage (.bss)
.section .bss
.balign 8
.global _fc_rt_argc
_fc_rt_argc:       .skip 8
.global _fc_rt_argv
_fc_rt_argv:       .skip 8
.global _fc_rt_envp
_fc_rt_envp:       .skip 8
_fc_tmp_seq:       .skip 8
.global _fc_rt_stderr_written
_fc_rt_stderr_written: .skip 8
.global _fc_fiber_runtime_active
_fc_fiber_runtime_active: .skip 8
_fc_term_saved:    .skip 8
_fc_term_orig:     .skip 64
_fc_str_arena_pos: .skip 8
.balign 16
.global _fc_main_tls
_fc_main_tls:      .skip 64
_fc_str_arena:     .skip STR_ARENA_CAP
